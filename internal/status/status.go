// Package status builds the read-only page a homelab shares with the people who
// use it rather than operate it.
//
// It is deliberately not the dashboard. The dashboard draws everything because
// its reader is the administrator; this page draws only what was ticked in
// Settings, carries no target, no option and no credential, and is rendered by
// the server so that sharing it needs no account and no JavaScript.
package status

import (
	"context"
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

// Sensor is one published service, as the page draws it.
type Sensor struct {
	Name      string
	Status    model.Status
	Uptime24  Uptime
	Uptime7   Uptime
	Uptime30  Uptime
	LastCheck time.Time
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

// Page is everything one request to the public page needs.
type Page struct {
	Sensors    []Sensor
	Incidents  []Incident
	ShowCauses bool
	Updated    time.Time
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

// IncidentWindow is how far back the page looks for outages. Thirty days is
// what somebody deciding whether the service is reliable wants to see; the
// dashboard's own log goes back forever.
const IncidentWindow = 30 * 24 * time.Hour

// Build assembles the page from the database and the scheduler's live state.
func Build(ctx context.Context, st *store.Store, snaps map[int64]scheduler.Snapshot, now time.Time) (Page, error) {
	page := Page{Sensors: []Sensor{}, Incidents: []Incident{}, Updated: now}

	ids, err := st.StatusSensors(ctx)
	if err != nil {
		return Page{}, err
	}
	_, page.ShowCauses = st.StatusPageSettings(ctx)
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

	// The scheduler's live state is the first answer for a status. It is not the
	// only one: a page can be asked for before the scheduler has reconciled after
	// a restart, and the last stored probe is still a fact about the service.
	lastResults, err := st.LastResults(ctx, 1)
	if err != nil {
		return Page{}, err
	}

	windows := []time.Duration{24 * time.Hour, 7 * 24 * time.Hour, 30 * 24 * time.Hour}
	uptimes := make([]map[int64]store.UptimeStats, len(windows))
	for i, w := range windows {
		stats, err := st.UptimeBySensor(ctx, now.Add(-w), now)
		if err != nil {
			return Page{}, err
		}
		uptimes[i] = stats
	}

	for _, id := range ids {
		sensor, ok := byID[id]
		if !ok {
			continue // deleted between the two queries; the join would hide it
		}
		row := Sensor{
			Name:   sensor.Name,
			Status: snaps[id].Status,
		}
		if !sensor.Enabled {
			row.Status = model.StatusPaused
		} else if row.Status == "" || row.Status == model.StatusUnknown {
			// No state yet: fall back to the last probe on disk.
			row.Status = model.StatusUnknown
			if results := lastResults[id]; len(results) > 0 {
				last := results[len(results)-1]
				row.LastCheck = last.TS
				// A success is a success; a failure is not yet an outage, because
				// the retry count has not been applied. Saying "unknown" for that
				// moment is the honest answer and never turns a blip red in public.
				if last.Success {
					row.Status = model.StatusUp
				}
			}
		}
		if snap, ok := snaps[id]; ok && snap.LastResult != nil {
			row.LastCheck = snap.LastResult.TS
		}
		row.Uptime24 = uptimeOf(uptimes[0][id])
		row.Uptime7 = uptimeOf(uptimes[1][id])
		row.Uptime30 = uptimeOf(uptimes[2][id])
		page.Sensors = append(page.Sensors, row)
	}

	// The page is a list of services, read once; alphabetical order is the one
	// an outsider can predict without knowing how the homelab is grouped.
	sort.SliceStable(page.Sensors, func(i, j int) bool {
		return lessFold(page.Sensors[i].Name, page.Sensors[j].Name)
	})

	incidents, err := st.ListIncidentsWithNames(ctx, store.IncidentFilter{
		OnlyOpen: true,
		Limit:    200,
	})
	if err != nil {
		return Page{}, err
	}
	resolved, err := st.ListIncidentsWithNames(ctx, store.IncidentFilter{
		OnlyResolved: true,
		Since:        now.Add(-IncidentWindow),
		Limit:        200,
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
		if page.ShowCauses {
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
	// A status page with two hundred lines of history is a log, and nobody reads
	// a log on the page they check from their phone. The dashboard is where the
	// whole list lives.
	if len(page.Incidents) > 50 {
		page.Incidents = page.Incidents[:50]
	}

	return page, nil
}

func uptimeOf(s store.UptimeStats) Uptime {
	return Uptime{Percent: s.Percent, Known: s.Total > 0}
}

// lessFold compares names case-insensitively, which is what a reader expects
// from "alphabetical" even though SQL would not.
func lessFold(a, b string) bool {
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la == lb {
		return a < b
	}
	return la < lb
}
