package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/j000k3r/probeglass/internal/model"
)

// TestThePublicStatusPageShowsOnlyWhatWasPublished walks the whole feature as an
// outsider would meet it: an administrator turns the link on and ticks one
// sensor, and the page that answers without any credential carries that sensor
// and neither the unpublished one nor internal addresses.
func TestThePublicStatusPageShowsOnlyWhatWasPublished(t *testing.T) {
	h := newHarness(t)
	h.setup()

	makeSensor := func(name, kind, target string) model.Sensor {
		t.Helper()
		resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
			"name": name, "type": kind, "target": target,
			"interval": 60, "timeout": 5, "retries": 1,
		}, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status = %d, body = %s", name, resp.StatusCode, readBody(t, resp))
		}
		var c model.Sensor
		decode(t, resp, &c)
		return c
	}
	// A real target, so the probe that follows gives the page a status to draw.
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer site.Close()
	published := makeSensor("Website", "http", site.URL)
	if resp := h.do(http.MethodPost, "/api/sensors/"+itoa(published.ID)+"/probe", nil, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("probe: status = %d", resp.StatusCode)
	}
	makeSensor("Internal NAS", "ping", "10.0.0.55")

	resp := h.do(http.MethodPut, "/api/status-token", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enable the page: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var state statusPageResponse
	decode(t, resp, &state)
	if !state.Enabled || state.Token == "" {
		t.Fatalf("enabling returned %+v", state)
	}

	resp = h.do(http.MethodPut, "/api/status-page", map[string]any{
		"config": map[string]any{
			"title":          "Homelab",
			"layout":         "cards",
			"logo":           "builtin:server",
			"background_url": "https://example.org/bg.jpg",
			"background_dim": 40,
			"notices": []map[string]any{{
				"text": "Maintenance tonight", "colour": "amber", "icon": "builtin:clock",
			}},
			"sections": []map[string]any{{
				"title":   "Web",
				"icon":    "builtin:globe",
				"sensors": []map[string]any{{"id": published.ID}},
			}},
		},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save the page: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	decode(t, resp, &state)
	if ids := state.Config.SensorIDs(); len(ids) != 1 || ids[0] != published.ID {
		t.Errorf("the saved selection is %v, want [%d]", ids, published.ID)
	}
	if state.Config.Title != "Homelab" || state.Config.Layout != "cards" || state.Config.Sections[0].Title != "Web" {
		t.Errorf("the page came back as %+v", state.Config)
	}

	// Asked without the harness cookie: this is the whole point of the page.
	public := func(path string, headers map[string]string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, h.http.URL+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := h.http.Client().Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	status, body := public("/status/"+state.Token, nil)
	if status != http.StatusOK {
		t.Fatalf("the page answered %d, want 200", status)
	}
	for _, want := range []string{"Website", "Operational"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not name %q", want)
		}
	}
	for _, unwanted := range []string{"Internal NAS", "10.0.0.55", site.URL} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the page leaks %q", unwanted)
		}
	}
	for _, want := range []string{"Homelab", "Web", "Maintenance tonight", `class="notice c-amber"`,
		"/assets/icons/clock.svg", "/assets/icons/server.svg", "/assets/icons/globe.svg",
		"https://example.org/bg.jpg", "--bg-dim:40",
		"https://j000k3r.github.io/probeglass-website/"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
	// The outsider's copy has no way into the dashboard.
	if strings.Contains(body, "/#/settings/monitor/status") {
		t.Error("the page offers its editor to somebody who is not signed in")
	}

	// The administrator's own request carries the session cookie, and with it the
	// link back to the panel.
	req, err := http.NewRequest(http.MethodGet, h.http.URL+"/status/"+state.Token, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.AddCookie(h.cookie)
	adminResp, err := h.http.Client().Do(req)
	if err != nil {
		t.Fatalf("admin page: %v", err)
	}
	adminBody, _ := io.ReadAll(adminResp.Body)
	adminResp.Body.Close()
	for _, want := range []string{"/#/settings/monitor/status", "Edit page"} {
		if !strings.Contains(string(adminBody), want) {
			t.Errorf("the administrator's page does not carry %q", want)
		}
	}
	if !strings.Contains(body, `name="robots" content="noindex, nofollow"`) {
		t.Error("the page does not keep search engines away")
	}

	// A wrong token and a page that is off look the same from outside.
	if status, _ := public("/status/wrong-token", nil); status != http.StatusNotFound {
		t.Errorf("a wrong token answered %d, want 404", status)
	}

	// The reader's browser language decides the words; nothing is stored.
	_, german := public("/status/"+state.Token, map[string]string{"Accept-Language": "de-DE,de;q=0.9,en;q=0.8"})
	if !strings.Contains(german, "Aktualisiert") || !strings.Contains(german, "Betriebsbereit") {
		t.Error("a German browser did not get the German words")
	}

	// Switching the page off revokes the link but keeps the selection.
	resp = h.do(http.MethodDelete, "/api/status-token", nil, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disable the page: status = %d", resp.StatusCode)
	}
	if status, _ := public("/status/"+state.Token, nil); status != http.StatusNotFound {
		t.Errorf("a disabled page still answered %d", status)
	}
	resp = h.do(http.MethodGet, "/api/status-page", nil, nil)
	decode(t, resp, &state)
	if state.Enabled || len(state.Config.SensorIDs()) != 1 {
		t.Errorf("after disabling: enabled=%v, selection=%v", state.Enabled, state.Config.SensorIDs())
	}
}

// TestTheStatusPageRefusesUnknownSensors keeps a typo in the panel from
// publishing nothing and saying it worked.
func TestTheStatusPageRefusesUnknownSensors(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPut, "/api/status-page", map[string]any{
		"config": map[string]any{
			"sections": []map[string]any{{"sensors": []map[string]any{{"id": 9999}}}},
		},
	}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown sensor: status = %d, want 400", resp.StatusCode)
	}
	if msg := readBody(t, resp); !strings.Contains(msg, "9999") {
		t.Errorf("the error does not name the sensor: %s", msg)
	}
}

// TestLogPathHidesTheStatusToken is the one line that keeps the share token out
// of a log file. It is a small function with a large consequence, so it is
// checked on its own rather than through a request.
func TestLogPathHidesTheStatusToken(t *testing.T) {
	if got := logPath("/status/abc123"); got != "/status/…" {
		t.Errorf("logPath of a status URL = %q, want the token replaced", got)
	}
	for _, path := range []string{"/api/sensors", "/status", "/statuses"} {
		if got := logPath(path); got != path {
			t.Errorf("logPath(%q) = %q, want it unchanged", path, got)
		}
	}
	if got := logPath("/status/"); got != "/status/…" {
		t.Errorf("logPath of an empty token = %q, want it scrubbed too", got)
	}
}

// TestThePublicPageServesOnlyItsOwnIcons covers the one public route that hands
// out bytes: an uploaded symbol reaches the page through the share token, and
// only while a sensor on the page wears it.
func TestThePublicPageServesOnlyItsOwnIcons(t *testing.T) {
	h := newHarness(t)
	h.setup()

	published := mustUpload(t, h, "published")
	private := mustUpload(t, h, "private")

	makeSensor := func(name, icon string) model.Sensor {
		t.Helper()
		resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
			"name": name, "type": "ping", "target": "192.0.2.20", "icon": icon,
			"interval": 60, "timeout": 5, "retries": 1,
		}, nil)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status = %d, body = %s", name, resp.StatusCode, readBody(t, resp))
		}
		var c model.Sensor
		decode(t, resp, &c)
		return c
	}
	shown := makeSensor("Shown", "upload:"+itoa(published.ID))
	makeSensor("Hidden", "upload:"+itoa(private.ID))

	resp := h.do(http.MethodPut, "/api/status-token", nil, nil)
	var state statusPageResponse
	decode(t, resp, &state)
	resp = h.do(http.MethodPut, "/api/status-page", map[string]any{
		"config": map[string]any{
			"sections": []map[string]any{{"title": "Services", "sensors": []map[string]any{{"id": shown.ID}}}},
		},
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}

	get := func(path string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, h.http.URL+path, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		resp, err := h.http.Client().Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}

	page, body := get("/status/" + state.Token)
	if page.StatusCode != http.StatusOK {
		t.Fatalf("page: status = %d", page.StatusCode)
	}
	wantIcon := "/status/" + state.Token + "/icon/" + itoa(published.ID)
	if !strings.Contains(string(body), wantIcon) {
		t.Errorf("the page does not point at its published icon %q", wantIcon)
	}

	iconResp, iconBytes := get(wantIcon)
	if iconResp.StatusCode != http.StatusOK {
		t.Fatalf("published icon: status = %d", iconResp.StatusCode)
	}
	if len(iconBytes) == 0 || !strings.HasPrefix(iconResp.Header.Get("Content-Type"), "image/") {
		t.Errorf("icon: %d bytes, type %q", len(iconBytes), iconResp.Header.Get("Content-Type"))
	}

	// An image of a sensor nobody published, and a valid image under a wrong
	// token, are both simply not there.
	if resp, _ := get("/status/" + state.Token + "/icon/" + itoa(private.ID)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unpublished icon answered %d", resp.StatusCode)
	}
	if resp, _ := get("/status/wrong/icon/" + itoa(published.ID)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a wrong token answered %d for an icon", resp.StatusCode)
	}
}
