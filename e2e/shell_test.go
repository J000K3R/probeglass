// Package e2e — the shell around the sensors.
//
// The tests here cover what the dashboard grew on top of the sensors themselves:
// who is signed in and the switches beside their name, how the sensors are laid
// out, where they are, and what the top bar does when it runs out of room.
//
// They are browser tests for the same reason as the others in this module. Each
// one is about something only a rendered page can be wrong about — a menu that
// is built but painted underneath the page, a preference that is written to the
// server and never read back, a map whose markers disappear along with the
// tiles. No API test can see any of that.
//
// Run them with:
//
//	cd e2e && go test ./... -run 'Account|Layout|Sites' -v
package e2e

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	// The two places these tests create. One is at a provider and one is in the
	// house, because the tree, the map and the legend all have something to say
	// about the difference, and one name would not have shown it.
	siteExternal = "Hetzner"
	siteLocal    = "Keller"

	// The discard port. Nothing listens on it, so a connection there is refused
	// immediately and every tile fails — which is the honest-map case: the
	// places still have to be drawn, and one sentence has to say what is
	// missing.
	deadTileURL = "http://127.0.0.1:9/{z}/{x}/{y}.png"
)

// --- plumbing shared by the three tests ------------------------------------

// artifactsDir is where a failing phase leaves its screenshot. It is the same
// environment variable the other browser tests use, so one CI job collects all
// of them.
func artifactsDir(t *testing.T) string {
	t.Helper()
	//nolint:misspell // "artifacts" is the spelling GitHub uses for the uploaded files
	if dir := os.Getenv("WEATHERGLASS_E2E_ARTIFACTS"); dir != "" {
		return dir
	}
	return t.TempDir()
}

// openBrowser starts a headless browser and returns the context it lives on
// together with the function that closes it.
//
// Without a deadline, for the reason the other tests give: a deadline on this
// context kills the browser rather than the action, so the phase that timed out
// would have nothing left to photograph.
func openBrowser(t *testing.T, browser string) (context.Context, func()) {
	t.Helper()
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	ctx, cancel := chromedp.NewContext(allocCtx)
	if err := chromedp.Do(ctx); err != nil {
		cancel()
		cancelAlloc()
		t.Fatalf("could not start the browser: %v", err)
	}
	return ctx, func() { cancel(); cancelAlloc() }
}

// firstRun walks the first-run path the way a person does: the account, the
// question of how much interface to show, and then past the tour, which covers
// the whole page while it is up.
//
// Advanced mode is chosen rather than left to the default because the settings
// panes these tests use — the sites and the tile source — are part of it.
func firstRun(t *testing.T, ctx context.Context, base, dir string) {
	t.Helper()
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
			chromedp.WaitVisible(`#app`),
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`.tour-card [data-tour="skip"]`),
			chromedp.WaitNotPresent(`.tour-card`),
		)
	})
	if err != nil {
		capture(t, ctx, dir, "first-run")
		t.Fatalf("the first run did not reach the dashboard: %v\n"+
			"the auth card said: %s", err, authError(ctx))
	}
}

// eventually waits for a predicate in the page and fails with the sentence the
// caller wrote when it never comes true.
//
// The predicate is evaluated as an expression, and a promise is awaited — which
// is what lets one of these ask the server rather than the screen. That matters
// for the preference writes: the interface follows the click immediately and the
// request only afterwards, so a test that reloads as soon as the attribute
// changes would be racing the write it means to check.
func eventually(t *testing.T, ctx context.Context, expr, what, dir, shot string, timeout time.Duration) {
	t.Helper()
	if _, err := chromedp.Run(ctx, chromedp.Poll[chromedp.Void](expr, chromedp.WithPollingTimeout(timeout))); err != nil {
		capture(t, ctx, dir, shot)
		t.Fatalf("%s: %v", what, err)
	}
}

// read evaluates an expression in the page and decodes what it returns.
func read[T any](t *testing.T, ctx context.Context, expr, what string) T {
	t.Helper()
	value, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
	if err != nil {
		t.Fatalf("could not read %s: %v", what, err)
	}
	return value
}

// openSettingsCategory chooses one of the settings page's categories.
//
// The page shows one category at a time and keeps the others in the document and
// hidden, so a panel that is not in the chosen category is not merely further
// down the page — waiting for it to become visible waits for something that is
// never going to happen. Choosing the category is part of getting to the panel,
// which is why every test that wants one says which.
//
// It waits for the tab before clicking it, because the page is drawn only once
// the settings it needs have come back from the server: a click sent immediately
// after the route changes has nothing to click yet.
func openSettingsCategory(key string) chromedp.Action[chromedp.Void] {
	selector := fmt.Sprintf(`.settings-tabs [data-category=%q]`, key)
	ready := fmt.Sprintf(
		"(() => { const tab = document.querySelector(%s); return !!tab && !tab.hidden; })()",
		strconv.Quote(selector))
	return func(ctx context.Context, _ *chromedp.Target) (chromedp.Void, error) {
		if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](ready,
			chromedp.WithPollingTimeout(10*time.Second))); err != nil {
			return chromedp.Void{}, fmt.Errorf("the %q category never appeared on the settings page: %w", key, err)
		}
		if err := chromedp.Do(ctx, chromedp.Click(selector)); err != nil {
			return chromedp.Void{}, fmt.Errorf("could not choose the %q category: %w", key, err)
		}
		return chromedp.Void{}, nil
	}
}

// --- the account area ------------------------------------------------------

// menuView is what the account menu holds.
type menuView struct {
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Switches []string `json:"switches"`
	Items    []string `json:"items"`
}

const readMenu = `(() => {
  const menu = document.querySelector('.user-menu');
  if (!menu) return null;
  const name = menu.querySelector('.user-menu-name');
  const role = menu.querySelector('.user-menu-role');
  return {
    name: name ? name.textContent.trim() : '',
    role: role ? role.textContent.trim() : '',
    switches: Array.from(menu.querySelectorAll('[data-setting]')).map(s => s.dataset.setting),
    items: Array.from(menu.querySelectorAll('[data-menu]')).map(b => b.dataset.menu),
  };
})()`

// TestAccountMenuSpeaksForTheSignedInAccount checks the area in the top right:
// that it names who is signed in, that it can be dismissed the two ways a
// popover is dismissed, that its quick switches reach the server, and that
// signing out actually ends the session.
//
// The last one is the point of the whole move. Signing out used to live at the
// bottom of the Settings page, where nobody looks for it, and a button that is
// merely in a different place is not worth a browser test — but a button whose
// click has to end a session, reload the shell and land on the sign-in form is.
func TestAccountMenuSpeaksForTheSignedInAccount(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// The chip carries the account rather than a placeholder, before anything is
	// opened: it is the part that is on screen the whole time.
	if got := text(ctx, t, "#user-chip-name"); got != testUser {
		capture(t, ctx, dir, "chip-wrong-name")
		t.Fatalf("the account chip says %q, want %q", got, testUser)
	}
	if got := text(ctx, t, "#user-avatar"); got != "E" {
		capture(t, ctx, dir, "chip-wrong-initial")
		t.Errorf("the avatar says %q, want the first letter of the username", got)
	}

	// --- the menu opens, and says who this is ------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.WaitVisible(`.user-menu`),
		)
	}); err != nil {
		capture(t, ctx, dir, "menu-did-not-open")
		t.Fatalf("clicking the account chip did not open the menu: %v", err)
	}

	menu := read[menuView](t, ctx, readMenu, "the account menu")
	if menu.Name != testUser {
		t.Errorf("the menu names %q, want %q", menu.Name, testUser)
	}
	// The first account is an administrator, and the menu says so — the role is
	// the part that tells a person why some buttons are greyed out.
	if menu.Role != "Administrator" {
		t.Errorf("the menu gives the role as %q, want Administrator", menu.Role)
	}
	if !slices.Contains(menu.Switches, "theme") || !slices.Contains(menu.Switches, "density") {
		t.Errorf("the menu offers the quick switches %v, want theme and density among them", menu.Switches)
	}
	if !equal(menu.Items, []string{"settings", "signout"}) {
		t.Errorf("the menu's items are %v, want [settings signout]", menu.Items)
	}

	// --- it can be dismissed the two ways a popover is ----------------------

	if err := chromedp.Do(ctx, chromedp.KeyEvent("\x1b")); err != nil {
		t.Fatalf("could not press Escape: %v", err)
	}
	eventually(t, ctx, `!document.querySelector('.user-menu')`,
		"Escape did not close the account menu", dir, "menu-escape", 5*time.Second)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.WaitVisible(`.user-menu`),
			// A click on the page behind it. The summary is the nearest thing
			// that is always there and does nothing when it is clicked, so a
			// menu that stays open is the only thing this can fail about.
			//
			// The CSS form is not decoration. A plain string is a text search
			// in the browser, not a selector lookup: it matches any node whose
			// text contains the string, comments included, and it then clicks
			// only the first of them. Markup that names "#summary" in a comment
			// — which index.html does, to explain why the tools row is a
			// sibling — adds a comment node to the result, and a comment has no
			// boxes at all, so waiting for it to be visible waits forever. The
			// element is meant here; ask for the element.
			chromedp.Click(chromedp.CSS(`#summary`)),
		)
	}); err != nil {
		capture(t, ctx, dir, "menu-click-away")
		t.Fatalf("could not click the page behind the account menu: %v", err)
	}
	eventually(t, ctx, `!document.querySelector('.user-menu')`,
		"a click outside the account menu did not close it", dir, "menu-click-away", 5*time.Second)

	// --- a quick switch reaches the server, and survives a reload -----------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.WaitVisible(`.user-menu [data-setting="theme"]`),
			// SetValue writes the property and dispatches input and change, so
			// this is the same event path a person's selection takes.
			chromedp.SetValue(`.user-menu [data-setting="theme"]`, "dark"),
		)
	}); err != nil {
		capture(t, ctx, dir, "theme-switch")
		t.Fatalf("could not choose the dark theme in the account menu: %v", err)
	}

	// The first half is the screen following the click, which it does before the
	// request is even sent.
	eventually(t, ctx, `document.documentElement.dataset.theme === 'dark'`,
		"the dark theme was chosen but the page did not switch to it", dir, "theme-not-applied", 5*time.Second)

	// The second half is the server having been told. Asking it is what makes
	// the reload below a test of the write rather than a test of a race.
	eventually(t, ctx,
		`fetch('/api/session').then(r => r.json()).then(s => (s.ui || {}).theme === 'dark')`,
		"the theme was switched but the server was never told", dir, "theme-not-stored", 15*time.Second)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Reload(),
			chromedp.WaitVisible(`#app`),
		)
	}); err != nil {
		capture(t, ctx, dir, "theme-reload")
		t.Fatalf("could not reload the dashboard: %v", err)
	}
	eventually(t, ctx, `document.documentElement.dataset.theme === 'dark'`,
		"the dark theme did not survive a reload: the switch was stored but not read back",
		dir, "theme-lost-on-reload", 15*time.Second)

	// --- signing out ends the session ---------------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.WaitVisible(`.user-menu [data-menu="signout"]`),
			chromedp.Click(`.user-menu [data-menu="signout"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "signout-click")
		t.Fatalf("could not press Sign out in the account menu: %v", err)
	}

	// Nothing is asserted about the moment in between: the handler reloads the
	// page, and what matters is where that lands. The sign-in form is the only
	// honest place for a session that has ended — a dashboard left on screen
	// after signing out would be the defect.
	//
	// WaitVisible rather than a poll of our own, because the reload it is waiting
	// through destroys the page the poll would be running in: chromedp's own
	// element waits re-resolve the frame on every attempt, so they survive a
	// navigation and a hand-written predicate does not.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.WaitVisible(`#auth`),
			chromedp.WaitNotVisible(`#app`),
		)
	}); err != nil {
		capture(t, ctx, dir, "signout-did-not-land")
		t.Fatalf("signing out did not return to the sign-in form: %v", err)
	}

	if got := visibleIDs(t, ctx, "#boot, #auth, #app, .view"); !equal(got, []string{"auth"}) {
		capture(t, ctx, dir, "signout-wrong-screen")
		t.Errorf("after signing out the visible screens are %v, want just the auth card.\n"+
			"Signing in again has to be possible from here, and the session has to be gone "+
			"on the server as well as on the screen.", got)
	}
}

// --- the layout switcher ---------------------------------------------------

// switcherView is what the layout control is showing.
type switcherView struct {
	Hidden  bool     `json:"hidden"`
	Visible bool     `json:"visible"`
	Layouts []string `json:"layouts"`
	Pressed []string `json:"pressed"`
}

const readSwitcher = `(() => {
  const host = document.querySelector('#layout-switcher');
  if (!host) return null;
  return {
    hidden: host.hidden,
    visible: host.getClientRects().length > 0,
    layouts: Array.from(host.querySelectorAll('[data-layout]')).map(b => b.dataset.layout),
    pressed: Array.from(host.querySelectorAll('[data-layout][aria-pressed="true"]')).map(b => b.dataset.layout),
  };
})()`

// clickLayout presses one of the view buttons and waits for the element that
// only that view has.
//
// Waiting for the element rather than for the stored preference is the whole
// assertion: a button that writes the right value and draws the wrong view looks
// perfectly healthy from the API and is exactly the failure a person would
// report.
//
// It opens the dashboard first, because the control only exists there — a click
// on a hidden button is not an error in chromedp, it is a click that does
// nothing, and the failure would then be reported against the view rather than
// against the route the test forgot to change.
func clickLayout(t *testing.T, ctx context.Context, dir, layout, marker string) {
	t.Helper()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.WaitVisible(`#layout-switcher [data-layout="grid"]`),
			chromedp.Click(fmt.Sprintf(`#layout-switcher [data-layout=%q]`, layout)),
			chromedp.Poll[chromedp.Void](fmt.Sprintf(
				`!!document.querySelector(%q) && `+
					`document.querySelector('#layout-switcher [data-layout=%q]').getAttribute('aria-pressed') === 'true'`,
				marker, layout), chromedp.WithPollingTimeout(20*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, dir, "layout-"+layout)
		t.Fatalf("switching to the %s view did not put %s on screen: %v", layout, marker, err)
	}
}

// TestLayoutSwitcherChangesTheView checks that the top bar's control really
// changes how the sensors are drawn, that the choice is kept, and that the
// control is only there where it means something.
func TestLayoutSwitcherChangesTheView(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.WaitVisible(`#layout-switcher [data-layout="grid"]`))
	}); err != nil {
		capture(t, ctx, dir, "switcher-missing")
		t.Fatalf("the layout switcher is not on the dashboard: %v", err)
	}

	control := read[switcherView](t, ctx, readSwitcher, "the layout switcher")
	if !equal(control.Layouts, []string{"grid", "list", "tree", "wall", "map"}) {
		t.Errorf("the layout switcher offers %v, want [grid list tree wall map]", control.Layouts)
	}
	// A fresh account starts on the cards, which is the default the server
	// hands out and the one the tour's card steps assume.
	if !equal(control.Pressed, []string{"grid"}) {
		t.Errorf("the switcher marks %v as current, want just grid", control.Pressed)
	}

	// Each view, and the element that only it draws. The list row is a table
	// row, the tree a collapsible node, the wall a tile, the map its canvas.
	clickLayout(t, ctx, dir, "list", `.sensors-table .row`)
	clickLayout(t, ctx, dir, "tree", `.site-node`)
	clickLayout(t, ctx, dir, "wall", `.wall-group .tile`)
	clickLayout(t, ctx, dir, "map", `.map-canvas`)

	// --- the choice is kept, and only where it is meaningful ----------------

	// Back to the wall, because that is the one the reload below has to bring
	// back — and because a value that is written on the way out and read on the
	// way in is not the same thing as one that is simply still in memory.
	clickLayout(t, ctx, dir, "wall", `.wall-group .tile`)
	eventually(t, ctx,
		`fetch('/api/session').then(r => r.json()).then(s => (s.ui || {}).layout === 'wall')`,
		"the wall was chosen but the server was never told", dir, "layout-not-stored", 15*time.Second)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Reload(), chromedp.WaitVisible(`#app`))
	}); err != nil {
		capture(t, ctx, dir, "layout-reload")
		t.Fatalf("could not reload the dashboard: %v", err)
	}
	eventually(t, ctx,
		`document.documentElement.dataset.layout === 'wall' && !!document.querySelector('.wall-group .tile')`,
		"the chosen layout did not survive a reload", dir, "layout-lost", 20*time.Second)

	// The switcher means something on the dashboard and nothing anywhere else,
	// so it is not on screen elsewhere — and hidden rather than merely
	// invisible: a control that is only transparent still takes focus and is
	// still read out by a screen reader.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			chromedp.Poll[chromedp.Void](`document.querySelector('#sites-panel') !== null`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "settings-unreachable")
		t.Fatalf("could not reach the settings screen: %v", err)
	}
	if gone := read[switcherView](t, ctx, readSwitcher, "the layout switcher"); !gone.Hidden || gone.Visible {
		capture(t, ctx, dir, "switcher-on-settings")
		t.Errorf("the layout switcher is on the settings screen (hidden=%v, visible=%v); "+
			"it belongs to the dashboard, and it has to be hidden rather than styled away",
			gone.Hidden, gone.Visible)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.Poll[chromedp.Void](`document.querySelector('#layout-switcher').hidden === false`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "switcher-did-not-come-back")
		t.Fatalf("the layout switcher did not come back with the dashboard: %v", err)
	}
}

// --- the top bar, and what it does when it runs out of room ----------------

// barShape is what the top bar looks like at one width: how many rows the sides
// take, which words are on screen, whether anything overflows the bar, whether the
// ⋮, the search field and the magnifier are in the bar at all, whether the tab strip
// has been squeezed past its own width, and — while the ⋮ panel is open — how many
// rows it has, whether any of it runs off the window, and which of the bar's own
// controls it holds.
type barShape struct {
	Rows        int      `json:"rows"`
	Words       []string `json:"words"`
	Overflow    bool     `json:"overflow"`
	Label       string   `json:"label"`
	Height      int      `json:"height"`
	More        bool     `json:"more"`
	Search      bool     `json:"search"`
	Magnifier   bool     `json:"magnifier"`
	TabsClipped bool     `json:"tabsClipped"`
	PanelRows   int      `json:"panelRows"`
	PanelOff    bool     `json:"panelOff"`
	PanelHas    []string `json:"panelHas"`
}

// readBar measures the bar rather than reading a stylesheet: the widths at which
// it gives up a word, and the width at which it folds its working controls into
// the ⋮, are in the CSS as media queries, and a test that only asked whether a
// rule existed would pass with the numbers in it wrong.
//
// A row is counted by banding the tops of the two side columns' children, because
// they are centred rather than aligned: their tops differ by a few pixels on one
// row and by half the bar on two.
//
// "On screen" is a box with a width and a height, not a `display` that is not
// `none`: a word inside a control that has been folded away still computes to
// whatever the rule for it says, and a test that read the computed style would
// report the words of a panel nobody can see.
const readBar = `(() => {
  const bar = document.querySelector('.topbar');
  const box = bar.getBoundingClientRect();
  const live = (sel) => {
    const n = document.querySelector(sel);
    if (!n) return false;
    const r = n.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const words = [];
  const shown = (sel, name) => { if (live(sel)) words.push(name); };
  shown('.brand-name', 'brand');
  shown('#tabs .tab > span', 'tabs');
  shown('#new-sensor > span', 'sensor');
  shown('.live-word', 'live');
  shown('.user-chip-name', 'user');
  shown('#layout-switcher .seg-btn span', 'layout');

  const tops = [...document.querySelectorAll('.topbar-side > *, .topbar-actions > *')]
    .filter((n) => n.getBoundingClientRect().width > 0)
    .map((n) => Math.round(n.getBoundingClientRect().top))
    .sort((a, b) => a - b);
  const bands = [];
  for (const top of tops) {
    if (!bands.length || top - bands[bands.length - 1] > 14) bands.push(top);
  }

  // The tab strip is the one thing in the bar that can be clipped without the bar
  // looking clipped: it scrolls rather than overflows (see .tabs in style.css), so a
  // strip whose last tab is half out of sight still reads as one line, and every
  // other check here calls the bar fine. Handing the magnifier its 34 pixels out of
  // the 320px row is what made this measurable; see the 380/379 pair below.
  const tabsEl = document.querySelector('#tabs');
  const tabsClipped = !!tabsEl && tabsEl.scrollWidth > tabsEl.clientWidth + 1;

  const panel = document.querySelector('.topbar.menu-open .topbar-extra');
  let panelRows = 0;
  let panelOff = false;
  const panelHas = [];
  if (panel && panel.getBoundingClientRect().width > 0) {
    const at = panel.getBoundingClientRect();
    panelOff = at.left < 0 || at.right > window.innerWidth + 1 || at.bottom > window.innerHeight + 1;
    const rows = new Set();
    for (const button of panel.querySelectorAll('button')) {
      if (button.getBoundingClientRect().width > 0) rows.add(Math.round(button.getBoundingClientRect().top));
    }
    panelRows = rows.size;
    for (const child of panel.children) {
      if (child.getBoundingClientRect().width > 0) panelHas.push(child.id);
    }
  }

  return {
    rows: bands.length,
    words,
    overflow: bar.scrollWidth > bar.clientWidth + 1,
    label: document.querySelector('#tabs .tab[data-route="dashboard"]').getAttribute('aria-label'),
    height: Math.round(box.height),
    more: live('#topbar-more'),
    search: live('.search'),
    magnifier: live('#topbar-search'),
    tabsClipped,
    panelRows,
    panelOff,
    panelHas,
  };
})()`

// TestTopBarGivesUpWordsThenControlsBeforeItsLine is about the one thing the bar
// must not do, which is grow a second row of buttons on an ordinary screen.
//
// It gives up three things, in this order and never more than it has to. First the
// words: the labels beside the icons in the layout switcher, which say what the
// icon beside them already says, and then every other word — the tab names, the
// brand, and the four on buttons — each of which keeps its name as a title and as
// an aria-label, because a word hidden with `display: none` is not read out and an
// icon alone has no name.
//
// Then the controls themselves, which is what this test grew a second half for.
// Below ~1133px the bar cannot hold its four working controls on one line with
// everything else, and it used to wrap instead: two rows from 1100 down, three
// from 600, and four at 440, because each side then broke inside itself as well.
// They now fold into the ⋮ inside the bar — the same elements, laid out as a
// panel — and the row they gave back is what the search field and the tabs live
// on. The bar is one line at every width down to 320, and that is the assertion
// this test exists for.
//
// The search field is the one thing that does not go in there. Below ~620px it
// leaves the row, because a field that fits there is a stub seven characters wide,
// and a magnifier takes its place in the row rather than a row of the ⋮: the field
// is drawn over the bar at the full width of it, one press away, which is the only
// shape a search field is worth having on a phone. That hand-off is the last pair
// of columns in the steps table below, and the last part of this test is what the
// press does.
//
// One thing in this bar fails quietly, and it is why the tab strip is measured
// separately. Everything else here is checked by whether it is on screen: a word, a
// button, a panel, a row count. The tab strip is the exception — it scrolls when it
// runs out of room rather than overflowing, so a strip with its last tab half out of
// sight is a bar that is still one line, with nothing wider than the bar and the ⋮
// where it should be. Handing the magnifier its 34 pixels out of the 320px row is
// what put the Settings tab out of view, and no assertion in the steps table below
// noticed; the strip now has its own.
//
// Where each step happens is a measured width, and this test is where the
// measurement is checked: the numbers live in a comment in style.css, and a
// comment does not fail.
func TestTopBarGivesUpWordsThenControlsBeforeItsLine(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// setWidth changes the window width and waits for the bar to have been laid out at
	// it, because a width change and a press are not interchangeable in the same breath:
	// the bar closes its panel on a resize — it has to, the panel only exists below the
	// width where the controls fold, and a panel left open across that width is a panel
	// laid out as a row of the bar — so a press that lands before the resize event has
	// been through the queue is a press with nothing to press. That was a real flake
	// here, and it was the test racing a listener the bar is meant to have, not the
	// other way round.
	setWidth := func(width int) {
		t.Helper()
		if err := withTimeout(ctx, func(tctx context.Context) error {
			return chromedp.Do(tctx,
				chromedp.EmulateViewport(int64(width), 900),
				chromedp.Poll[chromedp.Void](
					fmt.Sprintf(`document.documentElement.clientWidth === %d`, width),
					chromedp.WithPollingTimeout(10*time.Second)),
			)
		}); err != nil {
			t.Fatalf("could not set the viewport to %d: %v", width, err)
		}
		// The poll reads the layout, which is settled before the resize event is: give
		// the event its turn before anything is pressed.
		time.Sleep(200 * time.Millisecond)
	}

	// Every width below is measured on the plain dashboard rather than with the
	// header being arranged, because the plain dashboard is the wider of the two:
	// arranging it takes the layout switcher out of the bar and puts the button
	// that replaces it into a pill that was already there, so the state that says
	// where the bar stops fitting is the ordinary one.
	//
	// wide is the bar with everything it can carry, and with nothing to argue
	// about: every word is on screen.
	setWidth(2400)
	shape := read[barShape](t, ctx, readBar, "the top bar at 2400")
	if !slices.Contains(shape.Words, "layout") || !slices.Contains(shape.Words, "tabs") {
		capture(t, ctx, dir, "topbar-2400")
		t.Errorf("at 2400 the bar shows %v, and it should have room for every word", shape.Words)
	}
	if shape.Rows != 1 {
		t.Errorf("at 2400 the bar takes %d rows, want 1", shape.Rows)
	}

	// The steps down, and what each is allowed to cost. `want` is the list of
	// words that must still be on screen and the ones not in it must be gone;
	// `folded` is whether the four working controls are behind the ⋮; `search` is
	// whether the search field is still in the bar at all, and `magnifier` whether
	// the button that stands in for it is. Those last two are each other's negative:
	// the field is in the bar, or the thing that brings it back is.
	steps := []struct {
		width     int
		want      []string
		folded    bool
		search    bool
		magnifier bool
		why       string
	}{
		{1900, []string{"brand", "tabs", "sensor", "live", "user"}, false, true, false,
			"the switcher's labels say what the icons beside them say, so they go first"},
		{1500, nil, false, true, false,
			"below the second width the words are the icons' job, and the icons are still there"},
		{1200, nil, false, true, false,
			"the narrowest width the bar fits on one line at is 1134; this is above it"},
		{1120, nil, true, true, false,
			"the first width at which the four controls cannot share the line; they fold " +
				"into the ⋮ and the search field takes the room they left"},
		{700, nil, true, true, false,
			"still room for a field worth typing in"},
		{620, nil, true, true, false,
			"the last width with the field in the bar"},
		{560, nil, true, false, true,
			"a field that fits here is a stub seven characters wide; the magnifier takes " +
				"its place rather than the ⋮ swallowing the field"},
		{375, nil, true, false, true,
			"an ordinary phone"},
		{320, nil, true, false, true,
			"the narrowest phone still in use"},
	}
	for _, step := range steps {
		setWidth(step.width)

		got := read[barShape](t, ctx, readBar, fmt.Sprintf("the top bar at %d", step.width))
		if got.Rows != 1 {
			capture(t, ctx, dir, fmt.Sprintf("topbar-%d", step.width))
			t.Errorf("at %d the bar takes %d rows — %s, and it should still be one line "+
				"(height %dpx)", step.width, got.Rows, step.why, got.Height)
		}
		if got.Overflow {
			capture(t, ctx, dir, fmt.Sprintf("topbar-overflow-%d", step.width))
			t.Errorf("at %d something in the bar is wider than the bar", step.width)
		}
		if !equal(got.Words, step.want) {
			capture(t, ctx, dir, fmt.Sprintf("topbar-words-%d", step.width))
			t.Errorf("at %d the bar shows %v, want %v — %s", step.width, got.Words, step.want, step.why)
		}
		if got.More != step.folded {
			capture(t, ctx, dir, fmt.Sprintf("topbar-more-%d", step.width))
			t.Errorf("at %d the ⋮ is %v, want %v — %s", step.width, got.More, step.folded, step.why)
		}
		if got.Search != step.search {
			capture(t, ctx, dir, fmt.Sprintf("topbar-search-%d", step.width))
			t.Errorf("at %d the search field is %v in the bar, want %v — %s",
				step.width, got.Search, step.search, step.why)
		}
		if got.Magnifier != step.magnifier {
			capture(t, ctx, dir, fmt.Sprintf("topbar-magnifier-%d", step.width))
			t.Errorf("at %d the magnifier is %v in the bar, want %v — %s",
				step.width, got.Magnifier, step.magnifier, step.why)
		}
		// The tab strip scrolls rather than overflows, so a tab squeezed past the edge
		// of its own strip is the one failure in this bar that nothing above would see:
		// the bar is still one line, nothing is wider than the bar, and the last tab is
		// half out of sight. This is not hypothetical — the magnifier's 34 pixels came
		// out of a 320px row and put the Settings tab out of view, and every check in
		// this loop called the bar fine.
		if got.TabsClipped {
			capture(t, ctx, dir, fmt.Sprintf("topbar-tabs-clipped-%d", step.width))
			t.Errorf("at %d the tab strip is scrolled — its tabs no longer fit the room "+
				"the bar gave them: %s", step.width, step.why)
		}
	}

	// The widths either side of the point where the tabs give up padding they cannot
	// afford. It is a one-pixel boundary in the CSS, and it is invisible in every field
	// above: the words are gone on both sides of it, the ⋮ is folded on both sides of
	// it, the magnifier is in the row on both sides of it. The only thing that tells
	// them apart is whether the strip fits, which is why it is checked on its own. At
	// 380 the row is the mark and the tabs at their 26px of side padding and the bar
	// has single-digit pixels to spare, which is the tightest the bar is anywhere; at
	// 379 the tabs hug their icons and give the 32 pixels back.
	for _, width := range []int{380, 379, 359} {
		setWidth(width)
		if got := read[barShape](t, ctx, readBar, fmt.Sprintf("the top bar at %d", width)); got.TabsClipped {
			capture(t, ctx, dir, fmt.Sprintf("topbar-tabs-boundary-%d", width))
			t.Errorf("at %d the tab strip is scrolled: the tabs are carrying more padding "+
				"than the row can pay for", width)
		}
	}

	// And the words are not the only thing carrying the meaning: at a width where
	// every one of them is gone, the tab has to still say what it is. Checked at one
	// such width rather than at all of them — the aria-label is not a thing a media
	// query touches, and the steps above have already walked every boundary that
	// could take it away.
	setWidth(1200)
	if got := read[barShape](t, ctx, readBar, "the top bar at 1200"); got.Label != "Dashboard" {
		t.Errorf("with its word hidden the dashboard tab answers to %q, want %q — "+
			"a hidden word is not read out, so the name has to be on the button", got.Label, "Dashboard")
	}

	// The icons are controls, not pictures of controls: the tab that has lost its
	// word still has to go where it went before.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#tabs .tab[data-route="settings"]`),
			chromedp.Poll[chromedp.Void](`location.hash === '#/settings'`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "topbar-icon-tab-dead")
		t.Fatalf("an icon-only tab did not change the view: %v", err)
	}

	// And back, for the same reason: the panel is measured on the dashboard, which
	// is the only route where all four controls in it are real. Two of them, the
	// layout switcher and the tools pill, are hidden by the app on every other
	// route, and a test that opened the panel over the settings page would be
	// asking the bar about elements the app had already taken away.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#tabs .tab[data-route="dashboard"]`),
			chromedp.Poll[chromedp.Void](`location.hash === '#/dashboard'`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "topbar-back-dead")
		t.Fatalf("could not get back to the dashboard: %v", err)
	}

	// The panel behind the ⋮, which is where those four controls went. Folding
	// them away is only half of it: what is behind the button has to be the whole
	// of them, with their words, on a window narrow enough that the bar could not
	// hold them — and it has to be on screen at the narrowest width of all, which
	// is the one place a menu can hang off the edge it is anchored to.
	//
	// Read by id rather than by count: four rows could be four of anything, and the
	// point is that the four that left the bar are the four that came back.
	for _, width := range []int{1120, 700, 375, 320} {
		setWidth(width)
		if err := withTimeout(ctx, func(tctx context.Context) error {
			return chromedp.Do(tctx, chromedp.Click(`#topbar-more`))
		}); err != nil {
			t.Fatalf("at %d the ⋮ could not be pressed: %v", width, err)
		}
		time.Sleep(150 * time.Millisecond)

		opened := read[barShape](t, ctx, readBar, fmt.Sprintf("the top bar at %d with the panel open", width))
		if opened.PanelOff {
			capture(t, ctx, dir, fmt.Sprintf("topbar-panel-off-%d", width))
			t.Errorf("at %d the panel runs off the window", width)
		}
		if opened.PanelRows < 4 {
			capture(t, ctx, dir, fmt.Sprintf("topbar-panel-%d", width))
			t.Errorf("at %d the panel has %d rows; the four controls behind the ⋮ "+
				"should be in it, as rows", width, opened.PanelRows)
		}
		for _, id := range []string{"layout-switcher", "widgets-tools", "new-sensor", "live-chip"} {
			if !slices.Contains(opened.PanelHas, id) {
				capture(t, ctx, dir, fmt.Sprintf("topbar-panel-has-%d", width))
				t.Errorf("at %d the panel holds %v, and #%s is not among them — "+
					"the control that left the bar has to be the one that comes back",
					width, opened.PanelHas, id)
			}
		}
		// And the words come back with them. They were dropped to buy the bar a
		// line, and a panel has no line to buy: an icon with no word in a menu is
		// a menu nobody can read.
		for _, word := range []string{"layout", "sensor", "live"} {
			if !slices.Contains(opened.Words, word) {
				capture(t, ctx, dir, fmt.Sprintf("topbar-panel-words-%d", width))
				t.Errorf("at %d the open panel shows %v, and %q is not among them — "+
					"a folded-away control has to come back with its name",
					width, opened.Words, word)
			}
		}
		if !opened.More {
			t.Errorf("at %d the ⋮ left the bar while its own panel was open", width)
		}

		// Escape closes it again, the way every other popover on the page does.
		if err := withTimeout(ctx, func(tctx context.Context) error {
			return chromedp.Do(tctx,
				chromedp.KeyEvent("\x1b"),
				chromedp.Poll[chromedp.Void](`document.querySelector('#topbar-more').getAttribute('aria-expanded') === 'false'`,
					chromedp.WithPollingTimeout(5*time.Second)),
			)
		}); err != nil {
			capture(t, ctx, dir, fmt.Sprintf("topbar-panel-escape-%d", width))
			t.Errorf("at %d Escape did not close the panel: %v", width, err)
		}
	}

	// And the field that left the bar comes back over it. Below ~620px the bar has a
	// magnifier where the field was, and pressing it lays the field over the bar at
	// the full width of it — which is the only shape a search field is worth having
	// on a phone. The magnifier is in the row rather than a row of the ⋮, which is
	// the difference between a bar that keeps its search and one that hides it in a
	// menu; the steps table above is where that distinction is pinned down, and this
	// is what the press does.
	setWidth(375)
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#topbar-search`),
			chromedp.Poll[chromedp.Void](`document.activeElement === document.querySelector('#search')`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "topbar-magnifier-dead")
		t.Fatalf("the magnifier did not bring the search field back: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	over := read[barShape](t, ctx, readBar, "the top bar at 375 with the field over it")
	if !over.Search {
		capture(t, ctx, dir, "topbar-search-overlay")
		t.Error("the search field is not on screen after the magnifier asked for it")
	}
	// There is no row count to check here, and that is the design: the field is laid
	// over the bar's own width, so the two side columns stand down while it is
	// there. What has to hold is that they are gone and the field is the width of
	// the bar, which is the pair of checks below.
	if got := read[bool](t, ctx,
		`(() => {
		  const live = (sel) => { const n = document.querySelector(sel); if (!n) return false;
		    const r = n.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
		  return live('.topbar-side') || live('.topbar-actions');
		})()`, "whether the bar's own controls are still beside the overlaid field"); got {
		capture(t, ctx, dir, "topbar-search-overlay")
		t.Error("the bar kept its controls beside the search field laid over it; the field " +
			"is the width of the bar and there is no room for both")
	}
	if got := read[float64](t, ctx, `document.querySelector('#search').getBoundingClientRect().width`,
		"the width of the search field"); got < 300 {
		t.Errorf("the field laid over a 375px bar is %.0fpx wide; it should have the bar's "+
			"own width, not a stub's", got)
	}

	// Escape puts the bar back, and the magnifier with it: the field is over the bar,
	// not a page of its own, so there has to be a way out that is not a reload.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.KeyEvent("\x1b"),
			chromedp.Poll[chromedp.Void](`!document.querySelector('.topbar').classList.contains('search-open')`,
				chromedp.WithPollingTimeout(5*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "topbar-search-escape")
		t.Fatalf("Escape did not put the bar back after the search field: %v", err)
	}
	if got := read[barShape](t, ctx, readBar, "the top bar at 375 after Escape"); !got.Magnifier || got.Search {
		t.Errorf("after Escape the bar has the field over it %v and the magnifier %v; "+
			"want the field gone and the magnifier back in the row", got.Search, got.Magnifier)
	}
}

// --- sites, and the map ----------------------------------------------------

// treeNode is one place in the tree.
type treeNode struct {
	Site string `json:"site"`
	Kind string `json:"kind"`
	Rows int    `json:"rows"`
}

const readTree = `Array.from(document.querySelectorAll('.site-node')).map((node) => {
  const name = node.querySelector('.site-name');
  const kind = node.querySelector('.kind-tag');
  return {
    site: name ? name.textContent.trim() : '',
    kind: kind ? kind.textContent.trim() : '',
    rows: node.querySelectorAll('.tree-row').length,
  };
})`

// wallGroup is one place on the wall.
type wallGroup struct {
	Site  string `json:"site"`
	Kind  string `json:"kind"`
	Tiles int    `json:"tiles"`
}

// readWall takes the place's own name from the head's first text node, not from
// its whole text: the head also carries the kind tag and the count, and reading
// it all would make "Hetzner 1 of 1 broken" and "Hetzner" look the same to a
// comparison that is meant to be about names.
const readWall = `Array.from(document.querySelectorAll('.wall-group')).map((group) => {
  const head = group.querySelector('.wall-head');
  const kind = group.querySelector('.kind-tag');
  let site = '';
  for (const node of head.childNodes) {
    if (node.nodeType === 3 && node.textContent.trim()) { site = node.textContent.trim(); break; }
  }
  return {
    site,
    kind: kind ? kind.textContent.trim() : '',
    tiles: group.querySelectorAll('.tile').length,
  };
})`

// mapMarker is one place on the map, positioned inside the canvas.
type mapMarker struct {
	Site  string `json:"site"`
	Class string `json:"class"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
}

const readMarkers = `(() => {
  const canvas = document.querySelector('.map-canvas');
  if (!canvas) return null;
  const box = canvas.getBoundingClientRect();
  return {
    w: Math.round(box.width),
    h: Math.round(box.height),
    markers: Array.from(document.querySelectorAll('.site-marker')).map((m) => {
      const r = m.getBoundingClientRect();
      return {
        site: m.dataset.site,
        class: m.className,
        x: Math.round(r.left + r.width / 2 - box.left),
        y: Math.round(r.top + r.height / 2 - box.top),
      };
    }),
  };
})()`

type mapView struct {
	W       int         `json:"w"`
	H       int         `json:"h"`
	Markers []mapMarker `json:"markers"`
}

// createSite walks the "New site" dialog the way a person does.
func createSite(t *testing.T, ctx context.Context, dir, name, kind string, lat, lon float64) {
	t.Helper()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`[data-action="site-new"]`),
			chromedp.WaitVisible(`#modal input[name="site_name"]`),
			chromedp.SetValue(`#modal input[name="site_name"]`, name),
			chromedp.SetValue(`#modal select[name="kind"]`, kind),
			chromedp.SetValue(`#modal input[name="lat"]`, fmt.Sprintf("%g", lat)),
			chromedp.SetValue(`#modal input[name="lon"]`, fmt.Sprintf("%g", lon)),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotVisible(`#modal`),
			chromedp.Poll[chromedp.Void](
				fmt.Sprintf(`!!document.querySelector('#sites-panel tr[data-name=%q]')`, name),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, dir, "site-not-created")
		t.Fatalf("could not create the site %q through the settings screen: %v\n"+
			"the dialog said: %s", name, err, text(ctx, t, "#modal-error"))
	}
}

// assignSite puts the first sensor the installation has onto a place, through
// the sensor form's own picker.
func assignSite(t *testing.T, ctx context.Context, dir, site string) {
	t.Helper()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.WaitVisible(`#groups .card`),
			chromedp.Click(`#groups .card[data-origin="example"] [data-action="edit"]`),
			chromedp.WaitVisible(`#modal select[name="site"]`),
			chromedp.SetValue(`#modal select[name="site"]`, site),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotVisible(`#modal`),
			// The write has to have landed before the views are asked about it:
			// the tree is drawn from the summary, and a summary fetched a moment
			// too early would show the old grouping and be reported as a broken
			// tree.
			chromedp.Poll[chromedp.Void](
				fmt.Sprintf(`fetch('/api/summary').then(r => r.json())`+
					`.then(d => (d.sensors || []).some(s => s.site === %q))`, site),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, dir, "site-not-assigned")
		t.Fatalf("could not put a sensor on the site %q: %v", site, err)
	}
}

// TestSitesReachEveryViewAndTheMapIsHonest walks one place from the settings
// screen to the tree, the wall and the map, and then breaks the tile server on
// purpose.
//
// The last part is the one worth explaining. A map that draws nothing when the
// tiles do not arrive is useless in exactly the installation it is meant for —
// a homelab that is offline, or behind a firewall that allows the dashboard but
// not a tile server. So the assertion is not "the map looks right"; it is that
// the places keep their correct positions relative to each other and one
// sentence says what is missing.
func TestSitesReachEveryViewAndTheMapIsHonest(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// --- two places, made on the settings screen ---------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			chromedp.Poll[chromedp.Void](`document.querySelector('#sites-panel') !== null`,
				chromedp.WithPollingTimeout(15*time.Second)),
			// The page shows one category at a time, and the sites panel is not
			// in the one a fresh visit opens. The dialog below is clicked from
			// outside, which needs the button on screen rather than merely in the
			// document.
			openSettingsCategory("monitor"),
		)
	}); err != nil {
		capture(t, ctx, dir, "sites-panel-missing")
		t.Fatalf("the settings screen has no sites panel: %v", err)
	}

	// One at a provider, one in the house, and a good distance apart: the map
	// check below is about which is east of which, and two places in the same
	// street would make that assertion meaningless.
	createSite(t, ctx, dir, siteLocal, "local", 48.1372, 11.5756)
	createSite(t, ctx, dir, siteExternal, "external", 50.1109, 8.6821)

	// --- one sensor is put on one of them -----------------------------------

	assignSite(t, ctx, dir, siteExternal)

	// --- the tree gathers by place, then by group ---------------------------

	clickLayout(t, ctx, dir, "tree", `.site-node`)
	nodes := read[[]treeNode](t, ctx, readTree, "the tree")
	external := findNode(nodes, siteExternal)
	switch {
	case external == nil:
		capture(t, ctx, dir, "tree-missing-site")
		t.Errorf("the tree does not show the site %q; it shows %v", siteExternal, siteNames(nodes))
	case external.Kind != "External":
		t.Errorf("the tree calls %q %q, want External", siteExternal, external.Kind)
	case external.Rows != 1:
		t.Errorf("the tree shows %d sensor(s) under %q, want the one that was put there",
			external.Rows, siteExternal)
	}
	// The sensors nobody gave a place to are not hidden and not an error: they
	// are gathered under a heading of their own, which is where they all are on
	// an installation that has never heard of places.
	homeless := findNode(nodes, "No site")
	if homeless == nil {
		capture(t, ctx, dir, "tree-lost-the-unplaced")
		t.Errorf("the tree has no heading for the sensors without a site; it shows %v", siteNames(nodes))
	} else if homeless.Rows != 3 {
		t.Errorf("the \"No site\" heading holds %d sensor(s), want the three examples that were "+
			"not given a place", homeless.Rows)
	}
	// The second site has nothing on it, so it is not a heading: a place with no
	// sensors is a line of noise, not information.
	if findNode(nodes, siteLocal) != nil {
		t.Errorf("the tree shows %q, which has no sensors on it; empty places are left out",
			siteLocal)
	}

	// --- the wall does the same, with tiles ---------------------------------

	clickLayout(t, ctx, dir, "wall", `.wall-group .tile`)
	groups := read[[]wallGroup](t, ctx, readWall, "the wall")
	onWall := findGroup(groups, siteExternal)
	switch {
	case onWall == nil:
		capture(t, ctx, dir, "wall-missing-site")
		t.Errorf("the wall does not show the site %q; it shows %v", siteExternal, groupNames(groups))
	case onWall.Kind != "External":
		t.Errorf("the wall calls %q %q, want External", siteExternal, onWall.Kind)
	case onWall.Tiles != 1:
		t.Errorf("the wall shows %d tile(s) under %q, want the one sensor that was put there",
			onWall.Tiles, siteExternal)
	}
	if len(groups) != 2 {
		t.Errorf("the wall is in %d section(s), want two: the site and the sensors without one",
			len(groups))
	}

	// --- the map draws both places, at the right places ---------------------

	clickLayout(t, ctx, dir, "map", `.map-canvas`)
	before := readMap(t, ctx, dir)

	// --- and stays honest with the tile server broken -----------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			// The sites phase above left the page on Monitoring, and the page
			// remembers which category it was on. The map panel is in Look, so it
			// has to be chosen back before its field can be typed into.
			openSettingsCategory("look"),
			chromedp.WaitVisible(`#map-panel [name="tile_url"]`),
			chromedp.SetValue(`#map-panel [name="tile_url"]`, deadTileURL),
			chromedp.Click(`#map-panel [data-action="map-save"]`),
			chromedp.Poll[chromedp.Void](
				fmt.Sprintf(`fetch('/api/settings').then(r => r.json())`+
					`.then(s => !!(s.map && s.map.tile_url === %q))`, deadTileURL),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "tile-source-not-saved")
		t.Fatalf("could not point the map at an unreachable tile server: %v", err)
	}

	clickLayout(t, ctx, dir, "map", `.map-canvas`)
	eventually(t, ctx, `(() => {
		const note = document.querySelector('.map-note');
		return !!note && !note.hidden && note.textContent.trim().length > 0;
	})()`,
		"no tile could be fetched and the map did not say so; an empty grey rectangle "+
			"with no explanation is the failure this guards against",
		dir, "map-silent-when-offline", 30*time.Second)

	after := readMap(t, ctx, dir)

	// The places are still there, and still in the same positions. That is the
	// whole claim: the projection does not need the tiles, so the picture is
	// correct and only the ground under it is missing.
	if len(after.Markers) != 2 {
		capture(t, ctx, dir, "markers-lost-with-tiles")
		t.Errorf("with the tile server unreachable the map drew %d place(s), want both: "+
			"the markers must not depend on the tiles", len(after.Markers))
	}
	for _, marker := range after.Markers {
		was := findMarker(before.Markers, marker.Site)
		if was == nil {
			t.Errorf("the map lost %q when the tiles stopped arriving", marker.Site)
			continue
		}
		if marker.X != was.X || marker.Y != was.Y {
			t.Errorf("%q moved from (%d,%d) to (%d,%d) when the tile server went away",
				marker.Site, was.X, was.Y, marker.X, marker.Y)
		}
	}
}

// readAsync reads a value the page has to go and fetch before it can be read.
//
// Separate from read because chromedp waits for a promise only when it is asked to,
// and the plain helper does not ask. chromedp.Poll asks on its own — which is why
// the polls in this file can be handed a fetch — while Evaluate does not, and a
// fetch read through it comes back as an empty object and fails to unmarshal as the
// list that was expected.
func readAsync[T any](t *testing.T, ctx context.Context, expr, what string) T {
	t.Helper()
	value, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr, chromedp.EvalAwaitPromise))
	if err != nil {
		t.Fatalf("could not read %s: %v", what, err)
	}
	return value
}

// serverPlace is one place as the server holds it: enough of it to tell one from
// another and to tell what kind it is.
type serverPlace struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// readPlacesOnTheServer asks the API what places the installation has.
//
// Through the API rather than off the screen: what is under test is whether a place
// exists, and a screen that has not been redrawn yet would answer about the drawing
// instead. It is a promise, so chromedp waits for it — the expression returns
// `d.sites` and not the fetch that would have produced it.
const readPlacesOnTheServer = `fetch('/api/sites').then(r => r.json()).then(d => d.sites || [])`

// readWhereTheSensorStands asks the API where one sensor stands, by name.
//
// The name is part of the expression rather than a parameter of a helper, because
// chromedp's options are variadic and an explicitly passed nil in that variadic
// position panics inside chromedp rather than failing here.
func readWhereTheSensorStands(name string) string {
	return fmt.Sprintf(`fetch('/api/summary').then(r => r.json())`+
		`.then(d => (d.sensors || []).filter(s => s.name === %s).map(s => s.site || ''))`,
		jsQuote(name))
}

// openNewSensorForm clicks the one button in the bar that makes something and waits
// for the form it opens.
func openNewSensorForm(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.WaitVisible(`#new-sensor`),
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal select[name="site"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "sensor-form-missing")
		t.Fatalf("the sensor form did not open: %v", err)
	}
}

// pickTheNewSiteEntry chooses the last entry in the sensor form's place list, which
// is the one that makes a place.
//
// By position rather than by value: the value is an internal marker, and a test that
// hard-coded it would go on passing after the marker was renamed while the entry it
// stands for had stopped being the last one. What the entry is called is read back
// separately in the test, because an entry that does not say what it does is a
// button nobody presses.
//
// And by hand rather than with chromedp.SetValue. SetValue assigns `.value` in
// JavaScript and dispatches no event — the note over clearSearch in search_test.go
// says the same thing about the search box — while the fields that make a place are
// brought up by a change event. With SetValue they would stay hidden and the test
// would be measuring one way of setting a field instead of the form.
var pickTheNewSiteEntry = chromedp.Evaluate[chromedp.Void](`(() => {
  const el = document.querySelector('#modal select[name="site"]');
  el.value = el.options[el.options.length - 1].value;
  el.dispatchEvent(new Event('change', { bubbles: true }));
})()`)

// siteFieldsHidden says whether the two fields that make a place are off screen.
//
// `hidden` and not a computed style: the form shows them by clearing the attribute,
// which is also what keeps them out of the accessibility tree while they are down.
const siteFieldsHidden = `document.querySelector('#sensor-new-site').hidden`

// TestASiteCanBeMadeWhileTheSensorIsMade is about where the two are made.
//
// A sensor is often the moment somebody first knows where it stands: the rack goes
// in and the thing on it is written down in the same minute. The place picker used
// to offer only places that already existed, so the choice was to leave a
// half-filled sensor form, go to Settings, make the place and come back — or to give
// up on the place. Its last entry now makes one instead.
//
// The two things checked here are the two an API test cannot see.
//
// The first is that the fields a place needs are off screen until the entry that
// makes one is chosen. A name field for a place nobody asked for is a field that
// gets filled in by mistake, and the entry that reveals it is a claim about what it
// does — so it has to be called something that says so.
//
// The second is *when* the place is made: on save, not on choosing the entry. That
// is what the first half of this test is for — it abandons a form and then looks for
// what was left behind. An implementation that wrote the place the moment the entry
// was picked would pass the happy path just as well, and would leave an empty place
// behind every time somebody opened the form and thought better of it.
func TestASiteCanBeMadeWhileTheSensorIsMade(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	const (
		abandoned = "Werkstatt"
		place     = "Dachboden"
		sensor    = "Dachboden Thermometer"
	)

	// --- a form filled in and closed leaves nothing behind ------------------

	openNewSensorForm(t, ctx, dir)

	// The sensor's own required fields are filled in first, because a form the
	// browser refuses to submit never reaches the code that would say what is wrong
	// with the place — which is exactly how the Test button came to look dead.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#modal input[name="name"]`, sensor),
			// The default kind is HTTP and wants a URL. The discard port is
			// deliberate: saving a sensor does not probe it, and a target that
			// answered would tie this test to something being up.
			chromedp.SetValue(`#modal input[name="target"]`, "http://127.0.0.1:9/"),
		)
	}); err != nil {
		capture(t, ctx, dir, "sensor-form-not-filled")
		t.Fatalf("could not fill in the sensor form: %v", err)
	}

	if hidden := read[bool](t, ctx, siteFieldsHidden, "the fields that make a place"); !hidden {
		capture(t, ctx, dir, "new-site-fields-too-early")
		t.Errorf("the fields that make a place are on screen before the entry that makes " +
			"one was chosen")
	}

	labels := read[[]string](t, ctx,
		`Array.from(document.querySelectorAll('#modal select[name="site"] option')).map(o => o.textContent.trim())`,
		"the entries in the place picker")
	if len(labels) == 0 {
		t.Fatalf("the sensor form has no entries in its place picker at all")
	}
	if last := labels[len(labels)-1]; last != "New site…" {
		capture(t, ctx, dir, "new-site-entry-unnamed")
		t.Errorf("the last entry in the place picker says %q, want \"New site…\". It is the "+
			"only way to a place that does not exist yet, so it has to say what it does; "+
			"the entries are %v", last, labels)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, pickTheNewSiteEntry)
	}); err != nil {
		capture(t, ctx, dir, "new-site-entry-not-picked")
		t.Fatalf("could not choose the place-making entry: %v", err)
	}

	if hidden := read[bool](t, ctx, siteFieldsHidden, "the fields that make a place"); hidden {
		capture(t, ctx, dir, "new-site-fields-stayed-down")
		t.Errorf("choosing \"New site…\" did not bring up the fields that make one, so there " +
			"is no way to say what the place is called")
	}

	// Saving with the place unnamed is refused, and says which name is missing.
	// Named by the place and not by the sensor: the sensor has a name, and a message
	// about the wrong field would send somebody to fill in a field they already
	// filled in.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Click(`#modal-save`))
	}); err != nil {
		t.Fatalf("could not press Save: %v", err)
	}
	eventually(t, ctx, `!!document.querySelector('#modal-error').textContent.trim()`,
		"the form was saved with a place that has no name and said nothing about it",
		dir, "new-site-unnamed-silent", 10*time.Second)

	if msg := text(ctx, t, "#modal-error"); !strings.Contains(strings.ToLower(msg), "site") {
		capture(t, ctx, dir, "new-site-unnamed-wrong-reason")
		t.Errorf("saving a sensor with a nameless place is refused with %q, which does not "+
			"say that the place is what is missing", msg)
	}
	if visible := read[bool](t, ctx, `document.querySelector('#modal').open`, "the form"); !visible {
		capture(t, ctx, dir, "new-site-unnamed-closed")
		t.Errorf("the form closed although the place it was making had no name, so the " +
			"sensor was not saved and nothing says so")
	}

	// Now the form is filled in properly and then abandoned. Both the refusal above
	// and this closing are meant to leave no place behind, and the check below is
	// after both of them.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#modal input[name="site_name"]`, abandoned),
			chromedp.Click(`#modal-cancel`),
			chromedp.WaitNotVisible(`#modal`),
		)
	}); err != nil {
		capture(t, ctx, dir, "sensor-form-would-not-close")
		t.Fatalf("could not close the sensor form: %v", err)
	}

	// Looking for something that is *not* there needs a moment of stillness: a write
	// started by the click above would still be in flight and would answer the
	// question with the state before it landed. Three quarters of a second is far
	// longer than a request to a server on the same host, and far shorter than
	// anything a person would sit and watch.
	time.Sleep(750 * time.Millisecond)

	if places := readAsync[[]serverPlace](t, ctx, readPlacesOnTheServer, "the places on the server"); contains(placeNames(places), abandoned) {
		capture(t, ctx, dir, "abandoned-form-made-a-place")
		t.Errorf("a sensor form closed without saving left the place %q behind: the places "+
			"are now %v. A place is meant to be made when the sensor is saved, not when the "+
			"entry was chosen — otherwise every form opened and thought better of leaves an "+
			"empty place behind, and nobody ever asks for one", abandoned, placeNames(places))
	}

	// --- a place made while the sensor is made ------------------------------

	openNewSensorForm(t, ctx, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SetValue(`#modal input[name="name"]`, sensor),
			chromedp.SetValue(`#modal input[name="target"]`, "http://127.0.0.1:9/"),
			pickTheNewSiteEntry,
			chromedp.SetValue(`#modal input[name="site_name"]`, place),
			chromedp.SetValue(`#modal select[name="site_kind"]`, "external"),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotVisible(`#modal`),
			// Both writes have to have landed before they are asked about: the
			// sensor is saved after the place it names, and a summary fetched a
			// moment too early would show the sensor still without one.
			chromedp.Poll[chromedp.Void](
				fmt.Sprintf(`fetch('/api/sites').then(r => r.json())`+
					`.then(d => (d.sites || []).some(s => s.name === %s))`, jsQuote(place)),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "sensor-with-new-site-not-saved")
		t.Fatalf("could not make a sensor and its place in one go: %v\nthe form said: %s",
			err, text(ctx, t, "#modal-error"))
	}

	places := readAsync[[]serverPlace](t, ctx, readPlacesOnTheServer, "the places on the server")
	made := 0
	var kind string
	for _, p := range places {
		if p.Name == place {
			made++
			kind = p.Kind
		}
	}
	switch {
	case made == 0:
		capture(t, ctx, dir, "made-place-missing")
		t.Errorf("the sensor was saved and the place it named was not made with it: the "+
			"places are %v", placeNames(places))
	case made > 1:
		capture(t, ctx, dir, "made-place-twice")
		t.Errorf("the place %q exists %d times; saving one sensor makes one place, even "+
			"when the picker was used more than once", place, made)
	case kind != "external":
		capture(t, ctx, dir, "made-place-kind")
		t.Errorf("the place %q is %q, want external — the kind was chosen in the form and "+
			"the form is the only place it was said", place, kind)
	}
	if contains(placeNames(places), abandoned) {
		t.Errorf("the abandoned name %q became a place after all: the places are %v",
			abandoned, placeNames(places))
	}

	where := readAsync[[]string](t, ctx, readWhereTheSensorStands(sensor), "where the sensor stands")
	switch {
	case len(where) != 1:
		capture(t, ctx, dir, "made-sensor-missing")
		t.Errorf("the summary holds %d sensor(s) named %q, want the one this test made",
			len(where), sensor)
	case where[0] != place:
		capture(t, ctx, dir, "made-sensor-unplaced")
		t.Errorf("the sensor the form made stands on %q, want %q — the place it made in "+
			"the same breath is where it has to end up", where[0], place)
	}
}

// placeNames is the names of the places, for a failure message that says what the
// installation holds rather than just that what was looked for was not in it.
func placeNames(places []serverPlace) []string {
	names := make([]string, 0, len(places))
	for _, p := range places {
		names = append(names, p.Name)
	}
	return names
}

// readMap reads the markers, and fails with the reason if there is no map at
// all rather than with a nil-pointer panic three lines later.
func readMap(t *testing.T, ctx context.Context, dir string) mapView {
	t.Helper()
	view := read[mapView](t, ctx, readMarkers, "the map")
	if view.W == 0 || view.H == 0 {
		capture(t, ctx, dir, "map-has-no-size")
		t.Fatalf("the map has no box on screen (%dx%d)", view.W, view.H)
	}
	if len(view.Markers) != 2 {
		capture(t, ctx, dir, "map-missing-sites")
		t.Fatalf("the map drew %d place(s), want the two that were created: %v",
			len(view.Markers), markerNames(view.Markers))
	}

	external := findMarker(view.Markers, siteExternal)
	local := findMarker(view.Markers, siteLocal)
	if external == nil || local == nil {
		capture(t, ctx, dir, "map-missing-sites")
		t.Fatalf("the map drew %v, want %q and %q", markerNames(view.Markers), siteExternal, siteLocal)
	}

	// The kind of place is carried by the class as well as by the shape, so the
	// two are distinguishable without relying on colour.
	if !hasClass(external.Class, "k-external") {
		t.Errorf("the marker for %q is classed %q, want it to be the external kind",
			siteExternal, external.Class)
	}
	if !hasClass(local.Class, "k-local") {
		t.Errorf("the marker for %q is classed %q, want it to be the local kind",
			siteLocal, local.Class)
	}

	// Within the canvas: a marker drawn outside it is a place nobody can see and
	// a click nobody can make.
	for _, marker := range view.Markers {
		if marker.X < 0 || marker.X > view.W || marker.Y < 0 || marker.Y > view.H {
			t.Errorf("the marker for %q is at (%d,%d) in a %dx%d canvas, so it is off the map",
				marker.Site, marker.X, marker.Y, view.W, view.H)
		}
	}

	// The projection, checked the only way it can be from outside: the places
	// are two degrees apart in each direction, and east of one is east of the
	// other on screen too, with the more southerly one further down.
	//
	// The local site is east of and south of the external one, so it has to be
	// further right and further down. If the axes were swapped, transposed or
	// flipped, this is what notices.
	if local.X <= external.X {
		t.Errorf("%q is east of %q but is drawn at x=%d against x=%d",
			siteLocal, siteExternal, local.X, external.X)
	}
	if local.Y <= external.Y {
		t.Errorf("%q is south of %q but is drawn at y=%d against y=%d",
			siteLocal, siteExternal, local.Y, external.Y)
	}
	return view
}

// --- small helpers ---------------------------------------------------------

func hasClass(classes, want string) bool {
	return slices.Contains(strings.Fields(classes), want)
}

func findNode(nodes []treeNode, site string) *treeNode {
	for i := range nodes {
		if nodes[i].Site == site {
			return &nodes[i]
		}
	}
	return nil
}

func siteNames(nodes []treeNode) []string {
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		out = append(out, node.Site)
	}
	return out
}

func findGroup(groups []wallGroup, site string) *wallGroup {
	for i := range groups {
		if groups[i].Site == site {
			return &groups[i]
		}
	}
	return nil
}

func groupNames(groups []wallGroup) []string {
	out := make([]string, 0, len(groups))
	for _, group := range groups {
		out = append(out, group.Site)
	}
	return out
}

func findMarker(markers []mapMarker, site string) *mapMarker {
	for i := range markers {
		if markers[i].Site == site {
			return &markers[i]
		}
	}
	return nil
}

func markerNames(markers []mapMarker) []string {
	out := make([]string, 0, len(markers))
	for _, marker := range markers {
		out = append(out, marker.Site)
	}
	return out
}
