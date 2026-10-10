package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/doctor"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// ---------------------------------------------------------------------------
// example sensors
// ---------------------------------------------------------------------------

// A fresh dashboard must not be an empty grid: the four examples are what show a
// first-time user what the product does, and one of them points at Weatherglass
// itself so that there is always something green to look at.
func TestSetupCreatesExampleSensors(t *testing.T) {
	h := newHarness(t)
	h.setup()

	sensors := h.listSensors()

	if len(sensors) != 4 {
		t.Fatalf("a fresh installation has %d sensors, want 4 examples (%v)", len(sensors), sensorNames(sensors))
	}
	byName := map[string]model.Sensor{}
	for _, s := range sensors {
		byName[s.Name] = s
		if s.Group != "Examples" {
			t.Errorf("sensor %q: group = %q, want Examples", s.Name, s.Group)
		}
		// Nobody is alerted by a default. A first run that fires a real message
		// into a chat room before anything has been configured is not a helpful
		// default.
		if len(s.Channels) != 0 {
			t.Errorf("sensor %q is attached to %d channels, want none", s.Name, len(s.Channels))
		}
		if !s.Enabled {
			t.Errorf("sensor %q is disabled, want it running", s.Name)
		}
	}

	// The four double as a tour of what can be monitored, so each type is
	// represented.
	for _, want := range []model.SensorType{model.SensorHTTP, model.SensorPing, model.SensorDNS} {
		found := false
		for _, s := range sensors {
			if s.Type == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no example sensor of type %q", want)
		}
	}

	self, ok := byName["This dashboard (example)"]
	if !ok {
		t.Fatalf("the self-monitoring example is missing; got %v", sensorNames(sensors))
	}
	want := "http://" + config.DialAddr(h.server.cfg.Listen) + "/healthz"
	if self.Target != want {
		t.Errorf("the self-monitoring example points at %q, want %q", self.Target, want)
	}
}

// Reaching /api/setup more than once must not multiply the examples.
func TestExampleSensorsAreCreatedOnce(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// A second setup is refused, and the examples must not be doubled by the
	// attempt either.
	resp := h.do(http.MethodPost, "/api/setup",
		map[string]string{"username": "someone-else", "password": "homelab123"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second setup: status = %d, want 409", resp.StatusCode)
	}

	if n := len(h.listSensors()); n != 4 {
		t.Fatalf("after a second setup there are %d sensors, want still 4", n)
	}
}

// An installation driven from a YAML file is a deliberate one, and rows added
// behind its back would not be reconciled away on the next start.
func TestExampleSensorsAreSkippedWhenTheFileDeclaresSensors(t *testing.T) {
	h := newHarness(t)
	h.server.cfg.Sensors = []config.SensorSpec{
		{Name: "From YAML", Type: "http", Target: "https://example.org"},
	}
	h.setup()

	if n := len(h.listSensors()); n != 0 {
		t.Fatalf("a YAML-driven installation got %d example sensors, want none", n)
	}
}

// An installation that already holds sensors keeps them: the examples are for a
// database that has nothing in it.
func TestExampleSensorsAreSkippedWhenSensorsExist(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	existing := model.Sensor{
		Name: "Already there", Type: model.SensorHTTP, Target: "https://example.org",
		Interval: 60, Timeout: 10, Retries: 1, Enabled: true,
	}
	store.NormaliseSensor(&existing)
	if err := h.store.CreateSensor(ctx, &existing); err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	h.setup()

	sensors := h.listSensors()
	if len(sensors) != 1 || sensors[0].Name != "Already there" {
		t.Fatalf("got %v, want the pre-existing sensor alone", sensorNames(sensors))
	}
}

// ---------------------------------------------------------------------------
// the interface mode
// ---------------------------------------------------------------------------

func TestSetupStoresTheChosenMode(t *testing.T) {
	h := newHarness(t)
	h.setupWith(map[string]string{"password": testPassword, "mode": "simple"})

	if got := h.uiSettings()["mode"]; got != "simple" {
		t.Errorf("ui.mode = %q, want simple", got)
	}

	// And it can be changed afterwards, which is what the settings screen does.
	resp := h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"mode": "advanced"}}, origin(h))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switching mode: status = %d", resp.StatusCode)
	}
	if got := h.uiSettings()["mode"]; got != "advanced" {
		t.Errorf("ui.mode = %q after the switch, want advanced", got)
	}
}

func TestSetupRejectsAnUnknownMode(t *testing.T) {
	h := newHarness(t)
	resp := h.do(http.MethodPost, "/api/setup",
		map[string]string{"username": testUser, "password": testPassword, "mode": "turbo"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown mode: status = %d, want 400", resp.StatusCode)
	}
	// The refusal has to come before the account is written, or a typo in the
	// mode would leave the installation set up but unanswered.
	if !h.server.setupRequired() {
		t.Error("the rejected setup still created an account")
	}
}

func TestSetupRejectsAnEmptyUsername(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"", "   ", strings.Repeat("x", 65), "two words"} {
		resp := h.do(http.MethodPost, "/api/setup",
			map[string]string{"username": name, "password": testPassword}, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("setup with username %q: status = %d, want 400", name, resp.StatusCode)
		}
	}
	if !h.server.setupRequired() {
		t.Error("a refused setup created an account anyway")
	}
}

// A script that knows nothing about the mode keeps working: an omitted mode is
// not an error, and the dashboard then falls back to the one that hides nothing.
func TestSetupWithoutAModeStillWorks(t *testing.T) {
	h := newHarness(t)
	h.setup()

	if got := h.uiSettings()["mode"]; got != "advanced" {
		t.Errorf("default mode = %q, want advanced", got)
	}
}

// ---------------------------------------------------------------------------
// acknowledging
// ---------------------------------------------------------------------------

// sensorAndIncident creates a sensor with an open outage and returns both.
func (h *harness) sensorAndIncident(t *testing.T, reminderEvery int) (model.Sensor, model.Incident) {
	t.Helper()
	ctx := context.Background()
	c := model.Sensor{
		Name: "Router", Type: model.SensorPing, Target: "192.168.1.1",
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true,
		ReminderEvery: reminderEvery,
	}
	store.NormaliseSensor(&c)
	if err := h.store.CreateSensor(ctx, &c); err != nil {
		t.Fatalf("create sensor: %v", err)
	}
	id, err := h.store.OpenIncident(ctx, c.ID, time.Now().UTC().Add(-time.Minute), "no reply")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	inc, err := h.store.IncidentByID(ctx, id)
	if err != nil {
		t.Fatalf("load incident: %v", err)
	}
	return c, *inc
}

func TestAcknowledgeAndUnacknowledgeAnIncident(t *testing.T) {
	h := newHarness(t)
	h.setup()
	_, inc := h.sensorAndIncident(t, 60)

	resp := h.do(http.MethodPost, "/api/incidents/"+itoa(inc.ID)+"/ack",
		map[string]string{"note": "known reboot"}, origin(h))
	var acked model.Incident
	decode(t, resp, &acked)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack: status = %d", resp.StatusCode)
	}
	if !acked.Acknowledged() {
		t.Fatal("the response does not report the incident as acknowledged")
	}
	if acked.AckNote != "known reboot" {
		t.Errorf("ack_note = %q, want the note back", acked.AckNote)
	}
	// A person did it, and the record names them. Before accounts existed this
	// said "dashboard" — the name of a browser, which answers nothing when the
	// question is who acknowledged the outage at three in the morning.
	if acked.AckedBy != testUser {
		t.Errorf("acked_by = %q, want %q", acked.AckedBy, testUser)
	}

	// Acknowledging again is a conflict, and the message says which kind.
	resp = h.do(http.MethodPost, "/api/incidents/"+itoa(inc.ID)+"/ack", nil, origin(h))
	msg := errorMessage(t, resp)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second ack: status = %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(msg, "already acknowledged") {
		t.Errorf("second ack said %q, want it to say it is already acknowledged", msg)
	}

	// Taking it back leaves the incident open and unacknowledged.
	resp = h.do(http.MethodDelete, "/api/incidents/"+itoa(inc.ID)+"/ack", nil, origin(h))
	var unacked model.Incident
	decode(t, resp, &unacked)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unack: status = %d", resp.StatusCode)
	}
	if unacked.Acknowledged() {
		t.Error("the incident is still acknowledged after DELETE")
	}
	if !unacked.Ongoing() {
		t.Error("unacknowledging resolved the incident")
	}
}

func TestAcknowledgeASensorWithNoOutageIsAConflict(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// One of the example sensors, which is up or unknown and never down.
	sensors := h.listSensors()
	if len(sensors) == 0 {
		t.Fatal("no sensors to acknowledge")
	}

	resp := h.do(http.MethodPost, "/api/sensors/"+itoa(sensors[0].ID)+"/ack", nil, origin(h))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("acking a healthy sensor: status = %d, want 409", resp.StatusCode)
	}
	resp.Body.Close()

	// A sensor that does not exist is a 404, not a 409: different question,
	// different answer.
	resp = h.do(http.MethodPost, "/api/incidents/999999/ack", nil, origin(h))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("acking an unknown incident: status = %d, want 404", resp.StatusCode)
	}
	resp.Body.Close()
}

// The shortcut on a failed card: no incident id at hand, just the sensor.
func TestAcknowledgeThroughTheSensorShortcut(t *testing.T) {
	h := newHarness(t)
	h.setup()
	c, inc := h.sensorAndIncident(t, 60)

	resp := h.do(http.MethodPost, "/api/sensors/"+itoa(c.ID)+"/ack",
		map[string]string{"note": "ticket 4711"}, origin(h))
	var acked model.Incident
	decode(t, resp, &acked)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sensor ack: status = %d", resp.StatusCode)
	}
	// It acknowledged the same incident the long way round would have.
	if acked.ID != inc.ID {
		t.Errorf("acknowledged incident %d, want the open one %d", acked.ID, inc.ID)
	}
	if acked.AckNote != "ticket 4711" {
		t.Errorf("ack_note = %q, want the note back", acked.AckNote)
	}
}

// A cross-site POST must not be able to acknowledge, because the request would
// carry the session cookie automatically.
func TestAcknowledgeNeedsSameOrigin(t *testing.T) {
	h := newHarness(t)
	h.setup()
	_, inc := h.sensorAndIncident(t, 60)

	resp := h.do(http.MethodPost, "/api/incidents/"+itoa(inc.ID)+"/ack", nil,
		map[string]string{"Origin": "https://evil.example"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site ack: status = %d, want 403", resp.StatusCode)
	}

	open, err := h.store.IncidentByID(context.Background(), inc.ID)
	if err != nil {
		t.Fatalf("load incident: %v", err)
	}
	if open.Acknowledged() {
		t.Error("a cross-site request acknowledged the incident")
	}
}

// ---------------------------------------------------------------------------
// the diagnostic route
// ---------------------------------------------------------------------------

func TestDoctorRouteReturnsTheSameTextAsTheCommand(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/api/doctor", nil, origin(h))
	var out struct {
		Summary struct {
			OK     int  `json:"ok"`
			Warn   int  `json:"warn"`
			Fail   int  `json:"fail"`
			Failed bool `json:"failed"`
		} `json:"summary"`
		Findings []struct {
			Level  string `json:"level"`
			Label  string `json:"label"`
			Detail string `json:"detail"`
			Hint   string `json:"hint"`
		} `json:"findings"`
		Text    string `json:"text"`
		Command string `json:"command"`
	}
	decode(t, resp, &out)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doctor route: status = %d", resp.StatusCode)
	}
	if len(out.Findings) == 0 {
		t.Fatal("the diagnostic returned no findings")
	}
	if out.Summary.OK+out.Summary.Warn+out.Summary.Fail != len(out.Findings) {
		t.Errorf("the counts (%d/%d/%d) do not add up to %d findings",
			out.Summary.OK, out.Summary.Warn, out.Summary.Fail, len(out.Findings))
	}
	if out.Command != "docker exec weatherglass weatherglass doctor" {
		t.Errorf("command = %q, want the docker exec form", out.Command)
	}

	// The text has to be what the command line prints for exactly these
	// findings. Rebuilding the report from the JSON and rendering it compares the
	// two paths end to end, which is the strongest check available from here.
	report := doctor.Report{}
	for _, f := range out.Findings {
		report.Findings = append(report.Findings, doctor.Finding{
			Level: levelFromString(f.Level), Label: f.Label, Detail: f.Detail, Hint: f.Hint,
		})
	}
	var want bytes.Buffer
	report.Render(&want)
	if out.Text != want.String() {
		t.Errorf("the dashboard text differs from the command output:\ngot:\n%s\nwant:\n%s", out.Text, want.String())
	}
}

// The route is the most expensive thing the API can be asked to do, so a second
// request inside the cache window is served from the first run.
func TestDoctorRouteIsCached(t *testing.T) {
	h := newHarness(t)
	h.setup()

	var ranAt []time.Time
	for i := 0; i < 2; i++ {
		resp := h.do(http.MethodPost, "/api/doctor", nil, origin(h))
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			t.Fatalf("run %d: status = %d", i+1, resp.StatusCode)
		}
		var out struct {
			Summary struct {
				RanAt time.Time `json:"ran_at"`
			} `json:"summary"`
		}
		decode(t, resp, &out)
		ranAt = append(ranAt, out.Summary.RanAt)
	}
	if ranAt[0].IsZero() {
		t.Fatal("the diagnostic reported no run time")
	}
	if !ranAt[0].Equal(ranAt[1]) {
		t.Errorf("two runs inside the cache window: ran_at moved from %v to %v", ranAt[0], ranAt[1])
	}
}

// ---------------------------------------------------------------------------
// the guided tour
// ---------------------------------------------------------------------------

func TestTourVersionIsRemembered(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/api/onboarding/tour", map[string]int{"version": 3}, origin(h))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tour version: status = %d", resp.StatusCode)
	}

	got := h.uiSettings()["tour_version"]
	if got != "3" {
		t.Errorf("tour version = %q, want 3", got)
	}

	// It grows as steps are added, and it refuses a value that could only come
	// from a crafted request.
	resp = h.do(http.MethodPost, "/api/onboarding/tour", map[string]int{"version": 1 << 30}, origin(h))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("absurd tour version: status = %d, want 400", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func origin(h *harness) map[string]string {
	return map[string]string{"Origin": h.http.URL}
}

func (h *harness) listSensors() []model.Sensor {
	h.t.Helper()
	resp := h.do(http.MethodGet, "/api/sensors", nil, nil)
	var sensors []model.Sensor
	decode(h.t, resp, &sensors)
	return sensors
}

func (h *harness) uiSettings() map[string]string {
	h.t.Helper()
	resp := h.do(http.MethodGet, "/api/settings", nil, nil)
	var settings struct {
		UI map[string]string `json:"ui"`
	}
	decode(h.t, resp, &settings)
	return settings.UI
}

// setupWith is setup with a body of the caller's choosing, for the tests that
// care about what else the first-run request carries. The account name is filled
// in unless the caller is deliberately testing its absence.
func (h *harness) setupWith(body map[string]string) {
	h.t.Helper()
	if _, ok := body["username"]; !ok {
		body["username"] = testUser
	}
	resp := h.do(http.MethodPost, "/api/setup", body, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("setup returned %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			h.cookie = c
		}
	}
	if h.cookie == nil {
		h.t.Fatal("setup did not set a session cookie")
	}
}

func sensorNames(sensors []model.Sensor) []string {
	out := make([]string, 0, len(sensors))
	for _, s := range sensors {
		out = append(out, s.Name)
	}
	return out
}

func errorMessage(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error
}

func levelFromString(s string) doctor.Level {
	switch s {
	case "ok":
		return doctor.OK
	case "warn":
		return doctor.Warn
	case "fail":
		return doctor.Fail
	}
	return doctor.Level(-1)
}
