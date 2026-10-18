package e2e

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The online icon search, against the real dashboardicons.com.
//
// It is opt-in — PROBEGLASS_E2E_ONLINE=1 — and not part of what CI runs, because a
// test that fails when somebody else's website is down teaches the wrong lesson.
// What it is for is the one thing a unit test cannot say: that the route, the
// preview, the import and the drawing of the result are wired to each other in a
// browser. Run it by hand when the online catalogue changes shape.
//
//	PROBEGLASS_E2E_ONLINE=1 go test ./... -run Online -v
func TestTheOnlineIconSearchFindsAndImports(t *testing.T) {
	if os.Getenv("PROBEGLASS_E2E_ONLINE") != "1" {
		t.Skip("online: set PROBEGLASS_E2E_ONLINE=1 to search dashboardicons.com")
	}
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-icon-open]`),
			chromedp.Click(`[data-icon-open]`),
			chromedp.WaitVisible(`.icon-panel [data-icon-query]`),
			chromedp.SendKeys(`[data-icon-query]`, "pi-hole"),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('[data-icon-results] [data-icon-import]')`,
				chromedp.WithPollingTimeout(30*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "icon-online-search")
		t.Fatalf("the online search did not answer: %v\nit said: %s", err, onlineNote(ctx))
	}
	capture(t, ctx, dir, "icon-online-search")

	// The previews are ordinary lazy images fetched from this server, so wait for
	// one to finish before asking whether it arrived.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Poll[chromedp.Void](`(() => {
		  const img = document.querySelector('[data-icon-results] img');
		  return !!img && img.complete && img.naturalWidth > 0;
		})()`, chromedp.WithPollingTimeout(20*time.Second)))
	}); err != nil {
		capture(t, ctx, dir, "icon-online-preview")
		t.Fatalf("the picture beside a result never loaded: %v", err)
	}
	capture(t, ctx, dir, "icon-online-preview")

	// The result list names what it found, and the picture beside the name is this
	// server's preview route rather than the CDN — the content policy allows no
	// other origin.
	result := read[map[string]string](t, ctx, `(() => {
	  const tile = document.querySelector('[data-icon-import="pi-hole"]')
	    || document.querySelector('[data-icon-results] [data-icon-import]');
	  if (!tile) return { found: 'no' };
	  const img = tile.querySelector('img');
	  return {
	    found: 'yes',
	    name: tile.dataset.iconImport || '',
	    src: img ? img.getAttribute('src') : '',
	    loaded: img ? String(img.complete && img.naturalWidth > 0) : 'no-img',
	  };
	})()`, "the online result")
	if result["found"] != "yes" {
		t.Fatal("the search answer was shown as empty")
	}
	if !strings.HasPrefix(result["src"], "/api/icons/preview/") {
		t.Errorf("the preview is drawn from %q, want this server's preview route", result["src"])
	}
	if result["loaded"] != "true" {
		t.Errorf("the preview did not load (src %q); the picture is probably not being served", result["src"])
	}

	// Choosing it stores the picture and selects it, exactly as an upload does.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(fmt.Sprintf(`[data-icon-import=%q]`, result["name"])),
			chromedp.WaitNotPresent(`.icon-panel [data-icon-results]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "icon-online-import")
		t.Fatalf("choosing an online icon did not close the picker: %v", err)
	}
	if got := read[string](t, ctx, pickerValue, "what the picker holds after the import"); !strings.HasPrefix(got, "upload:") {
		t.Fatalf("the picker holds %q after importing; an online icon is stored like an upload", got)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, newSensor("Online symbol", "http://10.0.0.9/health"))
	}); err != nil {
		capture(t, ctx, dir, "icon-online-save")
		t.Fatalf("the sensor did not save: %v", err)
	}
	card := read[iconCard](t, ctx, fmt.Sprintf(cardIcon, "Online symbol"), "the card of the new sensor")
	if !card.Found {
		t.Fatal("the sensor has no card on the dashboard")
	}
	if !strings.HasPrefix(card.Image, "/api/icons/") {
		t.Errorf("the card draws %q, want the imported picture from /api/icons/", card.Image)
	}
	if card.Drawn != "" {
		t.Errorf("the imported icon is also drawn as a themed drawing (%q)", card.Drawn)
	}
	capture(t, ctx, dir, "icon-online-card")
}

// onlineNote is what the search field says, so that a failure on a machine with no
// route out reads as that rather than as a broken test.
func onlineNote(ctx context.Context) string {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`document.querySelector('[data-icon-note]')?.textContent.trim() || '(nothing)'`))
	if err != nil {
		return "(could not read it)"
	}
	return value
}
