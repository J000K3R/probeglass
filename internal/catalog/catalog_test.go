package catalog

import (
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
)

// TestTemplatesAreUsable catches the mistakes a hand-written catalogue makes:
// two templates with one id, a check with no assertion, an interval that would
// hammer a device, or a label that the interface cannot translate.
func TestTemplatesAreUsable(t *testing.T) {
	seen := map[string]bool{}
	for _, template := range Templates() {
		if template.ID == "" || seen[template.ID] {
			t.Errorf("template id %q is empty or repeated", template.ID)
		}
		seen[template.ID] = true
		if template.NameKey == "" || template.DescriptionKey == "" {
			t.Errorf("%s: a template without a name or description key", template.ID)
		}
		if len(template.Checks) == 0 {
			t.Errorf("%s: no checks", template.ID)
		}
		checkKeys := map[string]bool{}
		for _, check := range template.Checks {
			if check.Key == "" || checkKeys[check.Key] {
				t.Errorf("%s: check key %q is empty or repeated", template.ID, check.Key)
			}
			checkKeys[check.Key] = true
			if !strings.HasPrefix(check.LabelKey, "catalog.template.") {
				t.Errorf("%s/%s: label key %q is not a catalogue key", template.ID, check.Key, check.LabelKey)
			}
			if check.Interval < 30 {
				t.Errorf("%s/%s: interval %ds is too short for a device check", template.ID, check.Key, check.Interval)
			}
			switch check.Type {
			case model.SensorSNMP:
				oid, ok := check.Options["oid"].(string)
				if !ok || !strings.HasPrefix(oid, "1.3.6.1.") {
					t.Errorf("%s/%s: SNMP check without a dotted oid: %v", template.ID, check.Key, check.Options)
				}
				if check.Options["expect"] == nil && check.Options["min"] == nil && check.Options["max"] == nil {
					t.Errorf("%s/%s: SNMP check has no expect, min or max", template.ID, check.Key)
				}
			case model.SensorPing:
				if len(check.Options) != 0 {
					t.Errorf("%s/%s: a ping check carries options: %v", template.ID, check.Key, check.Options)
				}
			default:
				t.Errorf("%s/%s: unsupported template check type %q", template.ID, check.Key, check.Type)
			}
		}
	}
}
