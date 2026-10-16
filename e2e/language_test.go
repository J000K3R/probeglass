// Package e2e — switching the interface language.
//
// Every other test in this suite reads the dashboard in English, which is the
// language it is written in and the one the message catalogue falls back to.
// That is also what makes a broken translation invisible from the outside: `t()`
// answers with the English sentence for any key a dictionary is missing, and an
// English sentence in the middle of a German page is a sentence, not a blank.
// The unit tests in internal/web compare the dictionaries key by key and find
// exactly that; what they cannot say is whether the wiring between a dictionary
// and the screen works — whether the picker offers the language, whether choosing
// it reaches the server, whether the page in front of you changes, and whether it
// is still changed once the poll has rebuilt the page underneath it.
//
// The German is never spelled out here. What this test is about is that a word
// arrives and stays, in every part of the page that draws words a different way;
// which German word is the dictionaries' business, and the tests in internal/web
// are the ones that read them.
//
// Run it with:
//
//	cd e2e && go test ./... -run Language -v
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// languagePage is the page as a reader sees it: what the picker offers, which
// entry is chosen, and four words taken from four places that draw them in four
// different ways.
type languagePage struct {
	Names  []string `json:"names"`  // the picker's entries, as written
	Values []string `json:"values"` // their values, which is what is stored
	Chosen string   `json:"chosen"` // the value of the selected entry
	// Title is the heading of the settings page. It is written into index.html
	// and rewritten in place by applyTranslations, through its data-i18n attribute.
	Title string `json:"title"`
	// Tab is a navigation tab, the same mechanism one level out on the shell —
	// which is the half of the page a view-only redraw would leave behind.
	Tab string `json:"tab"`
	// Panel is the heading of the appearance panel, built by renderSettings in
	// JavaScript. Nothing can go back and rewrite it; only a redraw reaches it.
	Panel string `json:"panel"`
	// what the document says it is, which is what a screen reader and every
	// translation extension go by.
	Lang string `json:"lang"`
}

// The picker is asked for by its place in the settings page rather than by
// `select[data-setting="language"]` alone: the account menu carries a switch of
// the same name, and a selector that means two things means whichever of them
// happens to be in the document first.
const readLanguagePage = `(() => {
  const select = document.querySelector('#settings-body [data-panel="appearance"] select[data-setting="language"]');
  const options = select ? [...select.options] : [];
  const text = (sel) => {
    const el = document.querySelector(sel);
    return el ? el.textContent.trim() : '';
  };
  return {
    names: options.map((o) => o.textContent.trim()),
    values: options.map((o) => o.value),
    chosen: select ? select.value : '',
    title: text('#view-settings h1'),
    tab: text('#tabs [data-route="settings"] [data-i18n]'),
    panel: text('#settings-body [data-panel="appearance"] h2'),
    lang: document.documentElement.lang,
  };
})()`

// menuWords is the account menu, read the way a reader does: the labels on its
// switches and the words on its two buttons.
type menuWords struct {
	Labels []string `json:"labels"` // the switches' names, in order
	Items  []string `json:"items"`  // the buttons' words, in order
	Names  []string `json:"names"`  // the language entries, as written
	Chosen string   `json:"chosen"` // the selected language
}

const readMenuWords = `(() => {
  const menu = document.querySelector('.user-menu');
  const select = menu && menu.querySelector('select[data-setting="language"]');
  return {
    labels: menu ? [...menu.querySelectorAll('.user-menu-field > span')].map((s) => s.textContent.trim()) : [],
    items: menu ? [...menu.querySelectorAll('.user-menu-item')].map((b) => b.textContent.trim()) : [],
    names: select ? [...select.options].map((o) => o.textContent.trim()) : [],
    chosen: select ? select.value : '',
  };
})()`

// openTheAccountMenu opens the account menu and waits until it is drawn. It is
// closed again with Escape, which is one of the two ways a popover goes away and
// the one that leaves nothing for a later step to trip over.
func openTheAccountMenu(t *testing.T, ctx context.Context, dir, what string) menuWords {
	t.Helper()
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.Poll[chromedp.Void](`!!document.querySelector('.user-menu .user-menu-field')`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "account-menu-"+what)
		t.Fatalf("the account menu did not open: %v", err)
	}
	return read[menuWords](t, ctx, readMenuWords, "the account menu")
}

func closeTheAccountMenu(t *testing.T, ctx context.Context, dir, what string) {
	t.Helper()
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.KeyEvent("\x1b"))
	}); err != nil {
		capture(t, ctx, dir, "account-menu-close-"+what)
		t.Fatalf("could not press Escape: %v", err)
	}
	eventually(t, ctx, `!document.querySelector('.user-menu')`,
		"Escape did not close the account menu", dir, "account-menu-open-"+what, 10*time.Second)
}

// caughtUp is the predicate "the settings page has caught up with the language
// that was chosen": the document says it is that language, and the appearance
// panel — the one panel this page is read through — is no longer drawn in the
// words it had before.
//
// Waiting on the document's own lang alone would not do. setLanguage writes it
// before anything is redrawn, so a page that has just changed its mind and
// nothing else satisfies it; and the panel is not redrawn until the settings it
// is built from have come back from the server, which is a round trip later. The
// previous heading is passed in rather than a German one, so that this waits for
// the redraw without this test having to know a single translated word.
func caughtUp(code, was string) string {
	return `(() => {
	  if (document.documentElement.lang !== ` + jsQuote(code) + `) return false;
	  const h2 = document.querySelector('#settings-body [data-panel="appearance"] h2');
	  return !!h2 && h2.textContent.trim() !== ` + jsQuote(was) + `;
	})()`
}

// choosing is the select being changed the way a person's click on the list
// changes it: the value set and the change event let out. app.js listens for
// exactly that on #settings-body and in the account menu, and both go through the
// one write path.
func choosing(where, code string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.SetValue(where, code),
		chromedp.Poll[chromedp.Void](`document.documentElement.lang === `+jsQuote(code),
			chromedp.WithPollingTimeout(10*time.Second)),
	)
}

// goTo leaves one view for another and waits for the new one to be drawn. The
// page is rebuilt from scratch on every route change, so this is the blunt way to
// force the rebuild that a language has to survive.
func goTo(route, ready string) chromedp.Action[chromedp.Void] {
	return chromedp.Steps(
		chromedp.Evaluate[chromedp.Void](`location.hash = `+jsQuote(route)),
		chromedp.Poll[chromedp.Void](ready, chromedp.WithPollingTimeout(15*time.Second)),
	)
}

// TestTheLanguageSwitchReachesThePage walks the whole of it once: what the
// picker offers, what choosing does to the page, that the choice survives the
// page being rebuilt and the browser being reloaded, and that it can be undone.
func TestTheLanguageSwitchReachesThePage(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	firstRun(t, ctx, base, dir)

	// --- the settings page, on the panel the switch sits in ------------------
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("look"),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body [data-panel="appearance"] select[data-setting="language"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-picker-missing")
		t.Fatalf("the settings page never drew a language picker: %v", err)
	}

	english := read[languagePage](t, ctx, readLanguagePage, "the settings page")
	englishMenu := openTheAccountMenu(t, ctx, dir, "in-english")
	closeTheAccountMenu(t, ctx, dir, "in-english")

	// --- what the picker offers ----------------------------------------------
	//
	// Under its own name, not under its code. "Deutsch" is the whole point: the
	// reader who needs that entry is the one who cannot read the rest of the
	// list, and "DE" is a two-letter puzzle between two words. The same list is
	// in the account menu, written by a different module, and it has to be the
	// same list — two pickers offering two spellings of one language is worse
	// than one picker.
	if len(english.Values) < 2 {
		t.Fatalf("the picker offers %v. A dashboard with one language has no business "+
			"showing a picker at all, and this test has nothing to switch", english.Values)
	}
	if !equal(english.Names, englishMenu.Names) {
		capture(t, ctx, dir, "language-two-spellings")
		t.Errorf("the settings page writes the languages as %v and the account menu as "+
			"%v. Both read the same list out of the same module, so a difference here "+
			"means one of them is keeping its own copy.", english.Names, englishMenu.Names)
	}
	for i, name := range english.Names {
		if name == "" {
			capture(t, ctx, dir, "language-unnamed")
			t.Errorf("the picker offers an entry with no name at all")
			continue
		}
		if name == strings.ToUpper(english.Values[i]) {
			capture(t, ctx, dir, "language-coded")
			t.Errorf("the picker offers the language %q as %q — a code rather than a "+
				"name. Each language is named in its own language, which is what "+
				"`names` in i18n.js is for.", english.Values[i], name)
		}
	}
	if !contains(english.Names, "Deutsch") {
		capture(t, ctx, dir, "language-no-german")
		t.Errorf("the picker offers %v, and none of them is Deutsch — the language this "+
			"release added, and the one entry that has to be readable by somebody who "+
			"cannot read the others", english.Names)
	}
	if english.Chosen != "en" {
		t.Errorf("a fresh installation opens on the language %q, want en: the "+
			"documentation, the installer and the release notes are in English, so "+
			"that is where a first-time reader starts", english.Chosen)
	}

	// The three landmarks the rest of this test compares against. If one of them
	// moved, the comparison stops meaning anything, and saying so beats a screen
	// full of "still reads """.
	if english.Title == "" || english.Tab == "" || english.Panel == "" {
		capture(t, ctx, dir, "language-english-baseline")
		t.Fatalf("the English page did not have the three headings this test reads: %+v.\n"+
			"One of them has moved, and until this list is updated the test cannot "+
			"tell a translation from an element that is not there any more.", english)
	}

	// --- switching, from the settings page -----------------------------------
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			choosing(`#settings-body [data-panel="appearance"] select[data-setting="language"]`, "de"),
			chromedp.Poll[chromedp.Void](caughtUp("de", english.Panel),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-not-applied")
		t.Fatalf("choosing German did not reach the page: %v", err)
	}
	german := read[languagePage](t, ctx, readLanguagePage, "the settings page in German")

	if german.Chosen != "de" {
		t.Errorf("the picker shows %q as the chosen language after German was chosen",
			german.Chosen)
	}
	// Not decoration, and not read back from the picker: `lang` on the document is
	// what a screen reader, a hyphenation dictionary and a translation extension
	// go by, and all three are wrong on a page that says English and reads German.
	if german.Lang != "de" {
		t.Errorf("the page is in German and the document still says it is written in %q",
			german.Lang)
	}
	// Three words from three different ways of drawing one, and each of them is
	// a separate way for a translation not to arrive.
	if german.Title == english.Title {
		capture(t, ctx, dir, "language-heading-unchanged")
		t.Errorf("the settings heading still reads %q. It is written into index.html "+
			"and rewritten by applyTranslations through its data-i18n attribute, so "+
			"this is the shell having not been rewritten at all.", german.Title)
	}
	if german.Tab == english.Tab {
		capture(t, ctx, dir, "language-tab-unchanged")
		t.Errorf("the navigation tab still reads %q. Same mechanism as the heading, one "+
			"level out: a redraw of the view alone rewrites neither, so the tab is "+
			"what says whether the whole document was translated or only the part of "+
			"it under #settings-body.", german.Tab)
	}
	if german.Panel == english.Panel {
		capture(t, ctx, dir, "language-panel-unchanged")
		t.Errorf("the appearance panel still reads %q. That heading is built by "+
			"renderSettings and carries no attribute anything can rewrite, so it is "+
			"only ever reached by a redraw — this is what a switch that changes the "+
			"shell and not the view looks like.", german.Panel)
	}

	// --- the account menu, which is where the same switch also lives --------
	//
	// The menu is built when it is opened and not before, so opening it now is a
	// different question from the three above: it asks whether a module that
	// read the language at draw time gets the new one, rather than whether a page
	// drawn earlier was rewritten.
	germanMenu := openTheAccountMenu(t, ctx, dir, "in-german")
	if !equal(germanMenu.Names, german.Names) {
		t.Errorf("the account menu offers the languages as %v while the settings page "+
			"offers %v", germanMenu.Names, german.Names)
	}
	if germanMenu.Chosen != "de" {
		t.Errorf("the account menu opened in German still shows %q as the chosen "+
			"language", germanMenu.Chosen)
	}
	if equal(germanMenu.Items, englishMenu.Items) {
		capture(t, ctx, dir, "language-menu-unchanged")
		t.Errorf("the account menu still says %v after the language was switched.\n"+
			"The menu is built from the dictionary when it is opened, so it should "+
			"never be able to be stale — unless it was opened before the switch and "+
			"left standing.", germanMenu.Items)
	}
	closeTheAccountMenu(t, ctx, dir, "in-german")

	// --- switching from inside the menu --------------------------------------
	//
	// The menu carries the switch itself, and the menu is drawn earlier than the
	// change: it is built when it is opened and not again. Choosing from inside
	// it is the one path where the control and the words it is written in are the
	// same object — and the path a reader actually takes, because the menu is one
	// click away from anywhere.
	//
	// The switch is back to English, so that what the menu is expected to say
	// next is the English list already in hand. Opening it freshly first is what
	// makes the check mean something: the menu has to be seen German before it can
	// be seen turning English.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Click(`#user-menu-toggle`),
			chromedp.Poll[chromedp.Void](`!!document.querySelector('.user-menu .user-menu-field')`,
				chromedp.WithPollingTimeout(10*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-menu-reopen")
		t.Fatalf("the account menu did not open again: %v", err)
	}
	beforeSwitch := read[menuWords](t, ctx, readMenuWords, "the account menu just before switching inside it")
	if beforeSwitch.Chosen != "de" {
		capture(t, ctx, dir, "language-menu-not-german")
		t.Fatalf("the account menu opened showing %q as the chosen language, want de; "+
			"without that, the check below would pass on a menu that had never been "+
			"German", beforeSwitch.Chosen)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, choosing(`.user-menu select[data-setting="language"]`, "en"))
	}); err != nil {
		capture(t, ctx, dir, "language-menu-switch")
		t.Fatalf("choosing English in the account menu did not take: %v", err)
	}
	// The menu is still open — nothing closes it, and the control that was just
	// used is inside it — so what is read next is the menu that was on screen
	// while the language changed underneath it.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Poll[chromedp.Void](`(() => {
		  const menu = document.querySelector('.user-menu');
		  if (!menu) return false;
		  const item = menu.querySelector('.user-menu-item');
		  return !!item && item.textContent.trim() === `+jsQuote(englishMenu.Items[0])+`;
		})()`, chromedp.WithPollingTimeout(15*time.Second)))
	}); err != nil {
		capture(t, ctx, dir, "language-menu-stale")
		t.Fatalf("the account menu kept reading %v after the language was switched from "+
			"inside it, want %v.\nThe menu is built when it is opened, and the switch "+
			"it carries is offered in the menu itself — so a switch that redraws the "+
			"page and not the menu leaves the one thing the reader is looking at "+
			"written in the language they just left.", beforeSwitch.Items, englishMenu.Items)
	}
	afterMenu := read[menuWords](t, ctx, readMenuWords, "the account menu after switching inside it")
	if !equal(afterMenu.Items, englishMenu.Items) {
		capture(t, ctx, dir, "language-menu-partly-stale")
		t.Errorf("switching back to English inside the account menu left its buttons "+
			"reading %v, want %v", afterMenu.Items, englishMenu.Items)
	}
	closeTheAccountMenu(t, ctx, dir, "after-switching-inside")

	// Back to German, from the settings page, for the checks that follow.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			choosing(`#settings-body [data-panel="appearance"] select[data-setting="language"]`, "de"),
			chromedp.Poll[chromedp.Void](caughtUp("de", english.Panel),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-german-again")
		t.Fatalf("could not go back to German: %v", err)
	}
	german = read[languagePage](t, ctx, readLanguagePage, "the settings page in German again")

	// --- and it stays switched ------------------------------------------------
	//
	// The page is rebuilt from scratch on every poll and every route change, and
	// a language that survives the click and not the rebuild is the kind of wrong
	// no screenshot shows. Leaving the settings page and coming back is the blunt
	// way to force both.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/dashboard", `!!document.querySelector('#view-dashboard')`),
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("look"),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body [data-panel="appearance"] h2')`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-round-trip")
		t.Fatalf("could not leave the settings page and come back: %v", err)
	}
	after := read[languagePage](t, ctx, readLanguagePage, "the settings page after a round trip")
	if after.Title != german.Title || after.Tab != german.Tab || after.Panel != german.Panel {
		capture(t, ctx, dir, "language-lost-on-rebuild")
		t.Errorf("after leaving the settings page and coming back the headings read "+
			"%q, %q and %q, want %q, %q and %q.\nThe language was applied to a page "+
			"that was then rebuilt without it.", after.Title, after.Tab, after.Panel,
			german.Title, german.Tab, german.Panel)
	}

	// --- and it is remembered on the server -----------------------------------
	//
	// A reload is a fresh module with no memory of the click: if the choice only
	// ever lived in the browser, this is where it goes back to English.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			chromedp.Reload(),
			chromedp.Poll[chromedp.Void](`document.documentElement.lang === "de"`,
				chromedp.WithPollingTimeout(20*time.Second)),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body [data-panel="appearance"] h2')`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-forgotten")
		t.Fatalf("after a reload the dashboard is no longer in German: %v.\nThe "+
			"choice belongs with the account and is meant to follow its reader to "+
			"every device, so it has to be on the server and not in the page.", err)
	}
	reloaded := read[languagePage](t, ctx, readLanguagePage, "the settings page after a reload")
	if reloaded.Title != german.Title || reloaded.Panel != german.Panel {
		capture(t, ctx, dir, "language-not-read-back")
		t.Errorf("after a reload the headings read %q and %q, want %q and %q",
			reloaded.Title, reloaded.Panel, german.Title, german.Panel)
	}

	// --- and switching back is not one-way ------------------------------------
	//
	// The cheap way to make everything above pass is to translate once and never
	// look back: a setLanguage that only ever moves forward leaves somebody who
	// chose German by accident stuck in it.
	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			choosing(`#settings-body [data-panel="appearance"] select[data-setting="language"]`, "en"),
			chromedp.Poll[chromedp.Void](caughtUp("en", german.Panel),
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "language-no-way-back")
		t.Fatalf("could not switch back to English: %v", err)
	}
	back := read[languagePage](t, ctx, readLanguagePage, "the settings page in English again")
	if back.Title != english.Title || back.Tab != english.Tab || back.Panel != english.Panel {
		capture(t, ctx, dir, "language-back-incomplete")
		t.Errorf("switching back to English gave %q, %q and %q, want %q, %q and %q",
			back.Title, back.Tab, back.Panel, english.Title, english.Tab, english.Panel)
	}
	if back.Chosen != "en" {
		t.Errorf("after switching back the picker shows %q as the chosen language", back.Chosen)
	}
	if back.Lang != "en" {
		t.Errorf("after switching back to English the document still says it is written "+
			"in %q", back.Lang)
	}
}

// ---------------------------------------------------------------------------
// the whole page, rather than a sample of it
// ---------------------------------------------------------------------------

// catalogueEntry matches one `'key': 'text',` line in a shipped catalogue. The
// indentation is part of the pattern for the reason it is in internal/web: both
// files write every entry at two spaces, and the prose comments above them quote
// keys while explaining them.
var catalogueEntry = regexp.MustCompile(`(?m)^  '([^']+)': '(.*)',$`)

// fetchCatalogue reads a shipped dictionary back out of the running server.
//
// Off the server rather than off the disk, because the question is what the browser
// was given: a file that is in the working tree but not in the embedded set would be
// a dictionary nobody loads, and reading it here would hide exactly that.
func fetchCatalogue(t *testing.T, base, path string) map[string]string {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a read-only body on a test connection
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d; the dictionaries are meant to be servable without a session", path, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	out := map[string]string{}
	for _, m := range catalogueEntry.FindAllStringSubmatch(string(body), -1) {
		out[m[1]] = m[2]
	}
	if len(out) == 0 {
		t.Fatalf("%s held no message at all; the pattern this test reads it with has "+
			"gone out of step with the file's shape", path)
	}
	return out
}

// everyWordInTheDocument is every text node under <body>, hidden or not, plus the
// three attributes applyTranslations writes into — each tagged with the chain of
// ids it sits under.
//
// Hidden elements are included on purpose, and that is the whole point of doing it
// this way. Half of what this test found sat behind something: the sign-in screen
// behind the dashboard, the picker's rows behind a dialog nobody had opened, the
// close button on a dialog that was closed. Each of them is a word the next person
// to open that thing reads, and a test that only looked at what was on screen would
// have called the page translated while four English words waited to be found.
const everyWordInTheDocument = `(() => {
  const SEP = '\u0001';
  const where = (el) => {
    const parts = [];
    for (let n = el; n && n !== document.body; n = n.parentElement) {
      if (n.id) parts.unshift('#' + n.id);
      else if (typeof n.className === 'string' && n.className.trim())
        parts.unshift('.' + n.className.trim().split(/\s+/)[0]);
    }
    return parts.slice(-5).join(' > ');
  };
  const out = [];
  const walk = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  let node;
  while ((node = walk.nextNode())) {
    const t = (node.textContent || '').trim().replace(/\s+/g, ' ');
    if (t) out.push(t + SEP + where(node.parentElement));
  }
  for (const [name, label] of [['placeholder','placeholder'],['title','title'],['aria-label','aria-label']]) {
    for (const el of document.querySelectorAll('[' + name + ']')) {
      out.push(el.getAttribute(name) + SEP + where(el) + ' [' + label + ']');
    }
  }
  return out;
})()`

// TestNoEnglishSurvivesTheSwitch is the guard the other one cannot be.
//
// TestTheLanguageSwitchReachesThePage above holds four landmarks, one per way the
// page draws a word. That is enough to prove the wiring works and not nearly enough
// to prove the page is translated: every word it does not name is a word that can
// stay English forever without failing anything, and six of them did. They were the
// relative timestamps on every card, the three abbreviations in a card's footer, the
// layout switcher, the period chips on the incidents screen, the Customise button,
// the widget picker, four close buttons, two role-gated buttons and the sign-in
// screen — none of which any test was looking at.
//
// So this one does not look at chosen places. It takes the English catalogue and the
// German one, keeps every message the two spell differently, and then asserts that
// not one of those English strings appears anywhere in the document — hidden
// elements included — once the interface is set to German. It has no opinion about
// which words exist or where they should be, so it cannot be satisfied by adding a
// landmark for each one.
//
// What it cannot see is text that is not in a catalogue at all: sensor and group
// names, targets, and the probe messages the server writes into the history. Those
// are data rather than interface, they are the same in every language, and they are
// why this test compares against the catalogue instead of looking for English-looking
// words.
func TestNoEnglishSurvivesTheSwitch(t *testing.T) {
	browser := findBrowser(t)
	base := startServer(t, buildBinary(t))
	ctx, done := openBrowser(t, browser)
	defer done()
	dir := artifactsDir(t)

	english := fetchCatalogue(t, base, "/assets/i18n.js")
	german := fetchCatalogue(t, base, "/assets/i18n.de.js")

	// One entry per English sentence, not per key: two keys may hold the same
	// English and different German, and either of them on screen is the same
	// finding. The German is kept only to say in the failure what it should have
	// read.
	expected := map[string]string{}
	for key, en := range english {
		de, ok := german[key]
		if !ok || de == en || en == "" {
			continue
		}
		if _, taken := expected[en]; !taken {
			expected[en] = de
		}
	}
	if len(expected) < 100 {
		t.Fatalf("only %d of the English messages differ from the German; with a gap "+
			"that small this test would pass on any page at all", len(expected))
	}

	firstRun(t, ctx, base, dir)

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx,
			goTo("#/settings", `!!document.querySelector('#settings-body .settings-tabs [data-category]')`),
			openSettingsCategory("look"),
			chromedp.Poll[chromedp.Void](
				`!!document.querySelector('#settings-body [data-panel="appearance"] select[data-setting="language"]')`,
				chromedp.WithPollingTimeout(15*time.Second)),
			choosing(`#settings-body [data-panel="appearance"] select[data-setting="language"]`, "de"),
			chromedp.Poll[chromedp.Void](`document.documentElement.lang === 'de'`,
				chromedp.WithPollingTimeout(15*time.Second)),
		)
	}); err != nil {
		capture(t, ctx, dir, "leftovers-switch")
		t.Fatalf("could not switch to German: %v", err)
	}

	// Every view, at a width where the top bar is not folded, and with the folded
	// menu open afterwards so that the controls it holds are in the document too.
	for _, route := range []string{"dashboard", "incidents", "channels", "settings"} {
		if err := withTimeout(ctx, func(tctx context.Context) error {
			return chromedp.Do(tctx,
				chromedp.EmulateViewport(1500, 900),
				goTo("#/"+route, `!!document.querySelector('#view-`+route+`')`))
		}); err != nil {
			t.Fatalf("could not reach #/%s: %v", route, err)
		}
		noEnglish(t, ctx, dir, route, expected)
	}

	if err := withTimeout(ctx, func(tctx context.Context) error {
		return chromedp.Do(tctx, chromedp.Evaluate[chromedp.Void](`(() => {
		  const more = document.querySelector('#topbar-more');
		  if (more && !more.hidden) more.click();
		})()`))
	}); err != nil {
		t.Fatalf("could not open the folded menu: %v", err)
	}
	noEnglish(t, ctx, dir, "the folded menu", expected)
}

// noEnglish reports every English message still in the document, with the element
// it is in, so that a failure says where to look rather than only that something is
// wrong.
func noEnglish(t *testing.T, ctx context.Context, dir, where string, expected map[string]string) {
	t.Helper()
	words := read[[]string](t, ctx, everyWordInTheDocument, "every word in the document")
	seen := map[string]bool{}
	var found []string
	for _, raw := range words {
		text, path, _ := strings.Cut(raw, "\u0001")
		german, isMessage := expected[text]
		if !isMessage || seen[text+path] {
			continue
		}
		seen[text+path] = true
		found = append(found, fmt.Sprintf("%q in %s — it should read %q", text, path, german))
	}
	if len(found) == 0 {
		return
	}
	sort.Strings(found)
	capture(t, ctx, dir, "leftovers")
	t.Errorf("on %s the interface is set to German and %d message(s) are still English:\n  %s\n"+
		"t() falls through to English for a key a dictionary is missing, so an English "+
		"message on a German page is what a missing translation looks like — but it is "+
		"also what a word that was never put through the catalogue looks like, which is "+
		"what these were.", where, len(found), strings.Join(found, "\n  "))
}
