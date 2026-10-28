// Package status builds the read-only page a homelab shares with the people who
// use it rather than operate it.
//
// It is deliberately not the dashboard. The dashboard draws everything because
// its reader is the administrator; this page draws only what was ticked in
// Settings, in the shape the operator chose, carries no target, no option and no
// credential, and is rendered by the server so that sharing it needs no account
// and no JavaScript.
package status

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/scheduler"
	"github.com/j000k3r/probeglass/internal/store"
)

// Uptime is one window's figure for one sensor.
type Uptime struct {
	Percent float64
	// Known is false when the window holds no probes at all — a sensor added an
	// hour ago has a 30-day figure that is not 100%, it is absent. Drawing the
	// difference is the whole point.
	Known bool
}

// Beat is one recent probe, as the heartbeat strip draws it.
type Beat struct {
	Up      bool
	Excused bool
	When    time.Time
}

// Sensor is one published service, as the page draws it.
type Sensor struct {
	Name      string
	Status    model.Status
	Uptime24  Uptime
	Uptime7   Uptime
	Uptime30  Uptime
	LastCheck time.Time
	// IconURL is where the symbol is fetched from, and IconDrawn says whether it
	// is one of the monochrome drawings (drawn with a mask, so it follows the
	// theme) rather than a colour logo (an image as it is).
	IconURL   string
	IconDrawn bool
	Heartbeat []Beat
}

// SectionView is one heading of the rendered page and the services under it.
type SectionView struct {
	Title   string
	Colour  string
	Sensors []Sensor
}

// Incident is one outage on a published service.
type Incident struct {
	Sensor   string
	Started  time.Time
	Resolved *time.Time
	Ongoing  bool
	// Cause is the probe's own words, and is empty unless the installation chose
	// to publish them.
	Cause string
}

// NoticeView is one message at the top of the page, with its symbol resolved.
type NoticeView struct {
	Text      string
	Colour    string
	IconURL   string
	IconDrawn bool
}

// Page is everything one request to the public page needs.
type Page struct {
	Config     Config
	Sections   []SectionView
	Notices    []NoticeView
	Sensors    []Sensor
	Incidents  []Incident
	ShowCauses bool
	// EditURL is the dashboard's status panel, set only when the reader is a
	// signed-in administrator. It is the one link that does not lead outward.
	EditURL string
	Updated time.Time
}

// Overall is the one word the top of the page says: "up" when everything is
// fine, "warn" when something is degraded, "down" when something is out.
//
// Disabled and not-yet-probed sensors are neither: a paused service is not a
// fault, and an unknown one is not a claim.
func (p Page) Overall() model.Status {
	down, warn, up := false, false, false
	for _, s := range p.Sensors {
		switch s.Status {
		case model.StatusDown:
			down = true
		case model.StatusWarn:
			warn = true
		case model.StatusUp:
			up = true
		case model.StatusPaused, model.StatusUnknown:
			// Neither a claim nor a fault; a paused service is not down, and one
			// that has never been probed is not up.
		}
	}
	switch {
	case down:
		return model.StatusDown
	case warn:
		return model.StatusWarn
	case up:
		return model.StatusUp
	default:
		// Nothing is published, or everything on the page is paused or has not
		// been probed yet. "Operational" would be a claim about services the page
		// is not watching.
		return model.StatusUnknown
	}
}

// HeartbeatProbes is how many recent checks the strip holds. Thirty is about one
// line on a phone at the smallest the page is read.
const HeartbeatProbes = 30

// Build assembles the page from the database, the stored configuration and the
// scheduler's live state.
func Build(ctx context.Context, st *store.Store, snaps map[int64]scheduler.Snapshot, now time.Time, iconBase string) (Page, error) {
	cfg := LoadConfig(ctx, st)
	page := Page{
		Config:     cfg,
		Sections:   []SectionView{},
		Notices:    []NoticeView{},
		Sensors:    []Sensor{},
		Incidents:  []Incident{},
		ShowCauses: cfg.ShowCauses,
		Updated:    now,
	}

	for _, notice := range cfg.Notices {
		iconURL, drawn := iconOf(notice.Icon, iconBase)
		page.Notices = append(page.Notices, NoticeView{
			Text: notice.Text, Colour: notice.Colour, IconURL: iconURL, IconDrawn: drawn,
		})
	}

	ids := cfg.SensorIDs()
	if len(ids) == 0 {
		return page, nil
	}
	published := make(map[int64]bool, len(ids))
	for _, id := range ids {
		published[id] = true
	}

	sensors, err := st.ListSensors(ctx)
	if err != nil {
		return Page{}, err
	}
	byID := make(map[int64]model.Sensor, len(sensors))
	for _, c := range sensors {
		byID[c.ID] = c
	}

	// Uptime for the three windows, one aggregation each for the whole table.
	windows := []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
	uptimes := make([]map[int64]store.UptimeStats, len(windows))
	for i, w := range windows {
		stats, err := st.UptimeBySensor(ctx, now.Add(-w), now)
		if err != nil {
			return Page{}, err
		}
		uptimes[i] = stats
	}

	// The heartbeat is fetched only when the page draws it, one indexed query per
	// published sensor.
	heartbeats := map[int64][]Beat{}
	if cfg.ShowHeartbeat {
		for _, id := range ids {
			results, err := st.RecentResults(ctx, id, HeartbeatProbes)
			if err != nil {
				return Page{}, err
			}
			beats := make([]Beat, 0, len(results))
			for _, r := range results {
				beats = append(beats, Beat{Up: r.Success, Excused: r.Excused, When: r.TS})
			}
			heartbeats[id] = beats
		}
	}

	for _, section := range cfg.Sections {
		view := SectionView{Title: section.Title, Colour: section.Colour}
		for _, entry := range section.Sensors {
			sensor, ok := byID[entry.ID]
			if !ok {
				continue // deleted since the page was configured
			}
			row := Sensor{
				Name:      entry.Label,
				Uptime24:  uptimeOf(uptimes[0][entry.ID]),
				Uptime7:   uptimeOf(uptimes[1][entry.ID]),
				Uptime30:  uptimeOf(uptimes[2][entry.ID]),
				Heartbeat: heartbeats[entry.ID],
			}
			if row.Name == "" {
				row.Name = sensor.Name
			}
			icon := entry.Icon
			if icon == "" {
				icon = sensor.Icon
			}
			row.IconURL, row.IconDrawn = iconOf(icon, iconBase)
			row.Status = snaps[entry.ID].Status
			settleStatus(ctx, st, &row, sensor, snaps[entry.ID])
			view.Sensors = append(view.Sensors, row)
			page.Sensors = append(page.Sensors, row)
		}
		if len(view.Sensors) > 0 {
			page.Sections = append(page.Sections, view)
		}
	}

	incidents, err := st.ListIncidentsWithNames(ctx, store.IncidentFilter{
		OnlyOpen: true,
		Limit:    500,
	})
	if err != nil {
		return Page{}, err
	}
	resolved, err := st.ListIncidentsWithNames(ctx, store.IncidentFilter{
		OnlyResolved: true,
		Since:        now.Add(-time.Duration(cfg.IncidentDays) * 24 * time.Hour),
		Limit:        500,
	})
	if err != nil {
		return Page{}, err
	}
	incidents = append(incidents, resolved...)
	for _, inc := range incidents {
		if !published[inc.SensorID] {
			continue
		}
		row := Incident{
			Sensor:   inc.SensorName,
			Started:  inc.StartedAt,
			Resolved: inc.ResolvedAt,
			Ongoing:  inc.ResolvedAt == nil,
		}
		if cfg.ShowCauses {
			row.Cause = inc.Cause
		}
		page.Incidents = append(page.Incidents, row)
	}
	sort.SliceStable(page.Incidents, func(i, j int) bool {
		a, b := page.Incidents[i], page.Incidents[j]
		if a.Ongoing != b.Ongoing {
			return a.Ongoing
		}
		return a.Started.After(b.Started)
	})
	// A status page with hundreds of lines of history is a log, and nobody reads
	// a log on the page they check from their phone.
	if len(page.Incidents) > 50 {
		page.Incidents = page.Incidents[:50]
	}

	return page, nil
}

// settleStatus finishes a row's status: a disabled sensor is paused, and a
// missing scheduler state falls back to the last probe on disk.
//
// The fallback is one indexed lookup per affected sensor, and only in the
// seconds after a restart when the scheduler has not reconciled yet.
func settleStatus(ctx context.Context, st *store.Store, row *Sensor, sensor model.Sensor, snap scheduler.Snapshot) {
	if snap.LastResult != nil {
		row.LastCheck = snap.LastResult.TS
	}
	if !sensor.Enabled {
		row.Status = model.StatusPaused
		return
	}
	if row.Status != "" && row.Status != model.StatusUnknown {
		return
	}
	row.Status = model.StatusUnknown
	last, err := st.LastResult(ctx, sensor.ID)
	switch {
	case errors.Is(err, sql.ErrNoRows) || last == nil:
		// Never probed: unknown is the answer.
	case err != nil:
		return
	default:
		row.LastCheck = last.TS
		// A success is a success; a failure is not yet an outage, because the
		// retry count has not been applied. Saying "unknown" for that moment is
		// the honest answer and never turns a blip red in public.
		if last.Success {
			row.Status = model.StatusUp
		}
	}
}

func uptimeOf(s store.UptimeStats) Uptime {
	return Uptime{Percent: s.Percent, Known: s.Total > 0}
}

// iconOf turns a stored symbol reference into a URL the public page can fetch.
//
// Built-in drawings and brand marks live in the embedded assets, which are served
// without a session. An uploaded image lives in the database behind a viewer
// route, so the page reaches it through its own token: iconBase is the status
// page's URL, and the handler serves the bytes only while the token is valid and
// the image belongs to a sensor on the page.
func iconOf(ref, iconBase string) (url string, drawn bool) {
	switch {
	case strings.HasPrefix(ref, "upload:"):
		id := strings.TrimPrefix(ref, "upload:")
		if !plainDigits(id) || iconBase == "" {
			return "", false
		}
		return iconBase + "/icon/" + id, false
	case strings.HasPrefix(ref, "builtin:"):
		name := strings.TrimPrefix(ref, "builtin:")
		if !plainName(name) {
			return "", false
		}
		return "/assets/icons/" + name + ".svg", true
	case strings.HasPrefix(ref, "brand:"):
		name := strings.TrimPrefix(ref, "brand:")
		if !plainName(name) {
			return "", false
		}
		ext := "svg"
		switch name {
		case "mos", "unifi-protect", "esphome":
			ext = "png"
		}
		return "/assets/brands/" + name + "." + ext, false
	}
	return "", false
}

// plainDigits accepts an icon row id, and nothing that could become a path.
func plainDigits(s string) bool {
	if s == "" || len(s) > 18 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// plainName accepts the names the asset folders actually hold, so a stored
// reference cannot become a path to something else.
func plainName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}
