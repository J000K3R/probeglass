package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/j000k3r/weatherglass/internal/web"
)

// The dashboard is served from the binary, so every page load asks for the same
// bytes the browser already has. These tests are about the answer to that: an
// ETag and a 304, or the file itself when the browser's copy is not the current
// one. Nothing here is about the file server — it is about the validator in front
// of it, which is the part that can be wrong in a way nobody sees.

// assetsServer builds a server over an in-memory dashboard and its fingerprints,
// the way main wires the embedded one.
//
// The digests are computed here rather than taken from web.Fingerprints, because
// these files are not the shipped ones: what is under test is what the server does
// with a map of validators, and a test that asked the package for its own idea of
// the digests could not tell a wrong lookup from a wrong digest.
func assetsServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	h := newHarness(t)

	assets := fstest.MapFS{}
	etags := make(map[string]string, len(files))
	for name, body := range files {
		assets[name] = &fstest.MapFile{Data: []byte(body)}
		sum := sha256.Sum256([]byte(body))
		etags[name] = `"` + hex.EncodeToString(sum[:]) + `"`
	}

	rebuilt, err := New(context.Background(), Options{
		Config: h.server.cfg, Store: h.store, Scheduler: h.server.sched,
		Dispatcher: h.server.dispatcher, Broker: h.server.broker, Logger: h.server.log,
		Assets: assets, AssetETags: etags,
	})
	if err != nil {
		t.Fatalf("rebuild server with assets: %v", err)
	}
	ts := httptest.NewServer(rebuilt.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// fetchAsset asks for one dashboard file, with whatever request headers the case
// is about, and hands back the response and its body.
func fetchAsset(t *testing.T, ts *httptest.Server, path string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read body of %s: %v", path, err)
	}
	return resp, string(body)
}

// TestAnAssetCarriesAValidator is the first half of the arrangement: the browser
// can only send back an ETag it was given.
func TestAnAssetCarriesAValidator(t *testing.T) {
	const body = "export const nothing = true;\n"
	ts := assetsServer(t, map[string]string{"app.js": body})

	resp, got := fetchAsset(t, ts, "/assets/app.js", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got != body {
		t.Errorf("body = %q, want the file itself %q", got, body)
	}
	sum := sha256.Sum256([]byte(body))
	want := `"` + hex.EncodeToString(sum[:]) + `"`
	if tag := resp.Header.Get("ETag"); tag != want {
		t.Errorf("ETag = %q, want %q — the digest of the bytes in the response", tag, want)
	}
	// Still no-cache: an upgraded binary must not be paired with the dashboard
	// from the build before it. The validator is what makes that cheap, not a
	// lifetime.
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "no-cache") {
		t.Errorf("Cache-Control = %q, want it to say no-cache", cc)
	}
}

// TestASecondLoadOfAnAssetCostsNoBody is the other half, and the reason the
// header is there at all: the map, its borders and its place names are most of a
// megabyte, and a reload that changes nothing should not carry it again.
func TestASecondLoadOfAnAssetCostsNoBody(t *testing.T) {
	const body = "export const coastline = [];\n"
	ts := assetsServer(t, map[string]string{"app.js": body})

	first, _ := fetchAsset(t, ts, "/assets/app.js", nil)
	tag := first.Header.Get("ETag")
	if tag == "" {
		t.Fatalf("no ETag on the first response, so there is nothing for a browser to send back")
	}

	resp, got := fetchAsset(t, ts, "/assets/app.js", map[string]string{"If-None-Match": tag})
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("status = %d, want 304", resp.StatusCode)
	}
	if got != "" {
		t.Errorf("the 304 carried %d bytes of body, want none:\n%s", len(got), got)
	}
	// A 304 has to carry the validator again: a cache that got a bodyless answer
	// without one would have nothing to revalidate against next time.
	if again := resp.Header.Get("ETag"); again != tag {
		t.Errorf("ETag on the 304 = %q, want %q", again, tag)
	}
}

// TestAStaleCopyStillGetsTheFile is the half that has to keep working: after an
// upgrade the browser's validator is the old file's, and the answer has to be the
// new file rather than a shrug.
func TestAStaleCopyStillGetsTheFile(t *testing.T) {
	const body = "export const version = 2;\n"
	ts := assetsServer(t, map[string]string{"app.js": body})

	for _, header := range []string{
		`"a-tag-from-the-build-before-this-one"`,
		`W/"a-tag-from-the-build-before-this-one"`,
		"",
	} {
		resp, got := fetchAsset(t, ts, "/assets/app.js", map[string]string{"If-None-Match": header})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("If-None-Match: %q gave status %d, want 200", header, resp.StatusCode)
			continue
		}
		if got != body {
			t.Errorf("If-None-Match: %q got %q, want the file %q", header, got, body)
		}
	}
}

// TestIfNoneMatchIsAList covers the header's actual syntax rather than the single
// value a browser sends most of the time. Both shapes arrive in practice: a page
// that loaded several files can be revalidated as one, and `*` is what a client
// sends when it wants the file only if it is new at all.
func TestIfNoneMatchIsAList(t *testing.T) {
	const body = "export const borders = [];\n"
	ts := assetsServer(t, map[string]string{"app.js": body})

	first, _ := fetchAsset(t, ts, "/assets/app.js", nil)
	tag := first.Header.Get("ETag")
	sum := sha256.Sum256([]byte(body))
	if tag != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Fatalf("ETag = %q, which is not this file's digest", tag)
	}

	cases := []struct {
		header string
		want   int
		why    string
	}{
		{tag, http.StatusNotModified, "the tag on its own"},
		{" " + tag + " ", http.StatusNotModified, "the tag with whitespace around it"},
		{`"something-else", ` + tag, http.StatusNotModified, "the tag second in a list"},
		{tag + `, "something-else"`, http.StatusNotModified, "the tag first in a list"},
		{`W/` + tag, http.StatusNotModified, "the tag handed back weakened"},
		{"*", http.StatusNotModified, "the placeholder, which means any version"},
		{`"something-else", "and-another"`, http.StatusOK, "a list without the tag in it"},
		{"", http.StatusOK, "no header at all"},
	}
	for _, c := range cases {
		resp, _ := fetchAsset(t, ts, "/assets/app.js", map[string]string{"If-None-Match": c.header})
		if resp.StatusCode != c.want {
			t.Errorf("If-None-Match: %q (%s) gave status %d, want %d",
				c.header, c.why, resp.StatusCode, c.want)
		}
	}
}

// TestAnUnknownAssetPathIsLeftToTheFileServer keeps the wrapper from inventing an
// answer for a path it has no digest for. The 404 is the file server's, and it has
// to stay a 404.
func TestAnUnknownAssetPathIsLeftToTheFileServer(t *testing.T) {
	ts := assetsServer(t, map[string]string{"app.js": "export const nothing = true;\n"})

	resp, _ := fetchAsset(t, ts, "/assets/not-a-file.js", map[string]string{"If-None-Match": "*"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d for a path that is not there, want 404", resp.StatusCode)
	}
}

// TestEveryShippedAssetHasAValidator is the one test here that uses the real
// embedded dashboard: the wrapper only ever sees the map it is handed, so a file
// missing from that map would be re-downloaded on every page load and nothing else
// would say so.
func TestEveryShippedAssetHasAValidator(t *testing.T) {
	h := newHarness(t)
	assets := web.Assets()
	etags := web.Fingerprints()

	rebuilt, err := New(context.Background(), Options{
		Config: h.server.cfg, Store: h.store, Scheduler: h.server.sched,
		Dispatcher: h.server.dispatcher, Broker: h.server.broker, Logger: h.server.log,
		Assets: assets, AssetETags: etags,
	})
	if err != nil {
		t.Fatalf("rebuild server with the embedded dashboard: %v", err)
	}
	ts := httptest.NewServer(rebuilt.Handler())
	t.Cleanup(ts.Close)

	err = fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		tag := etags[path]
		if tag == "" {
			t.Errorf("%s has no ETag, so every page load downloads it again", path)
			return nil
		}
		// The shell is not fetched under /assets/: the file server redirects that
		// path to the directory, and the dashboard asks for "/" instead — see
		// handleIndex, which refuses to store it for the same reason. Its digest
		// is in the map all the same, because the map is the whole tree.
		if path == "index.html" {
			return nil
		}
		resp, body := fetchAsset(t, ts, "/assets/"+path, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET /assets/%s = %d, want 200", path, resp.StatusCode)
			return nil
		}
		if got := resp.Header.Get("ETag"); got != tag {
			t.Errorf("ETag of %s = %q, want %q", path, got, tag)
		}
		sum := sha256.Sum256([]byte(body))
		if tag != `"`+hex.EncodeToString(sum[:])+`"` {
			t.Errorf("the ETag of %s is not the digest of what was served, so a browser "+
				"would keep a stale copy of it", path)
		}
		resp, again := fetchAsset(t, ts, "/assets/"+path, map[string]string{"If-None-Match": tag})
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("revalidating %s = %d, want 304", path, resp.StatusCode)
		}
		if again != "" {
			t.Errorf("revalidating %s carried %d bytes of body", path, len(again))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the embedded dashboard: %v", err)
	}
}
