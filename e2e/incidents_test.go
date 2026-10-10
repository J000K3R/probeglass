// Package e2e — the incident list and its filter line.
//
// The filter is four controls whose whole job is to make one list smaller, and
// the failure it can have is not a crash but a lie: a status button that does not
// press, a period chip that changes nothing, an empty list under a filter that
// says "nothing has failed" when the truth is "nothing matched". None of that is
// visible from the API, which answers each of these queries correctly whether or
// not anybody wired a control to it.
//
// So this test drives the controls and reads the table.
package e2e

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// incidentList is what the incidents screen looks like from the outside.
//
// The counts come from the rendered table rather than from the API, which is the
// point: the question is whether the filter reached the list on screen.
type incidentList struct {
	Rows int `json:"rows"`
	// Open counts the rows still marked as running, which the stylesheet keys on.
	Open int `json:"open"`
	// Sensors is the set of sensor names the visible rows belong to.
	Sensors []string `json:"sensors"`
	// Causes is the set of messages, which is what the text field is being asked
	// about.
	Causes []string `json:"causes"`

	Empty    bool   `json:"empty"`  // "No incidents" is on screen
	Hides    bool   `json:"hides"`  // …and it blames the filter rather than the installation
	Reset    bool   `json:"reset"`  // the "reset filter" button is offered
	Status   string `json:"status"` // the pressed status button, "" for All
	Sensor   string `json:"sensor"` // the value of the sensor select
	Query    string `json:"query"`  // the text field
	Selected string `json:"selected"`
}

const readIncidentList = `(() => {
  const rows = [...document.querySelectorAll('#incident-list tbody tr')];
  const text = (row, sel) => {
    const el = row.querySelector(sel);
    return el ? el.textContent.trim() : '';
  };
  const visible = (el) => el !== null && el.getBoundingClientRect().height > 0;
  const visibleText = (sel) => {
    const el = document.querySelector(sel);
    return visible(el) ? el.textContent.trim() : '';
  };
  const pressed = document.querySelector('#incident-status [data-state][aria-pressed="true"]');
  return {
    rows: rows.length,
    open: rows.filter(r => r.classList.contains('row-crit')).length,
    sensors: [...new Set(rows.map(r => text(r, 'td b')))],
    causes: [...new Set(rows.map(r => text(r, 'td:nth-child(5)')).map(t => t.split('\n')[0]))],
    empty: visible(document.querySelector('#incidents-empty')),
    hides: visible(document.querySelector('#incidents-empty-filtered')),
    reset: visible(document.querySelector('#incident-reset')),
    status: pressed ? (pressed.dataset.state || '') : '(none pressed)',
    sensor: document.querySelector('#incident-sensor').value,
    query: document.querySelector('#incident-query').value,
    selected: visibleText('#incidents-empty-body'),
  };
})()`

// TestIncidentFilterNarrowsTheList walks the filter line the way somebody
// narrowing down an outage does: by state first, then by place, then by a word
// they remember, and then back out again with the reset button.
//
// It runs against the demo data set rather than against an empty installation,
// because a filter over nothing can only ever prove that it did not crash. The
// set is seeded before the daemon opens the database: `seed --demo` writes through
// a second process and cannot run while the first one holds the file.
func TestIncidentFilterNarrowsTheList(t *testing.T) {
	browser := findBrowser(t)
	bin := buildBinary(t)
	data := t.TempDir()
	seedDemo(t, bin, data)
	base := startServerIn(t, bin, data)

	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// --- the whole list -----------------------------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`location.hash = '#/incidents'`),
			chromedp.Poll[chromedp.Void](`document.querySelectorAll('#incident-list tbody tr').length > 2`,
				chromedp.WithPollingTimeout(20*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "incidents-unreachable")
		t.Fatalf("the incident list never filled: %v", err)
	}
	addFailingSensor(t, ctx, dir)

	all := read[incidentList](t, ctx, readIncidentList, "the incident list")
	if all.Open == 0 || all.Open == all.Rows {
		capture(t, ctx, dir, "incidents-unfiltered")
		t.Fatalf("the list holds %d incidents of which %d are running; the two state buttons "+
			"can only be told apart if there is at least one of each", all.Rows, all.Open)
	}
	if all.Status != "" {
		t.Errorf("a fresh list has %q pressed; want the All button, which is the empty state value", all.Status)
	}
	if all.Reset {
		t.Error("the reset button is offered before anything has been filtered")
	}

	// --- by state -----------------------------------------------------------

	clickIncidentFilter(t, ctx, dir, `#incident-status [data-state="open"]`, "incidents-open", `
		[...document.querySelectorAll('#incident-list tbody tr')].every(r => r.classList.contains('row-crit'))
		&& document.querySelectorAll('#incident-list tbody tr').length === `+strconv.Itoa(all.Open))
	open := read[incidentList](t, ctx, readIncidentList, "the running incidents")
	if open.Rows != all.Open {
		t.Errorf("the Open button shows %d rows, the list holds %d running outages", open.Rows, all.Open)
	}
	if !open.Reset {
		t.Error("the list is filtered but no way back out is offered")
	}

	clickIncidentFilter(t, ctx, dir, `#incident-status [data-state="resolved"]`, "incidents-resolved", `
		[...document.querySelectorAll('#incident-list tbody tr')].every(r => !r.classList.contains('row-crit'))
		&& document.querySelectorAll('#incident-list tbody tr').length === `+strconv.Itoa(all.Rows-all.Open))
	resolved := read[incidentList](t, ctx, readIncidentList, "the resolved incidents")
	if resolved.Rows != all.Rows-all.Open {
		t.Errorf("the Resolved button shows %d rows; %d of the %d outages have ended",
			resolved.Rows, all.Rows-all.Open, all.Rows)
	}

	// --- back to everything, and then by sensor -----------------------------

	clickIncidentFilter(t, ctx, dir, `#incident-status [data-state=""]`, "incidents-all-again",
		`document.querySelectorAll('#incident-list tbody tr').length === `+strconv.Itoa(all.Rows)+
			` && document.querySelector('#incident-status [data-state=""]').getAttribute('aria-pressed') === 'true'`)

	// The first sensor the unfiltered list names. Whatever it is, every row that
	// comes back has to belong to it — and there has to be fewer of them, or the
	// select is not doing anything.
	wanted := all.Sensors[0]
	awaitIncidentSensorOption(t, ctx, dir, wanted)
	// Chosen by what the option says rather than by its value: the value is the
	// sensor's id, and a test that reached for the id would be reading the thing
	// the interface is supposed to be hiding.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			(() => {
			  const select = document.querySelector('#incident-sensor');
			  const option = [...select.options].find(o => o.textContent.trim() === `+jsQuote(wanted)+`);
			  if (!option) throw new Error('no option for ' + `+jsQuote(wanted)+`);
			  select.value = option.value;
			  select.dispatchEvent(new Event('change'));
			})()`))
	}); err != nil {
		capture(t, ctx, dir, "incidents-by-sensor")
		t.Fatalf("could not choose %q in the filter: %v", wanted, err)
	}
	eventually(t, ctx,
		`[...document.querySelectorAll('#incident-list tbody tr')].length > 0 &&
		 [...document.querySelectorAll('#incident-list tbody tr')].every(
		   r => r.querySelector('td b').textContent.trim() === `+jsQuote(wanted)+`)`,
		"choosing a sensor did not narrow the list to that sensor", dir, "incidents-sensor-not-narrowed", 15*time.Second)
	bySensor := read[incidentList](t, ctx, readIncidentList, "the incident list for one sensor")
	if bySensor.Rows >= all.Rows {
		t.Errorf("one sensor shows %d of the %d incidents; the select changed nothing", bySensor.Rows, all.Rows)
	}

	// --- by a word ----------------------------------------------------------

	// A word out of one of the messages the full list carried, which is what
	// somebody remembers about an outage. It is read off the screen rather than
	// hard-coded so that a change to the demo set does not silently turn this into
	// a test of an empty list.
	word := longestWord(all.Causes[0])
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.SendKeys(`#incident-query`, word),
		)
	}); err != nil {
		capture(t, ctx, dir, "incidents-query")
		t.Fatalf("could not type into the filter: %v", err)
	}
	eventually(t, ctx,
		`document.querySelector('#incident-query').value === `+jsQuote(word)+` &&
		 [...document.querySelectorAll('#incident-list tbody tr')].length > 0 &&
		 [...document.querySelectorAll('#incident-list tbody tr')].every(
		   r => r.textContent.toLowerCase().includes(`+jsQuote(word)+`.toLowerCase()))`,
		"typing a word into the filter did not narrow the list to what mentions it",
		dir, "incidents-query-not-narrowed", 15*time.Second)

	// --- and an empty list that says why ------------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		// Written and dispatched rather than typed: what is being checked here is
		// the answer to a word that matches nothing, not the keyboard.
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			(() => {
			  const input = document.querySelector('#incident-query');
			  input.value = 'zzzz-nothing-matches-this';
			  input.dispatchEvent(new Event('input'));
			})()`))
	}); err != nil {
		t.Fatalf("could not type the second word: %v", err)
	}
	eventually(t, ctx,
		`document.querySelectorAll('#incident-list tbody tr').length === 0 &&
		 document.querySelector('#incidents-empty').getBoundingClientRect().height > 0`,
		"a filter matching nothing left the list empty but said nothing", dir, "incidents-empty-missing", 15*time.Second)
	empty := read[incidentList](t, ctx, readIncidentList, "the empty incident list")
	if !empty.Hides {
		capture(t, ctx, dir, "incidents-empty-blames-the-installation")
		t.Errorf("an empty list under a filter still says %q; it has to say that the filter is "+
			"what emptied it, or somebody goes looking for a fault that is a setting on this page",
			empty.Selected)
	}

	// --- and out again ------------------------------------------------------

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Click(`#incident-reset`))
	}); err != nil {
		capture(t, ctx, dir, "incidents-reset")
		t.Fatalf("could not press the reset button: %v", err)
	}
	// The whole list is back, which is the first reading or more: not the same
	// reading. The demo set's sensors are being probed for real while this runs, so
	// the list is alive underneath the test and a row may have been added since it
	// was first counted. What the reset has to do is bring back everything the
	// filter was hiding — and never less than what was there before the filter.
	eventually(t, ctx,
		`document.querySelectorAll('#incident-list tbody tr').length >= `+strconv.Itoa(all.Rows)+
			` && document.querySelector('#incident-reset').getBoundingClientRect().height === 0 &&
			 document.querySelector('#incident-query').value === ''`,
		"resetting the filter did not bring the whole list back", dir, "incidents-not-reset", 15*time.Second)
	back := read[incidentList](t, ctx, readIncidentList, "the reset incident list")
	if back.Status != "" || back.Query != "" || back.Sensor != "" || back.Reset {
		t.Errorf("after the reset the filter still reads status=%q sensor=%q query=%q reset=%v",
			back.Status, back.Sensor, back.Query, back.Reset)
	}
	if back.Rows < all.Rows {
		t.Errorf("the reset list holds %d rows, fewer than the %d it held before the filter", back.Rows, all.Rows)
	}
}

// clickIncidentFilter presses one control and waits for the predicate the caller
// states, which is the row count that control should produce.
func clickIncidentFilter(t *testing.T, ctx context.Context, dir, selector, shot, ready string) {
	t.Helper()
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Click(selector))
	}); err != nil {
		capture(t, ctx, dir, shot)
		t.Fatalf("could not press %s: %v", selector, err)
	}
	eventually(t, ctx, ready, "pressing "+selector+" did not produce the list it should have",
		dir, shot+"-result", 15*time.Second)
}

// addFailingSensor asks the daemon for a sensor that cannot succeed, and waits for
// its outage to show up in the list.
//
// The demo data set cannot supply the running outage this test needs. Its outages
// are synthesised: every window is opened and closed inside the same transaction
// (internal/demo), so by the time the daemon starts, all of them have ended. The
// list would then hold one state only, and the two buttons that exist to tell the
// states apart could not be told apart by any assertion this test could make.
//
// So the daemon is asked for one that is still failing. A TCP port on the loopback
// interface with nothing behind it is refused instantly on every platform, needs
// no network and no name resolution, and stays refused for as long as the test
// runs — the outage cannot end on its own and quietly empty the Open button again.
// The probe interval is the shortest the store allows, so the first failure is
// roughly three seconds away (the scheduler spreads a sensor's first probe).
//
// The sensor is created through the dashboard's own API rather than by writing to
// the database, because the e2e module deliberately does not depend on the store
// package. The request is made from the page, which is also where a browser gets
// the cookie and the Origin header the route insists on.
func addFailingSensor(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`
			fetch('/api/sensors', {
			  method: 'POST',
			  headers: { 'Content-Type': 'application/json' },
			  body: JSON.stringify({
			    name: 'Dead port',
			    type: 'tcp',
			    target: '127.0.0.1:9',
			    interval: 5,
			    timeout: 1,
			    retries: 1,
			    enabled: true,
			  }),
			}).then(r => { if (!r.ok) throw new Error('POST /api/sensors: ' + r.status); })`,
			chromedp.EvalAwaitPromise))
	})
	if err != nil {
		capture(t, ctx, dir, "incidents-create-sensor")
		t.Fatalf("could not create the sensor this test needs a running outage from: %v", err)
	}

	// Waited for by name, not for "any running outage". The demo set's sensors are
	// probed for real, and one of them reaching this list first — which is what
	// happens on a network without a route to the outside — would satisfy a looser
	// predicate while leaving this test resting on an outage that can end again.
	eventually(t, ctx,
		`[...document.querySelectorAll('#incident-list tbody tr')].some(
		   r => r.classList.contains('row-crit') && r.querySelector('td b').textContent.trim() === 'Dead port')`,
		"a sensor that cannot answer never produced a running incident", dir,
		"incidents-no-running-outage", 30*time.Second)
}

// awaitIncidentSensorOption waits until the filter's sensor dropdown offers `name`.
//
// A wait rather than a read, because the two lists on this screen are not filled
// from the same source at the same moment. The dropdown is built from the sensor
// list, and a sensor added while the page is open reaches that list through a
// refresh that is deliberately debounced by 400 milliseconds (scheduleRefresh) —
// while the incident list is asked for again the instant an incident event arrives,
// with no debounce at all. A sensor that fails its very first probe can raise an
// incident inside that window, and then the table names a sensor the dropdown
// beside it does not yet offer. Nothing is wrong with the screen: it catches up on
// its own within the debounce. What would be wrong is a test that read the dropdown
// in that gap and called the miss a product fault.
func awaitIncidentSensorOption(t *testing.T, ctx context.Context, dir, name string) {
	t.Helper()
	eventually(t, ctx,
		`[...document.querySelector('#incident-sensor').options].some(
		   o => o.textContent.trim() === `+jsQuote(name)+`)`,
		"the filter's sensor list never caught up: a sensor that exists on the list "+
			"beside it is missing from the dropdown", dir, "incidents-no-option", 15*time.Second)
}

// longestWord picks the longest word out of a sentence, so that searching for it
// finds that sentence and nothing else by accident.
func longestWord(sentence string) string {
	best := ""
	for _, word := range splitWords(sentence) {
		if len(word) > len(best) {
			best = word
		}
	}
	return best
}

func splitWords(s string) []string {
	var out []string
	var current []rune
	for _, r := range s {
		if r == ' ' || r == '\n' || r == '\t' || r == ',' || r == '.' || r == ':' || r == '(' || r == ')' {
			if len(current) > 0 {
				out = append(out, string(current))
				current = nil
			}
			continue
		}
		current = append(current, r)
	}
	if len(current) > 0 {
		out = append(out, string(current))
	}
	return out
}

func jsQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

// seedDemo writes the demo data set into an installation's data directory.
func seedDemo(t *testing.T, bin, data string) {
	t.Helper()
	cmd := exec.Command(bin, "seed", "--demo", "--data", data)
	cmd.Stdout = testWriter{t, "seed"}
	cmd.Stderr = testWriter{t, "seed"}
	if err := cmd.Run(); err != nil {
		t.Fatalf("could not seed the demo data set: %v", err)
	}
}
