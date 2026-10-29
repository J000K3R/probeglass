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
	Background *struct {
		MediaType string `json:"media_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
		Size      int    `json:"size"`
		Digest    string `json:"digest"`
	} `json:"background_upload"`
	Limits *struct {
		MaxBytes int `json:"max_bytes"`
		MaxSide  int `json:"max_side"`
	} `json:"background_limits"`
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
	// the first sensor, and an uploaded photograph. This is the whole editor, one
	// field at a time.
	bgFile := writeTestPNG(t, screenshotDir)
	var chosen sensorLite
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Clear(`#status-title`),
			chromedp.SendKeys(`#status-title`, "Homelab"),
			chromedp.Click(`#status-panel [data-status-theme][value="dark"]`),
			chromedp.Click(`#status-panel [data-status-accent][value="violet"]`),
			chromedp.Click(`#status-panel [data-status-layout][value="cards"]`),
			chromedp.Click(`#status-panel [data-status-add-notice]`),
			chromedp.SendKeys(`[data-notice-text="0"]`, "Maintenance tonight"),
			chromedp.Click(`#status-panel [data-notice-icon="0"][data-icon-ref="builtin:warning"]`),
			chromedp.Click(`#status-panel [data-notice-colour="0"][value="amber"]`),
			chromedp.Clear(`#status-background`),
			chromedp.SendKeys(`#status-background`, "https://assets.example.net/bg.jpg"),
			chromedp.Click(`#status-panel [data-status-dim][value="60"]`),
			// A logo from the icon picker, the same control the rest of the
			// dashboard uses. Clicked from script: the panel sits low enough on this
			// page that a coordinate click can land under the fixed top bar.
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel .status-logo-field [data-icon-open]').click()`),
			chromedp.Poll[chromedp.Void](`!!document.querySelector('#status-panel .status-logo-field [data-icon-set="builtin:server"]')`,
				chromedp.WithPollingTimeout(10*time.Second)),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel .status-logo-field [data-icon-set="builtin:server"]').click()`),
			chromedp.Poll[chromedp.Void](`document.querySelector('input[name="status_logo"]').value === 'builtin:server'`,
				chromedp.WithPollingTimeout(5*time.Second)),
			// The photograph: the file input uploads at once, before the page is
			// saved, because the image is not part of the JSON configuration.
			chromedp.SetUploadFiles(`[data-status-bg-upload]`, []string{bgFile}),
			chromedp.Poll[chromedp.Void](`fetch('/api/status-page').then(r => r.json())
				.then(s => s.background_upload !== null)`, chromedp.WithPollingTimeout(20*time.Second)),
			// The rest is clicked from script for the same reason as the logo: a
			// control under the fixed top bar is still a control, and this test is
			// about the editor's decisions, not about hit-testing.
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel [data-status-add-section]').click()`),
			chromedp.SendKeys(`[data-section-title="0"]`, "Web"),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('[data-section-sensors="0"]').click()`),
			chromedp.WaitVisible(`#modal-status-sensors[open]`),
			// A new section starts with nothing ticked.
			chromedp.Poll[chromedp.Void](`document.querySelectorAll('#status-sensors-pick input[data-assign-sensor]:checked').length === 0`,
				chromedp.WithPollingTimeout(5*time.Second)),
			// One click ticks the whole group, and one clears it again.
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-sensors-pick input[data-assign-group="0"]').click()`),
			chromedp.Poll[chromedp.Void](`document.querySelectorAll('#status-sensors-pick input[data-assign-sensor]:checked').length > 0`,
				chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#modal-status-sensors [data-assign-all="0"]').click()`),
			chromedp.Poll[chromedp.Void](`document.querySelectorAll('#status-sensors-pick input[data-assign-sensor]:checked').length === 0`,
				chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-sensors-pick input[data-assign-sensor]').click()`),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-sensors-apply').click()`),
			chromedp.WaitNotPresent(`#modal-status-sensors[open]`),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel [data-action="status-save"]').click()`),
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
	if saved.Background == nil || saved.Background.Width != 32 || saved.Background.Digest == "" {
		t.Fatalf("the uploaded background did not come back: %+v", saved.Background)
	}
	if saved.Limits == nil || saved.Limits.MaxSide != 4096 {
		t.Errorf("the panel does not know the background limits: %+v", saved.Limits)
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
	versioned := "/status/" + saved.Token + "/background?v=" + saved.Background.Digest
	for _, want := range []string{`data-theme="dark"`, `data-accent="violet"`, "layout-cards", "Homelab", ">Web<", "card-top",
		"Maintenance tonight", "notice c-amber", "/assets/icons/warning.svg",
		versioned, "--bg-dim:60", "/assets/icons/server.svg",
		"site-brand", "/#/settings/monitor/status"} {
		if !strings.Contains(html, want) {
			t.Errorf("the configured page does not carry %q", want)
		}
	}
	// The upload wins over the URL that is still saved as its fallback.
	if strings.Contains(html, "https://assets.example.net/bg.jpg") {
		t.Error("the page drew the URL although an image was uploaded")
	}

	// The bytes, the ETag, and a conditional request that answers 304.
	bgURL := "/status/" + saved.Token + "/background"
	info := readAsync[map[string]any](t, ctx, `fetch(`+jsQuote(bgURL)+`).then(r => ({
		status: r.status, type: r.headers.get('content-type'), etag: r.headers.get('etag'),
	}))`, "read the background headers")
	if info["status"].(float64) != 200 || info["type"] != "image/png" {
		t.Errorf("the background answered %v %v", info["status"], info["type"])
	}
	wantETag := `"bg-` + saved.Background.Digest + `"`
	if info["etag"] != wantETag {
		t.Errorf("the background ETag = %v, want %s", info["etag"], wantETag)
	}
	conditional := readAsync[int](t, ctx, `fetch(`+jsQuote(bgURL)+`, {headers: {'If-None-Match': `+jsQuote(wantETag)+`}}).then(r => r.status)`,
		"ask the background again")
	if conditional != 304 {
		t.Errorf("a conditional background request answered %d, want 304", conditional)
	}

	// A German browser gets the German words; the page is for the people the link
	// was sent to, and they did not choose a language in this installation.
	german := readAsync[string](t, ctx,
		`fetch(`+jsQuote(publicURL)+`, {headers: {'Accept-Language': 'de'}}).then(r => r.text())`,
		"read the public status page in German")
	if !strings.Contains(german, "Aktualisiert") {
		t.Error("a German browser did not get the German page")
	}

	// Removing the upload falls back to the configured URL, and the old image is
	// gone from the public route.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`window.confirm = () => true`),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel [data-action="status-background-remove"]').click()`),
			chromedp.Poll[chromedp.Void](`fetch('/api/status-page').then(r => r.json())
				.then(s => s.background_upload === null)`, chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "status-background-not-removed")
		t.Fatalf("the background could not be removed: %v", err)
	}
	afterRemove := readAsync[string](t, ctx, `fetch(`+jsQuote(publicURL)+`).then(r => r.text())`,
		"read the page after removing the background")
	if !strings.Contains(afterRemove, "https://assets.example.net/bg.jpg") {
		t.Error("the URL did not come back after the upload was removed")
	}
	// Asked past the browser's cache: the image was fetched a moment ago with a
	// day of freshness, and this asks the server rather than the cache.
	if gone := readAsync[int](t, ctx, `fetch(`+jsQuote(bgURL)+`, {cache: 'no-store'}).then(r => r.status)`,
		"ask the removed background"); gone != 404 {
		t.Errorf("the removed background answered %d, want 404", gone)
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

	// "Use my groups" turns the dashboard groups into sections, taking their
	// icons and colours with them. This replaces the one hand-made section; the
	// change is only in the editor, so the saved page is untouched.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`window.confirm = () => true`),
			chromedp.Evaluate[chromedp.Void](`document.querySelector('#status-panel [data-status-from-groups]').click()`),
			chromedp.Poll[chromedp.Void](`(() => {
				const title = document.querySelector('[data-section-title="0"]');
				return title && title.value.length > 0;
			})()`, chromedp.WithPollingTimeout(10*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "status-from-groups")
		t.Fatalf("the groups were not turned into sections: %v", err)
	}
	if got := read[string](t, ctx, `document.querySelector('[data-section-title="0"]').value`, "the first section title"); got != "Examples" {
		t.Errorf("the first section is %q, want the example group", got)
	}
	if got := read[string](t, ctx, `document.querySelector('[data-section-sensors="0"]').textContent`, "the section's sensor count"); !strings.Contains(got, "4") {
		t.Errorf("the group's section says %q, want four sensors", got)
	}

	// The edit link the page offers its administrator opens the Status panel in
	// the Monitoring category, not the settings page at large.
	if _, err := chromedp.Run(ctx, chromedp.Navigate(base+"/#/settings/monitor/status")); err != nil {
		t.Fatalf("open the edit link: %v", err)
	}
	eventually(t, ctx, `(() => {
		const panel = document.querySelector('#status-panel');
		const tab = document.querySelector('.settings-tabs [data-category="monitor"]');
		return panel && !panel.hidden && tab && tab.getAttribute('aria-selected') === 'true';
	})()`, "the edit link does not open the status panel", screenshotDir, "status-deep-link", 10*time.Second)
}
