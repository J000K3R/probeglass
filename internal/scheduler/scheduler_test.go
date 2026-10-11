package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/store"
)

// newTestScheduler returns a scheduler with a real store in a temporary
// directory and no alert channels, which is enough to drive the state machine.
//
// The broker is real because the probe loops publish to it, and a test that lets
// one run with a nil broker would take the whole test binary down with a nil
// dereference in a goroutine.
func newTestScheduler(t *testing.T) (*Scheduler, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"), log)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, event.NewBroker(), log), st
}

// runnerCount is how many probe loops the scheduler has running. The caller must
// not hold the scheduler's lock.
func runnerCount(s *Scheduler) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runners)
}

func newTestSensor(t *testing.T, st *store.Store, retries int) model.Sensor {
	t.Helper()
	c := model.Sensor{
		Name:     "Router",
		Type:     model.SensorPing,
		Target:   "192.168.1.1",
		Interval: 60,
		Timeout:  5,
		Retries:  retries,
		Enabled:  true,
	}
	store.NormaliseSensor(&c)
	if err := st.CreateSensor(context.Background(), &c); err != nil {
		t.Fatalf("create sensor: %v", err)
	}
	return c
}

func result(id int64, at time.Time, ok bool, msg string) model.Result {
	return model.Result{SensorID: id, TS: at.UTC(), Success: ok, Message: msg}
}

// The recovery alert is the message a user reads after an outage, and it states
// how long the sensor was down. That duration comes from the incident's start
// time, which the state machine must report before it moves the sensor's status
// timestamp on to the recovery time — otherwise every recovery says "was down
// for 0s" while the stored incident, computed from the database row, is correct.
func TestRecoveryAlertReportsTheRealOutageStart(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	down := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	up := down.Add(90 * time.Second)

	tr := s.step(ctx, c, result(c.ID, down, false, "request timeout"))
	if tr.alert != notify.EventDown {
		t.Fatalf("first failure: alert = %q, want a down alert", tr.alert)
	}
	if tr.incident == nil {
		t.Fatal("first failure opened no incident")
	}

	tr = s.step(ctx, c, result(c.ID, up, true, "pong"))
	if tr.alert != notify.EventUp {
		t.Fatalf("recovery: alert = %q, want an up alert", tr.alert)
	}
	if tr.incident == nil {
		t.Fatal("recovery carried no incident, so the alert cannot name the outage")
	}
	if !tr.incident.StartedAt.Equal(down) {
		t.Errorf("recovery incident StartedAt = %s, want the outage start %s",
			tr.incident.StartedAt, down)
	}
	if tr.incident.ResolvedAt == nil || !tr.incident.ResolvedAt.Equal(up) {
		t.Errorf("recovery incident ResolvedAt = %v, want %s", tr.incident.ResolvedAt, up)
	}

	// The stored row must agree with the alert: it is what the incident list
	// and the uptime percentages are built from.
	incidents, err := st.ListIncidents(ctx, store.IncidentFilter{SensorID: c.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	if len(incidents) != 1 {
		t.Fatalf("stored incidents = %d, want 1", len(incidents))
	}
	if got := incidents[0]; !got.StartedAt.Equal(down) || got.ResolvedAt == nil || !got.ResolvedAt.Equal(up) {
		t.Errorf("stored incident = %s → %v, want %s → %s",
			got.StartedAt, got.ResolvedAt, down, up)
	}
	if open, err := st.OpenIncidentFor(ctx, c.ID); err != nil || open != nil {
		t.Errorf("OpenIncidentFor after a recovery = %v (err %v), want none", open, err)
	}
}

// retries is the "do not wake me up for one dropped packet" knob, so the
// incident must open on the failure that reaches the threshold and not before.
func TestIncidentOpensOnTheFailureThatReachesRetries(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 3)

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		at := base.Add(time.Duration(i) * 60 * time.Second)
		tr := s.step(ctx, c, result(c.ID, at, false, "no reply"))
		if tr.alert != "" {
			t.Fatalf("failure %d of 3: alert = %q, want none inside the grace period", i+1, tr.alert)
		}
		if tr.incident != nil {
			t.Fatalf("failure %d of 3: incident opened too early", i+1)
		}
		if tr.current != model.StatusWarn {
			t.Fatalf("failure %d of 3: status = %q, want flaky — the retries are not used up yet", i+1, tr.current)
		}
	}

	third := base.Add(2 * 60 * time.Second)
	tr := s.step(ctx, c, result(c.ID, third, false, "no reply"))
	if tr.alert != notify.EventDown {
		t.Fatalf("third failure: alert = %q, want a down alert", tr.alert)
	}
	if tr.incident == nil {
		t.Fatal("third failure opened no incident")
	}
	// The outage began with the first failure, not with the third probe: the
	// incident duration is what the user sees on the timeline.
	if !tr.incident.StartedAt.Equal(base) {
		t.Errorf("incident StartedAt = %s, want the first failure %s", tr.incident.StartedAt, base)
	}
}

// A restart must not lose a running outage and must not re-announce it either.
func TestRestoreKeepsAnOpenIncidentWithoutAlertingAgain(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	down := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if err := st.InsertResult(ctx, &model.Result{SensorID: c.ID, TS: down, Success: false, Message: "boom"}); err != nil {
		t.Fatalf("insert result: %v", err)
	}
	if _, err := st.OpenIncident(ctx, c.ID, down, "boom"); err != nil {
		t.Fatalf("open incident: %v", err)
	}

	s.restoreState(ctx, c)
	snap := s.Snapshot(c.ID)
	if snap.Status != model.StatusDown {
		t.Errorf("restored status = %q, want down while an incident is open", snap.Status)
	}
	if !snap.Since.Equal(down) {
		t.Errorf("restored since = %s, want the incident start %s", snap.Since, down)
	}
}

// A blip survives a restart as a blip.
//
// The failure streak read back out of the history is what makes a sensor yellow
// — not the "unknown" a restart used to report, which is the word for a sensor
// nothing is known about at all. And going back up from yellow announces
// nothing, exactly as going down into yellow announced nothing.
func TestRestoreReadsABlipAsFlaky(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 3)

	blip := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if err := st.InsertResult(ctx, &model.Result{SensorID: c.ID, TS: blip, Success: false, Message: "no reply"}); err != nil {
		t.Fatalf("insert result: %v", err)
	}

	s.restoreState(ctx, c)
	snap := s.Snapshot(c.ID)
	if snap.Status != model.StatusWarn {
		t.Fatalf("restored status = %q, want flaky after one failed probe below the retries", snap.Status)
	}
	if !snap.Since.Equal(blip) {
		t.Errorf("restored since = %s, want the failed probe %s", snap.Since, blip)
	}

	tr := s.step(ctx, c, result(c.ID, blip.Add(time.Minute), true, "pong"))
	if tr.current != model.StatusUp {
		t.Errorf("recovery from flaky: status = %q, want up", tr.current)
	}
	if tr.alert != "" {
		t.Errorf("recovery from flaky: alert = %q, want none — the blip never alerted", tr.alert)
	}
	if tr.incident != nil {
		t.Errorf("recovery from flaky closed incident %d, want none: no incident was opened",
			tr.incident.ID)
	}
}

// A sensor that recovers must not leave a stale incident behind, even if its
// runtime state was lost: the recovery path repairs the database either way.
func TestRecoveryWithoutTrackedIncidentStillClosesTheRow(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)

	down := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if _, err := st.OpenIncident(ctx, c.ID, down, "orphan"); err != nil {
		t.Fatalf("open incident: %v", err)
	}

	// State that believes the sensor was already down but carries no incident id.
	s.mu.Lock()
	s.states[c.ID] = &state{status: model.StatusDown, since: down}
	s.mu.Unlock()

	tr := s.step(ctx, c, result(c.ID, down.Add(time.Minute), true, "pong"))
	if tr.alert != notify.EventUp {
		t.Fatalf("alert = %q, want an up alert", tr.alert)
	}
	open, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil {
		t.Fatalf("OpenIncidentFor: %v", err)
	}
	if open != nil {
		t.Errorf("incident %d is still open after a recovery", open.ID)
	}
}

// An acknowledged outage keeps being probed and stays open, but stops firing
// reminders. The two things it must not do are also tested: recovery still
// alerts, and taking the acknowledgement back reminds on the very next probe
// rather than waiting out a whole reminder interval.
func TestAcknowledgedOutageStopsRemindersUntilUnacknowledged(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()
	c := newTestSensor(t, st, 1)
	c.ReminderEvery = 60

	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

	// First failure: the outage opens and alerts. Nobody can have acknowledged
	// it yet — this is the alert that starts the story.
	tr := s.step(ctx, c, result(c.ID, base, false, "no reply"))
	if tr.alert != notify.EventDown {
		t.Fatalf("first failure: alert = %q, want a down alert", tr.alert)
	}
	open, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil || open == nil {
		t.Fatalf("open incident: %v, %v", open, err)
	}

	// A person acknowledges it.
	if err := st.AckIncident(ctx, open.ID, "dashboard", "known reboot", base); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Three reminder intervals pass in silence.
	for i := 1; i <= 3; i++ {
		at := base.Add(time.Duration(i) * 60 * time.Second)
		tr = s.step(ctx, c, result(c.ID, at, false, "no reply"))
		if tr.alert != "" {
			t.Fatalf("minute %d: alert = %q, want a reminder to be suppressed", i, tr.alert)
		}
		if tr.current != model.StatusDown {
			t.Fatalf("minute %d: status = %q, want the sensor to stay down", i, tr.current)
		}
	}

	// Taking the acknowledgement back must remind at once — the reminder was
	// overdue the whole time, and swallowing that would make "unacknowledge"
	// look like it did nothing.
	if err := st.UnackIncident(ctx, open.ID); err != nil {
		t.Fatalf("unack: %v", err)
	}
	at := base.Add(4 * 60 * time.Second)
	tr = s.step(ctx, c, result(c.ID, at, false, "no reply"))
	if tr.alert != notify.EventReminder {
		t.Fatalf("after unacknowledging: alert = %q, want an immediate reminder", tr.alert)
	}

	// Recovery is news even to whoever acknowledged the outage, so it is not
	// suppressed by the acknowledgement.
	if err := st.AckIncident(ctx, open.ID, "dashboard", "", base); err != nil {
		// The outage was not reopened, so the acknowledgement is still the first
		// one; a second call is refused. That is fine — it is already acked.
		if !strings.Contains(err.Error(), "already acknowledged") {
			t.Fatalf("re-ack: %v", err)
		}
	}
	tr = s.step(ctx, c, result(c.ID, at.Add(time.Minute), true, "reply"))
	if tr.alert != notify.EventUp {
		t.Fatalf("recovery: alert = %q, want the recovery to be announced", tr.alert)
	}
	if tr.current != model.StatusUp {
		t.Fatalf("recovery: status = %q, want up", tr.current)
	}
}

// TestReconcileBeforeStartStartsNoLoops keeps the two halves of the contract
// apart: Reconcile is reachable from inside an HTTP request, Start owns the
// loops.
//
// The API handlers call Reconcile after every change to the sensor list, and the
// context they hand it is the request's. A loop that took that context as its
// parent dies when the response is written — partway through its first probe —
// and the probe that does land before then writes a row into a table the same
// request has just counted. That is not hypothetical: it is what made the API's
// example-data test fail in CI now and then, and nothing in either package said
// so.
func TestReconcileBeforeStartStartsNoLoops(t *testing.T) {
	s, st := newTestScheduler(t)
	ctx := context.Background()

	// A target that refuses at once, so that if a loop did start, its first probe
	// would reach the store almost immediately.
	c := model.Sensor{
		Name:     "Refuses at once",
		Type:     model.SensorTCP,
		Target:   "127.0.0.1:1",
		Interval: 60,
		Timeout:  5,
		Retries:  1,
		Enabled:  true,
	}
	store.NormaliseSensor(&c)
	if err := st.CreateSensor(ctx, &c); err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	if err := s.Reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n := runnerCount(s); n != 0 {
		t.Errorf("Reconcile started %d probe loop(s) on a scheduler that was never "+
			"started, so their lifetime is the caller's and not the daemon's; the "+
			"loops belong to Start", n)
	}

	// The state, unlike the loop, is still rebuilt: it is what the sensor list on
	// screen reads, and it is why Reconcile is worth calling at all here.
	if _, ok := s.Snapshots()[c.ID]; !ok {
		t.Error("Reconcile before Start left the sensor without a state, so the " +
			"dashboard would show it as unknown until the daemon restarted")
	}

	// And Start still starts them — the check above must not have turned Reconcile
	// into a way of never probing at all.
	if err := s.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(s.Stop)
	if n := runnerCount(s); n != 1 {
		t.Errorf("Start left %d probe loop(s) running, want the one for the sensor", n)
	}
}
