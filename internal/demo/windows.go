package demo

import (
	"context"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// The maintenance windows the dataset puts on the dashboard.
//
// The demo is what the documentation's pictures and the website's demo are made
// from, and a feature that appears in none of those is a feature nobody discovers.
// Windows are the hardest of the set to show, because one that starts next Sunday
// is invisible: the dashboard carries no badge for a window that is not in force.
// So the dataset writes two — one in force at the moment it is seeded, which is
// what puts "maintenance" on a card, and one weekly, which is what the settings
// panel lists beside the form that made it.
type window struct {
	// sensor names one of the dataset's sensors, empty when the window is for the
	// whole of group.
	sensor string
	group  string
	// before and length place a one-off window relative to the seed: it begins
	// before, and ends length after that. Measured rather than written down as a
	// date, so that the demo is in force whenever somebody loads it.
	before time.Duration
	length time.Duration
	// weekdays, startMin and endMin are a weekly window, read on the clock of
	// timezone.
	weekdays []int
	startMin int
	endMin   int
	timezone string
	note     string
}

// The two windows. The note is the identity: it is what tells a second seed that
// this window is already there, and it is also the sentence a reader sees in the
// settings list, so it says what the window is for rather than that it is example
// data — the "(example)" in the sensor names already says where it came from.
var windows = []window{
	{
		// In force now: two hours in, ten to go. This is the one the card shows.
		sensor: "Example website", before: 2 * time.Hour, length: 12 * time.Hour,
		note: "Certificate renewal",
	},
	{
		// Next Sunday, 02:00 to 04:00 in Berlin — the zone the dataset's places are
		// near, and a time nobody would choose for anything they wanted to watch.
		group: "Homelab", weekdays: []int{0}, startMin: 2 * 60, endMin: 4 * 60,
		timezone: "Europe/Berlin", note: "Weekly backup window",
	},
}

// ensureWindows writes the two windows that are not already there, and reports how
// many it made.
//
// A window is recognised by its note. That is enough here and it is not a guess: the
// dataset owns its own notes, and a second `seed --demo` finding "Certificate
// renewal" already present is finding the one it wrote.
func ensureWindows(ctx context.Context, st *store.Store) (int, error) {
	existing, err := st.ListWindows(ctx)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, w := range existing {
		known[w.Note] = true
	}

	now := time.Now().UTC().Truncate(time.Minute)
	made := 0
	for _, d := range windows {
		if known[d.note] {
			continue
		}
		w := model.MaintenanceWindow{
			Kind:    model.WindowWeekly,
			Note:    d.note,
			Enabled: true,
			Origin:  model.OriginExample,
		}
		if d.sensor != "" {
			c, err := st.GetSensorByName(ctx, d.sensor)
			if err != nil {
				return made, err
			}
			start := now.Add(-d.before)
			end := start.Add(d.length)
			w.Kind = model.WindowOnce
			w.SensorID = c.ID
			w.StartsAt = &start
			w.EndsAt = &end
		} else {
			w.Group = d.group
			w.Weekdays = d.weekdays
			w.StartMin = d.startMin
			w.EndMin = d.endMin
			w.Timezone = d.timezone
		}
		if err := st.CreateWindow(ctx, &w); err != nil {
			return made, err
		}
		made++
	}
	return made, nil
}
