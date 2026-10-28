// Package scheduler runs the probes.
//
// One goroutine per enabled sensor, each on its own interval. A small state
// machine per sensor turns individual probe outcomes into status transitions,
// incidents and alerts:
//
//	unknown --first success--> up
//	unknown --N failures-----> down  (N = sensor.Retries, at least 1)
//	up      --first failure--> warn  (a blip: no incident, no alert)
//	warn    --success--------> up
//	warn    --N failures-----> down  (opens an incident, alerts)
//	up      --N failures-----> down  (opens an incident, alerts)
//	down    --success--------> up    (closes the incident, alerts)
//	down    --every reminder-> reminder alert while it stays down
//
// The warn state is the one that used to be invisible: a single failed probe
// above a sensor that was up left it looking green, and after a restart it read
// as "unknown", which is the word for a sensor nothing is known about at all.
// It is neither. Nothing is announced while a sensor is yellow — an alert for
// every blip would be noise, and the incident log is for outages.
//
// State is rebuilt from the database on startup, including still-open
// incidents and the length of the running failure streak. A container restart
// therefore neither loses an outage nor re-announces one that is already known.
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sync"
	"time"

	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/sensor"
	"github.com/j000k3r/probeglass/internal/store"
)

const (
	// maintenanceInterval is how often rollups and retention run.
	maintenanceInterval = 5 * time.Minute
	// escalationInterval is how often open incidents are checked for a due
	// escalation. Shorter than the maintenance pass because it is about how long
	// a person waits, not how long a rollup takes.
	escalationInterval = 30 * time.Second
	// maxStartupJitter spreads the first probe of every sensor after a restart.
	maxStartupJitter = 3 * time.Second
	// intervalJitter is the relative spread applied to every interval so that
	// sensors created together do not stay in lockstep forever.
	intervalJitter = 0.05
	// stateLookback bounds how far back the failure streak is reconstructed.
	stateLookback = 200
)

// state is the runtime bookkeeping for one sensor.
type state struct {
	status     model.Status
	since      time.Time
	failStreak int
	firstFail  time.Time
	incidentID int64
	lastNotify time.Time
	lastResult *model.Result

	// excused records that the sensor is currently down for a reason that is not its
	// own: a maintenance window is running over it, or something it sits behind is
	// down. It is what lets the failure branch tell "this has been failing for a
	// while and is still failing" from "this began failing", and it is why a spell of
	// being excused that ends while the box is still away produces a new incident
	// rather than silence.
	excused bool
}

// Snapshot is a read-only copy of a sensor's runtime state.
type Snapshot struct {
	Status     model.Status  `json:"status"`
	Since      time.Time     `json:"since"`
	FailStreak int           `json:"fail_streak"`
	LastResult *model.Result `json:"last_result,omitempty"`
	IncidentID int64         `json:"incident_id,omitempty"`
}

type runner struct {
	cancel context.CancelFunc
	sensor model.Sensor
}

// Scheduler owns the probe loops and the state machine.
type Scheduler struct {
	store      *store.Store
	dispatcher *notify.Dispatcher
	broker     *event.Broker
	log        *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	runners map[int64]*runner
	states  map[int64]*state
	// sensors is every sensor, read whole, for the same reason windows is: the
	// question "is this one's parent down" is asked once per probe, and answering it
	// by name needs the list. Kept in step by Reconcile, which already reads it.
	sensors []model.Sensor
	// windows is every maintenance window, read whole. It is held rather than
	// queried because the question "is this sensor inside a window right now" is
	// asked once per probe per sensor, and an installation has a handful of
	// windows; a query per probe would be a query every few seconds to answer
	// something that changes when somebody edits a form.
	windows []model.MaintenanceWindow
}

// New builds a scheduler. It does nothing until Start is called.
func New(st *store.Store, dispatcher *notify.Dispatcher, broker *event.Broker, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{
		store:      st,
		dispatcher: dispatcher,
		broker:     broker,
		log:        log,
		runners:    map[int64]*runner{},
		states:     map[int64]*state{},
	}
}

// excuseResult decides whether a probe is excused and records why.
//
// Split out of execute so that step can be driven without a probe. execute is the only
// caller that has a real result in hand before the row is written, and step is the
// only caller that acts on the answer — a test that drove step directly would be
// asserting on a flag nobody had set, which is how these tests passed while the
// feature did nothing.
func (s *Scheduler) excuseResult(c model.Sensor, res *model.Result) {
	res.Excused, res.Excuse = s.excuseOf(c, res.TS)
}

// excuseOf says whether a probe should be excused, and for what reason.
//
// Two reasons, one mechanism. A maintenance window is something a person said in
// advance; a parent that is down is something that happened to the sensor rather than
// by it. Both mean the same thing for the arithmetic — this sample is not evidence
// about the sensor — and a second mechanism would have needed a second copy of the
// boundary handling in step, which is where the subtle bugs live.
//
// The caller must hold s.mu, and this is the whole reason: step() holds it for its
// entire body, and every public reader — Snapshot, Snapshots, InWindow — takes the
// same non-reentrant lock, so calling one from in there deadlocks the probe loop. The
// parent chain is read straight out of s.states instead, where the lock is already in
// hand.
//
// A dependency wins over a window when both apply. Both are true, and only one badge
// fits: a parent that went down is the newer fact, and the one somebody reading the
// dashboard does not already know.
func (s *Scheduler) excuseOf(c model.Sensor, at time.Time) (bool, string) {
	if s.parentIsDown(c) {
		return true, model.ExcuseDependency
	}
	if store.ActiveWindows(s.windows, c, at) {
		return true, model.ExcuseMaintenance
	}
	return false, ""
}

// parentIsDown walks up the chain this sensor sits behind and reports whether any of
// them is down. The caller must hold s.mu.
//
// Down and not warn, on purpose: a blip on the router must not mark the thirty things
// behind it as excused, or one lost ping would quietly stop the whole installation
// from alerting. Only a status that has been confirmed by the retries counts, which is
// exactly what StatusDown means.
//
// The walk is bounded. A cycle cannot get in through the API or the configuration file
// — both are checked — but a database edited by hand is not the API, and a probe loop
// that never returns is worse than one that stops early and judges the sensor on its
// own evidence.
func (s *Scheduler) parentIsDown(c model.Sensor) bool {
	for depth, name := 0, c.Parent; name != "" && depth < model.MaxParentDepth; depth++ {
		parent, ok := s.sensorByName(name)
		if !ok {
			// A parent that is not there is not a reason to excuse anything: the
			// sensor is on its own, and its own probes are the evidence.
			return false
		}
		if st := s.states[parent.ID]; st != nil && st.status == model.StatusDown {
			return true
		}
		name = parent.Parent
	}
	return false
}

// sensorByName finds a sensor in the list the scheduler was last given. The caller
// must hold s.mu.
func (s *Scheduler) sensorByName(name string) (model.Sensor, bool) {
	for _, c := range s.sensors {
		if c.Name == name {
			return c, true
		}
	}
	return model.Sensor{}, false
}

// InWindow reports whether a sensor is inside a maintenance window at an instant.
//
// This one answer decides two things that have to agree: whether the probe result
// is marked as maintenance (and therefore left out of the uptime figure) and whether
// the failure that produced it is allowed to open an incident. Both come from
// model.MaintenanceWindow.Covers, so they cannot disagree.
//
// Exported because the API asks it too, when it draws a card. A third answer computed
// somewhere else is a card that says "expected" while the scheduler is opening an
// incident, which is the two halves of this feature disagreeing in public.
func (s *Scheduler) InWindow(c model.Sensor, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return store.ActiveWindows(s.windows, c, at)
}

// ReloadWindows re-reads the maintenance windows.
//
// Called when the scheduler starts and after any change to them, which is why it has
// to be safe to call from an HTTP request: Reconcile explains the same thing for the
// probe loops, and the answer is the same — the list is data, the loops are
// goroutines.
func (s *Scheduler) ReloadWindows(ctx context.Context) error {
	windows, err := s.store.ListWindows(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.windows = windows
	s.mu.Unlock()
	return nil
}

// Start launches the probe loops and the maintenance pass.
func (s *Scheduler) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	s.ctx = ctx
	s.cancel = cancel

	if err := s.Reconcile(ctx); err != nil {
		return err
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.maintenanceLoop(ctx)
	}()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.escalationLoop(ctx)
	}()
	return nil
}

// Stop cancels every probe loop and waits for them to finish.
func (s *Scheduler) Stop() {
	if s.cancel == nil {
		return
	}
	s.cancel()
	s.wg.Wait()
}

// Reconcile brings the running loops in line with the stored sensors: it starts
// loops for new or changed sensors and stops loops for removed or disabled
// ones. It is safe to call at any time.
//
// On a scheduler that was never started it reads the sensors and rebuilds their
// state, and stops there: running those loops belongs to Start, which calls
// Reconcile itself on the way up. The API handlers call Reconcile from inside an
// HTTP request — after a sensor is added, removed, imported or replaced — and a
// loop that took the request as its parent would die with the response, halfway
// through its first probe. Which is not hypothetical: it was what this did, and
// the one probe that got in before the response was flushed was one row in a
// table a test had just counted.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	sensors, err := s.store.ListSensors(ctx)
	if err != nil {
		return err
	}
	// Read before the lock, like the sensors above: the mutex guards the in-memory
	// state, and holding it across a query would serialise every probe loop behind
	// a database read. The windows come along on every reconcile rather than only
	// when they change, because this is also the path a restart takes and because a
	// stale list here would be a sensor that is silently not being excused.
	windows, err := s.store.ListWindows(ctx)
	if err != nil {
		return err
	}

	desired := make(map[int64]model.Sensor, len(sensors))
	for _, c := range sensors {
		if c.Enabled {
			desired[c.ID] = c
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.windows = windows
	s.sensors = sensors

	// Stop loops that are gone or newly disabled.
	for id, r := range s.runners {
		if _, ok := desired[id]; ok {
			continue
		}
		r.cancel()
		delete(s.runners, id)
		if st := s.states[id]; st != nil {
			st.status = model.StatusPaused
			st.failStreak = 0
			st.firstFail = time.Time{}
		}
		// A disabled sensor cannot be down; resolve anything still open so the
		// dashboard does not show a permanent outage.
		if err := s.store.CloseOpenIncidents(ctx, id, time.Now().UTC()); err != nil {
			s.log.Error("close incidents for disabled sensor", "sensor_id", id, "error", err)
		}
	}

	for id, c := range desired {
		if r, ok := s.runners[id]; ok {
			if reflect.DeepEqual(r.sensor, c) {
				continue
			}
			// Configuration changed: restart the loop so the new interval,
			// timeout and options take effect immediately.
			r.cancel()
			delete(s.runners, id)
		}
		if _, ok := s.states[id]; !ok {
			s.restoreState(ctx, c)
		}
		// Nothing probes before Start. The state above is what the dashboard reads
		// through Snapshots, and it is rebuilt here so that a sensor added while the
		// daemon is up is not reported as unknown for the rest of the request; the
		// loop is Start's business.
		parent := s.ctx
		if parent == nil {
			continue
		}
		loopCtx, cancel := context.WithCancel(parent)
		s.runners[id] = &runner{cancel: cancel, sensor: c}
		s.wg.Add(1)
		go s.loop(loopCtx, c)
	}

	// And once more over everything, now that every state exists: a sensor that is
	// down behind a parent that is also down has an incident it should not have.
	//
	// It is a second pass and not a check inside restoreState because restoreState
	// works one sensor at a time, in map order, and a child restored before its parent
	// would find no status to look at and rebuild the incident anyway. Ordering the
	// restore itself would mean sorting the whole set by the chain it sits in; walking
	// it again afterwards costs one loop and cannot be got wrong.
	for id, c := range desired {
		if !s.parentIsDown(c) {
			continue
		}
		st := s.states[id]
		if st == nil || st.incidentID == 0 {
			continue
		}
		// Closed with the reason, the same way a maintenance window closes one: an
		// incident that was reconstructed from the history a moment ago, for an
		// outage this sensor did not have.
		if tr := s.closeIncident(ctx, c, st, st.since, time.Now().UTC()); tr != nil {
			s.log.Info("closed an incident a restart reconstructed for a sensor behind "+
				"a parent that is down", "sensor", c.Name, "incident", tr.ID)
		}
		st.excused = true
	}
	return nil
}

// restoreState rebuilds a sensor's runtime state from stored data.
// The caller must hold s.mu.
func (s *Scheduler) restoreState(ctx context.Context, c model.Sensor) {
	st := &state{status: model.StatusUnknown, since: time.Now().UTC()}

	count, firstFail, lastSuccess, err := s.store.TrailingFailures(ctx, c.ID, stateLookback)
	if err != nil {
		s.log.Error("read trailing failures", "sensor", c.Name, "error", err)
	}
	st.failStreak = count
	st.firstFail = firstFail

	// The newest probe, so that a restart does not blank the dashboard. The status
	// above is decided from the history; this is the rest of what the cards draw —
	// the latency, the reply and when the sensor was last heard from — and without it
	// every sensor reads "up" with no reading beside it until its own first probe
	// lands, which is up to the startup jitter later. Nothing in the scheduler decides
	// anything from it, so restoring it can only change what is on screen.
	if last, err := s.store.LastResult(ctx, c.ID); err != nil {
		s.log.Error("read last result", "sensor", c.Name, "error", err)
	} else if last != nil {
		st.lastResult = last
	}

	open, err := s.store.OpenIncidentFor(ctx, c.ID)
	if err != nil {
		s.log.Error("read open incident", "sensor", c.Name, "error", err)
	}

	threshold := thresholdFor(c)
	switch {
	case open != nil:
		st.status = model.StatusDown
		st.since = open.StartedAt
		st.incidentID = open.ID
		st.lastNotify = time.Now().UTC()
	case count >= threshold:
		// The sensor went down while ProbeGlass was not running. Reconstruct
		// the incident from the results so the outage is not lost.
		start := firstFail
		if start.IsZero() {
			start = time.Now().UTC()
		}
		id, err := s.store.OpenIncident(ctx, c.ID, start, "reconstructed after restart")
		if err != nil {
			s.log.Error("reconstruct incident", "sensor", c.Name, "error", err)
		}
		st.status = model.StatusDown
		st.since = start
		st.incidentID = id
		st.lastNotify = time.Now().UTC()
		s.log.Info("reconstructed ongoing outage", "sensor", c.Name, "since", start)
	case count > 0:
		// Inside the grace period: the sensor has failed, but not often enough to
		// call it down. Yellow says exactly that much and no more — it used to say
		// "unknown", which is the word for a sensor nothing is known about.
		st.status = model.StatusWarn
		st.since = firstFail
		if st.since.IsZero() {
			st.since = time.Now().UTC()
		}
	default:
		if !lastSuccess.IsZero() {
			st.status = model.StatusUp
			st.since = lastSuccess
		}
	}
	s.states[c.ID] = st
}

// loop probes one sensor forever.
func (s *Scheduler) loop(ctx context.Context, c model.Sensor) {
	defer s.wg.Done()

	interval := time.Duration(c.Interval) * time.Second
	if interval <= 0 {
		interval = time.Minute
	}

	jitter := time.Duration(rand.Int64N(int64(maxStartupJitter) + 1))
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}

	s.execute(ctx, c)

	for {
		delay := applyJitter(interval)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.execute(ctx, c)
		}
	}
}

func applyJitter(d time.Duration) time.Duration {
	spread := float64(d) * intervalJitter
	if spread < 1 {
		return d
	}
	return d + time.Duration((rand.Float64()*2-1)*spread)
}

// execute runs one probe, stores the result and drives the state machine.
func (s *Scheduler) execute(ctx context.Context, c model.Sensor) {
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
	defer cancel()

	started := time.Now()
	out := runProber(probeCtx, c)
	latency := out.Latency
	if latency <= 0 {
		latency = time.Since(started)
	}

	res := model.Result{
		SensorID:   c.ID,
		TS:         time.Now().UTC(),
		Success:    out.Success,
		LatencyMS:  round2(float64(latency.Microseconds()) / 1000),
		StatusCode: out.StatusCode,
		Message:    out.Message,
	}
	// Marked here, at the only moment that knows both the sensor and the clock, and
	// before the row is written: the marker is what keeps this sample out of the
	// uptime figure, and by the time anybody reads it back the window it fell in may
	// have been deleted. The probe is still made and still stored — "expected" is a
	// fact about the outage, not about the measurement, and the chart should show
	// that the box really was away. A check somebody asked for by hand is marked the
	// same way, because it is the same probe.
	s.excuseResult(c, &res)

	if err := s.store.InsertResult(ctx, &res); err != nil {
		s.log.Error("store probe result", "sensor", c.Name, "error", err)
	}

	// The inventory follows the probe, not the state machine: a certificate is
	// worth keeping whether the page behind it answered or not.
	s.recordCertificate(ctx, c, out)

	s.applyOutcome(ctx, c, res)

	s.broker.Publish(event.Event{Kind: event.KindResult, SensorID: c.ID, Result: &res})
}

// recordCertificate keeps the certificate inventory in step with the probe that
// just ran.
//
// Only the three types that can meet a TLS certificate take part. A probe that
// finished without one — plain HTTP, TCP without TLS — clears the sensor's row,
// because that is a fact about the target changing rather than a failed
// measurement. A probe that never got as far as a handshake changes nothing: the
// certificate it could not reach may well still be the one in use, and a page
// that emptied itself on every timeout would say less the worse things got.
func (s *Scheduler) recordCertificate(ctx context.Context, c model.Sensor, out sensor.Outcome) {
	switch c.Type {
	case model.SensorHTTP, model.SensorTCP, model.SensorTLS:
	default:
		return
	}
	if out.Certificate != nil {
		cert := *out.Certificate
		cert.SensorID = c.ID
		cert.WarnDays = sensor.CertWarnDays(c)
		cert.ObservedAt = time.Now().UTC()
		if err := s.store.SaveCertificate(ctx, cert); err != nil {
			s.log.Error("store certificate", "sensor", c.Name, "error", err)
		}
		return
	}
	if !out.Success {
		return
	}
	if err := s.store.DeleteCertificate(ctx, c.ID); err != nil {
		s.log.Error("forget certificate", "sensor", c.Name, "error", err)
	}
}

// transition is the outcome of one state-machine step.
type transition struct {
	previous      model.Status
	current       model.Status
	statusChanged bool
	alert         notify.Event
	incident      *model.Incident
}

// applyOutcome drives the state machine for a probe outcome and emits alerts.
//
// The state transition happens under the mutex, together with the incident
// bookkeeping, so the two can never disagree. Everything that talks to the
// outside world (logging, event fan-out, alert delivery) happens afterwards,
// outside the lock.
func (s *Scheduler) applyOutcome(ctx context.Context, c model.Sensor, res model.Result) {
	tr := s.step(ctx, c, res)

	if tr.statusChanged {
		s.log.Info("sensor status changed",
			"sensor", c.Name, "from", string(tr.previous), "to", string(tr.current),
			"message", res.Message)
		s.broker.Publish(event.Event{
			Kind: event.KindStatus, SensorID: c.ID, Status: tr.current, Result: &res,
		})
	}
	if tr.incident != nil {
		s.broker.Publish(event.Event{
			Kind: event.KindIncident, SensorID: c.ID, Incident: tr.incident, Result: &res,
		})
	}
	if tr.alert != "" {
		// A scheduler built without a dispatcher sends nothing. That is a real
		// configuration — the tests build one, and alerting is not what the
		// scheduler is for — but it must not be a silent one, and above all it
		// must not be a fatal one. The call below runs in its own goroutine, so a
		// send on a nil dispatcher dereferences a nil pointer where there is no
		// caller to catch it: the panic takes the whole daemon down, and a
		// watchdog that dies while trying to tell somebody about an outage is a
		// worse outcome than the outage. So the mistake is logged and the probe
		// loop carries on.
		if s.dispatcher == nil {
			s.log.Error("an alert came due but this scheduler has no dispatcher, "+
				"so nothing was sent", "sensor", c.Name, "event", tr.alert)
			return
		}
		// Delivery happens off the scheduler goroutine: a slow chat webhook
		// must never delay the next probe. WithoutCancel keeps the request
		// alive if the sensor itself is being shut down.
		go s.dispatcher.Notify(context.WithoutCancel(ctx), c, tr.alert, res, tr.incident)
	}
}

// step performs the state transition for one outcome.
//
// Database calls happen while the mutex is held. That is deliberate: the
// statements are single-row writes on a serialised connection, and holding the
// lock keeps the state transitions and the incident rows consistent with each
// other, which matters far more than the microseconds it costs.
func (s *Scheduler) step(ctx context.Context, c model.Sensor, res model.Result) transition {
	s.mu.Lock()
	defer s.mu.Unlock()

	st := s.states[c.ID]
	if st == nil {
		st = &state{status: model.StatusUnknown, since: res.TS}
		s.states[c.ID] = st
	}
	st.lastResult = &res

	threshold := thresholdFor(c)
	tr := transition{previous: st.status, current: st.status}

	if res.Success {
		st.failStreak = 0
		st.firstFail = time.Time{}
		st.lastNotify = time.Time{}
		st.excused = false

		// Read the outage start before the status timestamp is rewritten below:
		// st.since is about to become the recovery time, and the recovery alert
		// reports how long the sensor was down.
		downSince := st.since

		if st.status != model.StatusUp {
			st.status = model.StatusUp
			st.since = res.TS
			tr.statusChanged = true
		}
		if tr.previous == model.StatusDown {
			tr.incident = s.closeIncident(ctx, c, st, downSince, res.TS)
			// No recovery alert for a probe inside a window. If the sensor went
			// down and came back inside one, no "down" was ever sent — an "up"
			// with nothing before it is a message about a problem the reader was
			// never told about. If the outage predates the window, the window
			// closed it with a note, and the recovery is reported when the window
			// ends and the sensor is checked outside it again.
			if !res.Excused {
				tr.alert = notify.EventUp
				st.lastNotify = res.TS
			}
		}
	} else {
		// The window boundaries are noticed here, on the first failure either side of
		// one, because a probe is the only thing that comes with a clock reading.
		switch {
		case res.Excused && !st.excused:
			st.excused = true
			// An outage that was already running. It was opened when the downtime
			// was not expected and it is expected now, so it ends — with a note,
			// because a closed incident that does not say why is worse than one
			// that was never opened. Nothing is sent: the message that opened it
			// went out, and a second one saying the maintenance has started is a
			// message about a message.
			if st.incidentID != 0 {
				tr.incident = s.closeIncident(ctx, c, st, st.since, res.TS)
			}
		case !res.Excused && st.excused:
			// The window has ended and the sensor is still away. What failed in
			// between was expected, so none of it is evidence about now: the streak
			// starts again from this probe, and the sensor has to fail its way to
			// the threshold like anything else before it is called down. The status
			// follows the streak, so a box that has been away for an hour goes back
			// to "flaky" for the retries — which is the honest word for "we know it
			// was away and we are finding out whether it still is".
			st.excused = false
			st.failStreak = 0
			st.firstFail = time.Time{}
			if st.status == model.StatusDown {
				st.status = model.StatusWarn
				tr.statusChanged = true
			}
		}

		if st.failStreak == 0 {
			st.firstFail = res.TS
		}
		st.failStreak++

		switch {
		case st.failStreak > 0 && st.failStreak < threshold && st.status != model.StatusDown:
			// A blip: the last probe failed, the retries are not used up. The sensor
			// may be on its way down or already on its way back, and until the streak
			// reaches the threshold there is no honest way to tell those apart — so
			// the status says that much and nothing more. No incident, no alert: the
			// incident log is for outages, and a message per blip is noise.
			st.status = model.StatusWarn
			st.since = st.firstFail
			tr.statusChanged = tr.previous != model.StatusWarn

		case st.failStreak >= threshold && st.status != model.StatusDown:
			start := st.firstFail
			if start.IsZero() {
				start = res.TS
			}
			st.status = model.StatusDown
			st.since = start
			tr.statusChanged = true
			// The status goes down whether or not a window is running — the box
			// really is away, and a card that stayed green through a scheduled
			// outage would be lying about the one thing it is for. What a window
			// suppresses is the incident and the message, which are claims that
			// somebody needs to do something.
			if res.Excused {
				break
			}
			tr.incident = s.openIncident(ctx, c, st, start, res.Message)
			tr.alert = notify.EventDown
			st.lastNotify = res.TS

		case st.status == model.StatusDown && st.incidentID == 0 && st.failStreak >= threshold && !st.excused:
			// Down, with no incident against it, and nothing to blame. This is the
			// sensor that a window — or a parent — closed the book on and that never
			// came back: the streak has climbed to the threshold again on its own, so
			// this is an
			// outage like any other, with its start at the first failure after the
			// window rather than at the one before it.
			start := st.firstFail
			if start.IsZero() {
				start = res.TS
			}
			st.since = start
			tr.incident = s.openIncident(ctx, c, st, start, res.Message)
			tr.alert = notify.EventDown
			st.lastNotify = res.TS

		case st.status == model.StatusDown && c.ReminderEvery > 0 && !res.Excused:
			if res.TS.Sub(st.lastNotify) >= time.Duration(c.ReminderEvery)*time.Second {
				open, err := s.store.OpenIncidentFor(ctx, c.ID)
				if err != nil {
					// Could not read the outage, so it cannot be known whether it
					// was acknowledged. Remaining silent would be the wrong guess
					// — the note is a courtesy, not a promise to stop.
					break
				}
				tr.incident = open
				if open == nil || !open.Acknowledged() {
					tr.alert = notify.EventReminder
					// lastNotify moves only when an alert actually goes out. The
					// acknowledgement therefore does not have to be undone to
					// resume: taking it back leaves the reminder overdue, and the
					// next probe fires one immediately.
					st.lastNotify = res.TS
				}
			}
		}
	}

	tr.current = st.status
	return tr
}

// openIncident records the start of an outage.
// The caller must hold s.mu.
func (s *Scheduler) openIncident(ctx context.Context, c model.Sensor, st *state, start time.Time, cause string) *model.Incident {
	id, err := s.store.OpenIncident(ctx, c.ID, start, truncate(cause, 500))
	if err != nil {
		s.log.Error("open incident", "sensor", c.Name, "error", err)
		return nil
	}
	st.incidentID = id
	return &model.Incident{ID: id, SensorID: c.ID, StartedAt: start, Cause: truncate(cause, 500)}
}

// closeIncident resolves the running outage. started is the moment the outage
// began, which the caller passes in because the caller has already moved the
// sensor's status timestamp on to the recovery time.
// The caller must hold s.mu.
func (s *Scheduler) closeIncident(ctx context.Context, c model.Sensor, st *state, started, at time.Time) *model.Incident {
	if st.incidentID != 0 {
		if err := s.store.CloseIncident(ctx, st.incidentID, at); err != nil {
			s.log.Error("close incident", "sensor", c.Name, "error", err)
		}
		inc := &model.Incident{ID: st.incidentID, SensorID: c.ID, StartedAt: started, ResolvedAt: &at}
		st.incidentID = 0
		return inc
	}
	// No tracked incident id: repair any row that is still open.
	if err := s.store.CloseOpenIncidents(ctx, c.ID, at); err != nil {
		s.log.Error("close open incidents", "sensor", c.Name, "error", err)
	}
	return nil
}

// ProbeNow runs a sensor once without touching its state or firing alerts. It
// backs the "Run now" button in the dashboard: a manual probe is informational.
func (s *Scheduler) ProbeNow(ctx context.Context, sensorID int64) (model.Result, error) {
	c, err := s.store.GetSensor(ctx, sensorID)
	if err != nil {
		return model.Result{}, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Duration(c.Timeout)*time.Second)
	defer cancel()

	started := time.Now()
	out := runProber(probeCtx, c)
	latency := out.Latency
	if latency <= 0 {
		latency = time.Since(started)
	}
	res := model.Result{
		SensorID:   c.ID,
		TS:         time.Now().UTC(),
		Success:    out.Success,
		LatencyMS:  round2(float64(latency.Microseconds()) / 1000),
		StatusCode: out.StatusCode,
		Message:    out.Message,
	}
	// Marked here, at the only moment that knows both the sensor and the clock, and
	// before the row is written: the marker is what keeps this sample out of the
	// uptime figure, and by the time anybody reads it back the window it fell in may
	// have been deleted. The probe is still made and still stored — "expected" is a
	// fact about the outage, not about the measurement, and the chart should show
	// that the box really was away. A check somebody asked for by hand is marked the
	// same way, because it is the same probe.
	s.excuseResult(c, &res)

	if err := s.store.InsertResult(ctx, &res); err != nil {
		return res, err
	}
	s.recordCertificate(ctx, c, out)

	s.mu.Lock()
	if st := s.states[c.ID]; st != nil {
		st.lastResult = &res
	}
	s.mu.Unlock()

	s.broker.Publish(event.Event{Kind: event.KindResult, SensorID: c.ID, Result: &res})
	return res, nil
}

// Snapshots returns the runtime state of every known sensor.
func (s *Scheduler) Snapshots() map[int64]Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]Snapshot, len(s.states))
	for id, st := range s.states {
		out[id] = Snapshot{
			Status:     st.status,
			Since:      st.since,
			FailStreak: st.failStreak,
			LastResult: st.lastResult,
			IncidentID: st.incidentID,
		}
	}
	return out
}

// Snapshot returns the runtime state of one sensor.
func (s *Scheduler) Snapshot(sensorID int64) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[sensorID]
	if !ok {
		return Snapshot{Status: model.StatusUnknown}
	}
	return Snapshot{
		Status:     st.status,
		Since:      st.since,
		FailStreak: st.failStreak,
		LastResult: st.lastResult,
		IncidentID: st.incidentID,
	}
}

// Forget drops the runtime state of a deleted sensor.
func (s *Scheduler) Forget(sensorID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runners[sensorID]; ok {
		r.cancel()
		delete(s.runners, sensorID)
	}
	delete(s.states, sensorID)
}

func (s *Scheduler) maintenanceLoop(ctx context.Context) {
	run := func() {
		mctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		if err := s.store.Maintain(mctx, time.Now()); err != nil {
			// A pass interrupted by the shutdown is not a failed pass. Without this,
			// every daemon that stops while a pass is running — the hourly rollup can
			// take a moment — writes an error into its log on the way out, and a line
			// that means "this is normal" teaches its reader to skim past the ones
			// that do not.
			if mctx.Err() != nil {
				return
			}
			s.log.Error("maintenance pass failed", "error", err)
			return
		}
		s.log.Debug("maintenance pass complete")
	}

	run()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func (s *Scheduler) escalationLoop(ctx context.Context) {
	// The first sweep runs immediately: a restart during an outage must not wait
	// another half minute to tell the second channel what the first one already
	// knows.
	s.escalateDue(ctx)
	ticker := time.NewTicker(escalationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.escalateDue(ctx)
		}
	}
}

// escalateDue sends the one escalation message an incident gets.
//
// It is a sweep rather than something the probe path does, because escalation is
// about the clock: a sensor with a fifteen-minute interval would otherwise
// escalate fifteen minutes late, and an escalation that arrives after somebody
// has already fixed the box is noise. The incident row records that the message
// went out, so a restart or a second sweep cannot send it again; a failed send is
// not retried, because the dispatcher has already tried three times and another
// attempt every half minute would be a worse problem than the silence.
func (s *Scheduler) escalateDue(ctx context.Context) {
	opens, err := s.store.OpenIncidents(ctx)
	if err != nil {
		s.log.Error("escalation sweep failed", "error", err)
		return
	}
	now := time.Now().UTC()
	for _, inc := range opens {
		if inc.EscalatedAt != nil || inc.Acknowledged() {
			continue
		}
		sensor, err := s.store.GetSensor(ctx, inc.SensorID)
		if err != nil {
			continue
		}
		if sensor.EscalateAfter <= 0 || len(sensor.EscalateChannels) == 0 {
			continue
		}
		if now.Sub(inc.StartedAt) < time.Duration(sensor.EscalateAfter)*time.Second {
			continue
		}
		if s.dispatcher == nil {
			return
		}
		// Mark first: the message must go out once, not once per sweep.
		if err := s.store.MarkIncidentEscalated(ctx, inc.ID, now); err != nil {
			s.log.Error("could not mark an incident escalated", "incident", inc.ID, "error", err)
			continue
		}
		incident := inc
		res := model.Result{
			SensorID: sensor.ID,
			TS:       incident.StartedAt,
			Success:  false,
			Message:  incident.Cause,
		}
		go s.dispatcher.NotifyEscalation(context.WithoutCancel(ctx), sensor, res, &incident)
		s.log.Info("escalation alert sent", "sensor", sensor.Name, "incident", incident.ID)
	}
}

// runProber dispatches to the right engine, defending the scheduler against a
// prober that panics.
func runProber(ctx context.Context, c model.Sensor) (out sensor.Outcome) {
	defer func() {
		if p := recover(); p != nil {
			out = sensor.Errf(0, "prober panicked: %v", p)
		}
	}()
	ck, ok := sensor.Get(c.Type)
	if !ok {
		return sensor.Errf(0, "unsupported sensor type %q", c.Type)
	}
	return ck.Run(ctx, c)
}

func thresholdFor(c model.Sensor) int {
	if c.Retries < 1 {
		return 1
	}
	return c.Retries
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func round2(f float64) float64 {
	return float64(int64(f*100+0.5)) / 100
}
