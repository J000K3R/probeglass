package notify

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// gotifyNotifier posts to a Gotify server.
//
// Gotify is the self-hosted push server a homelab often already runs: one binary
// beside the dashboard, an app token per application, and a phone app that
// subscribes to it. Same shape as ntfy and for the same reason — there is no
// account anywhere and nothing leaves the LAN unless the operator points it at
// something public.
type gotifyNotifier struct {
	server string
	token  string
	// priority is 0 to 10 on Gotify's own scale. Zero means "not chosen", which is
	// the reason it is an int rather than a string: the event's own priority is
	// what an unset field falls back to, and 0 is a priority somebody may really
	// want (a recovery that should not make a sound).
	priority int
	client   *httpDoer
}

func newGotify(cfg map[string]string) (Notifier, error) {
	server := strings.TrimRight(FieldValue(cfg, "server_url"), "/")
	if server == "" {
		return nil, permanentError("server URL is required")
	}
	if !isHTTPURL(server) {
		return nil, permanentError("server URL must start with http:// or https://")
	}
	token := FieldValue(cfg, "token")
	if token == "" {
		return nil, permanentError("app token is required")
	}
	priority := 0
	if raw := FieldValue(cfg, "priority"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 10 {
			return nil, permanentError("priority must be a number from 0 to 10")
		}
		priority = n
	}
	return &gotifyNotifier{
		server:   server,
		token:    token,
		priority: priority,
		client:   newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (g *gotifyNotifier) Type() string { return "gotify" }

// Send implements Notifier.
//
// The token travels in the X-Gotify-Key header rather than in the query string
// Gotify also accepts. It is the credential for the application, and a URL is the
// part of a request that ends up in proxy logs and in whatever somebody copies
// while debugging.
func (g *gotifyNotifier) Send(ctx context.Context, msg Message) error {
	_, err := g.client.doJSON(ctx, http.MethodPost, g.server+"/message",
		map[string]string{"X-Gotify-Key": g.token},
		map[string]any{
			"title":    msg.Title,
			"message":  msg.Body,
			"priority": g.priorityFor(msg.Event),
		})
	return err
}

// priorityFor is this channel's priority for one event, or the configured one when
// somebody set it.
func (g *gotifyNotifier) priorityFor(ev Event) int {
	if g.priority != 0 {
		return g.priority
	}
	switch ev {
	case EventDown:
		return 8
	case EventReminder:
		return 6
	case EventUp:
		return 4
	default:
		return 5
	}
}
