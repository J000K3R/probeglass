package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// ---------------------------------------------------------------------------
// guided tour
// ---------------------------------------------------------------------------

// tourVersionMax is the highest tour version this binary knows about. It is
// written by the frontend after a tour run finishes, and bounded here so a
// crafted request cannot fill the settings table with an arbitrary integer.
//
// It deliberately is not the number of steps: the tour grows, steps are
// reordered, and a step that only applies in advanced mode is not shown to
// everyone. All that has to survive a page reload is "which run of the tour has
// this installation seen", so one monotonic number is enough.
const tourVersionMax = 1000

// handleSetTourVersion answers POST /api/onboarding/tour.
//
// It gets its own endpoint rather than going through PUT /api/settings because
// that route validates values against a fixed whitelist of words, and this is a
// number that has to be able to grow. Sharing the route would mean loosening the
// whitelist for everything.
func (s *Server) handleSetTourVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Version < 0 || in.Version > tourVersionMax {
		writeError(w, http.StatusBadRequest, "invalid tour version")
		return
	}
	p, ok := principalOf(r)
	if !ok || p.User == nil {
		// The tour is something a person sits through, and the progress is theirs.
		// The API key has nobody behind it to record progress for.
		writeError(w, http.StatusBadRequest, "the API key has no tour progress to record")
		return
	}
	ctx := r.Context()
	if err := s.store.SetUserPref(ctx, p.User.ID, model.PrefTourVersion, strconv.Itoa(in.Version)); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the tour progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"version": in.Version})
}

// ---------------------------------------------------------------------------
// example sensors
// ---------------------------------------------------------------------------

// exampleSensor is one of the sensors a fresh installation starts with.
type exampleSensor struct {
	Name   string
	Type   model.SensorType
	Target string
}

// exampleSensorInterval is how often the examples probe. Five minutes is slow
// enough that nobody's first ten minutes are spent watching a dashboard refresh,
// and fast enough that the first result arrives while they are still looking.
const exampleSensorInterval = 300

// exampleSensors returns the four sensors a fresh installation starts with.
//
// The point is that a brand-new dashboard is not an empty grid. Somebody who has
// just set a password has no sensor, no history and no reason to trust the tool
// yet, and four editable examples — each a different type, each with a real
// result within a minute — show what the thing does and how it is configured far
// better than a paragraph of text could.
//
// The first one is Weatherglass itself. That is the important one: it is reached
// over loopback, so it works in a homelab with no internet and no other host to
// point at, and the dashboard therefore always has at least one green sensor to
// show. A first-run dashboard that is entirely grey or entirely red teaches the
// wrong thing about whether the product works.
//
// They belong to no alert channel. A default that can fire a real message into
// somebody's chat room before they have configured a single thing is not a
// helpful default.
func exampleSensors(dialAddr string) []exampleSensor {
	return []exampleSensor{
		{Name: "This dashboard (example)", Type: model.SensorHTTP,
			Target: "http://" + dialAddr + "/healthz"},
		{Name: "Internet gateway (example)", Type: model.SensorPing,
			Target: "192.168.1.1"},
		{Name: "Public DNS (example)", Type: model.SensorDNS,
			Target: "one.one.one.one"},
		{Name: "Example website (example)", Type: model.SensorHTTP,
			Target: "https://example.org"},
	}
}

// createExampleSensors seeds the four examples, and reports how many it created.
//
// Two conditions have to hold, and both are about not overruling the user:
//
//   - The configuration file declares no sensors. An installation driven from
//     YAML is a deliberate one, and adding four sensors to it would put rows in
//     the database that the next start would not remove.
//   - The table is empty. This makes the call idempotent, which matters because
//     the setup endpoint is reachable more than once in the window before the
//     password is stored.
//
// A failure here is never fatal to the setup: the password is the thing the user
// asked to set, and it has already been written by the time this runs. The error
// is returned so the caller can log it, and the caller ignores it.
func (s *Server) createExampleSensors(ctx context.Context, dialAddr string) (int, error) {
	if len(s.cfg.Sensors) > 0 {
		return 0, nil
	}
	existing, err := s.store.ListSensors(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	created := 0
	for _, ex := range exampleSensors(dialAddr) {
		c := model.Sensor{
			Name:     ex.Name,
			Type:     ex.Type,
			Target:   ex.Target,
			Interval: exampleSensorInterval,
			Timeout:  10,
			Retries:  2,
			Enabled:  true,
			Group:    "Examples",
			Options:  model.Options{},
			// The mark that makes "remove all the example data" possible later. It
			// is set here and by `seed --demo`, and by nothing else; a sensor the
			// operator typed in by hand leaves it empty and is therefore never
			// touched by the bulk delete.
			Origin: model.OriginExample,
		}
		store.NormaliseSensor(&c)
		if err := s.store.CreateSensor(ctx, &c); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// exampleDialAddr is where the examples are told to reach this daemon. It is the
// same address the doctor and the health check use, so a sensor pointing at the
// dashboard cannot disagree with the health check about where the dashboard is.
func exampleDialAddr(cfg *config.Config) string {
	return config.DialAddr(cfg.Listen)
}
