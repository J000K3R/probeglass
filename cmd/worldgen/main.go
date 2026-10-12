// Command worldgen turns Natural Earth files into the map data the dashboard draws.
//
// The dashboard ships a slippy map with no mapping library and, since the tile
// servers stopped being usable by anybody but their own websites, no tiles either.
// What it draws instead is this: the world's coastlines, its country and
// first-order administrative borders, Austria's political districts, its lakes and
// its rivers, and the names of the countries, the provinces, the cities and the towns
// on it — all from files small enough to embed in the binary. The map is
// still a real map: places land where they belong, a border is where the border is, and
// the graticule underneath is honest. It simply has nothing on it that somebody else's
// server had to be asked for.
//
// It reads a file and writes a file, once per dataset — the kind of thing being
// generated decides the parser, the encoding and the header:
//
//	kind       reads                                    writes
//	land       ne_10m_land.geojson                       world.js
//	countries  ne_10m_admin_0_boundary_lines_land        countries.js
//	borders    ne_10m_admin_1_states_provinces_lines     borders.js
//	lakes      ne_10m_lakes                              lakes.js
//	lakes      ne_10m_lakes_europe                       lakes_europe.js
//	rivers     ne_10m_rivers_lake_centerlines            rivers.js
//	rivers     ne_10m_rivers_europe                      rivers_europe.js
//	labels     ne_110m_admin_0_countries                 labels.js
//	regions    ne_10m_admin_1_label_points               regions.js
//	cities     ne_10m_populated_places_simple            cities.js
//	towns      GeoNames cities5000                        towns.js
//	districts  Statistik Austria political districts      districts.js
//
// Two of the twelve rows are the same kind run twice on two files, which is the one
// thing worth knowing about the shape of this table: Natural Earth publishes Europe a
// second time, with more water in it, and the second copy is where the middle-sized
// rivers and lakes of the Alps are — the Salzach, the Enns, the Zürichsee. Where the two
// copies overlap, -minus keeps the shape once (see skip).
//
// The six banded kinds — countries, borders, lakes, rivers and the two Europe
// supplements — write the same stream shape: one group per zoom band, each
// self-delimiting. That is not a coincidence to be tidied away, it is why one reader in
// the browser can walk any of them, so the grouping lives in one place here (see
// groupBands). districts.js is the seventh, and it is banded too: one band.
//
// It is the only thing in this repository that reads something nobody committed. So
// the output carries everything needed to reproduce it: the source URL, the checksum
// of the input, the command line, and the licence. Fetching the input is not this
// program's business — it is the dozen commands in docs/CONFIGURATION.md, so that
// regenerating a file is a thing a person can actually do rather than a thing this
// program would have to be trusted about.
//
// Ten of the twelve files are Natural Earth's and in the public domain. The other two
// ask to be named: GeoNames' towns and Statistik Austria's districts, both CC BY 4.0.
// Those two are the only things on this map that carry an attribution, which is why
// their licences are in their headers and not in a footnote somewhere.
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
//	rivers,         reader walks them in order and never needs an offset into the
//	districts       file. A band holds rings for the surface kinds and lines for
//	                the linear ones, and the reader does not care which: the
//	                count, then the points, band after band
//	labels, regions, a group count, then per group: the zoom, the rank, a label count,
//	cities          then the labels as longitude and latitude only. Grouping is what
//	                makes the rank and the zoom nearly free: consecutive labels of
//	                equal importance are one group rather than 4 numbers each
//
// In every stream the first point is absolute and every other is a delta from the one
// before it, so a reader accumulates from zero and has no special case to forget. A
// ring in land.js is its own chain — each ring starts absolute, so one dropped or
// reordered ring cannot shift the rest. In the banded files each shape is. In
// labels.js, regions.js and cities.js the chain runs once through the whole stream,
// groups and all, because a label is a point and there is nothing to reorder around it.
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
	"strconv"
	"strings"
)

// The command line, one flag set for all ten kinds. The defaults that matter differ
// per kind — a coastline and a border are not simplified the same way, a ring is not a
// line, and a district is drawn at a zoom where a country border's tolerance would show
// — so they are applied after parsing, from the kind, and only to the flags nobody set.
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
	kindCities    = "cities"
	kindTowns     = "towns"
	kindDistricts = "districts"
)

// hasCountryCode reports whether a kind's source carries a country code and therefore
// has a -only to honour.
//
// The other files have none: a border line between two countries is filed under neither,
// a river runs through several, and Austria's district file is one country by
// construction. -only with one of those kinds is refused rather than ignored — see run().
func hasCountryCode(kind string) bool {
	return kind == kindBorders || kind == kindLabels || kind == kindRegions ||
		kind == kindCities || kind == kindTowns
}

func run() error {
	kind := flag.String("kind", kindLand, "what to generate: land, countries, borders, lakes, rivers, labels, regions, cities, towns or districts")
	in := flag.String("in", "", "Natural Earth GeoJSON file to read (see docs/CONFIGURATION.md for where to get it)")
	out := flag.String("out", "", "file to write; the default is the asset belonging to -kind")
	tol := flag.Float64("tol", 0, "Douglas-Peucker tolerance in degrees; 0 means the default for -kind")
	minPoints := flag.Int("min-points", 0, "drop lines left with fewer points than this after simplifying; 0 means the default for -kind")
	only := flag.String("only", "", "keep only these countries (comma-separated codes — ADM0_A3 like AUT for the Natural Earth files, ISO 3166-1 alpha-2 like AT for the GeoNames towns file); empty keeps all of them. Not every -kind has country codes in its source")
	maxRank := flag.Int("max-rank", 0, "labels, regions, cities: drop names ranked less important than this; rivers: drop rivers ranked less important than this; unset means the default for -kind, and 0 means no limit")
	class := flag.String("class", "", "countries, borders: the one FEATURECLA to draw; unset means the default for -kind")
	minus := flag.String("minus", "", "lakes, rivers: a GeoJSON file whose named shapes are already drawn; shapes sharing a name with one of them are dropped here. Empty drops nothing")
	minPopulation := flag.Int("min-population", 0, "towns: drop places below this many inhabitants; 0 means the default for -kind")
	flag.Parse()

	if *in == "" {
		return fmt.Errorf("-in is required: this program does not fetch anything, and says so on purpose")
	}

	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })
	// defaulted takes the flag from the kind when nobody asked for one. A tolerance is
	// not a property of a file format, it is a property of the thing being drawn:
	// 0.005 flattens a coastline at the same visible cost as 0.002 flattens a district.
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
			kindCities: "cities.js", kindTowns: "towns.js", kindDistricts: "districts.js",
		}[*kind]
		if !ok {
			return fmt.Errorf("-kind %q is not one of %s", *kind, strings.Join(kinds(), ", "))
		}
		// The two Europe supplements are the same kinds run on a second file, and their
		// default asset is the second name. It is read off the file name for the same
		// reason the -out defaults exist at all: an invocation without one must not
		// quietly overwrite the world file with the finer copy of one continent.
		if europe := europeAsset(*kind, name); europe != "" {
			asset = europe
		}
		*out = filepath.Join("internal", "web", "assets", asset)
	}

	// -only names a country, and seven of the twelve sources have no country in them.
	// Refusing it is the point: an ignored filter would write a file with nothing in
	// it, which on the dashboard is a map with no rivers on it — a thing that looks
	// exactly like a dataset that has no rivers in it, and so is a thing nobody can
	// debug.
	if *only != "" && !hasCountryCode(*kind) {
		return fmt.Errorf("-kind %s reads a file with no country code in it: -only cannot be used with it", *kind)
	}

	// -max-rank is the same kind of refusal for the other half of the towns question. The
	// three name files cut themselves by a rank the source publishes; GeoNames publishes
	// no rank at all, so a -max-rank here would be accepted and would do nothing, and a
	// file generated by a flag that did nothing is a file whose header lies about how it
	// was made. -min-population is what cuts this one and it is named here so the person
	// who typed the wrong flag is told the right one.
	if given["max-rank"] && *kind == kindTowns {
		return fmt.Errorf("-kind towns is cut by -min-population, not by -max-rank: " +
			"GeoNames publishes a population and no rank, and a rank cut on a file that has " +
			"no ranks in it would drop nothing at all")
	}

	// And the country codes of the towns file are a different kind of code, which is not
	// a detail a person can be expected to guess. Every other name file here is Natural
	// Earth's and is filtered by the ADM0_A3 code — AUT, DEU, CHE — while GeoNames files
	// a place under the ISO 3166-1 alpha-2 code, AT, DE, CH. Both are called "the country
	// code" and neither is wrong, so -only AUT would match nothing at all and would be
	// reported as a code that is not real — which is true of this file and false of AUT,
	// and a message that is right for the wrong reason is the worst kind.
	if *kind == kindTowns && *only != "" {
		for _, code := range strings.Split(*only, ",") {
			if len(strings.TrimSpace(code)) != 2 {
				return fmt.Errorf("-only %s with -kind towns: GeoNames files a place under the "+
					"two-letter ISO 3166-1 code, not the three-letter one the Natural Earth "+
					"files use — -only AT, not -only AUT", *only)
			}
		}
	}

	// -minus drops the shapes another file already draws. It exists for one measured
	// reason: Natural Earth's two Europe supplements overlap the two world files on 31
	// river names and 6 lake names, and both are on screen at zoom 7, so without this
	// those 31 rivers are drawn twice, an inch apart, in two different generalisations.
	skip := nameSet{}
	if *minus != "" {
		if *kind != kindLakes && *kind != kindRivers {
			return fmt.Errorf("-kind %s has no names to match on: -minus is for lakes and rivers", *kind)
		}
		data, err := os.ReadFile(*minus)
		if err != nil {
			return err
		}
		skip, err = parseNames(data)
		if err != nil {
			return fmt.Errorf("%s: %w", *minus, err)
		}
	}

	filter := newCountryFilter(*only)

	switch *kind {
	case kindLand:
		return runLand(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 4), source)
	case kindCountries:
		code := *class
		if !given["class"] {
			code = countryClass
		}
		return runCountries(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 2), code, source)
	case kindBorders:
		code := *class
		if !given["class"] {
			code = borderClass
		}
		return runBorders(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 2), code, filter, source)
	case kindLakes:
		// The Europe supplement is the same kind on the second file: it writes the same
		// stream with its own header, and its own band table, which is deeper than the
		// world file's because it is the finer copy of one continent. The lookup is on
		// `name` and not on `*in`, which is the same thing only when the file was not
		// named by its path — and `-in /tmp/ne_10m_lakes_europe.geojson` is how anybody
		// runs this.
		if europeKinds[name] == kindLakes {
			return runEuropeLakes(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 4), skip, source)
		}
		return runLakes(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 4), skip, source)
	case kindRivers:
		// The Europe supplement ranks on its own scale, where the world file's cut would
		// keep nothing at all, so it carries its own default rather than a flag every
		// invocation would have to remember.
		rank := defaultInt(given, "max-rank", *maxRank, maxRiverRank)
		if europeKinds[name] == kindRivers {
			rank = defaultInt(given, "max-rank", *maxRank, europeRiverRank)
			return runEuropeRivers(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 2), rank, skip, source)
		}
		return runRivers(name, sum, *in, *out, defaulted("tol", *tol, worldTolerance), defaultInt(given, "min-points", *minPoints, 2), rank, skip, source)
	case kindLabels:
		return runLabels(name, sum, *in, *out, filter, *maxRank, source)
	case kindRegions:
		return runRegions(name, sum, *in, *out, filter, *maxRank, source)
	case kindCities:
		return runCities(name, sum, *in, *out, filter, *maxRank, source)
	case kindTowns:
		return runTowns(name, sum, *in, *out, filter, defaultInt(given, "min-population", *minPopulation, minTownPopulation), source)
	case kindDistricts:
		return runDistricts(name, sum, *in, *out, defaulted("tol", *tol, districtTolerance), defaultInt(given, "min-points", *minPoints, 4), source)
	}
	return fmt.Errorf("-kind %q is not one of %s", *kind, strings.Join(kinds(), ", "))
}

// europeKinds says which kind the two Europe supplements belong to, by the file they
// came from. It is keyed on the file rather than on a flag so that the two invocations
// look exactly like the invocations of the world files — the only difference between
// them is which file is read and which file is written.
var europeKinds = map[string]string{
	"ne_10m_lakes_europe.geojson":  kindLakes,
	"ne_10m_rivers_europe.geojson": kindRivers,
}

// europeAsset is the asset name for a Europe supplement, or empty for any other input.
// `file` is a base name, as -in already has one.
func europeAsset(kind, file string) string {
	if europeKinds[file] != kind {
		return ""
	}
	if kind == kindLakes {
		return "lakes_europe.js"
	}
	return "rivers_europe.js"
}

// kinds is the list the help and the error messages print, in the order of the table
// in the package comment.
func kinds() []string {
	return []string{kindLand, kindCountries, kindBorders, kindLakes, kindRivers,
		kindLabels, kindRegions, kindCities, kindTowns, kindDistricts}
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
//            has one such hole, the Caspian, %d point(s) of it — still the only one at
//            1:10m, which was worth checking again when the source changed.
//
// The borders, the water and the names are next to this file, from the same generator
// and with the same kind of header: countries.js, borders.js, lakes.js, lakes_europe.js,
// rivers.js, rivers_europe.js, districts.js, labels.js, regions.js, cities.js and
// towns.js. See
// docs/CONFIGURATION.md for where the input comes from.

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
func runLakes(name, sum, in, out string, tol float64, minPoints int, skip nameSet, source []byte) error {
	shapes, areas, dropped, err := parseLakeShapes(source, skip)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one lake survived -min-points %d; is this the 1:10m lakes file?",
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
// Filter:    every ring in the source: %d ring(s) in %d lake(s). A ring rather than a
//            lake, because a lake with an island in it is two rings. The lakes that are
//            missing are missing from the dataset, not from this file.
%s
//            The Caspian is not in this file, and that is not an omission: the 1:10m
//            lakes file does not carry it either, the coastline file has a hole there
//            instead, and a hole in a fill-rule="evenodd" fill is the sea showing
//            through. So the Caspian is drawn by world.js and its shore is stroked by
//            the same path that strokes every coast.
//            What the finer source bought, in one number: the Bodensee has 17 points in
//            the 1:50m source and 65 in the 1:10m one, and the tolerance below leaves 6
//            of those points and 14. So half of the coarseness was the source and half
//            was the simplification, and the second half is the one screw this layer has
//            left: -tol 0.01 leaves 19 points and costs 79 KB more, 0.005 leaves 34 and
//            costs 169. That is a size decision for whoever runs the dashboard, not a
//            correctness question for the generator, which is why the numbers are here
//            and the flag is -tol.
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
// borders and the names, not the water. The rivers are rivers.js, next to this file, and
// the finer copy of Europe is lakes_europe.js.

export const WORLD_LAKE_BANDS = [%s];
export const WORLD_LAKE_DATA = '%s';
`, provenance(lakeSource, name, sum), len(shapes), areas, droppedBlock(dropped, "lake"),
		tol, minPoints, sharpness(tol), strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d ring(s), %d point(s), band(s) %v",
		in, b.kept, len(shapes), b.points, b.bands))
}

// droppedBlock is the header line for what -minus took out, or nothing at all when it
// took nothing out. A file that dropped nothing says nothing: a "Dropped: 0" line is a
// line every reader has to read and none of them learns anything from.
func droppedBlock(dropped int, noun string) string {
	if dropped == 0 {
		return ""
	}
	return fmt.Sprintf("// Dropped:   %d %s(s) whose name is already drawn by another file, which is what\n"+
		"//            -minus is for: Natural Earth's Europe supplements overlap the world files\n"+
		"//            on the same water at the same zooms, and two lines an inch apart is one\n"+
		"//            line too many.\n", dropped, noun)
}

// maxRiverRank is how important a river has to be for this map to draw it.
//
// The 1:10m source ranks rivers 0 to 10, where 0 and 1 are the Amazon and the Nile and
// 10 belongs on a country map and not on a world one. Rank 4 is the cut this project
// chose: 472 lines, and it is the rank the Rhine carries. The cut was 3 while the source
// was 1:50m, and it moved when the source did, for a reason worth writing down — at 1:50m
// the Rhine is a line somewhere in the general direction of the Alps, and at rank 4 in
// the 1:10m file it is the river Vorarlberg's western border runs along.
const maxRiverRank = 4

// europeRiverRank is the same cut for the Europe supplement at 1:10m, and it is a
// different number for a reason that is not a preference: the two files rank on
// different scales. The world file runs 0 to 10 over the whole planet, where the Rhine is
// a 4; the Europe file runs 10 to 12 over one continent, where the Rhine is not in the
// file at all and the Salzach is an 11.
//
// So the world file's 4 keeps nothing here — it is below every rank in the file — and
// this is the cut that was chosen instead: 11, which is the smallest number that keeps
// the Salzach and the Enns, the two rivers of the western half of Austria that the world
// file has at no rank. 12 is streams through valleys: 316 more shapes over all of Europe
// for 18 KB, and a map at zoom 7 that is mostly small blue lines.
//
// Measured, both cuts: 11 is 1 247 of the file's 1 701 lines, 12 is all 1 619 that survive
// their own tolerance.
const europeRiverRank = 11

// runRivers writes the largest rivers, as lines.
func runRivers(name, sum, in, out string, tol float64, minPoints, maxRank int, skip nameSet, source []byte) error {
	shapes, kept, total, dropped, err := parseRiverShapes(source, maxRank, skip)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one river survived -max-rank %d and -min-points %d; is "+
			"this the 1:10m rivers file?", in, maxRank, minPoints)
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
// Filter:    scalerank <= %d, of 10. The source holds %d line(s) and %d of them are
//            rank %d or better. Rank 1 is the Amazon, the Nile, the Yangtze; rank 2 is
//            the Danube; rank 4 is the Rhine and the Elbe, which is the cut this map
//            draws at; rank 10 is a river only a country map has room for. -max-rank
//            moves the cut, and 0 means no cut at all.
%s
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
//
// This file holds the world's large rivers and no small ones, which is why the Salzach
// and the Enns are not in it: they are in rivers_europe.js, the second copy Natural Earth
// publishes for Europe, where the rank scale is the continent's own. Vorarlberg's rivers
// are in neither — measured, not assumed: this file has exactly one river in Vorarlberg,
// and it is the Rhine.

export const WORLD_RIVER_BANDS = [%s];
export const WORLD_RIVER_DATA = '%s';
`, provenance(riverSource, name, sum), maxRank, total, kept, maxRank,
		droppedBlock(dropped, "line"), tol, minPoints,
		sharpness(tol), strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) at rank <= %d, %d point(s), band(s) %v",
		in, b.kept, total, maxRank, b.points, b.bands))
}

// runEuropeLakes writes the finer copy of Europe's lakes.
//
// Natural Earth publishes Europe a second time, and the second copy is not the first at
// a smaller tolerance: it is more lakes, which is the whole point of it. The Zürichsee and
// the Walensee are in this file and in no world file. It overlaps the world file on five
// names — Lago di Como, Lago di Bracciano, Pyhäjärvi, Pääjärvi, Storsjön — and those five
// are dropped by -minus rather than drawn twice, an inch apart.
func runEuropeLakes(name, sum, in, out string, tol float64, minPoints int, skip nameSet, source []byte) error {
	shapes, areas, dropped, err := parseLakeShapes(source, skip)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one lake survived -min-points %d; is this the Europe "+
			"lakes supplement?", in, minPoints)
	}
	data, table := b.encode("ring")

	body := fmt.Sprintf(`// Europe's smaller lakes — the ones too fine for the world file — as filled rings, for
// the map to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind lakes -in <the file below> -minus <the lakes.js input>
//
%s
// Filter:    every ring in the source: %d lake(s), %d ring(s) in all. A ring rather than
//            a lake, because a lake with an island in it is two rings.
%s
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            The same tolerance as the world file, because the two are drawn on top of
//            each other in the same colour and a lake whose shore disagreed with its
//            neighbour's would show the seam.
// Bands:     min_zoom, read exactly as in lakes.js: this file's bands are 6 and 7, which
//            says what kind of lake these are — a lake to look at from a country or a
//            valley, and a point at a world view. WORLD_LAKE_EUROPE_BANDS is the table.
%s
//
// This is the second copy of Europe. lakes.js is the first, and it is the file that
// explains the stream, the tolerance and the bands; this one is the same stream written
// for a second source and does not explain them again. The rivers are rivers_europe.js.

export const WORLD_LAKE_EUROPE_BANDS = [%s];
export const WORLD_LAKE_EUROPE_DATA = '%s';
`, provenance(lakeEuropeSource, name, sum), areas, len(shapes),
		droppedBlock(dropped, "lake"), tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d ring(s), %d point(s), band(s) %v",
		in, b.kept, len(shapes), b.points, b.bands))
}

// runEuropeRivers writes the finer copy of Europe's rivers — the file the Salzach and the
// Enns are in, and the Ill of Vorarlberg is not.
//
// The supplement exists because the world file stops at the Rhine for all of Austria west
// of Salzburg: at rank 4 the Rhine is drawn, rank 5 to 10 are the second-order European
// systems, and everything below them is in this second copy alone. Measured for Austria:
// the world file has no Salzach, no Enns and no Traun at any rank, and this file has all
// three at 10 or 11.
//
// And measured the other way round, because it was the reason this layer was wanted and it
// turned out not to be true: the Ill is in this file, and it is not Vorarlberg's. Natural
// Earth's Ill is the one in Alsace, at 7.2 east; Vorarlberg's own rivers — the Ill, the
// Bregenzer Ach, the Dornbirner Ach, the Frutz — are in no Natural Earth file at any
// scale. It is checked by geometry and not by name, because the name is the same name and
// that is exactly the trap.
func runEuropeRivers(name, sum, in, out string, tol float64, minPoints, maxRank int, skip nameSet, source []byte) error {
	shapes, kept, total, dropped, err := parseRiverShapes(source, maxRank, skip)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one river survived -max-rank %d and -min-points %d; is "+
			"this the Europe rivers supplement?", in, maxRank, minPoints)
	}
	data, table := b.encode("line")

	body := fmt.Sprintf(`// Europe's smaller rivers — the ones too fine for the world file — as lines, for the map
// to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -kind rivers -in <the file below> -minus <the rivers.js input>
//
%s
// Filter:    scalerank <= %d, of 12. The source holds %d line(s) and %d of them are rank
//            %d or better. This file's ranks are not the world file's: rivers.js runs 0
//            to 10 for the whole planet and this one runs 10 to 12 for one continent, so
//            rank 10 here is the Traun, rank 11 is the Salzach, and rank 12 is a stream
//            through a valley.
%s
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            A line, so the floor is 2, as for a border.
// Bands:     min_zoom, read exactly as in rivers.js: this file's bands are 6 and 7, so a
//            world view draws none of it and a valley draws all of it.
//            WORLD_RIVER_EUROPE_BANDS is the table.
%s
//
// This is the second copy of Europe. rivers.js is the first, and it is the file that
// explains the stream and the bands; this one is the same stream written for a second
// source. The lakes are lakes_europe.js.
//
// What this file adds, where it was wanted, is Austria: the Salzach, the Enns and the
// Traun are here and in the world file at no rank. What it does not add is Vorarlberg's
// own rivers. The world file has exactly one river in Vorarlberg — the Rhine, rank 4,
// the river the western border runs along — and this one has none: the "Ill" in it is
// the Ill in Alsace, a different river that happens to share the name. Vorarlberg's Ill,
// the Bregenzer Ach, the Dornbirner Ach and the Frutz are in no Natural Earth file at any
// scale, so no tolerance, no band and no rank would put them on this map.

export const WORLD_RIVER_EUROPE_BANDS = [%s];
export const WORLD_RIVER_EUROPE_DATA = '%s';
`, provenance(riverEuropeSource, name, sum), maxRank, total, kept, maxRank,
		droppedBlock(dropped, "line"), tol, minPoints, sharpness(tol),
		strings.Join(table, "\n"), b.zooms(), data)

	return write(out, body, fmt.Sprintf("%s: %d of %d line(s) at rank <= %d, %d point(s), band(s) %v",
		in, b.kept, total, maxRank, b.points, b.bands))
}

// The districts are drawn deeper than anything else on this map, and that is what decides
// their tolerance. 0.02 degrees is 2 km: on a country border drawn from zoom 2 that is
// four pixels nobody notices, and on a district 20 km across drawn from zoom 8 it is a
// border that meets the next one in the wrong place. 0.002 is about 220 m, which is a
// pixel at zoom 8 and four at zoom 10, and it costs 72 KB instead of 13.
//
// Every other layer keeps the tolerance it had. This is not an inconsistency, it is the
// same rule applied to a layer that is drawn 500 times closer: the tolerance is a property
// of the thing being drawn, not of the program.
//
// The world tolerance was 0.02 until somebody watched a border come apart at zoom 12.
// What that measurement showed is worth writing down, because the obvious reading of the
// bands is wrong: a band is the zoom a shape is *first* drawn at, and every band up to the
// current zoom is drawn, so a country border filed in band 0 is still on screen at zoom
// 12 and is still simplified at whatever tolerance band 0 was written with. Simplifying
// only the deep bands finely would therefore have fixed almost nothing — the lines that
// look angular when somebody zooms in are the shallow ones. The tolerance is a property
// of the layer as a whole, and 0.005 is about 550 m: under a pixel to about zoom 8, one
// or two at zoom 9 and into the tens by zoom 12. It costs 1.2 MB — the geometry assets
// are 2.9 MB instead of 1.7.
const (
	worldTolerance    = 0.005
	districtTolerance = 0.002
	districtBand      = 8
)

// runDistricts writes Austria's political districts, filled and stroked as one band, with
// their names.
//
// This is the only layer on this map that has no zoom in its source. The file carries a
// district number and a name and nothing else, so the band is chosen here rather than
// read — 8, because that is the first zoom at which Vorarlberg's four districts and
// Vienna's one are separately visible — and the header says so in as many words. It is
// the second thing about this file that is a decision instead of a reading; the licence
// is the first.
func runDistricts(name, sum, in, out string, tol float64, minPoints int, source []byte) error {
	shapes, labels, areas, rings, err := parseDistricts(source)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	if len(labels) == 0 {
		return fmt.Errorf("%s: %d area(s) and not one of them carries a g_name", in, areas)
	}
	b := groupBands(shapes, tol, minPoints, countryFilter{})
	if b.kept == 0 {
		return fmt.Errorf("%s: not one district survived -min-points %d; is this the "+
			"Statistik Austria political districts file?", in, minPoints)
	}
	data, table := b.encode("ring")

	nameData, names, groups := encodeLabels(labels)
	encodedNames, err := json.Marshal(names)
	if err != nil {
		return err
	}

	body := fmt.Sprintf(`// Austria's political districts — the 94 Bezirke and the 23 statutory cities — as filled
// rings stroked as borders, and their names, for the map to draw when no tile server is
// configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	curl -o bezirke.geojson '<the URL below>'
//	go run ./cmd/worldgen -kind districts -in bezirke.geojson
//
%s
// Filter:    none, and the counts are the geometry rather than the register: the source's
//            117 districts arrive as %d area(s) and %d ring(s). Five of them are two
//            polygons each, and six polygons have a hole in them — the statutory city
//            that sits inside the district around it, cut out of it: Graz out of
//            Graz-Umgebung, Innsbruck out of Innsbruck-Land, Wels out of Wels-Land. All
//            of it is drawn, and the fill rule leaves those holes unpainted, which is
//            right: the city inside them is a district of its own.
// Tolerance: %g degrees (Douglas-Peucker), -min-points %d
//            %s
//            Finer than every other layer here, and deliberately: this one is drawn from
//            zoom %d, where the tolerance the borders use would be a border meeting the
//            next one in the wrong place. See the constant in the generator.
// Bands:     one band, %d, and it is the only band in the file. The source has no zoom
//            field — it is an administrative register, not a map — so this is the one
//            place in this program where a band is chosen rather than read, and the
//            choice is Vorarlberg: at zoom 8 its four districts and Vienna's one are
//            separately visible.
// Names:     one per district, %d name(s) in %d group(s), from g_name. The anchor is the
//            area of the district's largest polygon, and the centre of it — largest
//            because five districts are two polygons each, and a name on the smaller
//            piece would be a name in the wrong valley.
//
//            A centre and not a seat. The source carries a name, a number and an outline
//            and no town at all, so the middle of the area is what it can support and an
//            administrative capital would be a claim it cannot make. That is also why the
//            map draws these names under the city names instead of over them.
//
//            The centre is the polygon's centroid with the holes taken back out of it,
//            which is the textbook formula for a shape with a hole in it and not a
//            detail: six of the 117 are a district with a statutory city cut out of the
//            middle — Graz out of Graz-Umgebung, Innsbruck out of Innsbruck-Land, Wels
//            out of Wels-Land — and a centroid that ignored the hole would be the centre
//            of a district that is not a disk. It is a shift and not a rescue: the plain
//            centre of the outline lands outside the hole in all six anyway, so a city's
//            name and the name of the district around it stay close together, and the
//            map's collision pass is what decides which of the two gets drawn.
//
//            The stream is the one regions.js uses — a group count, then per group its
//            zoom, its rank and a name count, then the coordinates as one delta chain —
//            and WORLD_DISTRICT_POINTS is the second half of this file that is not
//            geometry. All 117 districts share one zoom and one rank, so there is one
//            group and the rank is 0 everywhere.
%s
//
// Austria and nowhere else. Every other country's district band is empty, because there
// is no public-domain dataset of them — Natural Earth's only second-order layer is the
// 3 224 counties of the United States — and this map would rather have a gap than a
// border drawn from a licence nobody read. The licence above is what filling that gap
// cost.

export const WORLD_DISTRICT_BANDS = [%s];
export const WORLD_DISTRICT_DATA = '%s';
export const WORLD_DISTRICT_POINTS = '%s';
export const WORLD_DISTRICT_NAMES = %s;
`, provenance(districtSource, name, sum), areas, rings, tol, minPoints,
		sharpness(tol), districtBand, districtBand, len(names), groups,
		strings.Join(table, "\n"), b.zooms(), data, nameData, encodedNames)

	return write(out, body, fmt.Sprintf("%s: %d of %d ring(s), %d point(s), %d name(s), band(s) %v",
		in, b.kept, rings, b.points, len(names), b.bands))
}

// parseDistricts reads the Austrian districts: every ring of every polygon, all of them
// in the one band, and one name per district.
//
// It is the only parser here that returns two kinds of thing at once, and it does so
// because the source is the only one that carries both in one place: a district is a
// shape and a name, and there is no second file to take the names from. The geometry is
// read the way the land file reads it; the zoom, which the land file carries per ring, has
// to be supplied, because this one is an administrative register rather than a map and has
// no zoom field at all.
//
// The property names are g_id and g_name, lower case and with a prefix, because this file
// comes out of a WFS rather than out of Natural Earth. The number is thrown away —
// nothing on this map draws a district's number — and the name is kept.
func parseDistricts(source []byte) (shapes []rawShape, labels []label, areas, rings int, err error) {
	features, err := parseFeatures(source)
	if err != nil {
		return nil, nil, 0, 0, err
	}

	for _, feature := range features {
		var props struct {
			Name string `json:"g_name"`
		}
		if err := json.Unmarshal(feature.properties, &props); err != nil {
			return nil, nil, 0, 0, err
		}
		polys, err := polygonsOf(feature.geoType, feature.coordinates)
		if err != nil {
			return nil, nil, 0, 0, err
		}

		name := strings.TrimSpace(props.Name)
		lon, lat := districtAnchor(polys)
		if name != "" {
			labels = append(labels, label{lon: lon, lat: lat, zoom: districtBand, name: name})
		}
		for _, poly := range polys {
			areas++
			for _, ring := range poly {
				rings++
				shapes = append(shapes, rawShape{points: ring, minZoom: districtBand})
			}
		}
	}

	// A guard rather than a filter: Austria has had between 94 and 121 political districts
	// since 1868, so a file with a handful of areas in it is a bad query — a bounding box
	// that answered for one state, or a WFS that returned the first page — and a map with
	// three districts on it looks exactly like a map of a country with three districts.
	if areas < 100 {
		return nil, nil, 0, 0, fmt.Errorf("%d area(s): this does not look like the whole of "+
			"Austria. Is the typeName in the URL the one the layer is published under? It "+
			"carries a date, and the date changes", areas)
	}
	return shapes, labels, areas, rings, nil
}

// districtAnchor is where a district's name goes: the centre of its largest polygon.
//
// A centre and not a seat. The source carries a name and a numbered shape and no town at
// all, so there is nothing to put a district's name on but the district — which is why
// these names are drawn under the city names rather than over them, and why the header of
// the file says centre rather than capital. A district whose name sits where its
// administrative seat is would be a claim this data cannot support; one that sits in the
// middle of the district is a description of what is actually known.
//
// Largest polygon, because a district with an island or an exclave in it is still mostly
// the mainland, and a name on the smaller piece would be a name in the wrong valley.
func districtAnchor(polys [][][][2]float64) (lon, lat float64) {
	bestArea := -1.0
	for _, poly := range polys {
		if len(poly) == 0 || len(poly[0]) == 0 {
			continue
		}
		cx, cy, area := polygonCentre(poly)
		if area > bestArea {
			bestArea, lon, lat = area, cx, cy
		}
	}
	return lon, lat
}

// polygonCentre is the centroid of one polygon and its area, with the holes taken back out
// of both.
//
// The subtraction is not pedantry: six of Austria's districts have the statutory city that
// sits inside them cut out as a hole — Graz out of Graz-Umgebung, Innsbruck out of
// Innsbruck-Land, Wels out of Wels-Land — and that city is a district of its own with a
// name of its own. A centroid of the outline alone would put "Graz-Umgebung" in the middle
// of Graz, which is a different district, and the map would then show one of the two names
// and drop the other as a collision.
func polygonCentre(poly [][][2]float64) (lon, lat, area float64) {
	for i, ring := range poly {
		rlon, rlat, rarea := ringCentroid(ring)
		if i == 0 {
			lon, lat, area = rlon*rarea, rlat*rarea, rarea
			continue
		}
		lon -= rlon * rarea
		lat -= rlat * rarea
		area -= rarea
	}
	if area <= 0 {
		// A hole as big as the ring around it leaves no area to take a centroid of. The
		// outline's own centre is the only answer left.
		lon, lat, _ = ringCentroid(poly[0])
		return lon, lat, 0
	}
	return lon / area, lat / area, area
}

// ringCentroid is the shoelace centroid of one closed ring, and its area.
//
// A ring with no area at all — three points in a line, or a file that closed nothing —
// divides by zero in the shoelace sum, and the middle of its bounding box is the answer
// that costs nothing and lands in the right country. The last point of a GeoJSON ring is
// the first one again, which contributes nothing to the sum and needs no special case.
func ringCentroid(ring [][2]float64) (lon, lat, area float64) {
	minLon, maxLon := ring[0][0], ring[0][0]
	minLat, maxLat := ring[0][1], ring[0][1]
	for _, p := range ring {
		minLon, maxLon = math.Min(minLon, p[0]), math.Max(maxLon, p[0])
		minLat, maxLat = math.Min(minLat, p[1]), math.Max(maxLat, p[1])
	}
	centre := func() (float64, float64) {
		return (minLon + maxLon) / 2, (minLat + maxLat) / 2
	}

	var twice, sumLon, sumLat float64
	for i, p := range ring {
		q := ring[(i+1)%len(ring)]
		cross := p[0]*q[1] - q[0]*p[1]
		twice += cross
		sumLon += (p[0] + q[0]) * cross
		sumLat += (p[1] + q[1]) * cross
	}
	if twice == 0 {
		clon, clat := centre()
		return clon, clat, 0
	}
	return sumLon / (3 * twice), sumLat / (3 * twice), math.Abs(twice) / 2
}

// runCities writes the names of the world's cities and towns.
//
// It is the third label kind and it shares everything with the other two — the stream,
// the grouping, the names array — because a city name and a province name are the same
// thing to a map: a word at a coordinate that belongs there from some zoom on. What it
// does not share is the prose, and the difference is in the file: Natural Earth's
// populated places are the one dataset here whose names are not all in the language of
// the place.
func runCities(name, sum, in, out string, filter countryFilter, maxRank int, source []byte) error {
	labels, err := parseCityLabels(source, filter)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}
	return writeLabels(name, sum, in, out, labels, maxRank, citySource)
}

// runTowns writes the names of the world's towns: the places between the cities in
// cities.js and nothing at all.
//
// It exists because of a measurement rather than an opinion. Natural Earth's populated
// places is a curated list of about thirty places per country — 24 in Austria, 92 in
// Germany — and thirty places in Austria means Vienna, Graz, Linz, Salzburg, Innsbruck,
// Klagenfurt, Bregenz and a handful of others. Wels, Steyr, Krems, Leoben, Amstetten and
// every other district seat of the country are not in it and never were, so a map
// zoomed into a Bezirk had a border around it and no town in it.
//
// GeoNames is the dataset that has them, and it is the second of the two sources on this
// map that is not public domain — see the licence in the header, which is the point of
// writing it into the file rather than into a page about the file.
func runTowns(name, sum, in, out string, filter countryFilter, minPopulation int, source []byte) error {
	labels, counts, err := parseTownLabels(source, filter, minPopulation)
	if err != nil {
		return fmt.Errorf("%s: %w", in, err)
	}

	data, names, groups := encodeLabels(labels)
	encodedNames, err := json.Marshal(names)
	if err != nil {
		return err
	}

	body := fmt.Sprintf(`// The names of the world's towns, for the map
// to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator and run it
// again:
//
//	curl -LO https://download.geonames.org/export/dump/cities5000.zip
//	unzip cities5000.zip
//	go run ./cmd/worldgen -kind towns -in cities5000.txt
//
%s
// Filter:    the %d places whose population field is at least %d, of the %d in the
//            file — and no section of a place: %d of the rows are a PPLX, a district
//            or a quarter of a city rather than a city, and twenty-two of those are the
//            districts of Vienna, each carrying the population of its own quarter. A map
//            that drew them would have "Favoriten" and "Ottakring" on it at the size of
//            a city.
//
//            There is no rank cut here, because this file has no ranks in it: GeoNames
//            publishes a population and no opinion about what a map should draw first,
//            and -max-rank is refused rather than ignored. See townZoom for the one
//            place this program turns one of those numbers into the other.
// Sizes:     %d name(s) of the %s in the dataset.
//            The cut is the knob that makes this file smaller — -min-population 50000
//            is a tenth of its size and a map of only the larger towns — and -only
//            keeps one country and is the one that makes it small enough to read.
//
//            -only takes the two-letter ISO 3166-1 code here and not the three-letter
//            one the Natural Earth files take: GeoNames files a place under "AT", and
//            "-only AUT" matches nothing in this file. That is refused rather than
//            answered with an empty file, because an empty towns file on the dashboard
//            is a map with no towns on it and no way to tell why.
//
//            This file is a supplement to cities.js and is drawn with it: the two are
//            one layer of names on the map, and where both have a place, the label that
//            lands on the other is dropped rather than drawn over it. That is the whole
//            of the overlap rule, and it needs no more than that because the two sources
//            agree on the spelling of the places they share — see the Names block below.
//            Nothing here is filtered against cities.js either, which is deliberate: the
//            collision is resolved where the boxes are known, which is the browser, and
//            not by a rule about names up here that would silently lose a town called
//            Neustadt because some other country has one.
%s//
// The stream is the one labels.js, regions.js and cities.js use — a group count, then
// per group its zoom, its rank and a name count, then the coordinates as one delta chain
// — and the file is read by the same read() in maplabels.js that walks the other four.
//
// What this file is not: it is not more of the world, it is more of the middle of it.
// Natural Earth's thirty places per country are the ones whose names everybody knows,
// and GeoNames' ten thousand is where the map stops being a list of capitals.

export const WORLD_TOWN_POINTS = '%s';
export const WORLD_TOWN_NAMES = %s;
`, provenance(townSource, name, sum), counts.above, minPopulation, counts.total, counts.sections,
		len(names), townSource.expected, namesParagraph(townSource), data, encodedNames)

	return write(out, body, fmt.Sprintf("%s: %d of %d place(s) at %d inhabitant(s), %d name(s) in %d group(s)",
		in, counts.kept, counts.total, minPopulation, len(names), groups))
}

// The GeoNames columns this reads, counted from zero. They are read by index because
// there is no header line to read them by — the file is a table and nothing else, and
// the field that says which column a value is in is the documentation of GeoNames rather
// than of the file.
const (
	townColumnName       = 1
	townColumnLatitude   = 4
	townColumnLongitude  = 5
	townColumnFeature    = 7
	townColumnCountry    = 8
	townColumnPopulation = 14
	townColumns          = 19
)

// minTownPopulation is the smallest place towns.js names: ten thousand inhabitants, in
// the source's own field.
//
// It is a cut and not a fact about the world, and the number was chosen by reading the
// file rather than by taste. At fifteen thousand — what GeoNames' own cities15000 file is
// cut at — Austria has 29 places in it, because that file's population for an Austrian
// town is not the census: Krems an der Donau is in it at 14 473 and Leoben at 8 264, and
// neither is small. At ten thousand the same file yields 51 for Austria, which is every
// district seat and a few besides, and the whole world still fits in under a megabyte.
//
// The cut is written into the header of every file it produces, so the number and the
// file cannot drift apart.
const minTownPopulation = 10000

// notTowns are the GeoNames feature codes that are not a place somebody lives in.
//
// PPLX is the one that matters and the reason this list exists at all: it is a section
// of a populated place, and GeoNames files every one of Vienna's 23 districts, every
// quarter of Innsbruck and Graz and Linz under it, each with the population of the
// quarter. Drawn, they are a dozen names across one city and no city.
//
// The other four are places that are gone or were never lived in: PPLQ abandoned, PPLW
// destroyed, PPLH a historical place, PPLCH a historical capital. A map that names them
// is a map that tells somebody a village is somewhere they can walk to.
var notTowns = map[string]bool{
	"PPLX": true, "PPLQ": true, "PPLW": true, "PPLH": true, "PPLCH": true,
}

// townZoom is the zoom a town's name belongs on, and it is computed here rather than read
// from the source.
//
// The cities file takes its zoom from Natural Earth, which publishes a min_zoom per
// place: somebody looked at a world map and wrote down when each name should appear.
// GeoNames publishes a population and no such opinion, so one of the two numbers this
// program needs has to be derived from the other, and this is the only place it happens.
//
// The ladder is a staircase of population and nothing else, which is honest: it says
// that ten times as many people puts a town one step nearer to the world view, and it
// does not pretend to know that a town of 60 000 in a valley matters more than one of
// 80 000 on a plain. The steps start at zoom 4, not at 1, because that is where
// cities.js already has the same places — Vienna is in that file at zoom 4 and in this
// one at 5, so the large towns arrive after the cities and never replace one.
func townZoom(population int) int {
	switch {
	case population >= 5_000_000:
		return 4
	case population >= 1_000_000:
		return 5
	case population >= 500_000:
		return 6
	case population >= 200_000:
		return 7
	case population >= 100_000:
		return 8
	case population >= 50_000:
		return 9
	case population >= 20_000:
		return 10
	default:
		return 11
	}
}

// townCounts is what the parser saw, for the header to report.
//
// Four numbers, and the header needs all four: total is what the file holds, sections is
// how many of the rows were a section of a place rather than one — the number a reader
// wondering where their town went needs, because Vienna is in this file twenty-four times
// and the map draws it once — above is the population cut on its own, and kept is what
// survived both the cut and -only, which is what the map actually gets.
type townCounts struct {
	kept     int
	total    int
	sections int
	above    int
}

// parseTownLabels reads GeoNames' towns file, which is a table rather than a map: one
// place per line, nineteen tab-separated columns, no header, and no geometry anywhere in
// it — a town in this file is a pair of numbers in column 5 and column 6.
//
// The counts it returns are the header's business rather than the parser's: how many
// places were kept, how many the file holds, and how many were a section of a place. A
// parser that returned only the labels would leave the file unable to say what it left
// out, and what it left out is most of this file.
func parseTownLabels(source []byte, filter countryFilter, minPopulation int) (out []label, counts townCounts, err error) {
	for _, line := range strings.Split(string(source), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != townColumns {
			return nil, townCounts{}, fmt.Errorf("row %s has %d column(s); every row of this file "+
				"has %d, and one that does not is a file that is not the GeoNames dump or "+
				"a line of it that was edited by hand", fields[0], len(fields), townColumns)
		}
		counts.total++

		population, err := strconv.Atoi(fields[townColumnPopulation])
		if err != nil {
			return nil, townCounts{}, fmt.Errorf("row %s has %q where the population goes: %w",
				fields[0], fields[townColumnPopulation], err)
		}
		if notTowns[fields[townColumnFeature]] {
			counts.sections++
			continue
		}
		if population < minPopulation {
			continue
		}
		counts.above++
		if !filter.keep(fields[townColumnCountry]) {
			continue
		}

		lon, err := strconv.ParseFloat(fields[townColumnLongitude], 64)
		if err != nil {
			return nil, townCounts{}, fmt.Errorf("row %s has %q where the longitude goes: %w",
				fields[0], fields[townColumnLongitude], err)
		}
		lat, err := strconv.ParseFloat(fields[townColumnLatitude], 64)
		if err != nil {
			return nil, townCounts{}, fmt.Errorf("row %s has %q where the latitude goes: %w",
				fields[0], fields[townColumnLatitude], err)
		}

		one := label{
			lon: lon, lat: lat, zoom: townZoom(population),
			name: strings.TrimSpace(fields[townColumnName]),
		}
		if err := checkLabel(one); err != nil {
			return nil, townCounts{}, err
		}
		out = append(out, one)
		counts.kept++
	}
	if len(out) == 0 {
		return nil, townCounts{}, fmt.Errorf("not one place survived -min-population %d and -only; "+
			"is this the GeoNames towns file, and is the -only code the two-letter ISO 3166-1 "+
			"one — \"AT\" for Austria and not \"AUT\"?", minPopulation)
	}
	return out, counts, nil
}

// placeClasses are the FEATURECLA values that are a place somebody lives in. The three
// left out — "Scientific station", "Historic place", "Meteorological Station", 47
// features between them — are coordinates with a name on them: a map that labels an
// Antarctic research hut the way it labels Bregenz has told the reader that somebody
// lives there.
var placeClasses = map[string]bool{
	"Populated place":        true,
	"Populated Place":        true, // one feature in the source spells it differently
	"Admin-0 capital":        true,
	"Admin-0 capital alt":    true,
	"Admin-0 region capital": true,
	"Admin-1 capital":        true,
	"Admin-1 region capital": true,
}

// parseCityLabels reads the city names and where they go.
//
// The zoom comes from the file's own min_zoom, in halves and thirds of a level, and is
// rounded up the way every other zoom in this program is. The rank comes from scalerank,
// which is what -max-rank cuts on. And the country comes from adm0_a3, which is why -only
// works here and not for a border: a city is in exactly one country.
func parseCityLabels(source []byte, filter countryFilter) ([]label, error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Name      string  `json:"name"`
				Country   string  `json:"adm0_a3"`
				MinZoom   float64 `json:"min_zoom"`
				Scalerank int     `json:"scalerank"`
				Class     string  `json:"featurecla"`
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

	var out []label
	for _, feature := range doc.Features {
		p := feature.Properties
		if feature.Geometry == nil || feature.Geometry.Type != "Point" ||
			p.Name == "" || !placeClasses[p.Class] || !filter.keep(p.Country) {
			continue
		}
		var point [2]float64
		if err := json.Unmarshal(feature.Geometry.Coordinates, &point); err != nil {
			return nil, err
		}
		one := label{
			lon: point[0], lat: point[1],
			rank: p.Scalerank, zoom: int(math.Ceil(p.MinZoom)), name: p.Name,
		}
		if err := checkLabel(one); err != nil {
			return nil, err
		}
		out = append(out, one)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("not one feature is a populated place; is this the " +
			"populated-places file, and is -only a real country code?")
	}
	return out, nil
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
%s%s// Sizes:     %d name(s) of the %s in the dataset.
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
		groups, len(names), namesParagraph(src), src.picked, len(names), src.expected,
		src.upper, data, src.upper, encodedNames)

	return write(out, body, fmt.Sprintf("%s: %d name(s) in %d group(s)", in, len(names), groups))
}

// namesParagraph is the header's Names block for a source: its own if it brought one,
// and the local-spelling one otherwise.
func namesParagraph(src source) string {
	if src.names != "" {
		return src.names
	}
	return localNames
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
	features, err := parseFeatures(source)
	if err != nil {
		return nil, err
	}

	var out []polygonRing
	for _, feature := range features {
		polys, err := polygonsOf(feature.geoType, feature.coordinates)
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

// geojsonFeature is one feature of a FeatureCollection, with its geometry still raw.
//
// The split is at the properties and nowhere else. Which tag a file spells its zoom with
// differs from file to file — "min_zoom" in the lakes, "MIN_ZOOM" in the borders, "g_name"
// in the districts — so the properties stay unread here and each parser decodes the ones
// it wants. The geometry below them is the same shape in all of them, and this is the
// part that is shared.
type geojsonFeature struct {
	properties  json.RawMessage
	geoType     string
	coordinates json.RawMessage
}

// parseFeatures reads a FeatureCollection down to its features, which is the one thing
// every geometry file here has in common. It is also where the "this is not a
// FeatureCollection" guard lives, so that there is one of it rather than one per parser.
func parseFeatures(source []byte) ([]geojsonFeature, error) {
	var doc struct {
		Features []struct {
			Properties json.RawMessage `json:"properties"`
			Geometry   struct {
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

	out := make([]geojsonFeature, 0, len(doc.Features))
	for _, feature := range doc.Features {
		out = append(out, geojsonFeature{
			properties:  feature.Properties,
			geoType:     feature.Geometry.Type,
			coordinates: feature.Geometry.Coordinates,
		})
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
//
// `skip` is -minus: lakes whose name is already drawn by another file. It is applied to
// a whole feature rather than to a ring, because a lake with an island in it is two
// rings of one lake and half a lake is not a thing to draw.
func parseLakeShapes(source []byte, skip nameSet) (out []rawShape, areas, dropped int, err error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Name    string  `json:"name"`
				MinZoom float64 `json:"min_zoom"`
			} `json:"properties"`
			Geometry struct {
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
		if skip.has(feature.Properties.Name) {
			dropped++
			continue
		}
		polys, err := polygonsOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, 0, 0, err
		}
		for _, poly := range polys {
			areas++
			for _, ring := range poly {
				out = append(out, rawShape{points: ring, minZoom: feature.Properties.MinZoom})
			}
		}
	}
	if len(out) == 0 {
		return nil, 0, 0, fmt.Errorf("not one lake has a ring; is this the 1:10m lakes file?")
	}
	return out, areas, dropped, nil
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
// asked for, and reports how many lines the file holds in all and how many -minus took
// out again.
//
// The properties are lower case here as in the lakes file, and there is no country code:
// a river runs through several countries, so a filter by country would be a filter that
// means something different here than it does for a border. -only is refused for this
// kind rather than honoured with that other meaning.
//
// The rank is read as a number with a decimal point rather than as an integer, and that
// is not slackness: the 1:50m file spells its ranks "3" and the 1:10m file spells the
// same ranks "3.0", and a parser that demanded an integer would have rejected the whole
// 1:10m file with a message about a field nobody thought was a float.
func parseRiverShapes(source []byte, maxRank int, skip nameSet) (out []rawShape, kept, total, dropped int, err error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Name      string  `json:"name"`
				Scalerank float64 `json:"scalerank"`
				MinZoom   float64 `json:"min_zoom"`
			} `json:"properties"`
			Geometry *struct {
				Type        string          `json:"type"`
				Coordinates json.RawMessage `json:"coordinates"`
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, 0, 0, 0, err
	}
	if len(doc.Features) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}

	for _, feature := range doc.Features {
		if feature.Geometry == nil {
			continue
		}
		lines, err := linesOf(feature.Geometry.Type, feature.Geometry.Coordinates)
		if err != nil {
			return nil, 0, 0, 0, err
		}
		for _, line := range lines {
			total++
			if maxRank > 0 && feature.Properties.Scalerank > float64(maxRank) {
				continue
			}
			// Counted before -minus, so that the header's "N of M are rank this or
			// better" stays a fact about the source rather than a fact about this run.
			kept++
			if skip.has(feature.Properties.Name) {
				dropped++
				continue
			}
			out = append(out, rawShape{points: line, minZoom: feature.Properties.MinZoom})
		}
	}
	if len(out) == 0 {
		return nil, 0, 0, 0, fmt.Errorf("not one river is rank %d or better; is this the "+
			"rivers-and-lake-centrelines file, and is -max-rank meant for this one?", maxRank)
	}
	return out, kept, total, dropped, nil
}

// nameSet is the set of names -minus already draws.
type nameSet map[string]bool

// has reports whether a shape of this name should be left out. An empty name never
// matches, and that is the whole reason this is a method rather than a map lookup at the
// call sites: the 1:10m lakes file holds 610 unnamed rings, and a set that answered yes
// to "" would have thrown away every one of them — a map that lost its unnamed lakes
// would look exactly like a map whose lake file was truncated.
func (s nameSet) has(name string) bool {
	return name != "" && s[name]
}

// parseNames reads the names out of a GeoJSON file for -minus. Every feature's own name,
// whatever the geometry: what -minus is given is the file whose shapes are already on the
// map, and which of them are rings and which are lines is not a question it has to
// answer.
func parseNames(source []byte) (nameSet, error) {
	var doc struct {
		Features []struct {
			Properties struct {
				Name string `json:"name"`
			} `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(source, &doc); err != nil {
		return nil, err
	}
	if len(doc.Features) == 0 {
		return nil, fmt.Errorf("no features; this wants a GeoJSON FeatureCollection, not a shapefile or a TopoJSON")
	}
	set := nameSet{}
	for _, feature := range doc.Features {
		if name := feature.Properties.Name; name != "" {
			set[name] = true
		}
	}
	return set, nil
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
	name     string // "Natural Earth 1:10m land"
	url      string // may carry its own line breaks and indent, for a query string too long for one line
	home     string // the site the name above belongs to; empty means naturalearthdata.com
	licence  string // the header's Licence block; empty means the Natural Earth one
	noun     string // what the names in this file are, for the header's first line
	kind     string // the -kind that writes it
	upper    string // the export prefix: WORLD_LABEL_*, WORLD_REGION_*
	expected string // how many of them the dataset has, for the header's size line
	picked   string // a header block, or empty, for a dataset that needs one
	names    string // the header's Names block; empty means the local-spelling one

	// checksum is an extra paragraph under the header's SHA-256, for a source whose
	// bytes are not stable. Empty for every file whose download is the same file twice.
	checksum string
}

// ne is where every Natural Earth file in this program comes from. Written once because
// it is the same forty characters nine times, and a typo in one of them would be a URL
// in a header that does not resolve.
const ne = "https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/"

var (
	landSource = source{
		name: "Natural Earth 1:10m land",
		url:  ne + "ne_10m_land.geojson",
	}
	countriesSource = source{
		name: "Natural Earth 1:10m admin-0 boundary lines",
		url:  ne + "ne_10m_admin_0_boundary_lines_land.geojson",
	}
	borderSource = source{
		name: "Natural Earth 1:10m admin-1 boundaries",
		url:  ne + "ne_10m_admin_1_states_provinces_lines.geojson",
	}
	lakeSource = source{
		name:  "Natural Earth 1:10m lakes",
		url:   ne + "ne_10m_lakes.geojson",
		upper: "LAKE",
	}
	lakeEuropeSource = source{
		name:  "Natural Earth 1:10m lakes, Europe",
		url:   ne + "ne_10m_lakes_europe.geojson",
		upper: "LAKE_EUROPE",
	}
	riverSource = source{
		name:  "Natural Earth 1:10m rivers and lake centrelines",
		url:   ne + "ne_10m_rivers_lake_centerlines.geojson",
		upper: "RIVER",
	}
	riverEuropeSource = source{
		name:  "Natural Earth 1:10m rivers, Europe",
		url:   ne + "ne_10m_rivers_europe.geojson",
		upper: "RIVER_EUROPE",
	}
	countrySource = source{
		name:     "Natural Earth 1:110m admin-0 countries",
		url:      ne + "ne_110m_admin_0_countries.geojson",
		noun:     "of the world's countries",
		kind:     "labels",
		upper:    "LABEL",
		expected: "177 countries",
	}
	regionSource = source{
		name:     "Natural Earth 1:10m admin-1 label points",
		url:      ne + "ne_10m_admin_1_label_points.geojson",
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
	citySource = source{
		name:     "Natural Earth 1:10m populated places",
		url:      ne + "ne_10m_populated_places_simple.geojson",
		noun:     "of the world's cities and towns",
		kind:     "cities",
		upper:    "CITY",
		expected: "7 342 places",
		names: `// Names:     whatever Natural Earth calls the place, and it is not one habit: the
//            provincial names next door are all in their own language and these are
//            not — "Zürich" keeps its umlaut, but Vienna and Munich arrive as "Vienna"
//            and "Munich". Nothing here renames them either way: a map that invented
//            its own spellings would disagree with the source file it is checked
//            against, which is the one thing this header promises it does not do.
`,
		picked: `// Picked:    the 7 295 places whose FEATURECLA is a place or a capital. The source
//            holds 7 342, and the 47 left out are "Scientific station", "Historic
//            place" and "Meteorological Station" — which are coordinates with a name
//            on them, and not places anybody lives.
`,
	}
	townSource = source{
		name: "GeoNames cities5000",
		url:  "https://download.geonames.org/export/dump/cities5000.zip",
		home: "https://www.geonames.org/",
		// The licence is the same CC BY 4.0 as the districts' and is a different
		// paragraph, because the condition is a different one: GeoNames asks for the
		// attribution and names no particular page to put it on.
		licence:  townLicence,
		noun:     "of the world's towns",
		kind:     "towns",
		upper:    "TOWN",
		expected: "69 793 places",
		names: `// Names:     GeoNames' own name column, which is its preferred romanized spelling and
//            not the local one: Vienna is "Vienna" here exactly as it is in cities.js,
//            Munich is "Munich", Tokyo is "Tokyo". The two files therefore agree word for
//            word wherever they overlap, and what this file adds is the places Natural
//            Earth has not got at all — "Krems an der Donau", "Wiener Neustadt", "Wels".
//            That is why the overlap needs no rule: a town whose name is already on the
//            map has its duplicate dropped by the collision pass, and there is no
//            question of which spelling wins because there is only one.
//
//            Where the two do differ it is the long official form against the short one —
//            GeoNames writes "Krems an der Donau" and nothing else writes Krems at all —
//            and the long form is the one kept. A reader zoomed into a district is
//            looking at a town two kilometres wide; the name may as well be the one on
//            the sign.
`,
	}
	districtSource = source{
		name: "Statistik Austria political districts",
		url: "https://www.statistik.at/gs-open/GEODATA/ows?service=WFS&version=1.1.0\n" +
			"//            &request=GetFeature&typeName=GEODATA:STATISTIK_AUSTRIA_POLBEZ_20260101\n" +
			"//            &srsName=EPSG:4326&outputFormat=application/json",
		home:     "https://data.statistik.gv.at/",
		licence:  districtLicence,
		noun:     "of Austria's political districts",
		kind:     "districts",
		upper:    "DISTRICT",
		expected: "117 areas",
		checksum: `//            Not for this file, and that is the WFS rather than the data: every response
//            from it carries a timeStamp of the moment it was built, so two downloads of
//            the same register differ in that one field and in nothing else — 21 588 222
//            bytes agreeing up to the last 124. What the checksum above identifies is
//            therefore the geometry on the day it was fetched, and a re-download that is
//            the same size and does not match it is still the same 117 districts.
`,
	}
)

// licence is the same paragraph in every file but the two that are not public domain,
// because they are the same dataset.
const licence = `// Licence:   public domain. Natural Earth asks for no attribution and this project
//            gives none, which is the whole reason it was chosen over a dataset that
//            would have put somebody else's name on the operator's own map.`

// districtLicence is one of the two licence blocks in this program that ask for
// something. The other is townLicence.
//
// Austria's political districts are not in any public-domain dataset — Natural Earth's
// only second-order layer is the 3 224 counties of the United States — so the choice was
// this or no districts at all. CC BY 4.0 is what Statistik Austria publishes them under,
// and the condition is that whoever shows them says whose they are. That condition is
// stated here, in the file, rather than in a footnote somewhere, because the file is
// what a person reads when they wonder where a border on their dashboard came from.
const districtLicence = `// Licence:   CC BY 4.0 — Statistik Austria, "Gliederung Österreichs in politische
//            Bezirke", https://data.statistik.gv.at/. This is one of the two files on
//            this map that are not public domain, and one of the two that carry a
//            condition: showing the map shows Statistik Austria's data, and CC BY 4.0
//            asks to be named for it. The other is the towns layer, under the same
//            licence and a different author. Nothing else here asks for anything.
//
//            The layer is Austria and only Austria. Everywhere else on the map the
//            district band is empty, because everywhere else there is no public-domain
//            source to fill it from — an empty band is the honest picture of that, and
//            a band drawn from a dataset with a different licence would not be.`

// townLicence is the second of the two licence blocks that ask for something.
//
// GeoNames is the only dataset here that has the towns in it — Natural Earth's populated
// places stops at about thirty per country and the difference is a map of Austria with no
// Krems on it — and it is CC BY 4.0, not public domain. The condition is that whoever
// shows the names says where they came from, and this file says it in its own header,
// where somebody looking at a town on their dashboard will find it.
//
// It is honest about the one thing a reader may wonder about, which is whether a name
// from here can be trusted next to a name from Natural Earth: both sources publish a
// preferred romanized spelling, and on the large places they are the same word.
const townLicence = `// Licence:   CC BY 4.0 — GeoNames, https://www.geonames.org/, from the cities5000
//            dump. This file is not public domain and it carries a condition: showing
//            the map shows GeoNames' names, and CC BY 4.0 asks to be named for them.
//            The other file on this map that asks for anything is Austria's districts,
//            which are under the same licence and name Statistik Austria in their own
//            header, in districts.js.
//
//            GeoNames' name column is a romanized preferred spelling rather than a local
//            one, which is the same habit Natural Earth has: Vienna is "Vienna" in both
//            files and not "Wien". Nothing here was rewritten, and the header says what
//            the source's own spelling rule is so that a reader can tell a name they
//            disagree with from a name this program changed.`

func provenance(src source, in, sum string) string {
	home, lic := src.home, src.licence
	if home == "" {
		home = "https://www.naturalearthdata.com/"
	}
	if lic == "" {
		lic = licence
	}
	return fmt.Sprintf(`// Source:    %s, %s
//            %s
%s
// Input:     %s
// SHA-256:   %s
//            The checksum is what identifies the input; the name above is only what
//            it happened to be saved as. Re-download the URL, check the checksum, and
//            the file this produced can be reproduced byte for byte.
%s`, src.name, home, src.url, lic, in, sum, src.checksum)
}

// localNames is the header block for every dataset whose names are in the language of
// the place. cities.js is the one file that brings its own, and it says so in as many
// words: a reader deciding whether to trust a spelling deserves to be told which kind of
// source they are looking at.
const localNames = `// Names:     in the source's own spelling and its own language — "Niedersachsen", not
//            "Lower Saxony", and "Kärnten", not "Carinthia". Nothing here translates
//            them, and that is deliberate: they are what a person living there calls
//            the place, and the map is not a document that has to read as one language.
`

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
