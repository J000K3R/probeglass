package notify

import (
	"context"
	"net/http"
	"time"
)

// Alert colours, kept in sync with the dashboard palette so an alert in a chat
// client and the same sensor on the board look alike.
const (
	colourDown  = 0xE5484D
	colourUp    = 0x30A46C
	colourInfo  = 0x3E63DD
	colourWarn  = 0xE5A03D
	colourMuted = 0x8B8D98
)

// colourFor picks the embed colour for an event.
func colourFor(ev Event) int {
	switch ev {
	case EventUp:
		return colourUp
	case EventReminder:
		return colourWarn
	case EventTest:
		return colourInfo
	default:
		return colourDown
	}
}

// discordNotifier posts rich embeds through a Discord webhook.
type discordNotifier struct {
	webhookURL string
	username   string
	client     *httpDoer
}

func newDiscord(cfg map[string]string) (Notifier, error) {
	url := FieldValue(cfg, "webhook_url")
	if url == "" {
		return nil, permanentError("webhook URL is required")
	}
	if !isHTTPURL(url) {
		return nil, permanentError("webhook URL must start with http:// or https://")
	}
	return &discordNotifier{
		webhookURL: url,
		username:   FieldValue(cfg, "username"),
		client:     newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (d *discordNotifier) Type() string { return "discord" }

// Send implements Notifier.
func (d *discordNotifier) Send(ctx context.Context, msg Message) error {
	username := d.username
	if username == "" {
		username = "ProbeGlass"
	}
	payload := map[string]any{
		"username": username,
		"embeds": []map[string]any{{
			"title":       truncateRunes(msg.Title, 240),
			"description": truncateRunes(msg.Body, 3800),
			"color":       colourFor(msg.Event), //nolint:misspell // Discord's field name, not prose
			"timestamp":   msg.Time.Format(time.RFC3339),
			"footer":      map[string]any{"text": "ProbeGlass · " + string(msg.Event)},
		}},
	}
	_, err := d.client.doJSON(ctx, http.MethodPost, d.webhookURL, nil, payload)
	return err
}
