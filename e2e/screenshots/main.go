// Command screenshots writes the documentation's pictures of the dashboard.
//
// It is a program and not a test because a test must not write into a second
// repository. This one runs the real daemon, drives the real interface in a real
// browser and puts the PNGs into probeglass-docs/static/img. Run it by hand
// whenever the interface changes:
//
//	cd e2e && go run ./screenshots
//	make readme-images   # six of them, again, for the README
//
// The README does not keep a second set of pictures: `make readme-images` copies
// the six it shows out of the set this program just wrote, so the two cannot drift
// apart into two different dashboards.
//
// The pictures are taken against the demo data set, because a dashboard with a
// week of history is what the documentation is trying to show. It is built the
// way a person would build it — a fresh database, the first-run form, the
// one-click removal of the sensors that install came with, and then
// `seed --demo` — so that every picture is of a state somebody could reach
// themselves. There is one exception: the alert channel exists only so that the
// channels page is a picture of the feature rather than of an empty list. Nothing
// is attached to it, so nothing is ever sent through it.
package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // G505: the TOTP below is RFC 6238, whose only
	// hash is HMAC-SHA1. Nothing here hashes a password with it; that is bcrypt in
	// the product, which this program does not import.
	"encoding/base32"
	"encoding/binary"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// The sign-in the screenshot database is built with. It is not a secret worth
// keeping: the database lives in a temporary directory and goes away with it.
const (
	accountName     = "jane"
	accountPassword = "probeglass-screenshots"
)

// productModule identifies the daemon's module when looking for its directory.
const productModule = "github.com/j000k3r/probeglass"

func main() {
	out := flag.String("out", "../../probeglass-docs/static/img",
		"directory the PNGs are written to")
	browser := flag.String("browser", "", "path to a Chromium-based browser")
	flag.Parse()

	if err := run(*out, *browser); err != nil {
		fmt.Fprintf(os.Stderr, "screenshots: %v\n", err)
		os.Exit(1)
	}
}

func run(out, browser string) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if browser == "" {
		var err error
		if browser, err = findBrowser(); err != nil {
			return err
		}
	}
	// Everything below that leaves the process — the build, the seed, the daemon —
	// runs under one deadline. A build that hangs should end the program and say so
	// rather than leave a browser waiting behind it.
	procCtx, cancelProc := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelProc()

	bin, err := buildDaemon(procCtx)
	if err != nil {
		return err
	}
	data, err := os.MkdirTemp("", "probeglass-shots-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(data) }()

	port, err := freePort(procCtx)
	if err != nil {
		return err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Run one: the account, and then the one-click removal of the sample
	// sensors, exactly as a new install would do it. Keeping them would leave
	// nine cards — four from the install plus five from the seed — and a
	// screenshot of a cluttered dashboard helps nobody.
	if err := withBrowser(browser, func(ctx context.Context) error {
		return withDaemon(ctx, bin, data, port, func() error { return firstRun(ctx, base, out) })
	}); err != nil {
		return err
	}
	if err := seedDemo(procCtx, bin, data); err != nil {
		return err
	}
	// Run two: a browser that has never been to this installation before, which
	// is what makes the sign-in screenshot a screenshot of the sign-in screen
	// rather than of a dashboard that was still signed in from the run above.
	return withBrowser(browser, func(ctx context.Context) error {
		return withDaemon(ctx, bin, data, port, func() error { return capture(ctx, base, out) })
	})
}

// withBrowser opens a browser with an empty profile and closes it again.
func withBrowser(browser string, fn func(context.Context) error) error {
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), browserOptions(browser)...)
	defer cancelAlloc()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()
	if err := chromedp.Do(ctx); err != nil {
		return fmt.Errorf("start the browser: %w", err)
	}
	if err := preferDark(ctx); err != nil {
		return err
	}
	// The one dialog in the product is window.confirm(), which guards the
	// removal of the sample data, and a driver that cannot answer it would sit
	// in front of it until the phase times out.
	acceptDialogs(ctx)
	return fn(ctx)
}

// preferDark tells the browser to report a dark colour scheme, the way a machine
// whose desktop is dark reports it.
//
// The dashboard's theme setting defaults to "auto", which *is* that answer, and
// most of the pictures are taken before the one step that chooses the theme
// explicitly. Without this the whole set would come out light on a light machine
// and dark on a dark one, and the documentation would quietly change appearance
// depending on who regenerated it and which desktop they happened to be using.
func preferDark(ctx context.Context) error {
	_, err := chromedp.Call(ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
		// The feature's name is the CSS spelling and cannot be respelled for the
		// linter: it is a protocol constant, not prose.
		//nolint:misspell // prefers-color-scheme
		Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: "dark"}},
	})
	if err != nil {
		return fmt.Errorf("ask the browser for a dark colour scheme: %w", err)
	}
	return nil
}

// firstRun creates the account and clears out what the install brought with it.
// It also photographs the setup screen, which is the one screen that exists only
// here: an installation that has an account never shows it again.
func firstRun(ctx context.Context, base string, out string) error {
	if err := do(ctx, "the first-run form",
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#auth-confirm`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "setup", 1600, 1000); err != nil {
		return err
	}
	err := do(ctx, "creating the account",
		chromedp.SendKeys(`#auth-username`, accountName),
		chromedp.SendKeys(`#auth-password`, accountPassword),
		chromedp.SendKeys(`#auth-confirm`, accountPassword),
		chromedp.Click(`#auth-submit`),
		// The second step of setup is the question of how much interface to
		// show; the pictures are taken in the full one.
		chromedp.WaitVisible(`#auth-mode-step`),
		chromedp.Click(`input[name="ui-mode"][value="advanced"]`),
		chromedp.Click(`#auth-submit`),
		chromedp.WaitVisible(`#app`),
	)
	if err != nil {
		return err
	}
	// The tour offers itself here and covers the page. There is nothing to
	// point at yet, since the sample sensors are about to go, so it is dismissed
	// and photographs itself later from a dashboard that has something on it.
	if err := dismissTour(ctx); err != nil {
		return fmt.Errorf("dismiss the tour: %w", err)
	}
	return do(ctx, "removing the sample sensors",
		chromedp.Click(`[data-route="settings"]`),
		// The panel holding the sample data is one of the Monitoring category's,
		// and a fresh visit opens on Look.
		category("monitor"),
		chromedp.WaitVisible(`#examples-panel`),
		// Centred before it is pressed: a click scrolls its target into view as
		// little as it can, and the panel is below the fold, so the button would
		// end up against the window's top edge with the top bar over it.
		centerOn(`#examples-panel`),
		chromedp.Click(`#examples-panel [data-action="drop-examples"]`),
		// The panel stays after the removal now, because it is also the way
		// back, so the thing waited for is the delete button going rather than
		// the panel. What it is replaced by — the button that loads the example
		// data again — is asserted by the tour test; here only the state the
		// later screenshots need matters.
		chromedp.WaitNotPresent(`#examples-panel [data-action="drop-examples"]`),
	)
}

// capture drives the signed-in dashboard and writes one PNG per screen.
func capture(ctx context.Context, base, out string) error {
	// --- the sign-in screen --------------------------------------------------

	if err := do(ctx, "the sign-in screen",
		chromedp.Navigate(base+"/"),
		chromedp.WaitVisible(`#auth-password`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "login", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "signing in",
		chromedp.SendKeys(`#auth-username`, accountName),
		chromedp.SendKeys(`#auth-password`, accountPassword),
		chromedp.Click(`#auth-submit`),
		chromedp.WaitVisible(`#app`),
	); err != nil {
		return err
	}

	// --- the tour ------------------------------------------------------------

	// Started from Settings, because the offer it makes on a fresh install has
	// already been seen and is not made twice. Its first steps are about the
	// dashboard, so the tour switches the view back under itself — the card is the
	// only thing it draws, and what it points at has to be behind it.
	if err := do(ctx, "starting the tour",
		chromedp.Click(`[data-route="settings"]`),
		// The button that starts the tour is on the Help category, and the page
		// remembers the category it was last on — which, after the removal above,
		// is Monitoring.
		category("help"),
		chromedp.WaitVisible(`[data-action="tour"]`),
		chromedp.Click(`[data-action="tour"]`),
		chromedp.WaitVisible(`.tour-card`),
	); err != nil {
		return err
	}
	if err := advanceTour(ctx, 3); err != nil {
		return fmt.Errorf("walk the tour to a sensor: %w", err)
	}
	if err := shot(ctx, out, "tour", 1600, 1000); err != nil {
		return err
	}
	if err := dismissTour(ctx); err != nil {
		return fmt.Errorf("dismiss the tour: %w", err)
	}

	// --- the four views ------------------------------------------------------

	if err := do(ctx, "the dashboard",
		chromedp.Click(`[data-route="dashboard"]`),
		chromedp.WaitVisible(`#groups .card`),
	); err != nil {
		return err
	}
	if err := waitFor(ctx, `document.querySelectorAll('#groups .card').length >= 5`); err != nil {
		return err
	}
	if err := shot(ctx, out, "dashboard", 1600, 1000); err != nil {
		return err
	}

	// --- the header of widgets -----------------------------------------------
	//
	// The header is a list that can be arranged. Three pictures are worth having:
	// the picker window, the arranging itself, and a header that somebody has
	// actually arranged. The clock is added and taken away again so that every
	// picture below is of the default header, which is what the manual shows first.
	if err := do(ctx, "arranging the header",
		chromedp.Click(`#widgets-tools [data-widget-edit]`),
		chromedp.WaitVisible(`#widgets-tools [data-widget-pick]`),
	); err != nil {
		return err
	}
	if err := do(ctx, "opening the widget picker",
		chromedp.Click(`#widgets-tools [data-widget-pick]`),
		chromedp.WaitVisible(`#widget-picker[open] .widget-pick`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "widgets-picker", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "closing the widget picker",
		chromedp.Click(`#widget-picker [data-close]`),
	); err != nil {
		return err
	}
	if err := waitFor(ctx, `!document.querySelector('#widget-picker[open]')`); err != nil {
		return err
	}
	if err := shot(ctx, out, "widgets-edit", 1600, 1000); err != nil {
		return err
	}

	if err := do(ctx, "putting a clock on the header",
		chromedp.Click(`#widgets-tools [data-widget-pick]`),
		chromedp.WaitVisible(`#widget-picker[open] [data-widget-add="clock"]`),
		chromedp.Click(`#widget-picker [data-widget-add="clock"]`),
		chromedp.WaitVisible(`#summary [data-widget="clock"] .widget-clock`),
		chromedp.Click(`#widgets-tools [data-widget-edit]`),
	); err != nil {
		return err
	}
	// The edit bar is what tells the two states apart in a photograph, and it is
	// put on and taken off by the same click — so the picture is held back until
	// it is gone, rather than until the tiles have moved.
	if err := waitFor(ctx, `!document.querySelector('#summary .widget-bar')`); err != nil {
		return err
	}
	if err := shot(ctx, out, "widgets", 1600, 1000); err != nil {
		return err
	}

	if err := do(ctx, "taking the clock off again",
		chromedp.Click(`#widgets-tools [data-widget-edit]`),
		chromedp.Click(`[data-widget="clock"] [data-widget-remove]`),
		chromedp.Click(`#widgets-tools [data-widget-edit]`),
	); err != nil {
		return err
	}
	if err := waitFor(ctx, `!document.querySelector('#summary [data-widget="clock"]')`); err != nil {
		return err
	}

	if err := do(ctx, "a sensor's history",
		chromedp.Click(`#groups .card`),
		chromedp.WaitVisible(`#detail[open] .detail-stats`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "detail", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "closing the history", chromedp.Click(`#detail [data-close]`)); err != nil {
		return err
	}

	// --- the other three views, and the account menu -------------------------

	// Nothing has to be arranged here. `seed --demo` above already gives the
	// sample set two places to live, with real coordinates and sensors on them —
	// a homelab, a rented server, and sensors at neither — so the tree has
	// something to fold and the map has something to draw the moment the page
	// loads. Re-creating those two places through the API would be a duplicate,
	// and a screenshot of the demo set is worth more than a screenshot of an
	// arrangement invented for the camera.

	// Each view is reached through the real switcher, not by setting the
	// preference: the picture then shows what pressing the button shows.
	for _, view := range []struct {
		layout, marker, name string
		settle               time.Duration
	}{
		{layout: "tree", marker: `.site-node`, name: "dashboard-tree"},
		{layout: "wall", marker: `.wall-group .tile`, name: "dashboard-wall"},
		// The map is drawn from the data rather than from the network: the
		// coastlines come out of the bundle and no tile address is configured, so
		// there is nothing to wait for but the layout. The wait is an `after`
		// action rather than a sleep here, because the resize that takes the
		// picture re-lays the map out and asks for the view again — waiting before
		// it would wait for the wrong one.
		{layout: "map", marker: `.map-canvas`, name: "dashboard-map", settle: 3 * time.Second},
	} {
		err := do(ctx, "the "+view.layout+" view",
			chromedp.Click(`#layout-switcher [data-layout="`+view.layout+`"]`),
			chromedp.WaitVisible(view.marker),
		)
		if err != nil {
			return err
		}
		var after []chromedp.Action[chromedp.Void]
		if view.settle > 0 {
			after = append(after, chromedp.Sleep(view.settle))
		}
		if err := shot(ctx, out, view.name, 1600, 900, after...); err != nil {
			return err
		}
	}

	// A second picture of the map, taken from much closer in.
	//
	// The demo set is a homelab in Berlin and a rented server in Virginia, and the
	// one view that holds both is a view of the world — where the country borders
	// and their names are all that is drawn, because the finer lines would be a grey
	// texture at that scale. The provinces arrive a few zoom levels later, so this
	// one is taken the way somebody gets there: the wheel, with the cursor on the
	// place they keep their servers, which is what zooms on the point under it.
	if err := zoomIn(ctx, `.site-marker[data-site="Homelab"]`, 3); err != nil {
		return err
	}
	if err := shot(ctx, out, "dashboard-map-provinces", 1600, 900,
		// A moment longer than the other views: the names are measured off the page
		// and dropped again, and this is the one picture whose whole subject is them.
		chromedp.Sleep(2*time.Second)); err != nil {
		return err
	}
	if err := do(ctx, "back to the cards",
		chromedp.Click(`#layout-switcher [data-layout="grid"]`),
		chromedp.WaitVisible(`#groups .card`),
	); err != nil {
		return err
	}

	// The account menu, open, over the dashboard: the role beside the name and
	// the three switches are the whole of what the picture is for. It is opened
	// by an `after` action for the same reason as the map's wait — the menu closes
	// itself on a resize, so opening it before the viewport is set would
	// photograph a menu that had already shut.
	if err := shot(ctx, out, "user-menu", 1600, 900,
		chromedp.Click(`#user-menu-toggle`),
		chromedp.WaitVisible(`.user-menu [data-setting="theme"]`),
	); err != nil {
		return err
	}
	if err := do(ctx, "closing the account menu",
		// The CSS form, because a plain string here is a text search rather than
		// a selector: it would match the comment in index.html that mentions
		// "#summary" as well as the element, and a comment has no boxes to
		// click. See the same click in shell_test.go.
		chromedp.Click(chromedp.CSS(`#summary`))); err != nil {
		return err
	}
	if err := waitFor(ctx, `!document.querySelector('.user-menu')`); err != nil {
		return err
	}

	// One channel, so that the page is a picture of the feature and not of an
	// empty list. It is created through the API because the form is not the
	// point here, and nothing is ever routed through it.
	if err := do(ctx, "creating an alert channel", eval(`
		fetch('/api/channels', {
			method: 'POST',
			headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({
				name: 'Ops webhook',
				type: 'webhook',
				enabled: true,
				config: { url: 'https://example.org/probeglass' },
			}),
		})`)); err != nil {
		return err
	}
	if err := do(ctx, "the incident log",
		chromedp.Click(`[data-route="incidents"]`),
		chromedp.Reload(),
		chromedp.WaitVisible(`#incident-list tr`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "incidents", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "the channels page",
		chromedp.Click(`[data-route="channels"]`),
		chromedp.WaitVisible(`#channel-list .channel`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "alerts", 1600, 1000); err != nil {
		return err
	}

	// --- the forms -----------------------------------------------------------

	if err := do(ctx, "the sensor form",
		chromedp.Click(`[data-route="dashboard"]`),
		chromedp.WaitVisible(`#new-sensor`),
		chromedp.Click(`#new-sensor`),
		chromedp.WaitVisible(`#modal[open] [name="target"]`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "sensor-form", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "closing the sensor form", chromedp.Click(`#modal [data-close]`)); err != nil {
		return err
	}
	// The recipe under a channel type's fields — the part of this dialog that says
	// where a bot token comes from and where a chat id comes from. Photographed on
	// Telegram, which is the first one most people set up and the one with the most
	// to explain; it is drawn by the type selected here, with no other choice made.
	if err := do(ctx, "the channel form",
		chromedp.Click(`[data-route="channels"]`),
		chromedp.WaitVisible(`#new-channel`),
		chromedp.Click(`#new-channel`),
		chromedp.WaitVisible(`#channel-fields .channel-setup ol li`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "channel-setup", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "closing the channel form", chromedp.Click(`#modal [data-close]`)); err != nil {
		return err
	}

	// --- settings, one category at a time ------------------------------------
	//
	// The page shows one of its five categories, so a picture of a panel is a
	// picture of the category the panel lives in, and the walk goes through them
	// in the order the page keeps them. Every step that wants a panel says which
	// category it is in, and each picture is of the whole category rather than of
	// a panel cut out of a long page.

	if err := do(ctx, "the settings page, on Look",
		chromedp.Click(`[data-route="settings"]`),
		category("look"),
		// Dark is a setting like any other, so it is chosen the way a person
		// chooses it rather than forced on the browser: the select's value is
		// set and the change event it would have raised is raised.
		eval(`(() => {
			const el = document.querySelector('[data-setting="theme"]');
			el.value = 'dark';
			el.dispatchEvent(new Event('change', { bubbles: true }));
		})()`),
	); err != nil {
		return err
	}
	// The setting is stored on the account and applied to the document, and the
	// document is what is photographed.
	if err := waitFor(ctx, `document.documentElement.dataset.theme === 'dark'`); err != nil {
		return err
	}
	if err := shot(ctx, out, "settings-look", 1600, 1000); err != nil {
		return err
	}

	// Monitoring: what the installation holds, and the list the header is built
	// from. The two places made earlier for the tree and the map are still there
	// and still carry their sensors, so the list of places is a picture of a list
	// in use rather than of an empty one.
	if err := do(ctx, "the Monitoring category", category("monitor")); err != nil {
		return err
	}
	if err := shot(ctx, out, "settings-monitor", 1600, 1000); err != nil {
		return err
	}
	if err := shot(ctx, out, "settings-sites", 1600, 700,
		centerOn(`#sites-panel`),
	); err != nil {
		return err
	}

	// The backup panel, caught in the state somebody deciding about a backup is
	// in: the history scope chosen, so the period menu is on screen next to it
	// rather than greyed out behind the default. The two menus are what the
	// documentation is talking about, so they are what the picture has to show.
	if err := do(ctx, "the Backup category",
		category("backup"),
		eval(`(() => {
			const scope = document.querySelector('#backup-scope');
			scope.value = 'history';
			scope.dispatchEvent(new Event('change', { bubbles: true }));
		})()`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "backup-panel", 1600, 760); err != nil {
		return err
	}

	// Access: the password, the second factor, the API key, and the accounts. All
	// of it is one category now, so the accounts picture below is on the same
	// screen as the second factor rather than a scroll away from it. The category
	// as a whole is not photographed separately: the accounts panel is the part of
	// it the manual has something to say about, and a picture of a second factor
	// beside a list of accounts is a picture of a list of accounts with less room.
	if err := do(ctx, "the Access category", category("access")); err != nil {
		return err
	}

	// Two more accounts, so that the table shows what a table of people looks
	// like rather than the single row of a fresh install.
	for _, person := range []struct{ name, role string }{
		{"anna", "operator"},
		{"ben", "viewer"},
	} {
		err := do(ctx, "adding "+person.name,
			chromedp.Click(`[data-action="user-new"]`),
			chromedp.WaitVisible(`#modal[open] [name="username"]`),
			chromedp.SendKeys(`#modal [name="username"]`, person.name),
			chromedp.SendKeys(`#modal [name="password"]`, accountPassword),
			chromedp.Click(`#modal input[name="role"][value="`+person.role+`"]`),
			chromedp.Click(`#modal-save`),
			chromedp.WaitNotPresent(`#modal[open]`),
		)
		if err != nil {
			return err
		}
	}

	// Saving an account rebuilds the whole settings panel, so the button this is
	// about to click is replaced the moment the dialog above closes. Waiting for
	// the rebuild — three rows can only be there once the render that knows about
	// ben has been painted — keeps the click off a node that is on its way out.
	if err := waitFor(ctx, `document.querySelectorAll('#users-panel .user-row').length === 3`); err != nil {
		return err
	}

	// The second factor of the account the pictures are taken with, enrolled for
	// real: the code that confirms it is computed here from the secret the server
	// handed out, which is the only way to end up with an account whose second
	// factor is genuinely on and a table row that says so.
	if err := do(ctx, "enrolling the second factor",
		chromedp.Click(`[data-action="totp-start"]`),
		chromedp.WaitVisible(`#modal[open] .qr`),
	); err != nil {
		return err
	}
	// An <img> that is in the document is not necessarily an <img> that has been
	// decoded, and a photograph of a QR code that has not been decoded is a
	// photograph of an empty box.
	if err := waitFor(ctx, `document.querySelector('#modal .qr').naturalWidth > 0`); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	if err := shot(ctx, out, "two-factor", 1600, 1000); err != nil {
		return err
	}

	secret, err := text(ctx, `#modal .secret-key`)
	if err != nil {
		return err
	}
	code, err := totpCode(strings.TrimSpace(secret), time.Now())
	if err != nil {
		return fmt.Errorf("compute a code for the enrolment: %w", err)
	}
	if err := do(ctx, "confirming the second factor",
		chromedp.SendKeys(`#modal [name="code"]`, code),
		chromedp.Click(`#modal-save`),
		chromedp.WaitVisible(`#modal[open] .recovery-codes li`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "recovery-codes", 1600, 1000); err != nil {
		return err
	}
	if err := do(ctx, "acknowledging the recovery codes", chromedp.Click(`#modal-save`)); err != nil {
		return err
	}
	if err := waitFor(ctx, `!document.querySelector('#modal[open]')`); err != nil {
		return err
	}
	// The toasts that said the accounts were created sit over the list for a few
	// seconds. The list is the subject of this picture, so they are waited out
	// rather than photographed on top of it.
	if err := waitFor(ctx, `!document.querySelector('#toasts .toast')`); err != nil {
		return err
	}
	if err := shot(ctx, out, "users", 1600, 900, centerOn(`#users-panel`)); err != nil {
		return err
	}

	// The panel holding the example data, on its own — framed rather than photographed
	// at the top of the category, because it is the last panel of Monitoring and the
	// one control a new installation is most likely to be looking for.
	//
	// The picture is taken in the loaded state rather than the empty one. The
	// panel has two states now — it offers the data back when there is none — and
	// the loaded one is what the page in the manual is about: it shows how many
	// example sensors there are and the button that removes them.
	//
	// The seed has already put example data back, so the loaded state is the one
	// the panel opens in and the restore button is not on screen to click. Both
	// directions are exercised instead: remove it, wait for the button that loads
	// it, press that one, and photograph the result. That is also what keeps this
	// picture honest — it is a photograph of data that was reloaded through the
	// interface, not of the seed's own leftovers.
	if err := do(ctx, "the Monitoring category again", category("monitor")); err != nil {
		return err
	}
	// The two directions of the same panel, and both presses need the panel in
	// front of the window: it is the last of Monitoring and sits below the fold,
	// so a click that scrolls its target into view by as little as it can leaves
	// the buttons against the top edge with the sticky bar over them.
	if err := do(ctx, "removing the example data",
		centerOn(`#examples-panel`),
		chromedp.Click(`#examples-panel [data-action="drop-examples"]`),
		chromedp.WaitVisible(`#examples-panel [data-action="restore-examples"]`),
	); err != nil {
		return err
	}
	if err := do(ctx, "loading the example data back",
		centerOn(`#examples-panel`),
		chromedp.Click(`#examples-panel [data-action="restore-examples"]`),
		chromedp.WaitVisible(`#examples-panel [data-action="drop-examples"]`),
	); err != nil {
		return err
	}
	if err := waitFor(ctx, `!document.querySelector('#toasts .toast')`); err != nil {
		return err
	}
	if err := shot(ctx, out, "example-data", 1600, 700,
		centerOn(`#examples-panel`),
	); err != nil {
		return err
	}

	// --- the diagnostics -----------------------------------------------------

	// Maintenance: a window over the present, for two of the invented sensors — one
	// that answers and one that does not — so the picture shows both halves at once:
	// what the badge looks like on a healthy card, and what it is for on a card that
	// is red.
	//
	// Made here rather than seeded into the demo data on purpose. The demo is what
	// somebody installs, and a maintenance window that arrived with a fresh
	// installation would be a feature nobody asked for. The picture is evidence; the
	// demo is a product.
	//
	// The reload afterwards is not tidiness either. The page was loaded before these
	// windows existed and holds the list it was given; a same-document navigation to
	// the same page returns that list rather than asking for it again, so without this
	// the panel below photographs an installation that is empty while the server says
	// otherwise — which is exactly what it did, and why this shot took three attempts
	// to get right.
	if err := do(ctx, "a maintenance window over the present",
		chromedp.Evaluate[chromedp.Void](`
	  (async () => {
	    const now = Date.now();
	    const summary = await (await fetch('/api/summary')).json();
	    for (const c of summary.sensors.filter((s) => s.origin === 'example').slice(0, 2)) {
	      const r = await fetch('/api/maintenance', {
	        method: 'POST',
	        headers: { 'Content-Type': 'application/json' },
	        body: JSON.stringify({
	          sensor_id: c.id,
	          kind: 'once',
	          starts_at: new Date(now - 3600e3).toISOString(),
	          ends_at: new Date(now + 3 * 3600e3).toISOString(),
	          timezone: 'UTC',
	          note: 'Firmware update',
	          enabled: true,
	        }),
	      });
	      if (!r.ok) throw new Error('POST /api/maintenance: ' + r.status);
	    }
	  })()`, chromedp.EvalAwaitPromise),
		chromedp.Reload(),
		// The reload lands back on whatever route the walk-through was last on, which
		// is the settings page — so the dashboard is a click away rather than a URL.
		chromedp.Click(`[data-route="dashboard"]`),
		chromedp.WaitVisible(`.card`),
	); err != nil {
		return err
	}
	// The cards first, while the dashboard is what is on screen.
	if err := shot(ctx, out, "maintenance-cards", 1600, 620); err != nil {
		return err
	}
	// The monitor category and not just the settings route: the page opens on whatever
	// category was last chosen, and a panel that is in another one is hidden — which
	// WaitVisible reads as absent.
	if err := do(ctx, "the maintenance panel",
		chromedp.Click(`[data-route="settings"]`),
		category("monitor"),
		chromedp.WaitVisible(`#windows-panel`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "maintenance-panel", 1600, 560,
		centerOn(`#windows-panel`),
	); err != nil {
		return err
	}
	if err := do(ctx, "running the doctor",
		category("help"),
		// Same reason as the example panel above: it is below the fold, and it
		// is framed rather than photographed at the top of the category, so it
		// is centred before it is pressed.
		centerOn(`[data-panel="diagnostics"]`),
		chromedp.Click(`[data-action="doctor-run"]`),
		chromedp.WaitVisible(`#doctor-result .doctor-summary`),
		chromedp.Click(`#doctor-result summary`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "settings-doctor", 1600, 1100); err != nil {
		return err
	}

	// The About panel, which is where an installation says which build it is running.
	// Its first row is the channel — a release, the dev image, or a working tree — and
	// it is the one fact a container cannot work out for itself, so it is the one that
	// has to be in a picture.
	if err := do(ctx, "the About panel",
		centerOn(`[data-panel="about"]`),
	); err != nil {
		return err
	}
	if err := shot(ctx, out, "settings-about", 1600, 700,
		centerOn(`[data-panel="about"]`),
	); err != nil {
		return err
	}
	return nil
}

// --- the browser -----------------------------------------------------------

// do runs one named step of the walk-through under its own deadline. Naming the
// steps is what makes a failure say which screen it was looking at; the deadline
// is what makes it say anything at all, because chromedp's waiting actions block
// until their context ends and this program's context ends when the browser does.
func do(ctx context.Context, name string, actions ...chromedp.Action[chromedp.Void]) error {
	step, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	if err := chromedp.Do(step, actions...); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// stepTimeout is how long one screen of the walk-through may take. It is generous
// — several of them wait for the daemon to answer — and it exists to turn a stuck
// step into a sentence rather than into a program that never returns.
const stepTimeout = 30 * time.Second

// eval runs a snippet in the page for its effect.
func eval(expression string) chromedp.Action[chromedp.Void] {
	return func(ctx context.Context, _ *chromedp.Target) (chromedp.Void, error) {
		_, err := chromedp.Run(ctx, chromedp.Evaluate[any](expression))
		return chromedp.Void{}, err
	}
}

// category opens one of the settings page's categories.
//
// The page shows one category at a time, so a picture of a panel is a picture of
// the category the panel lives in — and every step that wants a panel has to say
// which. It is written out here rather than shared with the tests because this is
// a different package, and the two have nothing else to say to each other.
//
// Two things about it are not obvious, and both cost an afternoon of a stuck step
// when they are left out.
//
// The page is put back at the top before the tab is pressed. The tab row is at the
// top of the page and this program scrolls: the picture before this one framed a
// panel in the middle of the window. A click scrolls what it is about to press
// into view as little as it can, which leaves the tab against the window's top
// edge — and the top bar is sticky and covering exactly that strip. The press
// lands on the bar, the step reports success, and the page is still on the
// category before. The failure surfaces one step later, as a panel that is not
// there because its category was never opened.
//
// The switch is waited for rather than assumed, for the same reason: a click that
// misses is not an error, so the only way to know the category opened is to look.
func category(key string) chromedp.Action[chromedp.Void] {
	selector := fmt.Sprintf(`#settings-body .settings-tabs [data-category=%q]`, key)
	ready := fmt.Sprintf(
		`(() => { const tab = document.querySelector(%q); return !!tab && !tab.hidden; })()`,
		selector)
	chosen := fmt.Sprintf(
		`(() => { const tab = document.querySelector(%q); return !!tab && tab.getAttribute('aria-selected') === 'true'; })()`,
		selector)
	return func(ctx context.Context, _ *chromedp.Target) (chromedp.Void, error) {
		// Back to the top first, so the tab row is in the middle of the window
		// rather than scrolled to its top edge by the click itself.
		if _, err := chromedp.Run(ctx, chromedp.Evaluate[any](`window.scrollTo(0, 0)`)); err != nil {
			return chromedp.Void{}, err
		}
		if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](ready,
			chromedp.WithPollingTimeout(15*time.Second))); err != nil {
			return chromedp.Void{}, fmt.Errorf("the %s category never appeared on the settings page: %w", key, err)
		}
		// Clicked from one expression rather than with chromedp.Click, which resolves
		// the tab to a node handle and then acts on it. The settings page draws twice —
		// once when the route changes, with whatever it already had, and again when the
		// accounts, the places and the windows come back — so a handle taken during the
		// first can be stale by the second, and the failure is a CDP error about a node
		// that is not there rather than anything about categories. Looking the tab up,
		// clicking it and letting go, in one turn of the page's own loop, leaves nothing
		// to go stale.
		press := fmt.Sprintf(`(() => {
		  const tab = document.querySelector(%q);
		  if (!tab) throw new Error('no such category tab');
		  tab.click();
		})()`, selector)
		if err := chromedp.Do(ctx, chromedp.Evaluate[chromedp.Void](press)); err != nil {
			return chromedp.Void{}, fmt.Errorf("could not choose the %s category: %w", key, err)
		}
		if err := chromedp.Do(ctx, chromedp.Poll[chromedp.Void](chosen,
			chromedp.WithPollingTimeout(10*time.Second))); err != nil {
			return chromedp.Void{}, fmt.Errorf(
				"pressed the %s tab and the page stayed on another category: %w", key, err)
		}
		return chromedp.Void{}, nil
	}
}

// centerOn frames a panel in the middle of the viewport, which is what a picture
// of one panel wants. Scrolling it to the top would put its heading behind the
// sticky top bar, and the heading is the first thing the picture is for — this is
// the same reason the panel-by-panel pictures were scrolled this way when the
// settings page was one long column.
func centerOn(selector string) chromedp.Action[chromedp.Void] {
	return eval(fmt.Sprintf(
		`document.querySelector(%q).scrollIntoView({block: 'center'})`, selector))
}

// acceptDialogs answers every window.confirm() with yes, for as long as the
// context lives. The dialog blocks the page until somebody answers it, so a
// driver that ignores it waits forever on a click that is not the problem.
func acceptDialogs(ctx context.Context) {
	dialogues := chromedp.Events(ctx, page.JavascriptDialogOpening)
	go func() {
		for _, err := range dialogues {
			if err != nil {
				return
			}
			_, _ = chromedp.Call(ctx, page.HandleJavaScriptDialog,
				page.HandleJavaScriptDialogParams{Accept: true})
		}
	}()
}

// shot photographs the page as it is. The viewport is set per picture, because
// the settings page needs height that the dashboard does not, and anything in
// `after` runs once it has taken effect — a page that has to be scrolled to its
// subject has to be scrolled *after* the resize, because the resize re-lays the
// page out and takes the old scroll position with it.
func shot(ctx context.Context, out, name string, width, height int,
	after ...chromedp.Action[chromedp.Void]) error {
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(int64(width), int64(height))); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	// A resize re-renders the dashboard, so the layout is given a moment to
	// settle before it is photographed.
	time.Sleep(400 * time.Millisecond)
	if len(after) > 0 {
		step, cancel := context.WithTimeout(ctx, stepTimeout)
		err := chromedp.Do(step, after...)
		cancel()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		time.Sleep(250 * time.Millisecond)
	}
	png, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	path := filepath.Join(out, name+".png")
	if err := os.WriteFile(path, png, 0o640); err != nil {
		return err
	}
	fmt.Printf("  %-22s %d KB\n", name+".png", len(png)/1024)
	return nil
}

// zoomIn turns the map's wheel with the cursor on one of its markers, ticks deep.
//
// The cursor is put on the marker as it is on screen rather than at a coordinate
// worked out here: the map zooms on the point under the pointer, so this is also
// the check that the place it is aiming at is where it looks — a pin that ends up
// somewhere else takes the zoom with it. Real wheel events, because the handler
// they reach is the one a person's wheel reaches.
func zoomIn(ctx context.Context, marker string, ticks int) error {
	at, err := chromedp.Run(ctx, chromedp.Evaluate[[]float64](
		`(() => {
			const el = document.querySelector(`+strconv.Quote(marker)+`);
			if (!el) return null;
			const box = el.getBoundingClientRect();
			return [box.left + box.width / 2, box.top + box.height / 2];
		})()`))
	if err != nil {
		return fmt.Errorf("finding the marker to zoom in on: %w", err)
	}
	if len(at) != 2 {
		return fmt.Errorf("the map has no %s marker to zoom in on", marker)
	}
	step, cancel := context.WithTimeout(ctx, stepTimeout)
	defer cancel()
	// The wheel is sent as a raw command rather than through the generated
	// Input.dispatchMouseEvent parameters, for one dull reason: those parameters
	// leave a zero `deltaX` out of the JSON, and Chrome refuses a mouseWheel event
	// that does not carry both deltas. Sending both, with a horizontal one that is
	// zero and stays zero, is what a wheel that is only turned does.
	wheel := chromedp.Func(func(ctx context.Context, target *chromedp.Target) error {
		return target.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type":   "mouseWheel",
			"x":      at[0],
			"y":      at[1],
			"deltaX": 0,
			"deltaY": -100,
		}, nil)
	})
	for i := 0; i < ticks; i++ {
		// No waiting between the ticks: the map's view is moved to the new zoom
		// before the handler returns and the marker stays under the cursor, so the
		// next tick is anchored on the same place.
		if _, err := chromedp.Run(step, wheel); err != nil {
			return fmt.Errorf("zooming the map in: %w", err)
		}
	}
	return nil
}

// advanceTour walks the tour forward by n steps, waiting for each one to be
// drawn. The tour paints its words before it moves the ring into place, and a
// step is worth photographing only once the highlight has settled.
func advanceTour(ctx context.Context, steps int) error {
	for i := 0; i < steps; i++ {
		before, err := text(ctx, ".tour-title")
		if err != nil {
			return err
		}
		step, cancel := context.WithTimeout(ctx, stepTimeout)
		err = chromedp.Do(step, chromedp.Click(`.tour-card [data-tour="next"]`))
		cancel()
		if err != nil {
			return err
		}
		if err := waitFor(ctx, fmt.Sprintf(
			`document.querySelector('.tour-title').textContent !== %q`, before)); err != nil {
			return fmt.Errorf("step %d of the tour did not arrive: %w", i+1, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
	return nil
}

func dismissTour(ctx context.Context) error {
	if err := do(ctx, "dismissing the tour",
		chromedp.WaitVisible(`.tour-card`),
		chromedp.Click(`.tour-card [data-tour="skip"]`),
	); err != nil {
		return err
	}
	return waitFor(ctx, `!document.querySelector('.tour-card')`)
}

// text reads the trimmed text of one element.
func text(ctx context.Context, selector string) (string, error) {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](fmt.Sprintf(
		`(document.querySelector(%q) || {}).textContent || ''`, selector)))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}

func waitFor(ctx context.Context, expression string) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expression))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("gave up waiting for %s", expression)
}

// findBrowser returns the first Chromium-based browser on this machine.
func findBrowser() (string, error) {
	candidates := []string{
		os.Getenv("PROBEGLASS_E2E_BROWSER"),
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/usr/bin/google-chrome",
		"/usr/bin/chromium",
		"/usr/bin/chromium-browser",
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		//nolint:gosec // G703: the path is one of the candidates listed above, or the
		// one the operator named in PROBEGLASS_E2E_BROWSER. There is no request and
		// no untrusted input anywhere near this program.
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no Chrome or Chromium found; pass -browser or set PROBEGLASS_E2E_BROWSER")
}

// browserOptions is the browser the browser test would have used, plus the size
// the pictures are taken at: a window big enough for the two-column dashboard, at
// a scale factor of one, so that the PNG is 1600 pixels wide and not 3200.
func browserOptions(browser string) []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	return append(opts,
		chromedp.ExecPath(browser),
		chromedp.Flag("hide-scrollbars", true),
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(1600, 1000),
	)
}

// --- the daemon ------------------------------------------------------------

// withDaemon runs the daemon for as long as fn takes. The port is reserved by the
// caller, so that the two runs this program makes share an address and the
// browser's session survives the restart in the middle.
func withDaemon(ctx context.Context, bin, data string, port int, fn func() error) error {
	//nolint:gosec // G204: the command is the binary this program just built, and
	// every argument after it is a literal or the port it chose itself.
	cmd := exec.CommandContext(ctx, bin, "serve",
		"--listen", fmt.Sprintf("127.0.0.1:%d", port),
		"--data", data)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the daemon: %w", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
		if err != nil {
			return err
		}
		if resp, err := http.DefaultClient.Do(req); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return fn()
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("the daemon did not become healthy on %s", base)
}

// buildDaemon compiles the daemon into a temporary file. The module is found by
// walking up from wherever this was started rather than by counting directory
// levels, so that `go run ./screenshots` works from e2e and from the repository
// root alike.
func buildDaemon(ctx context.Context) (string, error) {
	root, err := moduleRoot()
	if err != nil {
		return "", err
	}
	out := filepath.Join(os.TempDir(), "probeglass-screenshots")
	//nolint:gosec // G204: a fixed `go build` of a path this program found by
	// walking up to the module root; there is no input from anywhere else.
	cmd := exec.CommandContext(ctx, "go", "build", "-o", out, "./cmd/probeglass")
	cmd.Dir = root
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go build: %w", err)
	}
	return out, nil
}

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if declaresModule(filepath.Join(dir, "go.mod"), productModule) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod for %s above %s", productModule, dir)
		}
		dir = parent
	}
}

// declaresModule reports whether a go.mod's own module line names exactly this
// module. The comparison is on the whole line because the e2e module's path
// begins with the product's, and a substring test would settle for it.
func declaresModule(path, module string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "module "+module {
			return true
		}
	}
	return false
}

func seedDemo(ctx context.Context, bin, data string) error {
	fmt.Println("seeding the demo data set")
	cmd := exec.CommandContext(ctx, bin, "seed", "--demo", "--data", data)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("seed --demo: %w", err)
	}
	return nil
}

func freePort(ctx context.Context) (int, error) {
	var listen net.ListenConfig
	ln, err := listen.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = ln.Close() }()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("the reserved port is %T, not a TCP address", ln.Addr())
	}
	return addr.Port, nil
}

// --- the second factor -----------------------------------------------------

// totpCode computes the six-digit code for a secret, so that the enrolment
// photographed here ends with the second factor actually switched on. It is RFC
// 6238 with the parameters the product uses, written out rather than imported:
// this program is in its own module and has no business depending on the product.
func totpCode(secret string, at time.Time) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", err
	}
	var counter [8]byte
	// A clock before 1970 would give a negative step, and folding that round to the
	// far end of the 64-bit range would produce a six-digit code out of nowhere. RFC
	// 4226 counts from zero, so a moment before it is the first step.
	step := at.Unix() / 30
	if step < 0 {
		step = 0
	}
	binary.BigEndian.PutUint64(counter[:], uint64(step))

	mac := hmac.New(sha1.New, key)
	mac.Write(counter[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}
