package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/j000k3r/probeglass/internal/catalog"
	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/sensor"
	"github.com/j000k3r/probeglass/internal/store"
)

// The catalogue of sensor templates and the bulk create that applies one.
//
// A template is a list of ordinary sensors. Applying it adds them all in one
// transaction, so a device is either set up completely or not at all; the route
// takes the same sensor shape as POST /api/sensors and validates every entry
// before anything is written.

// handleSensorTemplates answers the static catalogue. It holds no secrets and no
// installation-specific data, so a viewer may read it with the rest of the
// dashboard.
func (s *Server) handleSensorTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"templates": catalog.Templates()})
}

type bulkSensorInput struct {
	Sensors []sensorInput `json:"sensors"`
}

// handleCreateSensorsBulk creates every sensor in one request.
func (s *Server) handleCreateSensorsBulk(w http.ResponseWriter, r *http.Request) {
	var in bulkSensorInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if len(in.Sensors) == 0 {
		writeError(w, http.StatusBadRequest, "no sensors were given")
		return
	}
	// A template is a handful of checks. A hundred leaves room for a large one
	// without turning a single request into an unbounded insert.
	if len(in.Sensors) > 100 {
		writeError(w, http.StatusBadRequest, "at most 100 sensors can be created at once, got %d", len(in.Sensors))
		return
	}

	existing, err := s.store.ListSensors(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load sensors")
		return
	}
	maxOrder := -1
	for _, c := range existing {
		if c.SortOrder > maxOrder {
			maxOrder = c.SortOrder
		}
	}

	created := make([]model.Sensor, len(in.Sensors))
	pointers := make([]*model.Sensor, len(created))
	for i, spec := range in.Sensors {
		c := spec.toSensor(model.Sensor{Enabled: true})
		store.NormaliseSensor(&c)

		// Every entry is checked before any is written. The store's transaction
		// would roll a partial batch back anyway, but a message that names the
		// offending sensor is more useful than a constraint violation.
		if err := s.store.ValidateSensor(r.Context(), c); err != nil {
			writeError(w, http.StatusBadRequest, "sensor %q: %s", sensorLabel(i, c), err.Error())
			return
		}
		if err := sensor.Validate(c); err != nil {
			writeError(w, http.StatusBadRequest, "sensor %q: %s", sensorLabel(i, c), err.Error())
			return
		}
		if err := s.store.ValidateIconRef(r.Context(), c.Icon); err != nil {
			writeError(w, http.StatusBadRequest, "sensor %q: %s", sensorLabel(i, c), err.Error())
			return
		}
		c.SortOrder = maxOrder + 1 + i
		created[i] = c
		pointers[i] = &created[i]
	}

	if err := s.store.CreateSensors(r.Context(), pointers); err != nil {
		s.fail(w, err, statusForStoreError(err), "%s", err.Error())
		return
	}
	if err := s.sched.Reconcile(r.Context()); err != nil {
		s.log.Error("reconcile after bulk create", "error", err)
	}
	for _, c := range created {
		s.broker.Publish(event.Event{Kind: event.KindSensor, SensorID: c.ID, TS: time.Now().UTC()})
	}
	writeJSON(w, http.StatusCreated, map[string]any{"sensors": maskSensors(created)})
}

// sensorLabel names a sensor in a validation error, falling back to its place in
// the request when it has no name yet.
func sensorLabel(index int, c model.Sensor) string {
	if c.Name != "" {
		return c.Name
	}
	return "#" + strconv.Itoa(index+1)
}
