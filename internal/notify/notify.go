// Package notify delivers alert messages to external services.
//
// Every channel type implements Notifier and registers itself in the
// constructors map. Types also publish a TypeInfo describing the fields their
// configuration needs, which the dashboard renders into a form. Adding a new
// service therefore means writing one file: the UI picks it up automatically.
package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Event names the situation an alert is about.
type Event string

const (
	EventDown       Event = "down"
	EventUp         Event = "up"
	EventReminder   Event = "reminder"
	EventEscalation Event = "escalation"
	EventTest       Event = "test"
)

// Message is a rendered alert, ready to be sent.
type Message struct {
	Event    Event
	Sensor   model.Sensor
	Result   model.Result
	Incident *model.Incident
	Title    string
	Body     string
	URL      string
	Time     time.Time
}

// Notifier sends one message to one destination.
type Notifier interface {
	// Type reports the channel type this notifier implements.
	Type() string
	// Send delivers the message. It must respect ctx.
	Send(ctx context.Context, msg Message) error
}

// Constructor builds a notifier from a stored configuration.
type Constructor func(cfg map[string]string) (Notifier, error)

// constructors is the registry of channel types.
var constructors = map[string]Constructor{
	"telegram": func(c map[string]string) (Notifier, error) { return newTelegram(c) },
	"discord":  func(c map[string]string) (Notifier, error) { return newDiscord(c) },
	"matrix":   func(c map[string]string) (Notifier, error) { return newMatrix(c) },
	"ntfy":     func(c map[string]string) (Notifier, error) { return newNtfy(c) },
	"gotify":   func(c map[string]string) (Notifier, error) { return newGotify(c) },
	"slack":    func(c map[string]string) (Notifier, error) { return newSlack(c) },
	"webhook":  func(c map[string]string) (Notifier, error) { return newWebhook(c) },
}

// FieldSpec describes one configuration field of a channel type.
type FieldSpec struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Required    bool   `json:"required"`
	Secret      bool   `json:"secret"`
	Placeholder string `json:"placeholder,omitempty"`
	Help        string `json:"help,omitempty"`
	// Example is one concrete value for the field, shown in the "?" popover
	// beside it. Placeholder is what goes inside the empty input — it has to be
	// short and readable at input width — and Help is the sentence that explains
	// what the field is for. Example is the third thing a person needs and
	// neither of the other two can carry: a real value they can pattern-match
	// against, long enough to be unambiguous and so not a placeholder.
	Example string `json:"example,omitempty"`
}

// TypeInfo describes a channel type to the dashboard.
type TypeInfo struct {
	Type      string      `json:"type"`
	Label     string      `json:"label"`
	Fields    []FieldSpec `json:"fields"`
	Templates bool        `json:"templates"`

	// Setup is the recipe for this type: where the token comes from, where the
	// chat or the room comes from, in the order somebody does it the first time.
	//
	// It is separate from Fields on purpose. A FieldSpec explains the box it
	// belongs to — what a value looks like — and that is no help at all to
	// somebody who has never made a Telegram bot and does not know that the
	// chat id is a thing that exists. The dashboard shows this list under the
	// fields, in full, without a fold: the reader who needs it is the reader who
	// does not yet know it is worth opening.
	Setup []string `json:"setup,omitempty"`
}

// TypeInfos returns every channel type the UI should offer, in a stable order.
func TypeInfos() []TypeInfo {
	order := []string{"telegram", "discord", "matrix", "ntfy", "gotify", "slack", "webhook"}
	out := make([]TypeInfo, 0, len(order))
	for _, t := range order {
		if info, ok := typeInfos[t]; ok {
			out = append(out, info)
		}
	}
	return out
}

// KnownType reports whether a channel type exists.
func KnownType(t string) bool {
	_, ok := constructors[t]
	return ok
}

// FieldSpecs returns the field list for a type.
func FieldSpecs(t string) []FieldSpec {
	if info, ok := typeInfos[t]; ok {
		return info.Fields
	}
	return nil
}

// IsSecret reports whether a field holds a credential.
func IsSecret(channelType, key string) bool {
	for _, f := range FieldSpecs(channelType) {
		if f.Key == key {
			return f.Secret
		}
	}
	return false
}

// ValidateChannel checks required fields and, when templates are supplied, that
// they parse. It runs before a channel is stored.
func ValidateChannel(c model.Channel) error {
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("name is required")
	}
	if !KnownType(c.Type) {
		return fmt.Errorf("unsupported channel type %q", c.Type)
	}
	for _, f := range FieldSpecs(c.Type) {
		if f.Required && strings.TrimSpace(c.Config[f.Key]) == "" {
			return fmt.Errorf("%s is required", f.Label)
		}
	}
	if _, err := newFromConfig(c.Type, c.Config); err != nil {
		return err
	}
	for _, key := range []string{TemplateTitleKey, TemplateBodyKey} {
		if raw := strings.TrimSpace(c.Config[key]); raw != "" {
			if _, err := template.New(key).Parse(raw); err != nil {
				return fmt.Errorf("%s is not a valid template: %w", key, err)
			}
		}
	}
	return nil
}

// newFromConfig instantiates a notifier, letting it validate its own config.
func newFromConfig(channelType string, cfg map[string]string) (Notifier, error) {
	ctor, ok := constructors[channelType]
	if !ok {
		return nil, fmt.Errorf("unsupported channel type %q", channelType)
	}
	return ctor(cfg)
}

// ---------------------------------------------------------------------------
// message rendering
// ---------------------------------------------------------------------------

// Template keys accepted in a channel's configuration.
const (
	TemplateTitleKey = "title_template"
	TemplateBodyKey  = "body_template"
)

// StoredMarker is the placeholder the API returns in place of a stored secret
// and accepts back when the value should not change.
const StoredMarker = "__stored__"

// templateData is the value custom templates are rendered against.
type templateData struct {
	Event      string
	Sensor     model.Sensor
	Result     model.Result
	Incident   *model.Incident
	Name       string
	Type       string
	Target     string
	Group      string
	Tags       []string
	Status     string
	Error      string
	Message    string
	LatencyMS  float64
	StatusCode int
	DownFor    string
	DownSince  string
	Time       string
	URL        string
}

const (
	defaultDownTitle     = "🔴 DOWN: {{.Name}}"
	defaultUpTitle       = "🟢 RECOVERED: {{.Name}}"
	defaultReminderTitle = "🔴 STILL DOWN: {{.Name}}"
	defaultTestTitle     = "ProbeGlass test alert"

	defaultDownBody = `{{.Name}} is DOWN.
Target: {{.Type}} {{.Target}}
{{- if .Group}}
Group: {{.Group}}
{{- end}}
Error: {{.Message}}
Since: {{.DownSince}}
Detected: {{.Time}}
{{- if .URL}}
Dashboard: {{.URL}}
{{- end}}`

	defaultUpBody = `{{.Name}} is back UP.
Target: {{.Type}} {{.Target}}
{{- if .DownFor}}
Was down for: {{.DownFor}}
{{- end}}
Response: {{.StatusCode}}
Latency: {{printf "%.0f" .LatencyMS}} ms
Detected: {{.Time}}
{{- if .URL}}
Dashboard: {{.URL}}
{{- end}}`

	defaultReminderBody = `{{.Name}} is still DOWN.
Target: {{.Type}} {{.Target}}
Error: {{.Message}}
Down since: {{.DownSince}} ({{.DownFor}})
{{- if .URL}}
Dashboard: {{.URL}}
{{- end}}`

	defaultEscalationTitle = "🔴 ESCALATION: {{.Name}}"
	defaultEscalationBody  = `{{.Name}} has been DOWN for {{.DownFor}} and nobody has acknowledged it.
Target: {{.Type}} {{.Target}}
{{- if .Group}}
Group: {{.Group}}
{{- end}}
Error: {{.Message}}
Down since: {{.DownSince}}
{{- if .URL}}
Dashboard: {{.URL}}
{{- end}}`

	defaultTestBody = `This is a test message from ProbeGlass.

If you can read this, the alert channel is configured correctly.
Time: {{.Time}}`
)

func defaultTemplates(ev Event) (title, body string) {
	switch ev {
	case EventUp:
		return defaultUpTitle, defaultUpBody
	case EventReminder:
		return defaultReminderTitle, defaultReminderBody
	case EventEscalation:
		return defaultEscalationTitle, defaultEscalationBody
	case EventTest:
		return defaultTestTitle, defaultTestBody
	default:
		return defaultDownTitle, defaultDownBody
	}
}

// render fills in a title and body, honouring a channel's custom templates.
func render(msg Message, cfg map[string]string, now time.Time) (string, string) {
	data := templateData{
		Event:      string(msg.Event),
		Sensor:     msg.Sensor,
		Result:     msg.Result,
		Incident:   msg.Incident,
		Name:       msg.Sensor.Name,
		Type:       string(msg.Sensor.Type),
		Target:     msg.Sensor.Target,
		Group:      msg.Sensor.Group,
		Tags:       msg.Sensor.Tags,
		Message:    firstNonEmpty(msg.Result.Message, "sensor failed"),
		LatencyMS:  msg.Result.LatencyMS,
		StatusCode: msg.Result.StatusCode,
		Time:       now.Format(time.RFC1123),
		URL:        msg.URL,
	}
	switch msg.Event {
	case EventUp:
		data.Status = "up"
	case EventTest:
		data.Status = "test"
	default:
		data.Status = "down"
	}
	if msg.Incident != nil {
		data.DownSince = msg.Incident.StartedAt.Format(time.RFC1123)
		data.DownFor = humanDuration(now.Sub(msg.Incident.StartedAt))
	} else if msg.Event != EventUp && !msg.Result.Success {
		data.DownSince = now.Format(time.RFC1123)
	}

	defTitle, defBody := defaultTemplates(msg.Event)
	title := executeTemplate(cfg[TemplateTitleKey], defTitle, data)
	body := executeTemplate(cfg[TemplateBodyKey], defBody, data)
	return title, body
}

func executeTemplate(custom, fallback string, data templateData) string {
	src := strings.TrimSpace(custom)
	customised := src != ""
	if !customised {
		src = fallback
	}
	if out, ok := renderTemplate(src, data); ok {
		return out
	}
	// A broken custom template must not lose the alert, and it must not leak
	// the template source either: both a parse error and an execution error
	// fall back to the built-in template, rendered with the same data.
	if customised {
		if out, ok := renderTemplate(fallback, data); ok {
			return out
		}
	}
	return strings.TrimSpace(fallback)
}

// renderTemplate executes src against data, reporting whether it worked.
func renderTemplate(src string, data templateData) (string, bool) {
	t, err := template.New("msg").Parse(src)
	if err != nil {
		return "", false
	}
	var sb strings.Builder
	if err := t.Execute(&sb, data); err != nil {
		return "", false
	}
	return strings.TrimSpace(sb.String()), true
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// humanDuration renders a downtime duration for a human.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// ---------------------------------------------------------------------------
// dispatcher
// ---------------------------------------------------------------------------

// Dispatcher renders and delivers alerts.
type Dispatcher struct {
	store     *store.Store
	log       *slog.Logger
	baseURL   string
	client    *httpDoer
	mu        sync.Mutex
	lastError map[int64]string
}

// NewDispatcher builds a dispatcher. baseURL is used to link back to the
// dashboard from an alert and may be empty.
func NewDispatcher(st *store.Store, log *slog.Logger, baseURL string) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store:     st,
		log:       log,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    newHTTPDoer(15 * time.Second),
		lastError: map[int64]string{},
	}
}

// Notify sends an alert for a sensor to every channel assigned to it. It is
// expected to be called from a goroutine: delivery must never stall the
// scheduler.
func (d *Dispatcher) Notify(ctx context.Context, sensor model.Sensor, ev Event, res model.Result, inc *model.Incident) {
	channels, err := d.store.ChannelsForSensor(ctx, sensor.ID)
	if err != nil {
		d.log.Error("load alert channels", "sensor", sensor.Name, "error", err)
		return
	}
	d.notifyChannels(ctx, sensor, ev, res, inc, channels)
}

// NotifyEscalation sends the one escalation message an incident gets, to the
// second channel list. It is the same delivery path with a different event and
// a different set of destinations.
func (d *Dispatcher) NotifyEscalation(ctx context.Context, sensor model.Sensor, res model.Result, inc *model.Incident) {
	channels, err := d.store.EscalationChannelsForSensor(ctx, sensor.ID)
	if err != nil {
		d.log.Error("load escalation channels", "sensor", sensor.Name, "error", err)
		return
	}
	d.notifyChannels(ctx, sensor, EventEscalation, res, inc, channels)
}

func (d *Dispatcher) notifyChannels(ctx context.Context, sensor model.Sensor, ev Event, res model.Result, inc *model.Incident, channels []model.Channel) {
	if len(channels) == 0 {
		d.log.Debug("no alert channels assigned", "sensor", sensor.Name, "event", ev)
		return
	}

	url := ""
	if d.baseURL != "" {
		url = fmt.Sprintf("%s/#/sensor/%d", d.baseURL, sensor.ID)
	}
	base := Message{
		Event:    ev,
		Sensor:   sensor,
		Result:   res,
		Incident: inc,
		URL:      url,
		Time:     time.Now().UTC(),
	}

	var wg sync.WaitGroup
	for _, ch := range channels {
		wg.Add(1)
		go func(ch model.Channel) {
			defer wg.Done()
			d.deliver(ctx, ch, base)
		}(ch)
	}
	wg.Wait()
}

// deliver sends one message through one channel.
func (d *Dispatcher) deliver(ctx context.Context, ch model.Channel, msg Message) {
	n, err := newFromConfig(ch.Type, ch.Config)
	if err != nil {
		d.log.Error("build notifier", "channel", ch.Name, "type", ch.Type, "error", err)
		return
	}
	msg.Title, msg.Body = render(msg, ch.Config, msg.Time)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if lastErr = n.Send(ctx, msg); lastErr == nil {
			d.clearError(ch.ID)
			d.log.Info("alert sent",
				"channel", ch.Name, "type", ch.Type,
				"sensor", msg.Sensor.Name, "event", msg.Event)
			return
		}
		if ctx.Err() != nil || !isRetryable(lastErr) {
			break
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt) * time.Second):
		}
	}
	d.setError(ch.ID, lastErr.Error())
	d.log.Error("alert delivery failed",
		"channel", ch.Name, "type", ch.Type,
		"sensor", msg.Sensor.Name, "event", msg.Event, "error", lastErr)
}

// SendTest delivers a test message, used by the "Send test" button.
func (d *Dispatcher) SendTest(ctx context.Context, ch model.Channel, sensor model.Sensor, res model.Result) error {
	n, err := newFromConfig(ch.Type, ch.Config)
	if err != nil {
		return err
	}
	url := ""
	if d.baseURL != "" {
		url = d.baseURL
	}
	msg := Message{
		Event:  EventTest,
		Sensor: sensor,
		Result: res,
		URL:    url,
		Time:   time.Now().UTC(),
	}
	msg.Title, msg.Body = render(msg, ch.Config, msg.Time)

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := n.Send(ctx, msg); err != nil {
		d.setError(ch.ID, err.Error())
		return err
	}
	d.clearError(ch.ID)
	return nil
}

// LastError reports the most recent delivery error for a channel, if any.
func (d *Dispatcher) LastError(channelID int64) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastError[channelID]
}

func (d *Dispatcher) setError(channelID int64, msg string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastError[channelID] = msg
}

func (d *Dispatcher) clearError(channelID int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.lastError, channelID)
}

// Errors reports every channel that currently has a delivery error.
func (d *Dispatcher) Errors() map[int64]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make(map[int64]string, len(d.lastError))
	for k, v := range d.lastError {
		out[k] = v
	}
	return out
}

// isRetryable decides whether a failed delivery is worth another attempt.
func isRetryable(err error) bool {
	var se *sendError
	if errors.As(err, &se) {
		return se.retryable
	}
	return false
}

// sendError wraps a delivery failure and records whether retrying could help.
type sendError struct {
	status    int
	retryable bool
	msg       string
}

func (e *sendError) Error() string {
	if e.status > 0 {
		return fmt.Sprintf("HTTP %d: %s", e.status, e.msg)
	}
	return e.msg
}

func retryableError(format string, args ...any) error {
	return &sendError{retryable: true, msg: fmt.Sprintf(format, args...)}
}

func permanentError(format string, args ...any) error {
	return &sendError{retryable: false, msg: fmt.Sprintf(format, args...)}
}

func statusError(status int, body string) error {
	// 429 and 5xx are worth retrying; 4xx usually means a bad token.
	return &sendError{
		status:    status,
		retryable: status == 429 || status >= 500,
		msg:       truncate(strings.TrimSpace(body), 200),
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
