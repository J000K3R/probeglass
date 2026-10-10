package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// webhookNotifier posts a structured JSON payload to an arbitrary endpoint.
// It is the escape hatch for services Weatherglass does not speak natively:
// Home Assistant, Node-RED, n8n, a shell script behind a tiny HTTP server, ...
type webhookNotifier struct {
	url        string
	method     string
	hmacSecret string
	client     *httpDoer
}

func newWebhook(cfg map[string]string) (Notifier, error) {
	url := FieldValue(cfg, "url")
	if url == "" {
		return nil, permanentError("URL is required")
	}
	if !isHTTPURL(url) {
		return nil, permanentError("URL must start with http:// or https://")
	}
	method := strings.ToUpper(FieldValue(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return nil, permanentError("method must be POST, PUT or PATCH")
	}
	return &webhookNotifier{
		url:        url,
		method:     method,
		hmacSecret: FieldValue(cfg, "hmac_secret"),
		client:     newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (w *webhookNotifier) Type() string { return "webhook" }

// webhookPayload is the documented JSON contract. Its shape is part of the
// public API: keep additions backwards compatible.
type webhookPayload struct {
	Event     string           `json:"event"`
	Title     string           `json:"title"`
	Message   string           `json:"message"`
	URL       string           `json:"url,omitempty"`
	Timestamp time.Time        `json:"timestamp"`
	Sensor    webhookSensor    `json:"sensor"`
	Result    webhookResult    `json:"result"`
	Incident  *webhookIncident `json:"incident,omitempty"`
}

type webhookSensor struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Type   string   `json:"type"`
	Target string   `json:"target"`
	Group  string   `json:"group,omitempty"`
	Tags   []string `json:"tags,omitempty"`
}

type webhookResult struct {
	Success    bool      `json:"success"`
	LatencyMS  float64   `json:"latency_ms"`
	StatusCode int       `json:"status_code,omitempty"`
	Message    string    `json:"message,omitempty"`
	TS         time.Time `json:"ts"`
}

type webhookIncident struct {
	ID         int64      `json:"id"`
	StartedAt  time.Time  `json:"started_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Cause      string     `json:"cause,omitempty"`
}

// Send implements Notifier.
func (w *webhookNotifier) Send(ctx context.Context, msg Message) error {
	payload := webhookPayload{
		Event:     string(msg.Event),
		Title:     msg.Title,
		Message:   msg.Body,
		URL:       msg.URL,
		Timestamp: msg.Time,
		Sensor: webhookSensor{
			ID:     msg.Sensor.ID,
			Name:   msg.Sensor.Name,
			Type:   string(msg.Sensor.Type),
			Target: msg.Sensor.Target,
			Group:  msg.Sensor.Group,
			Tags:   msg.Sensor.Tags,
		},
		Result: webhookResult{
			Success:    msg.Result.Success,
			LatencyMS:  msg.Result.LatencyMS,
			StatusCode: msg.Result.StatusCode,
			Message:    msg.Result.Message,
			TS:         msg.Result.TS,
		},
	}
	if msg.Incident != nil {
		inc := msg.Incident
		payload.Incident = &webhookIncident{
			ID:         inc.ID,
			StartedAt:  inc.StartedAt,
			ResolvedAt: inc.ResolvedAt,
			Cause:      inc.Cause,
		}
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return permanentError("encode payload: %v", err)
	}
	headers := map[string]string{
		"X-Weatherglass-Event": string(msg.Event),
	}
	if w.hmacSecret != "" {
		mac := hmac.New(sha256.New, []byte(w.hmacSecret))
		mac.Write(body)
		headers["X-Weatherglass-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	_, err = w.client.do(ctx, w.method, w.url, headers, "application/json", body)
	return err
}

// Compile-time check that every notifier satisfies the interface.
var (
	_ Notifier = (*telegramNotifier)(nil)
	_ Notifier = (*discordNotifier)(nil)
	_ Notifier = (*matrixNotifier)(nil)
	_ Notifier = (*ntfyNotifier)(nil)
	_ Notifier = (*slackNotifier)(nil)
	_ Notifier = (*webhookNotifier)(nil)
)
