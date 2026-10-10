package main

import (
	_ "embed"
	"fmt"
	"strings"
)

// The two files that are generated rather than copied. They sit beside this
// command rather than in the dashboard, because they are the demo's and not the
// product's.
//
//go:embed shim.js
var shimJS string

//go:embed demo.css
var demoCSS string

// docsURL and repoURL are what the demo bar links back to.
const (
	docsURL = "https://j000k3r.github.io/weatherglass-docs/"
	repoURL = "https://github.com/j000k3r/weatherglass"
)

// bar is the strip the demo wears above the dashboard.
//
// It says three things and no more: that this is a demo, that the settings in it
// last no longer than the tab, and where to go to read about or run the real
// thing. Everything under it is the product, so the sentence has to be here
// rather than sprinkled through the page.
//
// The links are relative and absolute respectively on purpose: "../" is the site
// the demo was copied into, which this command cannot know, and the other two are
// fixed addresses. It is injected as the first element of <body>, above the boot
// spinner and the sign-in card, so it is on screen even when the page is still
// loading.
const bar = `<div class="demo-bar" role="note">
  <span class="demo-tag">Demo</span>
  <span class="demo-text"><strong>Weatherglass with example sensors.</strong>
    This is a recording of the real dashboard, not a live installation. Anything you
    change here applies to this tab only and is not saved.</span>
  <span class="demo-links">
    <a href="../">Website</a>
    <a href="` + docsURL + `">Documentation</a>
    <a href="` + repoURL + `">GitHub</a>
  </span>
</div>
`

// rewriteShell turns the dashboard's index.html into the demo's.
//
// Two changes, and they are the whole of it. The five references to /assets/…
// become relative, because the demo is served from a subdirectory of somebody's
// website where a path beginning with a slash leaves the demo and lands on
// whatever is at the top of that site. And the bar, its styles and the two scripts
// are added.
//
// The scripts are classic and sit in the head; app.js and watchdog.js are modules
// at the end of the body, and a classic script always runs before a module does.
// That ordering is the only thing that lets the shim replace fetch before anything
// uses it, and it is why the shim does not need to be a module too.
func rewriteShell(shell string) (string, error) {
	if strings.Count(shell, `"/assets/`) != 6 {
		return "", fmt.Errorf("index.html names /assets/ %d time(s), not the six expected: "+
			"the dashboard has changed shape and this rewrite is no longer complete",
			strings.Count(shell, `"/assets/`))
	}
	shell = strings.ReplaceAll(shell, `"/assets/`, `"assets/`)

	head := `<!-- The demo's own three files. They are the only thing on this page that
     the product does not serve: the answers recorded while it was built, the shim
     that hands them out, and the strip that says what this is. Everything else is
     internal/web/assets, copied unmodified. -->
<link rel="stylesheet" href="demo.css">
<script src="fixtures.js"></script>
<script src="shim.js"></script>
`
	shell = strings.Replace(shell, "</head>", head+"</head>", 1)
	shell = strings.Replace(shell, "<body>\n", "<body>\n"+bar, 1)

	// The title in a tab full of tabs. The product's own title is just the name.
	shell = strings.Replace(shell, "<title>Weatherglass</title>",
		"<title>Weatherglass — demo</title>", 1)
	return shell, nil
}
