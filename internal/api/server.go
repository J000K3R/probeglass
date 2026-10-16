// Package api exposes the REST interface and serves the embedded dashboard.
//
// Everything under /api requires authentication, either through the session
// cookie the dashboard uses or through an API key for scripts. The dashboard
// shell itself is public: it has to load before a user can log in.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/scheduler"
	"github.com/j000k3r/probeglass/internal/store"
)

// Server holds the HTTP dependencies.
type Server struct {
	cfg        *config.Config
	store      *store.Store
	sched      *scheduler.Scheduler
	dispatcher *notify.Dispatcher
	broker     *event.Broker
	log        *slog.Logger
	assets     fs.FS
	assetETags map[string]string

	authMu        sync.RWMutex
	apiKey        string
	sessionSecret []byte

	// mapMu guards tileOrigin, the one piece of configuration read on every
	// single response. It is cached rather than read from the database per
	// request because the content policy needs it in the middleware, before any
	// handler runs, and a settings lookup there would put a database read in
	// front of every stylesheet.
	mapMu      sync.RWMutex
	tileOrigin string

	// logins throttles the password step by client address; totpLogins throttles
	// the second step by account. They are separate counters on purpose: the first
	// is guessing at many names from one place, the second at one name from
	// anywhere, and sharing a budget between them would let either exhaust the
	// other.
	logins     *loginLimiter
	totpLogins *loginLimiter

	// doctor serialises `probeglass doctor` runs started from the dashboard and
	// remembers the last one. It is the only endpoint here that reads the whole
	// database and dials the daemon's own health endpoint, and repeated clicks
	// must not turn into repeated full passes.
	doctor doctorCache
}

// Options configures a Server.
type Options struct {
	Config     *config.Config
	Store      *store.Store
	Scheduler  *scheduler.Scheduler
	Dispatcher *notify.Dispatcher
	Broker     *event.Broker
	Logger     *slog.Logger
	Assets     fs.FS
	// AssetETags is the validator for each file in Assets, keyed by its path
	// inside the assets directory (web.Fingerprints()). Empty means no
	// validators, which is what a test with an in-memory Assets wants and what
	// the dashboard gets away with when the map is a few kilobytes — but the
	// shipped dashboard is not, so main passes it in.
	AssetETags map[string]string
}

// New builds a Server and loads its authentication state.
func New(ctx context.Context, opts Options) (*Server, error) {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:        opts.Config,
		store:      opts.Store,
		sched:      opts.Scheduler,
		dispatcher: opts.Dispatcher,
		broker:     opts.Broker,
		log:        log,
		assets:     opts.Assets,
		assetETags: opts.AssetETags,
		logins:     newLoginLimiter(5, time.Minute),
		totpLogins: newLoginLimiter(5, time.Minute),
	}
	if err := s.loadAuth(ctx); err != nil {
		return nil, err
	}
	s.refreshTileOrigin(ctx)
	return s, nil
}

// TileOrigin is the origin the content policy allows images from, empty when
// there is none to allow.
func (s *Server) TileOrigin() string {
	s.mapMu.RLock()
	defer s.mapMu.RUnlock()
	return s.tileOrigin
}

// setTileOrigin records the origin derived from the configured tile URL.
func (s *Server) setTileOrigin(origin string) {
	s.mapMu.Lock()
	defer s.mapMu.Unlock()
	s.tileOrigin = origin
}

// refreshTileOrigin re-reads the tile URL and caches the origin it implies.
//
// A read error is not fatal: the map would then draw no tiles, which is the same
// thing an unset tile URL does, and neither is a reason to refuse to serve a
// dashboard. The default is used instead, so the failure mode of a broken
// settings table is the map working rather than the whole dashboard not.
func (s *Server) refreshTileOrigin(ctx context.Context) {
	tileURL := s.store.Setting(ctx, model.SettingTileURL, model.DefaultTileURL)
	s.setTileOrigin(tileOriginOf(tileURL))
}

// Handler builds the routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Public endpoints. These are the only routes reachable without a credential,
	// and the only one of them that changes anything is /api/setup, which refuses
	// to run once an account exists.
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/session", s.handleSession)
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/login/totp", s.handleLoginTOTP)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("POST /api/setup", s.handleSetup)

	// Authenticated endpoints, sorted by the least role that may reach them.
	//
	// Read routes sit at viewer, the things somebody on call does about an outage
	// sit at operator, and everything that changes the configuration sits at admin.
	// A route belongs here exactly once; there is no second place that grants
	// access, so this block is the whole authorization policy.
	view := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.requireAuth(model.RoleViewer, h))
	}
	oper := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.requireAuth(model.RoleOperator, h))
	}
	adm := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, s.requireAuth(model.RoleAdmin, h))
	}

	// Reading. A viewer sees everything the dashboard draws and may change
	// nothing.
	view("GET /api/summary", s.handleSummary)
	view("GET /api/events", s.handleEvents)
	view("GET /api/sensors", s.handleListSensors)
	view("GET /api/sensors/{id}", s.handleGetSensor)
	view("GET /api/sensors/{id}/results", s.handleSensorResults)
	view("GET /api/sensors/{id}/series", s.handleSensorSeries)
	view("GET /api/sensors/{id}/uptime", s.handleSensorUptime)
	view("GET /api/sensors/{id}/incidents", s.handleSensorIncidents)
	view("GET /api/incidents", s.handleIncidents)
	view("GET /api/search", s.handleSearch)
	view("GET /api/channels", s.handleListChannels)
	view("GET /api/channel-types", s.handleChannelTypes)
	view("GET /api/settings", s.handleGetSettings)
	view("GET /api/examples", s.handleExampleData)
	view("GET /api/sites", s.handleListSites)
	view("GET /api/maintenance", s.handleListWindows)

	// Acting on an outage that has already happened. Nothing here changes what is
	// monitored or how.
	oper("POST /api/sensors/validate", s.handleValidateSensor)
	oper("PATCH /api/sensors/{id}", s.handleSetSensorEnabled)
	oper("POST /api/sensors/{id}/probe", s.handleProbeSensor)
	oper("POST /api/sensors/{id}/ack", s.handleAckSensor)
	oper("POST /api/incidents/{id}/ack", s.handleAckIncident)
	oper("DELETE /api/incidents/{id}/ack", s.handleUnackIncident)
	oper("POST /api/doctor", s.handleRunDoctor)

	// The signed-in user's own session. Everyone may spend their own time on the
	// tour and change how the dashboard looks to them, and everyone may change
	// their own password and second factor.
	view("PUT /api/settings", s.handleUpdateSettings)
	view("POST /api/onboarding/tour", s.handleSetTourVersion)
	view("PUT /api/password", s.handleChangePassword)
	view("POST /api/me/totp/start", s.handleTOTPStart)
	view("GET /api/me/totp/qr", s.handleTOTPQR)
	view("POST /api/me/totp/confirm", s.handleTOTPConfirm)
	view("POST /api/me/totp/disable", s.handleTOTPDisable)
	view("POST /api/me/totp/recovery", s.handleTOTPRecovery)

	// Changing the configuration.
	adm("POST /api/sensors", s.handleCreateSensor)
	adm("PUT /api/sensors/{id}", s.handleUpdateSensor)
	adm("DELETE /api/sensors/{id}", s.handleDeleteSensor)
	adm("PUT /api/sensor-order", s.handleSensorOrder)
	adm("POST /api/channels", s.handleCreateChannel)
	adm("PUT /api/channels/{id}", s.handleUpdateChannel)
	adm("DELETE /api/channels/{id}", s.handleDeleteChannel)
	adm("POST /api/channels/test", s.handleTestChannelDraft)
	adm("POST /api/channels/{id}/test", s.handleTestChannel)
	adm("POST /api/api-key", s.handleRotateAPIKey)
	adm("DELETE /api/api-key", s.handleDeleteAPIKey)

	// Administering the installation and the people who may use it.
	adm("GET /api/users", s.handleListUsers)
	adm("POST /api/users", s.handleCreateUser)
	adm("PUT /api/users/{id}", s.handleUpdateUser)
	adm("DELETE /api/users/{id}", s.handleDeleteUser)
	adm("POST /api/users/{id}/password", s.handleResetUserPassword)
	adm("DELETE /api/examples", s.handleDeleteExampleData)
	adm("POST /api/examples", s.handleRestoreExampleData)
	adm("POST /api/sites", s.handleCreateSite)
	adm("PUT /api/sites/{name}", s.handleUpdateSite)
	adm("DELETE /api/sites/{name}", s.handleDeleteSite)
	// Maintenance windows are configuration, not an action taken during an outage:
	// an administrator decides in advance that a box is allowed to be away, exactly
	// as they decide which boxes exist. Acknowledging an incident is the other thing,
	// and that one is an operator's.
	adm("POST /api/maintenance", s.handleCreateWindow)
	adm("PUT /api/maintenance/{id}", s.handleUpdateWindow)
	adm("DELETE /api/maintenance/{id}", s.handleDeleteWindow)
	// The map's tile URL is installation-wide — it decides which host every
	// browser in the house talks to — so writing it is an administrator's
	// decision and not a preference, unlike PUT /api/settings, which any signed-in
	// viewer may change for themselves.
	adm("PUT /api/map", s.handleUpdateMap)

	// Backing the installation up and putting it back. Both sit at admin for the
	// same reason: the file that comes out holds every secret in the installation in
	// the clear, and the restore replaces the accounts and the API key along with
	// everything else — including the account of whoever is asking.
	adm("GET /api/backup", s.handleBackup)
	adm("POST /api/restore", s.handleRestore)

	// Dashboard shell and assets.
	if s.assets != nil {
		mux.Handle("GET /assets/", http.StripPrefix("/assets/", s.assetValidators(
			http.FileServer(http.FS(s.assets)))))
		mux.HandleFunc("GET /", s.handleIndex)
	}

	return s.withMiddleware(mux)
}

// ---------------------------------------------------------------------------
// embedded assets
// ---------------------------------------------------------------------------

// assetValidators gives every embedded file an ETag and answers a request that
// already holds that version with 304.
//
// Without it the dashboard is re-downloaded on every page load. http.FileServer
// builds its validators from the file's modification time, and an embed.FS file has
// none, so it sends no Last-Modified and no ETag — and with no validator a browser
// cannot revalidate, so `no-cache` (see withMiddleware) turns into a full body every
// time. That was a few tens of kilobytes when the map was a coastline; the map now
// draws borders and place names as well, and the difference is worth a header.
//
// The path is looked up after the /assets/ prefix has been stripped and cleaned, so
// the key is the path inside the embedded tree. A path with no fingerprint falls
// through to the file server untouched: that is a file somebody added without
// regenerating the map of digests — and it will not happen, because the digests are
// computed from the same embedded tree at startup.
func (s *Server) assetValidators(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tag := s.assetETags[path.Clean(strings.TrimPrefix(r.URL.Path, "/"))]
		if tag == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("ETag", tag)
		if matchesETag(r.Header.Get("If-None-Match"), tag) {
			// A 304 carries the validator and nothing else. The file server would
			// have written a body, so it cannot be left to answer this one.
			w.WriteHeader(http.StatusNotModified)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// matchesETag reports whether an If-None-Match header covers one ETag.
//
// The header is a list of tags and may carry the placeholder `*`, and the comparison
// is the weak one: RFC 9110 says If-None-Match never compares strongly, so `W/"x"`
// and `"x"` mean the same version here. A browser sends back exactly what it was
// given, so in practice this is one plain string — but a proxy that weakens tags on
// the way must not turn every request back into a full download.
func matchesETag(header, tag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	if header == "*" {
		return true
	}
	want := strings.TrimPrefix(tag, "W/")
	for part := range strings.SplitSeq(header, ",") {
		part = strings.TrimSpace(part)
		if part == "*" || strings.TrimPrefix(part, "W/") == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// middleware
// ---------------------------------------------------------------------------

// slowRequestThreshold is when a finished request is worth a warning. Every
// handler here answers from one SQLite file in single-digit milliseconds; ten
// seconds means something is wrong, and the log line says what to check.
const slowRequestThreshold = 10 * time.Second

// hashesPasswords reports whether the request is one of the handlers that spends its
// time in bcrypt rather than in the database: signing in, the first-time setup, and
// everything that checks or sets a password.
//
// It exists because the warning below used to blame the database for all of them,
// and that was measurably wrong. A password change with one processor under the race
// detector took 10.7 seconds, every millisecond of it in bcrypt — a password change
// compares the old password and hashes the new one, so it pays twice — while the hint
// pointed the reader at a stalled disk. A hint that sends you to the wrong place is
// worse than no hint, so the slow ones that are slow on purpose say so.
//
// The method is part of the match, not decoration: GET /api/users is the user list and
// never hashes, while POST /api/users creates a user and does. Matching on the path
// alone would blame bcrypt for a slow list, which is the same mistake pointing the
// other way.
func hashesPasswords(r *http.Request) bool {
	switch r.Method + " " + r.URL.Path {
	case "POST /api/login", "POST /api/login/totp", "POST /api/setup",
		"PUT /api/password", "POST /api/users",
		"POST /api/me/totp/recovery", "POST /api/me/totp/disable":
		return true
	}
	// The password reset carries a user id, so it cannot be named above. Only the POST
	// at that shape hashes; the PUT and DELETE beside it are the user record itself.
	return r.Method == http.MethodPost &&
		strings.HasPrefix(r.URL.Path, "/api/users/") &&
		strings.HasSuffix(r.URL.Path, "/password")
}

func (s *Server) withMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		// Dashboard files are embedded in the binary and change with it. no-cache
		// rather than a lifetime: an upgraded binary must not be paired with a
		// stylesheet from the build before it, and the only thing that can tell the
		// two apart is the validator — see assetValidators, which is what makes the
		// revalidation cheap. The files are served from memory, so a 304 costs
		// nothing and a full body costs almost nothing either.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			rec.Header().Set("Cache-Control", "no-cache")
		}

		setSecurityHeaders(rec, s.TileOrigin())
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)

		// Exactly one request line per request, at error for a server-side
		// failure and at info otherwise. The error line carries the same four
		// fields plus what went wrong, so it is a strict superset of the info
		// line and nothing is lost by not also writing that one.
		//
		// Assets are excluded from the info line because a single page load
		// pulls half a dozen of them and none of them is ever the interesting
		// one. Everything else is logged, because the sequence of non-asset
		// requests is what tells a stuck dashboard apart from a broken one: a
		// browser that shows a spinner and never asks for /api/session never ran
		// app.js at all, and that is worth being able to read off the terminal.
		//
		// A 4xx stays at info. It is not a defect — the dashboard produces 409,
		// 401 and 429 as ordinary traffic — and the body already carries the
		// explanation for whoever caused it.
		switch {
		case rec.status >= 500:
			attrs := []any{
				"method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration_ms", elapsed.Milliseconds(),
			}
			if rec.message != "" {
				attrs = append(attrs, "message", rec.message)
			}
			if rec.cause != nil {
				attrs = append(attrs, "error", rec.cause)
			}
			s.log.Error("http request failed", attrs...)
		case !strings.HasPrefix(r.URL.Path, "/assets/") || rec.status >= 400:
			s.log.Info("http request",
				"method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration_ms", elapsed.Milliseconds())
		}

		// A request that took seconds did not fail loudly, it just hung, and the
		// user sees a spinner. Say so, and say which of the two causes to look at
		// first — the database, or a handler that is slow because hashing is
		// expensive by design. Both are real; guessing the wrong one costs the
		// reader an evening with a disk that was never at fault.
		if elapsed >= slowRequestThreshold {
			hint := "the database may be locked or on a stalled disk"
			if hashesPasswords(r) {
				hint = "this one hashes a password, and bcrypt is slow on purpose; " +
					"a password change compares one hash and makes another, so it pays twice"
			}
			s.log.Warn("slow request",
				"method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration_ms", elapsed.Milliseconds(),
				"hint", hint)
		}
	})
}

func setSecurityHeaders(w http.ResponseWriter, tileOrigin string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	// Inline styles are allowed because the dashboard writes bar widths and
	// colours directly onto elements; scripts stay locked to same-origin files.
	//
	// Map tiles are the one thing allowed to come from somewhere other than this
	// server, and only the exact origin the tile URL names. Two things depend on
	// this line. Without the origin, the map is a blank rectangle and the browser
	// console has the only explanation — a CSP refusal is silent on the page, so
	// the failure looks like a broken map rather than a blocked request. And with
	// it, an operator who points the tile URL at their own server on the LAN gets
	// the same treatment as the public one: allowed by name, nothing else.
	imgSrc := "img-src 'self' data:"
	if tileOrigin != "" {
		imgSrc += " " + tileOrigin
	}
	h.Set("Content-Security-Policy",
		"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			imgSrc+"; connect-src 'self'; font-src 'self'; "+
			"base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
}

// tileOriginOf reduces a tile URL template to the origin the content policy has
// to allow.
//
// It answers with "" for anything it cannot parse or that has no host, and that
// is deliberate: an empty answer means "add nothing", so a malformed setting
// cannot loosen the policy — it fails towards the map not drawing, which is
// visible, rather than towards the policy being wide open, which is not.
//
// A tile URL on this server's own origin yields "" as well, and needs no
// allowance anyway: 'self' already covers it.
func tileOriginOf(tileURL string) string {
	u, err := url.Parse(strings.TrimSpace(tileURL))
	if err != nil || u.Host == "" {
		return ""
	}
	switch u.Scheme {
	case "http", "https":
		return u.Scheme + "://" + u.Host
	default:
		return ""
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int

	// message and cause describe the last error response written for this
	// request. withMiddleware reads them once, after the handler has returned.
	// message is what the client was told; cause is the underlying error, which
	// the client never sees but the log needs.
	message string
	cause   error
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush lets Server-Sent Events through the recorder.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// recordFailure remembers an error response so that the request logger can
// report it. Everything from 400 up is recorded, because the same mechanism
// serves the whole error surface; withMiddleware decides what is worth a log
// line, and only 5xx qualifies.
//
// The later write wins. A handler that answers once does not care, and one that
// fails while streaming should be described by its last words, not its first.
func (r *statusRecorder) recordFailure(status int, msg string, cause error) {
	if status < 400 {
		return
	}
	r.message = msg
	r.cause = cause
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// The status line is already sent, so all that is left is a note.
		slog.Default().Debug("encode response", "error", err)
	}
}

// writeError returns an error to the client.
//
// Note that it is no longer free of side effects: when the middleware installed
// a *statusRecorder, the message is also recorded on it, because that is how a
// 5xx reaches the request log. Wrapping the ResponseWriter in another type
// would silently stop every server-side failure from being logged.
func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeErrorCause(w, nil, status, format, args...)
}

// writeErrorCause is writeError with the underlying error attached. The cause
// is recorded for the request log and is deliberately never sent to the client:
// the client gets the generic message, the operator gets the detail.
//
// The type assertion is tolerant on purpose. writeError also runs against a
// bare httptest.ResponseRecorder in tests, and a writer the middleware did not
// install has nowhere to record the failure — which is a missing convenience,
// not a reason to panic.
func writeErrorCause(w http.ResponseWriter, cause error, status int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if rec, ok := w.(*statusRecorder); ok {
		rec.recordFailure(status, msg, cause)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail returns a server-side error to the client and remembers err for the
// request log. Every 5xx response goes through it; writeError alone leaves the
// cause out of the log entirely.
//
// It does not log by itself. withMiddleware is the single writer of request
// lines, because it is the only place that knows the method, the path and the
// duration — and a failure logged here as well would appear twice, once without
// any of that context.
//
// Use it only where the response itself is the failure. A background error that
// does not spoil the response (a reconcile that failed after a successful
// create, say) has no status to record and belongs in a direct s.log.Error.
func (s *Server) fail(w http.ResponseWriter, err error, status int, format string, args ...any) {
	writeErrorCause(w, err, status, format, args...)
}

// errUnknownField marks the encoding/json complaint about a field the destination
// does not declare.
//
// It is told apart from other decoding failures because on one route the request type
// *is* the authorization boundary: PATCH /api/sensors/{id} is an operator's route
// whose entire contract is the single field it declares. A body carrying anything
// else there is not a malformed request, it is a request for something that role may
// not do, and it deserves the answer that says so. Everywhere else an unknown field
// stays the plain 400 it has always been.
//
// The standard library reports this as an untyped error, so the message is the only
// thing to match on; the wording is fixed by encoding/json and pinned by a test.
var errUnknownField = errors.New("unknown field")

type unknownFieldError struct{ msg string }

func (e unknownFieldError) Error() string { return e.msg }
func (e unknownFieldError) Is(target error) bool {
	return target == errUnknownField
}

// decodeJSON reads a JSON body with a size limit and rejects unknown fields so
// that typos in a client are reported instead of silently ignored.
func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if strings.HasPrefix(err.Error(), unknownFieldPrefix) {
			return unknownFieldError{msg: err.Error()}
		}
		return err
	}
	return nil
}

// unknownFieldPrefix is how encoding/json opens its unknown-field complaint.
const unknownFieldPrefix = "json: unknown field "

func pathID(r *http.Request) (int64, error) {
	raw := r.PathValue("id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
}

// parseRange turns a range query parameter into a window.
func parseRange(raw string, def model.Range) (from, to time.Time, rng model.Range, err error) {
	to = time.Now().UTC()
	if raw == "" {
		rng = def
	} else {
		rng = model.Range(raw)
	}
	if !rng.Valid() {
		return time.Time{}, time.Time{}, rng, errors.New("invalid range")
	}
	d, ok := rng.Duration()
	if !ok {
		// "all" has no start of its own. A year of daily rollups is the useful
		// reading: it is where the retention model bottoms out anyway.
		d = 365 * 24 * time.Hour
	}
	return to.Add(-d), to, rng, nil
}

func parseInt(raw string, def, min, max int) int {
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
