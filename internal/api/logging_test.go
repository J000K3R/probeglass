package api

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// The request log is the only trace a server-side failure leaves. A 5xx that
// reaches the client and nothing else is a bug report with no evidence in it, so
// these tests pin the three properties that make the log worth reading: the
// cause is there, the client still does not get it, and a client mistake does
// not masquerade as a server fault.

// capturedLogs collects records so a test can assert on them. The mutex is not
// optional: the suite runs under -race, and the middleware writes from the
// server's goroutine while the test reads.
type capturedLogs struct {
	mu      sync.Mutex
	records []slog.Record
}

func newCapturedLogs() *capturedLogs { return &capturedLogs{} }

func (c *capturedLogs) logger() *slog.Logger {
	return slog.New(&captureHandler{out: c})
}

func (c *capturedLogs) find(msg string) []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Record
	for _, r := range c.records {
		if r.Message == msg {
			out = append(out, r)
		}
	}
	return out
}

type captureHandler struct{ out *capturedLogs }

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.out.mu.Lock()
	defer h.out.mu.Unlock()
	h.out.records = append(h.out.records, r.Clone())
	return nil
}

// WithAttrs and WithGroup hand back the same handler. These tests look at
// top-level attributes only, and flattening the groups loses nothing.
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// attr reads one top-level attribute of a record.
func attr(r slog.Record, key string) (string, bool) {
	var (
		val   string
		found bool
	)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			val, found = a.Value.String(), true
			return false
		}
		return true
	})
	return val, found
}

// forPath narrows records down to the ones about one request path.
func forPath(records []slog.Record, path string) []slog.Record {
	var out []slog.Record
	for _, r := range records {
		if p, ok := attr(r, "path"); ok && p == path {
			out = append(out, r)
		}
	}
	return out
}

// TestServerErrorIsLoggedAndKeepsTheCause opens the database, closes it, and
// asks for the sensors. Everything from there on fails, and the failure has to
// be readable in the log — that is the whole point.
//
// The request carries an API key rather than a cookie, because a cookie has to be
// looked up in the database before the request can even be authenticated, and the
// database is the thing that is broken. The key is compared against a value held
// in memory, so the request reaches the handler and fails where this test is
// looking: inside it.
func TestServerErrorIsLoggedAndKeepsTheCause(t *testing.T) {
	logs := newCapturedLogs()
	h := newHarnessWith(t, logs.logger())
	h.setup()

	const key = "wg_test_api_key_1234567890"
	ts := h.keyedServer(key)

	// Pull the database out from under the handler. Any store call from here on
	// fails with "sql: database is closed", which is the shape of the failure
	// this test is about: the handler cannot do its job through no fault of the
	// request.
	if err := h.store.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sensors", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-API-Key", key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}

	failures := forPath(logs.find("http request failed"), "/api/sensors")
	if len(failures) != 1 {
		t.Fatalf("got %d error lines for /api/sensors, want exactly 1:\n%s",
			len(failures), dump(logs.find("http request failed")))
	}
	rec := failures[0]
	if rec.Level != slog.LevelError {
		t.Errorf("level = %s, want ERROR", rec.Level)
	}
	if msg, ok := attr(rec, "message"); !ok || msg != "could not load sensors" {
		t.Errorf("message = %q (present=%v), want %q", msg, ok, "could not load sensors")
	}
	cause, ok := attr(rec, "error")
	if !ok || !strings.Contains(cause, "closed") {
		t.Errorf("error = %q (present=%v), want the underlying cause containing %q.\n"+
			"Without it the log says a request failed but not why, which is the "+
			"state this change exists to end.", cause, ok, "closed")
	}

	// One request, one line. The info line is a strict subset of the error line,
	// so writing both would only make the failure harder to pick out.
	if info := forPath(logs.find("http request"), "/api/sensors"); len(info) != 0 {
		t.Errorf("got %d info lines for /api/sensors, want 0: the error line already "+
			"carries method, path, status and duration", len(info))
	}
}

// TestServerErrorDoesNotLeakTheCauseToTheClient is the other half: the operator
// gets the detail, the browser gets a sentence it can show a person.
func TestServerErrorDoesNotLeakTheCauseToTheClient(t *testing.T) {
	logs := newCapturedLogs()
	h := newHarnessWith(t, logs.logger())
	h.setup()

	const key = "wg_test_api_key_1234567890"
	ts := h.keyedServer(key)

	if err := h.store.Close(); err != nil {
		t.Fatalf("close the store: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/sensors", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-API-Key", key)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()

	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)

	if body.Error != "could not load sensors" {
		t.Errorf("client body = %q, want %q", body.Error, "could not load sensors")
	}
	if strings.Contains(body.Error, "closed") || strings.Contains(body.Error, "sql:") {
		t.Errorf("the client body carries the server-side cause: %q.\n"+
			"The database path, the driver's message and the file layout are not "+
			"the browser's business.", body.Error)
	}
}

// TestClientErrorDoesNotLogAsServerFault keeps a 400 out of the error log. The
// dashboard produces 4xx as ordinary traffic — 409 setup required, 401, 429 on
// the login limiter — and promoting those to ERROR would train the reader to
// ignore the line that matters.
func TestClientErrorDoesNotLogAsServerFault(t *testing.T) {
	logs := newCapturedLogs()
	h := newHarnessWith(t, logs.logger())
	h.setup()

	// A sensor with no target is rejected by sensor.Validate.
	resp := h.do(http.MethodPost, "/api/sensors", map[string]string{"name": "incomplete"}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}

	if failures := forPath(logs.find("http request failed"), "/api/sensors"); len(failures) != 0 {
		t.Errorf("a 400 produced %d error line(s):\n%s", len(failures), dump(failures))
	}
	info := forPath(logs.find("http request"), "/api/sensors")
	if len(info) != 1 {
		t.Fatalf("got %d info lines for /api/sensors, want exactly 1", len(info))
	}
	if status, ok := attr(info[0], "status"); !ok || status != "400" {
		t.Errorf("info line status = %q (present=%v), want 400", status, ok)
	}
}

// dump renders records for a failure message, so a broken assertion says what
// was logged instead of only how many lines were expected.
func dump(records []slog.Record) string {
	if len(records) == 0 {
		return "  (no records)"
	}
	var b strings.Builder
	for _, r := range records {
		b.WriteString("  " + r.Level.String() + " " + r.Message)
		r.Attrs(func(a slog.Attr) bool {
			b.WriteString(" " + a.Key + "=" + a.Value.String())
			return true
		})
		b.WriteString("\n")
	}
	return b.String()
}
