package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

func TestAckAndUnackIncident(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	id, err := st.OpenIncident(ctx, c.ID, time.Now().UTC().Add(-time.Minute), "boom")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}

	at := time.Now().UTC()
	if err := st.AckIncident(ctx, id, "dashboard", "known reboot", at); err != nil {
		t.Fatalf("ack: %v", err)
	}

	open, err := st.OpenIncidentFor(ctx, c.ID)
	if err != nil || open == nil {
		t.Fatalf("open incident for: %v, %v", open, err)
	}
	if !open.Acknowledged() {
		t.Fatal("the incident is not acknowledged after AckIncident")
	}
	if open.AckedAt == nil || !open.AckedAt.Equal(at.Truncate(time.Nanosecond)) {
		t.Errorf("acked_at = %v, want %v", open.AckedAt, at)
	}
	if open.AckedBy != "dashboard" {
		t.Errorf("acked_by = %q, want dashboard", open.AckedBy)
	}
	if open.AckNote != "known reboot" {
		t.Errorf("ack_note = %q, want the note back", open.AckNote)
	}

	// Acknowledging twice is refused, and it does not overwrite the record: the
	// point of the note is who took responsibility when the outage started.
	if err := st.AckIncident(ctx, id, "api", "second", time.Now().UTC()); err == nil {
		t.Fatal("a second acknowledgement succeeded")
	} else if !strings.Contains(err.Error(), "already acknowledged") {
		t.Errorf("second ack error = %q, want it to say it is already acknowledged", err)
	}
	open, _ = st.OpenIncidentFor(ctx, c.ID)
	if open.AckedBy != "dashboard" || open.AckNote != "known reboot" {
		t.Errorf("the second ack overwrote the first: by=%q note=%q", open.AckedBy, open.AckNote)
	}

	// Taking it back clears the whole record, so the UI cannot show a name
	// beside no timestamp.
	if err := st.UnackIncident(ctx, id); err != nil {
		t.Fatalf("unack: %v", err)
	}
	open, _ = st.OpenIncidentFor(ctx, c.ID)
	if open.Acknowledged() {
		t.Error("the incident is still acknowledged after UnackIncident")
	}
	if open.AckedBy != "" || open.AckNote != "" {
		t.Errorf("unack left by=%q note=%q behind", open.AckedBy, open.AckNote)
	}
	if err := st.UnackIncident(ctx, id); err == nil {
		t.Error("unacknowledging twice succeeded")
	}
}

func TestAckRefusesResolvedAndUnknownIncidents(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	id, err := st.OpenIncident(ctx, c.ID, time.Now().UTC().Add(-time.Minute), "boom")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	if err := st.CloseIncident(ctx, id, time.Now().UTC()); err != nil {
		t.Fatalf("close incident: %v", err)
	}

	// "Acknowledge what is down" has no meaning once nothing is.
	err = st.AckIncident(ctx, id, "dashboard", "", time.Now().UTC())
	if err == nil {
		t.Fatal("acknowledging a resolved incident succeeded")
	}
	if !strings.Contains(err.Error(), "already resolved") {
		t.Errorf("error = %q, want it to say the incident is resolved", err)
	}

	err = st.AckIncident(ctx, 9999, "dashboard", "", time.Now().UTC())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("acknowledging an unknown incident = %v, want ErrNotFound", err)
	}

	// A resolved incident is still readable, and it reports itself as unacked.
	got, err := st.IncidentByID(ctx, id)
	if err != nil {
		t.Fatalf("incident by id: %v", err)
	}
	if got.Acknowledged() {
		t.Error("a resolved incident reports itself as acknowledged")
	}
	if _, err := st.IncidentByID(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown incident = %v, want ErrNotFound", err)
	}
}

func TestIncidentListCarriesTheAcknowledgement(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	id, err := st.OpenIncident(ctx, c.ID, time.Now().UTC().Add(-time.Minute), "boom")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	if err := st.AckIncident(ctx, id, "dashboard", "on it", time.Now().UTC()); err != nil {
		t.Fatalf("ack: %v", err)
	}

	rows, err := st.ListIncidentsWithNames(ctx, IncidentFilter{OnlyOpen: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d incidents, want 1", len(rows))
	}
	if !rows[0].Acknowledged() || rows[0].AckNote != "on it" {
		t.Errorf("the incident list dropped the acknowledgement: %+v", rows[0].Incident)
	}
	if rows[0].SensorName != "web" {
		t.Errorf("sensor_name = %q, want web", rows[0].SensorName)
	}
}
