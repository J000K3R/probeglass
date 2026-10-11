package main

import (
	"fmt"
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
	if _, _, err := parseDistrictShapes([]byte(small)); err == nil {
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

	out, areas, err := parseDistrictShapes([]byte(b.String()))
	if err != nil {
		t.Fatalf("parseDistrictShapes: %v", err)
	}
	if areas != 120 || len(out) != 120 {
		t.Errorf("%d area(s) and %d ring(s), want 120 of each", areas, len(out))
	}
	for _, shape := range out {
		if shape.minZoom != districtBand {
			t.Fatalf("a district came back with min_zoom %g, want %d. The source has no zoom "+
				"field at all, so this number is chosen and not read, and a district in the "+
				"wrong band is a district that appears at a zoom nobody asked for",
				shape.minZoom, districtBand)
		}
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
