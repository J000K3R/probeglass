package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/scheduler"
	"github.com/j000k3r/probeglass/internal/sensor"
	"github.com/j000k3r/probeglass/internal/store"
)

// ---------------------------------------------------------------------------
// meta
// ---------------------------------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if _, err := s.store.SchemaVersion(ctx); err != nil {
		// A 503 is a server-side failure like any other, so it carries its cause
		// into the log. The line names /healthz, which is what makes a failing
		// health check distinguishable from a failing dashboard request.
		s.fail(w, err, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": model.Version,
		"uptime":  time.Since(startedAt).Round(time.Second).String(),
	})
}

var startedAt = time.Now()

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":    model.Version,
		"build":      model.BuildInfo(),
		"commit":     model.Commit,
		"build_date": model.BuildDate,
		"channel":    model.BuildChannel,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(s.assets, "index.html")
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "dashboard assets are missing from this build")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-store, not no-cache. The shell names the script files to load, so a
	// stale copy of it is what pins a browser to a stale dashboard: the old
	// HTML asks for the old app.js, and the pair stays consistent — and broken
	// — no matter how the assets themselves are cached. Revalidating is not
	// enough when there is no validator to revalidate against; the file is a
	// few kilobytes served from memory, so simply refusing to store it is
	// cheaper than being clever.
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// ---------------------------------------------------------------------------
// live events
// ---------------------------------------------------------------------------

// handleEvents streams scheduler events over Server-Sent Events so the
// dashboard updates the moment a probe lands.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Unreachable through the real server: the middleware's recorder
		// implements Flush. Kept so that a differently wired server degrades to
		// a clear message rather than a silent hang.
		s.fail(w, nil, http.StatusInternalServerError, "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Stops nginx and friends from buffering the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.broker.Subscribe()
	defer cancel()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Kind, payload); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// ---------------------------------------------------------------------------
// dashboard summary
// ---------------------------------------------------------------------------

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sensors, err := s.store.ListSensors(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sensors")
		return
	}
	views, err := s.statusViews(ctx, sensors)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not build the dashboard")
		return
	}

	sum := model.Summary{Total: len(views)}
	for _, v := range views {
		switch v.Status {
		case model.StatusUp:
			sum.Up++
		case model.StatusDown:
			sum.Down++
		case model.StatusWarn:
			sum.Warn++
		case model.StatusPaused:
			sum.Paused++
		default:
			sum.Unknown++
		}
	}

	// The header shows "monitoring since" as the oldest probe still known.
	//
	// The question is asked of the results table and not of the scheduler, and that
	// is the whole of the reasoning: a scheduler only remembers the probes it has
	// seen, so a dashboard opened in the second after a restart would be told the
	// installation began watching just now — above a week of uptime charts, and
	// beside a settings page that is reporting the real oldest sample. The table is
	// the one place that knows. Nothing stored at all leaves the field at zero, and
	// the widget prints a dash rather than a date it made up.
	if oldest, _, err := s.store.DataSpan(ctx); err == nil {
		sum.Since = oldest
	}

	now := time.Now().UTC()
	if day, err := s.store.UptimeBySensor(ctx, now.Add(-24*time.Hour), now); err == nil {
		sum.Uptime24h, sum.AvgLatency, sum.Probes24h = headerFigures(day)
	}

	// The same tally over a week, so the header can put today's figure beside
	// what this installation usually does. A failure to read it is not a failure
	// to draw the header: the three week fields simply stay zero, and the tiles
	// that would have shown the comparison leave the line out.
	if week, err := s.store.UptimeBySensor(ctx, now.Add(-7*24*time.Hour), now); err == nil {
		sum.Uptime7d, sum.AvgLatency7d, sum.Probes7d = headerFigures(week)
	}

	if series, err := s.store.SummarySeries(ctx, now.Add(-24*time.Hour), now); err == nil {
		sum.Series = series
	}

	recent, err := s.store.ListIncidentsWithNames(ctx, store.IncidentFilter{Limit: 12})
	if err != nil {
		recent = nil
	}

	// The sites travel with the sensors rather than in a request of their own.
	// Three of the five layouts group or place the sensors by site, so every one
	// of them would otherwise open with a second round trip before it could draw
	// anything — and the tree would visibly re-shuffle itself when the second
	// answer arrived.
	sites, err := s.store.ListSites(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sites")
		return
	}
	// The per-site counts come out of the sensors already in hand, so carrying
	// them costs a loop and no query. Leaving them at zero was a real bug: the
	// Settings page reads its list from here when the dashboard loaded first, and
	// every site claimed to have no sensors on it while the tree beside it
	// plainly grouped four. The same numbers come from /api/sites, so the two
	// cannot disagree.
	counts := map[string]int{}
	for _, v := range views {
		if v.Site != "" {
			counts[v.Site]++
		}
	}
	for i := range sites {
		sites[i].SensorCount = counts[sites[i].Name]
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summary":   sum,
		"sensors":   views,
		"incidents": recent,
		"sites":     sites,
	})
}

// headerFigures tallies one window of per-sensor stats into the three numbers
// the header shows for it.
//
// The latency follows a different rule from the other two, and deliberately.
// Uptime and the probe count are counted over every probe, because a probe is a
// probe. The latency is the plain mean of the per-sensor means, which is the
// number on every card below — an installation whose one slow sensor answers
// every minute should still read as "one sensor is slow", not drag the header's
// figure along with the weight of its probe count. Weighting it by probes here
// would make the header disagree with the cards about the same installation.
//
// A sensor that has answered nothing in the window contributes no latency: a
// missing measurement is not a zero-millisecond one.
func headerFigures(per map[int64]store.UptimeStats) (uptime, latency float64, probes int) {
	uptime = 100
	var ok, measured int
	var latSum float64
	for _, st := range per {
		probes += st.Total
		ok += st.OK
		if st.Latency > 0 {
			latSum += st.Latency
			measured++
		}
	}
	if probes > 0 {
		uptime = float64(ok) / float64(probes) * 100
	}
	if measured > 0 {
		latency = latSum / float64(measured)
	}
	return uptime, latency, probes
}

// parentDown reports whether anything this sensor sits behind is down.
//
// The same question the scheduler asks, from the same two facts and in the same order
// — down and not warn, because a blip on the router must not excuse the thirty things
// behind it — so that a card and a probe loop cannot come to different conclusions
// about the same sensor.
func parentDown(byName map[string]model.Sensor, snaps map[int64]scheduler.Snapshot, c model.Sensor) bool {
	name := c.Parent
	for depth := 0; name != "" && depth < model.MaxParentDepth; depth++ {
		parent, ok := byName[name]
		if !ok {
			return false
		}
		if st, ok := snaps[parent.ID]; ok && st.Status == model.StatusDown {
			return true
		}
		name = parent.Parent
	}
	return false
}

// statusViews joins stored sensors with runtime state and uptime windows.
func (s *Server) statusViews(ctx context.Context, sensors []model.Sensor) ([]model.StatusView, error) {
	now := time.Now().UTC()
	snaps := s.sched.Snapshots()

	u24, err := s.store.UptimeBySensor(ctx, now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	u7, err := s.store.UptimeBySensor(ctx, now.Add(-7*24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	u30, err := s.store.UptimeBySensor(ctx, now.Add(-30*24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	recent, err := s.store.LastResults(ctx, 40)
	if err != nil {
		return nil, err
	}
	incidents, err := s.store.ListIncidentsWithNames(ctx, store.IncidentFilter{Limit: 200})
	if err != nil {
		return nil, err
	}
	// Read once for the whole list rather than per sensor, and the same helper the
	// scheduler uses: a card that said "expected" while the scheduler was opening an
	// incident would be the two halves of this feature disagreeing in public.
	windows, err := s.store.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]model.Sensor, len(sensors))
	for _, c := range sensors {
		byName[c.Name] = c
	}

	// newest incident per sensor, for the card footer
	lastIncident := map[int64]*model.Incident{}
	openIncident := map[int64]*model.Incident{}
	for i := range incidents {
		row := incidents[i]
		inc := row.Incident
		if _, seen := lastIncident[row.SensorID]; !seen {
			lastIncident[row.SensorID] = &inc
		}
		if inc.ResolvedAt == nil {
			if _, seen := openIncident[row.SensorID]; !seen {
				openIncident[row.SensorID] = &inc
			}
		}
	}

	views := make([]model.StatusView, 0, len(sensors))
	for _, c := range sensors {
		v := model.StatusView{
			Sensor: maskSensor(c),
			Status: model.StatusUnknown,
			Since:  c.CreatedAt,
		}
		if st, ok := snaps[c.ID]; ok {
			v.Status = st.Status
			v.FailStreak = st.FailStreak
			if !st.Since.IsZero() {
				v.Since = st.Since
			}
			if st.LastResult != nil {
				ts := st.LastResult.TS
				v.LastSensor = &ts
				v.LatencyMS = st.LastResult.LatencyMS
				v.StatusCode = st.LastResult.StatusCode
				v.Message = st.LastResult.Message
			}
		}
		if !c.Enabled {
			v.Status = model.StatusPaused
		}
		// The same two questions the scheduler asks, in the same order, for the same
		// answer: a card that said "expected" while the scheduler was opening an
		// incident would be the two halves of this disagreeing in public.
		if parentDown(byName, snaps, c) {
			v.Excuse = model.ExcuseDependency
		} else if store.ActiveWindows(windows, c, now) {
			v.Excuse = model.ExcuseMaintenance
		}
		if st, ok := u24[c.ID]; ok {
			v.Uptime24h = st.Percent
			v.AvgLatencyMS = st.Latency
		} else {
			v.Uptime24h = 100
		}
		if st, ok := u7[c.ID]; ok {
			v.Uptime7d = st.Percent
		} else {
			v.Uptime7d = 100
		}
		if st, ok := u30[c.ID]; ok {
			v.Uptime30d = st.Percent
		} else {
			v.Uptime30d = 100
		}
		v.Sparkline = sparkline(recent[c.ID])
		v.Heartbeat = heartbeat(recent[c.ID])
		v.OpenIncident = openIncident[c.ID]
		v.LastIncident = lastIncident[c.ID]
		views = append(views, v)
	}
	return views, nil
}

// heartbeat turns recent results into the plain up/down series the wall draws.
//
// It is deliberately not derived from the sparkline beside it. A sparkline point
// of zero means "this probe failed", but it is also what a genuinely instant
// success looks like, so a wall built on those numbers would eventually paint a
// working sensor red. The results carry the answer itself, so it is copied rather
// than inferred.
//
// The array runs oldest to newest, which is the order LastResults hands the rows
// back in and the order a heartbeat is read in: the newest check is the one that
// says what is happening now, and it belongs on the right.
func heartbeat(results []model.Result) []bool {
	out := make([]bool, len(results))
	for i, r := range results {
		out[i] = r.Success
	}
	return out
}

// sparkline turns recent results into latency points, using 0 for failures so
// the chart shows a visible dip rather than a gap.
func sparkline(results []model.Result) []float64 {
	out := make([]float64, 0, len(results))
	for _, r := range results {
		if r.Success {
			out = append(out, r.LatencyMS)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// sensors
// ---------------------------------------------------------------------------

// sensorInput is the body of a sensor create or update. Every field is a pointer
// so that "absent" is distinguishable from "empty": PUT is documented as a
// partial update, and a body carrying only {"target": "..."} must change the
// target and nothing else. With plain fields that body would blank the name,
// clear the group and reset the interval to its default.
type sensorInput struct {
	Name     *string `json:"name"`
	Type     *string `json:"type"`
	Target   *string `json:"target"`
	Interval *int    `json:"interval"`
	Timeout  *int    `json:"timeout"`
	Retries  *int    `json:"retries"`
	Enabled  *bool   `json:"enabled"`
	Group    *string `json:"group"`
	Site     *string `json:"site"`

	// Parent names another sensor this one sits behind. An explicit empty string
	// clears it, exactly as it clears the site.
	Parent        *string       `json:"parent"`
	Tags          *[]string     `json:"tags"`
	Options       model.Options `json:"options"`
	ChannelIDs    *[]int64      `json:"channel_ids"`
	ReminderEvery *int          `json:"reminder_every"`
	// Probe makes POST /api/sensors/validate also run the sensor once, which is
	// what the "Test" button in the dashboard form sends. It is ignored on
	// create and update.
	Probe bool `json:"probe"`
}

// toSensor applies the fields present in the request on top of base. Anything
// the client left out keeps its stored value; an explicit empty string or empty
// list clears it.
func (in sensorInput) toSensor(base model.Sensor) model.Sensor {
	c := base
	if in.Name != nil {
		c.Name = strings.TrimSpace(*in.Name)
	}
	if in.Type != nil {
		c.Type = model.SensorType(strings.ToLower(strings.TrimSpace(*in.Type)))
	}
	if in.Target != nil {
		c.Target = strings.TrimSpace(*in.Target)
	}
	if in.Interval != nil {
		c.Interval = *in.Interval
	}
	if in.Timeout != nil {
		c.Timeout = *in.Timeout
	}
	if in.Retries != nil {
		c.Retries = *in.Retries
	}
	if in.Group != nil {
		c.Group = strings.TrimSpace(*in.Group)
	}
	// An explicit empty string clears the site, exactly as it clears the group.
	// The store turns it into NULL; the column is a foreign key, and "" would be
	// a reference to a site that can never exist.
	if in.Site != nil {
		c.Site = strings.TrimSpace(*in.Site)
	}
	// The same shape as the site above, and for the same reason: the column is a
	// reference, and "" would be a reference to a sensor that can never exist.
	if in.Parent != nil {
		c.Parent = strings.TrimSpace(*in.Parent)
	}
	if in.Tags != nil {
		c.Tags = *in.Tags
	}
	if in.Options != nil {
		c.Options = in.Options
	}
	if in.ChannelIDs != nil {
		c.Channels = *in.ChannelIDs
	}
	if in.ReminderEvery != nil {
		c.ReminderEvery = *in.ReminderEvery
	}
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	}
	return c
}

func (s *Server) handleListSensors(w http.ResponseWriter, r *http.Request) {
	sensors, err := s.store.ListSensors(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sensors")
		return
	}
	writeJSON(w, http.StatusOK, maskSensors(sensors))
}

func (s *Server) handleGetSensor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	c, err := s.store.GetSensor(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "sensor not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensor")
		return
	}
	writeJSON(w, http.StatusOK, maskSensor(c))
}

func (s *Server) handleCreateSensor(w http.ResponseWriter, r *http.Request) {
	var in sensorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	// A sensor created without an explicit "enabled" starts enabled; on update
	// the same omission keeps the stored state, because the base carries it.
	c := in.toSensor(model.Sensor{Enabled: true})
	store.NormaliseSensor(&c)
	if err := s.store.ValidateSensor(r.Context(), c); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := sensor.Validate(c); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.scheduleOrder(r.Context(), &c); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not determine the sensor order")
		return
	}
	if err := s.store.CreateSensor(r.Context(), &c); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	if err := s.sched.Reconcile(r.Context()); err != nil {
		s.log.Error("reconcile after create", "error", err)
	}
	s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: c.ID, TS: time.Now().UTC()})
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleUpdateSensor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetSensor(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "sensor not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensor")
		return
	}
	if existing.Managed {
		writeError(w, http.StatusConflict,
			"this sensor is declared in the configuration file and can only be changed there")
		return
	}

	var in sensorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	updated := unmaskSensor(in.toSensor(existing), existing)
	store.NormaliseSensor(&updated)
	if err := s.store.ValidateSensor(r.Context(), updated); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := sensor.Validate(updated); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.UpdateSensor(ctx, &updated); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	if err := s.sched.Reconcile(ctx); err != nil {
		s.log.Error("reconcile after update", "error", err)
	}
	s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: updated.ID, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, updated)
}

// handleSetSensorEnabled answers PATCH /api/sensors/{id}, the one sensor write an
// operator may perform.
//
// It takes a request type of its own with a single field, rather than reusing
// PUT and ignoring most of it. decodeJSON refuses unknown fields, so anything
// other than "enabled" is a 400 before this function runs, and a field added to
// sensorInput later is automatically out of reach here. That is the whole
// point: there is no list of permitted fields to keep in step with the form.
func (s *Server) handleSetSensorEnabled(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &in); err != nil {
		if errors.Is(err, errUnknownField) {
			// Not "bad request": the body was well formed, it just asked for a change
			// this role may not make.
			writeError(w, http.StatusForbidden,
				"an operator may only pause or resume a sensor; %s", err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Enabled == nil {
		writeError(w, http.StatusBadRequest, "the only field this route changes is \"enabled\"")
		return
	}
	c, err := s.store.GetSensor(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "sensor not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensor")
		return
	}
	if c.Managed {
		writeError(w, http.StatusConflict,
			"this sensor is declared in the configuration file and can only be changed there")
		return
	}
	c.Enabled = *in.Enabled
	// UpdateSensor writes every column from the value it is handed, so the row
	// just read from the store is what carries the fields this request never
	// mentioned.
	if err := s.store.UpdateSensor(ctx, &c); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	if err := s.sched.Reconcile(ctx); err != nil {
		s.log.Error("reconcile after pause", "error", err)
	}
	s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: c.ID, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleDeleteSensor(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if existing, err := s.store.GetSensor(ctx, id); err == nil && existing.Managed {
		writeError(w, http.StatusConflict,
			"this sensor is declared in the configuration file and can only be removed there")
		return
	}
	if err := s.store.DeleteSensor(ctx, id); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	s.sched.Forget(id)
	s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: id, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleValidateSensor sensors a sensor without saving it, and optionally probes it.
func (s *Server) handleValidateSensor(w http.ResponseWriter, r *http.Request) {
	var in sensorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	c := in.toSensor(model.Sensor{Enabled: true})
	store.NormaliseSensor(&c)

	resp := map[string]any{"valid": true}
	if err := sensor.Validate(c); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"valid": false, "error": err.Error()})
		return
	}
	if in.Probe {
		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(c.Timeout+5)*time.Second)
		defer cancel()
		out := sensor.Test(ctx, c)
		resp["result"] = map[string]any{
			"success":     out.Success,
			"latency_ms":  float64(out.Latency.Microseconds()) / 1000,
			"status_code": out.StatusCode,
			"message":     out.Message,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleProbeSensor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	res, err := s.sched.ProbeNow(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "sensor not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "probe failed: %s", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleSensorOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := s.store.SetSensorOrder(r.Context(), in.IDs); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the order")
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindSensor, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// scheduleOrder puts a new sensor at the end of the dashboard ordering.
func (s *Server) scheduleOrder(ctx context.Context, c *model.Sensor) error {
	existing, err := s.store.ListSensors(ctx)
	if err != nil {
		return err
	}
	max := -1
	for _, e := range existing {
		if e.SortOrder > max {
			max = e.SortOrder
		}
	}
	c.SortOrder = max + 1
	return nil
}

// ---------------------------------------------------------------------------
// history
// ---------------------------------------------------------------------------

// requireSensor answers 404 when the sensor does not exist. Without it the history
// endpoints would happily return an empty series, and a deleted sensor would be
// indistinguishable from a sensor whose data was lost.
func (s *Server) requireSensor(w http.ResponseWriter, r *http.Request, id int64) bool {
	_, err := s.store.GetSensor(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "sensor not found")
		return false
	case err != nil:
		s.fail(w, err, http.StatusInternalServerError, "could not load the sensor")
		return false
	}
	return true
}

func (s *Server) handleSensorResults(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !s.requireSensor(w, r, id) {
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 100, 1, 2000)
	results, err := s.store.RecentResults(r.Context(), id, limit)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load results")
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleSensorSeries(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !s.requireSensor(w, r, id) {
		return
	}
	from, to, rng, err := parseRange(r.URL.Query().Get("range"), model.Range24h)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid range")
		return
	}
	buckets := parseInt(r.URL.Query().Get("buckets"), 0, 4, 500)
	if buckets == 0 {
		buckets = defaultBuckets(rng)
	}
	points, err := s.store.Series(r.Context(), id, from, to, buckets)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the series")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":   string(rng),
		"from":    from,
		"to":      to,
		"buckets": buckets,
		"points":  points,
	})
}

// defaultBuckets picks a bucket count that keeps roughly one point per pixel on
// a typical card while staying cheap to compute.
func defaultBuckets(rng model.Range) int {
	switch rng {
	case model.Range1h:
		return 60
	case model.Range24h:
		return 96
	case model.Range7d:
		return 84
	case model.Range30d:
		return 90
	case model.Range90d:
		return 90
	default:
		return 120
	}
}

func (s *Server) handleSensorUptime(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	from, to, rng, err := parseRange(r.URL.Query().Get("range"), model.Range24h)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid range")
		return
	}
	if !s.requireSensor(w, r, id) {
		return
	}
	stats, err := s.store.Uptime(ctx, id, from, to)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not compute uptime")
		return
	}
	incidents, err := s.store.SummariseIncidents(ctx, id, from, to)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not summarise incidents")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range":     string(rng),
		"from":      from,
		"to":        to,
		"uptime":    stats,
		"incidents": incidents,
	})
}

func (s *Server) handleSensorIncidents(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if !s.requireSensor(w, r, id) {
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 50, 1, 1000)
	incidents, err := s.store.ListIncidents(r.Context(), store.IncidentFilter{SensorID: id, Limit: limit})
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load incidents")
		return
	}
	writeJSON(w, http.StatusOK, incidents)
}

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := parseInt(query.Get("limit"), 50, 1, 1000)
	filter := store.IncidentFilter{
		Limit:    limit,
		SensorID: parseInt64(query.Get("sensor")),
		Query:    strings.TrimSpace(query.Get("q")),
	}
	// The filter line offers three readings of the list, so the API has three
	// words for it. "open=1" is the older spelling of the first one and keeps
	// working: it is what a script or a bookmark written against the last version
	// sends, and there is nothing wrong with it.
	switch state := strings.TrimSpace(query.Get("state")); state {
	case "", "all":
		filter.OnlyOpen = query.Get("open") == "1"
	case "open":
		filter.OnlyOpen = true
	case "resolved":
		filter.OnlyResolved = true
	default:
		writeError(w, http.StatusBadRequest, `state must be "open", "resolved" or "all"`)
		return
	}
	// Both spellings of "since" are accepted and they are the same thing: the
	// dashboard's filter row sends a window ("range=7d") because that is what its
	// buttons are, and a script wants to name a moment. Refusing one of them would
	// mean the API could not express what the page can.
	if raw := query.Get("range"); raw != "" {
		from, _, _, err := parseRange(raw, model.Range7d)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid range")
			return
		}
		filter.Since = from
	} else if raw := query.Get("since"); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp")
			return
		}
		filter.Since = at
	}
	incidents, err := s.store.ListIncidentsWithNames(r.Context(), filter)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load incidents")
		return
	}
	writeJSON(w, http.StatusOK, incidents)
}

// ---------------------------------------------------------------------------
// channels
// ---------------------------------------------------------------------------

type channelInput struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled *bool             `json:"enabled"`
	Config  map[string]string `json:"config"`
}

func (in channelInput) toChannel(base model.Channel) model.Channel {
	c := base
	c.Name = strings.TrimSpace(in.Name)
	c.Type = strings.ToLower(strings.TrimSpace(in.Type))
	if in.Enabled != nil {
		c.Enabled = *in.Enabled
	} else {
		c.Enabled = true
	}
	if in.Config != nil {
		c.Config = in.Config
	}
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	return c
}

// maskChannel hides stored secrets. The client sends the marker back to keep a
// value it never saw.
func maskChannel(c model.Channel) model.Channel {
	out := c
	out.Config = make(map[string]string, len(c.Config))
	for k, v := range c.Config {
		if notify.IsSecret(c.Type, k) && v != "" {
			out.Config[k] = notify.StoredMarker
			continue
		}
		out.Config[k] = v
	}
	return out
}

// unmaskChannel swaps the marker back for the stored value.
func unmaskChannel(incoming, existing model.Channel) model.Channel {
	out := incoming
	out.Config = make(map[string]string, len(incoming.Config))
	for k, v := range incoming.Config {
		if notify.IsSecret(incoming.Type, k) && v == notify.StoredMarker {
			out.Config[k] = existing.Config[k]
			continue
		}
		out.Config[k] = v
	}
	return out
}

func (s *Server) handleListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := s.store.ListChannels(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load channels")
		return
	}
	errorsByChannel := s.dispatcher.Errors()
	out := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		uses, err := s.store.CountSensorsUsingChannel(r.Context(), c.ID)
		if err != nil {
			uses = 0
		}
		masked := maskChannel(c)
		out = append(out, map[string]any{
			"id":           masked.ID,
			"name":         masked.Name,
			"type":         masked.Type,
			"enabled":      masked.Enabled,
			"managed":      masked.Managed,
			"config":       masked.Config,
			"sensor_count": uses,
			"last_error":   errorsByChannel[c.ID],
			"created_at":   masked.CreatedAt,
			"updated_at":   masked.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChannelTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, notify.TypeInfos())
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	c := in.toChannel(model.Channel{})
	store.NormaliseChannel(&c)
	if err := notify.ValidateChannel(c); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.CreateChannel(r.Context(), &c); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindChannel, TS: time.Now().UTC()})
	writeJSON(w, http.StatusCreated, maskChannel(c))
}

func (s *Server) handleUpdateChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	existing, err := s.store.GetChannel(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the channel")
		return
	}
	if existing.Managed {
		writeError(w, http.StatusConflict,
			"this channel is declared in the configuration file and can only be changed there")
		return
	}

	var in channelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	updated := unmaskChannel(in.toChannel(existing), existing)
	store.NormaliseChannel(&updated)
	if err := notify.ValidateChannel(updated); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.UpdateChannel(ctx, &updated); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindChannel, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, maskChannel(updated))
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if existing, err := s.store.GetChannel(ctx, id); err == nil && existing.Managed {
		writeError(w, http.StatusConflict,
			"this channel is declared in the configuration file and can only be removed there")
		return
	}
	if err := s.store.DeleteChannel(ctx, id); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindChannel, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleTestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	ch, err := s.store.GetChannel(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the channel")
		return
	}
	s.sendChannelTest(w, r, ch)
}

// handleTestChannelDraft tests a channel that has not been saved yet, so a
// token can be verified before it is stored.
func (s *Server) handleTestChannelDraft(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	ch := in.toChannel(model.Channel{})
	if idRaw := r.URL.Query().Get("id"); idRaw != "" {
		if existing, err := s.store.GetChannel(r.Context(), parseInt64(idRaw)); err == nil {
			ch = unmaskChannel(ch, existing)
		}
	}
	store.NormaliseChannel(&ch)
	if err := notify.ValidateChannel(ch); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	s.sendChannelTest(w, r, ch)
}

func (s *Server) sendChannelTest(w http.ResponseWriter, r *http.Request, ch model.Channel) {
	sample := model.Sensor{
		ID:     0,
		Name:   "ProbeGlass test",
		Type:   model.SensorHTTP,
		Target: "https://example.org",
	}
	res := model.Result{
		TS:        time.Now().UTC(),
		Success:   true,
		LatencyMS: 42,
		Message:   "HTTP 200 OK · 42ms",
	}
	if err := s.dispatcher.SendTest(r.Context(), ch, sample, res); err != nil {
		s.fail(w, err, http.StatusBadGateway, "delivery failed: %s", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, _ := principalOf(r)
	sensors, _ := s.store.ListSensors(ctx)
	channels, _ := s.store.ListChannels(ctx)
	probes, _ := s.store.ProbeCount(ctx)
	oldest, newest, _ := s.store.DataSpan(ctx)
	openIncidents, _ := s.store.CountOpenIncidents(ctx)
	raw, hourly := s.store.Retention()

	size := int64(0)
	if info, err := os.Stat(s.cfg.DBPath); err == nil {
		size = info.Size()
	}

	// The API key is the credential that opens everything, so it is only read back
	// to somebody who already has the run of the place. A viewer gets the field
	// left out rather than a 403: the Settings page is otherwise theirs, and a
	// single forbidden field should not take the page down.
	body := map[string]any{
		"ui": uiSettings(p.User),
		// The map's tile source, so the settings screen can show what is set
		// without a request of its own. It is readable by everyone — it is a URL
		// and an attribution line, and the map draws them for whoever is looking.
		// Writing it is not, which is why PUT /api/map is an administrator's route.
		"map": map[string]any{
			"tile_url": s.store.Setting(ctx, model.SettingTileURL, model.DefaultTileURL),
			"attribution": s.store.Setting(
				ctx, model.SettingTileAttribution, model.DefaultTileAttribution),
			// The borders and names the binary draws itself. It rides along with the
			// tile source because it is the same decision — what is under the pins —
			// and because both map mounts read it from here.
			"borders":          s.store.Setting(ctx, model.SettingMapBorders, model.DefaultMapBorders),
			"default_tile_url": model.DefaultTileURL,
			"placeholders":     model.TileURLPlaceholders,
		},
		"runtime": map[string]any{
			"version": model.Version,
			// The build stamp, because a dev build has no version to tell it apart from
			// the release it follows: the two are told apart by when they were made, and
			// the first question about a feature that is not on screen is whether the
			// running binary predates it.
			"build_date": model.BuildDate,
			// And which channel it came from, which is the only thing that tells two
			// containers on one machine apart. A container cannot see the tag it was
			// pulled by, so an installation on `latest` and one on `dev` look identical
			// from the inside — down to the commit, when both follow the same push.
			"channel":                model.BuildChannel,
			"base_url":               s.cfg.BaseURL,
			"listen":                 s.cfg.Listen,
			"data_dir":               s.cfg.DataDir,
			"database":               s.cfg.DBPath,
			"config_file":            s.cfg.Path,
			"retention_raw_hours":    int(raw.Hours()),
			"retention_hourly_hours": int(hourly.Hours()),
			"started_at":             startedAt.UTC(),
			"uptime":                 time.Since(startedAt).Round(time.Second).String(),
			"log_level":              s.cfg.LogLevel,
		},
		"stats": map[string]any{
			"sensors":        len(sensors),
			"channels":       len(channels),
			"probes":         probes,
			"open_incidents": openIncidents,
			"oldest_sample":  oldest,
			"newest_sample":  newest,
			"database_bytes": size,
			"sse_clients":    s.broker.Subscribers(),
		},
	}
	if p.Role().Rank() >= model.RoleAdmin.Rank() {
		key, _ := s.secrets()
		body["api_key"] = key
	}
	writeJSON(w, http.StatusOK, body)
}

// allowedUIValues guards the shared UI preferences against junk.
var allowedUIValues = map[string][]string{
	model.PrefTheme:   {"auto", "dark", "light"},
	model.PrefAccent:  {"azure", "violet", "emerald", "amber", "rose", "slate"},
	model.PrefDensity: {"compact", "comfortable"},
	model.PrefLayout:  model.Layouts,
	// The languages the dashboard ships. This list and `available` in i18n.js are
	// the same list written twice — once here, where it decides what may be
	// stored, and once in the browser, which is what draws the picker. A language
	// the server accepts and the dashboard cannot load would be stored and then
	// silently ignored; one the dashboard offers and the server refuses would come
	// back as a failed save. TestEveryShippedLanguageIsStorable holds the two
	// together.
	model.PrefLanguage: {"en", "de"},
	// mode is how much of the interface is shown: "simple" hides the fields and
	// whole areas a first-time user does not need, "advanced" shows everything.
	// Neither is a default — the setup flow asks — but once chosen it lives here
	// with the other preferences, because that is what it is.
	model.PrefMode: {"simple", "advanced"},
	// live is whether the dashboard keeps a connection open for the probe
	// stream. It is a preference and not a setting because it is about the
	// person looking, not about the installation: the same dashboard is read on
	// a desk and on a phone.
	model.PrefLive: {"on", "off"},
}

// validWidgetList checks the one preference whose value is a list.
//
// The header stores which widgets are shown and in what order in a single
// comma-separated string, so it cannot go through allowedUIValues: that table
// compares a value against a set of whole words, and every arrangement of widgets
// would have to be written out to use it. What replaces it is the same guarantee
// by a different road — every id is one the server knows, none is repeated, and
// the list is short enough that nobody is smuggling a payload through a
// preference. The ceiling is generous next to the eleven widgets that exist; what
// it is for is an unbounded string, not the twelfth widget.
func validWidgetList(value string) bool {
	if len(value) > 256 {
		return false
	}
	if value == "" {
		return true // every widget turned off is a choice, and a legitimate one
	}
	seen := map[string]bool{}
	count := 0
	for _, id := range strings.Split(value, ",") {
		count++
		if count > 16 || seen[id] {
			return false
		}
		known := false
		for _, w := range model.Widgets {
			if w == id {
				known = true
				break
			}
		}
		if !known {
			return false
		}
		seen[id] = true
	}
	return true
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UI map[string]string `json:"ui"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	ctx := r.Context()
	for key, value := range in.UI {
		if key == model.PrefWidgets {
			if !validWidgetList(value) {
				writeError(w, http.StatusBadRequest, "invalid value %q for %s", value, key)
				return
			}
			continue
		}
		allowed, ok := allowedUIValues[key]
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown setting %q", key)
			return
		}
		valid := false
		for _, a := range allowed {
			if a == value {
				valid = true
				break
			}
		}
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid value %q for %s", value, key)
			return
		}
	}
	// These preferences belong to whoever is looking at the dashboard, not to the
	// installation: two people sharing one ProbeGlass are allowed to disagree
	// about the theme. So they are written onto the signed-in account.
	p, ok := principalOf(r)
	if !ok || p.User == nil {
		writeError(w, http.StatusBadRequest, "the API key has no interface preferences")
		return
	}
	prefs := p.User.Prefs
	if prefs == nil {
		prefs = map[string]string{}
	}
	for key, value := range in.UI {
		prefs[key] = value
	}
	if err := s.store.SetUserPrefs(ctx, p.User.ID, prefs); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the setting")
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindSettings, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, map[string]any{"ui": uiSettings(&model.User{Prefs: prefs})})
}

// statusForStoreError maps store errors onto HTTP status codes.
func statusForStoreError(err error) int {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case strings.Contains(err.Error(), "already exists"):
		return http.StatusConflict
	case strings.Contains(err.Error(), "does not exist"):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func parseInt64(raw string) int64 {
	var n int64
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil {
		return 0
	}
	return n
}
