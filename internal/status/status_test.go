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

func TestNormaliseFillsAndDrops(t *testing.T) {
	cfg := Config{
		Title:        strings.Repeat("x", 200),
		Theme:        "neon",
		Accent:       "pink",
		Layout:       "wall",
		IncidentDays: 12,
		Notices: []Notice{
			{Text: "  Maintenance tonight  ", Colour: "amber", Icon: "builtin:clock"},
			{Text: "   ", Colour: "rose"},
			{Text: "Unknown colour", Colour: "pink"},
		},
		Sections: []Section{
			{Title: "  Web  ", Colour: "amber", Sensors: []Entry{{ID: 3}, {ID: 3}, {ID: 0}, {ID: 4}}},
			{Title: "Again", Sensors: []Entry{{ID: 4}}},
		},
	}
	Normalise(&cfg)
	if len(cfg.Title) != 80 {
		t.Errorf("title was not clamped: %d characters", len(cfg.Title))
	}
	if cfg.Theme != "auto" || cfg.Accent != "azure" || cfg.Layout != "list" || cfg.IncidentDays != 30 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if len(cfg.Sections) != 2 || cfg.Sections[0].Title != "Web" {
		t.Errorf("sections: %+v", cfg.Sections)
	}
	if got := cfg.SensorIDs(); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Errorf("SensorIDs = %v, want [3 4]", got)
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
