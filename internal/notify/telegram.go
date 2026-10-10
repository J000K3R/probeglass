package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// telegramNotifier posts messages through the Telegram Bot API.
type telegramNotifier struct {
	token    string
	chatID   string
	threadID int
	silent   bool
	client   *httpDoer
}

func newTelegram(cfg map[string]string) (Notifier, error) {
	token := FieldValue(cfg, "bot_token")
	if token == "" {
		return nil, permanentError("bot token is required")
	}
	chatID := FieldValue(cfg, "chat_id")
	if chatID == "" {
		return nil, permanentError("chat id is required")
	}
	t := &telegramNotifier{
		token:  token,
		chatID: chatID,
		silent: boolField(cfg, "silent"),
		client: newHTTPDoer(20 * time.Second),
	}
	if raw := FieldValue(cfg, "thread_id"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, permanentError("thread id must be a number, got %q", raw)
		}
		t.threadID = n
	}
	return t, nil
}

// Type implements Notifier.
func (t *telegramNotifier) Type() string { return "telegram" }

// Send implements Notifier.
func (t *telegramNotifier) Send(ctx context.Context, msg Message) error {
	text := fmt.Sprintf("<b>%s</b>\n%s", htmlEscape(msg.Title), htmlEscape(msg.Body))
	payload := map[string]any{
		"chat_id":              t.chatID,
		"text":                 truncateRunes(text, 4000),
		"parse_mode":           "HTML",
		"disable_notification": t.silent,
		"link_preview_options": map[string]any{"is_disabled": true},
	}
	if t.threadID != 0 {
		payload["message_thread_id"] = t.threadID
	}

	// The token is part of the URL, so the request must never be logged.
	endpoint := "https://api.telegram.org/bot" + t.token + "/sendMessage"
	body, err := t.client.doJSON(ctx, http.MethodPost, endpoint, nil, payload)
	if err != nil {
		return err
	}

	// Telegram answers 200 with ok=false for some rejections.
	var resp struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &resp); err == nil && !resp.OK && resp.Description != "" {
		return permanentError("telegram rejected the message: %s", resp.Description)
	}
	return nil
}
