package api

import (
	"net/http"
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
