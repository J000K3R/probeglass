package status

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

func TestOverallIsAboutWhatIsPublished(t *testing.T) {
	cases := []struct {
		name     string
		statuses []model.Status
		want     model.Status
	}{
		{"nothing published", nil, model.StatusUnknown},
		{"all fine", []model.Status{model.StatusUp, model.StatusUp}, model.StatusUp},
		{"one down", []model.Status{model.StatusUp, model.StatusDown}, model.StatusDown},
		{"degraded, not down", []model.Status{model.StatusUp, model.StatusWarn}, model.StatusWarn},
		{"a paused service is neither", []model.Status{model.StatusUp, model.StatusPaused}, model.StatusUp},
		{"only paused", []model.Status{model.StatusPaused}, model.StatusUnknown},
		{"never probed", []model.Status{model.StatusUnknown}, model.StatusUnknown},
	}
	for _, tc := range cases {
		page := Page{}
		for _, s := range tc.statuses {
			page.Sensors = append(page.Sensors, Sensor{Status: s})
		}
		if got := page.Overall(); got != tc.want {
			t.Errorf("%s: Overall = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestPickLabelsFollowsTheBrowser(t *testing.T) {
	cases := []struct{ header, want string }{
		{"", "en"},
		{"en-GB,en;q=0.9", "en"},
		{"de-DE,de;q=0.9,en;q=0.8", "de"},
		{"fr-FR,fr;q=0.9,de;q=0.5", "en"},
	}
	for _, tc := range cases {
		if got := pickLabels(tc.header).Lang; got != tc.want {
			t.Errorf("pickLabels(%q) = %q, want %q", tc.header, got, tc.want)
		}
	}
}

// TestRenderEscapesWhatASensorIsCalled is the reason the page is built with
// html/template and not by concatenation: a sensor name is text somebody typed,
// and it is about to be shown to people outside the installation.
func TestRenderEscapesWhatASensorIsCalled(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Sections = []Section{{Title: "Web", Sensors: []Entry{{ID: 1}}}}
	page := Page{
		Config:  cfg,
		Updated: time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC),
		Sections: []SectionView{{Title: "Web", Sensors: []Sensor{{
			Name:   `<script>alert("x")</script>`,
			Status: model.StatusUp,
		}}}},
		Sensors: []Sensor{{Status: model.StatusUp}},
	}
	var out bytes.Buffer
	if err := Render(&out, page, "en"); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()
	if strings.Contains(html, "<script>alert") {
		t.Error("the sensor name was written into the page unescaped")
	}
	if !strings.Contains(html, "&lt;script&gt;") {
		t.Errorf("the escaped name is not on the page: %s", html)
	}
	if !strings.Contains(html, `<meta name="robots" content="noindex, nofollow">`) {
		t.Error("the page does not keep search engines away")
	}
}

// TestRenderDrawsTheHistory is the page's new shape: a strip per service, a
// curve under it, the overall figure in the banner and the section's own count.
func TestRenderDrawsTheHistory(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Layout = "charts"
	cfg.HistoryDays = 90
	cfg.Sections = []Section{{Title: "Web", Sensors: []Entry{{ID: 1}, {ID: 2}}}}

	days := []DayPoint{
		{Date: time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), Uptime: Uptime{Percent: 100, Known: true}, HasData: true, HasLatency: true, LatencyMS: 20},
		{Date: time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC), Uptime: Uptime{Percent: 99.5, Known: true}, HasData: true, HasLatency: true, LatencyMS: 40, Outage: true},
		{Date: time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC), Maintenance: true, HasData: true, HasLatency: true, LatencyMS: 30},
		{Date: time.Date(2026, 8, 11, 0, 0, 0, 0, time.UTC)},
		{Date: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Uptime: Uptime{Percent: 95, Known: true}, HasData: true, HasLatency: true, LatencyMS: 60, Outage: true},
	}
	page := Page{
		Config:        cfg,
		Updated:       time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC),
		OverallUptime: Uptime{Percent: 99.9, Known: true},
		Sections: []SectionView{{Title: "Web", Up: 2, Total: 2, Sensors: []Sensor{
			{Name: "Web", Type: "HTTP", Status: model.StatusUp, UptimeHistory: Uptime{Percent: 99.9, Known: true}, Days: days},
			{Name: "Database", Type: "TCP", Status: model.StatusUp, UptimeHistory: Uptime{Percent: 100, Known: true}, Days: days},
		}}},
		Sensors: []Sensor{{Status: model.StatusUp}, {Status: model.StatusUp}},
	}
	var out bytes.Buffer
	if err := Render(&out, page, "en"); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()
	for _, want := range []string{
		"layout-charts", "uptime-strip", "chart-type", "chart-uptime",
		"strip-axis", "lat-canvas", "lat-line", "lat-band", "lat-outage",
		"banner-uptime", "2 of 2 operational", "99.90% · 90 days",
		`class="legend"`, "99.9% and up",
		`class="nodata"`, `class="warn"`, `class="excused"`, `class="down"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the page does not carry %q", want)
		}
	}
}

// TestRenderSaysWhenAWindowHasNoData keeps the grey strip from reading as a
// hundred outages: a service that has never been probed says so in words.
func TestRenderSaysWhenAWindowHasNoData(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Layout = "charts"
	days := make([]DayPoint, cfg.HistoryDays)
	page := Page{
		Config:  cfg,
		Updated: time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC),
		Sections: []SectionView{{Title: "New", Sensors: []Sensor{{
			Name: "Added today", Type: "HTTP", Status: model.StatusUnknown,
			UptimeHistory: Uptime{}, Days: days,
		}}}},
		Sensors: []Sensor{{Status: model.StatusUnknown}},
	}
	var out bytes.Buffer
	if err := Render(&out, page, "en"); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()
	if !strings.Contains(html, "No measurements in this window yet.") {
		t.Error("the page did not explain an empty window")
	}
	if got := strings.Count(html, `class="nodata"`); got != cfg.HistoryDays {
		t.Errorf("the empty strip has %d grey bars, want %d", got, cfg.HistoryDays)
	}
}

// TestRenderIncidentsAsCards covers both ends of the incident list: a resolved
// outage carries its duration, and an empty list says so instead of vanishing.
func TestRenderIncidentsAsCards(t *testing.T) {
	cfg := DefaultConfig()
	started := time.Date(2026, 10, 28, 12, 4, 0, 0, time.UTC)
	resolved := started.Add(15 * time.Minute)

	page := Page{
		Config:  cfg,
		Updated: started.Add(time.Hour),
		Incidents: []Incident{{
			Sensor: "Grafana", Started: started, Resolved: &resolved,
		}},
	}
	var out bytes.Buffer
	if err := Render(&out, page, "en"); err != nil {
		t.Fatalf("render: %v", err)
	}
	html := out.String()
	for _, want := range []string{
		"incident-box", "<b>Grafana</b>", `pill resolved`, ">Resolved<",
		"15 min", "incident-meta",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the incident card does not carry %q", want)
		}
	}

	empty := Page{Config: cfg, Updated: started}
	out.Reset()
	if err := Render(&out, empty, "en"); err != nil {
		t.Fatalf("render empty: %v", err)
	}
	if !strings.Contains(out.String(), "No incidents in the last 30 days.") {
		t.Error("an empty incident list did not say so")
	}
}

// TestDayPointsKeepGapsAndMaintenanceApart guards the difference between "not
// probed" and "inside a maintenance window". Both have no counted probes, and
// only one of them is a gap.
func TestDayPointsKeepGapsAndMaintenanceApart(t *testing.T) {
	points := []model.SeriesPoint{
		{},
		{Excused: 5},
		{Total: 10, OK: 8, UptimePC: 80, AvgMS: 42},
		{Total: 10, OK: 10, UptimePC: 100, AvgMS: 21},
	}
	days := dayPoints(points)
	if days[0].HasData || days[0].Maintenance || days[0].Outage {
		t.Errorf("an empty day is not a gap: %+v", days[0])
	}
	if !days[1].HasData || !days[1].Maintenance || days[1].Uptime.Known {
		t.Errorf("an excused day is maintenance, not data: %+v", days[1])
	}
	if !days[2].Outage || days[2].Uptime.Percent != 80 || days[2].LatencyMS != 42 {
		t.Errorf("a day with a failed probe: %+v", days[2])
	}
	if days[3].Outage || days[3].Uptime.Percent != 100 {
		t.Errorf("a clean day: %+v", days[3])
	}
}

func TestNormaliseFillsAndDrops(t *testing.T) {
	cfg := Config{
		Title:         strings.Repeat("x", 200),
		Theme:         "neon",
		Accent:        "pink",
		Layout:        "wall",
		IncidentDays:  12,
		Logo:          "  builtin:server  ",
		BackgroundURL: "http://example.org/bg.jpg",
		BackgroundDim: 95,
		Notices: []Notice{
			{Text: "  Maintenance tonight  ", Colour: "amber", Icon: "builtin:clock"},
			{Text: "   ", Colour: "rose"},
			{Text: "Unknown colour", Colour: "pink"},
		},
		Sections: []Section{
			{Title: "  Web  ", Icon: " builtin:globe ", Colour: "amber", Sensors: []Entry{{ID: 3}, {ID: 3}, {ID: 0}, {ID: 4}}},
			{Title: "Again", Sensors: []Entry{{ID: 4}}},
		},
	}
	Normalise(&cfg)
	if len(cfg.Title) != 80 {
		t.Errorf("title was not clamped: %d characters", len(cfg.Title))
	}
	if cfg.Theme != "auto" || cfg.Accent != "azure" || cfg.Layout != "charts" || cfg.IncidentDays != 30 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.HistoryDays != 90 {
		t.Errorf("history window was not defaulted: %d", cfg.HistoryDays)
	}
	if cfg.Logo != "builtin:server" {
		t.Errorf("logo: %q", cfg.Logo)
	}
	if cfg.BackgroundURL != "" {
		t.Errorf("a plain-http background survived: %q", cfg.BackgroundURL)
	}
	if cfg.BackgroundDim != 90 {
		t.Errorf("dimming was not clamped: %d", cfg.BackgroundDim)
	}
	if len(cfg.Sections) != 2 || cfg.Sections[0].Title != "Web" || cfg.Sections[0].Icon != "builtin:globe" {
		t.Errorf("sections: %+v", cfg.Sections)
	}
	if got := cfg.SensorIDs(); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("SensorIDs = %v, want [3 4]", got)
	}
	if got := backgroundURL("https://example.org/bg.jpg"); got != "https://example.org/bg.jpg" {
		t.Errorf("an https background was dropped: %q", got)
	}
	if got := backgroundURL("/assets/favicon.svg"); got != "/assets/favicon.svg" {
		t.Errorf("a same-origin background was dropped: %q", got)
	}
	if got := backgroundURL("//example.org/bg.jpg"); got != "" {
		t.Errorf("a protocol-relative background survived: %q", got)
	}
	if cfg.Sections[0].Colour != "amber" {
		t.Errorf("a valid colour was lost: %q", cfg.Sections[0].Colour)
	}
	if len(cfg.Notices) != 2 {
		t.Fatalf("notices: %+v", cfg.Notices)
	}
	if cfg.Notices[0].Text != "Maintenance tonight" || cfg.Notices[0].Colour != "amber" {
		t.Errorf("the first notice was not normalised: %+v", cfg.Notices[0])
	}
	if cfg.Notices[1].Colour != "" {
		t.Errorf("an unknown notice colour was kept: %q", cfg.Notices[1].Colour)
	}
}

func TestIconOfOnlyReturnsOurAssets(t *testing.T) {
	cases := []struct {
		ref   string
		url   string
		drawn bool
	}{
		{"builtin:router", "/assets/icons/router.svg", true},
		{"brand:sonarr", "/assets/brands/sonarr.svg", false},
		{"brand:mos", "/assets/brands/mos.png", false},
		{"upload:7", "/status/tok/icon/7", false},
		{"upload:7", "", false}, // no token, no route
		{"builtin:../../etc/passwd", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		base := "/status/tok"
		if tc.url == "" {
			base = ""
		}
		url, drawn := iconOf(tc.ref, base)
		if url != tc.url || drawn != tc.drawn {
			t.Errorf("iconOf(%q) = (%q, %v), want (%q, %v)", tc.ref, url, drawn, tc.url, tc.drawn)
		}
	}
}
