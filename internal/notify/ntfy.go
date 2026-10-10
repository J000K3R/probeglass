package notify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ntfyNotifier publishes to an ntfy topic. ntfy is the path of least
// resistance for phone push notifications in a homelab: no account needed on a
// self-hosted server, and the public instance works for a quick test.
type ntfyNotifier struct {
	server   string
	topic    string
	token    string
	priority string
	client   *httpDoer
}

func newNtfy(cfg map[string]string) (Notifier, error) {
	server := strings.TrimRight(FieldValue(cfg, "server_url"), "/")
	if server == "" {
		server = "https://ntfy.sh"
	}
	if !isHTTPURL(server) {
		return nil, permanentError("server URL must start with http:// or https://")
	}
	topic := FieldValue(cfg, "topic")
	if topic == "" {
		return nil, permanentError("topic is required")
	}
	priority := strings.ToLower(FieldValue(cfg, "priority"))
	switch priority {
	case "", "min", "low", "default", "high", "urgent":
	default:
		return nil, permanentError("priority must be min, low, default, high or urgent")
	}
	return &ntfyNotifier{
		server:   server,
		topic:    topic,
		token:    FieldValue(cfg, "token"),
		priority: priority,
		client:   newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (n *ntfyNotifier) Type() string { return "ntfy" }

// Send implements Notifier.
func (n *ntfyNotifier) Send(ctx context.Context, msg Message) error {
	priority := n.priority
	if priority == "" {
		priority = priorityFor(msg.Event)
	}
	headers := map[string]string{
		"Title":    msg.Title,
		"Priority": priority,
		"Tags":     tagsFor(msg.Event),
	}
	if n.token != "" {
		headers["Authorization"] = "Bearer " + n.token
	}
	endpoint := n.server + "/" + url.PathEscape(n.topic)
	_, err := n.client.do(ctx, http.MethodPost, endpoint, headers, "text/plain; charset=utf-8",
		[]byte(msg.Body))
	return err
}

func priorityFor(ev Event) string {
	switch ev {
	case EventDown:
		return "urgent"
	case EventReminder:
		return "high"
	default:
		return "default"
	}
}

func tagsFor(ev Event) string {
	switch ev {
	case EventDown:
		return "rotating_light"
	case EventUp:
		return "white_check_mark"
	case EventReminder:
		return "warning"
	default:
		return "bell"
	}
}
