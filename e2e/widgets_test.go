// Package e2e — the dashboard header, built out of widgets.
//
// The header used to be one fixed strip: a ring with the counts and four figures
// beside it. It is now a list of widgets that can be added, removed and put in
// order, and the list is stored on the account. Two things about that are worth a
// browser and cannot be checked from the API.
//
// The first is that the change is invisible for someone who makes no choice: the
// header has to start as exactly what it was, ring and four figures, or every
// existing screenshot and every habit is quietly wrong. The second is that a list
// is a thing with an order, and an order that is only in memory is an order that
// is lost on the next reload — which is the failure that looks like the feature
// working until the moment you come back to it.
//
// The same list is also shown twice: as tiles you drag, and as rows in Settings.
// They are one stored value seen two ways, and the test checks that they agree,
// because two views of one setting that can disagree are worse than one view.
//
// Run it with:
//
//	cd e2e && go test ./... -run 'Widget' -v
package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/j000k3r/weatherglass/internal/model"
)

// headerView is the strip as it is on screen: which widgets are on it, in what
// order, and whether the lamp still looks like a lamp.
type headerView struct {
	IDs     []string `json:"ids"`
	Ring    int      `json:"ring"`    // the ring drawn in the status widget
	Figures int      `json:"figures"` // the plain numbers beside it
}

const readHeader = `(() => {
  const strip = document.getElementById('summary');
  if (!strip) return { ids: [], ring: 0, figures: 0 };
  return {
    ids: [...strip.querySelectorAll(':scope > [data-widget]')].map((t) => t.dataset.widget),
    ring: strip.querySelectorAll('.widget-status .summary-ring svg').length,
    figures: strip.querySelectorAll(':scope > [data-widget] .stat').length,
  };
})()`

// pickerView is the Settings half of the same list: every widget there is, and
// which of them are switched on, in the order they are on the strip.
type pickerView struct {
	Offered []string `json:"offered"`
	Chosen  []string `json:"chosen"`
}

const readPicker = `(() => {
  const rows = [...document.querySelectorAll('#widget-picks .widget-pick')];
  const box = (row) => row.querySelector('[data-pick-toggle]');
  return {
    offered: rows.map((row) => box(row).dataset.pickToggle),
    chosen: rows.filter((row) => box(row).checked).map((row) => box(row).dataset.pickToggle),
  };
})()`

// headerIs is the predicate "the strip is exactly these widgets, in this order".
func headerIs(want string) string {
	return fmt.Sprintf(
		`[...document.querySelectorAll('#summary > [data-widget]')]`+
			`.map((t) => t.dataset.widget).join(',') === %q`, want)
}

// storedWidgets is the predicate "the account has been told this list". The
// screen follows the click immediately; the request only afterwards, so a test
// that reloads as soon as the tiles move would race the write it means to check.
func storedWidgets(want string) string {
	return fmt.Sprintf(
		`fetch('/api/session').then(r => r.json())`+
			`.then(s => ((s.ui || {}).widgets || '') === %q)`, want)
}

// TestDashboardHeaderIsTheOldHeaderByDefault checks the two things a fresh
// account is promised: the header it always had, and a Settings list that offers
// every widget the server knows about.
func TestDashboardHeaderIsTheOldHeaderByDefault(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	eventually(t, ctx, headerIs(model.DefaultWidgets),
		"a fresh account did not start with the header the dashboard has always "+
			"had; making the header configurable was not meant to change it for "+
			"anyone who does not configure it",
		dir, "widgets-default", 20*time.Second)

	// And it is still the same header in kind, not merely the same ids: a ring
	// with the counts, and four figures. The ids could be right with every tile
	// drawing something else.
	shape := read[headerView](t, ctx, readHeader, "the header")
	if shape.Ring != 1 {
		capture(t, ctx, dir, "widgets-no-ring")
		t.Errorf("the header draws %d status ring(s), want one", shape.Ring)
	}
	if shape.Figures != 4 {
		capture(t, ctx, dir, "widgets-figures")
		t.Errorf("the header shows %d figure(s), want the four a fresh account "+
			"starts with: uptime, latency, probes and since", shape.Figures)
	}

	// --- the picker, and the list that lives twice ---------------------------
	//
	// Which widgets exist is written down in two places: model.Widgets in Go,
	// which is what the server will store, and the registry in widgets.js, which
	// is what the browser can draw. A widget on one list and not the other is
	// either a blank tile or a setting that appears to save and does not, and
	// neither shows up in an API test. The picker lists every widget there is,
	// in the registry's order, so it is the browser's list to compare against.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#widget-picks`),
		)
	}); err != nil {
		capture(t, ctx, dir, "widget-picker-missing")
		t.Fatalf("the settings page never offered the widget list: %v", err)
	}

	picker := read[pickerView](t, ctx, readPicker, "the widget list")
	if len(model.Widgets) != len(picker.Offered) {
		capture(t, ctx, dir, "widget-parity")
		t.Errorf("the server knows %d widget(s) and the dashboard offers %d.\n"+
			"server: %v\ndashboard: %v", len(model.Widgets), len(picker.Offered),
			model.Widgets, picker.Offered)
	} else {
		// The order is each side's business — it is the list that has to agree,
		// which is the same rule the sensor types are compared by.
		for _, id := range model.Widgets {
			if !contains(picker.Offered, id) {
				t.Errorf("the server stores the widget %q and the dashboard does not "+
					"offer it, so it could be saved and never drawn", id)
			}
		}
		for _, id := range picker.Offered {
			if !contains(model.Widgets, id) {
				t.Errorf("the dashboard offers the widget %q and the server does not "+
					"know it, so choosing it would appear to save and not", id)
			}
		}
	}

	// The two views of the stored list agree. The strip is behind a hidden view
	// here, and that is fine: both are read off the same value, and a screen that
	// has to be looked at for the two to match is exactly what this rules out.
	strip := read[headerView](t, ctx, readHeader, "the header")
	if !equal(picker.Chosen, strip.IDs) {
		capture(t, ctx, dir, "widget-views-disagree")
		t.Errorf("the Settings list has %v switched on while the header shows %v.\n"+
			"They are one stored value seen two ways, so they cannot disagree",
			picker.Chosen, strip.IDs)
	}
	if !equal(picker.Chosen, model.ParseWidgets(model.DefaultWidgets)) {
		t.Errorf("the Settings list starts with %v switched on, want the default %v",
			picker.Chosen, model.ParseWidgets(model.DefaultWidgets))
	}
}

// TestWidgetListIsArrangedAndKept adds a widget, moves it, and reloads the page
// — the last step being the one that decides whether the list was saved or only
// remembered.
func TestWidgetListIsArrangedAndKept(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)
	eventually(t, ctx, headerIs(model.DefaultWidgets),
		"the header did not start with the default widgets",
		dir, "widgets-start", 20*time.Second)

	// A clock added at the end, then moved one place left. Two clicks rather than
	// a drag, deliberately: dragging is a pointer gesture, and the arrows are the
	// path that works from a keyboard — so this is also the check that the arrows
	// are wired to the same list the drag is.
	const afterAdd = "status,uptime,latency,probes,since,clock"
	const afterMove = "status,uptime,latency,probes,clock,since"

	click := func(selector, what string) {
		t.Helper()
		if err := withTimeout(ctx, func(tctx context.Context) error {
			return chromedp.Do(tctx, chromedp.Click(selector))
		}); err != nil {
			capture(t, ctx, dir, "widget-click")
			t.Fatalf("could not click %s: %v", what, err)
		}
	}

	click(`#widgets-tools [data-widget-edit]`, "the button that starts arranging the header")

	// The layout switcher stands down while the header is being arranged, and the
	// two buttons that arrange it take its place. This is asserted rather than
	// assumed because it is a width: the top bar holds one line at every width the
	// dashboard is read at, and a control that quietly costs it a second line does
	// not fail anything else in the suite.
	eventually(t, ctx, `document.querySelector('#layout-switcher').hidden === true`,
		"the layout switcher stayed in the top bar while the header was being arranged",
		dir, "widgets-switcher-still-there", 10*time.Second)

	// Adding is a window of its own rather than dashed tiles in the strip, so the
	// test has to go through the window: the button that opens it, then the row.
	eventually(t, ctx, `!!document.querySelector('#widgets-tools [data-widget-pick]')`,
		"arranging the header did not offer a way to add a widget",
		dir, "widgets-no-add", 10*time.Second)
	click(`#widgets-tools [data-widget-pick]`, "the button that opens the widget picker")

	eventually(t, ctx, `!!document.querySelector('#widget-picker[open] [data-widget-add="clock"]')`,
		"the picker did not open, or did not offer the widgets that are not on the "+
			"header yet",
		dir, "widgets-edit-mode", 10*time.Second)
	click(`#widget-picker [data-widget-add="clock"]`, "the clock in the widget picker")

	eventually(t, ctx, headerIs(afterAdd),
		"adding the clock did not put it at the end of the header",
		dir, "widget-added", 10*time.Second)

	// The window has to be out of the way by now, or the tile behind it is not
	// clickable at all — which is exactly what the next two clicks would report.
	eventually(t, ctx, `!document.querySelector('#widget-picker[open]')`,
		"choosing a widget left the picker standing over the header it was chosen for",
		dir, "widget-picker-still-open", 10*time.Second)

	click(`[data-widget="clock"] [data-widget-move="-1"]`, "the clock's left arrow")
	eventually(t, ctx, headerIs(afterMove),
		"the clock's left arrow did not move it one place along",
		dir, "widget-moved", 10*time.Second)

	// The order reaches the server, which is what makes it survive anything.
	eventually(t, ctx, storedWidgets(afterMove),
		"the header was rearranged but the account was never told",
		dir, "widget-not-stored", 15*time.Second)

	// --- and a reload is what proves it --------------------------------------
	//
	// Reading the strip back without reloading proves nothing: the tiles are
	// drawn from the list the click produced, which is in memory either way. The
	// reload is the difference between a setting and a coincidence.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Reload(), chromedp.WaitVisible(`#app`))
	}); err != nil {
		capture(t, ctx, dir, "widget-reload")
		t.Fatalf("could not reload the dashboard: %v", err)
	}
	eventually(t, ctx, headerIs(afterMove),
		"the header went back to another order after a reload, so the arrangement "+
			"was only ever in memory",
		dir, "widget-order-lost", 20*time.Second)

	// --- taking one away is the same road in reverse -------------------------
	arranging := `#widgets-tools [data-widget-edit]`
	click(arranging, "the button that starts arranging the header")
	eventually(t, ctx, `!!document.querySelector('[data-widget="clock"] [data-widget-remove]')`,
		"arranging the header did not put a remove button on the clock",
		dir, "widget-no-remove", 10*time.Second)
	click(`[data-widget="clock"] [data-widget-remove]`, "the clock's remove button")

	eventually(t, ctx, headerIs(model.DefaultWidgets),
		"removing the clock did not leave the header it started as",
		dir, "widget-not-removed", 10*time.Second)
	eventually(t, ctx, storedWidgets(model.DefaultWidgets),
		"the clock was removed from the header but the account still has it",
		dir, "widget-removal-not-stored", 15*time.Second)

	// And the switcher it stood down for comes back when the arranging is over,
	// because a control that leaves and does not return is a control that was lost.
	click(arranging, "the button that stops arranging the header")
	eventually(t, ctx, `document.querySelector('#layout-switcher').hidden === false`,
		"the layout switcher did not come back when the arranging was over",
		dir, "widgets-switcher-missing", 10*time.Second)
}

// contains reports whether a list holds a value.
func contains(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
