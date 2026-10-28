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
	page := Page{
		Updated: time.Date(2026, 10, 28, 17, 0, 0, 0, time.UTC),
		Sensors: []Sensor{{
			Name:   `<script>alert("x")</script>`,
			Status: model.StatusUp,
		}},
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
