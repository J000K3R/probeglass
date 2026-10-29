package status

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Config is what the operator made of the public page: the words and colours at
// the top, how much of each service to show, and the sections that decide who
// appears where.
//
// It is stored as one JSON setting rather than as columns because it is one
// decision. A page whose layout was saved but whose sections were not is a page
// nobody meant, and a single value cannot be half written.
type Config struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	// Logo is the page's own mark at the top; empty draws none. It is an icon
	// reference like everywhere else, so an upload is served through the page.
	Logo string `json:"logo,omitempty"`
	// BackgroundURL is an https image or a path on this server. It is the fallback
	// behind an uploaded photograph: while an upload exists the page uses that,
	// and this remains what is shown after the upload is removed. Empty keeps the
	// plain surface colour.
	BackgroundURL string `json:"background_url,omitempty"`
	// BackgroundDim is how much of the page colour is laid over the image, in
	// percent: 0 leaves the photograph bare, 90 leaves a hint of it.
	BackgroundDim int `json:"background_dim"`
	// Theme is "auto", "light" or "dark"; Accent one of model.ACCENTS; Layout
	// "list", "cards" or "compact".
	Theme  string `json:"theme"`
	Accent string `json:"accent"`
	Layout string `json:"layout"`

	ShowLogos     bool `json:"show_logos"`
	ShowUptime24  bool `json:"show_uptime_24"`
	ShowUptime7   bool `json:"show_uptime_7"`
	ShowUptime30  bool `json:"show_uptime_30"`
	ShowLastCheck bool `json:"show_last_check"`
	// ShowHeartbeat draws the last probes as a strip of ticks, the way a status
	// page usually says "it has been fine for a while" without a chart.
	ShowHeartbeat bool `json:"show_heartbeat"`
	ShowIncidents bool `json:"show_incidents"`
	// ShowCauses publishes the probe's own words on an incident. Off by default:
	// that text is written for the operator.
	ShowCauses bool `json:"show_causes"`
	// IncidentDays is 7, 30 or 90.
	IncidentDays int `json:"incident_days"`

	// Notices are the messages the operator writes at the top of the page, the
	// way a status page says "we are moving the NAS this evening" without an
	// incident.
	Notices []Notice `json:"notices"`

	Sections []Section `json:"sections"`
}

// Notice is one message on the public page.
type Notice struct {
	Text string `json:"text"`
	// Colour is one of the accents, or empty for the page's own.
	Colour string `json:"colour,omitempty"`
	// Icon is an icon reference — a built-in drawing, a brand mark or an upload —
	// shown before the text. Empty draws none.
	Icon string `json:"icon,omitempty"`
}

// Section is one heading on the public page. A sensor belongs to one section;
// the same id in two sections is the first one's.
type Section struct {
	Title string `json:"title"`
	// Icon is the symbol before the heading; empty draws none.
	Icon string `json:"icon,omitempty"`
	// Colour is one of the accents, or empty for the page's own accent.
	Colour  string  `json:"colour"`
	Sensors []Entry `json:"sensors"`
}

// Entry is one sensor in a section. The label and the icon are overrides for
// the public page; empty means the sensor's own.
type Entry struct {
	ID    int64  `json:"id"`
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

// Accents is the palette a page may use, shared with the dashboard's own.
var Accents = []string{"azure", "violet", "emerald", "amber", "rose", "slate"}

// Lays is the order the editor offers; the first is the default.
var Layouts = []string{"list", "cards", "compact"}

// DefaultConfig is the page somebody gets before they change anything: a plain
// list of the sensors they tick, with the same words in both languages.
func DefaultConfig() Config {
	return Config{
		Title:         "Status",
		BackgroundDim: 60,
		Theme:         "auto",
		Accent:        "azure",
		Layout:        "list",
		ShowLogos:     true,
		ShowUptime24:  true,
		ShowUptime7:   true,
		ShowUptime30:  true,
		ShowLastCheck: true,
		ShowHeartbeat: true,
		ShowIncidents: true,
		IncidentDays:  30,
		Notices:       []Notice{},
		Sections:      []Section{},
	}
}

// LoadConfig reads the stored page, falling back to the flat selection of an
// installation that was configured before sections existed.
func LoadConfig(ctx context.Context, st *store.Store) Config {
	cfg := DefaultConfig()
	if raw := st.Setting(ctx, model.SettingStatusPage, ""); strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			// A row somebody edited by hand is not worth refusing to draw: the
			// default page is a working page, and the panel can overwrite it.
			cfg = DefaultConfig()
		}
	} else {
		// Before sections, the selection was one list and the cause switch was a
		// setting of its own. Both are carried over once, and the next save
		// writes them into the config.
		ids, err := st.StatusSensors(ctx)
		if err == nil && len(ids) > 0 {
			section := Section{Title: "", Sensors: make([]Entry, 0, len(ids))}
			for _, id := range ids {
				section.Sensors = append(section.Sensors, Entry{ID: id})
			}
			cfg.Sections = []Section{section}
		}
		if _, causes := st.StatusPageSettings(ctx); causes {
			cfg.ShowCauses = true
		}
	}
	Normalise(&cfg)
	return cfg
}

// SaveConfig stores the page after normalising it. The caller has already
// checked that the sensors exist.
func SaveConfig(ctx context.Context, st *store.Store, cfg Config) error {
	Normalise(&cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return st.SetSetting(ctx, model.SettingStatusPage, string(raw))
}

// Normalise fills the gaps and drops what cannot be drawn. It is applied on both
// the way in and the way out, so a config written by an older version or edited
// by hand is still a page.
func Normalise(cfg *Config) {
	cfg.Title = clamp(strings.TrimSpace(cfg.Title), 80)
	if cfg.Title == "" {
		cfg.Title = "Status"
	}
	cfg.Subtitle = clamp(strings.TrimSpace(cfg.Subtitle), 240)
	cfg.Logo = strings.TrimSpace(cfg.Logo)
	cfg.BackgroundURL = backgroundURL(cfg.BackgroundURL)
	if cfg.BackgroundDim < 0 {
		cfg.BackgroundDim = 0
	}
	if cfg.BackgroundDim > 90 {
		cfg.BackgroundDim = 90
	}
	cfg.Theme = oneOf(cfg.Theme, []string{"auto", "light", "dark"}, "auto")
	cfg.Accent = oneOf(cfg.Accent, Accents, "azure")
	cfg.Layout = oneOf(cfg.Layout, Layouts, "list")
	if cfg.IncidentDays != 7 && cfg.IncidentDays != 30 && cfg.IncidentDays != 90 {
		cfg.IncidentDays = 30
	}

	if len(cfg.Notices) > 10 {
		cfg.Notices = cfg.Notices[:10]
	}
	notices := make([]Notice, 0, len(cfg.Notices))
	for _, notice := range cfg.Notices {
		notice.Text = clamp(strings.TrimSpace(notice.Text), 280)
		if notice.Text == "" {
			continue
		}
		notice.Colour = oneOf(notice.Colour, Accents, "")
		notice.Icon = strings.TrimSpace(notice.Icon)
		notices = append(notices, notice)
	}
	cfg.Notices = notices

	if len(cfg.Sections) > 20 {
		cfg.Sections = cfg.Sections[:20]
	}
	seen := map[int64]bool{}
	out := make([]Section, 0, len(cfg.Sections))
	for _, section := range cfg.Sections {
		section.Title = clamp(strings.TrimSpace(section.Title), 80)
		section.Icon = strings.TrimSpace(section.Icon)
		section.Colour = oneOf(section.Colour, Accents, "")
		entries := make([]Entry, 0, len(section.Sensors))
		for _, entry := range section.Sensors {
			if entry.ID <= 0 || seen[entry.ID] {
				continue
			}
			seen[entry.ID] = true
			entry.Label = clamp(strings.TrimSpace(entry.Label), 80)
			entries = append(entries, entry)
			if len(entries) >= 500 {
				break
			}
		}
		section.Sensors = entries
		out = append(out, section)
	}
	cfg.Sections = out
}

// SensorIDs is every sensor the page draws, in section order.
func (c Config) SensorIDs() []int64 {
	out := []int64{}
	for _, section := range c.Sections {
		for _, entry := range section.Sensors {
			out = append(out, entry.ID)
		}
	}
	return out
}

// backgroundURL keeps an image address that a browser can fetch from this page:
// https, or a path on this server. Anything else — plain http, a data URI, a
// protocol-relative address — is dropped rather than written into a style.
func backgroundURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 500 {
		return ""
	}
	if strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") {
		return value
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return value
}

func oneOf(value string, allowed []string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
}

func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n])
}
