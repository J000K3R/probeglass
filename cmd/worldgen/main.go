// Command worldgen turns Natural Earth files into the map data the dashboard draws.
//
// The dashboard ships a slippy map with no mapping library and, since the tile
// servers stopped being usable by anybody but their own websites, no tiles either.
// What it draws instead is this: the world's coastlines, its country and
// first-order administrative borders, its lakes and its largest rivers, and the names
// of the places on it — all from files small enough to embed in the binary. The map is
// still a real map: places land where they belong, a border is where the border is, and
// the graticule underneath is honest. It simply has nothing on it that somebody else's
// server had to be asked for.
//
// It reads a file and writes a file, once per dataset — the kind of thing being
// generated decides the parser, the encoding and the header:
//
//	kind       reads                                    writes
//	land       ne_50m_land.geojson                       world.js
//	countries  ne_50m_admin_0_boundary_lines_land        countries.js
//	borders    ne_10m_admin_1_states_provinces_lines     borders.js
//	lakes      ne_50m_lakes                              lakes.js
//	rivers     ne_50m_rivers_lake_centerlines            rivers.js
//	labels     ne_110m_admin_0_countries                 labels.js
//	regions    ne_10m_admin_1_label_points               regions.js
//
// The four banded kinds — countries, borders, lakes, rivers — write the same stream
// shape: one group per zoom band, each self-delimiting. That is not a coincidence to
// be tidied away, it is why one reader in the browser can walk any of them, so the
// grouping lives in one place here (see groupBands).
//
// It is the only thing in this repository that reads something nobody committed. So
// the output carries everything needed to reproduce it: the source URL, the checksum
// of the input, the command line, and the licence. Fetching the input is not this
// program's business — it is seven curls in docs/CONFIGURATION.md, so that regenerating
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
//	countries,      one such stream per zoom band, concatenated. Each is
//	borders, lakes, self-delimiting — it begins with its own shape count — so a
//	rivers          reader walks them in order and never needs an offset into the
//	                file. A band holds rings for the two surface kinds and lines for
//	                the two linear ones, and the reader does not care which: the
//	                count, then the points, band after band
//	labels, regions a group count, then per group: the zoom, the rank, a label count,
//	                then the labels as longitude and latitude only. Grouping is what
//	                makes the rank and the zoom nearly free: consecutive labels of
//	                equal importance are one group rather than 4 numbers each
//
// In every stream the first point is absolute and every other is a delta from the one
// before it, so a reader accumulates from zero and has no special case to forget. A
// ring in land.js is its own chain — each ring starts absolute, so one dropped or
// reordered ring cannot shift the rest. In the four banded files each shape is. In
// labels.js and regions.js the chain runs once through the whole stream, groups and
// all, because a label is a point and there is nothing to reorder around it.
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

// The command line, one flag set for all seven kinds. The defaults that matter differ
// per kind — a coastline and a border are not simplified the same way, and a ring is
// not a line — so they are applied after parsing, from the kind, and only to the flags
// nobody set.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worldgen:", err)
		os.Exit(1)
	}
}

// The kinds, in the order the header table lists them. One list rather than three
// copies of it, because a kind that the switch accepts but the -out table does not know
// would write its output to the empty path, and a kind the help does not list cannot be
// discovered at all.
const (
	kindLand      = "land"
	kindCountries = "countries"
	kindBorders   = "borders"
	kindLakes     = "lakes"
	kindRivers    = "rivers"
	kindLabels    = "labels"
	kindRegions   = "regions"
)

// countryKinds are the two kinds whose source carries a country code and which
// therefore have a -only to honour. The other files have none: a border line between
// two countries is filed under neither, and a river runs through several. -only with
// one of those kinds is refused rather than ignored — see run().
func hasCountryCode(kind string) bool {
	return kind == kindBorders || kind == kindLabels || kind == kindRegions
}

func run() error {
	kind := flag.String("kind", kindLand, "what to generate: land, countries, borders, lakes, rivers, labels or regions")
	in := flag.String("in", "", "Natural Earth GeoJSON file to read (see docs/CONFIGURATION.md for where to get it)")
	out := flag.String("out", "", "file to write; the default is the asset belonging to -kind")
	tol := flag.Float64("tol", 0, "Douglas-Peucker tolerance in degrees; 0 means the default for -kind")
	minPoints := flag.Int("min-points", 0, "drop lines left with fewer points than this after simplifying; 0 means the default for -kind")
	only := flag.String("only", "", "keep only these countries (comma-separated ADM0_A3 codes); empty keeps all of them. Not every -kind has country codes in its source")
	maxRank := flag.Int("max-rank", 0, "labels, regions: drop names ranked less important than this; rivers: drop rivers ranked less important than this; unset means the default for -kind, and 0 means no limit")
	class := flag.String("class", "", "countries, borders: the one FEATURECLA to draw; unset means the default for -kind")
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
			kindLand: "world.js", kindCountries: "countries.js", kindBorders: "borders.js",
			kindLakes: "lakes.js", kindRivers: "rivers.js",
			kindLabels: "labels.js", kindRegions: "regions.js",
		}[*kind]
		if !ok {
			return fmt.Errorf("-kind %q is not one of %s", *kind, strings.Join(kinds(), ", "))
		}
		*out = filepath.Join("internal", "web", "assets", asset)
	}

	// -only names a country, and three of the seven sources have no country in them.
	// Refusing it is the point: an ignored filter would write a file with nothing in
	// it, which on the dashboard is a map with no rivers on it — a thing that looks
	// exactly like a dataset that has no rivers in it, and so is a thing nobody can
	// debug.
	if *only != "" && !hasCountryCode(*kind) {
		return fmt.Errorf("-kind %s reads a file with no country code in it: -only cannot be used with it", *kind)
	}

	filter := newCountryFilter(*only)

	switch *kind {
	case kindLand:
		return runLand(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 4), source)
	case kindCountries:
		code := *class
		if !given["class"] {
			code = countryClass
		}
		return runCountries(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 2), code, source)
	case kindBorders:
		code := *class
		if !given["class"] {
			code = borderClass
		}
		return runBorders(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 2), code, filter, source)
	case kindLakes:
		return runLakes(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 4), source)
	case kindRivers:
		return runRivers(name, sum, *in, *out, defaulted("tol", *tol, 0.02), defaultInt(given, "min-points", *minPoints, 2), defaultInt(given, "max-rank", *maxRank, maxRiverRank), source)
	case kindLabels:
		return runLabels(name, sum, *in, *out, filter, *maxRank, source)
	case kindRegions:
		return runRegions(name, sum, *in, *out, filter, *maxRank, source)
	}
	return fmt.Errorf("-kind %q is not one of %s", *kind, strings.Join(kinds(), ", "))
}

// kinds is the list the help and the error messages print, in the order of the table
// in the package comment.
func kinds() []string {
	return []string{kindLand, kindCountries, kindBorders, kindLakes, kindRivers, kindLabels, kindRegions}
}

func defaultInt(given map[string]bool, name string, value, fallback int) int {
	if given[name] {
		return value
	}
	return fallback
}

// runLand writes the coastline.
//
// Every ring of every polygon comes out, holes included. ne_50m_land has exactly one
// hole — the Caspian — and while this kept the outer rings alone the Caspian was drawn
// as land. Nothing in the browser had to change to fix it: outline.js already fills with
// fill-rule="evenodd", so a ring nested inside another cuts its own hole out of the fill.
func runLand(name, sum, in, out string, tol float64, minPoints int, source []byte) error {
	parsed, err := parsePolygons(source)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	rings := make([][][2]float64, 0, len(parsed))
	holePoints := 0
	for _, ring := range parsed {
		if ring.hole {
			holePoints += len(ring.points)
		}
		rings = append(rings, ring.points)
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
// Rings:     every ring of every polygon, holes included, drawn with
//            fill-rule="evenodd" so a ring inside another is a hole in it. The source
//            has one such hole, the Caspian, %d point(s) of it.
//
// The borders, the water and the names are next to this file, from the same generator
// and with the same kind of header: countries.js, borders.js, lakes.js, rivers.js,
// labels.js, regions.js. See docs/CONFIGURATION.md for where the input comes from.

export const WORLD_RINGS = %d;
export const WORLD_SCALE = 1000;
export const WORLD_DATA = '%s';
`, provenance(landSource, name, sum), tol, minPoints, enc.points, before,
		100*float64(enc.points)/float64(before), sharpness(tol), holePoints, len(kept), enc.data())

	return write(out, body, fmt.Sprintf("%s: %d ring(s), %d point(s) in, %d out, %.1f%% kept",
		in, len(kept), before, enc.points, 100*float64(enc.points)/float64(before)))
}

// banded is shapes filed by the zoom band their author drew them for. All four banded
// kinds produce one of these, which is why the grouping and the encoding are written
// once: a reader in the browser walks a band stream without having to know whether the
// shapes in it are rings or lines.
type banded struct {
	byBand map[int][][][2]float64
	bands  []int // in band order, which is the order the reader walks them
	points int   // points kept, after simplifying
	kept   int   // shapes kept
}

// groupBands simplifies every shape and files it under the band its own zoom names.
//
// The band is the whole design of these four files. Natural Earth gives every line, lake
// and river a min_zoom — the zoom its author drew it for — and drawing all of them at
// every zoom would put the detail of a city map on a view of Europe. So a band holds the
// shapes whose zoom rounds up into it, and the map shows band b when it is at zoom b or
// deeper. That rounding is worth naming: a lake at min_zoom 4.7 is in band 5, and ceil is
// the one rule that reads a 2.0, a 2.1 and a 5.6 the same way.
//
// The filter is a country filter and most kinds have no country in their source; an
// empty filter, which is what those kinds pass, keeps everything.
func groupBands(shapes []rawShape, tol float64, minPoints int, filter countryFilter) banded {
	out := banded{byBand: map[int][][][2]float64{}}
	for _, shape := range shapes {
		if !filter.keep(shape.country) {
			continue
		}
		simplified := simplify(shape.points, tol)
		if len(simplified) < minPoints {
			continue
		}
		band := int(math.Ceil(shape.minZoom))
		out.byBand[band] = append(out.byBand[band], simplified)
		out.points += len(simplified)
		out.kept++
	}
	for band := range out.byBand {
		out.bands = append(out.bands, band)
	}
	sort.Ints(out.bands)
	return out
}

// encode writes one self-delimiting stream per band, in band order, and returns the
// stream together with the table of what each band holds.
//
// The table is not decoration. It is the only place a reader of one of these files can
// see which bands exist and what each one costs, and that is the question to ask when a
// world view looks slow: the cost of a zoom is the size of the bands up to it.
func (b banded) encode(noun string) (data string, table []string) {
	enc := &encoder{}
	table = make([]string, 0, len(b.bands))
	for _, band := range b.bands {
		shapes := b.byBand[band]
		enc.varint(uint64(len(shapes)))
		bandPoints := 0
		for _, shape := range shapes {
			enc.line(shape)
			bandPoints += len(shape)
		}
		table = append(table, fmt.Sprintf("//              zoom %-3d %5d %s(s), %6d point(s)",
			band, len(shapes), noun, bandPoints))
	}
	return enc.data(), table
}

// zooms renders the band list as the body of the JS array a header exports.
func (b banded) zooms() string {
	list := make([]string, 0, len(b.bands))
	for _, band := range b.bands {
		list = append(list, fmt.Sprint(band))
	}
	return strings.Join(list, ", ")
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
func runBorders(name, sum, in, out string, tol float64, minPoints int, class string, filter countryFilter, source []byte) error {
	lines, classes, err := parseLines(source, class, "the admin-1 states-and-provinces line file")
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	classLines, fileLines := classes[class], classTotal(classes)

	b := groupBands(lines, tol, minPoints, filter)
	if b.kept == 0 {
		return fmt.Errorf("%s: no %q line survived -only and -min-points %d; is this the "+
			"admin-1 line file, and is -only a real country code?", in, class, minPoints)
	}
	data, table := b.encode("line")

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
// Filter:    FEATURECLA == "%s". The source holds %d lines and %d of them
//            are that class; the rest are "Admin-1 region boundary", "Admin-1
//            statistical boundary" and "Admin-1 statistical meta bounds", which are
//            further subdivisions of the same ground. Drawing those too would put a
//            second line on every border.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            A line is not a ring: a border that simplifies to two points is still a
//            border, which is why the floor is 2 here and 4 for a ring. Drop
//            straight borders and Germany keeps 7%% of its border length, Switzerland
//            2%% — the shapes that vanish are the ones that are actually straight.
// Bands:     Natural Earth gives every line a MIN_ZOOM, the zoom its author drew it
//            for, and the lines are grouped by it here. WORLD_BORDER_BANDS lists those
//            groups in the order they appear in the data, and a map at zoom z draws
//            every band whose number is z or less — the lines up to and including the
//            zoom they were drawn for, and none of the finer ones.
%s
//
// These are the borders inside countries. The borders between them are countries.js,
// which is the file whose absence left Austria missing from the map. At a world view
// this layer draws about 13 KB of path; the whole file is worth having for the deep
// zooms. The setting that turns this layer off is Settings → appearance.

export const WORLD_BORDER_BANDS = [%s];
export const WORLD_BORDER_DATA = '%s';
`, provenance(borderSource, name, sum), class, fileLines, classLines, tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) in the source class, %d of %d in all, %d point(s), band(s) %v",
		in, b.kept, classLines, b.kept, fileLines, b.points, b.bands))
}

// runCountries writes the international borders: the outlines of the countries
// themselves, as opposed to the borders between the parts inside one.
//
// This is the file whose absence the dashboard's map was showing. The province layer
// comes from ne_10m_admin_1_states_provinces_lines, which holds the boundaries between
// first-order areas and never a national outline, so the map drew Austria's Bundesländer
// and no Austria. Measured on that source: 8 038 of its lines are the class the borders
// kind keeps, 443 of them fall inside Austria's bounding box — and not one of those 443
// is Austria's own border.
func runCountries(name, sum, in, out string, tol float64, minPoints int, class string, source []byte) error {
	lines, classes, err := parseLines(source, class, "the 1:50m admin-0 boundary line file")
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	classLines, fileLines := classes[class], classTotal(classes)

	// No filter: this file has no country code to filter on. It carries a NAME and a
	// BRK_A3, and a break code names the pair of countries a line separates rather than
	// one of them, so -only is refused for this kind in run() rather than honoured here
	// in a way that would mean something else than it does everywhere else.
	b := groupBands(lines, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: no %q line survived -min-points %d; is this the 1:50m "+
			"admin-0 boundary line file?", in, class, minPoints)
	}
	data, table := b.encode("line")

	body := fmt.Sprintf(`// The world's international borders — the outlines of the countries themselves — for
// the map to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind countries -in <the file below> -out internal/web/assets/countries.js
//
%s
// Filter:    FEATURECLA == "%s".
//            The source holds %d lines; %d of them are that class.
//            The other %d are the ones Natural Earth will not call settled, and they
//            are named here rather than drawn:
%s
//            A disputed or indefinite frontier drawn as a border is a map taking a
//            side, and this map has no side to take.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            The same tolerance as the coastline, because this is the other half of
//            the same drawing: a coast and the border meeting it have to agree, and
//            they agree by being simplified the same way.
// Bands:     MIN_ZOOM, as in borders.js, and read the same way: WORLD_COUNTRY_BANDS
//            lists the bands in data order and a map at zoom z draws every band whose
//            number is z or less.
%s
//
// borders.js is the layer inside these lines, lakes.js and rivers.js the water under
// them, and labels.js the names on them. One thing this file is not: it is not a
// polygon layer. A country here is the lines around it, which is why the map draws it as
// a border and not as a fill. See docs/CONFIGURATION.md for where the input comes from.

export const WORLD_COUNTRY_BANDS = [%s];
export const WORLD_COUNTRY_DATA = '%s';
`, provenance(countriesSource, name, sum), class, fileLines, classLines, fileLines-classLines,
		fold("//            ", classCounts(classes, class)), tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) in the source class, %d of %d in all, %d point(s), band(s) %v",
		in, b.kept, classLines, b.kept, fileLines, b.points, b.bands))
}

// runLakes writes the lakes, filled, banded by the zoom each was drawn for.
func runLakes(name, sum, in, out string, tol float64, minPoints int, source []byte) error {
	shapes, areas, err := parseLakeShapes(source)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one lake survived -min-points %d; is this the 1:50m lakes file?",
			in, minPoints)
	}
	data, table := b.encode("ring")

	body := fmt.Sprintf(`// The world's lakes, as filled rings, for the map to draw when no tile server is
// configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind lakes -in <the file below> -out internal/web/assets/lakes.js
//
%s
// Filter:    none. Every ring in the source is here: %d ring(s) in %d lake(s), which at
//            1:50m means Lake Victoria, the Great Lakes, the Bodensee. A ring rather
//            than a lake, because a lake with an island in it is two rings. The lakes
//            that are missing are missing from the dataset, not from this file.
//
//            The Caspian is not in this file, and that is not an omission: the 1:50m
//            lakes file does not carry it, the coastline file has a hole there instead,
//            and a hole in a fill-rule="evenodd" fill is the sea showing through. So
//            the Caspian is drawn by world.js and its shore is stroked by the same
//            path that strokes every coast.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            A ring, so the floor is 4 like the coastline and not 2 like a border: a
//            lake that simplifies to three points is a triangle, and a triangle is
//            worse than no lake.
// Bands:     min_zoom. This file and the rivers one spell it in lower case where the two
//            border files spell MIN_ZOOM in upper case, and both are read here as the
//            same number. WORLD_LAKE_BANDS lists the bands in data order and a map at
//            zoom z draws every band whose number is z or less, so a world view is the
//            three coarsest bands — Lake Victoria, the Great Lakes, the Aral — and
//            nothing a country map would want in it.
%s
//
// The lakes are part of the basemap rather than a layer of their own: they are drawn
// under the borders, always, and the switch in Settings → appearance turns off the
// borders and the names, not the water. The rivers are rivers.js, next to this file.

export const WORLD_LAKE_BANDS = [%s];
export const WORLD_LAKE_DATA = '%s';
`, provenance(lakeSource, name, sum), len(shapes), areas, tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d ring(s), %d point(s), band(s) %v",
		in, b.kept, len(shapes), b.points, b.bands))
}

// maxRiverRank is how important a river has to be for this map to draw it.
//
// The source ranks rivers 1 to 6, where 1 is the Amazon and 6 is a river a country map
// shows and a world map has no room for. Rank 3 is the cut this project chose: it holds
// the Danube without holding every tributary that reaches it.
const maxRiverRank = 3

// runRivers writes the largest rivers, as lines.
func runRivers(name, sum, in, out string, tol float64, minPoints, maxRank int, source []byte) error {
	shapes, kept, total, err := parseRiverShapes(source, maxRank)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one river survived -max-rank %d and -min-points %d; is "+
			"this the 1:50m rivers file?", in, maxRank, minPoints)
	}
	data, table := b.encode("line")

	body := fmt.Sprintf(`// The world's largest rivers, as lines, for the map to draw when no tile server is
// configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind rivers -in <the file below> -out internal/web/assets/rivers.js
//
%s
// Filter:    scalerank <= %d, of 6. The source holds %d line(s) and %d of them are
//            rank %d or better. Rank 1 is the Amazon, the Nile, the Yangtze; rank 2 is
//            the Danube; rank 3 is the Volga, the Ganges, the Niger; rank 6 is the Po
//            or the Loire, which a country map draws and a world map does not. -max-rank
//            moves the cut, and 0 means no cut at all.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            A line, so the floor is 2, as for a border.
// Bands:     min_zoom, lower case as in the lakes file, and read the same way as in the
//            two border files: WORLD_RIVER_BANDS lists the bands in data order and a map
//            at zoom z draws every band whose number is z or less. The Danube is in band
//            3, so a world view draws band 2 alone — the largest systems on the planet,
//            one per continent, and nothing that belongs on a country map.
%s
//
// Some of these lines run through a lake — Natural Earth publishes a river interrupted
// by a lake as a river plus a centreline — and drawing them keeps a river that crosses
// the Bodensee from appearing to stop at its shore. They are in the water layer, under
// the borders and the names.

export const WORLD_RIVER_BANDS = [%s];
export const WORLD_RIVER_DATA = '%s';
`, provenance(riverSource, name, sum), maxRank, total, kept, maxRank, tol, minPoints,
		sharpness(tol), strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) at rank <= %d, %d point(s), band(s) %v",
		in, b.kept, total, maxRank, b.points, b.bands))
}

// classTotal is how many lines a file holds in all, of every class, which is the number
// a header needs to say what its filter cost rather than only what it kept.
func classTotal(classes map[string]int) int {
	total := 0
	for _, n := range classes {
		total += n
	}
	return total
}

// classCounts names the classes a file holds besides the one kept, largest first. It is
// for the header, and it is a list of names rather than a count on purpose: "the other
// 35 lines" says nothing about whether they are a second copy of the same border or the
// world's disputed frontiers, and a reader deciding whether to trust this file needs to
// know which.
func classCounts(classes map[string]int, keep string) []string {
	type entry struct {
		name string
		n    int
	}
	rest := make([]entry, 0, len(classes))
	for name, n := range classes {
		if name != keep {
			rest = append(rest, entry{name, n})
		}
	}
	sort.Slice(rest, func(i, j int) bool {
		if rest[i].n != rest[j].n {
			return rest[i].n > rest[j].n
		}
		return rest[i].name < rest[j].name
	})
	parts := make([]string, 0, len(rest))
	for _, e := range rest {
		parts = append(parts, fmt.Sprintf("%q %d", e.name, e.n))
	}
	return parts
}

// fold wraps a list onto header lines. The header of a generated file is read on a
// terminal, and a single line naming forty source classes is a line nobody reads.
func fold(indent string, parts []string) string {
	const width = 78
	var lines []string
	line := ""
	for _, part := range parts {
		switch {
		case line == "":
			line = part
		case len(indent)+len(line)+len(", ")+len(part) > width:
			lines = append(lines, indent+line)
			line = part
		default:
			line += ", " + part
		}
	}
	if line != "" {
		lines = append(lines, indent+line)
	}
	return strings.Join(lines, "\n")
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

// polygonRing is one ring of a polygon: the outline, or one of its holes. GeoJSON
// orders them that way — the first ring is the outline, the rest of the array is the
// holes in it — so the distinction is the array index and nothing has to be worked out
// from the geometry.
type polygonRing struct {
	points [][2]float64
	hole   bool
}

// parsePolygons reads every ring out of a GeoJSON FeatureCollection of Polygon and
// MultiPolygon geometries.
//
// Every ring, holes included. A hole in this file is a lake, ne_50m_land has exactly one
// of them — the Caspian — and until this kept the outer rings alone the Caspian was
// drawn as land. outline.js fills with fill-rule="evenodd" and always did, so a ring
// nested inside another cuts a hole out of the fill by itself and no browser code had to
// change. The hole is reported rather than dropped because the header counts it: it is
// the sort of thing a reader of a generated file should be told about.
func parsePolygons(source []byte) ([]polygonRing, error) {
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

	var out []polygonRing
	for _, feature := range doc.Features {
		polys, err := polygonsOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, err
		}
		for _, poly := range polys {
			for i, ring := range poly {
				out = append(out, polygonRing{points: ring, hole: i > 0})
			}
		}
	}
	return out, nil
}

// polygonsOf unrolls one feature's geometry into its polygons, each a list of rings. A
// Polygon is one polygon, a MultiPolygon is several, which for this dataset is a
// country with islands in it. nil for any other geometry.
//
// The two polygon files — the land and the lakes — share this and nothing else, because
// they differ in the one thing a shared parser would have to be told anyway: the tag
// the file spells its zoom with, upper case in one and lower case in the other.
func polygonsOf(geoType string, coordinates json.RawMessage) ([][][][2]float64, error) {
	switch geoType {
	case "Polygon":
		var poly [][][2]float64
		if err := json.Unmarshal(coordinates, &poly); err != nil {
			return nil, err
		}
		return [][][][2]float64{poly}, nil
	case "MultiPolygon":
		var multi [][][][2]float64
		if err := json.Unmarshal(coordinates, &multi); err != nil {
			return nil, err
		}
		return multi, nil
	}
	return nil, nil
}

// rawShape is one ring or one line, with the two properties the banded kinds need from
// the source: the zoom its author drew it for, and which country it belongs to. One type
// for the four of them because they are read the same way — a lake ring and a river line
// differ in nothing the grouping can see.
type rawShape struct {
	points  [][2]float64
	minZoom float64
	country string
}

// parseLakeShapes reads the lakes: polygons, every ring of them, with the zoom each was
// drawn for.
//
// The property names are lower case in this file — "min_zoom", where the two border
// files write "MIN_ZOOM" — which is why this is not the same parser as the one for the
// borders. Writing it out rather than looking the tag up without regard to case is the
// same decision as everywhere else in this file: a lookup that matched anything would
// decode a file whose properties changed into a zoom of zero, and the only symptom would
// be a map that draws every lake at every zoom.
func parseLakeShapes(source []byte) (out []rawShape, areas int, err error) {
	var doc struct {
		Features []struct {
			Properties struct {
				MinZoom float64 `json:"min_zoom"`
			} `json:"properties"`
			Geometry struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, 0, err
	}
	if len(doc.Features) == 0 {
		return nil, 0, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	for _, feature := range doc.Features {
		polys, err := polygonsOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, 0, err
		}
		for _, poly := range polys {
			areas++
			for _, ring := range poly {
				out = append(out, rawShape{points: ring, minZoom: feature.Properties.MinZoom})
			}
		}
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("not one lake has a ring; is this the 1:50m lakes file?")
	}
	return out, areas, nil
}

// borderClass is the feature class kept out of the admin-1 line file.
//
// The file's other classes — "Admin-1 region boundary", "Admin-1 statistical boundary",
// "Admin-1 statistical meta bounds", and a fourth the dataset names with an American
// "z" — draw the same ground a second time in a second way. Measured on the 1:10m file: 36 167 lines are
// this class and 9 000 more are the others, and of the 194 countries in the file only
// two have no line of this class at all.
//
// It is a default for -class rather than a constant the parser knows, because the
// admin-0 file next door keeps a different class of its own and the two parsers are the
// same code.
const borderClass = "Admin-1 boundary"

// countryClass is the one feature class kept out of the admin-0 line file.
//
// The rest of that file is Natural Earth being careful: "Disputed (please verify)",
// "Line of control (please verify)", "Indefinite (please verify)" and "Indeterminant
// frontier". Drawing those as borders is a map taking a side in somebody's argument, so
// they are counted in the header and not drawn.
const countryClass = "International boundary (verify)"

// parseLines reads the border lines, and reports how many of them are the class kept,
// how many lines the file holds in all, and the count per class, so a header can say
// what its filter cost and what it left out rather than only what it produced.
//
// One parser for the two border files. They hold the same geometry and the same property
// names — upper case, and the country code is genuinely absent from the admin-0 file
// rather than spelled differently, which is why -only is refused for that kind instead
// of quietly keeping nothing.
func parseLines(source []byte, class, wants string) (out []rawShape, classes map[string]int, err error) {
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
		return nil, nil, err
	}
	if len(doc.Features) == 0 {
		return nil, nil, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	classes = map[string]int{}
	for _, feature := range doc.Features {
		if feature.Geometry == nil {
			continue
		}
		lines, err := linesOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, nil, err
		}
		// Every line counts towards the file's own total, whichever class it is: the
		// header states what share of the file a filter keeps, and a total counted
		// after the filter could only ever say "all of them".
		for _, line := range lines {
			classes[feature.Properties.FeatureClass]++
			if feature.Properties.FeatureClass != class {
				continue
			}
			out = append(out, rawShape{
				points:  line,
				minZoom: feature.Properties.MinZoom,
				country: feature.Properties.Country,
			})
		}
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("not one feature is a %q line; is this %s?", class, wants)
	}
	return out, classes, nil
}

// parseRiverShapes reads the river centerlines, keeping the ones at or above the rank
// asked for, and reports how many lines the file holds in all.
//
// The properties are lower case here as in the lakes file, and there is no country code:
// a river runs through several countries, so a filter by country would be a filter that
// means something different here than it does for a border. -only is refused for this
// kind rather than honoured with that other meaning.
func parseRiverShapes(source []byte, maxRank int) (out []rawShape, kept, total int, err error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Scalerank int     `json:"scalerank"`
				MinZoom   float64 `json:"min_zoom"`
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
		lines, err := linesOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, 0, 0, err
		}
		for _, line := range lines {
			total++
			if maxRank > 0 && feature.Properties.Scalerank > maxRank {
				continue
			}
			kept++
			out = append(out, rawShape{points: line, minZoom: feature.Properties.MinZoom})
		}
	}
	if len(out) == 0 {
		return nil, 0, 0, fmt.Errorf("not one river is rank %d or better; is this the 1:50m "+
			"rivers-and-lake-centrelines file?", maxRank)
	}
	return out, kept, total, nil
}

// linesOf is the same walk as polygonsOf for the two linear kinds: a LineString is one
// line, and a MultiLineString is several, which is what a river delta or a border a lake
// interrupts is published as.
func linesOf(geoType string, coordinates json.RawMessage) ([][][2]float64, error) {
	switch geoType {
	case "LineString":
		var line [][2]float64
		if err := json.Unmarshal(coordinates, &line); err != nil {
			return nil, err
		}
		return [][][2]float64{line}, nil
	case "MultiLineString":
		var multi [][][2]float64
		if err := json.Unmarshal(coordinates, &multi); err != nil {
			return nil, err
		}
		return multi, nil
	}
	return nil, nil
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
	countriesSource = source{
		name: "Natural Earth 1:50m admin-0 boundary lines",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_admin_0_boundary_lines_land.geojson",
	}
	borderSource = source{
		name: "Natural Earth 1:10m admin-1 boundaries",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_10m_admin_1_states_provinces_lines.geojson",
	}
	lakeSource = source{
		name: "Natural Earth 1:50m lakes",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_lakes.geojson",
	}
	riverSource = source{
		name: "Natural Earth 1:50m rivers and lake centrelines",
		url:  "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_50m_rivers_lake_centerlines.geojson",
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
