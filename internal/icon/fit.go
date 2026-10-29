package icon

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
)

// minFitSide is the smallest a picture is shrunk to while trying to get under the
// byte limit. Below this an icon is a smudge at the size the dashboard draws, and
// a file that still does not fit there is not a picture of anything.
const minFitSide = 64

// Fit makes a picture from somewhere else meet the rules an upload has to meet.
//
// The online catalogue holds pictures at whatever size the project drew them — 512
// is the common one, 623×512 is a wide logo, and some are larger on both sides. An
// upload is refused when it is bigger than the dashboard needs, and that refusal is
// right for a file somebody picked: it is their file, and quietly shrinking it
// would be a surprise. A picture being imported is a different thing. It was chosen
// by name from a catalogue, it is going to be drawn at twenty pixels, and the
// sensible answer to "too large" is to make it the size this program draws.
//
// So this reduces and re-encodes, and never enlarges. A picture that already fits
// comes back byte for byte — the common case, and the one where the stored copy
// should be exactly the file the project published.
//
// The reduction is an area average rather than a nearest-neighbour sample: the
// picture is being made smaller, every source pixel is a part of the result, and
// dropping the ones that fall between destinations is how a logo ends up with
// ragged edges and broken strokes.
func Fit(data []byte) ([]byte, Info, error) {
	if info, err := Decode(data); err == nil {
		return data, info, nil
	}
	// Only a size problem is worth working around. Anything else — text that is
	// not an image, a truncated file — gets the refusal Decode wrote, which says
	// what is wrong with it.
	sniffed := http.DetectContentType(data)
	if !AllowedMediaType(sniffed) {
		_, err := Decode(data)
		return nil, Info{}, err
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		_, decErr := Decode(data)
		return nil, Info{}, decErr
	}
	for side := MaxSide; side >= minFitSide; side = side * 4 / 5 {
		scaled := fitInto(src, side)
		var buf bytes.Buffer
		enc := &png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, scaled); err != nil {
			return nil, Info{}, fmt.Errorf("the picture could not be re-encoded: %w", err)
		}
		info, err := Decode(buf.Bytes())
		if err != nil {
			// Almost certainly the byte limit at this side, so a smaller one is
			// worth a try. A failure for any other reason lands here too, and the
			// loop ending is the same answer either way.
			continue
		}
		return buf.Bytes(), info, nil
	}
	return nil, Info{}, fmt.Errorf("the picture could not be brought under %s at any size",
		formatBytes(MaxBytes))
}

// fitInto returns src reduced so that neither side is larger than max, or src
// itself when it already is not.
func fitInto(src image.Image, max int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return src
	}
	scale := float64(max) / float64(w)
	if float64(h)*scale > float64(max) {
		scale = float64(max) / float64(h)
	}
	nw := maxInt(1, int(math.Round(float64(w)*scale)))
	nh := maxInt(1, int(math.Round(float64(h)*scale)))
	dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))

	// How much of the source one destination pixel covers, in source pixels.
	sx := float64(w) / float64(nw)
	sy := float64(h) / float64(nh)
	for y := 0; y < nh; y++ {
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		for x := 0; x < nw; x++ {
			x0, x1 := float64(x)*sx, float64(x+1)*sx
			var r, g, bl, a, total float64
			for yy := int(y0); yy < int(math.Ceil(y1)) && yy < h; yy++ {
				wy := math.Min(y1, float64(yy+1)) - math.Max(y0, float64(yy))
				if wy <= 0 {
					continue
				}
				for xx := int(x0); xx < int(math.Ceil(x1)) && xx < w; xx++ {
					wx := math.Min(x1, float64(xx+1)) - math.Max(x0, float64(xx))
					if wx <= 0 {
						continue
					}
					weight := wx * wy
					// RGBA is premultiplied, which is what averaging wants: a
					// transparent pixel contributes nothing rather than dragging
					// the colour of everything around it towards black.
					cr, cg, cb, ca := src.At(b.Min.X+xx, b.Min.Y+yy).RGBA()
					r += float64(cr) * weight
					g += float64(cg) * weight
					bl += float64(cb) * weight
					a += float64(ca) * weight
					total += weight
				}
			}
			if total == 0 {
				continue
			}
			r, g, bl, a = r/total, g/total, bl/total, a/total
			if a <= 0 {
				continue // already the zero pixel, which is fully transparent
			}
			// Written into the buffer rather than through the NRGBA type: the
			// standard library's name for that colour package is one the linter
			// this repository runs reads as a misspelling of "colour", and every
			// file that imports it has to say so. Four assignments are easier to
			// read than an exception.
			at := dst.PixOffset(x, y)
			dst.Pix[at] = straight(r, a)
			dst.Pix[at+1] = straight(g, a)
			dst.Pix[at+2] = straight(bl, a)
			dst.Pix[at+3] = uint8(math.Round(a / 0xffff * 255))
		}
	}
	return dst
}

// straight turns one premultiplied channel back into the straight alpha an NRGBA
// pixel stores. Both come from the RGBA method, which is 16-bit.
func straight(v, a float64) uint8 {
	s := v / a * 255
	switch {
	case s <= 0:
		return 0
	case s >= 255:
		return 255
	default:
		return uint8(math.Round(s))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
