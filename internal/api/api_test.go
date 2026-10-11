package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/event"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/notify"
	"github.com/j000k3r/probeglass/internal/scheduler"
	"github.com/j000k3r/probeglass/internal/store"
)

// TestMain drops bcrypt to its cheapest setting for the whole package.
//
// The cost exists to make a stolen hash expensive to crack. In a test the password is
// written in the file next to the hash, so the cost buys nothing there and the suite
// pays it dozens of times over: at cost 12 this package took six minutes on its own
// under the race detector, every second of it spent hashing passwords nobody is
// attacking.
//
// The two tests about a client that disconnects are the exception — they put the real
// cost back, because there the hash has to still be running after the client has gone.
// See withProductionBcryptCost.
func TestMain(m *testing.M) {
	bcryptCost = bcrypt.MinCost
	os.Exit(m.Run())
}

// withProductionBcryptCost hashes at the real cost for the duration of one test.
//
// Only the abandonment tests use it, and they are the one place where a fast hash
// would not speed the test up but empty it out: they cancel the request while the
// handler is still working, so at bcrypt.MinCost the whole handler finishes inside the
// 50 ms before the cancel lands, the context is never abandoned at all, and the test
// would pass while proving nothing.
func withProductionBcryptCost(t *testing.T) {
	t.Helper()
	old := bcryptCost
	bcryptCost = productionBcryptCost
	t.Cleanup(func() { bcryptCost = old })
}

// harness is a running API over a temporary database.
type harness struct {
	t      *testing.T
	server *Server
	store  *store.Store
	http   *httptest.Server
	cookie *http.Cookie
}

// newHarness builds a harness whose log output is thrown away. Almost every
// test here cares about status codes and bodies, and a discarded log keeps the
// transcript readable.
func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// newHarnessWith builds a harness over a caller-supplied logger, for the few
// tests that assert on what the server logged rather than on what it answered.
func newHarnessWith(t *testing.T, log *slog.Logger) *harness {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")

	st, err := store.Open(path, log)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Default()
	cfg.DBPath = path
	cfg.BaseURL = "http://probeglass.test"

	broker := event.NewBroker()
	sched := scheduler.New(st, nil, broker, log)
	srv, err := New(context.Background(), Options{
		Config:     cfg,
		Store:      st,
		Scheduler:  sched,
		Dispatcher: notify.NewDispatcher(st, log, cfg.BaseURL),
		Broker:     broker,
		Logger:     log,
	})
	if err != nil {
		t.Fatalf("build server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return &harness{t: t, server: srv, store: st, http: ts}
}

// keyedServer stores an API key and returns a second server over the same store
// that accepts it. The key is read into memory when a server is built, so this is
// what a restart after setting one looks like.
//
// It exists because a key, unlike a cookie, is checked without touching the
// database. That makes it the only way left to reach a handler when the store is
// the very thing that is broken — which is exactly the situation worth testing,
// and the one a script with a key would be in.
func (h *harness) keyedServer(key string) *httptest.Server {
	h.t.Helper()
	if err := h.store.SetSetting(context.Background(), model.SettingAPIKey, key); err != nil {
		h.t.Fatalf("store api key: %v", err)
	}
	rebuilt, err := New(context.Background(), Options{
		Config: h.server.cfg, Store: h.store, Scheduler: h.server.sched,
		Dispatcher: h.server.dispatcher, Broker: h.server.broker, Logger: h.server.log,
	})
	if err != nil {
		h.t.Fatalf("rebuild server: %v", err)
	}
	ts := httptest.NewServer(rebuilt.Handler())
	h.t.Cleanup(ts.Close)
	return ts
}

// do performs a request against the harness, carrying the session cookie.
func (h *harness) do(method, path string, body any, headers map[string]string) *http.Response {
	h.t.Helper()
	return h.doContext(context.Background(), method, path, body, headers)
}

// doContext is do with a caller-supplied context, so that a test can cancel a
// request in flight. A real browser does exactly that when the user reloads,
// and the handler has to cope with it.
func (h *harness) doContext(ctx context.Context, method, path string, body any, headers map[string]string) *http.Response {
	h.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			h.t.Fatalf("encode body: %v", err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, h.http.URL+path, reader)
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	resp, err := h.http.Client().Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

// decode reads a JSON response body and closes it.
func decode(t *testing.T, resp *http.Response, into any) {
	t.Helper()
	defer resp.Body.Close()
	if into == nil {
		return
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		t.Fatalf("decode response (status %d): %v", resp.StatusCode, err)
	}
}

// testUser and testPassword are the credentials the harness creates.
//
// The name is deliberately not "admin". A server that ignored the username it was
// handed and quietly made an "admin" anyway would pass every test that used that
// name, and the setup screen's whole point is that the name is the user's to
// choose.
const (
	testUser     = "homelab"
	testPassword = "homelab123"
)

// setup creates the first account and keeps the session cookie.
func (h *harness) setup() {
	h.t.Helper()
	resp := h.do(http.MethodPost, "/api/setup",
		map[string]string{"username": testUser, "password": testPassword}, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("setup returned %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			h.cookie = c
		}
	}
	if h.cookie == nil {
		h.t.Fatal("setup did not set a session cookie")
	}
}

// login signs in and returns the response, leaving the harness cookie alone.
func (h *harness) login(username, password string) *http.Response {
	h.t.Helper()
	return h.do(http.MethodPost, "/api/login",
		map[string]string{"username": username, "password": password}, nil)
}

// ---------------------------------------------------------------------------
// authentication
// ---------------------------------------------------------------------------

func TestPublicEndpointsNeedNoSession(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{"/healthz", "/api/version"} {
		resp := h.do(http.MethodGet, path, nil, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 without a session", path, resp.StatusCode)
		}
	}
}

func TestProtectedEndpointsRequireAuthentication(t *testing.T) {
	h := newHarness(t)

	// Before a password exists the dashboard should be told to run setup rather
	// than to log in.
	resp := h.do(http.MethodGet, "/api/sensors", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("before setup: status = %d, want 409", resp.StatusCode)
	}

	h.setup()
	h.cookie = nil

	resp = h.do(http.MethodGet, "/api/sensors", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("without a session: status = %d, want 401", resp.StatusCode)
	}
}

func TestSetupRejectsWeakPasswordsAndRunsOnlyOnce(t *testing.T) {
	h := newHarness(t)

	resp := h.do(http.MethodPost, "/api/setup",
		map[string]string{"username": testUser, "password": "abc"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("weak password: status = %d, want 400", resp.StatusCode)
	}

	// A username is not optional. Without one there is an account nobody would be
	// able to name, and the whole point of the account is that "who did this" has
	// an answer.
	resp = h.do(http.MethodPost, "/api/setup", map[string]string{"password": testPassword}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("missing username: status = %d, want 400", resp.StatusCode)
	}

	h.setup()

	// A second setup must not silently add a second administrator.
	resp = h.do(http.MethodPost, "/api/setup",
		map[string]string{"username": "someone-else", "password": "another-one"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("second setup: status = %d, want 409", resp.StatusCode)
	}

	// And the account the refused attempt named must not exist.
	if _, err := h.store.GetUserByName(context.Background(), "someone-else"); err == nil {
		t.Error("a refused setup created an account anyway")
	}
}

// abandonAfter sends a request and cancels it a moment later, which is what a
// browser does when the user reloads while the server is still working. The
// transport error is expected and ignored; what the test is after is the state
// the server is left in.
func (h *harness) abandonAfter(d time.Duration, method, path string, body any) {
	h.t.Helper()

	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatalf("encode body: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, h.http.URL+path, strings.NewReader(string(raw)))
	if err != nil {
		h.t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}

	go func() {
		time.Sleep(d)
		cancel()
	}()
	if resp, err := h.http.Client().Do(req); err == nil {
		resp.Body.Close()
	}
}

// abandonedWriteDeadline is how long the tests below wait for work that a handler
// does after its client has gone.
//
// The number to beat is bcrypt, not the database. A password change compares the old
// password and hashes the new one, so it pays that cost twice, and these two tests
// hash at the real cost on purpose. Measured on one processor under the race detector,
// that handler took 10.7 seconds end to end. A ten-second limit was a coin toss on CI,
// where nothing is faster, and when it lost the flip it blamed the product for
// discarding a password that was in fact stored.
//
// The rest of the suite no longer pays this cost at all — see TestMain — so this
// deadline only has to be generous for the two tests that need it.
const abandonedWriteDeadline = 60 * time.Second

// waitForUser polls until an account exists, or gives up.
//
// Polling rather than sleeping a fixed amount: the handler carries on after the
// client has gone, so there is no response to wait on, and a fixed sleep would
// be either flaky or needlessly slow.
func (h *harness) waitForUser() bool {
	h.t.Helper()
	deadline := time.Now().Add(abandonedWriteDeadline)
	for time.Now().Before(deadline) {
		if n, err := h.store.CountUsers(context.Background()); err == nil && n > 0 {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// A browser that reloads while the password is being hashed ends the request.
// bcrypt is deliberately slow — long enough that an impatient F5 lands in the middle
// of it on any machine — and the handler does its one and only write after that.
// Taking the request's context there threw the work away at the last moment: the
// account was not created, nothing said so, and the next page load showed the setup
// form again as though it had never been filled in.
func TestSetupSurvivesAClientThatGaveUp(t *testing.T) {
	withProductionBcryptCost(t)

	h := newHarness(t)
	h.abandonAfter(50*time.Millisecond, http.MethodPost, "/api/setup",
		map[string]string{"username": testUser, "password": testPassword})

	if !h.waitForUser() {
		t.Fatal("the account was discarded when the client disconnected; a reload during " +
			"hashing must not undo the one thing the user explicitly asked for")
	}

	// Created is not enough: it has to be the account they named, with the
	// password they chose.
	resp := h.login(testUser, testPassword)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login after an abandoned setup = %d, want 200: the account was stored wrong", resp.StatusCode)
	}
}

// accountHash reads the stored password hash of the test account, or fails.
//
// bcrypt salts every hash, so a changed password is always a changed hash — which
// makes "did the write land?" a string comparison instead of a bcrypt run. Doing
// this with bcrypt in the polling loop below would spend one hash per attempt, the
// very cost the deadline is there to accommodate.
func (h *harness) accountHash() string {
	h.t.Helper()
	u, err := h.store.GetUserByName(context.Background(), testUser)
	if err != nil {
		h.t.Fatalf("read the test account: %v", err)
	}
	return u.PasswordHash
}

// The same abandonment as in the setup test, on the other handler that hashes. It
// is the more expensive of the two: it compares the old password and hashes the
// new one, so it pays the bcrypt cost twice.
func TestChangePasswordSurvivesAClientThatGaveUp(t *testing.T) {
	withProductionBcryptCost(t)

	h := newHarness(t)
	h.setup()

	before := h.accountHash()

	h.abandonAfter(50*time.Millisecond, http.MethodPut, "/api/password",
		map[string]string{"current_password": testPassword, "new_password": "a-new-password"})

	// Watching the store rather than the sign-in endpoint, for two reasons: the
	// stored hash is the thing the handler writes, and polling /api/login would
	// spend failed sign-ins against the login limiter, which locks a client out for
	// a minute once five of them have piled up. That is a hazard in its own right,
	// whatever else is going on — a test that hammers the one endpoint with a
	// throttle in front of it cannot tell "the write was lost" from "I have been
	// locked out", and the second would be reported as the first.
	deadline := time.Now().Add(abandonedWriteDeadline)
	for h.accountHash() == before {
		if time.Now().After(deadline) {
			t.Fatal("the new password was discarded when the client disconnected; " +
				"a reload during hashing must not undo the one thing the user explicitly asked for")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Stored is not enough: it has to be the password that was typed, and the next
	// sign-in is what proves it.
	resp := h.login(testUser, "a-new-password")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("login with the new password = %d, want 200: the change did not store what was typed", resp.StatusCode)
	}
}

func TestLoginRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()

	resp := h.login(testUser, "wrong-password")
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password: status = %d, want 401", resp.StatusCode)
	}

	// An unknown name is refused the same way, and with the same status: if the
	// two differed, the login form would be an oracle for which accounts exist.
	unknown := h.login("nobody-here", testPassword)
	unknown.Body.Close()
	if unknown.StatusCode != http.StatusUnauthorized {
		t.Errorf("unknown username: status = %d, want 401", unknown.StatusCode)
	}

	login := h.login(testUser, testPassword)
	var ok struct {
		OK bool `json:"ok"`
	}
	decode(t, login, &ok)
	if login.StatusCode != http.StatusOK || !ok.OK {
		t.Fatalf("login: status = %d, body = %+v", login.StatusCode, ok)
	}

	// The cookie set by the login must be enough to read protected data.
	var cookie *http.Cookie
	for _, c := range login.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("login did not set a session cookie")
	}
	h.cookie = cookie

	// /api/sensors answers with a bare array, which is the contract the
	// dashboard's list view is built on.
	sensor := h.do(http.MethodGet, "/api/sensors", nil, nil)
	var sensors []model.Sensor
	decode(t, sensor, &sensors)
	if sensor.StatusCode != http.StatusOK {
		t.Errorf("GET /api/sensors = %d, want 200", sensor.StatusCode)
	}
	if sensors == nil {
		t.Error("GET /api/sensors returned null instead of an empty list")
	}
}

func TestSessionCookieIsHardened(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// No Secure flag on purpose: homelab installs are frequently reached over
	// plain HTTP on the LAN, and a Secure cookie would simply never be sent.
	if h.cookie.Secure {
		t.Error("the session cookie is marked Secure, which breaks plain-HTTP installs")
	}
	if !h.cookie.HttpOnly {
		t.Error("the session cookie is not HttpOnly")
	}
	if h.cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", h.cookie.SameSite)
	}
}

func TestCrossSiteWriteIsRejected(t *testing.T) {
	h := newHarness(t)
	h.setup()

	body := map[string]any{"name": "web", "type": "http", "target": "https://example.org", "interval": 60, "timeout": 10, "retries": 1}

	resp := h.do(http.MethodPost, "/api/sensors", body, map[string]string{"Origin": "http://evil.example"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site write: status = %d, want 403", resp.StatusCode)
	}

	// The same request from the dashboard's own origin is allowed.
	resp = h.do(http.MethodPost, "/api/sensors", body, map[string]string{"Origin": h.http.URL})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("same-origin write: status = %d, want 201", resp.StatusCode)
	}
}

func TestAPIKeyAuthenticatesWithoutACookie(t *testing.T) {
	h := newHarness(t)
	h.setup()

	// The key is read into memory when the server is built, so store it first
	// and then build a fresh server, which is what a restart would do.
	const key = "pg_test_api_key_1234567890"
	if err := h.store.SetSetting(context.Background(), model.SettingAPIKey, key); err != nil {
		t.Fatalf("store api key: %v", err)
	}

	rebuilt, err := New(context.Background(), Options{
		Config: h.server.cfg, Store: h.store, Scheduler: h.server.sched,
		Dispatcher: h.server.dispatcher, Broker: h.server.broker, Logger: h.server.log,
	})
	if err != nil {
		t.Fatalf("rebuild server: %v", err)
	}
	ts := httptest.NewServer(rebuilt.Handler())
	defer ts.Close()

	get := func(headers map[string]string) int {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/sensors", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	if got := get(map[string]string{"X-API-Key": key}); got != http.StatusOK {
		t.Errorf("X-API-Key: status = %d, want 200", got)
	}
	if got := get(map[string]string{"Authorization": "Bearer " + key}); got != http.StatusOK {
		t.Errorf("Bearer: status = %d, want 200", got)
	}
	if got := get(map[string]string{"X-API-Key": "wrong"}); got != http.StatusUnauthorized {
		t.Errorf("wrong key: status = %d, want 401", got)
	}

	// An API key must not need the CSRF proof a cookie needs, and a mutating
	// request with a foreign Origin must still be accepted when keyed.
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/sensors",
		strings.NewReader(`{"name":"k","type":"http","target":"https://example.org","interval":60,"timeout":10,"retries":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Origin", "http://somewhere.else")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("keyed write with a foreign origin: status = %d, want 201", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// the sensor/channel contract the dashboard depends on
// ---------------------------------------------------------------------------

func TestSensorAndChannelRoundTrip(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	// A channel with a secret.
	resp := h.do(http.MethodPost, "/api/channels", map[string]any{
		"name": "homelab ntfy", "type": "ntfy", "enabled": true,
		"config": map[string]string{"topic": "homelab", "token": "tk_supersecret"},
	}, origin)
	var channel model.Channel
	decode(t, resp, &channel)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create channel: status = %d", resp.StatusCode)
	}
	if channel.ID == 0 {
		t.Fatal("created channel has no id")
	}

	// The read path must not hand the token back.
	resp = h.do(http.MethodGet, "/api/channels", nil, nil)
	var channels []model.Channel
	decode(t, resp, &channels)
	if len(channels) != 1 {
		t.Fatalf("expected one channel, got %d", len(channels))
	}
	if got := channels[0].Config["token"]; got != notify.StoredMarker {
		t.Errorf("token = %q, want the stored marker %q", got, notify.StoredMarker)
	}
	if channels[0].Config["topic"] != "homelab" {
		t.Errorf("topic = %q, want the value to pass through", channels[0].Config["topic"])
	}

	// A sensor attached to that channel.
	resp = h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "Example website", "type": "http", "target": "https://example.org",
		"interval": 60, "timeout": 10, "retries": 1, "enabled": true,
		"group": "Internet", "tags": []string{"web"},
		"channel_ids": []int64{channel.ID},
		"options":     map[string]any{"method": "GET", "headers": map[string]string{"X-Homelab": "yes"}},
	}, origin)
	var sensor model.Sensor
	decode(t, resp, &sensor)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d", resp.StatusCode)
	}

	// The summary endpoint is what the dashboard cards are built from: if it
	// drops channel_ids, the pause/resume button silently detaches every alert.
	//
	// The sensor is looked up by id rather than assumed to be the only one: a
	// fresh installation also carries the example sensors the setup step creates,
	// and this test is about what the summary says about *this* sensor.
	resp = h.do(http.MethodGet, "/api/summary", nil, nil)
	var view struct {
		Sensors []model.StatusView `json:"sensors"`
	}
	decode(t, resp, &view)
	var found *model.StatusView
	for i := range view.Sensors {
		if view.Sensors[i].ID == sensor.ID {
			found = &view.Sensors[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("sensor %d is missing from the summary (%d sensors)", sensor.ID, len(view.Sensors))
	}
	if got := found.Channels; len(got) != 1 || got[0] != channel.ID {
		t.Fatalf("channel_ids in the summary = %v, want [%d]", got, channel.ID)
	}
	if found.Status == "" {
		t.Error("the summary view carries no status")
	}

	// Editing the sensor — which is what the card's buttons do — must not drop
	// the channel links or the options it did not mention.
	resp = h.do(http.MethodPut, "/api/sensors/"+itoa(sensor.ID), map[string]any{
		"name": "Example website", "type": "http", "target": "https://example.org",
		"interval": 60, "timeout": 10, "retries": 1, "enabled": false,
	}, origin)
	decode(t, resp, &sensor)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update sensor: status = %d", resp.StatusCode)
	}

	stored, err := h.store.GetSensor(context.Background(), sensor.ID)
	if err != nil {
		t.Fatalf("read stored sensor: %v", err)
	}
	if len(stored.Channels) != 1 {
		t.Errorf("channel links after an edit = %v, want them preserved", stored.Channels)
	}
	if stored.Enabled {
		t.Error("the pause in the update was not applied")
	}
	if stored.Options.Str("method", "") != "GET" {
		t.Errorf("method option = %q, want the stored value to survive a partial update", stored.Options.Str("method", ""))
	}

	// An explicit empty list is how a client says "no channels".
	resp = h.do(http.MethodPut, "/api/sensors/"+itoa(sensor.ID), map[string]any{
		"name": "Example website", "type": "http", "target": "https://example.org",
		"interval": 60, "timeout": 10, "retries": 1, "enabled": true,
		"channel_ids": []int64{},
	}, origin)
	decode(t, resp, &sensor)
	stored, _ = h.store.GetSensor(context.Background(), sensor.ID)
	if len(stored.Channels) != 0 {
		t.Errorf("channel links after an explicit clear = %v, want none", stored.Channels)
	}
	if !stored.Enabled {
		t.Error("the resume in the update was not applied")
	}

	// Round-tripping the marker must keep the real secret in the database.
	resp = h.do(http.MethodPut, "/api/channels/"+itoa(channel.ID), map[string]any{
		"name": "homelab ntfy", "type": "ntfy", "enabled": true,
		"config": map[string]string{"topic": "homelab", "token": notify.StoredMarker},
	}, origin)
	decode(t, resp, &channel)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("update channel: status = %d", resp.StatusCode)
	}
	storedChannel, err := h.store.GetChannel(context.Background(), channel.ID)
	if err != nil {
		t.Fatalf("read stored channel: %v", err)
	}
	if storedChannel.Config["token"] != "tk_supersecret" {
		t.Errorf("token = %q, want the original secret to be kept", storedChannel.Config["token"])
	}

	// An empty string is an explicit clear, not a no-op.
	resp = h.do(http.MethodPut, "/api/channels/"+itoa(channel.ID), map[string]any{
		"name": "homelab ntfy", "type": "ntfy", "enabled": true,
		"config": map[string]string{"topic": "homelab", "token": ""},
	}, origin)
	decode(t, resp, &channel)
	storedChannel, _ = h.store.GetChannel(context.Background(), channel.ID)
	if storedChannel.Config["token"] != "" {
		t.Errorf("token = %q, want an empty value to clear the secret", storedChannel.Config["token"])
	}
}

// A PUT that mentions one field must change one field. This is the contract the
// dashboard's inline buttons rely on, and the failure mode when it breaks is
// quiet: a sensor whose name was blanked reports "name is required", and one
// whose interval was reset to the default keeps probing, just less often.
func TestPartialUpdateTouchesOnlyWhatItSends(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "NAS", "type": "tcp", "target": "nas.local:445",
		"interval": 300, "timeout": 20, "retries": 4, "group": "Storage",
		"tags": []string{"infra", "lan"}, "reminder_every": 3600,
	}, origin)
	var sensor model.Sensor
	decode(t, resp, &sensor)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d", resp.StatusCode)
	}

	resp = h.do(http.MethodPut, "/api/sensors/"+itoa(sensor.ID),
		map[string]any{"target": "nas2.local:445"}, origin)
	var body struct {
		Error string `json:"error"`
	}
	decode(t, resp, &body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("partial update: status = %d, error = %q", resp.StatusCode, body.Error)
	}

	stored, err := h.store.GetSensor(context.Background(), sensor.ID)
	if err != nil {
		t.Fatalf("read stored sensor: %v", err)
	}
	if stored.Target != "nas2.local:445" {
		t.Errorf("target = %q, want the new value to be applied", stored.Target)
	}
	if stored.Name != "NAS" {
		t.Errorf("name = %q, want it to survive a body that did not mention it", stored.Name)
	}
	if stored.Type != model.SensorTCP {
		t.Errorf("type = %q, want it to survive", stored.Type)
	}
	if stored.Interval != 300 {
		t.Errorf("interval = %d, want 300: a reset to the default would make the sensor probe 5x more often", stored.Interval)
	}
	if stored.Timeout != 20 {
		t.Errorf("timeout = %d, want 20", stored.Timeout)
	}
	if stored.Retries != 4 {
		t.Errorf("retries = %d, want 4", stored.Retries)
	}
	if stored.Group != "Storage" {
		t.Errorf("group = %q, want it to survive", stored.Group)
	}
	if len(stored.Tags) != 2 {
		t.Errorf("tags = %v, want both to survive", stored.Tags)
	}
	if stored.ReminderEvery != 3600 {
		t.Errorf("reminder_every = %d, want 3600", stored.ReminderEvery)
	}

	// An explicit empty value still clears, so "absent" and "empty" stay
	// distinguishable in both directions.
	resp = h.do(http.MethodPut, "/api/sensors/"+itoa(sensor.ID),
		map[string]any{"group": "", "tags": []string{}}, origin)
	decode(t, resp, &sensor)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clearing update: status = %d", resp.StatusCode)
	}
	stored, _ = h.store.GetSensor(context.Background(), sensor.ID)
	if stored.Group != "" || len(stored.Tags) != 0 {
		t.Errorf("group = %q, tags = %v, want an explicit empty value to clear them", stored.Group, stored.Tags)
	}
	if stored.Name != "NAS" || stored.Interval != 300 {
		t.Error("clearing the group disturbed fields the request did not mention")
	}
}

func TestValidationErrorsAreReportedToTheUser(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "bad", "type": "http", "target": "not-a-url", "interval": 60, "timeout": 10, "retries": 1,
	}, origin)
	var errBody struct {
		Error string `json:"error"`
	}
	decode(t, resp, &errBody)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(errBody.Error, "scheme") {
		t.Errorf("error %q does not explain what is wrong", errBody.Error)
	}

	// A duplicate name is a conflict, which the dashboard shows differently.
	body := map[string]any{"name": "dup", "type": "http", "target": "https://example.org", "interval": 60, "timeout": 10, "retries": 1}
	first := h.do(http.MethodPost, "/api/sensors", body, origin)
	first.Body.Close()
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create: status = %d", first.StatusCode)
	}
	second := h.do(http.MethodPost, "/api/sensors", body, origin)
	second.Body.Close()
	if second.StatusCode != http.StatusConflict {
		t.Errorf("duplicate create: status = %d, want 409", second.StatusCode)
	}

	// The validate endpoint reports problems in the body rather than as an
	// error status, because the dashboard shows them inline in the form.
	resp = h.do(http.MethodPost, "/api/sensors/validate", map[string]any{
		"name": "dry run", "type": "http", "target": "not-a-url", "interval": 60, "timeout": 10, "retries": 1,
	}, origin)
	var verdict struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	decode(t, resp, &verdict)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("validate: status = %d, want 200 with a verdict", resp.StatusCode)
	}
	if verdict.Valid || verdict.Error == "" {
		t.Errorf("validate did not report the problem: %+v", verdict)
	}

	// A valid configuration is reported as valid.
	resp = h.do(http.MethodPost, "/api/sensors/validate", map[string]any{
		"name": "dry run", "type": "http", "target": "https://example.org", "interval": 60, "timeout": 10, "retries": 1,
	}, origin)
	decode(t, resp, &verdict)
	if !verdict.Valid {
		t.Errorf("a valid sensor was reported invalid: %s", verdict.Error)
	}

	sensors, err := h.store.ListSensors(context.Background())
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	for _, c := range sensors {
		if c.Name == "dry run" {
			t.Error("the validate endpoint stored a sensor")
		}
	}
}

func TestSettingsRoundTripAndValidation(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	resp := h.do(http.MethodPut, "/api/settings", map[string]any{
		"ui": map[string]string{
			"theme": "dark", "accent": "violet", "density": "compact",
			"layout": "list", "language": "en",
		},
	}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("settings update: status = %d", resp.StatusCode)
	}

	var settings struct {
		UI map[string]string `json:"ui"`
	}
	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	decode(t, resp, &settings)
	if settings.UI["theme"] != "dark" {
		t.Errorf("ui.theme = %v, want dark", settings.UI["theme"])
	}
	if settings.UI["layout"] != "list" {
		t.Errorf("ui.layout = %v, want list", settings.UI["layout"])
	}

	// These values are written straight onto <html> as attributes, so anything
	// outside the allowed set must be refused rather than stored.
	resp = h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"theme": "\"><script>alert(1)</script>"}}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("hostile theme: status = %d, want 400", resp.StatusCode)
	}
	resp = h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"notASetting": "x"}}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown setting: status = %d, want 400", resp.StatusCode)
	}

	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	decode(t, resp, &settings)
	if settings.UI["theme"] != "dark" {
		t.Errorf("ui.theme = %v, want the stored value to survive a rejected update", settings.UI["theme"])
	}

	// The header is the one preference whose value is a list rather than a word,
	// so it takes a different road through the same endpoint: the order has to
	// come back as it went in, because the order is half of what it means.
	resp = h.do(http.MethodPut, "/api/settings", map[string]any{
		"ui": map[string]string{"widgets": "clock,status,mini-map"},
	}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("widget list: status = %d, want 200", resp.StatusCode)
	}
	resp = h.do(http.MethodGet, "/api/settings", nil, nil)
	decode(t, resp, &settings)
	if got := settings.UI["widgets"]; got != "clock,status,mini-map" {
		t.Errorf("ui.widgets = %q, want the order to survive the round trip", got)
	}

	// Everything a widget list can be wrong about: an unknown id, a repeat, an
	// empty entry, a value that is not a list at all. Each would be a tile the
	// browser cannot draw or a setting that disagrees with the picker.
	for _, bad := range []string{"status,,nonsense", "nonsense", "status,status", "status,<script>"} {
		resp = h.do(http.MethodPut, "/api/settings",
			map[string]any{"ui": map[string]string{"widgets": bad}}, origin)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("hostile widget list %q: status = %d, want 400", bad, resp.StatusCode)
		}
	}

	// Turning every widget off is a choice, not a malformed value.
	resp = h.do(http.MethodPut, "/api/settings",
		map[string]any{"ui": map[string]string{"widgets": ""}}, origin)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("empty widget list: status = %d, want 200", resp.StatusCode)
	}
}

func TestHistoryEndpointsAnswerEveryRange(t *testing.T) {
	h := newHarness(t)
	h.setup()
	origin := map[string]string{"Origin": h.http.URL}

	resp := h.do(http.MethodPost, "/api/sensors", map[string]any{
		"name": "web", "type": "http", "target": "https://example.org",
		"interval": 60, "timeout": 10, "retries": 1,
	}, origin)
	var sensor model.Sensor
	decode(t, resp, &sensor)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create sensor: status = %d", resp.StatusCode)
	}

	// Every range the dashboard offers must answer, including "all", which has
	// no fixed duration and used to produce an empty window.
	for _, rng := range []string{"1h", "24h", "7d", "30d", "90d", "all"} {
		resp := h.do(http.MethodGet, "/api/sensors/"+itoa(sensor.ID)+"/series?range="+rng, nil, nil)
		var series struct {
			Points []model.SeriesPoint `json:"points"`
		}
		decode(t, resp, &series)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("range %s: status = %d", rng, resp.StatusCode)
			continue
		}
		if len(series.Points) == 0 {
			t.Errorf("range %s returned no buckets", rng)
		}
	}

	// An unknown range must be a bad request, not a silent default.
	resp = h.do(http.MethodGet, "/api/sensors/"+itoa(sensor.ID)+"/series?range=since-the-dawn-of-time", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown range: status = %d, want 400", resp.StatusCode)
	}

	// History for a sensor that does not exist is a 404.
	resp = h.do(http.MethodGet, "/api/sensors/99999/series", nil, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown sensor: status = %d, want 404", resp.StatusCode)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
