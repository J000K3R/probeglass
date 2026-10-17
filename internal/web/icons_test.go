package web

import (
	"io/fs"
	"path"
	"sort"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
)

// The built-in drawings are files under assets/icons, and the names the API offers
// are a list in internal/icon. Two lists, one feature, and nothing in Go holds them
// together — which is exactly the shape of mistake that goes unnoticed, because
// neither half of it is wrong on its own: a name without a file is an entry in the
// picker that draws nothing, and a file without a name is a drawing nobody can
// choose.
//
// So the test runs in both directions, the way the language dictionary test does.
func TestBuiltinIconsMatchTheFiles(t *testing.T) {
	files := map[string]bool{}
	err := fs.WalkDir(Assets(), "icons", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := strings.TrimSuffix(path.Base(p), ".svg")
		if path.Ext(p) != ".svg" {
			t.Errorf("%s is in the icon directory and is not an SVG; every drawing "+
				"there is served as one and chosen by its name", p)
			return nil
		}
		files[name] = true
		return nil
	})
	if err != nil {
		t.Fatalf("walk the icon directory: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("there are no built-in drawings at all, so the picker offers nothing")
	}

	named := map[string]bool{}
	for _, name := range icon.Names {
		if named[name] {
			t.Errorf("internal/icon lists %q twice", name)
		}
		named[name] = true
		if !files[name] {
			t.Errorf("the picker offers %q and assets/icons/%s.svg does not exist", name, name)
		}
	}
	var orphans []string
	for name := range files {
		if !named[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)
	if len(orphans) > 0 {
		t.Errorf("assets/icons holds drawings nothing can choose: %s", strings.Join(orphans, ", "))
	}
}

// TestEveryBuiltinIconHasAName covers the one lookup the catalogue test cannot see.
//
// The picker builds its labels as t(`icons.name.${name}`) and ranges over the names
// the API hands it, so a name the dictionaries have never heard of does not fail
// anywhere: t() answers with the key and the picker shows `icons.name.router` under
// the drawing. The list is fixed in Go, which is what makes it checkable at all.
func TestEveryBuiltinIconHasAName(t *testing.T) {
	for _, catalogue := range []string{"i18n.js", "i18n.de.js"} {
		keys := shippedCatalogue(t, catalogue)
		for _, name := range icon.Names {
			if _, ok := keys["icons.name."+name]; !ok {
				t.Errorf("%s has no label for the built-in icon %q, so the picker would "+
					"show the key instead of a word", catalogue, name)
			}
		}
	}
}

// TestBuiltinIconsAreDrawingsAndNotScripts holds every built-in to what a drawing is
// allowed to contain.
//
// These files are served from the dashboard's own origin, which is the one place
// where the difference between an image and a document matters: an SVG may carry
// script and external references, and a browser told to load one will oblige. The
// upload path refuses SVG for exactly this reason, and the set that ships inside the
// binary has to be at least as well behaved as what it refuses — a `<script>` or an
// `onload=` reaching one of these files would be the same hole with a shorter path
// to it.
func TestBuiltinIconsAreDrawingsAndNotScripts(t *testing.T) {
	for _, name := range icon.Names {
		src := readAsset(t, Assets(), "icons/"+name+".svg")
		lower := strings.ToLower(src)
		for _, forbidden := range []string{"<script", "javascript:", "onload", "onclick", "<foreignobject", "<use", "href="} {
			if strings.Contains(lower, forbidden) {
				t.Errorf("assets/icons/%s.svg contains %q; a built-in drawing is shapes "+
					"and nothing else", name, forbidden)
			}
		}
		// One drawing, one file, and a viewBox the dashboard can scale: the picker
		// draws them at 20 pixels and the cards at 16, and a file without a viewBox
		// has no size of its own to be scaled from.
		if !strings.Contains(lower, "viewbox=") {
			t.Errorf("assets/icons/%s.svg has no viewBox, so it cannot be scaled", name)
		}
		if !strings.Contains(lower, "currentcolor") {
			t.Errorf("assets/icons/%s.svg does not use currentColor; the dashboard "+
				"uses these as masks so that they follow the theme", name)
		}
	}
}
