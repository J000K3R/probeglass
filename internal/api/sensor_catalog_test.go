package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestTemplateCheckRefusesWhatItCannotAsk covers the two answers that need no
// device: an unknown template and a missing address. A real check is exercised by
// the sensor package's discovery test.
func TestTemplateCheckRefusesWhatItCannotAsk(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/api/sensor-templates/nope/check",
		map[string]any{"target": "192.0.2.10"}, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown template: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do(http.MethodPost, "/api/sensor-templates/qnap/check",
		map[string]any{"target": "  "}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing target: status = %d, want 400", resp.StatusCode)
	}
	if body := readBody(t, resp); !strings.Contains(body, "target is required") {
		t.Errorf("missing target answered %q", body)
	}
}

// TestTemplateCheckRunsAnApplicationCheck exercises the non-SNMP half: a web
// template answers by actually requesting its first check.
func TestTemplateCheckRunsAnApplicationCheck(t *testing.T) {
	h := newHarness(t)
	h.setup()

	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer web.Close()

	resp := h.do(http.MethodPost, "/api/sensor-templates/shelly/check",
		map[string]any{"target": strings.TrimPrefix(web.URL, "http://")}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("application check: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Device struct {
			SysDescr string `json:"sys_descr"`
		} `json:"device"`
		Result struct {
			Success bool `json:"success"`
		} `json:"result"`
	}
	decode(t, resp, &out)
	if !out.Result.Success {
		t.Errorf("the application check failed: %+v", out)
	}
	if !strings.Contains(out.Device.SysDescr, "200") {
		t.Errorf("the check answered %q, want the HTTP status", out.Device.SysDescr)
	}
}

func TestTemplateOrderIsAPerAccountPreference(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"template_order": "qnap,sonarr,generic"}}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid order: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	var settings struct {
		UI map[string]string `json:"ui"`
	}
	decode(t, resp, &settings)
	if got := settings.UI["template_order"]; got != "qnap,sonarr,generic" {
		t.Errorf("template_order = %q, want the saved order", got)
	}

	for _, bad := range []string{"QNAP", "qnap,qnap", "qnap,../etc"} {
		resp = h.do(http.MethodPut, "/api/settings",
			map[string]any{"ui": map[string]string{"template_order": bad}}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("order %q: status = %d, want 400", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}
}
