package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"math"
	"regexp"
	"slices"
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
// would notice. The same is true of the three files beside it, which have their own
// tests below.
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
	if rings < 1000 {
		t.Errorf("world.js claims %d ring(s); 1:50m land is about 1 400 landmasses and "+
			"islands, so this is a file that lost most of itself", rings)
	}

	data := strings.Trim(exportValue(t, js, "WORLD_DATA"), "'")
	if len(data) < 100000 {
		t.Fatalf("WORLD_DATA is %d characters long; the 1:50m coastline is around 200 000, "+
			"so this file is a fragment", len(data))
	}
	decoded, points := decodeWorld(t, data, rings, scale)

	if points < 20000 {
		t.Errorf("%d point(s) across %d ring(s); the whole 1:50m land layer is about 35 000, "+
			"so most of the world is missing from the file", points, rings)
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

	// One ring has to sit inside another, and that ring is the Caspian. The browser
	// fills this layer with fill-rule="evenodd", so a nested ring is a hole in the land
	// it is nested in — and the day the generator goes back to keeping outer rings only,
	// the hole disappears, the lakes file fills nothing, and the Caspian is drawn as
	// land. Nothing else in the build would notice.
	if n := nestedRings(decoded); n == 0 {
		t.Errorf("no ring lies inside another; the coastline is filled with " +
			"fill-rule=\"evenodd\", so the one hole in the source — the Caspian — is only a " +
			"hole while its ring is here, and without it the map draws a sea as land")
	}
}

// nestedRings counts the rings that lie inside another ring.
//
// The bounding box comes first because it is what makes this cheap: two rings can only
// nest if one box contains the other, and a world of islands has almost no such pair,
// so the point-in-polygon test runs a handful of times rather than two million. A box
// that is contained but a ring that is not is exactly what two neighbours on a border
// look like, which is why the box alone is not the answer.
func nestedRings(rings [][][2]float64) int {
	boxes := make([][4]float64, len(rings))
	for i, ring := range rings {
		box := [4]float64{180, 90, -180, -90}
		for _, p := range ring {
			box[0], box[1] = math.Min(box[0], p[0]), math.Min(box[1], p[1])
			box[2], box[3] = math.Max(box[2], p[0]), math.Max(box[3], p[1])
		}
		boxes[i] = box
	}

	n := 0
	for i, ring := range rings {
		for j, box := range boxes {
			if i == j {
				continue
			}
			if boxes[i][0] < box[0] || boxes[i][1] < box[1] ||
				boxes[i][2] > box[2] || boxes[i][3] > box[3] {
				continue
			}
			if pointInRing(ring[0], rings[j]) {
				n++
				break
			}
		}
	}
	return n
}

// pointInRing is the usual ray cast: an even number of crossings means the point is
// outside. Points exactly on the edge are a coin toss, which is fine here — a coastline
// vertex landing exactly on a lake shore vertex is not a case the answer matters for.
func pointInRing(pt [2]float64, ring [][2]float64) bool {
	inside := false
	for i, j := 0, len(ring)-1; i < len(ring); j, i = i, i+1 {
		if (ring[i][1] > pt[1]) != (ring[j][1] > pt[1]) &&
			pt[0] < (ring[j][0]-ring[i][0])*(pt[1]-ring[i][1])/(ring[j][1]-ring[i][1])+ring[i][0] {
			inside = !inside
		}
	}
	return inside
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

// exportInts reads `export const NAME = [1, 2, 3];`.
func exportInts(t *testing.T, js, name string) []int {
	t.Helper()
	raw := exportValue(t, js, name)
	var out []int
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("export const %s is not an array of numbers: %v", name, err)
	}
	return out
}

// exportStrings reads the JSON string array at the bottom of a name file.
func exportStrings(t *testing.T, js, name string) []string {
	t.Helper()
	var out []string
	if err := json.Unmarshal([]byte(exportValue(t, js, name)), &out); err != nil {
		t.Fatalf("export const %s is not a JSON array of strings: %v", name, err)
	}
	return out
}

func exportInt(t *testing.T, js, name string) int {
	t.Helper()
	n, err := strconv.Atoi(exportValue(t, js, name))
	if err != nil {
		t.Fatalf("export const %s is not a number: %v", name, err)
	}
	return n
}

// packed reads the generated files' own encoding back.
//
// It is a second implementation of the format on purpose — a decoder that shares its
// code with the encoder agrees with it about any mistake the two of them make — and it
// is one implementation for all four files because the format is one format: a number
// written base 63 with the most significant chunk first and closed by a chunk of 63,
// signed numbers zigzagged, and points in thousandths of a degree whose first is
// absolute and whose rest are deltas.
type packed struct {
	t    *testing.T
	data string
	pos  int
	lon  int64
	lat  int64
}

func newPacked(t *testing.T, data string) *packed {
	t.Helper()
	if len(data) == 0 {
		t.Fatal("the stream is empty; the whole file is missing, not merely wrong")
	}
	return &packed{t: t, data: data}
}

// value reads one unsigned number.
func (p *packed) value() int64 {
	p.t.Helper()
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var code [128]int
	for i := range code {
		code[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		code[alphabet[i]] = i
	}

	var v int64
	for n := 0; n < 12; n++ {
		if p.pos >= len(p.data) {
			p.t.Fatalf("the stream ends in the middle of a number at offset %d of %d",
				p.pos, len(p.data))
		}
		c := int(p.data[p.pos])
		p.pos++
		if c >= len(code) || code[c] < 0 {
			p.t.Fatalf("the stream holds %q (offset %d), which is not in the encoding "+
				"alphabet; the file and the format have drifted apart", p.data[p.pos-1], p.pos-1)
		}
		if code[c] == 63 {
			return v
		}
		v = v*63 + int64(code[c])
	}
	p.t.Fatalf("the stream has twelve digits of number at offset %d and no end to it", p.pos)
	return 0
}

// count reads a number that is meant to say how many of something follow.
func (p *packed) count(what string, max int) int {
	p.t.Helper()
	v := p.value()
	if v < 0 || v > int64(max) {
		p.t.Fatalf("%s is %d, which is not a number of things that came out of a map", what, v)
	}
	return int(v)
}

// signed reads one delta.
func (p *packed) signed() int64 {
	u := p.value()
	if u%2 == 1 {
		return -(u-1)/2 - 1
	}
	return u / 2
}

// reset starts a new point chain, which is what makes the next point absolute. A ring
// and a border line each start with one; the label streams never call it, so their chain
// runs from the first name to the last.
func (p *packed) reset() { p.lon, p.lat = 0, 0 }

// point reads one point, in thousandths of a degree unless the scale says otherwise.
func (p *packed) point(scale int) [2]float64 {
	p.t.Helper()
	p.lon += p.signed()
	p.lat += p.signed()
	return [2]float64{float64(p.lon) / float64(scale), float64(p.lat) / float64(scale)}
}

// end reports the characters the stream had left over.
func (p *packed) end() {
	p.t.Helper()
	if p.pos != len(p.data) {
		p.t.Errorf("the stream has %d character(s) left over after the last of its "+
			"content; the file holds more than it says it does", len(p.data)-p.pos)
	}
}

// decodeWorld reads the coastline, which is a ring count and then that many rings.
func decodeWorld(t *testing.T, data string, rings, scale int) (out [][][2]float64, points int) {
	t.Helper()
	p := newPacked(t, data)

	if got := int(p.value()); got != rings {
		t.Fatalf("WORLD_DATA holds %d ring(s), WORLD_RINGS says %d; the file and its own "+
			"header are two different files", got, rings)
	}
	out = make([][][2]float64, 0, rings)
	for i := 0; i < rings; i++ {
		n := p.count(fmt.Sprintf("ring %d's point count", i), 100000)
		if n < 3 {
			t.Fatalf("ring %d has %d point(s); three is the fewest that encloses any area", i, n)
		}
		p.reset()
		ring := make([][2]float64, n)
		for j := 0; j < n; j++ {
			ring[j] = p.point(scale)
		}
		out = append(out, ring)
		points += n
	}
	p.end()
	return out, points
}

// bandedFile is one of the four banded assets, decoded.
//
// The four files are countries.js, borders.js, lakes.js and rivers.js, and they hold
// rings or lines — a lake is a filled ring and a border is an open line — which is why
// `minPoints` is a parameter: the generator keeps two points for a line and four for a
// ring, and a test that asserted the wrong floor would fail on a correct file.
type bandedFile struct {
	bands  []int
	shapes [][][2]float64
	points int
	// minLon, minLat, maxLon, maxLat over every point in the file.
	box [4]float64
}

// decodeBanded reads one of the four banded files the way the browser does: the band
// list out of the header, then one self-delimiting stream per band, in band order, each
// beginning with its own shape count.
//
// It is a second implementation of the format on purpose — a decoder that shared its
// code with the generator would agree with it about any mistake the two of them made —
// and it checks the three things that go wrong quietly: a band list that does not
// ascend (the reader walks it in order and would stop early), a shape with too few
// points to be one, and a point that is off the planet, which is what a delta chain
// misread looks like.
func decodeBanded(t *testing.T, js, prefix string, minPoints int) bandedFile {
	t.Helper()

	out := bandedFile{
		bands: exportInts(t, js, prefix+"_BANDS"),
		box:   [4]float64{180, 90, -180, -90},
	}
	if len(out.bands) == 0 {
		t.Fatalf("%s_BANDS is empty; the bands are what the reader walks, so a file with none "+
			"of them draws nothing at any zoom", prefix)
	}
	for i := 1; i < len(out.bands); i++ {
		if out.bands[i] <= out.bands[i-1] {
			t.Fatalf("%s_BANDS is %v; the bands have to ascend, because the reader walks them "+
				"in order and stops at the first one past the current zoom", prefix, out.bands)
		}
	}

	p := newPacked(t, strings.Trim(exportValue(t, js, prefix+"_DATA"), "'"))
	const scale = 1000

	for b, zoom := range out.bands {
		n := p.count(fmt.Sprintf("band %d (zoom %d) shape count", b, zoom), 100000)
		if n == 0 {
			t.Errorf("band %d (zoom %d) holds nothing; the generator groups shapes by the zoom "+
				"their author drew them for, so an empty band means the grouping has come "+
				"apart", b, zoom)
		}
		for i := 0; i < n; i++ {
			got := p.count(fmt.Sprintf("band %d shape %d's point count", b, i), 100000)
			if got < minPoints {
				t.Fatalf("band %d shape %d has %d point(s); the generator keeps %d and up for "+
					"this file", b, i, got, minPoints)
			}
			p.reset()
			shape := make([][2]float64, got)
			for j := 0; j < got; j++ {
				pt := p.point(scale)
				if pt[0] < -180 || pt[0] > 180 || pt[1] < -90 || pt[1] > 90 {
					t.Fatalf("band %d shape %d point %d is at %g,%g, which is off the planet; "+
						"the delta chain has been misread", b, i, j, pt[0], pt[1])
				}
				shape[j] = pt
				out.box[0] = math.Min(out.box[0], pt[0])
				out.box[1] = math.Min(out.box[1], pt[1])
				out.box[2] = math.Max(out.box[2], pt[0])
				out.box[3] = math.Max(out.box[3], pt[1])
			}
			out.shapes = append(out.shapes, shape)
			out.points += got
		}
	}
	p.end()
	return out
}

// inBox counts the shapes of a file that have a point inside a lon/lat window. It is
// how the two tests below ask whether a particular place is in the data at all — the
// question a map cannot answer for itself, because a country that is missing from the
// file and a country somebody turned a layer off for look exactly alike on screen.
func inBox(f bandedFile, west, south, east, north float64) int {
	n := 0
	for _, shape := range f.shapes {
		for _, p := range shape {
			if p[0] >= west && p[0] <= east && p[1] >= south && p[1] <= north {
				n++
				break
			}
		}
	}
	return n
}

// TestTheEmbeddedBordersDecode reads the province borders the way the browser does.
//
// The borders are 670 KB of the repository's largest generated file, they are fetched by
// the map and by nothing else, and a file that lost its tail looks exactly like a map
// whose eastern half has no provinces in it — which is what a country that has none
// looks like. So: every band present and ascending, the lines counted, and the sum of
// them the number the header claims the class filter kept.
func TestTheEmbeddedBordersDecode(t *testing.T) {
	js := readAsset(t, Assets(), "borders.js")

	f := decodeBanded(t, js, "WORLD_BORDER", 2)
	if len(f.bands) < 5 {
		t.Fatalf("WORLD_BORDER_BANDS is %v; Natural Earth spreads its admin-1 lines over "+
			"about nine MIN_ZOOM levels, so this is a band list that lost most of itself", f.bands)
	}
	if f.bands[0] > 4 {
		t.Errorf("the first border band is zoom %d; the map shows a band from its own zoom "+
			"upwards, so a first band above 4 means no borders at all until the map is deep "+
			"inside one country", f.bands[0])
	}

	// 36 167 is what the class filter keeps of the file's 45 098 lines, and 97 513 points
	// is what the tolerance leaves of them. Both are in the header; these floors are
	// loose enough to survive a retuned tolerance and tight enough to catch a truncation.
	if len(f.shapes) < 30000 {
		t.Errorf("%d line(s) across the bands; the admin-1 class holds about 36 000, so "+
			"this file is missing the provinces of whole continents", len(f.shapes))
	}
	if f.points < 60000 {
		t.Errorf("%d point(s) across %d line(s); the 1:10m admin-1 lines are about 97 000, "+
			"so the tail of this file is somewhere else", f.points, len(f.shapes))
	}
	if span := f.box[2] - f.box[0]; span < 300 {
		t.Errorf("the border lines span %.1f° of longitude; provinces exist on every "+
			"continent, so this is a corner of the world", span)
	}
	if span := f.box[3] - f.box[1]; span < 100 {
		t.Errorf("the border lines span %.1f° of latitude; they run from Norway to "+
			"Argentina, so this is a corner of one", span)
	}
}

// TestTheEmbeddedCountryBordersDecode reads the international borders.
//
// This is the file whose absence was visible on the dashboard: the province layer holds
// boundaries between first-order areas and never a national outline, so the map drew
// Austria's Bundesländer and no Austria. Everything here is aimed at that one failure,
// because a map with no national borders on it is not a map that looks broken — it is a
// map that looks like a map.
func TestTheEmbeddedCountryBordersDecode(t *testing.T) {
	js := readAsset(t, Assets(), "countries.js")

	f := decodeBanded(t, js, "WORLD_COUNTRY", 2)
	if f.bands[0] > 2 {
		t.Errorf("the first country band is zoom %d; the map shows a band from its own zoom "+
			"upwards, so anything above 2 means the countries are missing at a world view — "+
			"which is where somebody will first look for them", f.bands[0])
	}

	// 358 of the file's 393 lines are the international class and 8 328 points survive
	// the tolerance; both numbers are in the header and both floors are loose.
	if len(f.shapes) < 250 {
		t.Errorf("%d line(s) across the bands; the international class holds 358, so this "+
			"file has lost whole borders", len(f.shapes))
	}
	if f.points < 5000 {
		t.Errorf("%d point(s) across %d line(s); the 1:50m international borders are about "+
			"8 300, so the tail of this file is somewhere else", f.points, len(f.shapes))
	}
	// 282° is the honest span: a border is a line where two countries meet, and the
	// two ends of the world — Chukotka, Alaska, the islands of the Pacific — are coast
	// and not frontier. A floor of 250 catches a file that lost a hemisphere without
	// demanding the one number this dataset cannot produce.
	if span := f.box[2] - f.box[0]; span < 250 {
		t.Errorf("the border lines span %.1f° of longitude; countries meet on every "+
			"continent, so this is a corner of the world", span)
	}

	// Austria, and this is the shape of the bug rather than a country worth naming: a
	// border file built from the wrong dataset passed every other check in this test —
	// bands, counts, spans — while not containing one line of the country it was
	// installed to draw.
	if n := inBox(f, 9.4, 46.3, 17.3, 49.1); n < 10 {
		t.Errorf("%d border line(s) reach Austria's bounding box; its outline and the "+
			"outlines of its neighbours meet there, so a handful means the borders went "+
			"back to being drawn from the province file", n)
	}
}

// TestTheEmbeddedLakesDecode reads the lakes.
//
// A lake is a filled ring, and the one that matters is the Caspian: it is the only hole
// in the coastline file, which means that if the ring does not survive, the hole in the
// land and the lake that should fill it are both missing and the Caspian is drawn as
// land — the failure this file was added to end.
func TestTheEmbeddedLakesDecode(t *testing.T) {
	js := readAsset(t, Assets(), "lakes.js")

	f := decodeBanded(t, js, "WORLD_LAKE", 4)
	if f.bands[0] > 3 {
		t.Errorf("the first lake band is zoom %d; a world view should show the lakes big "+
			"enough to be seen on one", f.bands[0])
	}
	if len(f.shapes) < 300 {
		t.Errorf("%d ring(s); the 1:50m lakes file holds 465, so this is missing the lakes "+
			"of whole continents", len(f.shapes))
	}
	if f.points < 5000 {
		t.Errorf("%d point(s) across %d ring(s); the tolerance leaves about 8 600, so the "+
			"tail of this file is somewhere else", f.points, len(f.shapes))
	}
	if span := f.box[2] - f.box[0]; span < 300 {
		t.Errorf("the lake rings span %.1f° of longitude; there are lakes on every "+
			"continent, so this is a corner of the world", span)
	}

	// Lake Victoria, which is drawn from the world view on, and the Bodensee, which is
	// not: two rings at two ends of the band list, so a file that lost either end fails
	// here. The Caspian is deliberately not in this test and not in this file — it is
	// the hole in the coastline, and the test above is where that is checked.
	if n := inBox(f, 31.6, -3.0, 34.9, 0.5); n == 0 {
		t.Errorf("no lake ring reaches Lake Victoria; its min_zoom of 1 puts it in the " +
			"coarsest band, so a file without it is missing the lakes a world view draws")
	}
	if n := inBox(f, 9.0, 47.4, 9.8, 47.85); n == 0 {
		t.Errorf("no lake ring reaches the Bodensee; it is one of the finer bands, so a file " +
			"that lost its tail keeps Lake Victoria and drops this one")
	}
}

// TestTheEmbeddedRiversDecode reads the rivers.
//
// Rivers are the one layer here that a world view does not want at all: Natural Earth
// ranks them 1 to 6 and the generator keeps 3 and better, which is the Amazon and the
// Danube and not the tributaries that reach them. A file generated at a looser cut would
// pass every count in this test and put a hairball over Europe.
func TestTheEmbeddedRiversDecode(t *testing.T) {
	js := readAsset(t, Assets(), "rivers.js")

	f := decodeBanded(t, js, "WORLD_RIVER", 2)
	if f.bands[0] > 3 {
		t.Errorf("the first river band is zoom %d; the Danube is drawn at zoom 3, so a first "+
			"band above it means no rivers on a map anybody would call a country view", f.bands[0])
	}
	if len(f.shapes) < 150 {
		t.Errorf("%d line(s) at rank 3 and better; there are 275, so this file has lost the "+
			"largest rivers of whole continents", len(f.shapes))
	}
	if f.points < 3000 {
		t.Errorf("%d point(s) across %d line(s); the tolerance leaves about 5 500, so the "+
			"tail of this file is somewhere else", f.points, len(f.shapes))
	}
	if span := f.box[2] - f.box[0]; span < 300 {
		t.Errorf("the rivers span %.1f° of longitude; they run on every continent, so this "+
			"is a corner of the world", span)
	}
}

// TestTheEmbeddedCountryNamesDecode reads the country names the way the browser does.
//
// A name and its point are two halves of one record stored in two different exports, so
// the failure this guards against is not a corrupt file but a changed one: a name list
// and a coordinate stream that no longer line up put the wrong name on the wrong place,
// which is a map that is confidently wrong and nothing else. The counts have to match,
// the names have to be there, and the few a reader can check by hand have to be the
// right ones.
func TestTheEmbeddedCountryNamesDecode(t *testing.T) {
	js := readAsset(t, Assets(), "labels.js")

	names := exportStrings(t, js, "WORLD_LABEL_NAMES")
	if len(names) < 150 {
		t.Fatalf("WORLD_LABEL_NAMES holds %d name(s); the 1:110m countries file has 177, so "+
			"this list has lost a fifth of the world", len(names))
	}
	for _, want := range []string{"Germany", "Austria", "Switzerland", "United States of America"} {
		if !slices.Contains(names, want) {
			t.Errorf("WORLD_LABEL_NAMES has no %q; the names come from the dataset's own NAME "+
				"field in its own spelling, and this one is a country the file is expected to "+
				"hold", want)
		}
	}

	points, groups := decodeLabels(t, strings.Trim(exportValue(t, js, "WORLD_LABEL_POINTS"), "'"))
	if points != len(names) {
		t.Fatalf("the coordinate stream holds %d point(s) and the name list %d; the two are "+
			"read side by side and a mismatch names every country after the first gap "+
			"incorrectly", points, len(names))
	}
	if groups < 5 {
		t.Errorf("%d group(s) of names; the file groups runs of equal importance, of which "+
			"the countries file has about 16", groups)
	}
}

// TestTheEmbeddedRegionNamesDecode reads the province, state and Bundesland names.
//
// What is worth pinning here is not the format — that is the test above — but the two
// decisions that were made while generating the file, both of which fail silently. The
// first is that the German Bundesländer carry the names the people living there use, and
// the second is that Natural Earth's stand-ins for places it has no name for
// ("JPN-99 (  minor island)") are not among them. A map that draws a database key over
// somebody's state is the kind of thing nobody reports as a bug, because it looks like a
// label nobody has got round to translating.
func TestTheEmbeddedRegionNamesDecode(t *testing.T) {
	js := readAsset(t, Assets(), "regions.js")

	names := exportStrings(t, js, "WORLD_REGION_NAMES")
	if len(names) < 3000 {
		t.Fatalf("WORLD_REGION_NAMES holds %d name(s); the 1:10m admin-1 file has 4 552 "+
			"after the deduplication, so this list has lost most of itself", len(names))
	}

	// The sixteen states of the country this project is written in, spelled as the
	// dataset spells them: not "Lower Saxony".
	bundeslaender := []string{
		"Baden-Württemberg", "Bayern", "Berlin", "Brandenburg", "Bremen", "Hamburg",
		"Hessen", "Mecklenburg-Vorpommern", "Niedersachsen", "Nordrhein-Westfalen",
		"Rheinland-Pfalz", "Saarland", "Sachsen", "Sachsen-Anhalt",
		"Schleswig-Holstein", "Thüringen",
	}
	for _, want := range bundeslaender {
		if !slices.Contains(names, want) {
			t.Errorf("WORLD_REGION_NAMES has no %q; the names are the dataset's own, and "+
				"all sixteen Bundesländer are expected in it", want)
		}
	}

	// 80 of the dataset's 4 632 names are stand-ins of this shape, and they are dropped
	// by pattern rather than by rank because the rank they mostly sit at is also where
	// Kingman Reef and the Bounty Islands live, which are places with names.
	placeholder := regexp.MustCompile(`^[A-Z]{3}-\d\d \(`)
	for _, name := range names {
		if placeholder.MatchString(name) {
			t.Errorf("WORLD_REGION_NAMES holds %q, which is one of the dataset's stand-ins "+
				"for an area it has no name for; a map that draws it has drawn a database key",
				name)
		}
	}

	points, groups := decodeLabels(t, strings.Trim(exportValue(t, js, "WORLD_REGION_POINTS"), "'"))
	if points != len(names) {
		t.Fatalf("the coordinate stream holds %d point(s) and the name list %d; the two are "+
			"read side by side and a mismatch names every area after the first gap incorrectly",
			points, len(names))
	}
	if groups < 5 {
		t.Errorf("%d group(s) of names; the file groups runs of equal importance, of which "+
			"this one has about 10", groups)
	}
}

// decodeLabels reads a name file's coordinate stream and returns the points and the
// number of groups.
//
// The stream holds no total, only a group count and the groups: each is a zoom, a rank
// and a count, and then that many points. The chain of deltas runs through all of them
// without a reset, so the points are read straight on from one group into the next.
func decodeLabels(t *testing.T, data string) (points, groups int) {
	t.Helper()
	p := newPacked(t, data)

	groups = p.count("the group count", 1000)
	if groups == 0 {
		t.Fatal("the stream holds no groups of names at all")
	}
	lastZoom := -1
	for g := 0; g < groups; g++ {
		zoom := p.count(fmt.Sprintf("group %d's zoom", g), 24)
		rank := p.count(fmt.Sprintf("group %d's rank", g), 255)
		if zoom < lastZoom {
			t.Fatalf("group %d belongs on the map from zoom %d, after a group that belongs "+
				"from zoom %d; the reader walks the groups in order and stops at the first "+
				"name that belongs deeper, so an out-of-order group is a name that is never "+
				"drawn", g, zoom, lastZoom)
		}
		lastZoom = zoom
		_ = rank

		n := p.count(fmt.Sprintf("group %d's label count", g), 100000)
		if n == 0 {
			t.Errorf("group %d (zoom %d, rank %d) holds no labels; a group is a run of names "+
				"and an empty one is a group that was split in two", g, zoom, rank)
		}
		for i := 0; i < n; i++ {
			pt := p.point(1000)
			if pt[0] < -180 || pt[0] > 180 || pt[1] < -90 || pt[1] > 90 {
				t.Fatalf("group %d label %d is at %g,%g, which is off the planet; the delta "+
					"chain has been misread", g, i, pt[0], pt[1])
			}
		}
		points += n
	}
	p.end()
	return points, groups
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

// TestEveryAssetHasAStableFingerprint is about the ETags the browser is given.
//
// A file with no fingerprint is re-downloaded on every page load, and a fingerprint
// that does not match the bytes it belongs to is worse: the browser would keep a
// copy under a tag that means a different file, and no upgrade would ever reach it.
// Both are invisible from the outside, and neither fails to compile.
func TestEveryAssetHasAStableFingerprint(t *testing.T) {
	assets := Assets()
	first := Fingerprints()

	// The map is built once per process. Handing out a different answer the second
	// time would make every validator in every open tab stale at once.
	second := Fingerprints()
	if len(first) != len(second) {
		t.Fatalf("the second call to Fingerprints holds %d file(s), the first %d",
			len(second), len(first))
	}

	seen := 0
	err := fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		seen++
		data, err := fs.ReadFile(assets, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		want := `"` + hex.EncodeToString(sum[:]) + `"`
		if got := first[path]; got != want {
			t.Errorf("Fingerprints()[%q] = %q, want the digest of the file %q", path, got, want)
		}
		if got := second[path]; got != want {
			t.Errorf("the second call gave %q for %q, want %q — an unstable validator "+
				"invalidates every open tab", got, path, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the embedded dashboard: %v", err)
	}
	// The walk has to have found the dashboard at all: an empty tree would satisfy
	// every check above by having nothing to check.
	if seen < 5 || len(first) != seen {
		t.Errorf("%d file(s) embedded and %d fingerprint(s); the two have to be the same "+
			"set, and the dashboard is more than five files", seen, len(first))
	}
}
