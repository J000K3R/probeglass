package icon

import (
	"bytes"
	"image"
	"image/png"
	"math"
	"testing"
)

// picture makes a PNG of the given size in the shape a logo has: a few smooth areas
// rather than noise, so that the file is small and the size limit that matters is
// the one on the sides.
//
// The pixels are written straight into the buffer rather than through the standard
// library's RGBA type, for the reason internal/api's test gives: that package's name
// is one this repository's linter reads as a misspelling of "colour".
func picture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			at := img.PixOffset(x, y)
			img.Pix[at] = uint8(x * 255 / w)
			img.Pix[at+1] = uint8(y * 255 / h)
			img.Pix[at+2] = 128
			img.Pix[at+3] = 255
		}
	}
	return encodePNG(t, img)
}

// noisyPicture makes one that will not compress: every pixel is different, which is
// how a picture ends up over the byte limit while being the right shape.
func noisyPicture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			at := img.PixOffset(x, y)
			img.Pix[at] = uint8(x*7 + y*13)
			img.Pix[at+1] = uint8(x*x + y*3)
			img.Pix[at+2] = uint8(x*11 ^ y*5)
			img.Pix[at+3] = uint8(200 + (x+y)%56)
		}
	}
	return encodePNG(t, img)
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode a picture: %v", err)
	}
	return buf.Bytes()
}

// A wide logo — the shape the online catalogue really has — is made the size the
// dashboard draws instead of being refused.
func TestFitReducesAWideLogo(t *testing.T) {
	data := picture(t, 623, 512)

	got, info, err := Fit(data)
	if err != nil {
		t.Fatalf("Fit(623x512): %v", err)
	}
	if info.Width > MaxSide || info.Height > MaxSide {
		t.Fatalf("Fit left a %dx%d image, and the limit is %d", info.Width, info.Height, MaxSide)
	}
	if info.Width != 512 {
		t.Errorf("the long side is %d, want 512", info.Width)
	}
	// The aspect ratio has to survive: a squashed logo is worse than a missing one.
	wantHeight := int(math.Round(512.0 / 623.0 * 512.0))
	if info.Height < wantHeight-1 || info.Height > wantHeight+1 {
		t.Errorf("the short side is %d, want about %d", info.Height, wantHeight)
	}
	if bytes.Equal(got, data) {
		t.Error("the picture came back unchanged, and it was too wide to keep")
	}
}

func TestFitLeavesAPictureThatAlreadyFitsAlone(t *testing.T) {
	data := picture(t, 64, 64)

	got, info, err := Fit(data)
	if err != nil {
		t.Fatalf("Fit(64x64): %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("a picture that already fits was re-encoded; the stored copy should be the original file")
	}
	if info.Width != 64 || info.Height != 64 {
		t.Errorf("Fit reported %dx%d", info.Width, info.Height)
	}
}

// A picture the right shape but too many bytes is made smaller until it fits. The
// limit that matters is the one on the file, not only on the sides.
func TestFitReducesBytesUntilItFits(t *testing.T) {
	data := noisyPicture(t, 800, 800)
	if len(data) <= MaxBytes {
		t.Fatalf("the test picture is only %d bytes; it was meant to be over the limit", len(data))
	}

	got, info, err := Fit(data)
	if err != nil {
		t.Fatalf("Fit(800x800, %d bytes): %v", len(data), err)
	}
	if len(got) > MaxBytes {
		t.Fatalf("Fit answered with %d bytes, and the limit is %d", len(got), MaxBytes)
	}
	if info.Width > MaxSide || info.Height > MaxSide {
		t.Fatalf("Fit left a %dx%d image", info.Width, info.Height)
	}
}

func TestFitRefusesWhatIsNotAnImage(t *testing.T) {
	_, _, err := Fit([]byte("this is not an image"))
	if err == nil {
		t.Fatal("Fit accepted text")
	}
	if err.Error() == "" {
		t.Error("the refusal says nothing")
	}
}
