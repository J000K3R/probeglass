package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/event"
	"github.com/j000k3r/weatherglass/internal/store"
)

// Acknowledging is not pausing.
//
// A paused sensor (enabled: false) is not watched at all. An acknowledged outage
// is still watched: probes keep running, the incident stays open and visible,
// and a sensor that recovers still sends its recovery alert. What stops is the
// repeat reminder on a sensor that is known to be down — the note is a person
// saying "I saw this, I am on it", and the thing they asked to stop is precisely
// the message that would otherwise arrive every reminder_every seconds.
//
// It hangs off the incident rather than the sensor because the two outages are
// different events. A flag on the sensor would keep silencing the next outage
// long after the note about this one stopped meaning anything, and would make it
// impossible to say which outage was acknowledged.

// ackRequest is the body of both acknowledgement endpoints.
type ackRequest struct {
	// Note is free text: what is being done about it, a ticket number, "known
	// reboot". Optional, and truncated to the note column's practical size.
	Note string `json:"note"`
}

// handleAckIncident answers POST /api/incidents/{id}/ack.
func (s *Server) handleAckIncident(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid incident id")
		return
	}
	var in ackRequest
	// An empty body is a valid acknowledgement with no note, so a decode error on
	// an empty body is not an error. Any other malformed body still is.
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
			return
		}
	}
	s.ackIncident(w, r, id, strings.TrimSpace(in.Note))
}

// handleUnackIncident answers DELETE /api/incidents/{id}/ack.
func (s *Server) handleUnackIncident(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid incident id")
		return
	}
	ctx := r.Context()
	if err := s.store.UnackIncident(ctx, id); err != nil {
		s.writeAckError(w, err, "could not take the acknowledgement back")
		return
	}
	incident, err := s.store.IncidentByID(ctx, id)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the incident")
		return
	}
	s.broker.Publish(event.Event{Kind: event.KindIncident, TS: time.Now().UTC(),
		SensorID: incident.SensorID, Incident: incident})
	writeJSON(w, http.StatusOK, incident)
}

// handleAckSensor answers POST /api/sensors/{id}/ack — the shortcut the dashboard
// uses on a failed card, where the running incident's id is not at hand.
func (s *Server) handleAckSensor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid sensor id")
		return
	}
	var in ackRequest
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
			return
		}
	}
	incident, err := s.store.OpenIncidentFor(r.Context(), id)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the incident")
		return
	}
	if incident == nil {
		// 409, not 404: the sensor exists and is fine. There is simply nothing
		// down to acknowledge, which is a conflict with the request's premise.
		writeError(w, http.StatusConflict, "this sensor has no open incident to acknowledge")
		return
	}
	s.ackIncident(w, r, incident.ID, strings.TrimSpace(in.Note))
}

// ackIncident performs the acknowledgement and publishes the event.
func (s *Server) ackIncident(w http.ResponseWriter, r *http.Request, incidentID int64, note string) {
	ctx, cancel := commitContext(r) // a short write; keep it off the request's fate
	defer cancel()

	by := s.ackAuthor(r)
	if err := s.store.AckIncident(ctx, incidentID, by, note, time.Now().UTC()); err != nil {
		s.writeAckError(w, err, "could not acknowledge the incident")
		return
	}
	incident, err := s.store.IncidentByID(ctx, incidentID)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not load the incident")
		return
	}
	s.log.Info("incident acknowledged", "incident", incidentID, "sensor", incident.SensorID, "by", by)
	s.broker.Publish(event.Event{Kind: event.KindIncident, TS: time.Now().UTC(),
		SensorID: incident.SensorID, Incident: incident})
	writeJSON(w, http.StatusOK, incident)
}

// ackAuthor names whoever acknowledged, for the record.
//
// A signed-in person is recorded by name, which is the point of having accounts at
// all: months later, "who said this was known?" has an answer. The API key belongs
// to no account, so it stays "api" — a script decided, not a person.
func (s *Server) ackAuthor(r *http.Request) string {
	if p, ok := principalOf(r); ok {
		return p.Name()
	}
	// A handler reached without going through the router has no principal. Saying
	// so beats inventing a name.
	return "unknown"
}

// writeAckError maps the store's acknowledgement errors onto status codes.
//
// "already acknowledged" and "already resolved" are conflicts with the current
// state rather than bad requests: the caller's intent was fine, the world moved
// on. They are worth distinguishing in the message because the UI shows it
// verbatim and "it is no longer down" is a different sentence from "you already
// said you knew".
func (s *Server) writeAckError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such incident")
	case strings.Contains(err.Error(), "already resolved"):
		writeError(w, http.StatusConflict, "this incident is already resolved — there is nothing down to acknowledge")
	case strings.Contains(err.Error(), "already acknowledged"):
		writeError(w, http.StatusConflict, "this incident is already acknowledged")
	case strings.Contains(err.Error(), "is not acknowledged"):
		writeError(w, http.StatusConflict, "this incident is not acknowledged")
	default:
		s.fail(w, err, http.StatusInternalServerError, "%s", fallback)
	}
}
