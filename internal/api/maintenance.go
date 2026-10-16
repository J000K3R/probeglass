package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Maintenance windows: the periods during which a sensor is expected to be down.
//
// The same shape as the site handlers next door — a list anyone may read, writes
// only for administrators — and for the same reason. A window is not an action
// taken during an outage; it is configuration, and it belongs beside the sensors
// and the places rather than beside acknowledging an incident.

// ---------------------------------------------------------------------------
// maintenance windows
// ---------------------------------------------------------------------------

// handleListWindows answers GET /api/maintenance.
//
// The kinds come along because the form offers them, and the browser is not the
// place to keep a second copy of a list the server already owns. It is the same
// arrangement the site kinds use.
func (s *Server) handleListWindows(w http.ResponseWriter, r *http.Request) {
	windows, err := s.store.ListWindows(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load maintenance windows")
		return
	}
	kinds := make([]string, 0, len(model.WindowKinds))
	for _, k := range model.WindowKinds {
		kinds = append(kinds, string(k))
	}
	// The seven weekday numbers, in the order the form lists them, so that a
	// translator never has to know that 0 is Sunday and the browser never has to
	// agree with Go about it.
	writeJSON(w, http.StatusOK, map[string]any{"windows": windows, "kinds": kinds})
}

// windowInput is the body of a window create or update.
//
// The two shapes of a window are both here and only one set is read, chosen by kind.
// That is deliberate: one endpoint rather than two, because the form is one form and
// a caller switching a window from weekly to one-off should not have to change which
// URL it posts to.
type windowInput struct {
	SensorID int64  `json:"sensor_id"`
	Group    string `json:"group"`
	Kind     string `json:"kind"`

	// StartsAt and EndsAt are RFC 3339 instants, which is what toISOString() in the
	// browser produces. Deliberately not a local-time string: the dashboard knows
	// the browser's zone and this process does not, and an instant that arrives
	// without one is an instant somebody has to guess at.
	StartsAt string `json:"starts_at"`
	EndsAt   string `json:"ends_at"`

	Weekdays []int  `json:"weekdays"`
	StartMin int    `json:"start_min"`
	EndMin   int    `json:"end_min"`
	Timezone string `json:"timezone"`

	Note    string `json:"note"`
	Enabled bool   `json:"enabled"`
}

// toWindow converts the body, reporting which field could not be read.
//
// The error names the field rather than saying "invalid body", because the three
// ways this can go wrong — a timestamp the browser did not send as RFC 3339, a kind
// that is not one of the two, a day number out of range — are fixed in three
// different places on the form.
func (in windowInput) toWindow() (model.MaintenanceWindow, error) {
	w := model.MaintenanceWindow{
		SensorID: in.SensorID,
		Group:    in.Group,
		Kind:     model.WindowKind(strings.ToLower(strings.TrimSpace(in.Kind))),
		Weekdays: in.Weekdays,
		StartMin: in.StartMin,
		EndMin:   in.EndMin,
		Timezone: in.Timezone,
		Note:     in.Note,
		Enabled:  in.Enabled,
	}
	for _, f := range []struct {
		name  string
		value string
		into  **time.Time
	}{
		{"starts_at", in.StartsAt, &w.StartsAt},
		{"ends_at", in.EndsAt, &w.EndsAt},
	} {
		if f.value == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, f.value)
		if err != nil {
			return w, fmt.Errorf("%s is not an RFC 3339 timestamp: %q", f.name, f.value)
		}
		t = t.UTC()
		*f.into = &t
	}
	return w, nil
}

// handleCreateWindow answers POST /api/maintenance.
func (s *Server) handleCreateWindow(w http.ResponseWriter, r *http.Request) {
	var in windowInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	win, err := in.toWindow()
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.CreateWindow(r.Context(), &win); err != nil {
		s.writeWindowError(w, err, "could not create the maintenance window")
		return
	}
	s.reloadWindows(r)
	s.log.Info("maintenance window created", "id", win.ID, "kind", string(win.Kind),
		"sensor_id", win.SensorID, "group", win.Group)
	writeJSON(w, http.StatusCreated, win)
}

// handleUpdateWindow answers PUT /api/maintenance/{id}.
func (s *Server) handleUpdateWindow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the window id in the path is not a number")
		return
	}
	var in windowInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	win, err := in.toWindow()
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if err := s.store.UpdateWindow(r.Context(), id, &win); err != nil {
		s.writeWindowError(w, err, "could not save the maintenance window")
		return
	}
	s.reloadWindows(r)
	s.log.Info("maintenance window updated", "id", id, "kind", string(win.Kind))
	writeJSON(w, http.StatusOK, win)
}

// handleDeleteWindow answers DELETE /api/maintenance/{id}.
//
// Nothing to refuse and nobody to warn: a window owns nothing, and the sensors it
// named carry on exactly as they did. Deleting one cannot leave a sensor pointing at
// something that is gone, which is why there is no unassign flag here as there is
// for a site.
func (s *Server) handleDeleteWindow(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the window id in the path is not a number")
		return
	}
	if err := s.store.DeleteWindow(r.Context(), id); err != nil {
		s.writeWindowError(w, err, "could not delete the maintenance window")
		return
	}
	s.reloadWindows(r)
	s.log.Info("maintenance window deleted", "id", id)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// reloadWindows hands the scheduler the new list, so that the next probe consults it.
//
// A window that has been saved and is not in the scheduler's copy is a window that
// silences nothing until the daemon is restarted, which is the kind of bug that looks
// like the feature not working at all. The failure is logged rather than returned:
// the row is written and correct, and the next Reconcile — every sensor change
// triggers one — picks the list up anyway, so refusing the request now would throw
// away a save that has already happened.
func (s *Server) reloadWindows(r *http.Request) {
	if s.sched == nil {
		return
	}
	if err := s.sched.ReloadWindows(r.Context()); err != nil {
		s.log.Error("the maintenance windows were saved but the scheduler could not "+
			"reload them; the change takes effect at the next reconcile",
			"error", err)
	}
}

// writeWindowError maps a store error onto a status code.
//
// The validation message is passed through as it stands rather than replaced by a
// generic one. "a window is for one sensor or for one group, not both and not
// neither" is the sentence that tells somebody which box to untick, and a handler
// that swallowed it in favour of "invalid request" would be throwing away the only
// useful part of the answer.
func (s *Server) writeWindowError(w http.ResponseWriter, err error, msg string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "maintenance window not found")
	case isWindowValidationError(err):
		writeError(w, http.StatusBadRequest, "%s", err.Error())
	case isManagedSiteError(err):
		writeError(w, http.StatusConflict, "%s", err.Error())
	default:
		s.fail(w, err, http.StatusInternalServerError, "%s", msg)
	}
}

// isWindowValidationError reports a window that could not be stored as it stands.
//
// Matched on the message, like isValidationError next door, and for the same reason:
// every one of these is a sentence written for a person, and a typed error that a
// handler could switch on would buy nothing a reader of the message does not already
// get. The list is shorter than the site one because a window has fewer ways to be
// wrong — what it has instead is the check that it names exactly one thing, which the
// store phrases in full.
func isWindowValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"unknown window kind",
		"not both and not neither",
		"may be at most",
		"needs a beginning and an end",
		"has to end after it begins",
		"needs at least one day",
		"is not a weekday",
		"within the day",
		"may not cover all 24 hours",
		"is not a time zone",
		"not an RFC 3339 timestamp",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
