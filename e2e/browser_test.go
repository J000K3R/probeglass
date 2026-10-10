// Package e2e drives the dashboard in a real browser.
//
// Every other test in this repository talks to the API. That is cheap and it
// covers a lot, but it cannot see the one failure that actually shipped: the
// dashboard loaded perfectly — /api/session and /api/summary both answered 200
// — while a full-height loading spinner sat on top of it and never went away.
// The cause was a single missing CSS rule, [hidden] losing to
// `.boot { display: grid }`, and it is invisible to every sensor that does not
// render a page.
//
// So this test renders a page, and it asserts on what a person would see
// rather than on what the server sent.
//
// It lives in its own Go module so that a browser driver never enters the
// product's dependency tree. Run it with:
//
//	cd e2e && go test ./... -v
//
// A browser is required. If none is found the test skips, unless
// WEATHERGLASS_E2E_REQUIRE_BROWSER=1, which turns that skip into a failure. CI
// sets it so that a missing browser can never quietly mean "no coverage".
package e2e

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/j000k3r/weatherglass/internal/model"
)

const (
	// Long enough for a cold start on a loaded CI runner.
	startupTimeout = 60 * time.Second

	// Per phase, not for the whole test: the timeout is applied after the
	// browser is up, so it costs only the phase that is stuck.
	phaseTimeout = 45 * time.Second

	// The form enforces minlength="8".
	testPassword = "weatherglass-e2e-password"
	// The account the first run creates. It is the name every acknowledgement
	// this test makes is recorded under, and the name it has to sign back in with.
	testUser = "e2e-operator"
)

// TestDashboardIsActuallyUsable walks the first-run path the way a person does
// and then checks what is on the screen.
func TestDashboardIsActuallyUsable(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()

	// Deliberately without a timeout. The first call that touches this context
	// starts the browser and binds its life to that context, so a deadline here
	// would kill the browser rather than the action — and would leave nothing
	// alive to take a screenshot with afterwards.
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Start the browser before anything else, so that the phase timeouts below
	// have a context they can safely be derived from.
	if err := chromedp.Do(ctx); err != nil {
		t.Fatalf("could not start the browser: %v", err)
	}

	// Subscribe to the console before the first navigation and read the queue
	// only at the end. Console subscribes when it returns, so this is what
	// keeps an error thrown during the very first page load from being missed.
	// Drained by a goroutine rather than ranged over inline: ranging blocks
	// until the context ends, which would hang the test instead of reporting it.
	consoleCtx, stopConsole := context.WithCancel(ctx)
	defer stopConsole()

	var (
		mu         sync.Mutex
		pageErrors []string
		netNotes   []string
	)
	messages := chromedp.Console(consoleCtx)
	go func() {
		for m, err := range messages {
			if err != nil {
				return // the context ended; nothing left to collect
			}
			switch {
			case m.IsException():
				mu.Lock()
				pageErrors = append(pageErrors, "uncaught: "+m.Text)
				mu.Unlock()
			case m.Type == chromedp.ConsoleError && m.Source == "":
				// console.error from the page's own code.
				mu.Lock()
				pageErrors = append(pageErrors, "console.error: "+m.Text)
				mu.Unlock()
			case m.Source == "security":
				// A blocked script or a refused connection, which is how a
				// Content Security Policy problem shows up.
				mu.Lock()
				pageErrors = append(pageErrors, "security: "+m.Text)
				mu.Unlock()
			case m.Type == chromedp.ConsoleError:
				// Browser-log noise, typically a resource that did not load.
				// Worth printing, but it is a different kind of defect and
				// failing here would report it as a broken dashboard.
				mu.Lock()
				netNotes = append(netNotes, m.Source+": "+m.Text)
				mu.Unlock()
			}
		}
	}()

	//nolint:misspell // "artifacts" is the spelling GitHub uses for the uploaded files
	screenshotDir := os.Getenv("WEATHERGLASS_E2E_ARTIFACTS")
	if screenshotDir == "" {
		screenshotDir = t.TempDir()
	}

	// --- phase 1: first run, create the account, land on the dashboard ------

	err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.EmulateViewport(1280, 900),
			chromedp.Navigate(base+"/"),

			// Waiting for the password field rather than for the page covers
			// the first paint as well.
			chromedp.WaitVisible(`#auth-password`),
			// The first run asks who this account is for as well as for the
			// password: every acknowledgement the dashboard records from here on
			// names the account it belongs to, so there has to be one.
			chromedp.SendKeys(`#auth-username`, testUser),
			chromedp.SendKeys(`#auth-password`, testPassword),
			chromedp.SendKeys(`#auth-confirm`, testPassword),
			chromedp.Click(`#auth-submit`),

			// Setup is two steps: the credentials are followed by the question of
			// how much interface to show. Clicking submit there only reaches
			// the second step, so the mode has to be chosen and submit pressed
			// again — otherwise the wait below times out on a card that is
			// working exactly as designed.
			chromedp.WaitVisible(`#auth-mode-step`),
			chromedp.Click(`input[name="ui-mode"][value="advanced"]`),
			chromedp.Click(`#auth-submit`),

			// Wait for the shell, not for the spinner to leave. Waiting on
			// #boot looks like the assertion this test exists for and is
			// worthless: showAuth hid the spinner before the click, so it is
			// already gone and the wait returns immediately — letting the
			// checks below run while the password is still being hashed. That
			// raced, and produced exactly the screenshot of a stuck auth card
			// that this test is supposed to catch.
			chromedp.WaitVisible(`#app`),

			// The tour offers itself once the dashboard has something to point
			// at, and it covers the whole page while it is up. Waiting for it and
			// skipping it here is not incidental: without it every click below
			// races a full-screen overlay, and "the ? button did nothing" would
			// be reported against a button that was merely covered. Skipping also
			// records the tour as seen, so the reload in phase 2 does not bring it
			// back.
			chromedp.WaitVisible(`.tour-card`),
			chromedp.Click(`[data-tour="skip"]`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "first-run")
		t.Fatalf("after creating the account the dashboard did not appear: %v\n"+
			"the auth card said: %s\n"+
			"see the screenshot in %s", err, authError(ctx), screenshotDir)
	}

	// The tour has taken its layer out of the document, not merely hidden it, so
	// this polls for the node rather than waiting for it to stop being visible:
	// chromedp's WaitNotVisible needs a node to look at and would report the
	// successful dismissal as a failure. Every click below would otherwise race
	// a full-screen overlay, and "the ? button did nothing" would be reported
	// against a button that was merely covered.
	if gone, err := chromedp.Run(ctx, chromedp.Poll[bool](`!document.querySelector('.tour-card')`)); err != nil {
		capture(t, ctx, screenshotDir, "tour-still-up")
		t.Fatalf("the guided tour was dismissed but is still on screen: %v", err)
	} else if !gone {
		capture(t, ctx, screenshotDir, "tour-still-up")
		t.Fatal("the guided tour was dismissed but its layer is still in the document")
	}

	// The spinner is what has to be gone, and it is only meaningful to ask
	// once the shell is up: a full-height spinner left on screen hides the
	// whole application while every request returns 200.
	if present, err := chromedp.Run(ctx, chromedp.Evaluate[bool](
		`document.getElementById('boot').getClientRects().length > 0`)); err != nil {
		t.Errorf("could not check the loading spinner: %v", err)
	} else if present {
		capture(t, ctx, screenshotDir, "spinner-still-visible")
		t.Errorf("the loading spinner is still on screen over a dashboard that has loaded: " +
			"an author CSS rule is outranking the user-agent [hidden] rule")
	}

	// The shell, and only the shell's dashboard tab. Every screen is queried, so
	// that a failure names what is on display rather than what is missing: the
	// interesting part of "the auth card came back" is that it is the auth card.
	const screens = "#boot, #auth, #app, .view"
	if visible := visibleIDs(t, ctx, screens); !equal(visible, []string{"app", "view-dashboard"}) {
		capture(t, ctx, screenshotDir, "wrong-elements-visible")
		t.Errorf("visible after setup = %v, want [app view-dashboard].\n"+
			"Something that should be hidden is on screen. The usual cause is an author "+
			"CSS rule setting `display` on an element the app hides with the hidden "+
			"attribute, which outranks the user-agent [hidden] rule.", visible)
	}

	// --- phase 2: a reload must not throw the user back to the login form --

	// This is the other half of the original report: the password was accepted
	// and the field came back anyway.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Navigate(base+"/"),
			chromedp.WaitVisible(`#app`),
			chromedp.WaitNotVisible(`#auth`),
			chromedp.WaitNotVisible(`#boot`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "reload")
		t.Fatalf("after a reload the dashboard was not reached: %v\n"+
			"the session cookie did not survive the round trip", err)
	}

	// --- phase 3: the field help has to be on screen, not merely built ---

	// Every field used to carry its explanation as a paragraph. They now sit
	// behind the "?" beside each label, and that panel is appended to the open
	// dialog rather than to <body> — a modal <dialog> lives in the top layer,
	// so a panel left on the body is painted underneath the dialog's backdrop
	// and never seen. The button then looks dead while the DOM says it worked,
	// which is why this asks what is under the pointer and not what exists.
	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal .help-dot`),
			chromedp.Click(`#modal .help-dot`),
			chromedp.WaitVisible(`.help-pop`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "field-help")
		t.Fatalf("the \"?\" beside a field did not open its explanation: %v", err)
	}

	// getClientRects and elementFromPoint rather than a computed display: the
	// panel computes as a visible grid even while something is painted over it.
	help := `(() => {
		const p = document.querySelector('.help-pop');
		if (!p) return 'no panel was built';
		if (p.getClientRects().length === 0) return 'the panel has no box on screen';
		const r = p.getBoundingClientRect();
		const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
		if (at !== p && !p.contains(at)) {
			return 'the panel is painted over by ' + (at ? at.tagName + '.' + at.className : 'nothing');
		}
		const d = document.querySelector('dialog[open]');
		if (!d) return 'no dialog is open while the panel is up';
		if (!d.contains(p)) return 'the panel is outside the open dialog';
		if (p.textContent.trim() === '') return 'the panel is empty';
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](help)); err != nil {
		t.Errorf("could not inspect the field help: %v", err)
	} else if problem != "" {
		capture(t, ctx, screenshotDir, "field-help-not-visible")
		t.Errorf("the field explanation is not readable: %s.\n"+
			"The panel is built in tooltip.js; it has to be appended inside the open "+
			"dialog, because a modal dialog paints its backdrop over anything left on "+
			"the body.", problem)
	}

	// Every type the server knows has to be choosable in the form. The list
	// lives twice — model.SensorTypes in Go, SENSOR_TYPES in app.js — and only
	// one of the two is what an operator ever sees. A type that is implemented,
	// documented and unreachable is the failure this catches, and it is not
	// hypothetical: it was found that way, with a dashboard served from a build
	// old enough to predate docker and snmp entirely. The order is the form's
	// business, so the two lists are compared as sets.
	wantTypes := make([]string, 0, len(model.SensorTypes))
	for _, st := range model.SensorTypes {
		wantTypes = append(wantTypes, string(st))
	}
	slices.Sort(wantTypes)
	offered, err := chromedp.Run(ctx, chromedp.Evaluate[string](`(() => {
		const type = document.querySelector('#sensor-type');
		if (!type) return '';
		return [...type.options].map((o) => o.value).sort().join(',');
	})()`))
	if err != nil {
		t.Errorf("could not read the sensor type choices: %v", err)
	} else if offered != strings.Join(wantTypes, ",") {
		capture(t, ctx, screenshotDir, "sensor-types")
		t.Errorf("the form offers the sensor types %q, want %q: a type the server "+
			"implements has to be reachable from the form that configures it",
			offered, strings.Join(wantTypes, ","))
	}

	// The Docker sensor's health check has to be switchable off from the form,
	// and the way to do it has to be on screen. A container without a
	// HEALTHCHECK can never report healthy, so a form that offers only the three
	// statuses — or that leaves the empty choice to be found by clearing a text
	// box — hands the operator a card that is red for good.
	dockerHealth := `(() => {
		const type = document.querySelector('#sensor-type');
		if (!type) return 'the form has no sensor type select';
		type.value = 'docker';
		type.dispatchEvent(new Event('change', { bubbles: true }));
		const field = document.querySelector('[name="opt_expect_health"]');
		if (!field) return 'the docker form has no expect_health field';
		if (field.tagName !== 'SELECT') return 'expect_health is a ' + field.tagName + ', not a choice';
		const options = [...field.options];
		const off = options.find((o) => o.value === '');
		if (!off) return 'expect_health offers no way to switch the check off';
		if (!off.textContent.trim()) return 'the switched-off choice has no label';
		if (!options.some((o) => o.value === 'healthy')) return 'expect_health no longer offers healthy';
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](dockerHealth)); err != nil {
		t.Errorf("could not inspect the docker health field: %v", err)
	} else if problem != "" {
		capture(t, ctx, screenshotDir, "docker-health-field")
		t.Errorf("the docker sensor's health check cannot be switched off from the form: %s", problem)
	}

	// The answer to the Test button is the one message that must not be missed,
	// and it arrives while the sensor form is open — the moment a toast left on
	// the page is painted behind the dialog's backdrop. The same question as the
	// help panel above, by the same means: what is under the pointer.
	toastForm := `(() => {
		const type = document.querySelector('#sensor-type');
		if (!type) return 'the form has no sensor type select';
		type.value = 'http';
		type.dispatchEvent(new Event('change', { bubbles: true }));
		const fill = (name, value) => {
			const field = document.querySelector('#modal [name="' + name + '"]');
			if (!field) return false;
			field.value = value;
			field.dispatchEvent(new Event('input', { bubbles: true }));
			field.dispatchEvent(new Event('change', { bubbles: true }));
			return true;
		};
		if (!fill('name', 'toast probe')) return 'the form has no name field';
		// Port 9 is a black hole: nothing listens there, so the probe fails and
		// the message the operator has to see is a failure.
		if (!fill('target', 'http://127.0.0.1:9/')) return 'the form has no target field';
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](toastForm)); err != nil {
		t.Errorf("could not fill in the sensor form: %v", err)
	} else if problem != "" {
		t.Fatalf("the Test button cannot be exercised: %s", problem)
	}

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#modal-test`),
			chromedp.WaitVisible(`#modal .toast`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "test-toast-missing")
		t.Fatalf("the sensor form's Test button produced no message: %v", err)
	}

	toastShown := `(() => {
		const n = document.querySelector('#modal .toast');
		if (!n) return 'no message was built inside the dialog';
		if (n.getClientRects().length === 0) return 'the message has no box on screen';
		const r = n.getBoundingClientRect();
		const at = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2);
		if (at !== n && !n.contains(at)) {
			return 'the message is painted over by ' + (at ? at.tagName + '.' + at.className : 'nothing');
		}
		if (n.textContent.trim() === '') return 'the message is empty';
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](toastShown)); err != nil {
		t.Errorf("could not inspect the test message: %v", err)
	} else if problem != "" {
		capture(t, ctx, screenshotDir, "test-toast-behind-dialog")
		t.Errorf("the result of the Test button is not readable: %s.\n"+
			"The toast is built in app.js; while a dialog is open it has to be appended "+
			"inside it, because a modal dialog paints its backdrop over anything left on "+
			"the page.", problem)
	}

	// And the same button on a form the server will not probe at all. This is the
	// case that was reported as "the Test button does nothing": the form was
	// incomplete, the server answered with the reason, and the reason was written
	// into one thin line at the bottom of a long dialog — the one place in it
	// nobody looks. Every outcome now writes that line *and* raises a message.
	//
	// The message from the run above is waited out rather than assumed away. It
	// stays on screen for a few seconds, and a check that matched it would pass
	// with the second press having done nothing whatever.
	if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](
		`document.querySelectorAll('#modal .toast').length === 0`,
		chromedp.WithPollingTimeout(10*time.Second))); err != nil {
		t.Logf("the message from the first press never went away: %v", err)
	}

	unfillable := `(() => {
		const field = document.querySelector('#modal [name="target"]');
		if (!field) return 'the form has no target field';
		field.value = '';
		field.dispatchEvent(new Event('input', { bubbles: true }));
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](unfillable)); err != nil {
		t.Errorf("could not empty the target field: %v", err)
	} else if problem != "" {
		t.Fatalf("the incomplete form cannot be built: %s", problem)
	}

	err = withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#modal-test`),
			chromedp.WaitVisible(`#modal .toast`),
		)
	})
	if err != nil {
		capture(t, ctx, screenshotDir, "test-silent-on-a-bad-form")
		t.Fatalf("pressing Test on a form the server refuses produced no message: %v.\n"+
			"This is the press that was reported as doing nothing: the answer goes to the "+
			"form's own error line, which is at the bottom of a dialog that is taller than "+
			"the window.", err)
	}

	refused := `(() => {
		const said = document.querySelector('#modal .toast');
		const line = document.querySelector('#modal-error');
		if (said.textContent.trim() === '') return 'the message is empty';
		if (said.getClientRects().length === 0) return 'the message has no box on screen';
		if (line.textContent.trim() === '') return 'the dialog left its own error line empty';
		return '';
	})()`
	if problem, err := chromedp.Run(ctx, chromedp.Evaluate[string](refused)); err != nil {
		t.Errorf("could not inspect the refusal: %v", err)
	} else if problem != "" {
		capture(t, ctx, screenshotDir, "test-refusal-unreadable")
		t.Errorf("the refusal of an incomplete form is not readable: %s", problem)
	}

	if err := chromedp.Do(ctx, chromedp.Click(`#modal [data-close]`)); err != nil {
		t.Logf("could not close the sensor form again: %v", err)
	}

	// Let the console settle: the last messages arrive with the events that
	// the reload produced.
	stopConsole()
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, n := range netNotes {
		t.Logf("browser log (not a failure): %s", n)
	}
	if len(pageErrors) > 0 {
		t.Errorf("the dashboard reported %d error(s) while loading:\n  %s",
			len(pageErrors), strings.Join(pageErrors, "\n  "))
	}
}

// withTimeout runs one phase under a deadline. The context is derived from the
// live browser context, so a phase that times out leaves the browser running
// and the parent context usable — which is what makes the screenshot below
// possible in exactly the case where it matters.
func withTimeout(ctx context.Context, fn func(context.Context) error) error {
	tctx, cancel := context.WithTimeout(ctx, phaseTimeout)
	defer cancel()
	return fn(tctx)
}

// --- assertions ------------------------------------------------------------

// authError returns what the auth card is complaining about. The app explains
// its own failures on that screen, and that sentence is worth far more in a
// failure report than the timeout that the timeout produced.
func authError(ctx context.Context) string {
	msg, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`(document.getElementById('auth-error') || {}).textContent || ''`))
	if err != nil {
		return "(could not be read: " + err.Error() + ")"
	}
	if msg == "" {
		return "(empty)"
	}
	return msg
}

// visibleIDs returns the ids of the elements matching sel that a person can
// actually see. getClientRects, not getComputedStyle: an element inside a
// hidden parent still computes its own display as `grid`, so checking that
// would call a thoroughly invisible tab bar visible.
func visibleIDs(t *testing.T, ctx context.Context, sel string) []string {
	t.Helper()
	expr := fmt.Sprintf(`Array.from(document.querySelectorAll(%q))
		.filter(el => el.getClientRects().length > 0)
		.map(el => el.id)`, sel)
	ids, err := chromedp.Run(ctx, chromedp.Evaluate[[]string](expr))
	if err != nil {
		t.Fatalf("could not read the visibility of %q: %v", sel, err)
	}
	return ids
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// capture writes a screenshot next to the test log. When this runs in CI
// nobody can look at the browser, and a picture of the stuck spinner says more
// than a stack trace does.
func capture(t *testing.T, ctx context.Context, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Logf("could not create %s: %v", dir, err)
		return
	}
	shot, err := chromedp.Run(ctx, chromedp.FullScreenshot(90))
	if err != nil {
		t.Logf("screenshot failed: %v", err)
		return
	}
	path := filepath.Join(dir, name+".png")
	if err := os.WriteFile(path, shot, 0o644); err != nil {
		t.Logf("could not write %s: %v", path, err)
		return
	}
	t.Logf("screenshot: %s", path)
}

// --- the browser -----------------------------------------------------------

// findBrowser returns the path to a Chromium-based browser, or skips — unless
// CI has asked for the skip to be a failure.
func findBrowser(t *testing.T) string {
	t.Helper()

	var candidates []string
	if p := os.Getenv("WEATHERGLASS_E2E_BROWSER"); p != "" {
		candidates = append(candidates, p)
	}
	candidates = append(candidates,
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
	)
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			candidates = append(candidates, p)
		}
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			t.Logf("using browser: %s", c)
			return c
		}
	}

	// Returning no path is not giving up: chromedp then looks for a browser
	// itself, in the places an ordinary Linux install puts one.
	if p, err := exec.LookPath("chromedriver"); err == nil {
		t.Logf("no browser binary found; falling back to chromedriver at %s and "+
			"letting chromedp locate the browser itself", p)
		return ""
	}

	if os.Getenv("WEATHERGLASS_E2E_REQUIRE_BROWSER") == "1" {
		t.Fatal("no Chromium-based browser found, and WEATHERGLASS_E2E_REQUIRE_BROWSER=1: " +
			"this job exists to render the dashboard, so a run without a browser is not a pass")
	}
	t.Skip("no Chromium-based browser found; install one, or point WEATHERGLASS_E2E_BROWSER at it")
	return ""
}

func browserOptions(browser string) []chromedp.ExecAllocatorOption {
	// Start from the defaults, which already cover headless mode, skipping the
	// first-run dialog and disabling the GPU.
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		// Runners execute as root, where the sandbox refuses to start, and give
		// the container a /dev/shm far too small for Chrome. Both flags are the
		// standard cure and cost nothing on a normal machine.
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
	)
	if browser != "" {
		opts = append(opts, chromedp.ExecPath(browser))
	}
	return opts
}

// --- the server under test -------------------------------------------------

// buildBinary compiles the daemon, so that the test exercises the real entry
// point rather than a handler assembled by the test.
func buildBinary(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("locate the module root: %v", err)
	}
	out := filepath.Join(t.TempDir(), "weatherglass")

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/weatherglass")
	cmd.Dir = root
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, combined)
	}
	return out
}

func startServer(t *testing.T, bin string) string {
	t.Helper()
	return startServerIn(t, bin, t.TempDir())
}

// startServerIn is startServer over a data directory the caller chose.
//
// It exists for the tests that have to put something in the database before the
// daemon opens it — `seed --demo` writes through a second process and cannot run
// while the first one holds the file.
func startServerIn(t *testing.T, bin, data string) string {
	t.Helper()

	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := exec.Command(bin, "serve",
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--data", data)
	cmd.Stdout = testWriter{t, "server"}
	cmd.Stderr = testWriter{t, "server"}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start the server: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	deadline := time.Now().Add(startupTimeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return base
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the server did not become healthy within %s", startupTimeout)
	return ""
}

// freePort asks the kernel for a port and gives it straight back. There is a
// race between the release and the daemon's bind, but on a CI runner with an
// idle loopback interface it does not bite, and the alternative — parsing the
// port back out of the log — ties the test to a log format.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

// testWriter sends the daemon's output to the test log, which is what makes a
// failing run diagnosable from the CI transcript alone.
type testWriter struct {
	t    *testing.T
	what string
}

func (w testWriter) Write(p []byte) (int, error) {
	if line := strings.TrimRight(string(p), "\n"); line != "" {
		w.t.Logf("%s: %s", w.what, line)
	}
	return len(p), nil
}
