// Package web embeds the dashboard so the binary stays a single file.
//
// The frontend is plain ES modules and hand-written SVG: no bundler, no npm, no
// CDN. That keeps the Docker build a single-stage Go compile and lets the whole
// dashboard work on a LAN with no internet access.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"sync"
)

//go:embed assets
var embedded embed.FS

// Assets returns the dashboard files rooted at the assets directory, so that
// "index.html", "app.js" and friends resolve directly.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		// Unreachable: the go:embed directive above fails the build if the
		// directory is missing.
		panic("web: embedded assets are incomplete: " + err.Error())
	}
	return sub
}

// Fingerprints is the ETag of every embedded file, keyed by its path inside the
// assets directory.
//
// It exists because http.FileServer can produce no validator for an embed.FS:
// there is no modification time to build a Last-Modified or an ETag from, so a
// browser is free to reuse whatever it has and free to re-fetch whatever it has
// not — which, with a cache-control of no-cache, means every page load downloads
// the whole dashboard. The coastline, the borders and the place names are most of
// a megabyte between them now, and none of it changes while the process runs: the
// bytes are in the binary.
//
// The digest is therefore the file's identity, and it is computed here, in the
// process, rather than by a build step. That keeps `go build` the whole build, and
// it means a fingerprint can never describe a file other than the one being
// served. It is computed once per process: an fs.FS rooted in an embed.FS is
// readonly, so the digests cannot go stale.
func Fingerprints() map[string]string {
	fingerprintsOnce.Do(func() {
		assets := Assets()
		digests := make(map[string]string, 16)
		err := fs.WalkDir(assets, ".", func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(assets, path)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			// Quoted, because that is the syntax of the header it goes into; the
			// strong form, because these bytes are exactly what was hashed.
			digests[path] = `"` + hex.EncodeToString(sum[:]) + `"`
			return nil
		})
		if err != nil {
			// Unreachable for the same reason as in Assets: a failure here means
			// the embedded copy of the dashboard is unreadable, and a dashboard
			// without its files is not something to start up and then explain.
			panic("web: cannot fingerprint the embedded assets: " + err.Error())
		}
		fingerprints = digests
	})
	return fingerprints
}

var (
	fingerprintsOnce sync.Once
	fingerprints     map[string]string
)
