package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// matrixNotifier sends messages over the Matrix client-server API.
type matrixNotifier struct {
	homeserver string
	token      string
	roomID     string
	client     *httpDoer
}

func newMatrix(cfg map[string]string) (Notifier, error) {
	homeserver := strings.TrimRight(FieldValue(cfg, "homeserver_url"), "/")
	if homeserver == "" {
		return nil, permanentError("homeserver URL is required")
	}
	if !isHTTPURL(homeserver) {
		return nil, permanentError("homeserver URL must start with http:// or https://")
	}
	token := FieldValue(cfg, "access_token")
	if token == "" {
		return nil, permanentError("access token is required")
	}
	roomID := FieldValue(cfg, "room_id")
	if roomID == "" {
		return nil, permanentError("room id is required")
	}
	if !strings.HasPrefix(roomID, "!") {
		return nil, permanentError("room id must be an internal id starting with \"!\", not an alias")
	}
	return &matrixNotifier{
		homeserver: homeserver,
		token:      token,
		roomID:     roomID,
		client:     newHTTPDoer(20 * time.Second),
	}, nil
}

// Type implements Notifier.
func (m *matrixNotifier) Type() string { return "matrix" }

// Send implements Notifier.
func (m *matrixNotifier) Send(ctx context.Context, msg Message) error {
	txn, err := randomHex(8)
	if err != nil {
		return retryableError("generate transaction id: %v", err)
	}
	endpoint := fmt.Sprintf("%s/_matrix/client/v3/rooms/%s/send/m.room.message/%s",
		m.homeserver, url.PathEscape(m.roomID), txn)

	plain := joinTitleBody(msg.Title, msg.Body)
	payload := map[string]any{
		"msgtype": "m.text",
		"body":    plain,
		"format":  "org.matrix.custom.html",
		"formatted_body": fmt.Sprintf("<strong>%s</strong><br><pre>%s</pre>",
			htmlEscape(msg.Title), htmlEscape(msg.Body)),
	}
	headers := map[string]string{"Authorization": "Bearer " + m.token}
	_, err = m.client.doJSON(ctx, http.MethodPut, endpoint, headers, payload)
	return err
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func isHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}
