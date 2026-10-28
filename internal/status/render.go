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
	Services   string
	Service    string
	Status     string
	Checks     string
	LastCheck  string
	Incidents  string
	NoServices string
	Updated    string
	Footer     string
	Edit       string
	Never      string
	Ongoing    string
	Resolved   string

	up, down, warn, paused, unknown string
}

var (
	english = labels{
		Lang: "en", Services: "Services", Service: "Service",
		Status: "Status", Checks: "Checks", LastCheck: "Last checked",
		Incidents: "Recent incidents", NoServices: "No services have been published yet.",
		Updated: "Updated", Footer: "ProbeGlass", Edit: "Edit page",
		Never: "never", Ongoing: "ongoing", Resolved: "resolved",
		up: "Operational", down: "Down", warn: "Degraded", paused: "Paused",
		unknown: "Unknown",
	}
	german = labels{
		Lang: "de", Services: "Dienste", Service: "Dienst",
		Status: "Status", Checks: "Prüfungen", LastCheck: "Zuletzt geprüft",
		Incidents: "Letzte Vorfälle", NoServices: "Es sind noch keine Dienste veröffentlicht.",
		Updated: "Aktualisiert", Footer: "ProbeGlass", Edit: "Seite bearbeiten",
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
	Config      Config
	Title       string
	Subtitle    string
	Updated     string
	Overall     model.Status
	OverallWord string
	Counts      string
	Labels      labels
	Sections    []sectionView
	Notices     []noticeView
	Incidents   []incidentView
	EditURL     string
}

type sectionView struct {
	Title   string
	Colour  string
	Sensors []sensorView
}

type sensorView struct {
	Name       string
	Status     model.Status
	StatusWord string
	Uptime24   string
	Uptime7    string
	Uptime30   string
	LastCheck  string
	IconURL    string
	IconDrawn  bool
	Beats      []beatView
}

type beatView struct {
	Class string
	Title string
}

type noticeView struct {
	Text      string
	Colour    string
	IconURL   string
	IconDrawn bool
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
	cfg := page.Config
	Normalise(&cfg)
	v := view{
		CSS:       template.CSS(pageCSS), //nolint:gosec // an embedded file, never user input
		Lang:      l.Lang,
		Config:    cfg,
		Title:     cfg.Title,
		Subtitle:  cfg.Subtitle,
		Overall:   page.Overall(),
		Labels:    l,
		Sections:  make([]sectionView, 0, len(page.Sections)),
		Notices:   make([]noticeView, 0, len(page.Notices)),
		Incidents: make([]incidentView, 0, len(page.Incidents)),
		EditURL:   page.EditURL,
	}
	for _, notice := range page.Notices {
		v.Notices = append(v.Notices, noticeView(notice))
	}

	v.Updated = page.Updated.UTC().Format("02 Jan 2006 15:04 UTC")
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
			// Drawn by the row, not counted in the one-line summary: it says how
			// many are fine, how many are out and how many are paused.
		}
	}
	v.Counts = l.counts(up, down, paused)

	for _, section := range page.Sections {
		sv := sectionView{Title: section.Title, Colour: section.Colour}
		for _, s := range section.Sensors {
			row := sensorView{
				Name:       s.Name,
				Status:     s.Status,
				StatusWord: l.word(s.Status),
				Uptime24:   uptimeText(s.Uptime24, l),
				Uptime7:    uptimeText(s.Uptime7, l),
				Uptime30:   uptimeText(s.Uptime30, l),
				LastCheck:  l.when(s.LastCheck),
				IconURL:    s.IconURL,
				IconDrawn:  s.IconDrawn,
			}
			for _, beat := range s.Heartbeat {
				class := "down"
				switch {
				case beat.Up:
					class = "up"
				case beat.Excused:
					class = "excused"
				}
				row.Beats = append(row.Beats, beatView{Class: class, Title: l.when(beat.When)})
			}
			sv.Sensors = append(sv.Sensors, row)
		}
		v.Sections = append(v.Sections, sv)
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
<html lang="{{.Lang}}" data-theme="{{.Config.Theme}}" data-accent="{{.Config.Accent}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex, nofollow">
<meta http-equiv="refresh" content="60">
<title>{{.Title}}</title>
<style>{{.CSS}}</style>
</head>
<body>
<main class="layout-{{.Config.Layout}}">
<header class="page-head">
  <h1>{{.Title}}</h1>
  {{if .Subtitle}}<p class="subtitle">{{.Subtitle}}</p>{{end}}
  <p class="updated">{{.Labels.Updated}} {{.Updated}}</p>
</header>

{{range .Notices}}
<div class="notice{{if .Colour}} c-{{.Colour}}{{end}}">
  {{template "logo" .}}
  <p>{{.Text}}</p>
</div>
{{end}}

<div class="banner {{.Overall}}">
  <strong>{{.OverallWord}}</strong>
  {{if .Counts}}<span class="counts">{{.Counts}}</span>{{end}}
</div>

{{if .Sections}}
{{range .Sections}}
<section class="group{{if .Colour}} c-{{.Colour}}{{end}}">
  {{if .Title}}<h2>{{.Title}}</h2>{{end}}

  {{if eq $.Config.Layout "cards"}}
  <div class="cards">
    {{range .Sensors}}
    <article class="card {{.Status}}">
      <div class="card-top">
        {{if $.Config.ShowLogos}}{{template "logo" .}}{{end}}
        <b>{{.Name}}</b>
        <span class="pill {{.Status}}">{{.StatusWord}}</span>
      </div>
      {{if or $.Config.ShowUptime24 $.Config.ShowUptime7 $.Config.ShowUptime30}}
      <div class="card-stats">
        {{if $.Config.ShowUptime24}}<span><i>24h</i>{{.Uptime24}}</span>{{end}}
        {{if $.Config.ShowUptime7}}<span><i>7d</i>{{.Uptime7}}</span>{{end}}
        {{if $.Config.ShowUptime30}}<span><i>30d</i>{{.Uptime30}}</span>{{end}}
      </div>
      {{end}}
      {{if and $.Config.ShowHeartbeat .Beats}}{{template "beats" .}}{{end}}
      {{if $.Config.ShowLastCheck}}<div class="small muted">{{$.Labels.LastCheck}}: {{.LastCheck}}</div>{{end}}
    </article>
    {{end}}
  </div>

  {{else if eq $.Config.Layout "compact"}}
  <div class="rows">
    {{range .Sensors}}
    <div class="row">
      {{if $.Config.ShowLogos}}{{template "logo" .}}{{end}}
      <span class="row-name">{{.Name}}</span>
      <span class="pill {{.Status}}">{{.StatusWord}}</span>
      {{if $.Config.ShowUptime24}}<span class="num">{{.Uptime24}}</span>{{end}}
      {{if and $.Config.ShowHeartbeat .Beats}}{{template "beats" .}}{{end}}
    </div>
    {{end}}
  </div>

  {{else}}
  <div class="table-wrap">
    <table>
      <thead><tr>
        <th>{{$.Labels.Service}}</th>
        <th>{{$.Labels.Status}}</th>
        {{if $.Config.ShowUptime24}}<th class="num">24h</th>{{end}}
        {{if $.Config.ShowUptime7}}<th class="num hide-sm">7d</th>{{end}}
        {{if $.Config.ShowUptime30}}<th class="num hide-sm">30d</th>{{end}}
        {{if $.Config.ShowHeartbeat}}<th class="hide-sm">{{$.Labels.Checks}}</th>{{end}}
        {{if $.Config.ShowLastCheck}}<th class="hide-sm">{{$.Labels.LastCheck}}</th>{{end}}
      </tr></thead>
      <tbody>
      {{range .Sensors}}
      <tr>
        <td class="service">{{if $.Config.ShowLogos}}{{template "logo" .}}{{end}}{{.Name}}</td>
        <td><span class="pill {{.Status}}">{{.StatusWord}}</span></td>
        {{if $.Config.ShowUptime24}}<td class="num">{{.Uptime24}}</td>{{end}}
        {{if $.Config.ShowUptime7}}<td class="num hide-sm">{{.Uptime7}}</td>{{end}}
        {{if $.Config.ShowUptime30}}<td class="num hide-sm">{{.Uptime30}}</td>{{end}}
        {{if $.Config.ShowHeartbeat}}<td class="hide-sm">{{if .Beats}}{{template "beats" .}}{{end}}</td>{{end}}
        {{if $.Config.ShowLastCheck}}<td class="muted hide-sm">{{.LastCheck}}</td>{{end}}
      </tr>
      {{end}}
      </tbody>
    </table>
  </div>
  {{end}}
</section>
{{end}}
{{else}}
<p class="muted">{{.Labels.NoServices}}</p>
{{end}}

{{if and .Config.ShowIncidents .Incidents}}
<section class="group">
<h2>{{.Labels.Incidents}}</h2>
<ul class="incidents">
{{range .Incidents}}<li{{if .Ongoing}} class="ongoing"{{end}}>
  <strong>{{.Sensor}}</strong> · <span class="muted">{{.When}} · {{.State}}</span>
  {{if .Cause}}<p>{{.Cause}}</p>{{end}}
</li>
{{end}}</ul>
</section>
{{end}}

<footer class="site-foot">
  <a class="site-brand" href="https://j000k3r.github.io/probeglass-website/" target="_blank" rel="noopener noreferrer">
    <img src="/assets/favicon.svg" alt="" width="26" height="26">
    <span>{{.Labels.Footer}}</span>
  </a>
  {{if .EditURL}}<a class="site-edit" href="{{.EditURL}}">{{.Labels.Edit}}</a>{{end}}
</footer>
</main>
</body>
</html>

{{define "logo"}}{{if .IconURL}}{{if .IconDrawn}}<span class="logo logo-drawn" style="--logo:url('{{.IconURL}}')" aria-hidden="true"></span>{{else}}<img class="logo" src="{{.IconURL}}" alt="" width="18" height="18">{{end}}{{end}}{{end}}

{{define "beats"}}<span class="beats" aria-hidden="true">{{range .Beats}}<i class="beat {{.Class}}" title="{{.Title}}"></i>{{end}}</span>{{end}}
`))
