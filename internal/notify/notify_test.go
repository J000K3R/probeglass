package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// ---------------------------------------------------------------------------
// channel definitions
// ---------------------------------------------------------------------------

func validConfigs() map[string]map[string]string {
	return map[string]map[string]string{
		"telegram": {"bot_token": "123:ABC", "chat_id": "-100123"},
		"discord":  {"webhook_url": "https://discord.com/api/webhooks/1/abc"},
		"matrix":   {"homeserver_url": "https://matrix.example.org", "access_token": "tok", "room_id": "!r:example.org"},
		"ntfy":     {"topic": "homelab-alerts"},
		"slack":    {"webhook_url": "https://hooks.slack.com/services/T/B/X"},
		"webhook":  {"url": "https://example.org/hook"},
	}
}

func TestValidateChannelAcceptsEveryType(t *testing.T) {
	for typ, cfg := range validConfigs() {
		ch := model.Channel{Name: "alerts " + typ, Type: typ, Config: cfg}
		if err := ValidateChannel(ch); err != nil {
			t.Errorf("%s: a valid channel was rejected: %v", typ, err)
		}
	}
}

func TestValidateChannelRejectsBadConfigs(t *testing.T) {
	cases := []struct {
		name    string
		channel model.Channel
		want    string
	}{
		{"no name", model.Channel{Type: "ntfy", Config: map[string]string{"topic": "t"}}, "name is required"},
		{"unknown type", model.Channel{Name: "x", Type: "smoke-signal"}, "unsupported channel type"},
		{"telegram without a token", model.Channel{Name: "x", Type: "telegram", Config: map[string]string{"chat_id": "1"}}, "Bot token is required"},
		{"telegram without a chat", model.Channel{Name: "x", Type: "telegram", Config: map[string]string{"bot_token": "1:2"}}, "Chat ID is required"},
		{"ntfy without a topic", model.Channel{Name: "x", Type: "ntfy"}, "Topic is required"},
		{"ntfy with a bad priority", model.Channel{Name: "x", Type: "ntfy", Config: map[string]string{"topic": "t", "priority": "louder"}}, "priority"},
		{"webhook without a URL", model.Channel{Name: "x", Type: "webhook"}, "URL is required"},
		{"webhook with a bad URL", model.Channel{Name: "x", Type: "webhook", Config: map[string]string{"url": "example.org/hook"}}, "http://"},
		{"webhook with a bad method", model.Channel{Name: "x", Type: "webhook", Config: map[string]string{"url": "https://example.org", "method": "DELETE"}}, "POST, PUT or PATCH"},
		{"ntfy with a bad server URL", model.Channel{Name: "x", Type: "ntfy", Config: map[string]string{"topic": "t", "server_url": "ntfy.sh"}}, "http://"},
		{"broken title template", model.Channel{Name: "x", Type: "ntfy", Config: map[string]string{"topic": "t", "title_template": "{{.Name"}}, "not a valid template"},
		{"broken body template", model.Channel{Name: "x", Type: "ntfy", Config: map[string]string{"topic": "t", "body_template": "{{.Nope"}}, "not a valid template"},
	}
	for _, tc := range cases {
		err := ValidateChannel(tc.channel)
		if err == nil {
			t.Errorf("%s: accepted an invalid channel", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err, tc.want)
		}
	}
}

func TestEveryChannelTypeIsFullyDescribed(t *testing.T) {
	infos := TypeInfos()
	byType := map[string]TypeInfo{}
	for _, info := range infos {
		if _, dup := byType[info.Type]; dup {
			t.Errorf("type %q is described twice", info.Type)
		}
		byType[info.Type] = info
	}

	for typ := range constructors {
		info, ok := byType[typ]
		if !ok {
			t.Errorf("%q has a constructor but no UI description", typ)
			continue
		}
		if info.Label == "" {
			t.Errorf("%q has no label for the dashboard", typ)
		}
		// Any type with no configurable field could never be set up in the UI.
		if len(info.Fields) == 0 {
			t.Errorf("%q exposes no configuration fields", typ)
		}
		// And a type without a recipe is one whose form only makes sense to
		// somebody who has already used the service once. At the very least it
		// has to say where the credential comes from and where the destination
		// comes from, which is two steps; every type here needs more than that.
		if len(info.Setup) < 2 {
			t.Errorf("%q has %d setup step(s); the dialog shows this list and a form "+
				"nobody can fill in is a form nobody fills in", typ, len(info.Setup))
		}
		for i, step := range info.Setup {
			if strings.TrimSpace(step) == "" {
				t.Errorf("%q has an empty setup step at position %d", typ, i)
			}
		}
		for _, f := range info.Fields {
			if f.Key == "" || f.Label == "" {
				t.Errorf("%q has a field without a key or label: %+v", typ, f)
			}
		}
	}
	for typ := range byType {
		if _, ok := constructors[typ]; !ok {
			t.Errorf("%q is described in the UI but has no implementation", typ)
		}
	}

	// Every type must be able to build a notifier from its own advertised fields
	// filled in with dummy values, or the form the UI renders cannot be saved.
	for typ, info := range byType {
		cfg := map[string]string{}
		for _, f := range info.Fields {
			if !f.Required {
				continue
			}
			cfg[f.Key] = dummyValue(f.Key)
		}
		if _, err := newFromConfig(typ, cfg); err != nil {
			t.Errorf("%q cannot be built from its own required fields: %v", typ, err)
		}
	}
}

func dummyValue(key string) string {
	switch key {
	case "webhook_url", "url", "homeserver_url":
		return "https://example.org/hook"
	case "room_id":
		return "!room:example.org"
	default:
		return "value"
	}
}

func TestIsSecretMarksCredentials(t *testing.T) {
	secrets := []struct{ typ, key string }{
		{"telegram", "bot_token"},
		{"discord", "webhook_url"},
		{"matrix", "access_token"},
		{"slack", "webhook_url"},
		{"ntfy", "token"},
		{"webhook", "hmac_secret"},
	}
	for _, s := range secrets {
		if !IsSecret(s.typ, s.key) {
			t.Errorf("%s.%s should be treated as a secret", s.typ, s.key)
		}
	}
	public := []struct{ typ, key string }{
		{"telegram", "chat_id"},
		{"ntfy", "topic"},
		{"ntfy", "server_url"},
		{"webhook", "url"},
		{"matrix", "room_id"},
	}
	for _, p := range public {
		if IsSecret(p.typ, p.key) {
			t.Errorf("%s.%s should not be treated as a secret", p.typ, p.key)
		}
	}
	// The marker must be recognisable, since the API round-trips it.
	if StoredMarker == "" {
		t.Error("StoredMarker must not be empty")
	}
}

// ---------------------------------------------------------------------------
// message rendering
// ---------------------------------------------------------------------------

func sampleMessage(ev Event) Message {
	started := time.Date(2026, 10, 8, 18, 0, 0, 0, time.UTC)
	return Message{
		Event: ev,
		Sensor: model.Sensor{
			ID: 1, Name: "Gateway ping", Type: model.SensorPing,
			Target: "192.168.1.1", Group: "Network", Tags: []string{"latency"},
		},
		Result: model.Result{Success: ev == EventUp, LatencyMS: 12.5, Message: "100% packet loss"},
		Incident: &model.Incident{
			ID: 7, SensorID: 1, StartedAt: started, Cause: "100% packet loss",
		},
		URL:  "http://homelab:8080/#/sensor/1",
		Time: started.Add(9 * time.Minute),
	}
}

func TestRenderDefaultTemplates(t *testing.T) {
	cases := []struct {
		event     Event
		wantTitle string
		wantBody  []string
	}{
		{EventDown, "DOWN: Gateway ping", []string{"Gateway ping is DOWN", "192.168.1.1", "Group: Network", "100% packet loss", "Dashboard: http://homelab:8080/#/sensor/1"}},
		{EventReminder, "STILL DOWN: Gateway ping", []string{"still DOWN", "Down since"}},
		{EventUp, "RECOVERED: Gateway ping", []string{"back UP", "Was down for: 9m 0s", "Latency: 12 ms"}},
		{EventTest, "ProbeGlass test alert", []string{"test message", "configured correctly"}},
	}
	for _, tc := range cases {
		msg := sampleMessage(tc.event)
		// Rendering against the message's own timestamp makes the downtime
		// duration deterministic: the incident started nine minutes earlier.
		title, body := render(msg, nil, msg.Time)
		if !strings.Contains(title, tc.wantTitle) {
			t.Errorf("%s: title %q does not contain %q", tc.event, title, tc.wantTitle)
		}
		for _, want := range tc.wantBody {
			if !strings.Contains(body, want) {
				t.Errorf("%s: body does not contain %q:\n%s", tc.event, want, body)
			}
		}
	}
}

func TestRenderWithoutIncidentStillNamesTheFailure(t *testing.T) {
	msg := sampleMessage(EventDown)
	msg.Incident = nil
	_, body := render(msg, nil, time.Now())
	if !strings.Contains(body, "Gateway ping is DOWN") {
		t.Errorf("body lost the failure summary:\n%s", body)
	}
	if strings.Contains(body, "Was down for") {
		t.Errorf("a first failure must not claim a downtime duration:\n%s", body)
	}
}

func TestRenderHonoursCustomTemplates(t *testing.T) {
	cfg := map[string]string{
		"title_template": `[{{.Group}}] {{.Status}}: {{.Name}}`,
		"body_template":  `{{.Target}} -> {{.Message}} ({{.LatencyMS}}ms)`,
	}
	title, body := render(sampleMessage(EventDown), cfg, time.Now())
	if title != "[Network] down: Gateway ping" {
		t.Errorf("title = %q", title)
	}
	if body != "192.168.1.1 -> 100% packet loss (12.5ms)" {
		t.Errorf("body = %q", body)
	}
}

func TestBrokenTemplateFallsBackInsteadOfLosingTheAlert(t *testing.T) {
	// A template that parses but cannot execute against the data.
	cfg := map[string]string{"title_template": `{{call .Name}}`}
	title, _ := render(sampleMessage(EventDown), cfg, time.Now())
	if !strings.Contains(title, "DOWN: Gateway ping") {
		t.Errorf("title = %q, want the default template", title)
	}

	// A template that does not even parse.
	cfg = map[string]string{"body_template": `{{.Name`}
	_, body := render(sampleMessage(EventDown), cfg, time.Now())
	if !strings.Contains(body, "Gateway ping is DOWN") {
		t.Errorf("body lost the alert:\n%s", body)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:             "45s",
		9 * time.Minute:              "9m 0s",
		3*time.Hour + 12*time.Minute: "3h 12m",
		50 * time.Hour:               "2d 2h",
		-5 * time.Second:             "0s",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// delivery
// ---------------------------------------------------------------------------

func TestWebhookSendsSignedJSON(t *testing.T) {
	type received struct {
		body   []byte
		header http.Header
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{body: body, header: r.Header.Clone()}
	}))
	defer srv.Close()

	const secret = "homelab-hmac-secret"
	notifier, err := newWebhook(map[string]string{"url": srv.URL, "hmac_secret": secret})
	if err != nil {
		t.Fatalf("build notifier: %v", err)
	}

	msg := sampleMessage(EventDown)
	msg.Title = "🔴 DOWN: Gateway ping"
	msg.Body = "Gateway ping is DOWN."
	if err := notifier.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case r := <-got:
		if ct := r.header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if ev := r.header.Get("X-ProbeGlass-Event"); ev != "down" {
			t.Errorf("X-ProbeGlass-Event = %q, want down", ev)
		}

		// The signature must cover the exact bytes that were sent.
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(r.body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if sig := r.header.Get("X-ProbeGlass-Signature"); sig != want {
			t.Errorf("signature = %q, want %q", sig, want)
		}

		var payload webhookPayload
		if err := json.Unmarshal(r.body, &payload); err != nil {
			t.Fatalf("payload is not valid JSON: %v", err)
		}
		if payload.Event != "down" {
			t.Errorf("event = %q", payload.Event)
		}
		if payload.Sensor.Name != "Gateway ping" || payload.Sensor.Target != "192.168.1.1" {
			t.Errorf("sensor = %+v", payload.Sensor)
		}
		if payload.Result.Success {
			t.Error("result.success = true for a down event")
		}
		if payload.Incident == nil || payload.Incident.ID != 7 {
			t.Errorf("incident = %+v, want the open incident", payload.Incident)
		}
		if payload.URL == "" {
			t.Error("payload has no dashboard URL")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook was never called")
	}
}

func TestWebhookWithoutSecretSendsNoSignature(t *testing.T) {
	header := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header <- r.Header.Clone()
	}))
	defer srv.Close()

	notifier, err := newWebhook(map[string]string{"url": srv.URL})
	if err != nil {
		t.Fatalf("build notifier: %v", err)
	}
	if err := notifier.Send(context.Background(), sampleMessage(EventUp)); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case h := <-header:
		if sig := h.Get("X-ProbeGlass-Signature"); sig != "" {
			t.Errorf("unsigned webhook carried a signature: %q", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the webhook was never called")
	}
}

func TestNtfyPublishesTitleAndPriority(t *testing.T) {
	type received struct {
		body   string
		header http.Header
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got <- received{body: string(body), header: r.Header.Clone()}
	}))
	defer srv.Close()

	notifier, err := newNtfy(map[string]string{
		"server_url": srv.URL, "topic": "homelab alerts", "token": "tk_123",
	})
	if err != nil {
		t.Fatalf("build notifier: %v", err)
	}
	msg := sampleMessage(EventDown)
	msg.Title = "Gateway ping is down"
	msg.Body = "no replies from 192.168.1.1"
	if err := notifier.Send(context.Background(), msg); err != nil {
		t.Fatalf("send: %v", err)
	}

	select {
	case r := <-got:
		if r.header.Get("Title") != "Gateway ping is down" {
			t.Errorf("Title = %q", r.header.Get("Title"))
		}
		if r.header.Get("Priority") != "urgent" {
			t.Errorf("Priority = %q, want urgent for a down event", r.header.Get("Priority"))
		}
		if r.header.Get("Authorization") != "Bearer tk_123" {
			t.Errorf("Authorization = %q", r.header.Get("Authorization"))
		}
		if r.body != "no replies from 192.168.1.1" {
			t.Errorf("body = %q", r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ntfy was never called")
	}
}

// TestTelegramNeverLeaksTheTokenInAnError is the security assertion for the one
// notifier that carries its credential in the URL: whatever goes wrong, the
// error text must not contain the token, because errors are logged and stored.
func TestTelegramNeverLeaksTheTokenInAnError(t *testing.T) {
	const token = "123456:AAHsupersecrettoken"
	notifier, err := newTelegram(map[string]string{"bot_token": token, "chat_id": "-100"})
	if err != nil {
		t.Fatalf("build notifier: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = notifier.Send(ctx, sampleMessage(EventDown))
	if err == nil {
		t.Fatal("sending on a cancelled context succeeded")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("the error text leaks the bot token: %q", err)
	}
	if strings.Contains(err.Error(), "api.telegram.org/bot") {
		t.Fatalf("the error text leaks the request URL: %q", err)
	}
}

// ---------------------------------------------------------------------------
// failure classification
// ---------------------------------------------------------------------------

func TestFailureClassification(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		retryable bool
	}{
		{"429 rate limited", statusError(http.StatusTooManyRequests, "slow down"), true},
		{"500 server error", statusError(http.StatusInternalServerError, "boom"), true},
		{"503 unavailable", statusError(http.StatusServiceUnavailable, "later"), true},
		{"401 bad token", statusError(http.StatusUnauthorized, "unauthorized"), false}, //nolint:misspell // a server's own wording, quoted
		{"404 wrong URL", statusError(http.StatusNotFound, "not found"), false},
		{"bad config", permanentError("topic is required"), false},
		{"network hiccup", retryableError("connection timed out"), true},
	}
	for _, tc := range cases {
		if got := isRetryable(tc.err); got != tc.retryable {
			t.Errorf("%s: isRetryable = %v, want %v", tc.name, got, tc.retryable)
		}
	}
	if isRetryable(nil) {
		t.Error("a nil error must not be retryable")
	}

	// The status must survive into the message so the channel card can show it.
	if msg := statusError(500, "internal error").Error(); !strings.Contains(msg, "500") {
		t.Errorf("status error message %q does not carry the status", msg)
	}
	// A long body is truncated rather than dumped into the log.
	long := statusError(500, strings.Repeat("x", 5000)).Error()
	if len(long) > 300 {
		t.Errorf("status error kept %d bytes of the response body", len(long))
	}
}
