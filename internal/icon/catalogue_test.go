package icon

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// catalogueServer stands in for dashboardicons: an index at /metadata.json and a
// picture for every name at /png/<name>.png.
func catalogueServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var indexHits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/metadata.json", func(w http.ResponseWriter, r *http.Request) {
		indexHits.Add(1)
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{
		  "plex":         {"base": "png", "aliases": [], "categories": ["Media"]},
		  "pi-hole":      {"base": "svg", "aliases": ["pihole", "dns"], "categories": ["Network"]},
		  "home-assistant": {"base": "svg", "aliases": ["hass"], "categories": ["Automation"]},
		  "router":       {"base": "svg", "aliases": [], "categories": ["Network"]}
		}`))
	})
	mux.HandleFunc("/png/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/png/"), ".png")
		switch name {
		case "missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("Couldn't find the requested file"))
			return
		case "big":
			// Bigger than an icon may be, and therefore a picture Fit has to be able
			// to work on. If this came back truncated at the icon limit, Fit would be
			// handed half a file and could not decode it at all.
			_, _ = w.Write(bytes.Repeat([]byte("p"), 3*MaxBytes))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("picture of " + name))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &indexHits
}

func newTestCatalogue(srv *httptest.Server) *Catalogue {
	c := NewCatalogue()
	c.IndexURL = srv.URL + "/metadata.json"
	c.ImageURL = srv.URL + "/png/"
	return c
}

func names(icons []CatalogueIcon) []string {
	out := make([]string, len(icons))
	for i, ic := range icons {
		out[i] = ic.Name
	}
	return out
}

func TestCatalogueSearchesNamesAndAliases(t *testing.T) {
	srv, _ := catalogueServer(t)
	c := newTestCatalogue(srv)
	ctx := context.Background()

	// An alias finds the icon: somebody searching for "pihole" wants pi-hole, and
	// the project keeps that spelling as an alias rather than as a second icon.
	got, err := c.Search(ctx, "pihole", 10)
	if err != nil {
		t.Fatalf("searching for an alias: %v", err)
	}
	if len(got) != 1 || got[0].Name != "pi-hole" {
		t.Fatalf("searching for pihole found %v, want pi-hole", names(got))
	}

	// A prefix finds them all, and the ones that start with the term come first.
	got, err = c.Search(ctx, "pi", 10)
	if err != nil {
		t.Fatalf("searching by prefix: %v", err)
	}
	if len(got) != 1 || got[0].Name != "pi-hole" {
		t.Fatalf("searching for pi found %v, want pi-hole", names(got))
	}

	// A name that starts with the term ranks above one that merely contains it.
	got, err = c.Search(ctx, "assistant", 10)
	if err != nil {
		t.Fatalf("searching by name: %v", err)
	}
	if len(got) != 1 || got[0].Name != "home-assistant" {
		t.Fatalf("searching for assistant found %v", names(got))
	}

	// Nothing matching is an empty answer, not an error.
	got, err = c.Search(ctx, "nothing-here", 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("searching for something absent answered %v, %v", names(got), err)
	}
}

func TestCatalogueRevalidatesInsteadOfDownloadingAgain(t *testing.T) {
	srv, hits := catalogueServer(t)
	c := newTestCatalogue(srv)
	ctx := context.Background()

	if _, err := c.Search(ctx, "plex", 10); err != nil {
		t.Fatalf("first search: %v", err)
	}
	// The second search asks again, with the validator it was given, and the answer
	// is "nothing changed" — which is the point: every search reaches the online
	// index, and the usual reach costs an empty response.
	got, err := c.Search(ctx, "plex", 10)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if len(got) != 1 || got[0].Name != "plex" {
		t.Fatalf("the second search found %v, want plex from the remembered index", names(got))
	}
	if n := hits.Load(); n != 2 {
		t.Fatalf("the index was asked for %d time(s), want 2 — every search revalidates", n)
	}
}

func TestCatalogueSaysWhenItCannotBeReached(t *testing.T) {
	srv, _ := catalogueServer(t)
	c := newTestCatalogue(srv)
	srv.Close()

	_, err := c.Search(context.Background(), "plex", 10)
	if !errors.Is(err, ErrCatalogueUnavailable) {
		t.Fatalf("a search with no network answered %v, want ErrCatalogueUnavailable", err)
	}
}

func TestCatalogueFetchesThePngForAName(t *testing.T) {
	srv, _ := catalogueServer(t)
	c := newTestCatalogue(srv)

	data, err := c.Fetch(context.Background(), "pi-hole")
	if err != nil {
		t.Fatalf("fetching a picture: %v", err)
	}
	if string(data) != "picture of pi-hole" {
		t.Fatalf("the picture is %q", data)
	}

	if _, err := c.Fetch(context.Background(), "missing"); err == nil {
		t.Fatal("a name with no picture was fetched as though it had one")
	}
}

func TestCatalogueRefusesANameThatIsNotOne(t *testing.T) {
	c := NewCatalogue()
	for _, bad := range []string{"", "Plex", "../etc/passwd", "a/b", "..", "-lead"} {
		if _, err := c.Fetch(context.Background(), bad); !errors.Is(err, ErrBadIconName) {
			t.Errorf("Fetch(%q) answered %v, want ErrBadIconName", bad, err)
		}
	}
}

// The catalogue's pictures are not written to this program's limits: one of them
// can be larger than the 64 KiB an icon may be. It has to arrive whole, because Fit
// is what turns it into an icon — and a file cut at the icon limit cannot be decoded
// at all, which is how "the picture is 65537 bytes" came to be an error.
func TestCatalogueHandsOverAPictureLargerThanAnIcon(t *testing.T) {
	srv, _ := catalogueServer(t)
	c := newTestCatalogue(srv)

	data, err := c.Fetch(context.Background(), "big")
	if err != nil {
		t.Fatalf("fetching a large picture: %v", err)
	}
	if len(data) != 3*MaxBytes {
		t.Fatalf("the picture arrived as %d bytes, want the whole %d", len(data), 3*MaxBytes)
	}
}
