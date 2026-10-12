package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

// These tests are about the two things in this program that are easy to get wrong and
// hard to see: the -minus set, which decides what is drawn twice, and the two parsers
// that read a file whose property names and rank scale are not the ones the rest of the
// datasets use.
//
// What they are not about is the geometry. A ring that comes out with the wrong shape is
// visible on the dashboard the moment anybody looks at it, and the browser's own tests
// decode all nine files and would fail on a stream that does not read back. What is
// invisible is a lake that is missing because its name matched something, a river that
// is missing because its rank was read as a string, and a districts file from the wrong
// query that generated a plausible little file instead of stopping.

const twoLakes = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "properties": {"name": "Zürichsee", "min_zoom": 6},
     "geometry": {"type": "Polygon", "coordinates": [
       [[8.4, 47.2], [8.9, 47.2], [8.9, 47.4], [8.4, 47.4], [8.4, 47.2]],
       [[8.6, 47.25], [8.7, 47.25], [8.7, 47.3], [8.6, 47.3], [8.6, 47.25]]]}},
    {"type": "Feature", "properties": {"name": "Walensee", "min_zoom": 7},
     "geometry": {"type": "Polygon", "coordinates": [
       [[9.0, 47.1], [9.3, 47.1], [9.3, 47.2], [9.0, 47.2], [9.0, 47.1]]]}},
    {"type": "Feature", "properties": {"name": "", "min_zoom": 5},
     "geometry": {"type": "Polygon", "coordinates": [
       [[1, 1], [2, 1], [2, 2], [1, 2], [1, 1]]]}}
  ]
}`

func TestParseNamesReadsOnlyTheNamesThereAre(t *testing.T) {
	set, err := parseNames([]byte(twoLakes))
	if err != nil {
		t.Fatalf("parseNames: %v", err)
	}
	if len(set) != 2 {
		t.Errorf("parseNames found %d name(s), want 2: the third feature has no name and a "+
			"name that is not there is not a name to match on", len(set))
	}
	if !set.has("Zürichsee") || !set.has("Walensee") {
		t.Errorf("parseNames found %v, want the two lakes the file names", set)
	}
	if set.has("") {
		t.Errorf("parseNames kept the empty name. The world lakes file has hundreds of " +
			"unnamed lakes in it, and a set that matches the empty name drops every one of " +
			"them — which is a map with no lakes in some places and a file that looks fine")
	}
	if set.has("Bodensee") {
		t.Errorf("parseNames matched a name that is not in the file")
	}
}

func TestTheMinusSetNeverMatchesAnEmptyName(t *testing.T) {
	// The guard, on its own, because it is the one line that decides whether a file with
	// unnamed shapes in it survives being filtered: has("") is false whatever the set
	// holds, and the empty string is exactly what 610 of the world's lakes have.
	set := nameSet{"": true}
	if set.has("") {
		t.Errorf("nameSet{\"\": true}.has(\"\") is true; an unnamed shape would then be " +
			"dropped by -minus, and -minus is for shapes two files both draw")
	}
}

func TestMinusDropsAWholeLakeAndNotHalfOfOne(t *testing.T) {
	full, areas, dropped, err := parseLakeShapes([]byte(twoLakes), nameSet{})
	if err != nil {
		t.Fatalf("parseLakeShapes: %v", err)
	}
	if areas != 3 || len(full) != 4 {
		t.Fatalf("without -minus: %d area(s) and %d ring(s), want 3 and 4 — two lakes and "+
			"one unnamed, one of them a lake with an island in it: one area, two rings", areas, len(full))
	}
	if dropped != 0 {
		t.Errorf("dropped %d shape(s) with an empty -minus set", dropped)
	}

	kept, areas, dropped, err := parseLakeShapes([]byte(twoLakes), nameSet{"Zürichsee": true})
	if err != nil {
		t.Fatalf("parseLakeShapes: %v", err)
	}
	if dropped != 1 {
		t.Errorf("dropped %d feature(s), want 1", dropped)
	}
	if areas != 2 || len(kept) != 2 {
		t.Errorf("after -minus: %d area(s) and %d ring(s), want 2 and 2. Both rings of the "+
			"Zürichsee have to go: a lake with an island in it is two rings of one lake, and "+
			"half a lake drawn on top of the complete one is worse than either", areas, len(kept))
	}
	if kept[0].minZoom != 7 {
		t.Errorf("the surviving lake has min_zoom %g, want 7 — the Walensee", kept[0].minZoom)
	}
}

const twoRivers = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "properties": {"name": "Rhein", "scalerank": 4.0, "min_zoom": 2},
     "geometry": {"type": "LineString", "coordinates": [[7.6, 47.6], [8.2, 47.9], [8.6, 48.4]]}},
    {"type": "Feature", "properties": {"name": "Ill", "scalerank": 11, "min_zoom": 6.7},
     "geometry": {"type": "LineString", "coordinates": [[7.2, 47.4], [7.1, 47.7]]}},
    {"type": "Feature", "properties": {"name": "Birs", "scalerank": 5, "min_zoom": 4},
     "geometry": {"type": "LineString", "coordinates": [[7.5, 47.3], [7.6, 47.5]]}}
  ]
}`

func TestTheRankCutAndTheMinusSetEachDropWhatTheyShould(t *testing.T) {
	// Scalerank is written as 4.0 in the 1:10m river file and as 11 in the Europe one,
	// and the generator reads it as a number with a decimal point. Read as an int it is
	// a parse failure and the whole file is refused — which is how the rivers of
	// Vorarlberg were nearly not drawn at all, so this is the assertion that keeps the
	// reader a float.
	out, kept, total, dropped, err := parseRiverShapes([]byte(twoRivers), 4, nameSet{})
	if err != nil {
		t.Fatalf("parseRiverShapes: %v", err)
	}
	if total != 3 {
		t.Errorf("total is %d, want 3 — the source count is what the header reports as "+
			"\"N of M\", and it is a fact about the file rather than about the cut", total)
	}
	if kept != 1 {
		t.Errorf("kept is %d, want 1: rank 4 is the cut and the cut includes it, so the Rhine "+
			"stays and the Birs at 5 and the Ill at 11 go", kept)
	}
	if len(out) != 1 || out[0].points[0][0] != 7.6 {
		t.Errorf("the rank cut at 4 kept %d line(s), want the Rhine alone", len(out))
	}
	if dropped != 0 {
		t.Errorf("dropped %d line(s) with an empty -minus set", dropped)
	}

	// And with -minus: the Rhine is named in the world file, so the Europe copy of it
	// must go even though it is in rank — while `kept`, which is the header's rank line,
	// stays a count of the source and does not move.
	out, kept, total, dropped, err = parseRiverShapes([]byte(twoRivers), 12, nameSet{"Rhein": true})
	if err != nil {
		t.Fatalf("parseRiverShapes: %v", err)
	}
	if dropped != 1 || len(out) != 2 {
		t.Errorf("-minus dropped %d line(s) and left %d, want 1 and 2", dropped, len(out))
	}
	if kept != 3 {
		t.Errorf("kept is %d after -minus; the count is of the source and the rank cut, so "+
			"-minus must not move it — otherwise the header's \"%d of %d\" line would shrink "+
			"every time a shape was skipped and would stop describing the file", kept, kept, total)
	}
	if total != 3 {
		t.Errorf("total is %d, want 3", total)
	}
}

func TestTheDistrictFileIsRefusedWhenItIsNotTheWholeCountry(t *testing.T) {
	// The failure this guards is a query that answered for one state, or a WFS that
	// returned its first page: a file with a handful of districts in it generates
	// happily, and a map with three districts on it is indistinguishable from a map of a
	// country that has three districts.
	small := `{"type":"FeatureCollection","features":[
	  {"type":"Feature","properties":{"g_id":"1","g_name":"Bregenz"},
	   "geometry":{"type":"Polygon","coordinates":[[[9.7,47.4],[9.8,47.4],[9.8,47.5],[9.7,47.5],[9.7,47.4]]]}}
	]}`
	if _, _, _, _, err := parseDistricts([]byte(small)); err == nil {
		t.Errorf("a one-district file was accepted; Austria has 117 and a file that answers " +
			"with one of them is a query that went wrong, not a country that lost its districts")
	} else if !strings.Contains(err.Error(), "Austria") {
		t.Errorf("the refusal is %q; it has to say what the file is not, because the person "+
			"reading it is looking at a WFS URL with a year in it", err)
	}

	// And the whole of it, built rather than pasted: 120 districts in a grid, which is
	// enough to pass the guard, and every ring has to come back in the one band the file
	// chooses. The band is the whole point of this kind — the Austrian register has no
	// zoom in it, so 8 is a decision and this is where it is checked.
	var b strings.Builder
	b.WriteString(`{"type":"FeatureCollection","features":[`)
	for i := range 120 {
		if i > 0 {
			b.WriteString(",")
		}
		lon := 9.5 + float64(i%12)*0.6
		lat := 46.5 + float64(i/12)*0.5
		fmt.Fprintf(&b, `{"type":"Feature","properties":{"g_id":"%d","g_name":"Bezirk %d"},
		  "geometry":{"type":"Polygon","coordinates":[[[%g,%g],[%g,%g],[%g,%g],[%g,%g],[%g,%g]]]}}`,
			i, i, lon, lat, lon+0.5, lat, lon+0.5, lat+0.4, lon, lat+0.4, lon, lat)
	}
	b.WriteString(`]}`)

	out, labels, areas, rings, err := parseDistricts([]byte(b.String()))
	if err != nil {
		t.Fatalf("parseDistricts: %v", err)
	}
	if areas != 120 || rings != 120 || len(out) != 120 {
		t.Errorf("%d area(s), %d ring(s) and %d shape(s), want 120 of each", areas, rings, len(out))
	}
	for _, shape := range out {
		if shape.minZoom != districtBand {
			t.Fatalf("a district came back with min_zoom %g, want %d. The source has no zoom "+
				"field at all, so this number is chosen and not read, and a district in the "+
				"wrong band is a district that appears at a zoom nobody asked for",
				shape.minZoom, districtBand)
		}
	}

	// The names are the half of this file that is not geometry, and they are the reason
	// the districts are readable at all: 117 outlines on their own are 117 shapes with
	// nothing on them. One per district, none empty, and each on the middle of its own
	// square — the grid is regular, so any other anchor is a centroid that went wrong.
	if len(labels) != 120 {
		t.Fatalf("%d name(s) for 120 areas; every district carries a g_name and every one "+
			"of them has to reach the map", len(labels))
	}
	for i, l := range labels {
		if l.name != fmt.Sprintf("Bezirk %d", i) {
			t.Fatalf("name %d is %q, want %q", i, l.name, fmt.Sprintf("Bezirk %d", i))
		}
		wantLon := 9.5 + float64(i%12)*0.6 + 0.25
		wantLat := 46.5 + float64(i/12)*0.5 + 0.2
		if math.Abs(l.lon-wantLon) > 1e-9 || math.Abs(l.lat-wantLat) > 1e-9 {
			t.Errorf("%s is anchored at (%g, %g), want (%g, %g): the anchor is the middle of "+
				"the district and a name anywhere else is a name on the wrong district",
				l.name, l.lon, l.lat, wantLon, wantLat)
		}
		if l.zoom != districtBand || l.rank != 0 {
			t.Errorf("%s carries zoom %d and rank %d, want %d and 0", l.name, l.zoom, l.rank, districtBand)
		}
	}
}

func TestADistrictIsNamedInTheMiddleOfItselfAndNotOfItsOutline(t *testing.T) {
	// The six that are not plain: a district with a statutory city cut out of it. The
	// hole is off centre on purpose, because a hole in the middle of a square shifts a
	// centroid by nothing at all and would pass whether the subtraction happened or not.
	//
	// The square is (0,0) to (1,1); the hole is (0.7,0.7) to (0.9,0.9), area 0.04. The
	// outline's own centre is (0.5,0.5); taking the hole back out moves both coordinates
	// away from it, to (0.5 - 0.8*0.04) / 0.96.
	polys, err := polygonsOf("Polygon", json.RawMessage(`[
	     [[0,0],[1,0],[1,1],[0,1],[0,0]],
	     [[0.7,0.7],[0.9,0.7],[0.9,0.9],[0.7,0.9],[0.7,0.7]]]`))
	if err != nil {
		t.Fatalf("polygonsOf: %v", err)
	}

	lon, lat := districtAnchor(polys)
	want := (0.5 - 0.8*0.04) / 0.96
	if math.Abs(lon-want) > 1e-9 || math.Abs(lat-want) > 1e-9 {
		t.Errorf("the anchor is (%g, %g), want (%g, %g). A centroid that ignored the hole "+
			"would be (0.5, 0.5), and on a real district that is the statutory city that "+
			"sits inside it — Graz inside Graz-Umgebung — which is a different district "+
			"with a name of its own",
			lon, lat, want, want)
	}

	// Largest polygon wins, and the smaller one is not allowed to pull the anchor. A
	// district with an exclave in it is still mostly where it is.
	two := [][][][2]float64{
		{{{0, 0}, {1, 0}, {1, 1}, {0, 1}, {0, 0}}},
		{{{8, 8}, {8.1, 8}, {8.1, 8.1}, {8, 8.1}, {8, 8}}},
	}
	lon, lat = districtAnchor(two)
	if math.Abs(lon-0.5) > 1e-9 || math.Abs(lat-0.5) > 1e-9 {
		t.Errorf("the anchor is (%g, %g) for a shape whose smaller half is at (8.05, 8.05); "+
			"the name belongs on the larger polygon", lon, lat)
	}

	// A ring with no area is a division by zero in the shoelace sum, and the middle of
	// its bounding box is the answer that costs nothing and lands in the right country.
	lon, lat = districtAnchor([][][][2]float64{{{{2, 4}, {6, 4}, {4, 4}, {2, 4}}}})
	if math.Abs(lon-4) > 1e-9 || math.Abs(lat-4) > 1e-9 {
		t.Errorf("a degenerate ring is anchored at (%g, %g), want the middle of its box "+
			"(4, 4)", lon, lat)
	}
}

// A GeoNames row is nineteen tab-separated columns and nothing else, so the test builds
// them in that shape rather than pasting one: the column a value sits in is the only thing
// identifying it, there is no header to check it against, and a row that is one column
// short shifts every field after the gap into the wrong one — which for this file means a
// population read as a longitude.
//
// The indexes here are the ones the parser reads: 1 name, 4 latitude, 5 longitude, 7
// feature code, 8 country code, 14 population. The other thirteen are filled with
// something that is not empty so that a parser reading the wrong column gets a wrong
// answer rather than an empty one.
func geonamesRow(geonameid, name, lat, lon, feature, country, population string) string {
	other := []string{"", "", "", "", "", "", "", "", "", "", "", "", ""}
	row := make([]string, 19)
	copy(row, other)
	row[0], row[1] = geonameid, name
	row[2], row[3] = name, name
	row[4], row[5], row[6], row[7] = lat, lon, "P", feature
	row[8], row[9] = country, ""
	row[10], row[11], row[12], row[13] = "01", "", "", ""
	row[14], row[15], row[16], row[17], row[18] = population, "", "150", "Europe/Vienna", "2026-01-01"
	return strings.Join(row, "\t")
}

// TestATownIsNotAQuarterOfACityAndNotAVillage is about the two rules that decide what
// towns.js holds, both of which fail invisibly.
//
// The first is the section rule. GeoNames files all 23 of Vienna's districts under the
// feature code PPLX, each with the population of the quarter and each a place of its own,
// so a file without the rule in it has twenty-two names across one city — Favoriten at
// 201 882 and Donaustadt at 187 007 — and the map reads as though it had been tagged by
// somebody walking around Vienna.
//
// The second is the cut. It is what makes the file a megabyte instead of fifteen, and it
// is the one thing in this file a person can move: -min-population 50000 is a map of the
// larger towns and a tenth of the size.
func TestATownIsNotAQuarterOfACityAndNotAVillage(t *testing.T) {
	source := strings.Join([]string{
		geonamesRow("2761369", "Vienna", "48.20849", "16.37208", "PPLC", "AT", "1691468"),
		geonamesRow("8063098", "Favoriten", "48.16891", "16.37546", "PPLX", "AT", "201882"),
		geonamesRow("2761524", "Wels", "48.15731", "14.02494", "PPLA3", "AT", "16946"),
		geonamesRow("7871499", "Kremsmünster", "48.05404", "14.12818", "PPL", "AT", "6585"),
		geonamesRow("2867714", "Munich", "48.13743", "11.57549", "PPLA", "DE", "1505005"),
	}, "\n") + "\n"

	labels, counts, err := parseTownLabels([]byte(source), countryFilter{}, 10000)
	if err != nil {
		t.Fatalf("parseTownLabels: %v", err)
	}
	if counts.total != 5 {
		t.Errorf("total is %d, want 5 — it is a fact about the file rather than about the "+
			"cut, and the header's \"of the N in the file\" line is written from it", counts.total)
	}
	if counts.sections != 1 {
		t.Errorf("sections is %d, want 1 — Favoriten is a PPLX and it is the one row that "+
			"is a quarter of a city rather than a city", counts.sections)
	}
	if counts.above != 3 {
		t.Errorf("above is %d, want 3: Vienna, Wels and Munich are at or above the cut and "+
			"are not sections; Kremsmünster is below it and Favoriten is a section. The "+
			"number is the population cut on its own, before -only, so it is what the "+
			"header can print without lying when somebody keeps one country", counts.above)
	}
	if len(labels) != 3 {
		t.Fatalf("%d label(s), want 3 — Vienna, Wels and Munich", len(labels))
	}
	for _, l := range labels {
		if l.name == "Favoriten" {
			t.Errorf("Favoriten reached the map; it is a district of Vienna filed as a place " +
				"of its own, and twenty-two of them would be drawn across the city")
		}
		if l.name == "Kremsmünster" {
			t.Errorf("Kremsmünster reached the map at %d inhabitant(s); the cut is 10 000", 6585)
		}
	}

	// The zoom is derived from the population and from nothing else, because this file
	// has no rank in it. Vienna at 1.69 million and Wels at 16 946 are five steps apart,
	// which is the whole of the rule: ten times as many people, one step nearer.
	if labels[0].zoom != 5 || labels[1].zoom != 11 {
		t.Errorf("Vienna came out at zoom %d and Wels at %d, want 5 and 11 — the ladder is "+
			"a staircase of population, and a town that arrived at the wrong step would "+
			"either be lost among the capitals or drawn over a city",
			labels[0].zoom, labels[1].zoom)
	}
	if labels[0].rank != 0 {
		t.Errorf("Vienna carries rank %d; this file has no ranks in it at all, and a rank "+
			"invented here would sort the towns against each other by a rule the source "+
			"does not have", labels[0].rank)
	}

	// And -only, which is the two-letter code here. The three-letter one is what every
	// other name file takes and it matches nothing in this one, which is why the run()
	// refuses it rather than writing an empty file.
	aut, counts, err := parseTownLabels([]byte(source), newCountryFilter("AT"), 10000)
	if err != nil {
		t.Fatalf("parseTownLabels -only AT: %v", err)
	}
	if len(aut) != 2 || counts.above != 3 {
		t.Errorf("-only AT kept %d label(s) and reported %d above the cut; the filter is the "+
			"last thing applied, so the cut's own count must not move when a country is "+
			"picked", len(aut), counts.above)
	}
	if _, _, err := parseTownLabels([]byte(source), newCountryFilter("AUT"), 10000); err == nil {
		t.Errorf("-only AUT was accepted; GeoNames files a place under \"AT\" and there is no " +
			"Austria under that spelling in this file, so a run that kept nothing has to say " +
			"so rather than write a towns file with no towns in it")
	}
}

func TestTheTownZoomLadderIsAStepPerPowerOfTen(t *testing.T) {
	// The ladder is the one number this program invents for GeoNames, so it is checked
	// at every step and at the boundary of each: a place exactly at a threshold belongs
	// to the deeper of the two groups, which is what "at least" means and what makes the
	// numbers in the header reproducible.
	for _, tc := range []struct {
		population int
		zoom       int
	}{
		{20_000_000, 4}, {5_000_000, 4}, {4_999_999, 5},
		{1_000_000, 5}, {999_999, 6},
		{500_000, 6}, {499_999, 7},
		{200_000, 7}, {199_999, 8},
		{100_000, 8}, {99_999, 9},
		{50_000, 9}, {49_999, 10},
		{20_000, 10}, {19_999, 11},
		{10_000, 11}, {9_999, 11},
	} {
		if got := townZoom(tc.population); got != tc.zoom {
			t.Errorf("townZoom(%d) = %d, want %d", tc.population, got, tc.zoom)
		}
	}
}

func TestARowWithTheWrongNumberOfColumnsIsRefused(t *testing.T) {
	// The failure this guards is not a corrupt file but a subtly different one: a line
	// that is one tab short shifts every field after the gap, so the population is read
	// out of the elevation column and the town lands at a zoom derived from a number
	// about a mountain. Nothing about the output would look wrong.
	short := geonamesRow("2761369", "Vienna", "48.20849", "16.37208", "PPLC", "AT", "1691468")
	short = short[:strings.LastIndex(short, "\t")]
	if _, _, err := parseTownLabels([]byte(short+"\n"), countryFilter{}, 10000); err == nil {
		t.Errorf("a row with 18 columns was accepted; every row of this file has 19 and the " +
			"column a value is in is the only thing identifying it")
	} else if !strings.Contains(err.Error(), "column") {
		t.Errorf("the refusal is %q; it has to say that the shape of the row is what is "+
			"wrong, because nothing else about the failure is visible", err)
	}
}

const threePlaces = `{
  "type": "FeatureCollection",
  "features": [
    {"type": "Feature", "properties": {"name": "Bregenz", "adm0_a3": "AUT", "min_zoom": 8.0, "scalerank": 10, "featurecla": "Admin-1 capital"},
     "geometry": {"type": "Point", "coordinates": [9.7471, 47.5031]}},
    {"type": "Feature", "properties": {"name": "Vaduz", "adm0_a3": "LIE", "min_zoom": 6.7, "scalerank": 7, "featurecla": "Admin-0 capital"},
     "geometry": {"type": "Point", "coordinates": [9.5215, 47.1415]}},
    {"type": "Feature", "properties": {"name": "Halley Bay", "adm0_a3": "ATA", "min_zoom": 4.7, "scalerank": 8, "featurecla": "Scientific station"},
     "geometry": {"type": "Point", "coordinates": [-26.5, -75.6]}}
  ]
}`

func TestCityLabelsKeepPlacesAndDropResearchStations(t *testing.T) {
	// All three, no filter.
	all, err := parseCityLabels([]byte(threePlaces), countryFilter{})
	if err != nil {
		t.Fatalf("parseCityLabels: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("%d label(s), want 2: the scientific station is a coordinate with a name on "+
			"it, and a map that labels an Antarctic hut the way it labels Bregenz has told "+
			"the reader that somebody lives there", len(all))
	}
	if all[0].name != "Bregenz" {
		t.Fatalf("the first label is %q; the labels have to come out in the file's order so "+
			"the stream and the name array stay in step", all[0].name)
	}
	if all[0].zoom != 8 {
		t.Errorf("Bregenz wants zoom %d; the file says 8.0 and the map rounds a half or a "+
			"third of a zoom up the way it does everywhere else", all[0].zoom)
	}
	if all[1].zoom != 7 {
		t.Errorf("Vaduz wants zoom %d; the file says 6.7, which rounds up to 7", all[1].zoom)
	}
	if all[0].rank != 10 || all[0].lon != 9.7471 || all[0].lat != 47.5031 {
		t.Errorf("Bregenz came out as %+v", all[0])
	}

	// And -only, which works here because a city is in exactly one country — the thing
	// a border file cannot say.
	aut, err := parseCityLabels([]byte(threePlaces), newCountryFilter("AUT"))
	if err != nil {
		t.Fatalf("parseCityLabels: %v", err)
	}
	if len(aut) != 1 || aut[0].name != "Bregenz" {
		t.Errorf("-only AUT kept %d label(s), want Bregenz alone", len(aut))
	}

	// A filter that matches nothing is an error rather than an empty file: a names file
	// with no names in it is a map with no names on it, which is what a working map with
	// the switch off also looks like.
	if _, err := parseCityLabels([]byte(threePlaces), newCountryFilter("NZL")); err == nil {
		t.Errorf("-only NZL was accepted; there is no New Zealand in this file and a file " +
			"with nothing in it is indistinguishable from a file that failed")
	}
}
