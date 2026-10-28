package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestScanRefusesPublicRanges(t *testing.T) {
	h := newHarness(t)
	h.setup()

	for _, cidr := range []string{"", "8.8.8.0/24", "10.0.0.0/8"} {
		resp := h.do(http.MethodPost, "/api/scan", map[string]any{"cidr": cidr}, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("scan %q: status = %d, want 400", cidr, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp := h.do(http.MethodPost, "/api/scan", map[string]any{"cidr": "127.0.0.1/32"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback scan: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if body := readBody(t, resp); !strings.Contains(body, `"hosts"`) {
		t.Errorf("the scan answer has no hosts: %s", body)
	}
}
