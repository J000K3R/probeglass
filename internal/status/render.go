package status

import (
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"math"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// labels is the page's fixed wording, in the reader's language. Two languages,
// chosen from the browser's Accept-Language, because a status page is read by the
// people a homelab serves and the person operating it should not have to pick a
// language on their behalf.
type labels struct {
	Lang       string
	Title      string
	Services   string
	Service    string
	Status     string
	LastCheck  string
	Incidents  string
	NoServices string
	Updated    string
	Footer     string
	Never      string
	Ongoing    string
	Resolved   string

	up, down, warn, paused, unknown string
}

var (
	english = labels{
		Lang: "en", Title: "Status", Services: "Services", Service: "Service",
		Status: "Status", LastCheck: "Last checked", Incidents: "Recent incidents",
		NoServices: "No services have been published yet.",
		Updated:    "Updated", Footer: "ProbeGlass",
		Never: "never", Ongoing: "ongoing", Resolved: "resolved",
		up: "Operational", down: "Down", warn: "Degraded", paused: "Paused",
		unknown: "Unknown",
	}
	german = labels{
		Lang: "de", Title: "Status", Services: "Dienste", Service: "Dienst",
		Status: "Status", LastCheck: "Zuletzt geprüft", Incidents: "Letzte Vorfälle",
		NoServices: "Es sind noch keine Dienste veröffentlicht.",
		Updated:    "Aktualisiert", Footer: "ProbeGlass",
		Never: "noch nie", Ongoing: "andauernd", Resolved: "behoben",
		up: "Betriebsbereit", down: "Ausgefallen", warn: "Gestört",
		paused: "Pausiert", unknown: "Unbekannt",
	}
)

// pickLabels chooses German only when the browser asks for it first. It is a
// reading aid, not a setting: nothing here is stored, and an English page is
// what everybody else gets.
func pickLabels(acceptLanguage string) labels {
	for _, part := range strings.Split(acceptLanguage, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "" || tag == "*":
			continue
		case strings.HasPrefix(tag, "de"):
			return german
		default:
			return english
		}
	}
	return english
}

// view is the page with every value already a string. Nothing is decided in the
// template, so the template is only a shape and cannot disagree with the builder
// about what a status means.
type view struct {
	CSS         template.CSS
	Lang        string
	Title       string
	Updated     string
	Overall     model.Status
	OverallWord string
	Counts      string
	Labels      labels
	Sensors     []sensorView
	Incidents   []incidentView
}

type sensorView struct {
	Name       string
	Status     model.Status
	StatusWord string
	Uptime24   string
	Uptime7    string
	Uptime30   string
	LastCheck  string
}

type incidentView struct {
	Sensor  string
	When    string
	State   string
	Cause   string
	Ongoing bool
}

// Render writes the page. The labels are chosen here rather than by the handler
// so that a new caller cannot forget them.
func Render(w io.Writer, page Page, acceptLanguage string) error {
	l := pickLabels(acceptLanguage)
	v := view{
		CSS:       template.CSS(pageCSS), //nolint:gosec // an embedded file, never user input
		Lang:      l.Lang,
		Title:     l.Title,
		Updated:   page.Updated.UTC().Format("02 Jan 2006 15:04 UTC"),
		Overall:   page.Overall(),
		Labels:    l,
		Sensors:   make([]sensorView, 0, len(page.Sensors)),
		Incidents: make([]incidentView, 0, len(page.Incidents)),
	}
	if l.Lang == "de" {
		v.Updated = page.Updated.UTC().Format("02.01.2006 15:04 UTC")
	}
	v.OverallWord = l.word(page.Overall())

	up, down, paused := 0, 0, 0
	for _, s := range page.Sensors {
		switch s.Status {
		case model.StatusUp:
			up++
		case model.StatusDown:
			down++
		case model.StatusPaused:
			paused++
		case model.StatusWarn, model.StatusUnknown:
			// Degraded and never-probed are drawn by the row, not counted in the
			// one-line summary: it says how many are fine, how many are out and
			// how many are paused, which is what a reader acts on.
		}
	}
	v.Counts = l.counts(up, down, paused)

	for _, s := range page.Sensors {
		v.Sensors = append(v.Sensors, sensorView{
			Name:       s.Name,
			Status:     s.Status,
			StatusWord: l.word(s.Status),
			Uptime24:   uptimeText(s.Uptime24, l),
			Uptime7:    uptimeText(s.Uptime7, l),
			Uptime30:   uptimeText(s.Uptime30, l),
			LastCheck:  l.when(s.LastCheck),
		})
	}
	for _, inc := range page.Incidents {
		state := l.Resolved
		if inc.Ongoing {
			state = l.Ongoing
		} else if inc.Resolved != nil {
			state = l.Resolved + " · " + durationText(inc.Resolved.Sub(inc.Started), l)
		}
		v.Incidents = append(v.Incidents, incidentView{
			Sensor:  inc.Sensor,
			When:    l.when(inc.Started),
			State:   state,
			Cause:   inc.Cause,
			Ongoing: inc.Ongoing,
		})
	}
	return pageTemplate.Execute(w, v)
}

func (l labels) word(s model.Status) string {
	switch s {
	case model.StatusUp:
		return l.up
	case model.StatusDown:
		return l.down
	case model.StatusWarn:
		return l.warn
	case model.StatusPaused:
		return l.paused
	case model.StatusUnknown:
		return l.unknown
	default:
		return l.unknown
	}
}

// when is a moment as the page writes it. The zone is always UTC and always
// named: a public page read from another timezone should not have to guess.
func (l labels) when(t time.Time) string {
	if t.IsZero() {
		return l.Never
	}
	if l.Lang == "de" {
		return t.UTC().Format("02.01.2006 15:04 UTC")
	}
	return t.UTC().Format("02 Jan 2006 15:04 UTC")
}

// counts is the line under the banner. The three words are adjectives, so
// neither language inflects them and no plural form is needed.
func (l labels) counts(up, down, paused int) string {
	parts := []string{}
	if up > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", up, l.countWord("operational", "betriebsbereit")))
	}
	if down > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", down, l.countWord("down", "ausgefallen")))
	}
	if paused > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", paused, l.countWord("paused", "pausiert")))
	}
	return strings.Join(parts, " · ")
}

func (l labels) countWord(en, de string) string {
	if l.Lang == "de" {
		return de
	}
	return en
}

func uptimeText(u Uptime, l labels) string {
	if !u.Known {
		return "—"
	}
	// Two decimals, rounded, and 100 is written as 100% rather than 100.00%.
	p := math.Round(u.Percent*100) / 100
	if p >= 100 {
		if l.Lang == "de" {
			return "100 %"
		}
		return "100%"
	}
	if l.Lang == "de" {
		return strings.Replace(fmt.Sprintf("%.2f", p), ".", ",", 1) + " %"
	}
	return fmt.Sprintf("%.2f%%", p)
}

func durationText(d time.Duration, l labels) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	parts := []string{}
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", days, l.unit("d", "T")))
	}
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", hours, l.unit("h", "Std.")))
	}
	if minutes > 0 && days == 0 {
		parts = append(parts, fmt.Sprintf("%d %s", minutes, l.unit("min", "Min.")))
	}
	if len(parts) == 0 {
		return "< 1 " + l.unit("min", "Min.")
	}
	return strings.Join(parts, " ")
}

// unit is a duration's abbreviation; only German differs, and only for hours.
func (l labels) unit(en, de string) string {
	if l.Lang == "de" {
		return de
	}
	return en
}

// pageCSS is the page's whole stylesheet, embedded so that the page stays one
// response and the styles are still a file somebody can read.
//
//go:embed page.css
var pageCSS string

var pageTemplate = template.Must(template.New("status").Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="refresh" content="60">
<title>{{.Title}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<main>
<header>
<h1>{{.Title}}</h1>
<p class="updated">{{.Labels.Updated}} {{.Updated}}</p>
</header>
<div class="banner {{.Overall}}">
  <strong>{{.OverallWord}}</strong>
  {{if .Counts}}<span class="counts">{{.Counts}}</span>{{end}}
</div>

<section>
<h2>{{.Labels.Services}}</h2>
{{if .Sensors}}
<div class="table-wrap">
<table>
<thead><tr>
  <th>{{.Labels.Service}}</th>
  <th>{{.Labels.Status}}</th>
  <th class="num">24h</th>
  <th class="num hide-sm">7d</th>
  <th class="num hide-sm">30d</th>
  <th class="hide-sm">{{.Labels.LastCheck}}</th>
</tr></thead>
<tbody>
{{range .Sensors}}<tr>
  <td>{{.Name}}</td>
  <td><span class="pill {{.Status}}">{{.StatusWord}}</span></td>
  <td class="num">{{.Uptime24}}</td>
  <td class="num hide-sm">{{.Uptime7}}</td>
  <td class="num hide-sm">{{.Uptime30}}</td>
  <td class="muted hide-sm">{{.LastCheck}}</td>
</tr>
{{end}}</tbody>
</table>
</div>
{{else}}<p class="muted">{{.Labels.NoServices}}</p>{{end}}
</section>

{{if .Incidents}}
<section>
<h2>{{.Labels.Incidents}}</h2>
<ul class="incidents">
{{range .Incidents}}<li{{if .Ongoing}} class="ongoing"{{end}}>
  <strong>{{.Sensor}}</strong> · <span class="muted">{{.When}} · {{.State}}</span>
  {{if .Cause}}<p>{{.Cause}}</p>{{end}}
</li>
{{end}}</ul>
</section>
{{end}}

<footer>{{.Labels.Footer}}</footer>
</main>
</body>
</html>
`))
