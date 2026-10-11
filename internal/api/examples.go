package api

import (
	"net/http"
	"time"

	"github.com/j000k3r/weatherglass/internal/demo"
	"github.com/j000k3r/weatherglass/internal/event"
)

// exampleDays is how much history a restored dataset is given. A week is what the
// dashboard's 7-day range reads, so every chart and every uptime bar has
// something in it the moment the button is pressed.
const exampleDays = 7

// ---------------------------------------------------------------------------
// example data
// ---------------------------------------------------------------------------

// handleExampleData answers GET /api/examples.
//
// It is readable by anyone, because it is what the Settings screen asks before
// deciding whether to show the removal button at all. It reports counts and
// nothing else; there is no secret in how many invented sensors a fresh
// installation has.
func (s *Server) handleExampleData(w http.ResponseWriter, r *http.Request) {
	data, err := s.store.CountExampleData(r.Context())
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not count the example data")
		return
	}
	writeJSON(w, http.StatusOK, data)
}

// handleDeleteExampleData answers DELETE /api/examples.
//
// This is the one destructive bulk operation in the product, so the response says
// exactly how much went: the caller shows it, and a number is much easier to trust
// than "done".
func (s *Server) handleDeleteExampleData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	removed, err := s.store.DeleteExampleData(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not remove the example data")
		return
	}

	// The rows are gone, so the scheduler has to be told: a goroutine may still be
	// probing a sensor that no longer exists.
	if removed.Sensors > 0 {
		if err := s.sched.Reconcile(ctx); err != nil {
			s.log.Error("reconcile after removing the example data", "error", err)
		}
	}
	s.log.Info("example data removed",
		"sensors", removed.Sensors, "results", removed.Results, "incidents", removed.Incidents)
	// Every connected dashboard redraws. There is no narrower event for "the grid
	// changed shape", and one extra summary fetch per client is nothing.
	s.broker.Publish(event.Event{Kind: event.KindSensor, TS: time.Now().UTC()})
	writeJSON(w, http.StatusOK, removed)
}

// handleRestoreExampleData answers POST /api/examples.
//
// It is the way back from the removal button. What it writes is the rich demo
// dataset — the one the public demo is generated from, sensors in their groups
// with an invented week of history — rather than the lean set a fresh
// installation starts with, because somebody who has just deleted everything and
// asked for it back wants to see what the dashboard looks like with something on
// it, curves and all. The two sets are deliberately different and a test keeps
// them apart; how many sensors each holds is the dataset's business, and is asked
// of the dataset rather than written down here.
//
// Running it twice is harmless: a name that is already in the table is left
// alone, so the second call creates nothing and invents nothing.
func (s *Server) handleRestoreExampleData(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// A file that declares sensors is a deliberate installation, and the example
	// data is not what it asked for. Writing rows anyway would put sensors in the
	// database that the next start would not touch, which is how two sources of
	// truth begin to disagree.
	if len(s.cfg.Sensors) > 0 {
		writeError(w, http.StatusConflict,
			"this installation is configured from a file; add sensors there")
		return
	}

	res, err := demo.Seed(ctx, s.store, exampleDays)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not restore the example data")
		return
	}

	// New rows mean the scheduler has to be told, exactly as it does after a
	// removal — only this time it is a sensor to start probing rather than one to
	// stop.
	if res.Sensors > 0 {
		if err := s.sched.Reconcile(ctx); err != nil {
			s.log.Error("reconcile after restoring the example data", "error", err)
		}
	}
	s.log.Info("example data restored", "sensors", res.Sensors, "results", res.Samples)
	s.broker.Publish(event.Event{Kind: event.KindSensor, TS: time.Now().UTC()})

	// The answer is the count the dashboard already knows how to show, not what
	// was created: "5 example sensor(s)" is a fact about the table, and the table
	// is what the panel is about.
	data, err := s.store.CountExampleData(ctx)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not count the example data")
		return
	}
	writeJSON(w, http.StatusOK, data)
}
