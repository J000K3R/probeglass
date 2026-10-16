package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// Maintenance windows over HTTP, and the one integration that matters: that a sensor
// under a window says so in the summary the dashboard draws its cards from. The
// arithmetic behind the figures is tested in internal/store and the silence in
// internal/scheduler; what is left for this file is the shape of the API and the flag
// the card reads.

// createWindow posts a window and returns it.
func createWindow(t *testing.T, h *harness, body map[string]any) model.MaintenanceWindow {
	t.Helper()
	resp := h.do(http.MethodPost, "/api/maintenance", body, origin(h))
	defer resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create window: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var w model.MaintenanceWindow
	decode(t, resp, &w)
	return w
}

// listWindows reads the window list back.
func listWindows(t *testing.T, h *harness) []model.MaintenanceWindow {
	t.Helper()
	resp := h.do(http.MethodGet, "/api/maintenance", nil, nil)
	defer resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list windows: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var out struct {
		Windows []model.MaintenanceWindow `json:"windows"`
		Kinds   []string                  `json:"kinds"`
	}
	decode(t, resp, &out)
	// The form offers the kinds, so they come from the server rather than from a
	// second copy of the list in the browser. A test that never looked would not
	// notice the day one of them stopped being sent.
	if len(out.Kinds) != len(model.WindowKinds) {
		t.Fatalf("the list came back with %d kinds, want %d", len(out.Kinds), len(model.WindowKinds))
	}
	return out.Windows
}

// TestWindowRoundTrip walks a weekly window through the whole API: create, list, edit,
// delete. A weekly one rather than a one-off, because it is the shape with the most
// fields and therefore the one where a field is most likely to be dropped on the way
// through.
func TestWindowRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()
	sensor := h.listSensors()[0]

	created := createWindow(t, h, map[string]any{
		"sensor_id": sensor.ID,
		"kind":      "weekly",
		"weekdays":  []int{0, 3},
		"start_min": 3 * 60,
		"end_min":   4*60 + 30,
		"timezone":  "Europe/Berlin",
		"note":      "nightly snapshot",
		"enabled":   true,
	})
	if created.ID == 0 {
		t.Fatalf("the window came back without an id")
	}
	if created.Kind != model.WindowWeekly {
		t.Errorf("kind = %q, want weekly", created.Kind)
	}
	if len(created.Weekdays) != 2 || created.Weekdays[0] != 0 || created.Weekdays[1] != 3 {
		t.Errorf("weekdays = %v, want [0 3]", created.Weekdays)
	}
	if created.StartMin != 180 || created.EndMin != 270 {
		t.Errorf("the window came back as %d-%d minutes, want 180-270",
			created.StartMin, created.EndMin)
	}
	if created.Timezone != "Europe/Berlin" {
		t.Errorf("timezone = %q, want Europe/Berlin", created.Timezone)
	}

	if got := listWindows(t, h); len(got) != 1 || got[0].ID != created.ID {
		t.Fatalf("the list holds %d windows, want the one just made", len(got))
	}

	// An edit that switches it to a one-off, which is the case the single endpoint
	// exists for: the form is one form, and changing the shape must not mean a
	// different URL.
	now := time.Now().UTC().Truncate(time.Second)
	resp := h.do(http.MethodPut, "/api/maintenance/"+itoa(created.ID), map[string]any{
		"sensor_id": sensor.ID,
		"kind":      "once",
		"starts_at": now.Format(time.RFC3339),
		"ends_at":   now.Add(2 * time.Hour).Format(time.RFC3339),
		"timezone":  "UTC",
		"enabled":   true,
	}, origin(h))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("edit: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	var edited model.MaintenanceWindow
	decode(t, resp, &edited)
	if edited.Kind != model.WindowOnce {
		t.Errorf("after the edit the kind is %q, want once", edited.Kind)
	}
	if edited.StartsAt == nil || !edited.StartsAt.Equal(now) {
		t.Errorf("starts_at = %v, want %v", edited.StartsAt, now)
	}
	// The weekly fields are gone rather than carried over. A window that kept them
	// would be one whose shape depends on the order somebody edited it in.
	if len(edited.Weekdays) != 0 || edited.StartMin != 0 || edited.EndMin != 0 {
		t.Errorf("the one-off window kept the weekly fields: %+v", edited)
	}

	resp = h.do(http.MethodDelete, "/api/maintenance/"+itoa(created.ID), nil, origin(h))
	defer resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: status = %d, body = %s", resp.StatusCode, readBody(t, resp))
	}
	if got := listWindows(t, h); len(got) != 0 {
		t.Errorf("after the delete the list still holds %d windows", len(got))
	}
}

// TestWindowValidationReachesTheCaller checks that the sentence the store wrote is the
// sentence the caller reads.
//
// It is tempting to answer every refusal with "invalid request". That would throw away
// the only part of the answer that helps: "a window is for one sensor or for one group,
// not both and not neither" says which box to untick, and a handler that replaced it
// with four generic words would leave somebody staring at a form.
func TestWindowValidationReachesTheCaller(t *testing.T) {
	h := newHarness(t)
	h.setup()
	sensor := h.listSensors()[0]
	now := time.Now().UTC()

	cases := []struct {
		name string
		body map[string]any
		want string
	}{
		{
			"a window for nothing at all",
			map[string]any{"kind": "once", "starts_at": now.Format(time.RFC3339),
				"ends_at": now.Add(time.Hour).Format(time.RFC3339), "timezone": "UTC"},
			"not both and not neither",
		},
		{
			"a window for a sensor and a group at once",
			map[string]any{"sensor_id": sensor.ID, "group": "homelab", "kind": "once",
				"starts_at": now.Format(time.RFC3339),
				"ends_at":   now.Add(time.Hour).Format(time.RFC3339), "timezone": "UTC"},
			"not both and not neither",
		},
		{
			"a weekly window with no days",
			map[string]any{"sensor_id": sensor.ID, "kind": "weekly",
				"start_min": 60, "end_min": 120, "timezone": "UTC"},
			"needs at least one day",
		},
		{
			"a time zone nobody has",
			map[string]any{"sensor_id": sensor.ID, "kind": "once",
				"starts_at": now.Format(time.RFC3339),
				"ends_at":   now.Add(time.Hour).Format(time.RFC3339), "timezone": "Mars/Olympus"},
			"is not a time zone",
		},
		{
			// The browser sends what toISOString() produced; anything else is a bug
			// in the dashboard, and saying so is faster than decoding the wrong thing.
			"a timestamp that is not an instant",
			map[string]any{"sensor_id": sensor.ID, "kind": "once",
				"starts_at": "tonight", "ends_at": now.Add(time.Hour).Format(time.RFC3339),
				"timezone": "UTC"},
			"not an RFC 3339 timestamp",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := h.do(http.MethodPost, "/api/maintenance", c.body, origin(h))
			defer resp.Body.Close() //nolint:errcheck // a test response body
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, readBody(t, resp))
			}
			body := readBody(t, resp)
			if !strings.Contains(body, c.want) {
				t.Errorf("the refusal does not say why:\n  got:  %s\n  want it to mention: %s",
					body, c.want)
			}
		})
	}
}

// TestTheSummarySaysWhichSensorsAreUnderMaintenance is the integration the dashboard
// depends on.
//
// The status stays what the probe found — the box really is away — so this flag is the
// only thing that tells a card to say "expected" rather than "broken". A summary that
// stopped setting it would leave a scheduled outage and an unplanned one looking
// identical, which is the state of affairs the whole feature exists to fix.
func TestTheSummarySaysWhichSensorsAreUnderMaintenance(t *testing.T) {
	h := newHarness(t)
	h.setup()
	sensors := h.listSensors()
	covered, uncovered := sensors[0], sensors[1]

	now := time.Now().UTC()
	createWindow(t, h, map[string]any{
		"sensor_id": covered.ID, "kind": "once",
		"starts_at": now.Add(-time.Hour).Format(time.RFC3339),
		"ends_at":   now.Add(time.Hour).Format(time.RFC3339),
		"timezone":  "UTC", "enabled": true,
	})

	// The window is covering the present, so the scheduler's copy has to know about
	// it without a restart. This is what reloadWindows is for, and a test that only
	// asked the store would not notice the day that stopped happening.
	if !h.server.sched.InWindow(covered, now) {
		t.Errorf("the scheduler does not know about a window that was just saved; it " +
			"would go on opening incidents for an outage the operator announced")
	}

	resp := h.do(http.MethodGet, "/api/summary", nil, nil)
	defer resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary: status = %d", resp.StatusCode)
	}
	var summary struct {
		Views []model.StatusView `json:"sensors"`
	}
	decode(t, resp, &summary)

	seen := map[int64]bool{}
	for _, v := range summary.Views {
		seen[v.ID] = v.InMaintenance
	}
	if !seen[covered.ID] {
		t.Errorf("the sensor under a window is not reported as being in maintenance")
	}
	if seen[uncovered.ID] {
		t.Errorf("a sensor with no window over it is reported as being in maintenance")
	}

	// And a window that has been switched off stops covering, which is the one check
	// the form's switch has to be held to.
	windows := listWindows(t, h)
	off := windows[0]
	resp = h.do(http.MethodPut, "/api/maintenance/"+itoa(off.ID), map[string]any{
		"sensor_id": covered.ID, "kind": "once",
		"starts_at": now.Add(-time.Hour).Format(time.RFC3339),
		"ends_at":   now.Add(time.Hour).Format(time.RFC3339),
		"timezone":  "UTC", "enabled": false,
	}, origin(h))
	resp.Body.Close() //nolint:errcheck // a test response body
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("switching the window off: status = %d", resp.StatusCode)
	}
	if h.server.sched.InWindow(covered, now) {
		t.Errorf("a window that was switched off still covers the sensor")
	}
}

// TestAWindowSurvivesADatabaseRoundTrip is a small guard on the columns rather than on
// the API: the two shapes share one table, and the fields of the shape that is not in
// use have to come back empty rather than carrying whatever the last row left.
func TestAWindowSurvivesADatabaseRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()
	sensor := h.listSensors()[0]
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	once := model.MaintenanceWindow{
		SensorID: sensor.ID, Kind: model.WindowOnce,
		StartsAt: &now, EndsAt: ptr(now.Add(time.Hour)),
		Timezone: "UTC", Note: "swap", Enabled: true,
	}
	if err := h.server.store.CreateWindow(ctx, &once); err != nil {
		t.Fatalf("create: %v", err)
	}
	back, err := h.server.store.GetWindow(ctx, once.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if back.StartsAt == nil || !back.StartsAt.Equal(now) {
		t.Errorf("starts_at came back as %v, want %v", back.StartsAt, now)
	}
	if len(back.Weekdays) != 0 || back.EndMin != 0 {
		t.Errorf("a one-off window came back with weekly fields: %+v", back)
	}
	if back.Note != "swap" || !back.Enabled {
		t.Errorf("note or enabled was lost: %+v", back)
	}
}

func ptr[T any](v T) *T { return &v }
