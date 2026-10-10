package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// postRaw sends a body exactly as it is, without the harness's JSON encoding.
//
// The restore route takes the uploaded file as the request body — there is no
// multipart anywhere in this program and no wrapper around the bytes — so the test has
// to be able to send bytes that are not a JSON object the harness built.
func (h *harness) postRaw(path string, body []byte, headers map[string]string) *http.Response {
	h.t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.http.URL+path, bytes.NewReader(body))
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// downloadBackup fetches a backup and returns the file and the headers that describe it.
func (h *harness) downloadBackup(query string) (*http.Response, []byte) {
	h.t.Helper()
	resp := h.do(http.MethodGet, "/api/backup"+query, nil, nil)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatalf("read the backup: %v", err)
	}
	return resp, body
}

// ---------------------------------------------------------------------------
// who may reach them
// ---------------------------------------------------------------------------

// TestBackupRoutesAreAdministratorsOnly is the whole authorization policy for this
// feature, in one table.
//
// It is worth its own test rather than a line in the role table because of what is on
// the other side: GET /api/backup hands out every bot token and every password hash in
// the installation in the clear, and POST /api/restore replaces the accounts, the API
// key and the configuration — including that of whoever is asking. A viewer or an
// operator reaching either one would be the worst mistake available in this file.
func TestBackupRoutesAreAdministratorsOnly(t *testing.T) {
	h := newHarness(t)
	h.setup()
	admin := h.cookie
	h.addUser(t, "operator", "operator1234", model.RoleOperator)
	h.addUser(t, "viewer", "viewer1234", model.RoleViewer)
	operator := h.signIn("operator", "operator1234")
	viewer := h.signIn("viewer", "viewer1234")

	for _, c := range []struct {
		who    string
		cookie *http.Cookie
		method string
		path   string
		want   int
	}{
		{"nobody signed in", nil, http.MethodGet, "/api/backup", http.StatusUnauthorized},
		{"a viewer", viewer, http.MethodGet, "/api/backup", http.StatusForbidden},
		{"an operator", operator, http.MethodGet, "/api/backup", http.StatusForbidden},
		{"an administrator", admin, http.MethodGet, "/api/backup", http.StatusOK},

		{"nobody signed in", nil, http.MethodPost, "/api/restore", http.StatusUnauthorized},
		{"a viewer", viewer, http.MethodPost, "/api/restore", http.StatusForbidden},
		{"an operator", operator, http.MethodPost, "/api/restore", http.StatusForbidden},
	} {
		got := h.as(c.cookie, func() *http.Response {
			resp := h.postRaw(c.path, []byte(`{}`), origin(h))
			if c.method == http.MethodGet {
				resp.Body.Close()
				return h.do(c.method, c.path, nil, nil)
			}
			return resp
		})
		got.Body.Close()
		if got.StatusCode != c.want {
			t.Errorf("%s: %s %s = %d, want %d", c.who, c.method, c.path, got.StatusCode, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// downloading
// ---------------------------------------------------------------------------

// TestBackupDownloadLooksLikeADownload checks the four headers that decide what the
// browser does with the answer, and that the body is the document it claims to be.
//
// The filename is built on the server from a constant and the date. Nothing from the
// request goes into it; a name taken from a query parameter is a header-injection
// route, and this is the one route in the program that serves a file rather than JSON.
func TestBackupDownloadLooksLikeADownload(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp, body := h.downloadBackup("")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q", ct)
	}
	disposition := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(disposition, "attachment;") {
		t.Errorf("Content-Disposition = %q, want an attachment", disposition)
	}
	want := "weatherglass-backup-" + time.Now().UTC().Format("2006-01-02") + ".json"
	if !strings.Contains(disposition, want) {
		t.Errorf("Content-Disposition = %q, want it to name %q", disposition, want)
	}
	// A backup is a credential and must not be left in a cache anywhere on the way.
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	if h.server.cfg.BaseURL != "http://weatherglass.test" {
		t.Fatal("the harness changed under this test")
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("the download is not JSON: %v", err)
	}
	var format string
	if err := json.Unmarshal(doc["format"], &format); err != nil || format != "weatherglass-backup" {
		t.Errorf("format = %q (%v)", format, err)
	}

	// The example data the setup screen leaves behind is in there: a download that
	// says it is a backup but names no table would be worse than an error.
	if _, ok := doc["sensors"]; !ok {
		t.Error("the download names no sensors table")
	}
}

// TestBackupRejectsBadQueryParameters keeps a typo from producing a file that is
// quietly not what was asked for. A window of "0" days that silently means "everything"
// is the kind of thing nobody notices until they need the file.
func TestBackupRejectsBadQueryParameters(t *testing.T) {
	h := newHarness(t)
	h.setup()

	for _, query := range []string{"?scope=everything", "?days=abc", "?days=0", "?days=-3"} {
		resp, body := h.downloadBackup(query)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET /api/backup%s = %d, want 400", query, resp.StatusCode)
			continue
		}
		// The refusal is a JSON error, not a half-written file with the download
		// headers already on it.
		if strings.HasPrefix(string(body), "{") {
			var e struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(body, &e); err != nil || e.Error == "" {
				t.Errorf("GET /api/backup%s: the error has no message: %s", query, body)
			}
		}
		if disp := resp.Header.Get("Content-Disposition"); disp != "" {
			t.Errorf("GET /api/backup%s: the refusal still says %q", query, disp)
		}
	}

	// And the two scopes that are real are accepted, so the loop above is not passing
	// because everything fails.
	for _, query := range []string{"?scope=config", "?scope=history&days=30", "?scope=history&days=all"} {
		if resp, _ := h.downloadBackup(query); resp.StatusCode != http.StatusOK {
			t.Errorf("GET /api/backup%s = %d, want 200", query, resp.StatusCode)
		}
	}
}

// ---------------------------------------------------------------------------
// restoring
// ---------------------------------------------------------------------------

// TestRestoreRoundTripThroughTheAPI walks the whole feature the way the dashboard does
// it: download a file, change something, put the file back, find the change undone and
// the daemon told to carry on.
//
// The sensor event matters as much as the data. A restore that puts the configuration
// right but leaves the scheduler probing what was there before would be invisible from
// the dashboard — the sensors would look correct and not be checked.
func TestRestoreRoundTripThroughTheAPI(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "Router", "type": "ping", "target": "10.0.0.1", "interval": 60,
	}, origin(h))
	var created model.Sensor
	decode(t, resp, &created)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d", resp.StatusCode)
	}

	_, file := h.downloadBackup("?scope=config")

	// Something the file does not know about, so that the restore has work to do.
	del := h.do(http.MethodDelete, "/api/sensors/"+itoa(created.ID), nil, origin(h))
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete sensor: status = %d", del.StatusCode)
	}

	events, stop := h.server.broker.Subscribe()
	defer stop()

	restore := h.postRaw("/api/restore", file, origin(h))
	var result struct {
		Counts struct {
			Sensors int `json:"sensors"`
			Users   int `json:"users"`
		} `json:"counts"`
		SafetyCopy string `json:"safety_copy"`
	}
	decode(t, restore, &result)
	if restore.StatusCode != http.StatusOK {
		t.Fatalf("restore: status = %d", restore.StatusCode)
	}
	if result.Counts.Sensors == 0 {
		t.Error("the restore counted no sensors")
	}
	// The safety copy is written beside the database, and the answer names it by its
	// last element only — the server's directory layout is not the client's business.
	if result.SafetyCopy == "" {
		t.Error("the restore did not write a safety copy")
	}
	if strings.Contains(result.SafetyCopy, "/") {
		t.Errorf("the answer names the safety copy as %q, which leaks the directory", result.SafetyCopy)
	}

	sensors := h.listSensors()
	found := false
	for _, s := range sensors {
		if s.Name == "Router" {
			found = true
		}
	}
	if !found {
		t.Error("the deleted sensor did not come back")
	}

	// The event stream says so, so that an open dashboard reloads instead of showing
	// the configuration of a moment ago.
	deadline := time.After(2 * time.Second)
	kinds := map[string]bool{}
	for len(kinds) < 2 {
		select {
		case ev := <-events:
			kinds[string(ev.Kind)] = true
		case <-deadline:
			t.Fatalf("the restore published %v, want a sensor and a settings event", kinds)
		}
	}
	if !kinds["sensor"] || !kinds["settings"] {
		t.Errorf("the restore published %v, want a sensor and a settings event", kinds)
	}
}

// TestRestoreRefusesABadFileWithoutChangingAnything checks the answer a person gets,
// and that nothing moved behind it.
func TestRestoreRefusesABadFileWithoutChangingAnything(t *testing.T) {
	h := newHarness(t)
	h.setup()

	before := h.server.store.Setting(t.Context(), model.SettingTileURL, "")
	sensorsBefore := h.listSensors()

	for _, c := range []struct {
		what string
		body []byte
		want int
	}{
		{"an empty body", nil, http.StatusBadRequest},
		{"something that is not JSON at all", []byte("PK\x03\x04a zip file"), http.StatusBadRequest},
		// A well-formed JSON document that happens to be somebody else's file: the
		// `format` field is the one thing that has to match, so anything else has to
		// be refused before a single row is touched.
		{"JSON that is not a backup", []byte(`{"format":"some-other-tool"}`), http.StatusBadRequest},
		{"a file from a newer version", []byte(
			`{"format":"weatherglass-backup","format_version":99,"schema_version":3,"settings":{}}`),
			http.StatusBadRequest},
		{"a file from a newer schema", []byte(
			`{"format":"weatherglass-backup","format_version":1,"schema_version":99,"settings":{}}`),
			http.StatusBadRequest},
	} {
		resp := h.postRaw("/api/restore", c.body, origin(h))
		var body struct {
			Error string `json:"error"`
		}
		decode(t, resp, &body)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status = %d, want %d", c.what, resp.StatusCode, c.want)
			continue
		}
		if body.Error == "" {
			t.Errorf("%s: the answer carries no explanation", c.what)
		}
		// The sentinel the store wraps its complaints with is for errors.Is and for the
		// log; it must not be the first thing a person reads.
		if strings.Contains(body.Error, "not a usable backup") {
			t.Errorf("%s: the message reads %q", c.what, body.Error)
		}
	}

	if after := h.server.store.Setting(t.Context(), model.SettingTileURL, ""); after != before {
		t.Error("a refused file changed a setting")
	}
	if after := h.listSensors(); len(after) != len(sensorsBefore) {
		t.Errorf("a refused file left %d sensors, there were %d", len(after), len(sensorsBefore))
	}
}

// TestRestoreRefusesAFileThatIsTooLarge pins the one limit that cannot be reached by
// accident with a real backup: 64 MiB is about ten times a large installation's history,
// so anything above it was not produced by this program.
func TestRestoreRefusesAFileThatIsTooLarge(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// A body that is over the limit but still starts like a backup, so that the size
	// is the only thing wrong with it. It is never decoded: the reader stops at the
	// cap.
	huge := append([]byte(`{"format":"weatherglass-backup","pad":"`),
		bytes.Repeat([]byte("x"), (64<<20)+1024)...)

	resp := h.postRaw("/api/restore", huge, origin(h))
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}
	if !strings.Contains(body.Error, "MiB") {
		t.Errorf("the message does not say how large is too large: %q", body.Error)
	}
}
