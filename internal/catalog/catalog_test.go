package catalog

import (
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/icon"
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
		if template.Icon != "" {
			if err := icon.Check(template.Icon); err != nil {
				t.Errorf("%s: %v", template.ID, err)
			}
		}
		checkKeys := map[string]bool{}
		for _, check := range template.Checks {
			if check.Key == "" || checkKeys[check.Key] {
				t.Errorf("%s: check key %q is empty or repeated", template.ID, check.Key)
			}
			checkKeys[check.Key] = true
			if !strings.HasPrefix(check.LabelKey, "catalog.template.") && !strings.HasPrefix(check.LabelKey, "catalog.check.") {
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
			case model.SensorHTTP:
				if check.Target == "" || !strings.Contains(check.Target, "{address}") {
					t.Errorf("%s/%s: an HTTP check needs a target pattern with {address}: %q",
						template.ID, check.Key, check.Target)
				}
				if check.Options["expected_status"] == nil {
					t.Errorf("%s/%s: an HTTP check needs an expected_status", template.ID, check.Key)
				}
			case model.SensorTCP:
				if check.Target == "" || !strings.Contains(check.Target, "{host}") {
					t.Errorf("%s/%s: a TCP check needs a target pattern with {host}: %q",
						template.ID, check.Key, check.Target)
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

// TestSeerrReplacesOverseerrAndJellyseerr pins the merge: one request-manager
// template, on the port and endpoint Seerr kept from both of its predecessors.
func TestSeerrReplacesOverseerrAndJellyseerr(t *testing.T) {
	seerr, ok := Find("seerr")
	if !ok {
		t.Fatal("the catalogue has no Seerr template")
	}
	if seerr.Vendor != "Seerr" || seerr.Icon != "brand:seerr" {
		t.Errorf("Seerr = %+v, want the Seerr vendor and mark", seerr)
	}
	var apiFound bool
	for _, check := range seerr.Checks {
		if check.Key != "api" {
			continue
		}
		apiFound = true
		if check.Target != "http://{address}:5055/api/v1/status" {
			t.Errorf("Seerr API target = %q", check.Target)
		}
		if check.Options["api_key_header"] != "X-Api-Key" {
			t.Errorf("Seerr API key header = %v", check.Options["api_key_header"])
		}
	}
	if !apiFound {
		t.Error("Seerr has no API check")
	}
	for _, old := range []string{"overseerr", "jellyseerr"} {
		if _, ok := Find(old); ok {
			t.Errorf("the retired %q template is still in the catalogue", old)
		}
	}
}
