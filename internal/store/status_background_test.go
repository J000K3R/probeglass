package store

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
)

// TestStatusBackgroundRoundTrip covers the whole life of the one image: stored
// unchanged, replaced, read back, and removed.
func TestStatusBackgroundRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, ok, err := st.StatusBackground(ctx); err != nil || ok {
		t.Fatalf("a fresh store reports a background: ok=%v err=%v", ok, err)
	}

	first := testPNG(t, 640, 360)
	var bg model.StatusBackground
	if err := st.SetStatusBackground(ctx, &bg, first); err != nil {
		t.Fatalf("store background: %v", err)
	}
	if bg.Width != 640 || bg.Height != 360 || bg.Size != len(first) || bg.Digest == "" {
		t.Errorf("stored background = %+v", bg)
	}

	meta, ok, err := st.StatusBackground(ctx)
	if err != nil || !ok {
		t.Fatalf("read background: ok=%v err=%v", ok, err)
	}
	if meta.Digest != bg.Digest || meta.Size != len(first) {
		t.Errorf("metadata = %+v, want the stored digest and size", meta)
	}

	read, data, ok, err := st.StatusBackgroundBytes(ctx)
	if err != nil || !ok {
		t.Fatalf("read background bytes: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(data, first) {
		t.Error("the stored bytes are not the bytes that were uploaded")
	}
	if read.MediaType != "image/png" {
		t.Errorf("media type = %q", read.MediaType)
	}

	second := testPNG(t, 320, 200)
	var replaced model.StatusBackground
	if err := st.SetStatusBackground(ctx, &replaced, second); err != nil {
		t.Fatalf("replace background: %v", err)
	}
	if replaced.Digest == bg.Digest || replaced.Size == bg.Size {
		t.Error("the replacement did not change the digest and the size")
	}

	if err := st.DeleteStatusBackground(ctx); err != nil {
		t.Fatalf("delete background: %v", err)
	}
	if _, ok, err := st.StatusBackground(ctx); err != nil || ok {
		t.Errorf("the background survived the delete: ok=%v err=%v", ok, err)
	}
	if err := st.DeleteStatusBackground(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("a second delete = %v, want ErrNotFound", err)
	}
}

func TestStatusBackgroundRefusesWhatTheLimitsRefuse(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	for name, data := range map[string][]byte{
		"text":  []byte("not a picture"),
		"wide":  testPNG(t, 4097, 1),
		"empty": {},
	} {
		var bg model.StatusBackground
		if err := st.SetStatusBackground(ctx, &bg, data); err == nil {
			t.Errorf("%s was stored", name)
		}
	}
	if _, ok, _ := st.StatusBackground(ctx); ok {
		t.Error("a refused upload wrote a row")
	}
}
