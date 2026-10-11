package api

import (
	"net/http"
	"testing"

	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/demo"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// The example data used to be a one-way door: the button in Settings took it
// away and nothing could bring it back. These tests are about the way back.
//
// Two properties carry the whole feature. Restoring must be idempotent, because
// it is a button and buttons get pressed twice. And it must not trip over a name
// that is already taken, because sensors.name is unique and somebody's own
// sensor is not a reason to fail the request.

// restoreExamples posts the restore request and returns what the server said.
func restoreExamples(t *testing.T, h *harness) (int, store.ExampleData) {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/examples", nil, map[string]string{"Origin": h.http.URL})
	var data store.ExampleData
	decode(t, resp, &data)
	return resp.StatusCode, data
}

// emptyExamples removes whatever example data the installation came with, so
// that a test starts from a known empty table. It returns how many sensors were
// removed, which is also the assertion that the setup step still writes them.
func emptyExamples(t *testing.T, h *harness) int {
	t.Helper()
	resp := h.do(http.MethodDelete, "/api/examples", nil, map[string]string{"Origin": h.http.URL})
	var removed store.ExampleData
	decode(t, resp, &removed)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete example data: status = %d", resp.StatusCode)
	}
	return removed.Sensors
}

// TestRestoreExampleDataBringsBackHistory is the feature end to end: remove
// everything, ask for it back, and get a dataset with a week of invented history
// rather than an empty grid.
func TestRestoreExampleDataBringsBackHistory(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// What a fresh installation starts with: the four the setup step writes. Both
	// sets arrive with a week of history now — that is what makes the first-run
	// dashboard worth looking at — so what separates them is how many sensors and
	// how much of the rest of the dataset comes with them.
	if n := emptyExamples(t, h); n != 4 {
		t.Fatalf("the setup step created %d example sensor(s), want the four it is "+
			"meant to", n)
	}
	status, restored := restoreExamples(t, h)
	if status != http.StatusOK {
		t.Fatalf("restore: status = %d, want 200", status)
	}

	// More than four: the button loads the whole dataset — sensors in their groups,
	// on their sites, with the richer histories — where the setup step writes only
	// the four examples. That difference is the user's decision and is pinned here so
	// the two paths cannot quietly merge. The count comes from the dataset rather
	// than from a number written down here, because the number is the thing that
	// changes.
	if want := len(demo.Sensors()); restored.Sensors != want {
		t.Errorf("restored %d sensor(s), want the %d of the rich dataset",
			restored.Sensors, want)
	}
	if restored.Results == 0 {
		t.Error("the restored sensors have no probe history; the dataset is supposed " +
			"to arrive with a week of it")
	}
	if restored.Incidents == 0 {
		t.Error("the restored sensors have no incidents; the outages in the dataset " +
			"are what the incident log and the uptime bars are meant to show")
	}

	// The GET route the dashboard reads has to agree with what the POST answered,
	// or the panel would show one number and the toast another.
	resp := h.do(http.MethodGet, "/api/examples", nil, nil)
	var reported store.ExampleData
	decode(t, resp, &reported)
	if reported != restored {
		t.Errorf("GET /api/examples reports %+v, the restore answered %+v",
			reported, restored)
	}
}

// TestRestoreExampleDataIsIdempotent covers the second click. It must create
// nothing and invent nothing, which is why Seed reports what it wrote.
func TestRestoreExampleDataIsIdempotent(t *testing.T) {
	h := newHarness(t)
	h.setup()
	emptyExamples(t, h)

	_, first := restoreExamples(t, h)
	status, second := restoreExamples(t, h)
	if status != http.StatusOK {
		t.Fatalf("second restore: status = %d, want 200", status)
	}
	if second != first {
		t.Errorf("the second run changed the data: %+v, then %+v", first, second)
	}
}

// TestRestoreExampleDataLeavesAnOccupiedNameAlone is the UNIQUE constraint
// showing through the handler.
//
// Somebody with their own sensor called "Example website" must get the rest of the
// dataset, not a 500 — and their sensor must come back untouched, above all not
// marked as example data, which would put it in front of the removal button.
func TestRestoreExampleDataLeavesAnOccupiedNameAlone(t *testing.T) {
	h := newHarness(t)
	h.setup()
	emptyExamples(t, h)

	origin := map[string]string{"Origin": h.http.URL}
	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "Example website", "type": "http", "target": "https://mine.example",
		"interval": 60, "timeout": 10, "retries": 1, "enabled": true,
	}, origin)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d", resp.StatusCode)
	}
	decode(t, resp, nil)

	status, restored := restoreExamples(t, h)
	if status != http.StatusOK {
		t.Fatalf("restore: status = %d, want 200 — a taken name must not fail the "+
			"whole request", status)
	}
	if want := len(demo.Sensors()) - 1; restored.Sensors != want {
		t.Errorf("restored %d sensor(s), want the %d whose names were free",
			restored.Sensors, want)
	}

	resp = h.do(http.MethodGet, "/api/sensors", nil, nil)
	var sensors []model.Sensor
	decode(t, resp, &sensors)
	var mine *model.Sensor
	for i := range sensors {
		if sensors[i].Target == "https://mine.example" {
			mine = &sensors[i]
		}
	}
	if mine == nil {
		t.Fatal("the sensor that was already there is gone")
	}
	if mine.Origin == model.OriginExample {
		t.Error("the sensor that was already there came back marked as example data, " +
			"so the removal button would offer to delete it")
	}
}

// TestRestoreExampleDataRefusesAFileConfiguredInstallation keeps the one guard:
// an installation driven from YAML is a deliberate one, and rows written next to
// it would be rows its own next start would not touch.
func TestRestoreExampleDataRefusesAFileConfiguredInstallation(t *testing.T) {
	h := newHarness(t)
	h.setup()
	emptyExamples(t, h)

	h.server.cfg.Sensors = []config.SensorSpec{{Name: "from-file", Type: "http"}}

	status, data := restoreExamples(t, h)
	if status != http.StatusConflict {
		t.Fatalf("restore against a file-configured installation: status = %d, want 409",
			status)
	}
	if data.Sensors != 0 {
		t.Errorf("the refused restore still reports %d sensor(s)", data.Sensors)
	}
}
