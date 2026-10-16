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
