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

	"github.com/j000k3r/weatherglass/internal/event"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/notify"
	"github.com/j000k3r/weatherglass/internal/sensor"
	"github.com/j000k3r/weatherglass/internal/store"
)

const (
	// maintenanceInterval is how often rollups and retention run.
	maintenanceInterval = 5 * time.Minute
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

	desired := make(map[int64]model.Sensor, len(sensors))
	for _, c := range sensors {
		if c.Enabled {
			desired[c.ID] = c
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
		// The sensor went down while Weatherglass was not running. Reconstruct
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
	if err := s.store.InsertResult(ctx, &res); err != nil {
		s.log.Error("store probe result", "sensor", c.Name, "error", err)
	}

	s.applyOutcome(ctx, c, res)

	s.broker.Publish(event.Event{Kind: event.KindResult, SensorID: c.ID, Result: &res})
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
			tr.alert = notify.EventUp
			st.lastNotify = res.TS
		}
	} else {
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
			tr.incident = s.openIncident(ctx, c, st, start, res.Message)
			tr.alert = notify.EventDown
			st.lastNotify = res.TS

		case st.status == model.StatusDown && c.ReminderEvery > 0:
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
	if err := s.store.InsertResult(ctx, &res); err != nil {
		return res, err
	}

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
