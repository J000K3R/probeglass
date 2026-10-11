package notify

import "strings"

// This file is the single place that describes what each alert channel needs.
// The dashboard builds its forms from these definitions, so a field added here
// shows up in the UI without any frontend change.
//
// Every field carries three strings and they are not interchangeable: Label names
// it, Help explains what belongs in it, and Example shows one. The dashboard
// puts Help and Example behind the "?" beside the input, because a form that
// explains itself in place is a form nobody reads twice.

// Shared field definitions, reused across channel types.
var (
	fTitleTemplate = FieldSpec{
		Key:         TemplateTitleKey,
		Label:       "Custom title template",
		Placeholder: "leave empty for the default",
		Help:        "Go template. Available: {{.Name}} {{.Type}} {{.Target}} {{.Group}} {{.Status}} {{.Message}} {{.LatencyMS}} {{.StatusCode}} {{.DownFor}} {{.DownSince}} {{.Time}} {{.URL}}",
		Example:     "⚠️ {{.Name}} is {{.Status}}",
	}
	fBodyTemplate = FieldSpec{
		Key:         TemplateBodyKey,
		Label:       "Custom body template",
		Placeholder: "leave empty for the default",
		Help:        "Same variables as the title template.",
		Example:     "{{.Name}} ({{.Target}}) is {{.Status}} — {{.Message}}",
	}
)

// typeInfos describes every supported channel type.
var typeInfos = map[string]TypeInfo{
	"telegram": {
		Type:  "telegram",
		Label: "Telegram",
		Setup: []string{
			"Open @BotFather in Telegram and send it /newbot. It asks for a name and answers with a token.",
			"Paste that token into Bot token above.",
			"Add the bot to the group you want alerts in, or open a direct chat with it, and send it any message.",
			"Open https://api.telegram.org/bot<token>/getUpdates in a browser — with your own token in place of <token> — and read the number in chat.id. A group's id starts with a minus.",
			"That address contains the token. Do not paste it into a chat, an issue or a ticket; anyone who has it controls the bot.",
		},
		Fields: []FieldSpec{
			{Key: "bot_token", Label: "Bot token", Required: true, Secret: true,
				Placeholder: "123456:ABC-DEF...",
				Help:        "Create a bot with @BotFather and paste the token it gives you.",
				Example:     "123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"},
			{Key: "chat_id", Label: "Chat ID", Required: true,
				Placeholder: "-1001234567890",
				Help:        "Send a message to the bot, then open https://api.telegram.org/bot<token>/getUpdates and read chat.id.",
				Example:     "-1001987654321"},
			{Key: "thread_id", Label: "Topic / thread ID",
				Help:    "Only for forum groups. Leave empty otherwise.",
				Example: "42"},
			{Key: "silent", Label: "Send silently",
				Help:    "Deliver without a notification sound. Use \"true\" to enable.",
				Example: "true"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
	"discord": {
		Type:  "discord",
		Label: "Discord",
		Setup: []string{
			"In Discord, open the settings of the channel that should carry the alerts.",
			"Go to Integrations → Webhooks and press New Webhook.",
			"Give it a name, pick the channel, and press Copy Webhook URL.",
			"Paste that URL into Webhook URL above. Anyone who has it can post to the channel, so it is stored as a secret here.",
		},
		Fields: []FieldSpec{
			{Key: "webhook_url", Label: "Webhook URL", Required: true, Secret: true,
				Placeholder: "https://discord.com/api/webhooks/...",
				Help:        "In Discord: Channel settings → Integrations → Webhooks → New webhook → Copy URL.",
				Example:     "https://discord.com/api/webhooks/1234567890123456789/AbCdEfGhIjKlMnOpQrStUvWxYz0123456789AbCdEfGhIj"},
			{Key: "username", Label: "Override username",
				Placeholder: "ProbeGlass",
				Help:        "The name the webhook posts under, instead of the one configured in Discord.",
				Example:     "Homelab watchdog"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
	"matrix": {
		Type:  "matrix",
		Label: "Matrix",
		Setup: []string{
			"Enter the base address of your homeserver, without the /_matrix path: https://matrix.example.org, not .../_matrix/client.",
			"In Element, open Settings → Help & About, expand Advanced, and reveal the Access token. Paste it into Access token above.",
			"Open the room's settings, go to Advanced, and read the Internal room ID. It starts with ! and carries a colon, like !kRcQvY:matrix.org.",
			"The account the token belongs to has to be in that room already — a room it has not joined cannot be posted to.",
			"The access token is the whole account, not just this channel. Treat it like a password.",
		},
		Fields: []FieldSpec{
			{Key: "homeserver_url", Label: "Homeserver URL", Required: true,
				Placeholder: "https://matrix.example.org",
				Help:        "Base URL of your homeserver, without /_matrix.",
				Example:     "https://matrix.org"},
			{Key: "access_token", Label: "Access token", Required: true, Secret: true,
				Help:    "Element: Settings → Help & About → Access token.",
				Example: "syt_youruser_AbCdEfGhIjKlMnOpQrStUvWx"},
			{Key: "room_id", Label: "Room ID", Required: true,
				Placeholder: "!abcdefg:homeserver.example.org",
				Help:        "Room settings → Advanced → Internal room ID.",
				Example:     "!kRcQvYtLmNpZxW:matrix.org"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
	"ntfy": {
		Type:  "ntfy",
		Label: "ntfy",
		Setup: []string{
			"Pick a topic name and subscribe to it in the ntfy app, or open your server's page with that topic in the address, so you can see what arrives.",
			"Enter it in Topic above. Leave the server URL as it is to use the public https://ntfy.sh.",
			"On the public server anyone who guesses the topic can read your alerts — a phone going down at 3 a.m. and a sensor called \"Garage\". Choose a long, unguessable name, or run your own server and enter its address.",
			"Fill in an access token only if that server asks for one.",
		},
		Fields: []FieldSpec{
			{Key: "server_url", Label: "Server URL",
				Placeholder: "https://ntfy.sh",
				Help:        "Your own ntfy server, or the public one. Leave the default for ntfy.sh.",
				Example:     "https://ntfy.example.org"},
			{Key: "topic", Label: "Topic", Required: true,
				Placeholder: "my-homelab-alerts",
				Help:        "Pick an unguessable topic name when using the public ntfy.sh server.",
				Example:     "homelab-alerts-7f3a91"},
			{Key: "token", Label: "Access token", Secret: true,
				Help:    "Only needed on a protected server (Bearer token).",
				Example: "tk_agqdpokjmyi3irb0p1nvccz1m8z2"},
			{Key: "priority", Label: "Priority",
				Placeholder: "high",
				Help:        "min, low, default, high or urgent.",
				Example:     "urgent"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
	"slack": {
		Type:  "slack",
		Label: "Slack",
		Setup: []string{
			"Open https://api.slack.com/apps, press Create New App → From scratch, and pick the workspace.",
			"In the app's settings, open Incoming Webhooks and switch Activate Incoming Webhooks on.",
			"Press Add New Webhook to Workspace, pick the channel the alerts should go to, and allow it.",
			"Copy the webhook URL Slack shows and paste it here. The receiving channel is baked into that URL; fill in Channel override only to send somewhere else.",
		},
		Fields: []FieldSpec{
			{Key: "webhook_url", Label: "Webhook URL", Required: true, Secret: true,
				Placeholder: "https://hooks.slack.com/services/...",
				Help:        "Create an Incoming Webhook app in your Slack workspace.",
				Example:     "https://hooks.slack.com/services/T00000000/B00000000/XXXXXXXXXXXXXXXXXXXXXXXX"},
			{Key: "channel", Label: "Channel override",
				Placeholder: "#alerts",
				Help:        "Post to this channel instead of the one the webhook was created for.",
				Example:     "#homelab"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
	"webhook": {
		Type:  "webhook",
		Label: "Generic webhook",
		Setup: []string{
			"Enter the address on your own side that should receive the alerts and make it accept a POST.",
			"Each request carries Content-Type: application/json and the header X-ProbeGlass-Event with the event kind in it — down, up or test.",
			"With an HMAC secret set above, the body is signed with HMAC-SHA256 and the signature arrives in X-ProbeGlass-Signature, prefixed with sha256=. Compare it before you act on the body.",
			"The body carries the event, the sensor and the last result, in the same shapes /api/sensors and /api/incidents use; when the alert is about an incident, that is in it too.",
		},
		Fields: []FieldSpec{
			{Key: "url", Label: "URL", Required: true,
				Placeholder: "https://example.org/hooks/probeglass",
				Help:        "Receives a JSON POST with the event, sensor and result.",
				Example:     "https://homeassistant.local:8123/api/webhook/probeglass"},
			{Key: "method", Label: "HTTP method",
				Placeholder: "POST",
				Help:        "POST unless the receiving endpoint expects something else.",
				Example:     "PUT"},
			{Key: "hmac_secret", Label: "HMAC secret", Secret: true,
				Help:    "When set, the body is signed with HMAC-SHA256 and the signature is sent in X-ProbeGlass-Signature.",
				Example: "a long random string, e.g. from `openssl rand -hex 32`"},
			fTitleTemplate,
			fBodyTemplate,
		},
	},
}

// FieldValue reads a trimmed config value.
func FieldValue(cfg map[string]string, key string) string {
	return strings.TrimSpace(cfg[key])
}
