package notify

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// slackNotifier posts to a Slack incoming webhook.
type slackNotifier struct {
	webhookURL string
	channel    string
	client     *httpDoer
}

func newSlack(cfg map[string]string) (Notifier, error) {
	url := FieldValue(cfg, "webhook_url")
	if url == "" {
		return nil, permanentError("webhook URL is required")
	}
	if !isHTTPURL(url) {
		return nil, permanentError("webhook URL must start with http:// or https://")
	}
	channel := FieldValue(cfg, "channel")
	if channel != "" && !strings.HasPrefix(channel, "#") && !strings.HasPrefix(channel, "@") {
		channel = "#" + channel
	}
	return &slackNotifier{
		webhookURL: url,
		channel:    channel,
		client:     newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (s *slackNotifier) Type() string { return "slack" }

// Send implements Notifier.
func (s *slackNotifier) Send(ctx context.Context, msg Message) error {
	text := "*" + msg.Title + "*\n" + msg.Body
	if msg.URL != "" {
		text += "\n<" + msg.URL + "|Open in ProbeGlass>"
	}
	payload := map[string]any{
		"text":         truncateRunes(text, 3500),
		"unfurl_links": false,
	}
	if s.channel != "" {
		payload["channel"] = s.channel
	}
	_, err := s.client.doJSON(ctx, http.MethodPost, s.webhookURL, nil, payload)
	return err
}
