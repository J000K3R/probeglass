package api

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/j000k3r/weatherglass/internal/config"
	"github.com/j000k3r/weatherglass/internal/doctor"
)

// doctorTimeout bounds one diagnostic run. doctor.Run already carries a default
// of ten seconds; it is repeated here because the HTTP handler has to keep a
// context alive for exactly as long as the work it is waiting on.
const doctorTimeout = 10 * time.Second

// doctorCacheTTL is how long a finished report is reused.
//
// doctor reads the whole database and dials the daemon's own healthz endpoint,
// so it is the most expensive thing this API can be asked to do. A person
// clicking "Run" three times while reading the output must not start three
// complete passes over the same file, and a browser that refetches the settings
// screen must not start one at all.
const doctorCacheTTL = 15 * time.Second

// doctorResult is one rendered report: the structured findings, the counts, and
// the text the command line would have printed.
type doctorResult struct {
	Report doctor.Report
	Text   string
	At     time.Time
}

// doctorCache serialises diagnostic runs and remembers the last one.
//
// It holds its lock across the whole run rather than only around the map. That
// is deliberate: a second request arriving mid-run should wait for that run and
// read its result, not start a competing pass — single-flight is the entire
// point, and the critical section is a bounded ten seconds with no other caller
// inside it. Everything else on the server keeps serving.
type doctorCache struct {
	mu   sync.Mutex
	last *doctorResult
}

// handleRunDoctor answers POST /api/doctor with a fresh or recently cached
// report.
//
// It is a POST rather than a GET because it is not a read of a resource: it
// performs network I/O and opens the database, and a GET would invite a browser
// or a proxy to replay it.
func (s *Server) handleRunDoctor(w http.ResponseWriter, r *http.Request) {
	res, err := s.runDoctor(r)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "the diagnostic could not be run")
		return
	}

	ok, warn, fail := res.Report.Counts()
	findings := make([]map[string]any, 0, len(res.Report.Findings))
	for _, f := range res.Report.Findings {
		findings = append(findings, map[string]any{
			"level":  f.Level.String(),
			"label":  f.Label,
			"detail": f.Detail,
			"hint":   f.Hint,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"summary": map[string]any{
			"ok":     ok,
			"warn":   warn,
			"fail":   fail,
			"failed": res.Report.Failed(),
			"ran_at": res.At.UTC(),
		},
		"findings": findings,
		// The exact text the CLI prints, so that the answer in the dashboard and
		// the answer in the terminal cannot drift apart.
		"text": res.Text,
		// The command to paste into a shell for the same output without the
		// dashboard. Shown in the UI next to the text.
		"command": "docker exec weatherglass weatherglass doctor",
	})
}

// runDoctor returns a cached report or produces a new one.
func (s *Server) runDoctor(r *http.Request) (*doctorResult, error) {
	s.doctor.mu.Lock()
	defer s.doctor.mu.Unlock()

	if s.doctor.last != nil && time.Since(s.doctor.last.At) < doctorCacheTTL {
		return s.doctor.last, nil
	}

	// Bounded, but not tied to the request's own cancellation: a diagnostic that
	// is nearly finished should not be thrown away because the browser navigated
	// away, because the next page load would then start it again from the start.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), doctorTimeout)
	defer cancel()

	// The daemon is running, so the configuration necessarily resolved. ConfigErr
	// is therefore nil here and the "config" finding says "ok" — which is honest
	// but is the one place where this route is weaker than the command: a file
	// that is broken enough to stop startup is exactly what the CLI is for.
	report := doctor.Run(ctx, doctor.Options{
		Config:     s.cfg,
		HealthAddr: config.DialAddr(s.cfg.Listen),
		Timeout:    doctorTimeout,
	})

	var buf bytes.Buffer
	report.Render(&buf)

	res := &doctorResult{Report: report, Text: buf.String(), At: time.Now().UTC()}
	s.doctor.last = res
	return res, nil
}
