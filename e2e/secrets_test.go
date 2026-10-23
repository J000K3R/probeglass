// Package e2e — generating a credential in the sensor form.
//
// A community or a v3 passphrase is a value the person invents and then types into
// the device as well. The Generate button makes one, shows it, and copies it; what
// only a browser can show is that the button belongs to the field it is beside and
// that the value it writes is the one the form will save.
package e2e

import (
	"context"
	"regexp"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

var generatedSecret = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestSNMPSecretsCanBeGenerated(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-type-open]`),
			chromedp.Click(`#modal [data-type-open]`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#modal-catalog [data-sensor-type="snmp"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			chromedp.Click(`#modal-catalog [data-sensor-type="snmp"]`),
			chromedp.WaitVisible(`#modal [name="opt_oid"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "snmp-form")
		t.Fatalf("could not open the SNMP form: %v", err)
	}

	generate := func(key string) string {
		t.Helper()
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(() => {
			const button = document.querySelector('#modal [data-option-key="`+key+`"] [data-generate-secret]');
			if (!button) return false;
			button.click();
			return true;
		})()`)); err != nil {
			t.Fatalf("could not press Generate for %s: %v", key, err)
		}
		return read[string](t, ctx,
			`document.querySelector('#modal [name="opt_`+key+`"]')?.value || ''`,
			"the generated "+key)
	}

	// The default version is v2c, so the community field is the one on screen.
	community := generate("community")
	if !generatedSecret.MatchString(community) {
		t.Errorf("the generated community is %q, want 32 hex characters", community)
	}
	if typ := read[string](t, ctx,
		`document.querySelector('#modal [name="opt_community"]')?.type || ''`,
		"the community field's type"); typ != "text" {
		t.Errorf("the generated community is still a password field (%q); it has to be readable while it is stored on the device", typ)
	}

	// v3 shows the user, the level and the two passphrases. authPriv is the
	// default level, so both passphrases are asked for.
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[bool](`(() => {
		const radio = document.querySelector('#modal [name="opt_version"][value="3"]');
		if (!radio) return false;
		radio.closest('label').click();
		return true;
	})()`)); err != nil {
		t.Fatalf("could not choose SNMP v3: %v", err)
	}
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.WaitVisible(`#modal [name="opt_auth_pass"]`),
			chromedp.WaitVisible(`#modal [name="opt_priv_pass"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "snmp-v3-fields")
		t.Fatalf("the v3 passphrase fields did not appear: %v", err)
	}

	auth := generate("auth_pass")
	priv := generate("priv_pass")
	for name, value := range map[string]string{"auth_pass": auth, "priv_pass": priv} {
		if !generatedSecret.MatchString(value) {
			t.Errorf("the generated %s is %q, want 32 hex characters", name, value)
		}
	}
	if auth == priv {
		t.Errorf("auth_pass and priv_pass were generated as the same value %q; two passphrases have to differ", auth)
	}
}
