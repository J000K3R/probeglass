package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// TestAProbePutsItsCertificateInTheInventory runs the whole path once: an HTTPS
// sensor, a real handshake with a real certificate, and the inventory page's
// request. What it pins is that the page is fed by probes rather than by a second
// handshake of its own — the certificate the sensor saw is the certificate that
// is listed, subject and dates included.
func TestAProbePutsItsCertificateInTheInventory(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// httptest's certificate is for example.com and expires in a year. It is not
	// trusted by this machine, so the sensor skips verification — which is also
	// the homelab case the inventory is most useful in.
	web := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer web.Close()

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "Self-signed site", "type": "http", "target": web.URL,
		"interval": 300, "timeout": 10, "retries": 1,
		"options": map[string]any{"insecure_skip_verify": true, "cert_warn_days": 30},
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var sensor model.Sensor
	decode(t, resp, &sensor)

	resp = h.do(http.MethodPost, "/api/sensors/"+itoa(sensor.ID)+"/probe", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}

	resp = h.do(http.MethodGet, "/api/certificates", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("certificates: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Certificates []model.Certificate `json:"certificates"`
	}
	decode(t, resp, &out)

	if len(out.Certificates) != 1 {
		t.Fatalf("the inventory has %d row(s), want 1: %+v", len(out.Certificates), out.Certificates)
	}
	c := out.Certificates[0]
	if c.SensorID != sensor.ID || c.SensorName != sensor.Name || c.Target != web.URL {
		t.Errorf("the row does not name its sensor: %+v", c)
	}
	if c.Subject == "" || c.Issuer == "" || c.Fingerprint == "" {
		t.Errorf("the certificate was stored without its identity: %+v", c)
	}
	if !c.NotAfter.After(time.Now()) || c.DaysLeft < 300 {
		t.Errorf("the expiry looks wrong: not_after = %s, days_left = %d", c.NotAfter, c.DaysLeft)
	}
	if c.WarnDays != 30 {
		t.Errorf("warn_days = %d, want the sensor's 30", c.WarnDays)
	}
	if c.TLSVersion == "" {
		t.Error("the handshake's TLS version was not recorded")
	}

	// A sensor that never got a certificate has no row of its own: plain HTTP is
	// a fact about the target, not a certificate with blank fields.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer plain.Close()
	resp = h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "Plain site", "type": "http", "target": plain.URL,
		"interval": 300, "timeout": 10, "retries": 1,
	}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create plain sensor: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var plainSensor model.Sensor
	decode(t, resp, &plainSensor)
	if resp := h.do(http.MethodPost, "/api/sensors/"+itoa(plainSensor.ID)+"/probe", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("plain probe: status = %d", resp.StatusCode)
	}
	resp = h.do(http.MethodGet, "/api/certificates", nil, nil)
	decode(t, resp, &out)
	if len(out.Certificates) != 1 {
		t.Errorf("plain HTTP added a row: %+v", out.Certificates)
	}
}
