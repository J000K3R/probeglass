package web

import (
	"testing"

	"github.com/j000k3r/probeglass/internal/catalog"
)

// TestEveryTemplateHasWords is the link between the Go catalogue and the two
// dictionaries. The catalogue's keys are looked up with t() at runtime, where a
// missing key ships the key itself on screen; the catalogue's own test cannot see
// the dictionaries, and the dictionary test cannot see the catalogue.
func TestEveryTemplateHasWords(t *testing.T) {
	english := shippedCatalogue(t, "i18n.js")
	german := shippedCatalogue(t, "i18n.de.js")
	for _, template := range catalog.Templates() {
		keys := []string{template.NameKey, template.DescriptionKey}
		if template.System != "" {
			keys = append(keys, "catalog.system."+template.System)
		}
		for _, tag := range template.Tags {
			keys = append(keys, "catalog.tag."+tag)
		}
		if template.Credential != nil {
			keys = append(keys, template.Credential.LabelKey)
			if template.Credential.HelpKey != "" {
				keys = append(keys, template.Credential.HelpKey)
			}
		}
		for _, check := range template.Checks {
			keys = append(keys, check.LabelKey)
		}
		for _, key := range keys {
			if _, ok := english[key]; !ok {
				t.Errorf("template %s uses %q, which i18n.js does not have", template.ID, key)
			}
			if _, ok := german[key]; !ok {
				t.Errorf("template %s uses %q, which i18n.de.js does not have", template.ID, key)
			}
		}
	}
}
