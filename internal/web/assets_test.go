package web

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/j000k3r/weatherglass/internal/model"
)

// The dashboard switches between its three screens — the boot spinner, the
// auth card and the shell — by setting `element.hidden`. That works only while
// no author rule declares a `display` for the same element, because an author
// rule outranks the user-agent `[hidden] { display: none }` no matter how
// unspecific it is.
//
// This was a real bug: `.boot` set `display: grid; min-height: 100vh`, so the
// spinner could not be hidden and sat on top of a dashboard that was loading
// perfectly. It looked exactly like a hang, and no endpoint-level test could
// see it, because every request was returning 200.
//
// The guard is a single `[hidden] { display: none !important }`. These tests
// keep it honest: it must exist, nothing may outrank it, and every element the
// app toggles must be covered by it.
func TestHiddenAttributeWinsOverAuthorDisplay(t *testing.T) {
	assets := Assets()
	css := readAsset(t, assets, "style.css")

	// Comments out of the way first: brace matching over prose is guesswork.
	stripped := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(css, "")

	hidden, rivals := classifyDisplayRules(stripped)

	if len(hidden) == 0 {
		t.Fatal("no [hidden] rule in style.css: every `element.hidden = true` is a no-op " +
			"for any element whose class declares a display, and the failure is invisible to API tests")
	}

	// The declaration has to carry !important, or the first specific class rule
	// in the file beats it again.
	if !strings.Contains(hidden[0], "!important") {
		t.Errorf("[hidden] rule is %q; without !important it does not outrank the class "+
			"rules that caused this bug", hidden[0])
	}
	if !strings.Contains(hidden[0], "none") {
		t.Errorf("[hidden] rule is %q; it must set display: none", hidden[0])
	}

	// A second !important display rule could tie and win on specificity.
	for _, rule := range rivals {
		t.Errorf("display rule %q also uses !important; it can outrank the [hidden] rule "+
			"and silently un-hide an element the app believes is hidden", rule)
	}
}

// Every id the dashboard toggles through `.hidden` should be reachable by the
// global rule — which it is, since the rule is attribute-based and unconditional
// — but this fails loudly if the app ever switches to a different mechanism
// (a class, say) that the rule would not cover.
func TestHiddenTogglesAreAttributeBased(t *testing.T) {
	assets := Assets()
	js := readAsset(t, assets, "app.js")
	html := readAsset(t, assets, "index.html")

	byID := regexp.MustCompile(`\$\('#([A-Za-z0-9_-]+)'\)\.hidden`)
	byClass := regexp.MustCompile(`\$\('#([A-Za-z0-9_-]+)'\)\.classList\.(add|remove|toggle)\('hidden'`)
	// Also catch the other direction: elements marked hidden in the markup.
	markup := regexp.MustCompile(`id="([A-Za-z0-9_-]+)"[^>]*\shidden`)

	ids := map[string]bool{}
	for _, m := range byID.FindAllStringSubmatch(js, -1) {
		ids[m[1]] = true
	}
	for _, m := range markup.FindAllStringSubmatch(html, -1) {
		ids[m[1]] = true
	}

	if len(ids) == 0 {
		t.Fatal("found no `.hidden` toggles at all; this test has stopped testing anything")
	}

	if m := byClass.FindStringSubmatch(js); m != nil {
		t.Errorf("#%s is toggled with classList 'hidden', which the [hidden] rule does not cover", m[1:])
	}

	// The spinner is the one that must never be able to stick: it is full
	// height, so a stuck one hides the entire application.
	if !ids["boot"] {
		t.Error("app.js no longer hides #boot; if that is deliberate, the boot spinner " +
			"now needs another way to leave the screen")
	}
}

// TestEveryWidgetHasAName keeps the widget registry and the message catalogue
// spelled the same way.
//
// A widget's tile, its row in the settings picker and its label while the strip
// is being arranged are all drawn from the message `widgets.<id>`, where `<id>` is
// the name the registry and the server both use. Look that up and miss, and `t()`
// answers with the key itself rather than with nothing: the tile quietly says
// "widgets.mini-map", which is what the map widget said until this test was
// written — and which no endpoint test can see, because the name is drawn by the
// browser and the request that stored the list was a perfectly good 200.
//
// The list of ids comes from the server, because that is the copy that decides
// what may be stored, and the catalogue is read out of the asset the browser
// actually loads.
func TestEveryWidgetHasAName(t *testing.T) {
	catalogue := readAsset(t, Assets(), "i18n.js")

	named := map[string]bool{}
	entry := regexp.MustCompile(`'widgets\.([A-Za-z0-9_-]+)':`)
	for _, m := range entry.FindAllStringSubmatch(catalogue, -1) {
		named[m[1]] = true
	}

	for _, id := range model.Widgets {
		if !named[id] {
			t.Errorf("the widget %q has no message `widgets.%s` in i18n.js.\n"+
				"Every widget is drawn from that key — the tile, the picker row and "+
				"the label in the strip — and a missing one shows up on screen as "+
				"the key itself.", id, id)
		}
	}
}

func readAsset(t *testing.T, assets fs.FS, name string) string {
	t.Helper()
	data, err := fs.ReadFile(assets, name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// classifyDisplayRules returns the bodies of rules selecting [hidden] and the
// bodies of every other rule that declares display with !important.
func classifyDisplayRules(css string) (hidden, rivals []string) {
	rule := regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	display := regexp.MustCompile(`(?:^|;)\s*display\s*:`)

	for _, m := range rule.FindAllStringSubmatch(css, -1) {
		selector := strings.Join(strings.Fields(m[1]), " ")
		body := strings.Join(strings.Fields(m[2]), " ")

		// Only declarations that set display matter here. A rule that merely
		// mentions display in passing is not a rival.
		if !display.MatchString(";" + body) {
			continue
		}
		switch {
		case strings.Contains(selector, "[hidden]"):
			hidden = append(hidden, body)
		case strings.Contains(body, "!important"):
			rivals = append(rivals, selector+" { "+body+" }")
		}
	}
	return hidden, rivals
}
