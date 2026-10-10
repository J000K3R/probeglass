// Package e2e — the box in the top bar.
//
// Two things about the search can only be wrong in a rendered page, and both of
// them are why this file exists rather than another API test.
//
// The first is that a hit is a **link**. The store test proves the right rows come
// back and the API test proves the right rows are allowed out; neither can say
// whether clicking one lands anywhere, and "the search finds it and clicking does
// nothing" is a search that looks like it works for as long as it takes somebody to
// try it.
//
// The second is where the field **is**. It was called "Search sensors" and pinned to
// the right-hand end of the bar, and it was asked to become a plain "Search" in the
// middle. Only a layout measurement can hold that, and it has to be taken at two
// window widths: the bar deliberately falls back to a two-row arrangement below 1360
// pixels, and the two arrangements are different answers to the same question.
//
// Run it with:
//
//	cd e2e && go test ./... -run 'Search' -v
package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// clickSearchHit clicks the row whose title is exactly `title`, and fails loudly
// rather than silently doing nothing when there is no such row.
//
// By title rather than by index: the list is grouped by kind, and an index written
// into this file would be a fact about today's ordering that the next kind added
// would quietly invalidate.
func clickSearchHit(title string) string {
	return `(() => {
  const hit = [...document.querySelectorAll('#search-results .search-hit')]
    .find(b => b.querySelector('.search-hit-title')?.textContent.trim() === ` + jsQuote(title) + `);
  if (!hit) throw new Error('no hit titled ' + ` + jsQuote(title) + `);
  hit.click();
})()`
}

// titlesAre is the predicate "the list offers a row with exactly this title".
func titlesAre(title string) string {
	return `(() => {
	  const rows = [...document.querySelectorAll('#search-results .search-hit-title')];
	  return rows.some(r => r.textContent.trim() === ` + jsQuote(title) + `);
	})()`
}

// clearSearch empties the field and tells the module it changed, so that a second
// search in a test types into a fresh box rather than onto the end of the first query.
//
// chromedp.SetValue will not do: it writes `.value` through a JavaScript assignment
// and dispatches no event, so the field would look emptied while the module went on
// holding the query it had, and the second search would be the first one again. The
// input event is the whole of what this needs to be.
var clearSearch = chromedp.Evaluate[chromedp.Void](`(() => {
  const el = document.querySelector('#search');
  el.value = '';
  el.dispatchEvent(new Event('input', { bubbles: true }));
})()`)

// typeInSearch puts a query in the box: empty it, then type.
//
// chromedp.Steps rather than a wrapper that calls chromedp.Run: running one action from
// inside another on the same target waits for a target the outer action is already
// holding, which is a deadlock and not a convenience.
func typeInSearch(text string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(clearSearch, chromedp.SendKeys(`#search`, text))
}

func TestSearchFindsASettingAndASensor(t *testing.T) {
	browser := findBrowser(t)
	bin := buildBinary(t)
	base := startServer(t, bin)

	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// A sensor with a name nobody else has, so that the only possible hit for the
	// second query is the one this test put there.
	const sensorName = "Zonk Router"
	createSensorThroughTheAPI(t, ctx, dir, sensorName, "ping", "127.0.0.1")
	eventually(t, ctx, cardExists(sensorName),
		"the sensor was created but never reached the dashboard",
		dir, "search-sensor-missing", 20*time.Second)

	// --- a setting ---------------------------------------------------------
	//
	// "backup" matches no sensor, no place, no channel and no account: it is a panel
	// of the settings page, and settings are the one kind of hit the server knows
	// nothing about — the answer comes from a table inside search.js. So this is the
	// case that proves the browser's own half of the list is searched at all.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, typeInSearch("backup"))
	}); err != nil {
		capture(t, ctx, dir, "search-type")
		t.Fatalf("could not type into the search field: %v", err)
	}

	eventually(t, ctx, titlesAre("Backup and restore"),
		"typing \"backup\" did not offer the Backup and restore panel",
		dir, "search-setting-lost", 15*time.Second)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](clickSearchHit("Backup and restore")),
			chromedp.WaitVisible(`[data-panel="backup"]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "search-setting-jump")
		t.Fatalf("clicking the Backup and restore hit did not land on the settings "+
			"panel it names: %v", err)
	}

	// --- a sensor ----------------------------------------------------------
	//
	// The other half of the list, opened with the keyboard rather than the mouse,
	// because Enter is what the box is for: type three letters, press it, be there.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, typeInSearch(sensorName))
	}); err != nil {
		capture(t, ctx, dir, "search-sensor-type")
		t.Fatalf("could not search for the sensor: %v", err)
	}

	eventually(t, ctx, titlesAre(sensorName),
		"searching for the sensor's exact name did not offer it",
		dir, "search-sensor-lost", 15*time.Second)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.KeyEvent("\r"))
	}); err != nil {
		t.Fatalf("could not press Enter: %v", err)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.WaitVisible(`#detail[open]`))
	}); err != nil {
		capture(t, ctx, dir, "search-sensor-open")
		t.Fatalf("pressing Enter on the sensor hit did not open its window: %v", err)
	}
	if got := text(ctx, t, "#detail-title"); got != sensorName {
		capture(t, ctx, dir, "search-sensor-wrong")
		t.Errorf("the window that opened is titled %q, want %q", got, sensorName)
	}
}

// searchBar is the top bar and the field in it, in CSS pixels.
//
// The bar's own rectangle is measured rather than the window's, and that is not
// pedantry: a classic scrollbar takes its width out of the layout, so "the middle of
// the window" and "the middle of the bar" differ by half a scrollbar on the machines
// that have them and not on the ones that do not. The bar is what the field is
// centred in, so the bar is what it is compared against.
type searchBar struct {
	BarL    float64 `json:"barL"`
	BarR    float64 `json:"barR"`
	SideL   float64 `json:"sideL"` // width of the left column
	SideR   float64 `json:"sideR"` // width of the right column
	SearchL float64 `json:"searchL"`
	SearchR float64 `json:"searchR"`
	SearchT float64 `json:"searchT"`
	BrandT  float64 `json:"brandT"`
	BrandB  float64 `json:"brandB"`
	Label   string  `json:"label"`
}

const readSearchBar = `(() => {
  const bar = document.querySelector('.topbar');
  const field = document.querySelector('#search');
  const sides = [...bar.querySelectorAll(':scope > .topbar-side')];
  if (!bar || !field || sides.length !== 2) return null;
  // The brand in the bar, not the one on the sign-in screen: the class is on that
  // screen too, and it comes first in the document, so a document-wide lookup
  // reads a hidden element and answers zero. It did, for as long as this helper
  // has existed, which is why the check that used it never once fired.
  const brand = bar.querySelector('.brand');
  if (!brand) return null;
  const f = field.getBoundingClientRect();
  const b = brand.getBoundingClientRect();
  const bar2 = bar.getBoundingClientRect();
  return {
    barL: bar2.left, barR: bar2.right,
    sideL: sides[0].getBoundingClientRect().width,
    sideR: sides[1].getBoundingClientRect().width,
    searchL: f.left, searchR: f.right, searchT: f.top,
    brandT: b.top, brandB: b.bottom,
    label: field.getAttribute('placeholder') || '',
  };
})()`

// TestTheSearchFieldIsAPlainSearchInTheMiddle holds the two things that were asked
// for about the field itself: the word on it is "Search" rather than "Search sensors",
// and on a wide window it sits in the middle of the bar rather than at the end of the
// buttons.
//
// Each width is held to what it is meant to be rather than to one rule. At 1440 the
// bar has room for three columns and the field takes the middle one, as a fixed
// 240 pixels; at 1100 the bar is one line still, but a line it only holds because the
// four working controls have folded into the ⋮ beside the field — and the room they
// gave back is the field's, which is what makes it 736 pixels there rather than the
// 240 it is at 1440. Below about 620 there is no field worth having left, and it
// leaves the row altogether; shell_test.go is where that is checked, along with the
// ⋮ row that lays it back over the bar at the full width.
//
// 1100 rather than a laptop's 1280, because the fold is what this half is about, and
// the fold happens at 1133.
func TestTheSearchFieldIsAPlainSearchInTheMiddle(t *testing.T) {
	browser := findBrowser(t)
	bin := buildBinary(t)
	base := startServer(t, bin)

	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	wide, err := measureSearchBar(t, ctx, dir, 1440, 900)
	if err != nil {
		t.Fatalf("could not measure the top bar at 1440: %v", err)
	}

	if wide.Label != "Search" {
		capture(t, ctx, dir, "search-label")
		t.Errorf("the field says %q, want \"Search\" — it used to promise sensors and "+
			"sensors only, and the promise is what was wrong", wide.Label)
	}

	barCentre := wide.BarL + (wide.BarR-wide.BarL)/2
	fieldCentre := wide.SearchL + (wide.SearchR-wide.SearchL)/2
	if delta := fieldCentre - barCentre; delta > 2 || delta < -2 {
		capture(t, ctx, dir, "search-off-centre")
		t.Errorf("at %g pixels wide the field's centre is %g and the bar's is %g — %g "+
			"pixels off. The bar is three columns with the field in the middle one",
			wide.BarR-wide.BarL, fieldCentre, barCentre, delta)
	}
	// The mechanism behind that, checked separately because the result can be right
	// by accident: the two outer columns take equal shares of what is left over, and
	// it is that equality — not the field's own width — that puts it in the middle.
	if d := wide.SideL - wide.SideR; d > 1 || d < -1 {
		t.Errorf("the bar's outer columns are %g and %g pixels wide; they are meant to "+
			"take equal shares, which is the only reason the field between them is "+
			"centred", wide.SideL, wide.SideR)
	}
	// And it is a column rather than the whole bar, which is what stops "its centre
	// is the bar's centre" from being true of a field that simply fills everything.
	if wide.SearchR-wide.SearchL > (wide.BarR-wide.BarL)/2 {
		t.Errorf("the field is %g pixels of a %g pixel bar; it is meant to be the "+
			"middle column, not most of the width",
			wide.SearchR-wide.SearchL, wide.BarR-wide.BarL)
	}

	narrow, err := measureSearchBar(t, ctx, dir, 1100, 900)
	if err != nil {
		t.Fatalf("could not measure the top bar at 1100: %v", err)
	}
	if narrow.Label != "Search" {
		t.Errorf("at 1100 the field says %q, want \"Search\"", narrow.Label)
	}
	// The fold is what this half is about, and it is visible in the field's own
	// width: the controls that left the row are the pixels the field gained. If a
	// later change makes the field shrink when the bar is narrow instead of growing,
	// the fold gave its room to something else.
	narrowW := narrow.SearchR - narrow.SearchL
	if narrowW <= wide.SearchR-wide.SearchL {
		t.Errorf("the field is %g pixels at 1100 and %g at 1440 — with the four "+
			"controls folded into the ⋮ at 1100 the field is meant to have their room, "+
			"not less than the fixed column it is on a wide window",
			narrowW, wide.SearchR-wide.SearchL)
	}
	// And it is a field, not a stub: at 1100 it is 736 of the bar's 1100 pixels.
	if narrowW < 0.5*(narrow.BarR-narrow.BarL) {
		capture(t, ctx, dir, "search-narrow-squeezed")
		t.Errorf("at %g pixels wide the field is %g pixels, less than half the bar; the "+
			"fold is meant to leave it a field worth typing in",
			narrow.BarR-narrow.BarL, narrowW)
	}
	// It shares the one line rather than having a row of its own: its middle sits on
	// the same line as the brand's. Two elements on the same line have centres within
	// a couple of pixels of each other however differently they are sized; one above
	// the other cannot, and that is the difference between 62 pixels of bar and 96.
	fieldMid := narrow.SearchT + 18 // the field is 36 pixels tall, and its own box gives
	brandMid := narrow.BrandT + (narrow.BrandB-narrow.BrandT)/2
	if d := fieldMid - brandMid; d > 4 || d < -4 {
		capture(t, ctx, dir, "search-narrow-own-row")
		t.Errorf("at %g pixels wide the field's middle is at %g and the brand's at %g — "+
			"%g pixels apart, which is two rows. Below 1134 the field used to take a row "+
			"of its own; it is meant to hold the one line now",
			narrow.BarR-narrow.BarL, fieldMid, brandMid, d)
	}
	// The last width where that still holds, and the first where it does not. 620 is
	// the edge: the field is 264 pixels there, which is a field. At 600 it is not in
	// the bar at all — the bar gave it up rather than squeeze it to seven characters,
	// and the ⋮ has it instead.
	if edge, err := measureSearchBar(t, ctx, dir, 620, 900); err != nil {
		t.Fatalf("could not measure the top bar at 620: %v", err)
	} else if got := edge.SearchR - edge.SearchL; got < 240 {
		capture(t, ctx, dir, "search-620")
		t.Errorf("at 620 the field is %g pixels; that is the last width it is in the bar, "+
			"and it is meant to be a field there", got)
	}
	if gone, err := measureSearchBar(t, ctx, dir, 600, 900); err != nil {
		t.Fatalf("could not measure the top bar at 600: %v", err)
	} else if got := gone.SearchR - gone.SearchL; got != 0 {
		capture(t, ctx, dir, "search-600")
		t.Errorf("at 600 the field is still %g pixels wide in the bar; below 620 it is "+
			"meant to be out of the row and behind the ⋮, laid over the bar at full "+
			"width when it is asked for", got)
	}
}

// measureSearchBar resizes the window and reads the bar back.
//
// The reading is polled rather than taken once. A resize is answered by the browser
// over a frame or two, and a measurement taken in the same tick as the request
// describes the layout of the previous width — which at the breakpoint is a different
// arrangement, so it would not be a rounding error but the wrong answer. The poll runs
// on an interval rather than on animation frames because a headless browser is free to
// throttle a page it does not consider visible.
func measureSearchBar(t *testing.T, ctx context.Context, dir string, w, h int) (searchBar, error) {
	t.Helper()
	var out searchBar
	err := withTimeout(ctx, func(tctx context.Context) error {
		if err := chromedp.Do(tctx, chromedp.EmulateViewport(int64(w), int64(h))); err != nil {
			return err
		}
		// The predicate answers null until the layout has caught up with the width
		// that was asked for, so the poll keeps going instead of returning a bar
		// measured at the old size.
		// readSearchBar is already an invoked expression, so it is embedded as it is
		// rather than called again.
		expr := `(() => {
		  const r = ` + readSearchBar + `;
		  if (!r) return null;
		  return Math.round(r.barR - r.barL) === document.documentElement.clientWidth
		    ? r : null;
		})()`
		var err error
		out, err = chromedp.Run(tctx, chromedp.Poll[searchBar](expr,
			chromedp.WithPollingInterval(50*time.Millisecond),
			chromedp.WithPollingTimeout(10*time.Second)))
		return err
	})
	if err != nil {
		capture(t, ctx, dir, "search-bar-measure")
		return out, err
	}
	return out, nil
}
