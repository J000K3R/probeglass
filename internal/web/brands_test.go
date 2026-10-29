package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/catalog"
)

// TestEveryBrandMarkHasAFile holds the catalogue's brand references to the assets
// that are actually shipped. A template naming a mark that is not there draws an
// empty square in the picker, which reads as a broken icon rather than a missing
// file.
func TestEveryBrandMarkHasAFile(t *testing.T) {
	for _, template := range catalog.Templates() {
		if !strings.HasPrefix(template.Icon, "brand:") {
			continue
		}
		name := strings.TrimPrefix(template.Icon, "brand:")
		found := false
		for _, ext := range []string{".svg", ".png"} {
			if _, err := os.Stat(filepath.Join("assets", "brands", name+ext)); err == nil {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s wears brand:%s, which has no file in assets/brands", template.ID, name)
		}
	}
	// The old marks stay, because sensors created from the retired templates still
	// carry those references; the new one must be there for the template.
	for _, name := range []string{"seerr.svg", "overseerr.svg", "jellyseerr.svg"} {
		if _, err := os.Stat(filepath.Join("assets", "brands", name)); err != nil {
			t.Errorf("assets/brands/%s is missing: %v", name, err)
		}
	}
}
