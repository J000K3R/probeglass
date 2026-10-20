package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestMetricsUsesItsOwnReadOnlyToken covers the whole shape of the endpoint:
// off until a token is made, unreachable without it, readable with it, and off
// again when it is removed.
func TestMetricsUsesItsOwnReadOnlyToken(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodGet, "/metrics", nil, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("metrics without a token: status = %d, want 404 (body %s)",
			resp.StatusCode, readBody(t, resp))
	}
	resp.Body.Close()

	resp = h.do(http.MethodGet, "/api/metrics-token", nil, nil)
	var state struct {
		Enabled bool   `json:"enabled"`
		Token   string `json:"token"`
	}
	decode(t, resp, &state)
	if state.Enabled || state.Token != "" {
		t.Errorf("a fresh installation already has metrics enabled: %+v", state)
	}

	resp = h.do(http.MethodPut, "/api/metrics-token", nil, nil)
	decode(t, resp, &state)
	if !state.Enabled || state.Token == "" {
		t.Fatalf("rotating the token did not produce one: %+v", state)
	}

	resp = h.do(http.MethodGet, "/metrics", nil, nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics without a credential: status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("WWW-Authenticate = %q", resp.Header.Get("WWW-Authenticate"))
	}
	resp.Body.Close()

	resp = h.do(http.MethodGet, "/metrics", nil, map[string]string{
		"Authorization": "Bearer not-the-token",
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("metrics with a wrong token: status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do(http.MethodGet, "/metrics", nil, map[string]string{
		"Authorization": "Bearer " + state.Token,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("metrics with the token: status = %d, body = %s",
			resp.StatusCode, readBody(t, resp))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want Prometheus text", ct)
	}
	body := readBody(t, resp)
	for _, want := range []string{
		"probeglass_sensor_up{",
		"probeglass_sensor_uptime_ratio{",
		"probeglass_sensor_latency_seconds{",
		"probeglass_sensor_open_incident{",
		"probeglass_incidents_open ",
		"probeglass_sensors{status=\"up\"}",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the metrics body has no %q:\n%s", want, body)
		}
	}

	// The API key is not the metrics token. Handing a scraper the admin key must
	// not work by accident.
	resp = h.do(http.MethodPost, "/api/api-key", nil, nil)
	var key struct {
		APIKey string `json:"api_key"`
	}
	decode(t, resp, &key)
	if key.APIKey == "" {
		t.Fatal("could not make an API key")
	}
	resp = h.do(http.MethodGet, "/metrics", nil, map[string]string{
		"X-API-Key": key.APIKey,
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("the administrative API key opened /metrics: status = %d", resp.StatusCode)
	}
	resp.Body.Close()

	resp = h.do(http.MethodDelete, "/api/metrics-token", nil, nil)
	resp.Body.Close()
	resp = h.do(http.MethodGet, "/metrics", nil, map[string]string{
		"Authorization": "Bearer " + state.Token,
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("metrics after the token was removed: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}
