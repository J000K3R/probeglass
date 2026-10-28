package e2e

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// The public status page has two halves that meet in one place: the panel that
// chooses what is published, and the page an outsider reads. This walks from one
// to the other with the browser, which is the only way to check that the link the
// panel shows is the link the server answers.

type statusPanelState struct {
	Enabled bool   `json:"enabled"`
	Token   string `json:"token"`
	Config  struct {
		Title    string `json:"title"`
		Theme    string `json:"theme"`
		Accent   string `json:"accent"`
		Layout   string `json:"layout"`
		Sections []struct {
			Title   string `json:"title"`
			Sensors []struct {
				ID int64 `json:"id"`
			} `json:"sensors"`
		} `json:"sections"`
	} `json:"config"`
}

type sensorLite struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Target string `json:"target"`
}

func TestTheStatusPagePanelAndThePageItShares(t *testing.T) {
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
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#status-panel [data-action="status-enable"]`),
			chromedp.Click(`#status-panel [data-action="status-enable"]`),
			chromedp.Poll[chromedp.Void](`(() => {
				const url = document.querySelector('#status-url');
				return url && /\/status\/[A-Za-z0-9_-]+$/.test(url.value);
			})()`, chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "status-page-not-enabled")
		t.Fatalf("the status page link was not generated: %v", err)
	}

	// Build the page: a title, dark theme, violet accent, cards, one section with
	// the first sensor. This is the whole editor, one field at a time.
	var chosen sensorLite
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Clear(`#status-title`),
			chromedp.SendKeys(`#status-title`, "Homelab"),
			chromedp.Click(`#status-panel [data-status-theme][value="dark"]`),
			chromedp.Click(`#status-panel [data-status-accent][value="violet"]`),
			chromedp.Click(`#status-panel [data-status-layout][value="cards"]`),
			chromedp.Click(`#status-panel [data-status-add-section]`),
			chromedp.SendKeys(`[data-section-title="0"]`, "Web"),
			chromedp.Click(`[data-section-sensors="0"]`),
			chromedp.WaitVisible(`#modal-status-sensors[open]`),
			chromedp.Click(`#status-sensors-pick input[data-assign-sensor]`),
			chromedp.Click(`#status-sensors-apply`),
			chromedp.WaitNotPresent(`#modal-status-sensors[open]`),
			chromedp.Click(`#status-panel [data-action="status-save"]`),
			// Asked of the server rather than of the panel: the panel is rebuilt
			// after a save, and a poll on the old element cannot see that.
			chromedp.Poll[chromedp.Void](`fetch('/api/status-page').then(r => r.json())
				.then(s => s.config.sections.length === 1 && s.config.sections[0].sensors.length === 1)`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "status-page-not-saved")
		t.Fatalf("the page was not saved: %v", err)
	}

	saved := readAsync[statusPanelState](t, ctx, `fetch('/api/status-page').then(r => r.json())`,
		"read the status page state")
	if !saved.Enabled || len(saved.Config.Sections) != 1 || len(saved.Config.Sections[0].Sensors) != 1 {
		t.Fatalf("after saving: %+v", saved)
	}
	id := saved.Config.Sections[0].Sensors[0].ID
	if saved.Config.Title != "Homelab" || saved.Config.Theme != "dark" ||
		saved.Config.Accent != "violet" || saved.Config.Layout != "cards" ||
		saved.Config.Sections[0].Title != "Web" {
		t.Errorf("the page came back as %+v", saved.Config)
	}
	sensors := readAsync[[]sensorLite](t, ctx, `fetch('/api/sensors').then(r => r.json())`,
		"read the sensors")
	for _, s := range sensors {
		if s.ID == id {
			chosen = s
		}
	}
	if chosen.Name == "" {
		t.Fatalf("the saved sensor %d is not in the list", id)
	}

	// The page itself, fetched from the settings tab with no extra header. The
	// token is the only credential it needs.
	publicURL := `/status/` + saved.Token
	html := readAsync[string](t, ctx, `fetch(`+jsQuote(publicURL)+`).then(r => r.text())`,
		"read the public status page")
	if !strings.Contains(html, chosen.Name) {
		t.Errorf("the public page does not name the published sensor %q", chosen.Name)
	}
	if chosen.Target != "" && strings.Contains(html, chosen.Target) {
		t.Errorf("the public page leaks the sensor target %q", chosen.Target)
	}
	if want := "noindex, nofollow"; !strings.Contains(html, want) {
		t.Errorf("the public page does not carry %q", want)
	}
	for _, want := range []string{`data-theme="dark"`, `data-accent="violet"`, "layout-cards", "Homelab", ">Web<", "card-top"} {
		if !strings.Contains(html, want) {
			t.Errorf("the configured page does not carry %q", want)
		}
	}

	// A German browser gets the German words; the page is for the people the link
	// was sent to, and they did not choose a language in this installation.
	german := readAsync[string](t, ctx,
		`fetch(`+jsQuote(publicURL)+`, {headers: {'Accept-Language': 'de'}}).then(r => r.text())`,
		"read the public status page in German")
	if !strings.Contains(german, "Aktualisiert") {
		t.Error("a German browser did not get the German page")
	}

	// Switching the link off makes it answer 404 immediately.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`window.confirm = () => true`),
			chromedp.Click(`#status-panel [data-action="status-disable"]`),
			chromedp.Poll[chromedp.Void](`document.querySelector('[data-action="status-enable"]') !== null`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "status-page-not-disabled")
		t.Fatalf("the status page could not be switched off: %v", err)
	}
	code := readAsync[int](t, ctx, `fetch(`+jsQuote(publicURL)+`).then(r => r.status)`,
		"ask the disabled page")
	if code != 404 {
		t.Errorf("the disabled link answered %d, want 404", code)
	}
}
