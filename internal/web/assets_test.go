package web

import (
	"io/fs"
	"math"
	"regexp"
	"strconv"
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

// TestTheEmbeddedCoastlineDecodes reads the coastline the way the browser does.
//
// world.js is generated, embedded, and read by nothing else in the build. A file that
// was truncated — by a merge, by a checkout that ran out of room, by a generator that
// died halfway through — therefore compiles, ships, and produces a map with no land on
// it, which is exactly what a map of nowhere looks like. Nothing else in this repository
// would notice.
//
// So the decoder below is a second implementation of the format, written against the
// format as documented in cmd/worldgen rather than by calling that program's code, and
// what it checks is the shape of the result: the right number of rings, every point on
// the planet, and a world that is in fact the whole world rather than a corner of it.
func TestTheEmbeddedCoastlineDecodes(t *testing.T) {
	js := readAsset(t, Assets(), "world.js")

	rings := exportInt(t, js, "WORLD_RINGS")
	scale := exportInt(t, js, "WORLD_SCALE")
	if scale != 1000 {
		t.Errorf("WORLD_SCALE = %d; the generator quantises to thousandths of a degree, so a "+
			"reader that trusts this number is wrong by whatever factor it is", scale)
	}
	if rings < 50 {
		t.Errorf("world.js claims %d ring(s); 1:110m land is well over a hundred landmasses "+
			"and islands, so this is a file that lost most of itself", rings)
	}

	data := strings.Trim(exportValue(t, js, "WORLD_DATA"), "'")
	if len(data) < 1000 {
		t.Fatalf("WORLD_DATA is %d characters long, which cannot be a world", len(data))
	}
	decoded, points := decodeWorld(t, data, rings, scale)

	if points < 1000 {
		t.Errorf("%d point(s) across %d ring(s); the whole 1:110m land layer is several "+
			"thousand, so most of the world is missing from the file", points, rings)
	}

	// Every point has to be somewhere on the planet, and between them they have to
	// cover it. A decoder off by a factor, or a delta chain that never resets, scores
	// badly on both of these and on nothing else that is easy to measure.
	minLon, maxLon := 180.0, -180.0
	minLat, maxLat := 90.0, -90.0
	for i, ring := range decoded {
		if len(ring) < 3 {
			t.Errorf("ring %d has %d point(s); three is the fewest that encloses any area",
				i, len(ring))
			continue
		}
		for j, p := range ring {
			lon, lat := p[0], p[1]
			if lon < -180 || lon > 180 || lat < -90 || lat > 90 {
				t.Fatalf("ring %d point %d is at %g,%g, which is off the planet; the delta "+
					"chain has been misread", i, j, lon, lat)
			}
			minLon, maxLon = math.Min(minLon, lon), math.Max(maxLon, lon)
			minLat, maxLat = math.Min(minLat, lat), math.Max(maxLat, lat)
		}
	}
	if span := maxLon - minLon; span < 300 {
		t.Errorf("the rings span %.1f° of longitude; a world map spans nearly 360, so this "+
			"is a corner of one", span)
	}
	if span := maxLat - minLat; span < 120 {
		t.Errorf("the rings span %.1f° of latitude; land reaches from well inside the "+
			"Arctic to well inside the Antarctic, so this is a corner of a world", span)
	}
}

// exportValue reads `export const NAME = value;` out of a generated asset.
func exportValue(t *testing.T, js, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^export const ` + name + ` = (.+);$`).FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("the asset has no `export const %s = ...;` on a line of its own", name)
	}
	return m[1]
}

func exportInt(t *testing.T, js, name string) int {
	t.Helper()
	n, err := strconv.Atoi(exportValue(t, js, name))
	if err != nil {
		t.Fatalf("export const %s is not a number: %v", name, err)
	}
	return n
}

// decodeWorld is the format from cmd/worldgen, read a second time and on purpose not
// by calling that code: a decoder that shares its implementation with the encoder
// agrees with it about any mistake they both make.
//
// A number is base 63 written most significant chunk first, closed by a chunk of 63.
// A ring's first point is absolute and the rest are deltas, so accumulating from zero
// covers both without a special case. Coordinates are thousandths of a degree.
func decodeWorld(t *testing.T, data string, rings, scale int) (out [][][2]float64, points int) {
	t.Helper()

	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var code [128]int
	for i := range code {
		code[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		code[alphabet[i]] = i
	}

	pos := 0
	value := func() int64 {
		t.Helper()
		var v int64
		for n := 0; n < 12; n++ {
			if pos >= len(data) {
				t.Fatalf("WORLD_DATA ends in the middle of a number at offset %d of %d",
					pos, len(data))
			}
			c := int(data[pos])
			pos++
			if c >= len(code) || code[c] < 0 {
				t.Fatalf("WORLD_DATA has %q (offset %d), which is not in the encoding "+
					"alphabet; the file and the format have drifted apart", data[pos-1], pos-1)
			}
			if code[c] == 63 {
				return v
			}
			v = v*63 + int64(code[c])
		}
		t.Fatalf("WORLD_DATA has two hundred digits of number at offset %d and no end to it",
			pos)
		return 0
	}
	unzigzag := func(u int64) int64 {
		if u%2 == 1 {
			return -(u-1)/2 - 1
		}
		return u / 2
	}

	if got := value(); int(got) != rings {
		t.Fatalf("WORLD_DATA holds %d ring(s), WORLD_RINGS says %d; the file is two files",
			got, rings)
	}
	out = make([][][2]float64, 0, rings)
	for i := 0; i < rings; i++ {
		n := int(value())
		if n <= 0 || n > 100000 {
			t.Fatalf("ring %d has %d point(s), which is not a number that came out of a "+
				"coastline", i, n)
		}
		ring := make([][2]float64, n)
		var lon, lat int64
		for j := 0; j < n; j++ {
			lon += unzigzag(value())
			lat += unzigzag(value())
			ring[j] = [2]float64{float64(lon) / float64(scale), float64(lat) / float64(scale)}
		}
		out = append(out, ring)
		points += n
	}
	if pos != len(data) {
		t.Errorf("WORLD_DATA has %d character(s) left over after the last ring; the file "+
			"holds more than it says it does", len(data)-pos)
	}
	return out, points
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
