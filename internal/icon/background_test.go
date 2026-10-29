package icon

import (
	"bytes"
	"image"
	"strings"
	"testing"
)

func flatPicture(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = 20
		img.Pix[i+1] = 40
		img.Pix[i+2] = 60
		img.Pix[i+3] = 255
	}
	return encodePNG(t, img)
}

func TestBackgroundLimitsAcceptAFourKImage(t *testing.T) {
	data := flatPicture(t, 3840, 2160)
	info, err := DecodeWithin(data, BackgroundLimits)
	if err != nil {
		t.Fatalf("a 4K image was refused: %v", err)
	}
	if info.Width != 3840 || info.Height != 2160 || info.MediaType != "image/png" {
		t.Errorf("info = %+v, want 3840×2160 PNG", info)
	}
}

func TestBackgroundLimitsRefuseTooManyPixels(t *testing.T) {
	data := flatPicture(t, BackgroundMaxSide+1, 1)
	_, err := DecodeWithin(data, BackgroundLimits)
	if err == nil || !strings.Contains(err.Error(), "a background image may be at most 4096×4096 pixels") {
		t.Fatalf("a too-wide image answered %v, want the pixel sentence", err)
	}
}

func TestBackgroundLimitsRefuseTooManyBytes(t *testing.T) {
	data := bytes.Repeat([]byte("p"), BackgroundMaxBytes+1)
	_, err := DecodeWithin(data, BackgroundLimits)
	if err == nil || !strings.Contains(err.Error(), "a background image may be at most 12 MiB") {
		t.Fatalf("an oversized file answered %v, want the byte sentence", err)
	}
}

func TestBackgroundLimitsRefuseWhatIsNotAnImage(t *testing.T) {
	for name, data := range map[string][]byte{
		"text": []byte("this is not a picture"),
		"webp": append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 32)...),
	} {
		_, err := DecodeWithin(data, BackgroundLimits)
		if err == nil || !strings.Contains(err.Error(), "a background image has to be a PNG, a JPEG or a GIF") {
			t.Errorf("%s answered %v, want the format sentence", name, err)
		}
	}
}

func TestIconErrorWordingIsUnchanged(t *testing.T) {
	_, err := Decode(bytes.Repeat([]byte("p"), MaxBytes+1))
	if err == nil || !strings.Contains(err.Error(), "an icon may be at most 64 KiB") {
		t.Errorf("icon byte refusal = %v", err)
	}
	_, err = Decode([]byte("not an image"))
	if err == nil || !strings.Contains(err.Error(), "an icon has to be a PNG, a JPEG or a GIF") {
		t.Errorf("icon format refusal = %v", err)
	}
}

func TestExactMiBIsNamed(t *testing.T) {
	if got := formatBytes(12 << 20); got != "12 MiB" {
		t.Errorf("formatBytes(12 MiB) = %q", got)
	}
	if got := formatBytes(MaxBytes + 1); got != "65537 bytes" {
		t.Errorf("formatBytes(MaxBytes+1) = %q, want raw bytes", got)
	}
}
