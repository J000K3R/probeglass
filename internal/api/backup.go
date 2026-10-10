package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/event"
	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// The two admin routes in this file: the configuration and the history as one
// downloadable file, and the way back.
//
// Both are admin routes and nothing less. A backup carries every bot token and every
// password hash in the clear — see the note at the top of internal/store/backup.go
// for why it has to — and a restore replaces the accounts, the API key and the whole
// configuration. There is no version of either one that a viewer or an operator
// should be able to reach.

// maxRestoreBytes caps the uploaded file at 64 MiB.
//
// The limit is not a guess at the size of a backup; it is the point past which a body
// is certainly not one. A history export for a large installation lands in the low
// single-digit megabytes, and a file ten times that has been edited, concatenated or
// sent by something other than the dashboard. decodeJSON's 1 MiB is far too small
// here, which is why this route does not use it.
const maxRestoreBytes = 64 << 20

// handleBackup writes the installation out as one JSON document.
//
// # Why the headers are set before the first byte and not after
//
// The export streams: the document is written table by table straight into the
// response, because a backup with history is a few megabytes and there is no reason
// to hold it in memory when the encoder can hand it out as it goes. The price of
// streaming is that the response is committed the moment the first byte is written,
// and a failure after that cannot become a 500 any more — it can only truncate.
//
// So the two things that can be checked up front are checked up front: the scope and
// the day count, which the store validates before it writes its opening brace. A
// failure further in — the disk goes away mid-export — truncates the file, and the
// truncated file is JSON that does not parse. The restore refuses it with "this is
// not a Weatherglass backup", which is the right answer to a file that stops in the
// middle. That is the real safety net, and it is the reason streaming is acceptable
// here at all.
//
// # The filename is built here, from a constant
//
// Never from anything in the request. Together with nosniff (set for every response
// in withMiddleware) and `attachment`, the browser cannot run the payload as a page
// or as a script even if somebody finds a way to make the content look like one.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = store.BackupScopeConfig
	}
	if scope != store.BackupScopeConfig && scope != store.BackupScopeHistory {
		writeError(w, http.StatusBadRequest,
			"scope must be %q or %q", store.BackupScopeConfig, store.BackupScopeHistory)
		return
	}
	days, err := parseBackupDays(r.URL.Query().Get("days"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		`attachment; filename="weatherglass-backup-`+time.Now().UTC().Format("2006-01-02")+`.json"`)
	// A backup is a credential. It must not sit in the browser cache, and no proxy
	// on the way to it should keep a copy either.
	w.Header().Set("Cache-Control", "no-store")

	out := &countingWriter{w: w}
	counts, err := s.store.Export(r.Context(), out, store.ExportOptions{
		Scope:      scope,
		Days:       days,
		AppVersion: model.Version,
	})
	if err != nil {
		// Nothing has been written, so this can still be an honest error response. If
		// something has, the status is already 200 and all that is left is to say so
		// in the log — the truncated file is what the client gets, and it will not
		// pass the format check when it is uploaded again.
		if out.n == 0 {
			w.Header().Del("Content-Disposition")
			w.Header().Del("Cache-Control")
			s.fail(w, err, http.StatusInternalServerError, "could not write the backup")
			return
		}
		s.log.Error("backup export truncated", "scope", scope, "days", days,
			"bytes_written", out.n, "error", err)
		return
	}

	s.log.Info("backup exported", append([]any{"scope", scope, "days", days},
		countAttrs(counts)...)...)
}

// parseBackupDays reads the history window.
//
// "all" is the absence of a bound rather than a very large number, which is what the
// empty query parameter means too. Everything else has to be a plain number of days;
// a typo becomes a message instead of quietly exporting something other than what was
// asked for.
func parseBackupDays(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "all" {
		return 0, nil
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days <= 0 {
		return 0, errors.New(`days must be a positive number or "all"`)
	}
	return days, nil
}

// handleRestore reads a backup from the request body and puts it in place.
//
// # The file arrives as the body, with nothing in between
//
// There is no multipart anywhere in this program, and this is not the place to invent
// the first one. The browser sends the File object it was handed as the raw request
// body — no FormData, no encoding, nothing buffered in JavaScript either — and this
// reads it from r.Body with a json.Decoder over it. The decoder streams, so the
// uploaded file is never held in memory whole.
//
// decodeJSON is the wrong helper here twice over: its limit is 1 MiB, and it insists
// on decoding into a struct it owns. This route decodes into store.BackupDoc, which
// has to be read as a whole anyway before anything is decided — the "check everything
// before touching anything" order is the store's, and it is the point.
//
// # What happens after the commit
//
// The three pieces of state the server keeps outside the database are reloaded: the
// API key and the session secret, both of which live in the settings table and would
// otherwise stay the old ones while the database knows new ones; and the map's tile
// origin, which is derived from a setting. Then the scheduler is told to catch up —
// Reconcile already exists and is already used by the sensor routes for exactly this,
// so the restored sensors start being probed without a restart. Finally the event
// stream is told, so that every dashboard that is open reloads instead of showing the
// configuration of a moment ago.
//
// None of the four failing is allowed to spoil the answer: the restore has happened
// and is committed, and reporting a failure now would invite a second one on top of a
// database that is already correct.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreBytes)

	result, err := s.store.Restore(ctx, r.Body, store.RestoreOptions{})
	if err != nil {
		s.restoreFailed(w, err)
		return
	}

	// Logged before the reloads, so that the one line that says what happened is not
	// lost behind a later complaint about a cache.
	s.log.Info("backup restored", append([]any{
		"safety_copy", result.SafetyCopy,
		"token_epoch_notice", result.TokenEpochNotice,
	}, countAttrs(result.Counts)...)...)

	if err := s.loadAuth(ctx); err != nil {
		s.log.Error("reload auth after restore", "error", err)
	}
	s.refreshTileOrigin(ctx)
	if err := s.sched.Reconcile(ctx); err != nil {
		s.log.Error("reconcile after restore", "error", err)
	}
	s.broker.Publish(event.Event{Kind: event.KindSensor, TS: time.Now().UTC()})
	s.broker.Publish(event.Event{Kind: event.KindSettings, TS: time.Now().UTC()})

	writeJSON(w, http.StatusOK, map[string]any{
		"counts":             result.Counts,
		"safety_copy":        baseName(result.SafetyCopy),
		"token_epoch_notice": result.TokenEpochNotice,
	})
}

// restoreFailed turns a failed restore into the answer it deserves.
//
// The three cases are told apart because they are three different situations for the
// person who clicked the button. The file was too big (413) — they picked something
// that is not a backup at all. The file was wrong (400) — the sentence the store wrote
// says what is wrong with it, and it is shown as it stands. Anything else (500) is this
// server failing to do its own job, and it has nothing to do with the file.
func (s *Server) restoreFailed(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge,
			"the file is larger than %d MiB, which is more than a backup of this installation can be",
			maxRestoreBytes>>20)
		return
	}
	if errors.Is(err, store.ErrBadBackup) {
		writeError(w, http.StatusBadRequest, "%s", badBackupMessage(err))
		return
	}
	s.fail(w, err, http.StatusInternalServerError, "could not restore the backup")
}

// badBackupMessage drops the sentinel's own words from a file complaint.
//
// The store wraps every complaint about a file with ErrBadBackup so that this route can
// recognise one; the wrapping text is there for errors.Is and for a log line, and it
// would read as nonsense at the top of a message meant for a person: "not a usable
// backup: this file is not a Weatherglass backup". What is wanted is the second half.
func badBackupMessage(err error) string {
	return strings.TrimPrefix(err.Error(), store.ErrBadBackup.Error()+": ")
}

// countAttrs turns a Counts into log key–value pairs.
//
// Numbers only, always. A backup file holds every secret in the installation, and the
// one way to leak it into a log is to be helpful about what was in it.
func countAttrs(c store.Counts) []any {
	return []any{
		"settings", c.Settings,
		"sites", c.Sites,
		"channels", c.Channels,
		"sensors", c.Sensors,
		"users", c.Users,
		"incidents", c.Incidents,
		"results_hourly", c.Hourly,
		"results_daily", c.Daily,
	}
}

// baseName reduces a path to its last element for display.
//
// The safety copy is written next to the database, and the person reading the message
// does not need and should not be given the server's directory layout.
func baseName(path string) string {
	if path == "" {
		return ""
	}
	if i := strings.LastIndexAny(path, `/\`); i >= 0 {
		return path[i+1:]
	}
	return path
}

// countingWriter counts what has gone out and passes it through.
//
// It exists for one question, asked in one place: has the response been committed yet?
// The export streams, so the answer decides whether a failure can still become an error
// page or can only be a truncated file.
type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
