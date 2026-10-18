// Package e2e — icons in the dashboard.
//
// The API tests prove the routes and the store tests prove the tables; what neither
// can say is whether the picker in the browser builds the value the server accepts.
// It is the shape of thing that works until somebody looks at it: the value lives in
// a hidden input, the panel is drawn only when it is opened, and one of the two ways
// of choosing a symbol sends a file rather than a string.
//
// Run it with:
//
//	cd e2e && go test ./... -run 'Icon' -v
package e2e

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// pickerValue is what the form's picker currently holds, read from the page rather
// than from the API: the hidden input is what the form will send.
const pickerValue = `(() => {
  const input = document.querySelector('#modal [data-icon-field] input[type="hidden"]');
  return input ? input.value : '(no picker)';
})()`

// cardIcon is the symbol drawn on the card of a sensor called name. It answers with
// what is in the DOM, which is the whole question a browser test is for: a value that
// reached the database and is drawn nowhere is a value the user does not have.
const cardIcon = `((name) => {
  const card = [...document.querySelectorAll('#groups .card')]
    .find((c) => c.querySelector('.card-name')?.textContent.trim() === name);
  if (!card) return { found: false };
  const drawn = card.querySelector('.card-name .icon-drawn');
  const image = card.querySelector('.card-name .icon-image');
  return {
    found: true,
    drawn: drawn ? getComputedStyle(drawn).getPropertyValue('--icon').trim() : '',
    image: image ? image.getAttribute('src') : '',
  };
})('%s')`

// iconCard is what cardIcon answers with.
type iconCard struct {
	Found bool   `json:"found"`
	Drawn string `json:"drawn"`
	Image string `json:"image"`
}

// chooseBuiltin opens the picker in the dialog that is open and clicks one drawing.
func chooseBuiltin(ref string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.WaitVisible(`[data-icon-open]`),
		chromedp.Click(`[data-icon-open]`),
		chromedp.WaitVisible(fmt.Sprintf(`[data-icon-set=%q]`, ref)),
		chromedp.Click(fmt.Sprintf(`[data-icon-set=%q]`, ref)),
		chromedp.WaitNotPresent(`.icon-panel .icon-grid`),
	)
}

// newSensor fills in the sensor form and saves it. The fields are the three the
// server insists on; everything else has a default.
func newSensor(name, target string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.SetValue(`#modal-form [name="name"]`, name),
		chromedp.SetValue(`#modal-form [name="target"]`, target),
		chromedp.Click(`#modal-save`),
		chromedp.WaitNotPresent(`#modal[open]`),
	)
}

// TestThePickerPutsASymbolOnASensor walks the whole path once: choose a drawing in
// the picker, save the sensor, and read the card.
//
// The drawing is checked by the mask it is served through rather than by a class
// name, because that URL is what decides which picture appears — a card that carries
// the class and points at nothing draws a blank box, and a test that only looked for
// the class would pass.
func TestThePickerPutsASymbolOnASensor(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.WaitVisible(`#new-sensor`),
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-icon-open]`),
			// The value before anything is chosen: the empty string, which is what
			// every row had before this feature existed.
			chromedp.Poll[chromedp.Void](`document.querySelector('#modal [data-icon-field] input[type="hidden"]').value === ''`,
				chromedp.WithPollingTimeout(5*time.Second)),
			chooseBuiltin("builtin:router"),
		)
	}); err != nil {
		capture(t, ctx, dir, "icon-picker")
		t.Fatalf("the picker did not open or did not take the choice: %v", err)
	}

	if got := read[string](t, ctx, pickerValue, "what the picker holds"); got != "builtin:router" {
		t.Fatalf("the picker holds %q after choosing the router, want builtin:router", got)
	}
	// And it says what it holds. The sensor editor's picker is the symbol alone —
	// the picture is the label — so the word it is announced with is the thing to
	// check: a button that keeps saying "Choose an icon" after one was chosen is a
	// form that looks like it forgot.
	button := read[map[string]string](t, ctx, `(() => {
	  const b = document.querySelector('#modal [data-icon-open]');
	  return {
	    label: b ? b.getAttribute('aria-label') : '(none)',
	    title: b ? b.getAttribute('title') : '(none)',
	  };
	})()`, "the picker's button")
	if button["label"] != "Router" || button["title"] != "Router" {
		t.Errorf("the picker's button is announced as %q / %q, want Router", button["label"], button["title"])
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, newSensor("Picked router", "http://10.0.0.1/health"))
	}); err != nil {
		capture(t, ctx, dir, "icon-sensor-save")
		t.Fatalf("the sensor did not save: %v", err)
	}

	card := read[iconCard](t, ctx, fmt.Sprintf(cardIcon, "Picked router"), "the card of the new sensor")
	if !card.Found {
		capture(t, ctx, dir, "icon-card-missing")
		t.Fatal("the sensor has no card on the dashboard")
	}
	if !strings.Contains(card.Drawn, "/assets/icons/router.svg") {
		t.Errorf("the card draws %q, want the router drawing", card.Drawn)
	}

	// And in the window the card opens. The detail dialog is the one place a sensor
	// appears without its card around it, so a symbol that stops at the dashboard
	// leaves the window looking like a different sensor from the one it describes.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Evaluate[chromedp.Void](`(() => {
			  const card = [...document.querySelectorAll('#groups .card')]
			    .find((c) => c.querySelector('.card-name')?.textContent.trim() === 'Picked router');
			  if (card) card.click();
			})()`),
			chromedp.WaitVisible(`#detail[open] #detail-title .icon-drawn`),
		)
	}); err != nil {
		capture(t, ctx, dir, "icon-detail")
		t.Fatalf("the sensor's window does not carry the symbol: %v", err)
	}
	detailIcon := read[string](t, ctx,
		`getComputedStyle(document.querySelector('#detail-title .icon-drawn')).getPropertyValue('--icon').trim()`,
		"the symbol in the sensor's window")
	if !strings.Contains(detailIcon, "/assets/icons/router.svg") {
		t.Errorf("the window's heading draws %q, want the router drawing", detailIcon)
	}
}

// TestAnUploadedIconIsDrawnAsAnImage is the other half of the picker: a file goes up,
// comes back as an uploaded icon, and is drawn as a picture of its own rather than as
// a drawing that follows the theme.
//
// It is the one path where the dashboard sends something that is not JSON — the body
// is FormData built from the file input — so it is the one path where a mistake in the
// request would not be caught by any other test in this repository.
func TestAnUploadedIconIsDrawnAsAnImage(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	picture := writeTestPNG(t, dir)

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.WaitVisible(`#new-sensor`),
			chromedp.Click(`#new-sensor`),
			chromedp.WaitVisible(`#modal[open] [data-icon-open]`),
			chromedp.Click(`[data-icon-open]`),
			// The label and not the input: the input is hidden behind it, and WaitVisible
			// on something that is never visible waits forever.
			chromedp.WaitVisible(`.icon-upload`),
			// The file input is hidden behind the label that draws the button, which
			// is how a file input is styled without losing the browser's own picker.
			chromedp.SetUploadFiles(`[data-icon-upload]`, []string{picture}),
			// The upload is a request of its own, so the tile appears when the server
			// has answered rather than when the file was chosen.
			chromedp.Poll[chromedp.Void](`!!document.querySelector('.icon-grid-uploads [data-icon-set^="upload:"]')`,
				chromedp.WithPollingTimeout(20*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "icon-upload")
		t.Fatalf("the upload did not come back as an icon: %v", err)
	}

	ref := read[string](t, ctx,
		`document.querySelector('.icon-grid-uploads [data-icon-set^="upload:"]').dataset.iconSet`,
		"the reference of the uploaded icon")
	if ref == "" {
		t.Fatal("the uploaded tile carries no reference")
	}
	if got := read[string](t, ctx, pickerValue, "what the picker holds after the upload"); got != ref {
		t.Fatalf("the picker holds %q after uploading %q, want the new icon to be selected", got, ref)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, newSensor("Uploaded picture", "http://10.0.0.2/health"))
	}); err != nil {
		capture(t, ctx, dir, "icon-upload-save")
		t.Fatalf("the sensor did not save: %v", err)
	}

	card := read[iconCard](t, ctx, fmt.Sprintf(cardIcon, "Uploaded picture"), "the card of the new sensor")
	if !card.Found {
		t.Fatal("the sensor has no card on the dashboard")
	}
	want := "/api/icons/" + ref[len("upload:"):]
	if card.Image != want {
		t.Errorf("the card draws the image %q, want %q", card.Image, want)
	}
	if card.Drawn != "" {
		t.Errorf("the uploaded icon is drawn as a themed drawing (%q) as well", card.Drawn)
	}
}

// TestTheGroupsPanelDressesAHeading covers the third place a symbol appears: a
// group's heading, which is drawn from the settings rather than from the sensors.
//
// The colour is checked with it, because the two are one dialog and one save: a
// heading that wears the symbol but not the colour is the panel and the dashboard
// disagreeing about what was written.
func TestTheGroupsPanelDressesAHeading(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// The example sensors all carry the group "Examples", which is what makes this
	// reachable without creating a group first — and a group is only ever created by
	// typing its name on a sensor, so there is nothing to create here anyway.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#groups-panel [data-action="group-edit"]`),
			chromedp.Click(`#groups-panel [data-action="group-edit"][data-name="Examples"]`),
			chromedp.WaitVisible(`#group-form [data-icon-open]`),
			chooseBuiltin("builtin:bell"),
			chromedp.SetValue(`#group-form [name="colour"]`, "amber"),
			chromedp.Click(`#group-save`),
			chromedp.WaitNotPresent(`#modal-group[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "group-editor")
		t.Fatalf("the group editor did not save: %v\nIt said: %s", err, groupError(ctx))
	}

	// The row in the panel carries what was chosen, and that is what colours the
	// little dot beside the name in the table.
	row := read[map[string]string](t, ctx, `(() => {
	  const tr = document.querySelector('#groups-panel tr[data-name="Examples"]');
	  return { colour: tr ? tr.dataset.colour || '' : '(no row)' };
	})()`, "the group's row")
	if row["colour"] != "amber" {
		t.Errorf("the panel row carries colour %q, want amber", row["colour"])
	}

	// And the dashboard, where the heading is drawn.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/dashboard", `!!document.querySelector('#groups .group-title')`),
		)
	}); err != nil {
		capture(t, ctx, dir, "group-heading")
		t.Fatalf("the dashboard did not come back: %v", err)
	}
	// The name and not the whole heading: the heading carries how many sensors are
	// in the group as well, and that is its own element so that the two can be told
	// apart here.
	heading := read[map[string]string](t, ctx, `(() => {
	  const h = document.querySelector('#groups .group-title');
	  const icon = h ? h.querySelector('.icon-drawn') : null;
	  const name = h ? h.querySelector('.group-name') : null;
	  return {
	    text: name ? name.textContent.trim() : '(none)',
	    colour: h ? h.dataset.colour || '' : '',
	    icon: icon ? getComputedStyle(icon).getPropertyValue('--icon').trim() : '',
	  };
	})()`, "the group's heading")
	if heading["text"] != "Examples" {
		t.Fatalf("the first heading on the dashboard is %q, want Examples", heading["text"])
	}
	if heading["colour"] != "amber" {
		t.Errorf("the heading carries colour %q, want amber", heading["colour"])
	}
	if !strings.Contains(heading["icon"], "/assets/icons/bell.svg") {
		t.Errorf("the heading draws %q, want the bell", heading["icon"])
	}
}

// groupError reads the message the group dialog is showing.
func groupError(ctx context.Context) string {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`document.querySelector('#group-error')?.textContent.trim() || '(nothing)'`))
	if err != nil {
		return "(could not read it)"
	}
	return value
}

// writeTestPNG writes a small real image beside the failing test's other files. It has
// to be a real PNG: the server decodes an upload before it stores it, so a hand-made
// header would be refused by the very check the upload path exists to run.
//
// The pixels are written straight into the buffer rather than through the standard
// library's RGBA type. That package's name is one the linter this repository runs reads
// as a misspelling of "colour", so every file that imports it carries an exception;
// four lines of arithmetic are easier to read than another one of those.
func writeTestPNG(t *testing.T, dir string) string {
	t.Helper()
	const side = 32
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	for y := 0; y < side; y++ {
		for x := 0; x < side; x++ {
			at := img.PixOffset(x, y)
			img.Pix[at] = uint8(x * 8)
			img.Pix[at+1] = uint8(y * 8)
			img.Pix[at+2] = 140
			img.Pix[at+3] = 255
		}
	}
	path := filepath.Join(dir, "test-icon.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create the test image: %v", err)
	}
	defer file.Close()
	if err := png.Encode(file, img); err != nil {
		t.Fatalf("encode the test image: %v", err)
	}
	return path
}

// TestTheSiteEditorCarriesASymbol closes the loop on the third place a symbol can be
// set — and it is the one that was missing when this was first written: a site's icon
// could be chosen and stored, and nothing drew it. The row in the settings list and the
// heading in the tree are both checked here, because they are two different pieces of
// drawing code and only one of them was written first.
func TestTheSiteEditorCarriesASymbol(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// "Homelab" is one of the two places the example sensors stand on, so there is a
	// row to edit without making one first.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("monitor"),
			chromedp.WaitVisible(`#sites-panel [data-action="site-edit"]`),
			chromedp.Click(`#sites-panel [data-action="site-edit"][data-name="Homelab"]`),
			chromedp.WaitVisible(`#site-form [data-icon-open]`),
			chooseBuiltin("builtin:server"),
			chromedp.Click(`#site-save`),
			chromedp.WaitNotPresent(`#modal-site[open]`),
		)
	}); err != nil {
		capture(t, ctx, dir, "site-icon-editor")
		t.Fatalf("the site editor did not save: %v\nIt said: %s", err, siteError(ctx))
	}

	// The row in the panel. The place is drawn with a kind dot already, so the
	// drawing is looked for by the file it is served through rather than by "there is
	// an icon somewhere in this row".
	row := read[string](t, ctx, `(() => {
	  const tr = document.querySelector('#sites-panel tr[data-name="Homelab"]');
	  const icon = tr ? tr.querySelector('.icon-drawn') : null;
	  return icon ? getComputedStyle(icon).getPropertyValue('--icon').trim() : '(none)';
	})()`, "the icon on the site's row")
	if !strings.Contains(row, "/assets/icons/server.svg") {
		t.Errorf("the settings row draws %q, want the server drawing", row)
	}

	// And the heading in the tree, which is a different function in a different file.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/dashboard", `!!document.querySelector('#layout-switcher')`),
			chromedp.Click(`#layout-switcher [data-layout="tree"]`),
			chromedp.WaitVisible(`.site-node .site-head`),
		)
	}); err != nil {
		capture(t, ctx, dir, "site-icon-tree")
		t.Fatalf("the tree did not come up: %v", err)
	}
	heading := read[string](t, ctx, `(() => {
	  const head = document.querySelector('.site-node .site-head');
	  const icon = head ? head.querySelector('.icon-drawn') : null;
	  return icon ? getComputedStyle(icon).getPropertyValue('--icon').trim() : '(none)';
	})()`, "the icon on the tree's heading")
	if !strings.Contains(heading, "/assets/icons/server.svg") {
		t.Errorf("the place's heading draws %q, want the server drawing", heading)
	}
}

// siteError reads the message the site dialog is showing.
func siteError(ctx context.Context) string {
	value, err := chromedp.Run(ctx, chromedp.Evaluate[string](
		`document.querySelector('#site-error')?.textContent.trim() || '(nothing)'`))
	if err != nil {
		return "(could not read it)"
	}
	return value
}
