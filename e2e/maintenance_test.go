// Package e2e — maintenance windows in the dashboard.
//
// The API tests prove the endpoints; the store tests prove the arithmetic; the
// scheduler tests prove the silence. What none of them can say is whether the form in
// the browser builds the body the server will accept — and the two shapes of a window
// (one-off and weekly) mean the form has two halves, only one of which is on screen at
// a time. That is the kind of thing that works until somebody looks at it.
//
// Run it with:
//
//	cd e2e && go test ./... -run 'Maintenance' -v
package e2e

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// windowRow is one row of the maintenance table as a reader sees it.
type windowRow struct {
	What  string `json:"what"`
	When  string `json:"when"`
	Note  string `json:"note"`
	State string `json:"state"`
}

const readWindowRows = `(() => {
  return [...document.querySelectorAll('#windows-panel .windows-table tbody tr')].map((tr) => {
    const td = [...tr.querySelectorAll('td')];
    return {
      what: td[0] ? td[0].textContent.trim() : '',
      when: td[1] ? td[1].textContent.trim() : '',
      note: td[2] ? td[2].textContent.trim() : '',
      state: td[3] ? td[3].textContent.trim() : '',
    };
  });
})()`

// openTheWindowEditor clicks New window and waits for the dialog.
func openTheWindowEditor() chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.WaitVisible(`#windows-panel [data-action="window-new"]`),
		chromedp.Click(`#windows-panel [data-action="window-new"]`),
		chromedp.WaitVisible(`#window-form [name="window_scope"]`),
	)
}

// TestTheMaintenancePanelCreatesAWindow walks the whole path once: the panel is on the
// settings page, the form builds a window the server accepts, and what was saved is
// what the table then shows.
//
// A weekly window, because it is the shape with the most fields and the one whose
// values have to survive a round trip through minutes-past-midnight, a weekday list and
// a time zone — three things that are easy to send in a form the server refuses.
func TestTheMaintenancePanelCreatesAWindow(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("monitor"),
		)
	}); err != nil {
		capture(t, ctx, dir, "windows-panel")
		t.Fatalf("could not reach the monitoring settings: %v", err)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.WaitVisible(`#windows-panel`))
	}); err != nil {
		capture(t, ctx, dir, "windows-panel-missing")
		t.Fatalf("the settings page has no maintenance panel: %v", err)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, openTheWindowEditor())
	}); err != nil {
		capture(t, ctx, dir, "windows-editor")
		t.Fatalf("the New window button did not open an editor: %v", err)
	}

	// The weekly half is hidden until the kind select says otherwise. A form that
	// showed both halves at once would ask for fields that do not apply and refuse
	// the answer, which is exactly the sort of thing this test exists to catch.
	visible := read[map[string]bool](t, ctx, `(() => ({
	  once: !document.querySelector('#window-body [data-kind="once"]').hidden,
	  weekly: !document.querySelector('#window-body [data-kind="weekly"]').hidden,
	}))()`, "which halves of the form are shown")
	if visible["weekly"] || !visible["once"] {
		t.Errorf("a new window opens showing once=%v weekly=%v, want once only",
			visible["once"], visible["weekly"])
	}

	// Fill it in the way a person would: a weekly window on two days, from three to
	// half past four in the morning.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#window-form [name="window_kind"]`, "weekly"),
			chromedp.Poll[chromedp.Void](
				`!document.querySelector('#window-body [data-kind="weekly"]').hidden`,
				chromedp.WithPollingTimeout(5*time.Second)),
			chromedp.SetValue(`#window-form [name="window_start"]`, "03:00"),
			chromedp.SetValue(`#window-form [name="window_end"]`, "04:30"),
			chromedp.Click(`#window-form [name="window_day_0"]`),
			chromedp.Click(`#window-form [name="window_day_3"]`),
			chromedp.SetValue(`#window-form [name="window_note"]`, "Nightly snapshot"),
			// The save button, and not chromedp.Submit. Submit calls the form's own
			// submit() method, which is specified to fire no `submit` event — so the
			// listener that reads the fields and posts them never runs, and a
			// `method="dialog"` form simply closes. The test then reads an empty table
			// and the reason is nowhere on screen. Every other dialog test in this
			// suite clicks the button for the same reason.
			chromedp.Click(`#window-save`),
			chromedp.WaitNotPresent(`#modal-window[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "windows-submit")
		t.Fatalf("the window form did not save: %v\nIt said: %s", err, windowError(ctx))
	}

	rows := read[[]windowRow](t, ctx, readWindowRows, "the maintenance table")
	if len(rows) != 1 {
		capture(t, ctx, dir, "windows-rows")
		t.Fatalf("the table shows %d windows, want the one just saved: %+v", len(rows), rows)
	}
	if rows[0].Note != "Nightly snapshot" {
		t.Errorf("the note came back as %q", rows[0].Note)
	}
	if rows[0].What == "" {
		t.Errorf("the row does not name what the window is for: %+v", rows[0])
	}
	// The two days and the time have to have survived the trip. Checked loosely — the
	// names come from Intl and are the browser's business — but the times are ours.
	if got := rows[0].When; len(got) < 11 {
		t.Errorf("the row says the window is %q; it should name the days and the hours", got)
	}
}

// windowError reads the message the dialog is showing, so that a failure says what the
// server refused rather than only that something went wrong.
func windowError(ctx context.Context) string {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`document.querySelector('#window-error')?.textContent.trim() || '(nothing)'`, nil))
	if err != nil {
		return "(could not read it)"
	}
	return value
}

// TestASensorUnderAWindowSaysSoOnItsCard is the answer to a report from somebody who
// used the feature: they made a maintenance window and everything stayed green.
//
// It was true, and it was a gap rather than a bug in the arithmetic. The API published
// in_maintenance on every status view from the moment the feature landed, and the
// dashboard used it in exactly one place — the "running now" mark in the windows list —
// and nowhere on a sensor. So a window did what it said it did and nothing on the
// screen admitted it: the one thing a person needs in order to decide whether to get up
// was the one thing not drawn.
//
// The status stays what the probe found, so this is about the second pill and the ring
// around the card rather than about a repainted status. A card that went purple while
// the box was genuinely unreachable would be lying about the one thing a card is for.
func TestASensorUnderAWindowSaysSoOnItsCard(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	const sensorName = "Zonk Switch"
	createSensorThroughTheAPI(t, ctx, dir, sensorName, "http", "http://127.0.0.1:1/")
	eventually(t, ctx, cardExists(sensorName),
		"the sensor was created but never reached the dashboard",
		dir, "maint-card-missing", 20*time.Second)

	// Before the window: no marker, which is the half that keeps this test from
	// passing on a page that draws the pill for every sensor.
	before := read[cardMark](t, ctx, readCardMark(sensorName), "the card before the window")
	if before.InMaint {
		t.Fatalf("the card is marked as in maintenance before any window exists")
	}

	// A window over the present, created through the API: the dialog is the other
	// test's subject, and this one is about what the card then shows.
	now := time.Now().UTC()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			fetch('/api/maintenance', {
			  method: 'POST',
			  headers: { 'Content-Type': 'application/json' },
			  body: JSON.stringify({
			    sensor_id: `+strconv.FormatInt(before.ID, 10)+`,
			    kind: 'once',
			    starts_at: `+jsQuote(now.Add(-time.Hour).Format(time.RFC3339))+`,
			    ends_at: `+jsQuote(now.Add(time.Hour).Format(time.RFC3339))+`,
			    timezone: 'UTC',
			    note: 'disk swap',
			    enabled: true,
			  }),
			}).then(r => { if (!r.ok) throw new Error('POST /api/maintenance: ' + r.status); })`,
			chromedp.EvalAwaitPromise))
	})
	if err != nil {
		capture(t, ctx, dir, "maint-card-create")
		t.Fatalf("could not create the window: %v", err)
	}

	// The reload is how this test gets a fresh summary, and the alternative was to
	// sit still for the dashboard's own poll — a minute between sweeps, with the event
	// stream as the fast path. A window made through the API raises no event, which is
	// not how a person makes one: the dialog in Settings refreshes the cards on its own
	// way out, and that path is TestTheMaintenancePanelCreatesAWindow's subject. This
	// one is about what the card then shows.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Reload(),
			chromedp.WaitVisible(`.card`),
		)
	}); err != nil {
		capture(t, ctx, dir, "maint-card-reload")
		t.Fatalf("could not reload the dashboard: %v", err)
	}

	eventually(t, ctx, `(() => {
	  const card = [...document.querySelectorAll('.card')]
	    .find((c) => c.querySelector('.card-name')?.textContent.trim() === `+jsQuote(sensorName)+`);
	  return !!card && card.classList.contains('in-maint');
	})()`,
		"the sensor is inside a maintenance window and its card is not marked as such",
		dir, "maint-card-unmarked", 20*time.Second)

	after := read[cardMark](t, ctx, readCardMark(sensorName), "the card inside the window")
	if after.Pill == "" {
		t.Errorf("the card carries the marker class but no word for it; a ring with " +
			"nothing to read says something is different without saying what")
	}
	if after.Status == "" {
		t.Errorf("the card lost its status pill")
	}
}

// cardMark is what a card says about a sensor: its status word, whether the
// maintenance pill is on it, and whether the card carries the marker class.
type cardMark struct {
	ID      int64  `json:"id"`
	Status  string `json:"status"`
	Pill    string `json:"pill"`
	InMaint bool   `json:"inMaint"`
}

func readCardMark(name string) string {
	return `(() => {
	  const card = [...document.querySelectorAll('.card')]
	    .find((c) => c.querySelector('.card-name')?.textContent.trim() === ` + jsQuote(name) + `);
	  if (!card) return null;
	  return {
	    id: Number(card.dataset.id),
	    status: card.querySelector('.pill:not(.st-maint)')?.textContent.trim() || '',
	    pill: card.querySelector('.pill.st-maint')?.textContent.trim() || '',
	    inMaint: card.classList.contains('in-maint'),
	  };
	})()`
}
