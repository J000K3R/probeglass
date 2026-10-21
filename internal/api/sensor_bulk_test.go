package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
)

// TestSensorTemplatesAreServed checks that the catalogue reaches the browser in
// the shape it draws: an id, the words as translation keys, and checks that are
// ordinary sensors with options.
func TestSensorTemplatesAreServed(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodGet, "/api/sensor-templates", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("templates: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Templates []struct {
			ID     string `json:"id"`
			Name   string `json:"name_key"`
			Checks []struct {
				Key     string           `json:"key"`
				Label   string           `json:"label_key"`
				Type    model.SensorType `json:"type"`
				Options model.Options    `json:"options"`
			} `json:"checks"`
		} `json:"templates"`
	}
	decode(t, resp, &out)

	if len(out.Templates) < 5 {
		t.Fatalf("the catalogue has %d template(s), want at least the five built in", len(out.Templates))
	}
	seen := map[string]bool{}
	for _, template := range out.Templates {
		seen[template.ID] = true
		if template.Name == "" || len(template.Checks) == 0 {
			t.Errorf("template %q is missing a name or checks: %+v", template.ID, template)
		}
		for _, check := range template.Checks {
			if check.Label == "" {
				t.Errorf("%s/%s has no label key", template.ID, check.Key)
			}
		}
	}
	for _, want := range []string{"generic", "qnap", "synology", "unifi", "apc-ups"} {
		if !seen[want] {
			t.Errorf("the catalogue has no %q template", want)
		}
	}
}

// TestBulkCreateIsAllOrNothing applies two sensors at once and then tries a batch
// with one bad entry; the good batch lands, the bad one leaves nothing behind.
func TestBulkCreateIsAllOrNothing(t *testing.T) {
	h := newHarness(t)
	h.setup()

	before := h.sensorCount(t)
	body := map[string]any{"sensors": []map[string]any{
		{
			"name": "NAS uptime", "type": "snmp", "target": "192.0.2.10",
			"interval": 300, "timeout": 10, "retries": 1,
			"options": map[string]any{
				"version": "3", "username": "monitor", "security_level": "authPriv",
				"auth_protocol": "SHA-256", "auth_pass": "auth-secret",
				"priv_protocol": "AES-256", "priv_pass": "priv-secret",
				"oid": "1.3.6.1.2.1.1.3.0", "min": 1,
			},
		},
		{
			"name": "NAS is reachable", "type": "ping", "target": "192.0.2.10",
			"interval": 60, "timeout": 10, "retries": 1,
		},
	}}
	resp := h.do(http.MethodPost, "/api/sensors/bulk", body, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bulk create: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var created struct {
		Sensors []model.Sensor `json:"sensors"`
	}
	decode(t, resp, &created)
	if len(created.Sensors) != 2 {
		t.Fatalf("bulk create returned %d sensor(s), want 2", len(created.Sensors))
	}
	if got := h.sensorCount(t); got != before+2 {
		t.Errorf("the installation has %d sensors after the batch, want %d", got, before+2)
	}

	// The response masks the v3 credentials just like a single create does.
	for _, c := range created.Sensors {
		if c.Name != "NAS uptime" {
			continue
		}
		if c.Options["auth_pass"] != notify.StoredMarker || c.Options["priv_pass"] != notify.StoredMarker {
			t.Errorf("the v3 passphrases were not masked: %v", c.Options)
		}
		if c.Options["username"] != "monitor" {
			t.Errorf("the v3 username was lost: %v", c.Options)
		}
	}

	// One invalid entry must take the whole batch with it, including the valid one
	// before it.
	bad := map[string]any{"sensors": []map[string]any{
		{"name": "Would be fine", "type": "ping", "target": "192.0.2.11", "interval": 60},
		{"name": "Bad SNMP", "type": "snmp", "target": "192.0.2.11", "interval": 60,
			"options": map[string]any{"version": "3", "oid": "1.3.6.1.2.1.1.3.0", "min": 1}},
	}}
	resp = h.do(http.MethodPost, "/api/sensors/bulk", bad, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an invalid batch: status = %d, want 400", resp.StatusCode)
	}
	if msg := readBody(t, resp); !strings.Contains(msg, "username is required") {
		t.Errorf("the error does not name the bad sensor's problem: %s", msg)
	}
	if got := h.sensorCount(t); got != before+2 {
		t.Errorf("the invalid batch wrote something: %d sensors, want %d", got, before+2)
	}
}

func (h *harness) sensorCount(t *testing.T) int {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/sensors", nil, nil)
	var sensors []model.Sensor
	decode(t, resp, &sensors)
	return len(sensors)
}
