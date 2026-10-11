// Command worldgen turns a Natural Earth land polygon file into the coastline the
// dashboard draws when no map tiles are configured.
//
// The dashboard ships a slippy map with no mapping library and, since the tile
// servers stopped being usable by anybody but their own websites, no tiles either.
// What it draws instead is this: the world's coastlines, as rings of coordinates in
// a file small enough to embed in the binary. The map is still a real map — places
// land where they belong, and the graticule underneath is honest — it simply has
// nothing on it that somebody else's server had to be asked for.
//
// It reads a file and writes a file, and it is the only thing in this repository
// that reads something nobody committed. So the output carries everything needed to
// reproduce it: the source URL, the checksum of the input, the command line, and the
// licence. Fetching the input is not this program's business — it is one curl in
// docs/CONFIGURATION.md, so that regenerating the file is a thing a person can
// actually do rather than a thing this program would have to be trusted about.
//
// # The format
//
// A string of six-bit characters, in the base64 alphabet, decoded by
// internal/web/assets/outline.js. It holds one ring count and then, per ring, a point
// count followed by the points as deltas from the one before:
//
//	ring count          varint
//	per ring:
//	  point count       varint
//	  per point:
//	    Δ longitude     zigzag varint, in thousandths of a degree
//	    Δ latitude      zigzag varint, in thousandths of a degree
//
// A varint is base 63 written most significant chunk first, closed by a chunk of 63.
// The closing chunk is one character per number and it is the price of having a
// marker at all: six bits is exactly enough for 64 symbols and no more, so a digit and
// a "there is more" cannot share one. The digits are one character each up to 62, which
// every delta between two neighbouring points of a coastline is, and two or three for
// the first point of a ring, which is absolute.
//
// Deltas are most of the saving: consecutive points of a coastline are a few
// thousandths of a degree apart, so almost the whole file is two characters per
// coordinate. It also keeps the output to one line of source text, which is why a
// 5 000-point world costs about 20 KB — a fortieth of what the same rings cost as
// the GeoJSON they came from.
//
// Coordinates are quantised to a thousandth of a degree and rings are simplified
// before that. Both were chosen against the map they feed: at zoom 19, one
// thousandth of a degree is a hundredth of a pixel, and the map does not go past
// zoom 19. The simplification is Douglas-Peucker at a tolerance in degrees, which
// over-simplifies longitude near the poles — see the note in
// docs/CONFIGURATION.md for what that costs and what to do about it.
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
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "worldgen:", err)
		os.Exit(1)
	}
}

func run() error {
	in := flag.String("in", "", "Natural Earth GeoJSON file to read (see docs/CONFIGURATION.md for where to get it)")
	out := flag.String("out", "internal/web/assets/world.js", "file to write")
	tol := flag.Float64("tol", 0.02, "Douglas-Peucker tolerance in degrees; larger is smaller and coarser")
	minPoints := flag.Int("min-points", 4, "drop rings left with fewer points than this after simplifying")
	flag.Parse()

	if *in == "" {
		return fmt.Errorf("-in is required: this program does not fetch anything, and says so on purpose")
	}

	source, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sum := sha256hex(source)

	rings, err := parseLand(source)
	if err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	before := countPoints(rings)

	kept := make([][][2]float64, 0, len(rings))
	for _, ring := range rings {
		simplified := simplify(ring, *tol)
		if len(simplified) < *minPoints {
			continue
		}
		kept = append(kept, simplified)
	}
	if len(kept) == 0 {
		return fmt.Errorf("%s: every ring was simplified away; is -tol %g meant for a different dataset?",
			*in, *tol)
	}

	data, after := encode(kept)
	body := render(filepath.Base(*in), sum, *tol, *minPoints, len(kept), before, after, data)
	// 0644 and the linter is told why rather than obeyed: the output is a source file
	// in the repository, committed and read by the compiler and by anybody who wants to
	// check it. There is nothing in it to keep from anybody — the input it came from is
	// public domain and the repository it is committed to is open.
	if err := os.WriteFile(*out, []byte(body), 0o644); err != nil { //nolint:gosec // G306: a committed source file, see above
		return err
	}
	fmt.Printf("%s: %d ring(s), %d point(s) in, %d out, %.1f%% kept, %d byte file\n",
		*in, len(kept), before, after, 100*float64(after)/float64(before), len(body))
	return nil
}

// parseLand reads the rings out of a GeoJSON FeatureCollection of Polygon and
// MultiPolygon geometries.
//
// Holes are dropped, and that is a decision rather than an oversight. A hole in a
// land polygon is a lake, the coarsest Natural Earth layer draws the Caspian and
// the Great Lakes and nothing smaller, and drawing them correctly means a
// fill-rule that gets the winding of two nested rings right — in a projection where
// the rings are not polygons any more anyway. A lake drawn as land at zoom 3 is a
// lake nobody notices; a lake drawn inside out is a continent-shaped hole.
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

// simplify runs Douglas-Peucker over a closed ring and returns it closed again.
//
// The ring is treated as an open line from its first point back to itself: the
// end point is pinned so the shape stays closed, which is what the fill needs. The
// tolerance is a distance in degrees on a flat lon/lat plane and therefore
// over-states its own effect near the poles, where a degree of longitude is a
// fraction of a degree of latitude. That is the honest limitation of doing this in
// degrees, and at 1:110m the Arctic is coarse either way.
func simplify(ring [][2]float64, tol float64) [][2]float64 {
	if len(ring) < 3 {
		return ring
	}
	keep := make([]bool, len(ring))
	keep[0] = true
	keep[len(ring)-1] = true
	dp(ring, 0, len(ring)-1, tol, keep)

	out := make([][2]float64, 0, len(ring))
	for i, point := range ring {
		if keep[i] {
			out = append(out, point)
		}
	}
	return out
}

// dp marks the points of ring[lo:hi+1] that lie further than tol from the line
// between its ends, and recurses into the two halves it divides.
func dp(ring [][2]float64, lo, hi int, tol float64, keep []bool) {
	if hi <= lo+1 {
		return
	}
	far, at := 0.0, -1
	for i := lo + 1; i < hi; i++ {
		if d := distance(ring[i], ring[lo], ring[hi]); d > far {
			far, at = d, i
		}
	}
	if at < 0 || far <= tol {
		return
	}
	keep[at] = true
	dp(ring, lo, at, tol, keep)
	dp(ring, at, hi, tol, keep)
}

// distance is the perpendicular distance from p to the line through a and b, or
// the distance to a when the two are the same point.
func distance(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	if dx == 0 && dy == 0 {
		return math.Hypot(p[0]-a[0], p[1]-a[1])
	}
	return math.Abs(dy*(p[0]-a[0])-dx*(p[1]-a[1])) / math.Hypot(dx, dy)
}

// encode writes the rings out and reports how many points went in.
//
// The alphabet is the URL-safe base64 one, in a different order, so that a reader
// who has seen one of these before recognises the shape. Nothing depends on the
// order; it is only a lookup table.
func encode(rings [][][2]float64) (string, int) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

	var b strings.Builder
	// A varint is base 63, most significant chunk first, and a chunk of 63 closes
	// it. The closing chunk costs one character per number, which is the price of
	// having a marker at all: six bits is exactly enough for 64 symbols and no more,
	// so a digit and a "there is more" cannot share one.
	varint := func(v uint64) {
		var digits []byte
		for {
			digits = append(digits, byte(v%63))
			v /= 63
			if v == 0 {
				break
			}
		}
		for i := len(digits) - 1; i >= 0; i-- {
			b.WriteByte(alphabet[digits[i]])
		}
		b.WriteByte(alphabet[63])
	}
	// Zigzag: interleave the sign into the low bit, so -1 becomes 1, 1 becomes 2 and
	// -2 becomes 3 — small negatives stay as short as small positives.
	//
	// The two conversions move bits and nothing else. Go defines a signed shift as
	// arithmetic and a signed-to-unsigned conversion as the two's-complement pattern,
	// so no value can be lost, which is exactly what the linter cannot see from here.
	// A coordinate is also bounded a hundred times over by the time it arrives: it is
	// degrees times a thousand.
	zigzag := func(v int64) uint64 {
		return uint64(v<<1) ^ uint64(v>>63) //nolint:gosec // G115: bit pattern, not magnitude
	}

	varint(uint64(len(rings)))
	points := 0
	for _, ring := range rings {
		varint(uint64(len(ring)))
		prevLon, prevLat := int64(0), int64(0)
		for i, point := range ring {
			lon, lat := quantise(point)
			if i == 0 {
				// The first point is absolute, so that a ring does not depend on the
				// ring before it: one dropped or reordered ring cannot shift the rest.
				varint(zigzag(lon))
				varint(zigzag(lat))
			} else {
				varint(zigzag(lon - prevLon))
				varint(zigzag(lat - prevLat))
			}
			prevLon, prevLat = lon, lat
			points++
		}
	}
	return b.String(), points
}

// quantise rounds a degree pair to thousandths of a degree, which is a hundredth of
// a pixel at the deepest zoom the map offers.
func quantise(point [2]float64) (int64, int64) {
	return int64(math.Round(point[0] * 1000)), int64(math.Round(point[1] * 1000))
}

func countPoints(rings [][][2]float64) int {
	n := 0
	for _, ring := range rings {
		n += len(ring)
	}
	return n
}

// render lays the file out, provenance first. The header is the whole reason this
// is a committed file rather than a build step: anybody who wants to check it, or
// replace it, has everything they need in the file itself.
func render(in, sum string, tol float64, minPoints, rings, before, after int, data string) string {
	return fmt.Sprintf(`// The world's coastlines, for the map to draw when no tile server is configured.
//
// Generated by cmd/worldgen. Do not edit by hand — edit the generator, or the
// tolerance, and run it again:
//
//	go run ./cmd/worldgen -in <the file below> -out internal/web/assets/world.js
//
// Source:    Natural Earth 1:110m land, https://www.naturalearthdata.com/
//            https://raw.githubusercontent.com/nvkelso/natural-earth-vector/master/geojson/ne_110m_land.geojson
// Licence:   public domain. Natural Earth asks for no attribution and this project
//            gives none, which is the whole reason it was chosen over a dataset that
//            would have put somebody else's name on the operator's own map.
// Input:     %s
// SHA-256:   %s
//            The checksum is what identifies the input; the name above is only what
//            it happened to be saved as. Re-download the URL, check the checksum, and
//            the file this produced can be reproduced byte for byte.
// Tolerance: %g degrees (Douglas-Peucker), %d points in, %d kept, -min-points %d
//
// Roughly 1:110m: coastlines are recognisable to about zoom 6 and become straight
// lines between distant points in a city. Entering a tile URL in Settings is what
// that field is for. See docs/CONFIGURATION.md.

export const WORLD_RINGS = %d;
export const WORLD_SCALE = 1000;
export const WORLD_DATA = '%s';
`, in, sum, tol, before, after, minPoints, rings, data)
}

func sha256hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
