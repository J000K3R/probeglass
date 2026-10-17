package icon

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	// The three formats are the only ones an upload may be, so their decoders are
	// imported here rather than registered somewhere else: this file is what knows
	// what an icon may be, and a decoder missing from it would mean a format the
	// allowlist names and nothing can read.
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"
)

const (
	// MaxBytes bounds one uploaded image. Generous for a symbol drawn at twenty
	// pixels and small enough that the database stays one file somebody can copy:
	// a hundred icons at the cap are six megabytes, beside a SQLite file that
	// holds weeks of probe results.
	MaxBytes = 64 << 10

	// MaxSide bounds the width and the height in pixels. An image twice the size
	// of the largest screen anybody draws an icon on is a file that was not meant
	// to be a symbol, and decoding one costs memory on every upload.
	MaxSide = 512
)

// MediaTypes are the formats an upload may be, in the order the file picker
// offers them.
//
// SVG is deliberately not among them, and that is the one refusal worth writing
// down: an SVG is not an image but a document, it may carry script and external
// references, and it would be served from this dashboard's own origin — where
// that script would then run with the session cookie of whoever looked at it. A
// raster image cannot do that. Everything the uploaded set is for is a small
// picture, and a picture can be a PNG.
var MediaTypes = []string{"image/png", "image/jpeg", "image/gif"}

// Info is what the database records about one uploaded image.
//
// The dimensions are worked out at upload and stored rather than measured at read
// time. Nothing needs them to serve the bytes, but they are what lets the picker
// lay a grid out without decoding every image it lists, and what a later re-encode
// — or a change of the size cap — would want to know.
type Info struct {
	MediaType     string
	Width, Height int
}

// Decode reads an uploaded image far enough to say what it is, and far enough to be
// sure that it is all there.
//
// It is both the allowlist and the malformation check, and it is why an upload is
// never stored unread: a file that claims to be a PNG and is not, or one that is
// truncated halfway, would otherwise be accepted and then drawn as a broken image
// on the dashboard, where the only report is a console entry nobody reads. Here it
// is a message next to the field that chose the file.
//
// It reads the file three times over, cheapest pass first: the first bytes to see
// what it claims to be, the header for the dimensions, and the whole image last.
// The last pass is the one that catches a file that stops halfway — a PNG header is
// enough for the first two and says nothing about whether the rest arrived — and it
// runs after the size limits so that a picture too large to be an icon is refused
// before it is decoded.
func Decode(data []byte) (Info, error) {
	var info Info
	if len(data) == 0 {
		return info, errors.New("the file is empty")
	}
	if len(data) > MaxBytes {
		return info, fmt.Errorf("an icon may be at most %s; this one is %s",
			formatBytes(MaxBytes), formatBytes(len(data)))
	}
	// What the file says it is, before what it is decoded as. DetectContentType
	// reads the first bytes and nothing else, so this rejects a text file that was
	// renamed to .png before a decoder ever looks at it as an image.
	sniffed := http.DetectContentType(data)
	if !allowed(sniffed) {
		return info, fmt.Errorf("an icon has to be a PNG, a JPEG or a GIF; this looks like %s",
			describe(sniffed))
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return info, fmt.Errorf("the image could not be read: %w", err)
	}
	info = Info{MediaType: "image/" + format, Width: cfg.Width, Height: cfg.Height}
	if info.Width <= 0 || info.Height <= 0 {
		return info, errors.New("the image has no size")
	}
	if info.Width > MaxSide || info.Height > MaxSide {
		return info, fmt.Errorf("an icon may be at most %d×%d pixels; this one is %d×%d",
			MaxSide, MaxSide, info.Width, info.Height)
	}
	// The decode decides the media type, not the sniff: the two agree for a real
	// image, and where they would not, the bytes that just decoded are the ones the
	// file will be served as.
	if !allowed(info.MediaType) {
		return info, fmt.Errorf("images of type %s are not accepted", info.MediaType)
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return info, fmt.Errorf("the image could not be read: %w", err)
	}
	return info, nil
}

func allowed(mediaType string) bool {
	for _, m := range MediaTypes {
		if m == mediaType {
			return true
		}
	}
	return false
}

// describe names what was found instead, so that "this looks like text/plain" is
// more use than a refusal that only says no.
func describe(mediaType string) string {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = mediaType[:i]
	}
	return mediaType
}

// formatBytes writes a size the way a person says it, for the two places a limit
// is quoted back at whoever exceeded it.
func formatBytes(n int) string {
	if n%1024 == 0 && n >= 1024 {
		return fmt.Sprintf("%d KiB", n/1024)
	}
	return fmt.Sprintf("%d bytes", n)
}
