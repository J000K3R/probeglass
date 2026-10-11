// The guided tour, walked the way a person walks it.
//
// The other browser test asks whether the dashboard draws itself and whether a
// click does what it says. This one asks the harder question about the tour:
// does it actually get anywhere? The tour switches views, waits for targets
// that are not on screen yet, and branches at the end on a three-way choice —
// all of which can fail silently, leaving a card on screen that is legible,
// plausible and about nothing.
//
// So the assertions here are about the journey and not the words: that the
// offered tour runs, that it lands on the settings view when a step lives
// there, that a sensor part is really ringed, that the offer at the end moves
// the tour into the subject it names, and that finishing it sticks.
package e2e

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// tourView is the tour as it is on screen at one moment.
type tourView struct {
	Title string `json:"title"`
	Count string `json:"count"`
	// Choice is true while the three-way offer at the end is up.
	Choice bool `json:"choice"`
	// View is the id of the view on display underneath, e.g. "view-settings".
	View string `json:"view"`
	// Ring is the width of the highlight, which is zero when a step has nothing
	// to point at.
	Ring float64 `json:"ring"`
	// Where the card sits, so that a click can wait for it to stop moving.
	At string `json:"at"`
}

const readTour = `(() => {
  const card = document.querySelector('.tour-card');
  if (!card) return null;
  const choice = card.querySelector('.tour-choice');
  const ring = document.querySelector('.tour-ring');
  const shown = document.querySelector('.view:not([hidden])');
  const box = card.getBoundingClientRect();
  return {
    title: card.querySelector('.tour-title').textContent,
    count: card.querySelector('.tour-count').textContent,
    choice: !!choice && !choice.hidden && choice.getClientRects().length > 0,
    view: shown ? shown.id : '',
    ring: ring && !ring.hidden ? ring.getBoundingClientRect().width : 0,
    at: box.left + ',' + box.top,
  };
})()`

// TestGuidedTourActuallyWalks walks the tour from the offer a fresh install
// makes to the sentence it ends on, and checks that it went somewhere on the
// way.
func TestGuidedTourActuallyWalks(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	// Without a deadline, for the same reason as in the other test: a deadline
	// on this context kills the browser rather than the action, and a phase that
	// timed out would then have nothing left to photograph.
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	//nolint:misspell // "screenshotDir" is the spelling GitHub uses for the uploaded files
	screenshotDir := os.Getenv("PROBEGLASS_E2E_ARTIFACTS")
	if screenshotDir == "" {
		screenshotDir = t.TempDir()
	}

	// Everything the page reports as an error, so that a tour which throws on
	// its way through is reported here rather than only on another screen.
	consoleCtx, stopConsole := context.WithCancel(ctx)
	defer stopConsole()
	var (
		mu         sync.Mutex
		pageErrors []string
	)
	go func() {
		for m, err := range chromedp.Console(consoleCtx) {
			if err != nil {
				return
			}
			if m.IsException() || (m.Type == chromedp.ConsoleError && m.Source == "") {
				mu.Lock()
				pageErrors = append(pageErrors, m.Text)
				mu.Unlock()
			}
		}
	}()

	// --- phase 1: a fresh install offers the tour by itself ------------------

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
			// Not started from Settings but offered, because that is what a
			// person sees first: the tour waits for a dashboard with something
			// to point at, and the example sensors are what it waits for.
			chromedp.WaitVisible(`.tour-card`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "tour-not-offered")
		t.Fatalf("a fresh install did not offer the guided tour: %v", err)
	}

	first := tour(ctx, t)
	if first.Title == "" {
		capture(t, ctx, screenshotDir, "tour-empty")
		t.Fatal("the tour is on screen with no title in it")
	}
	if !strings.HasPrefix(first.Count, "1 / ") {
		t.Errorf("the tour starts at %q, want the first of its steps", first.Count)
	}

	// The one thing the tour must not leave unsaid. Every dashboard a new
	// install shows is invented, and a person who does not know that will read
	// the numbers as their own network.
	body := text(ctx, t, ".tour-body")
	if !strings.Contains(strings.ToLower(body), "sample data") {
		capture(t, ctx, screenshotDir, "tour-without-disclaimer")
		t.Errorf("the opening step does not say the dashboard is sample data; it says:\n  %s", body)
	}

	// --- phase 2: walk to the end, noting where it goes ----------------------

	var (
		sawSettings   bool
		sawSensorPart bool
		seen          []string
	)
	for step := 0; step < 40; step++ {
		now := tour(ctx, t)
		if now.Choice {
			break
		}
		if now.View == "view-settings" {
			sawSettings = true
		}
		// A ring around a single card part is what "goes closer into a sensor"
		// means; a step that only centred its card would leave this zero.
		if now.Ring > 0 && now.Ring < 500 {
			sawSensorPart = true
		}
		seen = append(seen, now.Title)

		if !advance(ctx, t, now.Title) {
			capture(t, ctx, screenshotDir, "tour-stuck")
			if _, up := tourOr(ctx); !up {
				t.Fatalf("the tour left the screen at %q, before it offered a subject "+
					"to go into. It visited:\n  %s", now.Title, strings.Join(seen, "\n  "))
			}
			t.Fatalf("the tour did not move on after %q; it visited:\n  %s",
				now.Title, strings.Join(seen, "\n  "))
		}
	}

	if !sawSettings {
		capture(t, ctx, screenshotDir, "tour-never-opened-settings")
		t.Errorf("the tour never showed the settings view, although it has steps about "+
			"the sample data there. It visited:\n  %s", strings.Join(seen, "\n  "))
	}
	if !sawSensorPart {
		capture(t, ctx, screenshotDir, "tour-never-zoomed")
		t.Errorf("no step highlighted a single part of a sensor card, which is the "+
			"walk-through the tour exists for. It visited:\n  %s", strings.Join(seen, "\n  "))
	}

	offer := tour(ctx, t)
	if !offer.Choice {
		capture(t, ctx, screenshotDir, "tour-no-offer")
		t.Fatalf("the tour ended at %q without offering a subject to go into", offer.Title)
	}
	if offer.Count == "" {
		t.Error("the offer at the end of the tour carries no step count")
	}

	// --- phase 3: the offer has to move the tour -----------------------------

	if err := chromedp.Do(ctx, chromedp.Click(`.tour-card [data-tour-topic="incidents"]`)); err != nil {
		capture(t, ctx, screenshotDir, "tour-offer-unclickable")
		t.Fatalf("the offer at the end of the tour could not be answered: %v", err)
	}
	if !waitFor(ctx, `document.querySelector('.view:not([hidden])').id === 'view-incidents'`) {
		capture(t, ctx, screenshotDir, "tour-offer-went-nowhere")
		t.Fatal("choosing \"Incidents\" did not take the tour to the incidents view")
	}
	after := tour(ctx, t)
	if after.Title == "" || after.Title == offer.Title {
		capture(t, ctx, screenshotDir, "tour-offer-went-nowhere")
		t.Fatalf("choosing \"Incidents\" left the tour on %q", after.Title)
	}
	if after.Count == offer.Count {
		t.Errorf("the step count stayed at %q after the choice, so the topic added "+
			"nothing to the tour", after.Count)
	}

	// --- phase 4: finishing it sticks ---------------------------------------

	// The subject ends on a step of its own, and that step on the closing card,
	// so the tour is done when the card is gone rather than when a particular
	// step has been reached.
	left := false
	for step := 0; step < 6 && !left; step++ {
		now, up := tourOr(ctx)
		if !up {
			left = true
			break
		}
		if advance(ctx, t, now.Title) {
			continue
		}
		if _, still := tourOr(ctx); !still {
			left = true
			break
		}
		capture(t, ctx, screenshotDir, "tour-would-not-finish")
		t.Fatalf("the tour would not move on from %q", now.Title)
	}
	if !left {
		capture(t, ctx, screenshotDir, "tour-would-not-finish")
		now, _ := tourOr(ctx)
		t.Fatalf("six steps after the offer the tour is still on %q", now.Title)
	}
	if !waitFor(ctx, `!document.querySelector('.tour-card')`) {
		capture(t, ctx, screenshotDir, "tour-still-up")
		t.Fatal("the tour was finished but its card is still on screen")
	}

	// The version is what keeps it from coming back, and it is written by a
	// request that is allowed to fail — so it is worth checking rather than
	// assuming.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Navigate(base+"/"),
			chromedp.WaitVisible(`#app`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "tour-reload")
		t.Fatalf("the dashboard did not come back after finishing the tour: %v", err)
	}
	if present, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`!!document.querySelector('.tour-card')`)); err != nil {
		t.Errorf("could not check whether the tour came back: %v", err)
	} else if present {
		capture(t, ctx, screenshotDir, "tour-came-back")
		t.Error("the tour was offered again after it had been walked to the end")
	}

	// --- phase 5: the simple interface gets a shorter tour --------------------

	// The tour has just been walked to the end, so it will not offer itself again
	// and has to be started by hand — from Settings, which is also where the
	// interface mode is chosen. Changing a select is setting its value and letting
	// the change event go, which is the same path a person's click takes.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("look"),
			chromedp.WaitVisible(`select[data-setting="mode"]`),
			chromedp.Evaluate[chromedp.Void](simpleMode),
			// Settings draws itself twice: once on arrival and again when the panels
			// that need data have it. Waiting for the users list as well as for the
			// panel to be hidden means the second draw is the one in front of us, and
			// the button is not thrown away between being found and being clicked.
			chromedp.Poll[chromedp.Void](simpleApplied, chromedp.WithPollingTimeout(10*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, screenshotDir, "tour-simple-mode")
		t.Fatalf("the interface could not be switched to the simple one: %v", err)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			// Pressed from inside the page rather than with chromedp.Click: the
			// settings panels are redrawn whenever the dashboard receives an event,
			// and a click driven from outside takes several round trips between
			// finding the button and pressing it, which is long enough for the button
			// to be a different element by then. This dispatches the same click event
			// the panel's own handler listens for, in one go.
			chromedp.Evaluate[chromedp.Void](
				`document.querySelector('[data-action="tour"]').click()`),
			chromedp.WaitVisible(`.tour-card`),
		)
	}); err != nil {
		capture(t, ctx, screenshotDir, "tour-simple-mode")
		t.Fatalf("the tour could not be started in the simple interface: %v", err)
	}

	simple := tour(ctx, t)
	both, advanced := stepTotal(simple.Count), stepTotal(offer.Count)
	if both <= 0 || advanced <= 0 {
		capture(t, ctx, screenshotDir, "tour-count-unreadable")
		t.Fatalf("the step counts could not be read: %q in the simple interface, %q in "+
			"the advanced one", simple.Count, offer.Count)
	}
	if both >= advanced {
		capture(t, ctx, screenshotDir, "tour-not-shorter")
		t.Errorf("the tour is %s long in the simple interface and %s in the advanced "+
			"one, so the steps that belong to the advanced mode alone were not left out",
			simple.Count, offer.Count)
	}

	// And it still has to get to the end rather than stop at the first step it can
	// no longer show.
	offered := false
	for step := 0; step < 40; step++ {
		now, up := tourOr(ctx)
		if !up {
			break
		}
		if now.Choice {
			offered = true
			break
		}
		if !advance(ctx, t, now.Title) {
			break
		}
	}
	if !offered {
		capture(t, ctx, screenshotDir, "tour-simple-stuck")
		t.Error("the tour never reached its offer at the end in the simple interface")
	}

	stopConsole()
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for _, message := range pageErrors {
		t.Errorf("the tour reported an error on its way through: %s", message)
	}
}

// TestTheTourTellsTheTruthAboutSampleData is the other half of the test above.
//
// The tour is restartable from Settings, and the example sensors are the first
// thing an experienced person deletes — so by the time it is restarted they are
// usually long gone. The welcome step and the step that takes a sensor apart both
// used to say the dashboard was invented whatever was on screen. That is the one
// sentence this product cannot afford to get wrong: said over somebody's own
// network it calls their router a fake, and it teaches them to disbelieve the
// warning that is supposed to stop them trusting the numbers.
//
// So: remove the sample data, put a sensor of the person's own on the dashboard,
// and walk the tour again from the beginning.
func TestTheTourTellsTheTruthAboutSampleData(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	//nolint:misspell // "screenshotDir" is the spelling GitHub uses for the uploaded files
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
			// Dismiss the tour a fresh install offers by itself; this test is about
			// the one somebody starts by hand afterwards.
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`.tour-card [data-tour="skip"]`),
			chromedp.WaitNotPresent(`.tour-card`),
			// The removal button asks first, with the browser's own confirm box,
			// which cannot be answered from outside without a dialog handler.
			// Answering it here is the whole of the seam, and it is not a shortcut:
			// what is under test is the tour, and a person who clicks the button and
			// then OK does exactly this.
			chromedp.Evaluate[chromedp.Void](`window.confirm = () => true`),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#examples-panel`),
			chromedp.Click(`#examples-panel [data-action="drop-examples"]`),
			// The panel stays after the removal now, because it is also the way
			// back. What is waited for is the delete button going, and the button
			// that loads the example data again taking its place.
			chromedp.WaitVisible(`#examples-panel [data-action="restore-examples"]`),
			chromedp.WaitNotPresent(`#examples-panel [data-action="drop-examples"]`),
			// One sensor of the person's own. Created through the API because the
			// form is a page of its own and has nothing to do with the question
			// asked here: the tour has a card to take apart that is not an example,
			// which is the case this test exists for.
			chromedp.Evaluate[chromedp.Void](createOwnSensor),
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/dashboard'`),
			chromedp.Poll[chromedp.Void](oneOwnCard, chromedp.WithPollingTimeout(15*time.Second)),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "own-sensor-not-there")
		t.Fatalf("could not put the installation into the state under test — no sample "+
			"data and one sensor of one's own: %v", err)
	}

	// --- start it again by hand, the way a person would -----------------------

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/settings'`),
			openSettingsCategory("help"),
			chromedp.WaitVisible(`[data-action="tour"]`),
			chromedp.Click(`[data-action="tour"]`),
			chromedp.WaitVisible(`.tour-card`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "tour-not-restartable")
		t.Fatalf("the tour could not be started again from Settings: %v", err)
	}

	body := strings.ToLower(text(ctx, t, ".tour-body"))
	for _, claim := range []string{"sample data", "no real machine", "invented"} {
		if strings.Contains(body, claim) {
			capture(t, ctx, screenshotDir, "tour-claims-sample-data")
			t.Errorf("the tour is on an installation whose sample data has been removed and "+
				"still calls the dashboard %q; it says:\n  %s", claim, body)
		}
	}
	if !strings.Contains(body, "skip at any time") {
		capture(t, ctx, screenshotDir, "tour-wrong-opening")
		t.Errorf("the opening step of the restarted tour is not the one about the reader's "+
			"own dashboard; it says:\n  %s", body)
	}

	// And it must still do its job: point at a card and name its parts. The card it
	// points at has to be the one that is there, which is now nobody's example.
	ringed := false
	for step := 0; step < 9; step++ {
		settle(ctx)
		if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](ringedOnOwnCard)); err == nil && ok {
			ringed = true
			break
		}
		now, up := tourOr(ctx)
		if !up || !advance(ctx, t, now.Title) {
			break
		}
	}
	if !ringed {
		capture(t, ctx, screenshotDir, "tour-never-zoomed-into-an-own-sensor")
		t.Error("the restarted tour never highlighted a part of the only card there was, " +
			"so the walk-through of a sensor is gone as soon as the sample data is")
	}
}

// createOwnSensor adds a sensor through the API and answers with its status code.
const createOwnSensor = `fetch('/api/sensors', {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify({ name: 'Own router', type: 'ping', target: '192.0.2.1' }),
}).then((r) => r.status)`

// oneOwnCard is the state under test: exactly one card on the dashboard, and it is
// not one of the installation's own samples.
const oneOwnCard = `(() => {
  const cards = document.querySelectorAll('#groups .card');
  return cards.length === 1 && cards[0].dataset.origin !== 'example';
})()`

// ringedOnOwnCard is true while the tour is highlighting the card that is not an
// example. The geometry is compared, not just the presence of a ring, because a
// ring left over from the step before sits in the same document.
const ringedOnOwnCard = `(() => {
  const ring = document.querySelector('.tour-ring');
  if (!ring || ring.hidden) return false;
  const card = document.querySelector('#groups .card');
  if (!card) return false;
  const box = ring.getBoundingClientRect();
  const at = card.getBoundingClientRect();
  return box.width > 0 && box.width < 500 && Math.abs(box.left - at.left) < 24;
})()`

// simpleMode switches the interface to the simple one, the way the select in
// Settings does: set the value and let the change event out, which is the path a
// person's click on the list takes. Nothing here pokes at the tour directly.
const simpleMode = `(() => {
  const select = document.querySelector('select[data-setting="mode"]');
  select.value = 'simple';
  select.dispatchEvent(new Event('change', { bubbles: true }));
})()`

// simpleApplied is true once the simple interface has been chosen and the
// settings page has caught up with it.
//
// The check is on the mode and on the advanced-only panel being off the screen,
// not on that panel's own `hidden` attribute. The page shows one category at a
// time and keeps the rest hidden, so "is the user panel hidden" stopped being a
// question about the mode the moment the tabs arrived — it depends on which
// category happens to be up. What the mode does is CSS, so what is read back is
// what the browser computed, together with the tab that is actually selected:
// a page with no category on screen is a page that has not drawn itself.
const simpleApplied = `(() => {
  if (document.documentElement.dataset.mode !== 'simple') return false;
  const tab = document.querySelector('#settings-body [data-category][aria-selected="true"]');
  if (!tab || tab.hidden) return false;
  const users = document.querySelector('#users-panel');
  return users !== null && getComputedStyle(users).display === 'none';
})()`

// stepTotal reads the "3 / 16" in the corner of the tour card as a number, so two
// tours can be compared without either of them having to promise a length.
func stepTotal(count string) int {
	_, total, ok := strings.Cut(count, "/")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(strings.TrimSpace(total))
	if err != nil {
		return -1
	}
	return n
}

// tour reads what the tour is showing, and fails rather than returning nonsense
// when the card is not there at all, because this is only used where a step is
// expected on screen.
func tour(ctx context.Context, t *testing.T) tourView {
	t.Helper()
	view, ok := tourOr(ctx)
	if !ok {
		t.Fatal("the guided tour is not on screen")
	}
	return view
}

// tourOr reads the tour if it is up. The card is taken out of the document when
// the tour ends, so "no card" is an ordinary answer and not an error.
func tourOr(ctx context.Context) (tourView, bool) {
	view, err := chromedp.Run(ctx, chromedp.Evaluate[tourView](readTour))
	if err != nil {
		return tourView{}, false
	}
	return view, true
}

// advance presses Next and reports whether the tour is still going afterwards. It
// waits for the card to stop moving first, and for the title to change
// afterwards, rather than reading straight away: a step that has to switch views
// draws its words in the middle of the screen and only then moves into place, so
// a click aimed at the earlier position would land on nothing and be read as a
// step that does nothing.
//
// A false therefore means either that the tour has finished — the last step does
// that — or that the click could not be delivered at all. The caller tells the
// two apart by asking tourOr whether anything is still on screen.
func advance(ctx context.Context, t *testing.T, from string) bool {
	t.Helper()
	if !clickNext(ctx) {
		return false
	}
	if waitFor(ctx, titleIsNot(from)) {
		now, up := tourOr(ctx)
		return up && now.Title != from
	}
	// One more try: the click can be delivered a hair before the card finishes
	// moving, and a tour that is merely hard to click is not a broken tour. A
	// button that does nothing at all still fails here.
	settle(ctx)
	if !clickNext(ctx) {
		return false
	}
	if !waitFor(ctx, titleIsNot(from)) {
		return false
	}
	now, up := tourOr(ctx)
	return up && now.Title != from
}

// clickNext presses the button at the right-hand end of the card, which says Next
// until the last step and Done on it.
func clickNext(ctx context.Context) bool {
	if err := chromedp.Do(ctx, chromedp.Click(`.tour-card [data-tour="next"]`)); err != nil {
		return false
	}
	return true
}

// titleIsNot is the condition advance waits for: either the tour has moved on
// to another step, or it has taken its card away altogether.
func titleIsNot(title string) string {
	return fmt.Sprintf(
		`(() => { const c = document.querySelector('.tour-card'); `+
			`return !c || c.querySelector('.tour-title').textContent !== %q; })()`, title)
}

// settle waits until the tour card has held the same position for two reads in a
// row, which is as close to "it has finished drawing" as the outside can get.
func settle(ctx context.Context) {
	previous := ""
	for i := 0; i < 100; i++ {
		now, up := tourOr(ctx)
		if !up {
			return
		}
		if now.At == previous {
			return
		}
		previous = now.At
		time.Sleep(20 * time.Millisecond)
	}
}

// text returns the visible text of one element, or the empty string.
func text(ctx context.Context, t *testing.T, selector string) string {
	t.Helper()
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](fmt.Sprintf(
		`(document.querySelector(%q) || {}).textContent || ''`, selector)))
	if err != nil {
		t.Fatalf("could not read %s: %v", selector, err)
	}
	return strings.TrimSpace(value)
}

// waitFor polls a boolean expression in the page for a few seconds.
func waitFor(ctx context.Context, expression string) bool {
	for i := 0; i < 200; i++ {
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expression))
		if err == nil && ok {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}
