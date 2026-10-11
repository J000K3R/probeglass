// Command worldgen turns Natural Earth files into the map data the dashboard draws.
//
// The dashboard ships a slippy map with no mapping library and, since the tile
// servers stopped being usable by anybody but their own websites, no tiles either.
// What it draws instead is this: the world's coastlines, its country and
// first-order administrative borders, and the names of the places on it — all from
// files small enough to embed in the binary. The map is still a real map: places
// land where they belong, a border is where the border is, and the graticule
// underneath is honest. It simply has nothing on it that somebody else's server had
// to be asked for.
//
// It reads a file and writes a file, once per dataset — the kind of thing being
// generated decides the parser, the encoding and the header:
//
//	kind      reads                                     writes
//	land      ne_50m_land.geojson                        world.js
//	borders   ne_10m_admin_1_states_provinces_lines      borders.js
//	labels    ne_110m_admin_0_countries                  labels.js
//	regions   ne_10m_admin_1_label_points                regions.js
//
// It is the only thing in this repository that reads something nobody committed. So
// the output carries everything needed to reproduce it: the source URL, the checksum
// of the input, the command line, and the licence. Fetching the input is not this
// program's business — it is four curls in docs/CONFIGURATION.md, so that regenerating
// a file is a thing a person can actually do rather than a thing this program would
// have to be trusted about.
//
// # The format
//
// Every file is a string of six-bit characters, in the base64 alphabet, decoded by
// internal/web/assets/outline.js. A varint is base 63 written most significant chunk
// first, closed by a chunk of 63. The closing chunk is one character per number and it
// is the price of having a marker at all: six bits is exactly enough for 64 symbols and
// no more, so a digit and a "there is more" cannot share one. The digits are one
// character each up to 62, which every delta between two neighbouring points of a
// coastline is, and two or three for the first point of a line, which is absolute.
//
// Deltas are most of the saving: consecutive points of a coastline are a few
// thousandths of a degree apart, so almost the whole file is two characters per
// coordinate. It also keeps the output to one line of source text, which is why a
// 35 000-point world costs about 70 KB — a fortieth of what the same rings cost as
// the GeoJSON they came from.
//
// Three streams are written in that alphabet, and they differ only in how they are
// cut up:
//
//	land            a ring count, then per ring: a point count, then the points
//	borders         one such stream per zoom band, concatenated. Each is
//	                self-delimiting — it begins with its own line count — so a reader
//	                walks them in order and never needs an offset into the file
//	labels, regions a group count, then per group: the zoom, the rank, a label count,
//	                then the labels as longitude and latitude only. Grouping is what
//	                makes the rank and the zoom nearly free: consecutive labels of
//	                equal importance are one group rather than 4 numbers each
//
// In every stream the first point is absolute and every other is a delta from the one
// before it, so a reader accumulates from zero and has no special case to forget. A
// ring in land.js is its own chain — each ring starts absolute, so one dropped or
// reordered ring cannot shift the rest. In borders.js each line is. In labels.js and
// regions.js the chain runs once through the whole stream, groups and all, because a
// label is a point and there is nothing to reorder around it.
//
// The names are not in that alphabet. They are a plain JSON array of strings, written
// by encoding/json — text is not something to invent an encoding for, and UTF-8 in a
// source file is what every other name in this project already is.
//
// Coordinates are quantised to a thousandth of a degree and lines are simplified
// before that. Both were chosen against the map they feed: at zoom 19, one thousandth
// of a degree is a hundredth of a pixel, and the map does not go past zoom 19. The
// simplification is Douglas-Peucker at a tolerance in degrees, which over-simplifies
// longitude near the poles — see the note in docs/CONFIGURATION.md for what that costs
// and what to do about it.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// The command line, one flag set for all four kinds. The defaults that matter differ
// per kind — a coastline and a border are not simplified the same way, and a ring is
// not a line — so they are applied after parsing, from the kind, and only to the flags
// nobody set.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worldgen:", err)
		os.Exit(1)
	}
}

func run() error {
	kind := flag.String("kind", "land", "what to generate: land, borders, labels or regions")
	in := flag.String("in", "", "Natural Earth GeoJSON file to read (see docs/CONFIGURATION.md for where to get it)")
	out := flag.String("out", "", "file to write; the default is the asset belonging to -kind")
	tol := flag.Float64("tol", 0, "Douglas-Peucker tolerance in degrees; 0 means the default for -kind")
	minPoints := flag.Int("min-points", 0, "drop lines left with fewer points than this after simplifying; 0 means the default for -kind")
	only := flag.String("only", "", "keep only these countries (comma-separated ADM0_A3 codes); empty keeps all of them")
	maxRank := flag.Int("max-rank", 0, "labels, regions: drop names ranked less important than this; 0 means no limit")
	flag.Parse()

	if *in == "" {
		return fmt.Errorf("-in is required: this program does not fetch anything, and says so on purpose")
	}

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })
	// defaulted takes the flag from the kind when nobody asked for one. A tolerance is
	// not a property of a file format, it is a property of the thing being drawn:
	// 0.02 flattens a coastline at the same visible cost as 0.05 flattens a border.
	defaulted := func(name string, value float64, fallback float64) float64 {
		if given[name] {
			return value
		}
		return fallback
	}

	source, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sum := sha256hex(source)
	name := filepath.Base(*in)

	// -out is derived from -kind rather than shared, and that is not tidiness: a
	// single default of world.js would mean an invocation with no -out quietly
	// overwrote the coastline with whatever it did generate.
	if *out == "" {
		asset, ok := map[string]string{
			"land": "world.js", "borders": "borders.js",
			"labels": "labels.js", "regions": "regions.js",
		}[*kind]
		if !ok {
			return fmt.Errorf("-kind %q is not one of land, borders, labels, regions", *kind)
		}
		*out = filepath.Join("internal", "web", "assets", asset)
	}

	filter := newCountryFilter(*only)

	switch *kind {
	case "land":
		return runLand(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 4), source)
	case "borders":
		return runBorders(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 2), filter, source)
	case "labels":
		return runLabels(name, sum, *in, *out, filter, *maxRank, source)
	case "regions":
		return runRegions(name, sum, *in, *out, filter, *maxRank, source)
	}
	return fmt.Errorf("-kind %q is not one of land, borders, labels, regions", *kind)
}

func defaultInt(given map[string]bool, name string, value, fallback int) int {
	if given[name] {
		return value
	}
	return fallback
}

// runLand writes the coastline.
func runLand(name, sum, in, out string, tol float64, minPoints int, source []byte) error {
	rings, err := parseLand(source)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	before := countPoints(rings)

	kept := make([][][2]float64, 0, len(rings))
	for _, ring := range rings {
		simplified := simplify(ring, tol)
		if len(simplified) < minPoints {
			continue
		}
		kept = append(kept, simplified)
	}
	if len(kept) == 0 {
		return fmt.Errorf("%s: every ring was simplified away; is -tol %g meant for a different dataset?",
			in, tol)
	}

	enc := &encoder{}
	enc.varint(uint64(len(kept)))
	for _, ring := range kept {
		enc.line(ring)
	}

	body := fmt.Sprintf(`// The world's coastlines, for the map to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind land -in <the file below> -out internal/web/assets/world.js
//
%s
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %d of the %d points in the source, %.0f%% kept.
//            %s
//
// The borders and the names are next to this file, from the same generator and with
// the same kind of header: borders.js, labels.js, regions.js. See
// docs/CONFIGURATION.md for where the input comes from.

export const WORLD_RINGS = %d;
export const WORLD_SCALE = 1000;
export const WORLD_DATA = '%s';
`, provenance(landSource, name, sum), tol, minPoints, enc.points, before,
		100*float64(enc.points)/float64(before), sharpness(tol), len(kept), enc.data())

	return write(out, body, fmt.Sprintf("%s: %d ring(s), %d point(s) in, %d out, %.1f%% kept",
		in, len(kept), before, enc.points, 100*float64(enc.points)/float64(before)))
}

// runBorders writes the first-order administrative borders, grouped into the zoom
// bands Natural Earth itself puts them in.
//
// The grouping is the whole design of this file. Every line in the source carries a
// MIN_ZOOM — the zoom its author drew it for — and drawing all of them at every zoom
// would put the detail of a city map on a view of Europe. So a band holds the lines
// whose MIN_ZOOM is that band or less, and the map shows band b when it is at zoom b
// or deeper: at zoom 3 it draws the two bands that belong there and not the other
// seven, which is why a world view costs 13 KB of path where the whole file is 600.
func runBorders(name, sum, in, out string, tol float64, minPoints int, filter countryFilter, source []byte) error {
	lines, classLines, fileLines, err := parseLines(source)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}

	byBand := map[int][][][2]float64{}
	points, kept := 0, 0
	for _, line := range lines {
		if !filter.keep(line.country) {
			continue
		}
		simplified := simplify(line.points, tol)
		if len(simplified) < minPoints {
			continue
		}
		band := int(math.Ceil(line.minZoom))
		byBand[band] = append(byBand[band], simplified)
		points += len(simplified)
		kept++
	}

	bands := make([]int, 0, len(byBand))
	for band := range byBand {
		bands = append(bands, band)
	}
	sort.Ints(bands)
	if len(bands) == 0 {
		return fmt.Errorf("%s: no border survived -only and -min-points %d; is this the "+
			"admin-1 line file, and is -only a real country code?", in, minPoints)
	}

	// One stream per band, concatenated in band order. Each begins with its own line
	// count, so the reader walks them in order and needs no offsets.
	enc := &encoder{}
	table := make([]string, 0, len(bands))
	for _, band := range bands {
		bandLines := byBand[band]
		enc.varint(uint64(len(bandLines)))
		bandPoints := 0
		for _, line := range bandLines {
			enc.line(line)
			bandPoints += len(line)
		}
		table = append(table, fmt.Sprintf("//              zoom %-3d %5d line(s), %6d point(s)",
			band, len(bandLines), bandPoints))
	}
	zoomList := make([]string, 0, len(bands))
	for _, band := range bands {
		zoomList = append(zoomList, fmt.Sprint(band))
	}

	body := fmt.Sprintf(`// The world's first-order administrative borders — states, provinces, German
// Bundesländer and everything else at that level — for the map to draw when no tile
// server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind borders -in <the file below> -out internal/web/assets/borders.js
//
%s
// Filter:    FEATURECLA == "Admin-1 boundary". The source holds %d lines and %d of them
//            are that class; the rest are "Admin-1 region boundary", "Admin-1
//            statistical boundary" and "Admin-1 statistical meta bounds", which are
//            further subdivisions of the same ground. Drawing those too would put a
//            second line on every border.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            A line is not a ring: a border that simplifies to two points is still a
//            border, which is why the floor is 2 here and 4 for the coastline. Drop
//            straight borders and Germany keeps 7%% of its border length, Switzerland
//            2%% — the shapes that vanish are the ones that are actually straight.
// Bands:     Natural Earth gives every line a MIN_ZOOM, the zoom its author drew it
//            for, and the lines are grouped by it here. WORLD_BORDER_BANDS lists those
//            groups in the order they appear in the data, and a map at zoom z draws
//            every band whose number is z or less — the lines up to and including the
//            zoom they were drawn for, and none of the finer ones.
%s
//
// At a world view this draws about 13 KB of path; the whole file is worth having for
// the deep zooms. The setting that turns this layer off is Settings → appearance.

export const WORLD_BORDER_BANDS = [%s];
export const WORLD_BORDER_DATA = '%s';
`, provenance(borderSource, name, sum), fileLines, classLines, tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), strings.Join(zoomList, ", "), enc.data())

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) in the source class, %d of %d in all, %d point(s), band(s) %v",
		in, kept, classLines, kept, fileLines, points, bands))
}

// runLabels writes the country names.
func runLabels(name, sum, in, out string, filter countryFilter, maxRank int, source []byte) error {
	labels, err := parseCountryLabels(source, filter)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	return writeLabels(name, sum, in, out, labels, maxRank, countrySource)
}

// runRegions writes the names of the first-order administrative areas — the German
// Bundesländer with the names they have in German, the Swiss cantons, the French
// départements, the American states, and the rest of the world's provinces.
func runRegions(name, sum, in, out string, filter countryFilter, maxRank int, source []byte) error {
	labels, err := parseRegionLabels(source, filter)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	return writeLabels(name, sum, in, out, labels, maxRank, regionSource)
}

// writeLabels is the half of the two label kinds that is the same: sort into groups,
// encode, render, write.
func writeLabels(name, sum, in, out string, labels []label, maxRank int, src source) error {
	if maxRank > 0 {
		kept := labels[:0]
		for _, l := range labels {
			if l.rank <= maxRank {
				kept = append(kept, l)
			}
		}
		labels = kept
	}
	if len(labels) == 0 {
		return fmt.Errorf("%s: no name survived -only and -max-rank %d", in, maxRank)
	}

	data, names, groups := encodeLabels(labels)
	// The names are text, and text is not something to invent an encoding for. The
	// marshaller escapes the four characters that would end a script early and leaves
	// the rest of UTF-8 alone, which is what makes "Kärnten" readable in the file.
	encodedNames, err := json.Marshal(names)
	if err != nil {
		return err
	}

	body := fmt.Sprintf(`// The names %s, for the map
// to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator and run it
// again:
//
//	go run ./cmd/worldgen -kind %s -in <the file below> -out internal/web/assets/%s
//
%s
// Grouped:   %d group(s) by zoom and rank, %d name(s) in all. A group is a run of
//            names the source rates as equally important that belongs on the map from
//            the same zoom, which is why they are stored together: the two numbers are
//            written once per group instead of once per name.
// Names:     in the source's own spelling and its own language — "Niedersachsen", not
//            "Lower Saxony", and "Kärnten", not "Carinthia". Nothing here translates
//            them, and that is deliberate: they are what a person living there calls
//            the place, and the map is not a document that has to read as one language.
%s// Sizes:     %d name(s) of the %s in the dataset.
//            -max-rank drops the least important of them and is the knob that makes
//            this file smaller; -only keeps one country and is the one that makes it
//            small enough to read at all.
//
// The stream is a group count, then per group its zoom, its rank, a label count, and
// the labels as longitude and latitude in thousandths of a degree — the first one
// absolute, every other a delta from the one before, chain and all across groups.
//
// The names are the JSON array at the bottom, in the same order as the labels. Text is
// not something to invent an encoding for.

export const WORLD_%s_POINTS = '%s';
export const WORLD_%s_NAMES = %s;
`, src.noun, src.kind, filepath.Base(out), provenance(src, name, sum),
		groups, len(names), src.picked, len(names), src.expected,
		src.upper, data, src.upper, encodedNames)

	return write(out, body, fmt.Sprintf("%s: %d name(s) in %d group(s)", in, len(names), groups))
}

// label is one place name and where it goes on the map.
type label struct {
	lon, lat float64
	rank     int // the source's own importance, smaller is more important
	zoom     int // the shallowest zoom the name belongs on
	name     string
}

// encodeLabels packs the labels and returns the stream, the names in the same order,
// and how many groups there are.
//
// The order is (zoom, then rank) between groups and longitude within one, which is
// what makes the deltas small: names of equal importance that belong on the map at the
// same zoom tend to be in the same part of the world.
//
// The comparison carries on through latitude and the name, which is not about the
// deltas at all. The labels arrive in no particular order — the province list is built
// by walking a map, and Go walks a map in a different order every run — so a comparison
// that stopped at the longitude would leave two names at the same point to be written
// either way round, and the file would differ from one run to the next. The header
// promises that this file can be reproduced byte for byte, and a promise like that is
// kept by the last line of a comparator, not by the first three. Two names that match
// on all five are interchangeable, so what is left is still deterministic.
func encodeLabels(labels []label) (data string, names []string, groups int) {
	sort.Slice(labels, func(i, j int) bool {
		a, b := labels[i], labels[j]
		if a.zoom != b.zoom {
			return a.zoom < b.zoom
		}
		if a.rank != b.rank {
			return a.rank < b.rank
		}
		if a.lon != b.lon {
			return a.lon < b.lon
		}
		if a.lat != b.lat {
			return a.lat < b.lat
		}
		return a.name < b.name
	})

	enc := &encoder{}
	var runs [][]label
	for i := 0; i < len(labels); {
		j := i
		for j < len(labels) && labels[j].zoom == labels[i].zoom && labels[j].rank == labels[i].rank {
			j++
		}
		runs = append(runs, labels[i:j])
		i = j
	}

	enc.varint(uint64(len(runs)))
	names = make([]string, 0, len(labels))
	for _, run := range runs {
		enc.varint(byteValue(run[0].zoom))
		enc.varint(byteValue(run[0].rank))
		enc.varint(uint64(len(run)))
		for _, l := range run {
			enc.point([2]float64{l.lon, l.lat})
			names = append(names, l.name)
		}
	}
	return enc.data(), names, len(runs)
}

// encoder writes the packed streams.
//
// It holds the running point of a chain — an int64 pair in thousandths of a degree —
// because the difference from the point before is the whole encoding. A line resets it,
// which makes that line's first point absolute; a point does not, which is how the
// label streams run one chain from beginning to end.
type encoder struct {
	b      strings.Builder
	points int
	lon    int64
	lat    int64
}

func (e *encoder) data() string { return e.b.String() }

// varint writes v as base 63, most significant chunk first, closed by a chunk of 63.
func (e *encoder) varint(v uint64) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	var digits []byte
	for {
		digits = append(digits, byte(v%63))
		v /= 63
		if v == 0 {
			break
		}
	}
	for i := len(digits) - 1; i >= 0; i-- {
		e.b.WriteByte(alphabet[digits[i]])
	}
	e.b.WriteByte(alphabet[63])
}

// zigzag interleaves the sign into the low bit, so -1 becomes 1, 1 becomes 2 and -2
// becomes 3 — small negatives stay as short as small positives.
//
// The two conversions move bits and nothing else. Go defines a signed shift as
// arithmetic and a signed-to-unsigned conversion as the two's-complement pattern, so
// no value can be lost, which is exactly what the linter cannot see from here. A
// coordinate is also bounded a hundred times over by the time it arrives: it is degrees
// times a thousand.
func zigzag(v int64) uint64 {
	return uint64(v<<1) ^ uint64(v>>63) //nolint:gosec // G115: bit pattern, not magnitude
}

// delta writes one point as the difference from the point before it.
func (e *encoder) delta(lon, lat int64) {
	e.varint(zigzag(lon - e.lon))
	e.varint(zigzag(lat - e.lat))
	e.lon, e.lat = lon, lat
}

// point appends a point to the chain that is running.
func (e *encoder) point(p [2]float64) {
	e.delta(quantise(p))
	e.points++
}

// line writes a point count followed by that many points, as its own chain: the first
// point is absolute, so that a line does not depend on the line before it.
func (e *encoder) line(points [][2]float64) {
	e.varint(uint64(len(points)))
	e.lon, e.lat = 0, 0
	for _, point := range points {
		e.point(point)
	}
}

// quantise rounds a degree pair to thousandths of a degree, which is a hundredth of a
// pixel at the deepest zoom the map offers.
func quantise(point [2]float64) (int64, int64) {
	return int64(math.Round(point[0] * 1000)), int64(math.Round(point[1] * 1000))
}

// parseLand reads the rings out of a GeoJSON FeatureCollection of Polygon and
// MultiPolygon geometries.
//
// Holes are dropped, and that is a decision rather than an oversight. A hole in a land
// polygon is a lake, the coarsest Natural Earth layer draws the Caspian and the Great
// Lakes and nothing smaller, and drawing them correctly means a fill-rule that gets the
// winding of two nested rings right — in a projection where the rings are not polygons
// any more anyway. A lake drawn as land at zoom 3 is a lake nobody notices; a lake
// drawn inside out is a continent-shaped hole.
func parseLand(source []byte) ([][][2]float64, error) {
	var doc struct {
		Features []struct {
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	var out [][][2]float64
	for _, feature := range doc.Features {
		switch feature.Geometry.Type {
		case "Polygon":
			var poly [][][2]float64
			if err := json.Unmarshal(feature.Geometry.Coordinates, &poly); err != nil {
				return nil, err
			}
			if len(poly) > 0 {
				out = append(out, poly[0])
			}
		case "MultiPolygon":
			var multi [][][][2]float64
			if err := json.Unmarshal(feature.Geometry.Coordinates, &multi); err != nil {
				return nil, err
			}
			for _, poly := range multi {
				if len(poly) > 0 {
					out = append(out, poly[0])
				}
			}
		}
	}
	return out, nil
}

// rawLine is one border line with the two properties the generator needs from the
// source: the zoom its author drew it for, and which country it belongs to.
type rawLine struct {
	points  [][2]float64
	minZoom float64
	country string
}

// borderClass is the one feature class kept out of the admin-1 line file.
//
// The file's other classes — "Admin-1 region boundary", "Admin-1 statistical boundary",
// "Admin-1 statistical meta bounds", and a fourth the dataset names with an American
// "z" — draw the same ground a second time in a second way. Measured on the 1:10m file: 36 167 lines are
// this class and 9 000 more are the others, and of the 194 countries in the file only
// two have no line of this class at all.
const borderClass = "Admin-1 boundary"

// parseLines reads the border lines, and reports both how many of them are the class
// kept and how many lines the file holds in all, so the header can say what the filter
// cost rather than only what it produced.
//
// The property names are upper case here where the label-point file's are lower case.
// That is the shape Natural Earth publishes them in, and it is written out rather than
// papered over with a case-insensitive lookup: a file whose properties stopped matching
// would otherwise decode into empty country codes and a zoom of zero, and the only
// thing that would say so is a map with no borders on it.
func parseLines(source []byte) (out []rawLine, kept, total int, err error) {
	var doc struct {
		Features []struct {
			Properties struct {
				FeatureClass string  `json:"FEATURECLA"`
				MinZoom      float64 `json:"MIN_ZOOM"`
				Country      string  `json:"ADM0_A3"`
			} `json:"properties"`
			Geometry *struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, 0, 0, err
	}
	if len(doc.Features) == 0 {
		return nil, 0, 0, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	for _, feature := range doc.Features {
		if feature.Geometry == nil {
			continue
		}
		var lines [][][2]float64
		switch feature.Geometry.Type {
		case "LineString":
			var line [][2]float64
			if err := json.Unmarshal(feature.Geometry.Coordinates, &line); err != nil {
				return nil, 0, 0, err
			}
			lines = append(lines, line)
		case "MultiLineString":
			var multi [][][2]float64
			if err := json.Unmarshal(feature.Geometry.Coordinates, &multi); err != nil {
				return nil, 0, 0, err
			}
			lines = multi
		}
		// Every line counts towards the file's own total, whichever class it is: the
		// header states what share of the file this keeps, and a total counted after
		// the filter could only ever say "all of them".
		for _, line := range lines {
			total++
			if feature.Properties.FeatureClass != borderClass {
				continue
			}
			kept++
			out = append(out, rawLine{
				points:  line,
				minZoom: feature.Properties.MinZoom,
				country: feature.Properties.Country,
			})
		}
	}
	if len(out) == 0 {
		return nil, 0, 0, fmt.Errorf("not one feature is a %q line; is this the "+
			"admin-1 states-and-provinces line file?", borderClass)
	}
	return out, kept, total, nil
}

// parseCountryLabels reads the country names and where they should be drawn.
//
// Upper case properties again: this is ne_110m_admin_0_countries.
func parseCountryLabels(source []byte, filter countryFilter) ([]label, error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Name      string  `json:"NAME"`
				LabelX    float64 `json:"LABEL_X"`
				LabelY    float64 `json:"LABEL_Y"`
				MinLabel  float64 `json:"MIN_LABEL"`
				LabelRank int     `json:"LABELRANK"`
				Country   string  `json:"ADM0_A3"`
				ISO2      string  `json:"ISO_A2"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	var out []label
	for _, feature := range doc.Features {
		p := feature.Properties
		if p.Name == "" || len(filter) > 0 && !filter.keep(p.Country) && !filter.keep(p.ISO2) {
			continue
		}
		// MIN_LABEL is the zoom Natural Earth's own map first writes the name at, in
		// halves and thirds of a level. The map here draws in whole levels, so the
		// name arrives at the first one that is not too early.
		one := label{
			lon: p.LabelX, lat: p.LabelY,
			rank: p.LabelRank, zoom: int(math.Ceil(p.MinLabel)), name: p.Name,
		}
		if err := checkLabel(one); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("not one feature has a NAME and a LABEL_X; is this the " +
			"admin-0 countries file?")
	}
	return out, nil
}

// placeholderName matches Natural Earth's stand-in for an area it has no name for:
// "JPN-99 (  minor island)", "KIR-00 (Kiribati aggregation)". 45 of the 4 632 areas
// are named that way, and a map that draws one of them has drawn a database key on
// somebody's dashboard.
//
// It is a pattern rather than a rank because the rank it mostly lives at also holds
// Kingman Reef and the Bounty Islands, which are places with names.
var placeholderName = regexp.MustCompile(`^[A-Z]{3}-\d\d \(`)

// parseRegionLabels reads the names of the first-order administrative areas.
//
// The property names are lower case in this file, the one exception among the four
// datasets — ne_10m_admin_1_label_points is a different export by a different hand.
//
// One area can have several label points, placed for whichever shape the area takes at
// whichever zoom. The best-ranked one is the one kept, which is what makes this 4 649
// names rather than 11 291: a name drawn four times is a name drawn four times.
func parseRegionLabels(source []byte, filter countryFilter) ([]label, error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Code     string `json:"adm1_code"`
				Name     string `json:"name"`
				Rank     int    `json:"datarank"`
				Country  string `json:"sr_adm0_a3"`
				Fallback string `json:"iso_a2"`
			} `json:"properties"`
			Geometry *struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	best := map[string]label{}
	for _, feature := range doc.Features {
		p := feature.Properties
		if feature.Geometry == nil || feature.Geometry.Type != "Point" || p.Name == "" {
			continue
		}
		if placeholderName.MatchString(p.Name) {
			continue
		}
		if p.Rank == 0 {
			p.Rank = 99
		}
		if !filter.keep(p.Country) && !filter.keep(p.Fallback) {
			continue
		}
		previous, seen := best[p.Code]
		if seen && previous.rank <= p.Rank {
			continue
		}
		var point [2]float64
		if err := json.Unmarshal(feature.Geometry.Coordinates, &point); err != nil {
			return nil, err
		}
		one := label{
			lon: point[0], lat: point[1],
			rank: p.Rank, zoom: regionZoom(p.Rank), name: p.Name,
		}
		if err := checkLabel(one); err != nil {
			return nil, err
		}
		best[p.Code] = one
	}
	if len(best) == 0 {
		return nil, fmt.Errorf("not one feature is a Point with a name; is this the " +
			"admin-1 label points file?")
	}
	out := make([]label, 0, len(best))
	for _, l := range best {
		out = append(out, l)
	}
	return out, nil
}

// labelZoom and labelRank are the ranges the packed stream holds a name's zoom and rank
// in. One byte each: a zoom is a level of the map, of which there are 25 at the most
// before the tiles stop existing, and a rank is an importance order nobody has more than
// a couple of hundred of.
const (
	labelZoom = 24
	labelRank = 255
)

// checkLabel refuses a zoom or a rank the packing cannot hold.
//
// Neither is a map that is slightly wrong: a zoom of -1 or a rank of 100 000 would be
// truncated into the stream and surface as a name drawn at the wrong time, or not at
// all, with nothing anywhere saying why. The datasets are consistent today and this is
// about the day one of them is not.
func checkLabel(l label) error {
	if l.zoom < 0 || l.zoom > labelZoom {
		return fmt.Errorf("label %q wants zoom %d; the map has 0 to %d", l.name, l.zoom, labelZoom)
	}
	if l.rank < 0 || l.rank > labelRank {
		return fmt.Errorf("label %q has rank %d; ranks run 0 to %d", l.name, l.rank, labelRank)
	}
	return nil
}

// byteValue narrows a zoom or a rank to the unsigned varint the stream keeps it in.
//
// Every caller has already run checkLabel over the same numbers, which is what the
// comment is for: the cast is safe there and nowhere else, so it is written once, next
// to the check that makes it true, instead of at each of the two places that pack one.
func byteValue(v int) uint64 { return uint64(v) } //nolint:gosec // range-checked by checkLabel

// regionZoom is the zoom a province name belongs on, taken from Natural Earth's own
// importance rank for the name: the most important names arrive when their border does
// (see the bands in borders.js), the least important only when somebody has zoomed in
// far enough to want them.
func regionZoom(rank int) int {
	switch {
	case rank <= 3:
		return 5
	case rank <= 7:
		return 7
	default:
		return 9
	}
}

// newCountryFilter turns -only into a lookup. An empty -only keeps everything, which
// is the difference between "all of them" and "none of them" and is worth being
// explicit about.
func newCountryFilter(only string) countryFilter {
	set := countryFilter{}
	for _, code := range strings.Split(only, ",") {
		if code = strings.ToUpper(strings.TrimSpace(code)); code != "" {
			set[code] = true
		}
	}
	return set
}

type countryFilter map[string]bool

func (f countryFilter) keep(code string) bool {
	if len(f) == 0 {
		return true
	}
	return f[strings.ToUpper(code)]
}

// simplify runs Douglas-Peucker over a ring or a line and returns it.
//
// A ring is treated as an open line from its first point back to itself: the end point
// is pinned so the shape stays closed, which is what the fill needs, and a line is
// pinned for the same reason — its ends are the ends of the border. The tolerance is a
// distance in degrees on a flat lon/lat plane and therefore over-states its own effect
// near the poles, where a degree of longitude is a fraction of a degree of latitude.
// That is the honest limitation of doing this in degrees, and at 1:50m the Arctic is
// coarse either way.
func simplify(line [][2]float64, tol float64) [][2]float64 {
	if len(line) < 3 {
		return line
	}
	keep := make([]bool, len(line))
	keep[0] = true
	keep[len(line)-1] = true
	dp(line, 0, len(line)-1, tol, keep)

	out := make([][2]float64, 0, len(line))
	for i, point := range line {
		if keep[i] {
			out = append(out, point)
		}
	}
	return out
}

// dp marks the points of line[lo:hi+1] that lie further than tol from the line between
// its ends, and recurses into the two halves it divides.
func dp(line [][2]float64, lo, hi int, tol float64, keep []bool) {
	if hi <= lo+1 {
		return
	}
	far, at := 0.0, -1
	for i := lo + 1; i < hi; i++ {
		if d := distance(line[i], line[lo], line[hi]); d > far {
			far, at = d, i
		}
	}
	if at < 0 || far <= tol {
		return
	}
	keep[at] = true
	dp(line, lo, at, tol, keep)
	dp(line, at, hi, tol, keep)
}

// distance is the perpendicular distance from p to the line through a and b, or the
// distance to a when the two are the same point.
func distance(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	return math.Abs(dy*(p[0]-a[0])-dx*(p[1]-a[1])) / math.Hypot(dx, dy)
}

// countPoints counts a set of rings.
func countPoints(rings [][][2]float64) int {
	n := 0
	for _, ring := range rings {
		n += len(ring)
	}
	return n
}

// source is the dataset a generated file came from, for its header. The URL is not a
// courtesy: it is the other half of the checksum, and without it the checksum
// identifies a file nobody can find.
type source struct {
	name     string // "Natural Earth 1:50m land"
	url      string
	noun     string // what the names in this file are, for the header's first line
	kind     string // the -kind that writes it
	upper    string // the export prefix: WORLD_LABEL_*, WORLD_REGION_*
	expected string // how many of them the dataset has, for the header's size line
	picked   string // a header block, or empty, for a dataset that needs one
}

var (
	landSource = source{
		name: "Natural Earth 1:50m land",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_land.geojson",
	}
	borderSource = source{
		name: "Natural Earth 1:10m admin-1 boundaries",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_admin_1_states_provinces_lines.geojson",
	}
	countrySource = source{
		name:     "Natural Earth 1:110m admin-0 countries",
		url:      "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_admin_0_countries.geojson",
		noun:     "of the world's countries",
		kind:     "labels",
		upper:    "LABEL",
		expected: "177 countries",
	}
	regionSource = source{
		name:     "Natural Earth 1:10m admin-1 label points",
		url:      "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_admin_1_label_points.geojson",
		noun:     "of the world's first-order administrative areas",
		kind:     "regions",
		upper:    "REGION",
		expected: "4 649 areas",
		picked: `// Picked:    one label point per area. The source holds 11 291 for its 4 649 areas,
//            placed differently for the shape an area has at different zooms; the
//            best-ranked of them is the one kept here, because a name drawn four times
//            is a name drawn four times.
`,
	}
)

// licence is the same paragraph in all four files, because it is the same dataset.
const licence = `// Licence:   public domain. Natural Earth asks for no attribution and this project
//            gives none, which is the whole reason it was chosen over a dataset that
//            would have put somebody else's name on the operator's own map.`

// provenance is the block every header starts with: where the data came from, and what
// the file it was made from was.
func provenance(src source, in, sum string) string {
	return fmt.Sprintf(`// Source:    %s, https://www.naturalearthdata.com/
//            %s
%s
// Input:     %s
// SHA-256:   %s
//            The checksum is what identifies the input; the name above is only what
//            it happened to be saved as. Re-download the URL, check the checksum, and
//            the file this produced can be reproduced byte for byte.
`, src.name, src.url, licence, in, sum)
}

// sharpness says how far the simplification can be seen, because the tolerance on its
// own does not tell anybody anything.
//
// One pixel at zoom z is 360/(256·2^z) degrees of longitude, so a tolerance of t
// degrees is t·256·2^z/360 pixels of error at that zoom. The map is honest where that
// number is small, and it is the tolerance — not the scale of the input — that decides
// where that is.
func sharpness(tol float64) string {
	if tol <= 0 {
		return "Nothing was simplified: every point of the input is in the file below."
	}
	fit := int(math.Floor(math.Log2(360 / (256 * tol))))
	return fmt.Sprintf("Within a pixel of the truth to about zoom %d;\n//            visibly angular by about zoom %d.",
		fit, fit+4)
}

// write puts the file where it was asked for and says what it did.
func write(out, body, summary string) error {
	// 0644 and the linter is told why rather than obeyed: the output is a source file
	// in the repository, committed and read by the compiler and by anybody who wants to
	// check it. There is nothing in it to keep from anybody — the input it came from is
	// public domain and the repository it is committed to is open.
	if err := os.WriteFile(out, []byte(body), 0o644); err != nil { //nolint:gosec // G306: a committed source file, see above
		return err
	}
	fmt.Printf("%s, %d byte file\n", summary, len(body))
	return nil
}

func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
