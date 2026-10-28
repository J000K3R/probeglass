package e2e

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The certificate inventory is a page about objects rather than about probes, so
// the thing worth checking is that the objects get there at all: a certificate a
// sensor met, with the name it belongs to, in the order somebody acts on them.
//
// The example data is the source. Its two public TLS sensors carry certificates
// written by demo.Seed, which means this test neither needs the internet nor has
// to wait for a probe — the page is complete the moment the data is.

// certRows is the table as a person reads it.
const certRowsScript = `(() => [...document.querySelectorAll('#cert-list .certs-table tbody tr')].map(tr => {
  const cells = tr.querySelectorAll('td');
  return {
    sensor: ((cells[0] || {}).querySelector('b') || {}).textContent?.trim() || '',
    subject: ((cells[1] || {}).querySelector('b') || {}).textContent?.trim() || '',
    issuer: (cells[2] || {}).textContent?.trim() || '',
    days: (cells[4] || {}).textContent?.trim() || '',
    expired: tr.classList.contains('is-expired'),
  };
}))()`

// restoreExamples asks for the demo dataset the way the Settings button does,
// but without walking the panel: this test is about the page that data fills.
const restoreExamples = `(async () => {
  const res = await fetch('/api/examples', {method: 'POST'});
  const data = await res.json();
  window.__examplesRestored = data.sensors > 0;
})()`

// certificatesAPI is /api/certificates as the page sees it.
const certificatesAPI = `fetch('/api/certificates').then(r => r.json())`

type certRow struct {
	Sensor  string `json:"sensor"`
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	Days    string `json:"days"`
	Expired bool   `json:"expired"`
}

type certificateAPI struct {
	Certificates []struct {
		SensorName string    `json:"sensor_name"`
		Subject    string    `json:"subject"`
		NotAfter   time.Time `json:"not_after"`
		DaysLeft   int       `json:"days_left"`
	} `json:"certificates"`
}

func TestTheCertificatePageListsWhatTheProbesMet(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	//nolint:misspell // the spelling GitHub uses for the uploaded files
	screenshotDir := os.Getenv("PROBEGLASS_E2E_ARTIFACTS")
	if screenshotDir == "" {
		screenshotDir = t.TempDir()
	}

	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(base+"/"),
			chromedp.WaitVisible(`#auth-password`),
			chromedp.SendKeys(`#auth-username`, testUser),
			chromedp.SendKeys(`#auth-password`, testPassword),
			chromedp.SendKeys(`#auth-confirm`, testPassword),
			chromedp.Click(`#auth-submit`),
			chromedp.WaitVisible(`#auth-mode-step`),
			chromedp.Click(`input[name="ui-mode"][value="advanced"]`),
			chromedp.Click(`#auth-submit`),
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`.tour-card [data-tour="skip"]`),
			chromedp.WaitNotPresent(`.tour-card`),
			// The dataset, through the same endpoint the Settings button uses.
			chromedp.Evaluate[chromedp.Void](restoreExamples),
			chromedp.Poll[chromedp.Void](
				`window.__examplesRestored === true && document.querySelectorAll('#groups .card').length > 0`,
				chromedp.WithPollingTimeout(30*time.Second)),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/certificates'`),
			chromedp.Poll[chromedp.Void](
				`document.querySelectorAll('#cert-list .certs-table tbody tr').length >= 2`,
				chromedp.WithPollingTimeout(20*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "certificates-not-listed")
		t.Fatalf("the certificate page did not fill itself from the example data: %v", err)
	}

	rows := read[[]certRow](t, ctx, certRowsScript, "read the certificate rows")
	var tls, dns *certRow
	for i := range rows {
		switch rows[i].Sensor {
		case "Example website TLS":
			tls = &rows[i]
		case "DNS over TLS":
			dns = &rows[i]
		}
	}
	if tls == nil || dns == nil {
		t.Fatalf("the page does not list both example TLS sensors: %+v", rows)
	}
	if tls.Subject != "example.org" {
		t.Errorf("the example.org row names the certificate %q", tls.Subject)
	}
	if !strings.Contains(dns.Issuer, "Google Trust Services") {
		t.Errorf("the DNS row names the issuer %q, want the organisation on the certificate", dns.Issuer)
	}
	if tls.Expired || dns.Expired {
		t.Errorf("a demo certificate is drawn as expired: %+v", rows)
	}

	// Sorted by expiry, which is the claim the page is for. Asked of the API
	// rather than of the rendered day counts, because those are words.
	api := readAsync[certificateAPI](t, ctx, certificatesAPI, "read the certificate API")
	if len(api.Certificates) < 2 {
		t.Fatalf("the API returned %d certificate(s)", len(api.Certificates))
	}
	for i := 1; i < len(api.Certificates); i++ {
		prev, this := api.Certificates[i-1], api.Certificates[i]
		if this.NotAfter.Before(prev.NotAfter) {
			t.Errorf("the inventory is out of order: %s (%s) comes after %s (%s)",
				this.SensorName, this.NotAfter, prev.SensorName, prev.NotAfter)
		}
	}

	// The filter is the count's other half: the buckets must not hide a valid
	// certificate, and a bucket with nothing in it has to say so rather than
	// leave the table blank.
	valid := 0
	expired := 0
	for _, c := range api.Certificates {
		if c.DaysLeft < 0 {
			expired++
		} else {
			valid++
		}
	}
	if _, err := chromedp.Run(ctx, chromedp.Click(`#cert-filters [data-cert-filter="valid"]`)); err != nil {
		t.Fatalf("click the valid filter: %v", err)
	}
	eventually(t, ctx,
		`document.querySelectorAll('#cert-list .certs-table tbody tr').length === `+strconv.Itoa(valid),
		"the valid filter does not show every valid certificate", screenshotDir, "certificates-valid-filter", 5*time.Second)

	if expired == 0 {
		if _, err := chromedp.Run(ctx, chromedp.Click(`#cert-filters [data-cert-filter="expired"]`)); err != nil {
			t.Fatalf("click the expired filter: %v", err)
		}
		eventually(t, ctx, `(() => {
			const empty = document.querySelector('#certs-empty-filtered');
			return empty && !empty.hidden &&
				document.querySelectorAll('#cert-list .certs-table tbody tr').length === 0;
		})()`,
			"an empty filter left the table blank without saying why", screenshotDir, "certificates-empty-filter", 5*time.Second)
	}

	// A row is a door to the sensor, like every other row in this dashboard.
	//
	// The page is reloaded first, because this is the cold load that used to lose
	// the click: the tab fetches its own list, so its rows can be on screen before
	// the summary that carries the sensors has arrived. The handler queues the
	// request until that summary lands, and the reload is what puts the two in the
	// order that found the bug.
	if _, err := chromedp.Run(ctx, chromedp.Reload()); err != nil {
		t.Fatalf("reload the certificate page: %v", err)
	}
	eventually(t, ctx, `document.querySelectorAll('#cert-list .certs-table tbody tr').length >= 2`,
		"the certificate page did not come back after the reload", screenshotDir, "certificates-reloaded", 10*time.Second)
	// The click is a real one at the row's coordinates, not a scripted .click():
	// what this checks is that the row is a target a person can hit.
	if _, err := chromedp.Run(ctx, chromedp.Click(`#cert-list .certs-table tbody tr:first-child`)); err != nil {
		t.Fatalf("click the first certificate row: %v", err)
	}
	eventually(t, ctx, `(() => {
		const sheet = document.querySelector('#detail');
		return sheet && sheet.open && document.querySelector('#detail-title').textContent.trim() !== '';
	})()`,
		"a certificate row does not open its sensor", screenshotDir, "certificates-row-detail", 10*time.Second)
}
