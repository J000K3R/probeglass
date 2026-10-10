package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// incidentsFilterFixture puts one open and one resolved outage behind the API, on
// two sensors with different names, so that the query parameters can be told apart
// from one another by what comes back.
type incidentsFilterFixture struct {
	h       *harness
	router  model.Sensor
	printer model.Sensor
}

func newIncidentsFilterFixture(t *testing.T) *incidentsFilterFixture {
	t.Helper()
	h := newHarness(t)
	h.setup()
	ctx := context.Background()

	f := &incidentsFilterFixture{h: h}
	f.router = f.sensor(t, "Router", model.SensorPing, "10.0.0.1")
	f.printer = f.sensor(t, "Printer", model.SensorTCP, "10.0.0.9")

	// Thirty hours back, so that the newest outage is about twenty-nine hours old:
	// outside a day-long window and comfortably inside a week-long one, with no
	// dependence on which side of a boundary a second lands.
	base := time.Now().UTC().Add(-30 * time.Hour).Truncate(time.Second)

	old, err := h.store.OpenIncident(ctx, f.printer.ID, base, "paper jam")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	if err := h.store.CloseIncident(ctx, old, base.Add(5*time.Minute)); err != nil {
		t.Fatalf("close incident: %v", err)
	}
	if _, err := h.store.OpenIncident(ctx, f.router.ID, base.Add(time.Hour), "connection refused"); err != nil {
		t.Fatalf("open incident: %v", err)
	}
	return f
}

func (f *incidentsFilterFixture) sensor(t *testing.T, name string, kind model.SensorType, target string) model.Sensor {
	t.Helper()
	s := model.Sensor{
		Name: name, Type: kind, Target: target,
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true,
	}
	store.NormaliseSensor(&s)
	if err := f.h.store.CreateSensor(context.Background(), &s); err != nil {
		t.Fatalf("create sensor %q: %v", name, err)
	}
	return s
}

// causes asks the route for a list and returns the causes it answered with, which
// is what the filter is being asked about. Comparing causes rather than counts
// means a test that quietly matched the wrong row cannot pass on a coincidence of
// sizes.
func (f *incidentsFilterFixture) causes(t *testing.T, query string) []string {
	t.Helper()
	resp := f.h.do(http.MethodGet, "/api/incidents"+query, nil, nil)
	var rows []store.IncidentRow
	decode(t, resp, &rows)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/incidents%s = %d", query, resp.StatusCode)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Cause)
	}
	return out
}

func (f *incidentsFilterFixture) want(causes ...string) map[string]int {
	out := map[string]int{}
	for _, c := range causes {
		out[c]++
	}
	return out
}

// TestIncidentsRouteStateParameter walks the three words the filter line's
// segmented control sends, and the older "open=1" spelling that a bookmark or a
// script from before this feature still carries.
//
// The last case is the one worth the line: an unknown state must be refused rather
// than ignored. A filter that silently answers a different question than the one
// asked is how somebody concludes their installation is healthy.
func TestIncidentsRouteStateParameter(t *testing.T) {
	f := newIncidentsFilterFixture(t)

	for _, c := range []struct {
		query string
		want  map[string]int
	}{
		{"", f.want("paper jam", "connection refused")},
		{"?state=all", f.want("paper jam", "connection refused")},
		{"?state=open", f.want("connection refused")},
		{"?state=resolved", f.want("paper jam")},
		{"?open=1", f.want("connection refused")},
	} {
		got := f.causes(t, c.query)
		if len(got) != len(c.want) {
			t.Errorf("GET /api/incidents%s returned %v, want %v", c.query, got, c.want)
			continue
		}
		for _, cause := range got {
			if c.want[cause] == 0 {
				t.Errorf("GET /api/incidents%s returned %q, which it should not have", c.query, cause)
			}
		}
	}

	// A typo is an error, and it explains itself.
	resp := f.h.do(http.MethodGet, "/api/incidents?state=everything", nil, nil)
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("?state=everything = %d, want 400", resp.StatusCode)
	}
	if body.Error == "" {
		t.Error("the refusal carries no explanation")
	}
}

// TestIncidentsRouteNarrowsBySensorAndText checks the other two parameters the
// filter line sends, end to end: the sensor comes back as an id from the select,
// and the text comes back as whatever was typed into the box.
func TestIncidentsRouteNarrowsBySensorAndText(t *testing.T) {
	f := newIncidentsFilterFixture(t)

	if got := f.causes(t, "?sensor="+itoa(f.router.ID)); len(got) != 1 || got[0] != "connection refused" {
		t.Errorf("?sensor=<router> returned %v", got)
	}
	if got := f.causes(t, "?q=jam"); len(got) != 1 || got[0] != "paper jam" {
		t.Errorf("?q=jam returned %v", got)
	}
	// The sensor's own name is matched too, which is how somebody remembers an
	// outage: "the time the Printer went".
	if got := f.causes(t, "?q=printer"); len(got) != 1 || got[0] != "paper jam" {
		t.Errorf("?q=printer returned %v", got)
	}
	// A "%" is a percent sign and matches nothing here. Sent as a wildcard it would
	// have matched both rows, which is exactly the failure this guards.
	if got := f.causes(t, "?q=%25"); len(got) != 0 {
		t.Errorf("?q=%% returned %v, want nothing", got)
	}
	// Both at once, so that the two conditions are not each other's alibi.
	if got := f.causes(t, "?sensor="+itoa(f.printer.ID)+"&q=jam"); len(got) != 1 {
		t.Errorf("?sensor=<printer>&q=jam returned %v", got)
	}
	if got := f.causes(t, "?sensor="+itoa(f.router.ID)+"&q=jam"); len(got) != 0 {
		t.Errorf("?sensor=<router>&q=jam returned %v, want nothing", got)
	}
}

// TestIncidentsRouteRangeIsAWindow checks that the period chips do something, and
// that a period nobody offers is refused rather than quietly read as "everything".
func TestIncidentsRouteRangeIsAWindow(t *testing.T) {
	f := newIncidentsFilterFixture(t)

	// Both outages are older than a day and younger than a week, so the two chips
	// have to disagree. A window that returned everything would be indistinguishable
	// from a filter that does nothing.
	if got := f.causes(t, "?range=24h"); len(got) != 0 {
		t.Errorf("?range=24h returned %v, but everything in the fixture is over a day old", got)
	}
	if got := f.causes(t, "?range=7d"); len(got) != 2 {
		t.Errorf("?range=7d returned %v, want both", got)
	}

	resp := f.h.do(http.MethodGet, "/api/incidents?range=fortnight", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("?range=fortnight = %d, want 400", resp.StatusCode)
	}
}
