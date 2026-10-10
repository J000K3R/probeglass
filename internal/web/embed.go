// Package web embeds the dashboard so the binary stays a single file.
//
// The frontend is plain ES modules and hand-written SVG: no bundler, no npm, no
// CDN. That keeps the Docker build a single-stage Go compile and lets the whole
// dashboard work on a LAN with no internet access.
package web

import (
	"embed"
	"io/fs"
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
