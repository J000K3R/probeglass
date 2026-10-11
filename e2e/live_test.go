// Package e2e — the dashboard that follows the probes as they happen.
//
// The live board is a thing the API cannot be asked about. Every endpoint it
// uses answers correctly whether or not the page is listening to the stream: a
// board that never updates and a board that updates are told apart only by
// looking at it while a probe lands.
//
// The two failures this file exists for are opposite, and both are quiet:
//
//   - A board that redraws itself on every probe. The sensors are correct in
//     both cases; what is lost is the shape of the page around them. Collapsed
//     places spring open, the scroll jumps, the window somebody is reading is
//     rebuilt — once a minute, forever. Nothing about that is visible to a
//     request-level test, and it is worse than a board that stands still.
//
//   - A board that stops updating and does not say so. That is why the switch is
//     checked on the server as well as on the screen: "off" has to mean the
//     request is gone, not that the page has decided to ignore what arrives.
//
// Run them with:
//
//	cd e2e && go test ./... -run 'Live' -v
package e2e

import (
	"context"
	"fmt"
	"net"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// testSensorPrefix is part of every sensor these tests make, so that a failure
// screenshot can be read without wondering which of the names on it was the
// one under test.
const testSensorPrefix = "live-test "

// TestLiveUpdatesPatchTheBoardWithoutAReload watches one probe land on an open
// dashboard and checks what moved and what did not.
//
// What moved: the state of the one sensor that changed, and the counts in the
// header beside it. What must not move: the page itself. The places are collapsed
// before the probe is let through, and they have to still be collapsed after it —
// a board that draws itself again from the top satisfies every claim about the
// data while being the thing nobody wants to use.
//
// The sensor is one whose answer the test holds: a TCP port on the loopback
// interface, with this test as the process listening on it. Nothing else can
// decide when it starts failing, which is what makes the assertion about the
// moment of the change rather than about a minute later.
func TestLiveUpdatesPatchTheBoardWithoutAReload(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// --- a sensor this test can switch off -----------------------------------

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("could not open the port the test sensor is meant to answer on: %v", err)
	}
	defer func() { _ = ln.Close() }()
	name := testSensorPrefix + "port"
	createSensorThroughTheAPI(t, ctx, dir, name, "tcp",
		"127.0.0.1:"+strconv.Itoa(ln.Addr().(*net.TCPAddr).Port))

	// Up, because something is listening. The first probe is spread over a few
	// seconds by the scheduler, so this is a wait and not a reading.
	eventually(t, ctx, cardExists(name),
		"the sensor this test made was never drawn on the board at all", dir, "live-sensor-missing", 30*time.Second)
	eventually(t, ctx, cardIs(name, "st-up"),
		"the test sensor is not up although this test is listening on its port; nothing "+
			"about what follows would mean anything",
		dir, "live-sensor-not-up", 30*time.Second)
	id := read[int](t, ctx, sensorID(name), "the id of the test sensor")

	// --- an open board, with the places folded away --------------------------

	clickLayout(t, ctx, dir, "tree", ".site-node")
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](
			`document.querySelectorAll('.site-node').forEach(d => { d.open = false; })`))
	}); err != nil {
		capture(t, ctx, dir, "live-collapse")
		t.Fatalf("could not collapse the places in the tree: %v", err)
	}
	eventually(t, ctx, `document.querySelectorAll('.site-node').length > 0 &&
		[...document.querySelectorAll('.site-node')].every(d => !d.open)`,
		"the places would not stay collapsed, so the check below has nothing to check",
		dir, "live-not-collapsed", 10*time.Second)

	// A value only this page holds. A reload wipes it, a redraw does not — which
	// is exactly the difference being tested: it distinguishes the two ways the
	// board could have brought itself up to date.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](
			`window.__probeglassAlive = 'the page was never reloaded'`))
	}); err != nil {
		t.Fatalf("could not mark the page: %v", err)
	}
	downBefore := downCount(t, ctx)

	// The flash is a class that is taken off again 900ms later, so it is caught
	// rather than looked for: an observer records every node that ever carried it,
	// and nothing about when this test happens to look can lose it. Polling for
	// "down and flashing" in one breath would be a race with the very thing it came
	// to see, and a test that loses that race reports a working board as broken.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			window.__probeglassFlashes = [];
			new MutationObserver((records) => {
			  for (const record of records) {
			    const el = record.target;
			    if (record.attributeName === 'class' && el.classList &&
			        el.classList.contains('is-flash')) {
			      window.__probeglassFlashes.push(Number(el.dataset.id));
			    }
			  }
			}).observe(document.querySelector('#groups'),
			  { subtree: true, attributes: true, attributeFilter: ['class'] })`))
	}); err != nil {
		t.Fatalf("could not watch the board for flashes: %v", err)
	}

	// --- one probe fails ------------------------------------------------------

	if err := ln.Close(); err != nil {
		t.Fatalf("could not close the port the test sensor answers on: %v", err)
	}

	// Polled at a stated interval rather than the default animation-frame one: the
	// state has to be seen to change at all before anything else here is worth
	// asserting, and a check tied to frames goes as fast as the frames do.
	eventuallyEvery(t, ctx, fmt.Sprintf(`(() => {
	  const row = [...document.querySelectorAll('#groups [data-id]')]
	    .find(el => Number(el.dataset.id) === %d && !el.parentElement.closest('[data-id]'));
	  return !!row && row.classList.contains('st-down');
	})()`, id),
		"a probe that failed never reached the open board", dir, "live-not-patched",
		50*time.Millisecond, 30*time.Second)

	// --- and what that did, and did not, move --------------------------------

	// The row is the sensor, replaced in place. Both halves are read from it: the
	// class the stylesheet colours, and the word a person reads.
	row := read[liveRow](t, ctx, readLiveRow(id), "the patched row")
	if row.Word != "Down" {
		t.Errorf("the patched row says %q, want Down", row.Word)
	}
	if row.Name != name {
		t.Errorf("the row at id %d is %q; the node that was replaced was not the one the "+
			"probe was about", id, row.Name)
	}
	if !row.InPlace {
		t.Errorf("the row for %q is no longer inside the place it was drawn in; "+
			"patching one sensor moved it", name)
	}

	flash := read[[]int](t, ctx, `window.__probeglassFlashes || []`,
		"the nodes the board has flashed")
	if !slices.Contains(flash, id) {
		capture(t, ctx, dir, "live-no-flash")
		t.Errorf("the probe was drawn with no flash (the board flashed %v); a board that is "+
			"correct without ever showing that something happened is a board nobody watches",
			flash)
	}

	// The heading of the place the sensor sits in is patched with it, and it has to
	// agree with what is drawn under it — in both numbers. The buttons on a row carry
	// a data-id too, so a heading that counts every element mentioning an id turns
	// four sensors into twenty-four and one outage into five.
	head := read[siteHead](t, ctx, readSiteHead, "the place heading")
	if head.Count != head.Rows {
		capture(t, ctx, dir, "live-head-miscount")
		t.Errorf("the heading says the place holds %d sensor(s) and %d are drawn under it",
			head.Count, head.Rows)
	}
	if head.Down != head.DownRows {
		capture(t, ctx, dir, "live-head-miscount-down")
		t.Errorf("the heading says %d of them are down and %d rows are drawn as down",
			head.Down, head.DownRows)
	}

	// The header is patched from the event as well, without asking the server for
	// anything. It is allowed to move by more than one: the example sensors are
	// being probed for real while this runs.
	downAfter := downCount(t, ctx)
	if downAfter < downBefore+1 {
		t.Errorf("the header counted %d sensors down before the probe failed and %d after; "+
			"the state of the sensor reached its row but not the summary beside it",
			downBefore, downAfter)
	}

	// --- the page itself -------------------------------------------------------

	if alive := read[string](t, ctx, `window.__probeglassAlive || ''`, "the page's own marker"); alive == "" {
		capture(t, ctx, dir, "live-reloaded-the-page")
		t.Errorf("the board brought itself up to date by reloading; a probe landing is not " +
			"a reason to throw the page away")
	}

	opened := read[[]string](t, ctx, `[...document.querySelectorAll('.site-node')]
		.filter(d => d.open)
		.map(d => d.querySelector('.site-name').textContent.trim())`, "the places that are open")
	if len(opened) > 0 {
		capture(t, ctx, dir, "live-reopened-a-place")
		t.Errorf("the places %v were collapsed and opened themselves when a probe landed; "+
			"a board that redraws itself on every check is worse than one that stands still",
			opened)
	}
}

// TestLiveOffStopsTheStream checks the switch, from both ends.
//
// The screen is the easy half: the control changes state and says so. The half
// that matters is the server's — "off" has to mean the request is gone. A page
// that keeps the stream open and quietly ignores what arrives is a page that is
// burning a connection to say nothing, and no amount of looking at the screen
// would reveal it. /api/diagnostics counts its subscribers, so the server can be
// asked directly.
//
// And then the absence itself: with the stream closed, a sensor created through
// the API is not on the board and stays off it, until the switch is turned back
// on — at which point it appears, which is also the check that coming back on
// catches up rather than waiting for the next poll.
func TestLiveOffStopsTheStream(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// --- on by default, and genuinely connected ------------------------------

	eventually(t, ctx, `document.querySelector('#live-chip').dataset.live === 'on'`,
		"the board does not come up following the probes", dir, "live-not-on-by-default", 20*time.Second)

	// Through the server's own arithmetic rather than the page's: the number of
	// clients holding an event stream open. One is this browser.
	eventuallyAsking(t, ctx, sseClientsIs("=== 1"),
		"the page says it is live but the server has nobody subscribed to the stream",
		dir, "live-no-subscriber", 20*time.Second)

	// --- off, through the control ---------------------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Click(`#live-chip`))
	}); err != nil {
		capture(t, ctx, dir, "live-chip-click")
		t.Fatalf("could not press the live control: %v", err)
	}
	eventually(t, ctx, `document.querySelector('#live-chip').dataset.live === 'off' &&
		document.querySelector('#live-chip').getAttribute('aria-pressed') === 'false'`,
		"pressing the live control did not switch it off", dir, "live-not-off", 10*time.Second)

	eventuallyAsking(t, ctx, sseClientsIs("=== 0"),
		"live updates were switched off and the server still has a stream held open: "+
			"\"off\" that keeps the connection is not off",
		dir, "live-stream-still-open", 20*time.Second)

	// The word beside the dot changes too, not only its colour: the control has to
	// be readable by somebody who cannot tell the two colours apart.
	chip := read[liveChip](t, ctx, readLiveChip, "the live control")
	if chip.Word == "" || chip.Label == "" {
		t.Errorf("the live control is off and says nothing about it (word %q, label %q)",
			chip.Word, chip.Label)
	}

	// --- and nothing changes on its own ---------------------------------------

	// Watched for as long as the minute-long safety net can be ruled out. That net
	// is meant to be there and is not what this is about: a refresh from it would be
	// the board being correct, and this test failing for the wrong reason. The
	// interval starts when the dashboard does, a moment after the document, so the
	// rest of the current minute is a lower bound on when it next fires.
	hold := read[int](t, ctx, `Math.min(6000, Math.max(2500, 60000 - (performance.now() % 60000) - 3000))`,
		"how long the board can be watched without the safety net firing")

	name := testSensorPrefix + "quiet"
	createSensorThroughTheAPI(t, ctx, dir, name, "tcp", "127.0.0.1:9")

	time.Sleep(time.Duration(hold) * time.Millisecond)
	if onBoard := read[bool](t, ctx, sensorOnBoard(name), "whether the new sensor is on the board"); onBoard {
		capture(t, ctx, dir, "live-updated-while-off")
		t.Errorf("a sensor was added on the server and appeared on the board within %dms "+
			"while live updates were off", hold)
	}

	// --- on again, and the board catches up -----------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Click(`#live-chip`))
	}); err != nil {
		capture(t, ctx, dir, "live-chip-click-back")
		t.Fatalf("could not press the live control a second time: %v", err)
	}
	eventually(t, ctx, `document.querySelector('#live-chip').dataset.live === 'on'`,
		"the live control did not come back on", dir, "live-not-on-again", 10*time.Second)

	// Nothing is replayed on a stream that was not open while it happened, so the
	// only honest way back is to ask. A board that comes back on and stays as stale
	// as it was is not live either.
	eventually(t, ctx, sensorOnBoard(name),
		"live updates were switched back on and the board stayed as stale as it was: "+
			"nothing the stream missed while it was closed is ever sent again",
		dir, "live-not-caught-up", 20*time.Second)

	eventuallyAsking(t, ctx, sseClientsIs(">= 1"),
		"the board is live again but the server still has nobody subscribed",
		dir, "live-not-resubscribed", 20*time.Second)
}

// --- reading the board -----------------------------------------------------

// liveRow is one sensor in the tree, as the patched node presents it.
type liveRow struct {
	Name string `json:"name"`
	Word string `json:"word"`
	// InPlace is whether the row is still inside a place, rather than somewhere
	// else on the page: a node rebuilt outside its container is a sensor that has
	// moved, and moving is the thing patching exists to avoid.
	InPlace bool `json:"inPlace"`
}

func readLiveRow(id int) string {
	return fmt.Sprintf(`(() => {
	  const row = [...document.querySelectorAll('#groups [data-id]')]
	    .find(el => Number(el.dataset.id) === %d && !el.parentElement.closest('[data-id]'));
	  if (!row) return null;
	  const name = row.querySelector('.row-name');
	  const pill = row.querySelector('.pill');
	  return {
	    name: name ? name.textContent.trim().split('\n')[0].trim() : '',
	    word: pill ? pill.textContent.trim() : '',
	    inPlace: !!row.closest('.site-node'),
	  };
	})()`, id)
}

// siteHead is what one place in the tree says about itself, against what is
// actually drawn under it.
type siteHead struct {
	// Count and Down are the two numbers read out of the heading, in the order the
	// heading gives them; Count is -1 when the heading carries no number at all.
	Count int `json:"count"`
	Down  int `json:"down"`
	// Rows and DownRows are the same two counts taken from the tree itself.
	Rows     int `json:"rows"`
	DownRows int `json:"downRows"`
}

const readSiteHead = `(() => {
  const node = [...document.querySelectorAll('.site-node')].find(d => d.querySelector('.tree-row'));
  if (!node) return null;
  const rows = [...node.querySelectorAll('.tree-row')];
  const numbers = (node.querySelector('.site-count') || {textContent: ''})
    .textContent.match(/[0-9]+/g) || [];
  return {
    count: numbers.length ? Number(numbers[0]) : -1,
    down: numbers.length > 1 ? Number(numbers[1]) : 0,
    rows: rows.length,
    downRows: rows.filter(r => r.classList.contains('st-down')).length,
  };
})()`

type liveChip struct {
	// State is the control's own attribute: on, waiting or off.
	State string `json:"state"`
	Word  string `json:"word"`
	Label string `json:"label"`
}

const readLiveChip = `(() => {
  const chip = document.querySelector('#live-chip');
  if (!chip) return null;
  const word = chip.querySelector('.live-word');
  return {
    state: chip.dataset.live || '',
    word: word ? word.textContent.trim() : '',
    label: chip.getAttribute('aria-label') || '',
  };
})()`

// sseClientsIs is the server's own count of open event streams, held against a
// comparison — read over the API rather than from the page, because that is the
// only way to ask whether "off" closed the connection or merely stopped looking
// at what arrives on it.
//
// The comparison has to be inside the last .then: the predicate a poll awaits is
// the promise itself, and `fetch(...).then(...) === 1` is a promise compared to a
// number, which is false, immediately, and forever.
func sseClientsIs(want string) string {
	return `fetch('/api/settings')
		.then(r => r.json())
		.then(d => ((d.stats || {}).sse_clients ` + want + `))`
}

// eventuallyAsking is eventually for a predicate that goes out to the server.
//
// The requests are spaced half a second apart rather than fired every 50ms: a
// poll of an API fills the daemon's log with hundreds of lines in a few seconds,
// and the line that says what actually went wrong is in there somewhere.
func eventuallyAsking(t *testing.T, ctx context.Context, expr, what, dir, shot string, timeout time.Duration) {
	t.Helper()
	eventuallyEvery(t, ctx, expr, what, dir, shot, 500*time.Millisecond, timeout)
}

// eventuallyEvery is eventually with the interval stated.
//
// The interval matters whenever the predicate is watching for a change rather
// than for a state, and it matters most for the shortest-lived thing on this
// board. chromedp's default is the animation frame, so a check made on a page that
// is not being painted goes as slowly as the frames do, and a 900ms window can pass
// unseen. An interval is a promise about how often the question is asked.
func eventuallyEvery(t *testing.T, ctx context.Context, expr, what, dir, shot string, every, timeout time.Duration) {
	t.Helper()
	if _, err := chromedp.Run(ctx, chromedp.Poll[chromedp.Void](expr,
		chromedp.WithPollingInterval(every),
		chromedp.WithPollingTimeout(timeout))); err != nil {
		capture(t, ctx, dir, shot)
		t.Fatalf("%s: %v", what, err)
	}
}

// sensorOnBoard is true when a sensor of that name is drawn, in the view that is
// currently up. Read by name because that is what a person looks for.
func sensorOnBoard(name string) string {
	return `[...document.querySelectorAll('#groups [data-id]')].some(
		el => (el.querySelector('.card-name, .row-name, .tile-name') || {textContent: ''})
			.textContent.trim().startsWith(` + jsQuote(name) + `))`
}

// cardExists is true when the named sensor is drawn as one of the cards.
func cardExists(name string) string {
	return `[...document.querySelectorAll('#groups .card')].some(
		c => (c.querySelector('.card-name') || {textContent: ''}).textContent.trim() === ` +
		jsQuote(name) + `)`
}

// cardIs is true when the named sensor is drawn as a card in the given state
// class — the one place on the board the state is written down for a stylesheet.
func cardIs(name, class string) string {
	return `[...document.querySelectorAll('#groups .card')].some(
		c => (c.querySelector('.card-name') || {textContent: ''}).textContent.trim() === ` +
		jsQuote(name) + ` && c.classList.contains(` + jsQuote(class) + `))`
}

// sensorID reads the id the page gave a sensor it drew, which is what the patch
// path looks nodes up by.
func sensorID(name string) string {
	return `Number([...document.querySelectorAll('#groups [data-id]')].find(
		el => (el.querySelector('.card-name, .row-name, .tile-name') || {textContent: ''})
			.textContent.trim().startsWith(` + jsQuote(name) + `)).dataset.id)`
}

// downCount reads the header's count of sensors that are down. Missing is zero:
// the summary leaves a count out when it is nothing.
func downCount(t *testing.T, ctx context.Context) int {
	t.Helper()
	return read[int](t, ctx,
		`(() => {
		   const n = document.querySelector('#summary .count.s-crit .count-n');
		   return n ? Number(n.textContent.trim()) : 0;
		 })()`,
		"the header's count of sensors that are down")
}

// createSensorThroughTheAPI asks the daemon for a sensor, from the page, so that
// the request carries the cookie and the Origin header the route insists on.
//
// The e2e module deliberately does not depend on the store package, so this is the
// only door into an installation from a test: the API the dashboard itself uses.
func createSensorThroughTheAPI(t *testing.T, ctx context.Context, dir, name, typ, target string) {
	t.Helper()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			fetch('/api/sensors', {
			  method: 'POST',
			  headers: { 'Content-Type': 'application/json' },
			  body: JSON.stringify({
			    name: `+jsQuote(name)+`,
			    type: `+jsQuote(typ)+`,
			    target: `+jsQuote(target)+`,
			    interval: 5,
			    timeout: 1,
			    retries: 1,
			    enabled: true,
			  }),
			}).then(r => { if (!r.ok) throw new Error('POST /api/sensors: ' + r.status); })`,
			chromedp.EvalAwaitPromise))
	})
	if err != nil {
		capture(t, ctx, dir, "live-create-sensor")
		t.Fatalf("could not create the sensor %q: %v", name, err)
	}
}
