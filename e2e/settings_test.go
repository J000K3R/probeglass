// Package e2e — the settings page, one category at a time.
//
// The settings page used to be one long column: every panel under its heading,
// and a rail across the top that jumped between them. It is now five categories
// with a tab each, and only the chosen one is on screen. That change is invisible
// to every API test, because the panels are all still in the document and all
// still carry their values — what a browser can see and nothing else can is that
// choosing a category shows that one and takes the others away.
//
// The second half is the half that is easy to break later. The chosen category is
// remembered in the page rather than on the server, and the page is rebuilt from
// scratch on every poll and every route change. A category that survives the
// click and not the rebuild looks right in a screenshot and wrong in use: you
// pick Backup, read for a minute, and a poll you never saw moves you back to Look.
//
// Run it with:
//
//	cd e2e && go test ./... -run SettingsCategories -v
package e2e

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// settingsGroups is the shape of the page: the five categories, and which panels
// belong to each.
//
// It is a second copy of SETTINGS_GROUPS in app.js, which is deliberate. The
// test's job is to say what the page ought to look like, so reading the answer
// out of the page would make it agree with itself whatever it did. A panel that
// moves to another category is not by itself a bug — it is a change to this list,
// and the failure says so in those words rather than as a missing element.
var settingsGroups = []struct {
	key    string
	panels []string
}{
	{key: "look", panels: []string{"appearance", "map"}},
	{key: "widgets", panels: []string{"widgets"}},
	{key: "monitor", panels: []string{"dashboard", "sites", "maintenance", "examples"}},
	{key: "access", panels: []string{"access", "users"}},
	{key: "backup", panels: []string{"backup"}},
	{key: "help", panels: []string{"help", "diagnostics", "about"}},
}

// categoryView is the page as a reader sees it: which tabs are offered, which one
// is marked current, and which panels are on screen.
type categoryView struct {
	Tabs     []string `json:"tabs"`     // every tab, in order
	Hidden   []string `json:"hidden"`   // the tabs that are not offered
	Selected []string `json:"selected"` // the current one — one, or none while it is being drawn
	Shown    []string `json:"shown"`    // the panels that are not hidden
	Groups   []string `json:"groups"`   // the group each of those belongs to
}

const readCategories = `(() => {
  const tabs = [...document.querySelectorAll('#settings-body .settings-tabs [data-category]')];
  const panels = [...document.querySelectorAll('#settings-body [data-panel]')];
  const shown = panels.filter((p) => !p.hidden);
  return {
    tabs: tabs.map((t) => t.dataset.category),
    hidden: tabs.filter((t) => t.hidden).map((t) => t.dataset.category),
    selected: tabs
      .filter((t) => !t.hidden && t.getAttribute('aria-selected') === 'true')
      .map((t) => t.dataset.category),
    shown: shown.map((p) => p.dataset.panel),
    groups: shown.map((p) => p.dataset.group),
  };
})()`

// TestSettingsShowsOneCategoryAtATime walks the tabs and checks, at each one,
// that it is the only category with anything on screen — and that the choice is
// still there after the page has been rebuilt underneath it.
func TestSettingsShowsOneCategoryAtATime(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body .settings-tabs [data-category]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "settings-tabs-missing")
		t.Fatalf("the settings page never drew its categories: %v", err)
	}

	// Every category is offered, and exactly one of them is marked current. Two
	// marked tabs would mean the page is showing one category while claiming
	// another, which is worse than showing the wrong one.
	first := read[categoryView](t, ctx, readCategories, "the settings categories")
	if want := groupKeys(); !equal(first.Tabs, want) {
		t.Errorf("the settings page offers the categories %v, want %v.\n"+
			"If a category was added or renamed, this list is what has to be "+
			"updated with it.", first.Tabs, want)
	}
	if !equal(first.Selected, []string{"look"}) {
		t.Errorf("the settings page opens on %v, want [look]: a fresh visit has "+
			"nothing remembered, and Look is the first category there is",
			first.Selected)
	}

	// Each category in turn: it is the only one whose panels are on screen, and
	// every panel it owns is among them.
	for _, group := range settingsGroups {
		chooseCategory(t, ctx, dir, group.key)
		view := read[categoryView](t, ctx, readCategories, "the "+group.key+" category")

		if !equal(view.Selected, []string{group.key}) {
			capture(t, ctx, dir, "category-"+group.key)
			t.Errorf("after choosing %q the page marks %v as current", group.key, view.Selected)
		}
		if kinds := distinct(view.Groups); !equal(kinds, []string{group.key}) {
			capture(t, ctx, dir, "category-"+group.key+"-mixed")
			t.Errorf("after choosing %q the panels on screen belong to %v; only one "+
				"category is meant to be shown at a time, and %v is a panel of "+
				"another one", group.key, kinds, view.Shown)
		}
		got := slices.Clone(view.Shown)
		slices.Sort(got)
		want := slices.Clone(group.panels)
		slices.Sort(want)
		if !equal(got, want) {
			capture(t, ctx, dir, "category-"+group.key+"-panels")
			t.Errorf("the %q category shows %v, want %v. A panel that changed "+
				"category is not a failure by itself — it means settingsGroups in "+
				"this test is out of date.", group.key, got, want)
		}
	}

	// --- the choice outlives the page being redrawn --------------------------
	//
	// The page is rebuilt on every poll and on every route change, and the
	// category lives in the page rather than on the server — so the rebuild is
	// where it can be lost. A round trip is the cheap way to force one; waiting
	// for the sixty-second poll would prove the same thing more slowly.
	chooseCategory(t, ctx, dir, "backup")

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.WaitVisible(`#view-dashboard`),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body .settings-tabs [data-category]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "settings-revisit")
		t.Fatalf("could not leave the settings page and come back: %v", err)
	}

	back := read[categoryView](t, ctx, readCategories, "the settings categories again")
	if !equal(back.Selected, []string{"backup"}) {
		capture(t, ctx, dir, "category-lost")
		t.Errorf("after leaving the settings page and returning it is on %v, want "+
			"[backup].\nThe chosen category is remembered in the page and has to "+
			"survive the rebuild that every poll and every route change causes; "+
			"otherwise the page moves the reader back to the first category "+
			"without being asked.", back.Selected)
	}
}

// chooseCategory presses a category tab, failing the test rather than an
// assertion when the tab cannot be reached at all — a category that is not there
// is a different problem from one that is there and shows the wrong thing. It
// runs the shared tab helper inside a phase deadline of its own, so that a tab
// which never appears reports the category by name instead of timing out against
// whatever ran before it.
func chooseCategory(t *testing.T, ctx context.Context, dir, key string) {
	t.Helper()
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, openSettingsCategory(key))
	}); err != nil {
		capture(t, ctx, dir, "category-"+key+"-unreachable")
		t.Fatalf("the settings page never offered the %q category: %v", key, err)
	}
}

// groupKeys is the categories in the order the page keeps them in.
func groupKeys() []string {
	keys := make([]string, 0, len(settingsGroups))
	for _, group := range settingsGroups {
		keys = append(keys, group.key)
	}
	return keys
}

// distinct returns the values of a list once each, in the order they first
// appear. It is how "which categories are on screen" is turned from one group
// per panel into the list of groups.
func distinct(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
