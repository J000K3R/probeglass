package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"

	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
	"github.com/j000k3r/weatherglass/internal/totp"
)

const (
	sessionCookie = "weatherglass_session"
	// sessionTTL is how long a dashboard login stays valid.
	sessionTTL = 30 * 24 * time.Hour
	// challengeTTL is how long the half-finished sign-in between the password and
	// the second factor stays valid. It has to be long enough to find a phone and
	// type six digits, and short enough that it is worthless later.
	challengeTTL = 2 * time.Minute
	// bcryptCost 12 is a good balance for a homelab box: strong enough that a
	// stolen hash is expensive to crack, fast enough to log in instantly.
	bcryptCost = 12
	// minPasswordLength is deliberately only a floor; the threat model is a
	// private network, not the public internet.
	minPasswordLength = 8
	// maxUsernameLength is a sanity bound, not a security one.
	maxUsernameLength = 64

	// sessionPrefix and challengePrefix are what keep the two signed values from
	// being mistakable for one another. They are checked before the signature, so
	// a value can only ever be read as the kind of token it was minted as.
	//
	// The version prefix on the session also retires the pre-multi-user cookie
	// format, which carried no identity at all: it is now rejected with a plain
	// "sign in again", which is the correct outcome and the only one available.
	sessionPrefix   = "v2."
	challengePrefix = "c1."
)

// dummyHash is what a password is compared against when the username does not
// exist.
//
// It exists for the timing, not for the answer: a missing account would otherwise
// return before bcrypt ran, and the difference between "no such user" and "wrong
// password" would be visible in a stopwatch to anyone who cared to look. It is
// generated once, on the first login with a name that does not exist, so the cost
// is not paid at every start.
var dummyHash = sync.OnceValues(func() (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte("weatherglass no such user"), bcryptCost)
	return string(h), err
})

// ---------------------------------------------------------------------------
// who is asking
// ---------------------------------------------------------------------------

// principal is the identity a request is acting as, put into the request context
// by requireAuth so that no handler has to look it up a second time.
type principal struct {
	// User is the signed-in account. It is nil for the API key, which belongs to
	// no account: it is a script credential for the whole installation.
	User *model.User
	// ByKey records that the request was authenticated with the API key. It is
	// what ackAuthor reports as "api", and it is what exempts a write from the
	// Origin check, which only exists to protect an ambient cookie.
	ByKey bool
}

// Role is what the principal may do. The API key is an administrator: it is the
// same credential the Home Assistant integration and every script use, and it has
// always had full access. That is a deliberate decision and is written down in
// SECURITY.md rather than left implicit.
func (p principal) Role() model.Role {
	if p.User == nil {
		return model.RoleAdmin
	}
	return p.User.Role
}

// Name is how the principal is recorded as the actor in an incident note.
func (p principal) Name() string {
	if p.User == nil {
		return "api"
	}
	return p.User.Username
}

// principalKey is the context key. An unexported struct type, so nothing outside
// this package can plant a principal in a context.
type principalKey struct{}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// principalOf returns the principal requireAuth attached. The second result is
// false in a test that calls a handler directly rather than through the router.
func principalOf(r *http.Request) (principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(principal)
	return p, ok
}

// ---------------------------------------------------------------------------
// the server-side auth state
// ---------------------------------------------------------------------------

// loadAuth establishes the session secret, the API key and the initial account.
func (s *Server) loadAuth(ctx context.Context) error {
	secret, err := s.store.EnsureSecret(ctx, model.SettingSessionSecret)
	if err != nil {
		return fmt.Errorf("session secret: %w", err)
	}
	rawSecret, err := hex.DecodeString(secret)
	if err != nil {
		return fmt.Errorf("session secret is not hex: %w", err)
	}

	s.authMu.Lock()
	s.sessionSecret = rawSecret
	s.apiKey = s.store.Setting(ctx, model.SettingAPIKey, "")
	s.authMu.Unlock()

	users, err := s.store.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("count users: %w", err)
	}
	if users == 0 && s.cfg.AdminPassword != "" {
		if err := s.bootstrapAdmin(ctx, s.cfg.AdminPassword); err != nil {
			return err
		}
		return nil
	}
	if users == 0 {
		s.log.Warn("no user account yet; open the web interface to create one")
	}
	return nil
}

// bootstrapAdmin creates the first account from WEATHERGLASS_ADMIN_PASSWORD. It
// only ever runs on a database with no users at all, which is the same condition
// the setup screen checks, so it cannot add an administrator to a running
// installation.
func (s *Server) bootstrapAdmin(ctx context.Context, password string) error {
	hash, err := hashPassword(password)
	if err != nil {
		return fmt.Errorf("WEATHERGLASS_ADMIN_PASSWORD: %w", err)
	}
	u := &model.User{Username: "admin", PasswordHash: hash, Role: model.RoleAdmin}
	if err := s.store.CreateFirstUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrSetupTaken) {
			// Another process won the race. Nothing to do and nothing wrong.
			return nil
		}
		return err
	}
	s.log.Info("administrator account created from WEATHERGLASS_ADMIN_PASSWORD", "username", u.Username)
	return nil
}

func hashPassword(pw string) (string, error) {
	if len(pw) < minPasswordLength {
		return "", fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	if len(pw) > 256 {
		return "", errors.New("password must be at most 256 characters")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// validUsername checks a name before it reaches the database. The rules are the
// ones people actually use in a login name; what is deliberately excluded is
// whitespace and control characters, because a username ends up in log lines, in
// incident notes and in an otpauth:// label, and all three are places where a
// stray newline is a problem rather than a curiosity.
func validUsername(name string) error {
	if name == "" {
		return errors.New("a username is required")
	}
	if len([]rune(name)) > maxUsernameLength {
		return fmt.Errorf("a username may be at most %d characters", maxUsernameLength)
	}
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case strings.ContainsRune("._@+-", r):
		default:
			return errors.New("a username may contain letters, digits and . _ @ + - only")
		}
	}
	return nil
}

// setupRequired reports whether the installation still needs its first account.
//
// It asks the database rather than a cached flag. The cache would have to be
// invalidated by every path that creates or deletes a user, and being wrong in
// that direction means the public setup route stays open.
func (s *Server) setupRequired() bool {
	n, err := s.store.CountUsers(context.Background())
	if err != nil {
		// Unreadable means "assume an account exists": refusing the setup screen is
		// a nuisance, opening it on a running installation is a way in.
		return false
	}
	return n == 0
}

func (s *Server) secrets() (apiKey string, secret []byte) {
	s.authMu.RLock()
	defer s.authMu.RUnlock()
	return s.apiKey, s.sessionSecret
}

// ---------------------------------------------------------------------------
// signed values
// ---------------------------------------------------------------------------

func sign(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verifySigned splits "<payload>.<signature>" and checks the signature. The split
// is at the last dot, because a payload may contain dots of its own.
func verifySigned(key []byte, value string) (string, bool) {
	i := strings.LastIndexByte(value, '.')
	if i < 0 {
		return "", false
	}
	payload, sig := value[:i], value[i+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	if !hmac.Equal(mac.Sum(nil), got) {
		return "", false
	}
	return payload, true
}

// signingKey derives a key for one purpose from the session secret.
//
// A challenge signed with the session secret itself would be a session, and the
// only thing standing between the two would be the prefix check. Deriving a
// separate key means the two are not interchangeable even if a later version
// gives them the same payload.
func signingKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("wg-" + purpose + "-v1"))
	return mac.Sum(nil)
}

// signSession builds a cookie value of the form
// "v2.<userID>.<tokenEpoch>.<expiryUnix>.<hmac>".
func signSession(secret []byte, userID, epoch int64, expiry time.Time) string {
	payload := fmt.Sprintf("%s%d.%d.%d", sessionPrefix, userID, epoch, expiry.Unix())
	return payload + "." + sign(secret, payload)
}

// verifySession checks a cookie value and returns the account and epoch it was
// issued for.
//
// A value that is not a session is refused by the prefix, before the signature is
// even considered. That is what makes a challenge token unusable here.
func verifySession(secret []byte, value string) (userID, epoch int64, ok bool) {
	if !strings.HasPrefix(value, sessionPrefix) {
		return 0, 0, false
	}
	payload, ok := verifySigned(secret, value)
	if !ok {
		return 0, 0, false
	}
	fields := strings.Split(strings.TrimPrefix(payload, sessionPrefix), ".")
	if len(fields) != 3 {
		return 0, 0, false
	}
	userID, err1 := strconv.ParseInt(fields[0], 10, 64)
	epoch, err2 := strconv.ParseInt(fields[1], 10, 64)
	expiry, err3 := strconv.ParseInt(fields[2], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil || userID <= 0 {
		return 0, 0, false
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return 0, 0, false
	}
	return userID, epoch, true
}

// signChallenge mints the short-lived proof that a password was accepted, which
// the second-factor step trades for a session.
func (s *Server) signChallenge(userID int64) string {
	_, secret := s.secrets()
	payload := fmt.Sprintf("%s%d.%d", challengePrefix, userID, time.Now().Add(challengeTTL).Unix())
	return payload + "." + sign(signingKey(secret, "challenge"), payload)
}

// verifyChallenge checks a challenge and returns the account it belongs to.
func (s *Server) verifyChallenge(value string) (int64, bool) {
	if !strings.HasPrefix(value, challengePrefix) {
		return 0, false
	}
	_, secret := s.secrets()
	payload, ok := verifySigned(signingKey(secret, "challenge"), value)
	if !ok {
		return 0, false
	}
	fields := strings.Split(strings.TrimPrefix(payload, challengePrefix), ".")
	if len(fields) != 2 {
		return 0, false
	}
	userID, err1 := strconv.ParseInt(fields[0], 10, 64)
	expiry, err2 := strconv.ParseInt(fields[1], 10, 64)
	if err1 != nil || err2 != nil || userID <= 0 {
		return 0, false
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return 0, false
	}
	return userID, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, userID, epoch int64, expiry time.Time) {
	_, secret := s.secrets()
	//nolint:gosec // G124: HttpOnly and SameSite are set; Secure is left off on
	// purpose, for the homelab-over-plain-HTTP reason spelled out below.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    signSession(secret, userID, epoch, expiry),
		Path:     "/",
		Expires:  expiry,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure is deliberately not set: homelab installs are commonly reached
		// over plain HTTP on a LAN. Put a TLS reverse proxy in front when the
		// dashboard is reachable from anywhere else.
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	//nolint:gosec // G124: same deliberate omission of Secure as above; the
	// cookie is being expired, so it carries no session either way.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// sessionUser resolves a session cookie to the account it belongs to.
//
// The account row is read on every request, and the token epoch in the cookie is
// compared with the one stored. That read is the whole reason a role change, a
// disable or a deletion takes effect on the next click instead of in thirty days.
// It is one indexed lookup in a local SQLite file, next to a bcrypt comparison or
// a network probe it is nothing.
//
// "No session" and "the database is not answering" are kept apart deliberately.
// Both used to come back as a plain false, which meant an unreadable database
// looked to the dashboard exactly like a signed-out browser: the operator was
// bounced to the login form and typed a password that could not possibly work,
// with nothing on screen saying the disk was the problem. The error travels up so
// that the request can fail as the server fault it is.
func (s *Server) sessionUser(r *http.Request) (*model.User, bool, error) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false, nil
	}
	_, secret := s.secrets()
	userID, epoch, ok := verifySession(secret, c.Value)
	if !ok {
		return nil, false, nil
	}
	u, err := s.store.GetUser(r.Context(), userID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// The account was deleted while the cookie was still out there. That is
		// not a failure of the installation, it is the deletion working.
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	if u.Disabled || u.TokenEpoch != epoch {
		return nil, false, nil
	}
	return &u, true, nil
}

// apiKeyValid reports whether the request carries the configured API key.
func (s *Server) apiKeyValid(r *http.Request) bool {
	key, _ := s.secrets()
	if key == "" {
		return false
	}
	presented := r.Header.Get("X-API-Key")
	if presented == "" {
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			presented = strings.TrimPrefix(auth, "Bearer ")
		}
	}
	if presented == "" {
		return false
	}
	return hmac.Equal([]byte(presented), []byte(key))
}

// authenticate resolves the request's identity, by API key first and cookie
// second. The error is the store failing to answer, which the caller must not
// mistake for a missing session.
func (s *Server) authenticate(r *http.Request) (principal, bool, error) {
	if s.apiKeyValid(r) {
		return principal{ByKey: true}, true, nil
	}
	u, ok, err := s.sessionUser(r)
	if err != nil {
		return principal{}, false, err
	}
	if !ok {
		return principal{}, false, nil
	}
	return principal{User: u}, true, nil
}

// requireAuth wraps a handler with authentication, an authorization floor and a
// cross-site request check for cookie-authenticated writes.
//
// min is the least role that may reach the handler. Everything is expressed as a
// comparison against the role order rather than as a list of allowed roles, so a
// new route is a one-word decision and a role added later does not silently gain
// access to routes nobody revisited.
func (s *Server) requireAuth(min model.Role, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := s.authenticate(r)
		if err != nil {
			s.fail(w, err, http.StatusInternalServerError, "authentication could not be checked")
			return
		}
		if !ok {
			if s.setupRequired() {
				writeError(w, http.StatusConflict, "setup required")
				return
			}
			writeError(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if p.Role().Rank() < min.Rank() {
			writeError(w, http.StatusForbidden,
				"this action needs the %s role; you are signed in as %s", min, p.Role())
			return
		}

		// A cookie is sent automatically by the browser, so writes must be
		// proven to originate from this dashboard. Strict SameSite already
		// blocks cross-site sends; this is the second lock on the door.
		if !p.ByKey && isMutating(r.Method) && !sameOrigin(r) {
			writeError(w, http.StatusForbidden, "cross-site request rejected")
			return
		}
		next(w, r.WithContext(withPrincipal(r.Context(), p)))
	})
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOrigin compares the Origin (or Referer) header against the Host header.
// Requests without either header (curl, scripts) are allowed through: they
// carry no ambient cookie and are already covered by the API key path.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if ref := r.Header.Get("Referer"); ref != "" {
			if i := strings.Index(ref, "://"); i >= 0 {
				rest := ref[i+3:]
				if j := strings.IndexByte(rest, '/'); j >= 0 {
					origin = ref[:i+3] + rest[:j]
				} else {
					origin = ref
				}
			}
		}
	}
	if origin == "" {
		return true
	}
	host := r.Host
	// Compare scheme-less authorities, tolerating an http/https mismatch on
	// reverse-proxied setups where the browser sees TLS but the proxy strips it.
	trimmed := origin
	if i := strings.Index(trimmed, "://"); i >= 0 {
		trimmed = trimmed[i+3:]
	}
	return strings.EqualFold(trimmed, host)
}

// ---------------------------------------------------------------------------
// login throttling
// ---------------------------------------------------------------------------

// loginLimiter slows down password guessing without needing external state.
type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
	max      int
	window   time.Duration
}

type attemptRecord struct {
	count int
	first time.Time
	until time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		attempts: map[string]*attemptRecord{},
		max:      max,
		window:   window,
	}
}

// allowed reports whether a login from key may proceed.
func (l *loginLimiter) allowed(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.attempts[key]
	if !ok {
		return true, 0
	}
	now := time.Now()
	if rec.until.After(now) {
		return false, time.Until(rec.until)
	}
	if now.Sub(rec.first) > l.window {
		delete(l.attempts, key)
	}
	return true, 0
}

// fail records a failed attempt and returns the lockout duration, if any.
func (l *loginLimiter) fail(key string) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	rec, ok := l.attempts[key]
	if !ok || now.Sub(rec.first) > l.window {
		rec = &attemptRecord{first: now}
		l.attempts[key] = rec
	}
	rec.count++
	if rec.count >= l.max {
		rec.until = now.Add(l.window)
		rec.count = 0
		rec.first = now
		return l.window
	}
	return 0
}

// succeed clears the record after a correct password.
func (l *loginLimiter) succeed(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, key)
}

// ---------------------------------------------------------------------------
// session and sign-in handlers
// ---------------------------------------------------------------------------

// sessionAccount is the signed-in user as the dashboard sees them. It is
// separate from model.User so that the response cannot accidentally grow a field
// nobody meant to publish.
type sessionAccount struct {
	ID       int64      `json:"id"`
	Username string     `json:"username"`
	Role     model.Role `json:"role"`
	// TOTPEnabled and RecoveryCodesLeft drive the Two-factor panel, which has to
	// know whether to offer enrolment or removal.
	TOTPEnabled       bool `json:"totp_enabled"`
	RecoveryCodesLeft int  `json:"recovery_codes_left"`
}

type sessionResponse struct {
	Authenticated   bool              `json:"authenticated"`
	SetupRequired   bool              `json:"setup_required"`
	Version         string            `json:"version"`
	BaseURL         string            `json:"base_url,omitempty"`
	UI              map[string]string `json:"ui"`
	User            *sessionAccount   `json:"user,omitempty"`
	RetentionRaw    int               `json:"retention_raw_hours"`
	RetentionHourly int               `json:"retention_hourly_hours"`
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	// Unlike every other route, an unreadable store is not reported as an error
	// here. This is what the dashboard asks before it draws anything, and a 500
	// would leave it with a blank page and no way forward. Answering "not signed
	// in" at least gets the login form on screen, where the next attempt — a real
	// request against the same broken store — says what is actually wrong.
	p, authenticated, _ := s.authenticate(r)
	resp := sessionResponse{
		Authenticated: authenticated,
		SetupRequired: s.setupRequired(),
		Version:       model.Version,
		BaseURL:       s.cfg.BaseURL,
		UI:            uiSettings(p.User),
	}
	if p.User != nil {
		left, err := s.store.CountRecoveryCodes(r.Context(), p.User.ID)
		if err != nil {
			left = 0
		}
		resp.User = &sessionAccount{
			ID:                p.User.ID,
			Username:          p.User.Username,
			Role:              p.User.Role,
			TOTPEnabled:       p.User.TOTPEnabled,
			RecoveryCodesLeft: left,
		}
	}
	raw, hourly := s.store.Retention()
	resp.RetentionRaw = int(raw.Hours())
	resp.RetentionHourly = int(hourly.Hours())
	writeJSON(w, http.StatusOK, resp)
}

// uiSettings renders the interface preferences. With no account behind the
// request — an API key, or nobody at all — it yields the defaults.
func uiSettings(u *model.User) map[string]string {
	prefs := model.DefaultPrefs()
	if u == nil {
		return prefs
	}
	for _, k := range model.PrefKeys {
		prefs[k] = u.Pref(k)
	}
	return prefs
}

// commitContext detaches a write from the client's connection, but not from
// time.
//
// Both callers hash a password with bcrypt first, which costs about a quarter of
// a second, and a quarter of a second is long enough for an impatient reload to
// end the request. Writing with r.Context() after that means the work is thrown
// away at the last moment: the password the user chose is not stored, nothing
// tells them so, and the next page load shows the form again as if they had
// never filled it in. That is a silent loss of the one piece of state they
// explicitly asked to set.
//
// Detaching keeps the outcome. The timeout keeps a stalled disk from pinning
// the connection open, and it is generous, because the write is a few bytes into
// a local SQLite file.
func commitContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if !s.setupRequired() {
		writeError(w, http.StatusConflict, "an account already exists")
		return
	}
	// Mode is the interface mode chosen on the step after the password. It is
	// optional so that a script driving /api/setup keeps working without knowing
	// about it; an omitted mode leaves the preference unwritten, and the dashboard
	// then reads the fallback, which shows everything.
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Mode     string `json:"mode"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if err := validUsername(in.Username); err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	if in.Mode != "" {
		valid := false
		for _, m := range allowedUIValues[model.PrefMode] {
			if m == in.Mode {
				valid = true
				break
			}
		}
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid mode %q", in.Mode)
			return
		}
	}
	hash, err := hashPassword(in.Password)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	// The password change itself is detached from the request, for the reason
	// spelled out on commitContext; the account creation happens inside the same
	// detached window because the two are one operation.
	ctx, cancel := commitContext(r)
	defer cancel()

	u := &model.User{Username: in.Username, PasswordHash: hash, Role: model.RoleAdmin}
	if err := s.store.CreateFirstUser(ctx, u); err != nil {
		if errors.Is(err, store.ErrSetupTaken) {
			writeError(w, http.StatusConflict, "an account already exists")
			return
		}
		if errors.Is(err, store.ErrUserExists) {
			writeError(w, http.StatusConflict, "that username is already taken")
			return
		}
		s.fail(w, err, http.StatusInternalServerError, "could not create the account")
		return
	}

	// The account exists, which is the thing the user asked for; everything below
	// is a courtesy and must not be able to turn a successful setup into a failed
	// one. Each failure is logged and otherwise ignored.
	if in.Mode != "" {
		if err := s.store.SetUserPref(ctx, u.ID, model.PrefMode, in.Mode); err != nil {
			s.log.Error("could not store the interface mode", "error", err)
		}
	}
	if n, err := s.createExampleSensors(ctx, exampleDialAddr(s.cfg)); err != nil {
		s.log.Error("could not create the example sensors", "error", err, "created", n)
	} else if n > 0 {
		s.log.Info("created example sensors", "count", n)
	}

	s.setSessionCookie(w, u.ID, u.TokenEpoch, time.Now().Add(sessionTTL))
	s.log.Info("first user account created", "username", u.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.setupRequired() {
		writeError(w, http.StatusConflict, "setup required")
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	key := clientKey(r)
	if ok, wait := s.logins.allowed(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests,
			"too many failed attempts, try again in %s", wait.Round(time.Second))
		return
	}

	user, err := s.store.GetUserByName(r.Context(), in.Username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.fail(w, err, http.StatusInternalServerError, "could not look up the account")
		return
	}
	hash := user.PasswordHash
	if hash == "" {
		// No such account. Compare against the dummy hash anyway, so that the
		// answer takes as long as a wrong password does. If generating it failed
		// the comparison below returns immediately, which is a slightly faster
		// refusal and nothing worse.
		if dummy, err := dummyHash(); err == nil {
			hash = dummy
		}
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(in.Password)) != nil {
		if lock := s.logins.fail(key); lock > 0 {
			s.log.Warn("login locked out after repeated failures", "client", key)
		}
		// One message for both "no such user" and "wrong password": which of the
		// two it was is not something an unauthenticated caller needs to know.
		writeError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	if user.Disabled {
		s.logins.fail(key)
		writeError(w, http.StatusForbidden, "this account is disabled")
		return
	}

	s.logins.succeed(key)

	if user.TOTPEnabled {
		// The password is right, but it is only half of the credential. The
		// challenge says so, and expires on its own.
		writeJSON(w, http.StatusOK, map[string]any{
			"totp_required": true,
			"challenge":     s.signChallenge(user.ID),
		})
		return
	}

	s.recordLogin(r, user.ID)
	s.setSessionCookie(w, user.ID, user.TokenEpoch, time.Now().Add(sessionTTL))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleLoginTOTP is the second step: it trades a challenge and a code for a
// session. The code may also be one of the account's recovery codes.
func (s *Server) handleLoginTOTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	userID, ok := s.verifyChallenge(in.Challenge)
	if !ok {
		writeError(w, http.StatusUnauthorized, "this sign-in attempt has expired; start again")
		return
	}

	// Throttled per account, not per client address: the challenge has already
	// proved that whoever holds it knows the password, so the thing being guessed
	// at is this account's six digits.
	key := "totp:" + strconv.FormatInt(userID, 10)
	if ok, wait := s.totpLogins.allowed(key); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests,
			"too many failed attempts, try again in %s", wait.Round(time.Second))
		return
	}

	user, err := s.store.GetUser(r.Context(), userID)
	if err != nil || user.Disabled || !user.TOTPEnabled {
		writeError(w, http.StatusUnauthorized, "this sign-in attempt has expired; start again")
		return
	}

	now := time.Now()
	step, codeOK := totp.Verify(user.TOTPSecret, in.Code, now)
	// A code from a step this account has already spent is not a wrong code, it is
	// a replayed one, and the two have different fixes — so they get different
	// answers. Recognising it costs nothing: Verify has already done the work.
	replayed := codeOK && step <= user.TOTPLastStep

	if codeOK && !replayed {
		if err := s.store.RecordTOTPStep(r.Context(), user.ID, step); err != nil {
			s.fail(w, err, http.StatusInternalServerError, "could not record the sign-in")
			return
		}
	} else {
		used, err := s.store.ConsumeRecoveryCode(r.Context(), user.ID, in.Code, now)
		if err != nil {
			s.fail(w, err, http.StatusInternalServerError, "could not check the recovery code")
			return
		}
		if !used {
			s.totpLogins.fail(key)
			if replayed {
				writeError(w, http.StatusUnauthorized,
					"that code has already been used; wait for the next one")
				return
			}
			writeError(w, http.StatusUnauthorized, "wrong code")
			return
		}
	}

	s.totpLogins.succeed(key)
	s.recordLogin(r, user.ID)
	s.setSessionCookie(w, user.ID, user.TokenEpoch, time.Now().Add(sessionTTL))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// recordLogin stamps the last successful sign-in. It is a courtesy to whoever
// reads the user list, so a failure is logged and otherwise ignored.
func (s *Server) recordLogin(r *http.Request, userID int64) {
	if err := s.store.RecordUserLogin(context.WithoutCancel(r.Context()), userID, time.Now().UTC()); err != nil {
		s.log.Error("could not record the sign-in", "error", err, "user", userID)
	}
}

// clientKey identifies a client for throttling purposes.
func clientKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// ---------------------------------------------------------------------------
// the signed-in user's own credentials
// ---------------------------------------------------------------------------

// passwordChange is the body of PUT /api/password.
type passwordChange struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p, ok := principalOf(r)
	if !ok || p.User == nil {
		// The API key has no password. Saying which credential is missing is more
		// useful than a bare refusal.
		writeError(w, http.StatusBadRequest, "the API key has no password to change")
		return
	}
	var in passwordChange
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(p.User.PasswordHash), []byte(in.Current)) != nil {
		writeError(w, http.StatusUnauthorized, "current password is wrong")
		return
	}
	newHash, err := hashPassword(in.New)
	if err != nil {
		writeError(w, http.StatusBadRequest, "%s", err.Error())
		return
	}
	// Detached from the request for the same reason as in handleSetup: the new
	// password has already been hashed, the user typed it, and abandoning the
	// write here would leave the old password in place while they believe it
	// was changed.
	ctx, cancel := commitContext(r)
	defer cancel()
	if err := s.store.SetUserPassword(ctx, p.User.ID, newHash); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not store the password")
		return
	}
	// The write bumped this account's token epoch, so every session of it — this
	// one included — is now stale. Signing in again means re-issuing the cookie
	// under the new epoch, which is cheaper and quieter than making the user do
	// it. Only this account's sessions are affected: other people's are not.
	u, err := s.store.GetUser(ctx, p.User.ID)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not re-read the account")
		return
	}
	s.setSessionCookie(w, u.ID, u.TokenEpoch, time.Now().Add(sessionTTL))
	s.log.Info("password changed", "username", u.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRotateAPIKey(w http.ResponseWriter, r *http.Request) {
	key, err := store.NewToken(24)
	if err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not generate a key")
		return
	}
	if err := s.store.SetSetting(r.Context(), model.SettingAPIKey, key); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not store the key")
		return
	}
	s.authMu.Lock()
	s.apiKey = key
	s.authMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"api_key": key})
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSetting(r.Context(), model.SettingAPIKey); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not delete the key")
		return
	}
	s.authMu.Lock()
	s.apiKey = ""
	s.authMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
