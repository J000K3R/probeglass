package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/store"
)

// A maintenance window is a promise about silence: a probe that fails inside one
// opens no incident and sends no message, and the status still says the box is
// away. The uptime half is tested in internal/store, where the arithmetic lives.
//
// These tests drive step() directly, with the marker already on the result. That is
// the seam the scheduler owns — execute() sets the marker and step() acts on it — and
// driving it here means a test can say what it is about: a failure inside a window,
// not a failure that happened to happen at the time of day this test runs.

// inWindow marks a result as having landed inside a maintenance window, which is
// what execute() does before the row is written.
func inWindow(r model.Result) model.Result {
	r.Maint = true
	return r
}

// TestAFailureInsideAWindowOpensNoIncident is the headline promise, and the two
// halves of it are checked together: nothing is claimed, and nothing is hidden.
func TestAFailureInsideAWindowOpensNoIncident(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 3)

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// Four failures inside the window — one more than the sensor's retries, so a
	// sensor that was merely counted would have been called down and would have
	// opened an incident on the fourth.
	for i := 0; i < 4; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		tr := s.step(ctx, c, inWindow(result(c.ID, at, false, "no reply")))
		if tr.alert != "" {
			t.Fatalf("probe %d inside the window: alert = %q, want nothing sent — "+
				"the downtime was announced in advance", i, tr.alert)
		}
		if tr.incident != nil {
			t.Fatalf("probe %d inside the window: an incident was opened for an "+
				"outage that was expected", i)
		}
	}

	// And the status is still honest about what the probe found.
	tr := s.step(ctx, c, inWindow(result(c.ID, base.Add(4*time.Minute), false, "no reply")))
	if tr.current != model.StatusDown {
		t.Errorf("status = %q, want %q — the window silences the alert, not the fact "+
			"that the box is not answering", tr.current, model.StatusDown)
	}
	open, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil {
		t.Fatalf("read open incident: %v", err)
	}
	if open != nil {
		t.Errorf("an incident was opened for a sensor inside a maintenance window: %+v", open)
	}
}

// TestAWindowEndsAnOutageThatWasAlreadyRunning is the decision that an outage
// already open when the window begins is closed with a note.
//
// The alternative — leaving it open and going quiet — would leave the dashboard
// showing an outage that nobody is being told about, for as long as the window lasts
// and then some. Closing it says what happened and lets the ordinary detection open a
// new one if the box stays away.
func TestAWindowEndsAnOutageThatWasAlreadyRunning(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// An ordinary outage, with an alert, before anybody planned anything.
	tr := s.step(ctx, c, result(c.ID, base, false, "no reply"))
	if tr.alert != notify.EventDown {
		t.Fatalf("the first failure did not alert: %q", tr.alert)
	}
	before, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil || before == nil {
		t.Fatalf("no incident was opened for an ordinary outage: %v, %v", before, err)
	}

	// The window begins and the next probe lands inside it.
	at := base.Add(2 * time.Minute)
	tr = s.step(ctx, c, inWindow(result(c.ID, at, false, "no reply")))
	if tr.incident == nil {
		t.Fatalf("the window began while an outage was open and the outage was left " +
			"open; an incident that nobody will be told about is worse than one that " +
			"was closed")
	}
	if tr.alert != "" {
		t.Errorf("closing the outage sent %q; the message that opened it already went "+
			"out and a second one saying the maintenance has started is a message "+
			"about a message", tr.alert)
	}
	if !tr.incident.ResolvedAt.After(before.StartedAt) {
		t.Errorf("the incident was closed before it began: %v", tr.incident)
	}

	after, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil {
		t.Fatalf("read open incident: %v", err)
	}
	if after != nil {
		t.Errorf("an incident is still open after a window began: %+v", after)
	}
}

// TestAWindowThatEndsLeavesTheSensorToEarnItsIncidentBack is the other side of that
// decision, and the one that would otherwise be a hole: a box that was down before the
// window and is still down after it.
//
// None of what failed during the window is evidence about now. The sensor starts again
// from the first failure outside it, and has to use up its retries like anything else —
// which is what stops a single blip right after a backup window from becoming an outage.
func TestAWindowThatEndsLeavesTheSensorToEarnItsIncidentBack(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 3)

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// Down before the window, so there is an outage to end.
	s.step(ctx, c, result(c.ID, base, false, "no reply"))
	s.step(ctx, c, result(c.ID, base.Add(time.Minute), false, "no reply"))
	tr := s.step(ctx, c, result(c.ID, base.Add(2*time.Minute), false, "no reply"))
	if tr.current != model.StatusDown {
		t.Fatalf("setup: status = %q, want down", tr.current)
	}

	// Three minutes inside a window.
	for i := 3; i <= 5; i++ {
		s.step(ctx, c, inWindow(result(c.ID, base.Add(time.Duration(i)*time.Minute), false, "no reply")))
	}

	// The window ends and the box is still away. One failure is not an outage.
	at := base.Add(6 * time.Minute)
	tr = s.step(ctx, c, result(c.ID, at, false, "still no reply"))
	if tr.alert != "" {
		t.Errorf("the first failure after the window alerted at once: %q", tr.alert)
	}
	if tr.current != model.StatusWarn {
		t.Errorf("status = %q, want %q — the window is over and the sensor has to fail "+
			"its way to the threshold again", tr.current, model.StatusWarn)
	}

	// Two more, and the retries are used up.
	s.step(ctx, c, result(c.ID, at.Add(time.Minute), false, "still no reply"))
	tr = s.step(ctx, c, result(c.ID, at.Add(2*time.Minute), false, "still no reply"))
	if tr.alert != notify.EventDown {
		t.Fatalf("the sensor failed its way to the threshold after the window and no "+
			"alert went out: %q.\nA box that never came back from a maintenance window "+
			"is an outage, and the window excused the failures inside it, not the ones "+
			"after it.", tr.alert)
	}
	if tr.incident == nil {
		t.Fatalf("the threshold was reached with no incident opened")
	}
	if tr.incident.StartedAt.Before(at) {
		t.Errorf("the new outage is dated from %v, before the window ended at %v — "+
			"the failures in between were the ones that were expected",
			tr.incident.StartedAt, at)
	}
}

// TestAWindowIsSilentWhenTheSensorComesBackInsideIt covers the planned outage that
// goes to plan: down, and then up again, all of it expected.
//
// The recovery is silent on purpose. No "down" was ever sent, so an "up" would be a
// message about a problem the reader was never told about.
func TestAWindowIsSilentWhenTheSensorComesBackInsideIt(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	s.step(ctx, c, inWindow(result(c.ID, base, false, "no reply")))
	tr := s.step(ctx, c, inWindow(result(c.ID, base.Add(time.Minute), true, "reply")))
	if tr.alert != "" {
		t.Errorf("the sensor recovered inside a window and %q went out; nothing had "+
			"been sent about it going down, so there is nothing to follow up", tr.alert)
	}
	if tr.current != model.StatusUp {
		t.Errorf("status = %q, want up — the window silences the messages, not the "+
			"measurement", tr.current)
	}
	open, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil {
		t.Fatalf("read open incident: %v", err)
	}
	if open != nil {
		t.Errorf("an incident is open after a planned outage that went to plan: %+v", open)
	}
}

// TestAProbeInsideAWindowIsMarkedAndCountedOut is the wiring rather than the state
// machine: execute() has to ask the window list, and the answer has to reach the
// stored row. Without the marker the whole feature is a suppression that the uptime
// figure knows nothing about.
//
// ProbeNow is used rather than a probe loop because it is the same code path with the
// same marking, and it can be waited for.
func TestAProbeInsideAWindowIsMarkedAndCountedOut(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	// A target that refuses at once, so the probe fails and the result is unambiguous
	// about which side of the window it fell on — and so that this test does not sit
	// for the length of a probe timeout waiting for a reserved address to stay silent.
	c := newTestSensor(t, st, 1)
	c.Type = model.SensorHTTP
	c.Target = "http://127.0.0.1:1/" // port 1: nothing listens, and the kernel says so
	if err := st.UpdateSensor(ctx, &c); err != nil {
		t.Fatalf("update sensor: %v", err)
	}

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Hour)
	if err := st.CreateWindow(ctx, &model.MaintenanceWindow{
		SensorID: c.ID, Kind: model.WindowOnce, StartsAt: &from, EndsAt: &to,
		Timezone: "UTC", Note: "test", Enabled: true,
	}); err != nil {
		t.Fatalf("create window: %v", err)
	}
	if err := s.ReloadWindows(ctx); err != nil {
		t.Fatalf("reload windows: %v", err)
	}

	res, err := s.ProbeNow(ctx, c.ID)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !res.Maint {
		t.Fatalf("a probe taken inside a maintenance window came back unmarked, so it " +
			"counts against the uptime figure of a period somebody said in advance to " +
			"expect nothing from")
	}

	// And it is stored that way, not only returned that way — an installation
	// restarted between the probe and the read would keep the row and lose the flag.
	stored, err := st.RecentResults(ctx, c.ID, 1)
	if err != nil || len(stored) != 1 {
		t.Fatalf("read the stored result: %v, %v", stored, err)
	}
	if !stored[0].Maint {
		t.Errorf("the result was returned marked but stored unmarked")
	}
}

// TestAWindowIsMatchedThroughTheSchedulerNotTheSensorID holds the group case end to
// end, because a window on a group is the one that reaches sensors the window never
// named.
func TestAWindowIsMatchedThroughTheSchedulerNotTheSensorID(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)
	c.Group = "homelab"
	if err := st.UpdateSensor(ctx, &c); err != nil {
		t.Fatalf("update sensor: %v", err)
	}
	other := model.Sensor{
		Name: "Other", Type: model.SensorPing, Target: "192.168.1.2",
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true, Group: "rented",
	}
	store.NormaliseSensor(&other)
	if err := st.CreateSensor(ctx, &other); err != nil {
		t.Fatalf("create the second sensor: %v", err)
	}

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Hour)
	if err := st.CreateWindow(ctx, &model.MaintenanceWindow{
		Group: "homelab", Kind: model.WindowOnce, StartsAt: &from, EndsAt: &to,
		Timezone: "UTC", Enabled: true,
	}); err != nil {
		t.Fatalf("create window: %v", err)
	}
	if err := s.ReloadWindows(ctx); err != nil {
		t.Fatalf("reload windows: %v", err)
	}

	at := time.Now().UTC()
	if !s.inWindow(c, at) {
		t.Errorf("a window on the group %q does not reach a sensor carrying it", c.Group)
	}
	if s.inWindow(other, at) {
		t.Errorf("a window on the group %q reaches a sensor in %q", c.Group, other.Group)
	}
}

// TestAWindowTheFileDoesNotOwnIsTheOnlyOneThatCanBeDeleted is a small guard on the
// managed flag reaching the scheduler's copy of the list: a window the configuration
// file owns must not be removable, and the refusal has to come from the store rather
// than from a check somebody remembered to write in the handler.
func TestAManagedWindowIsRefusedHere(t *testing.T) {
	_, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	from := time.Now().UTC().Add(-time.Minute)
	to := time.Now().UTC().Add(time.Hour)
	w := model.MaintenanceWindow{
		SensorID: c.ID, Kind: model.WindowOnce, StartsAt: &from, EndsAt: &to,
		Timezone: "UTC", Enabled: true, Managed: true,
	}
	if err := st.CreateWindow(ctx, &w); err != nil {
		t.Fatalf("create window: %v", err)
	}
	if err := st.DeleteWindow(ctx, w.ID); err == nil {
		t.Fatalf("a window declared in the configuration file was deleted through the " +
			"store")
	} else if !strings.Contains(err.Error(), "configuration file") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}
