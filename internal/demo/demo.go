// Package demo holds the dataset a homelab can be shown with before it has any
// history of its own, and the code that invents that history.
//
// It lives here rather than in the seed command because two callers need the same
// data: `weatherglass seed --demo`, which fills a fresh database from the shell,
// and the button in Settings that puts the example data back after somebody has
// removed it. A second copy of the dataset would be a second thing to keep in
// step, and the two would drift the moment a sensor was added.
//
// Everything it writes carries model.OriginExample, which is what the removal
// button keys on. Nothing here reads or writes a sensor the operator made.
package demo

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
	"github.com/j000k3r/weatherglass/internal/store"
)

// Result reports what Seed did, so a caller can say it out loud.
type Result struct {
	// Sensors is how many sensors it created. Zero means everything was already
	// there, which is what a second click on the button looks like.
	Sensors int `json:"sensors"`
	// Samples is how many probe results it invented.
	Samples int `json:"samples"`
}

// sensor is one entry of the demo dataset: a real, reachable target plus the
// shape of its synthetic history. The numbers are deliberately boring — the
// point is a dashboard that looks like a homelab, not a benchmark.
type sensor struct {
	name       string
	sensorType model.SensorType
	target     string
	group      string
	tags       []string
	baseMS     float64
	jitterMS   float64
	intervalS  int
	options    model.Options
	successMsg string
	failMsg    string
	// outages are offsets from "now", in hours, and how long they lasted.
	outages []outage
}

type outage struct {
	startHoursAgo float64
	minutes       int
	cause         string
}

var sensors = []sensor{
	{
		name: "Example website", sensorType: model.SensorHTTP, target: "https://example.org",
		group: "Internet", tags: []string{"web", "public"},
		baseMS: 148, jitterMS: 45, intervalS: 60,
		options:    model.Options{"expected_status": "200-299", "cert_warn_days": 14},
		successMsg: "HTTP 200 OK", failMsg: "dial tcp: i/o timeout",
		outages: []outage{
			{startHoursAgo: 61, minutes: 26, cause: "HTTP 503 Service Unavailable"},
			{startHoursAgo: 19, minutes: 7, cause: "dial tcp: i/o timeout"},
		},
	},
	{
		name: "Public resolver", sensorType: model.SensorDNS, target: "one.one.one.one",
		group: "Internet", tags: []string{"dns"},
		baseMS: 24, jitterMS: 11, intervalS: 60,
		options:    model.Options{"record_type": "A"},
		successMsg: "1 A record", failMsg: "no such host",
		outages: []outage{
			{startHoursAgo: 44, minutes: 11, cause: "DNS: server misbehaving"},
		},
	},
	{
		name: "Gateway ping", sensorType: model.SensorPing, target: "1.1.1.1",
		group: "Network", tags: []string{"latency"},
		baseMS: 11, jitterMS: 5, intervalS: 30,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []outage{
			{startHoursAgo: 30, minutes: 4, cause: "100% packet loss"},
		},
	},
	{
		name: "Resolver port", sensorType: model.SensorTCP, target: "1.1.1.1:53",
		group: "Network", tags: []string{"tcp"},
		baseMS: 31, jitterMS: 9, intervalS: 120,
		successMsg: "connected in 31ms", failMsg: "connection refused",
		outages: []outage{
			{startHoursAgo: 92, minutes: 18, cause: "connection refused"},
			{startHoursAgo: 8, minutes: 3, cause: "connection timed out"},
		},
	},
	{
		name: "Example website TLS", sensorType: model.SensorTLS, target: "example.org:443",
		group: "Internet", tags: []string{"tls", "public"},
		baseMS: 162, jitterMS: 52, intervalS: 300,
		options:    model.Options{"server_name": "example.org", "cert_warn_days": 14},
		successMsg: "TLS 1.3 · certificate valid for 82 day(s)", failMsg: "TLS certificate expires in 6 day(s)",
		outages: []outage{},
	},
}

// Sensors returns the dataset as sensors ready to be stored.
//
// Every one is marked as example data, and the options map is copied rather than
// handed out: the package-level table must not be something a caller can edit
// through the value it was given.
func Sensors() []model.Sensor {
	out := make([]model.Sensor, 0, len(sensors))
	for _, d := range sensors {
		out = append(out, d.model())
	}
	return out
}

func (d sensor) model() model.Sensor {
	opts := model.Options{}
	for k, v := range d.options {
		opts[k] = v
	}
	return model.Sensor{
		Name:     d.name,
		Type:     d.sensorType,
		Target:   d.target,
		Interval: d.intervalS,
		Timeout:  10,
		Retries:  1,
		Enabled:  true,
		Group:    d.group,
		Tags:     d.tags,
		Options:  opts,
		// The same marker the setup screen writes. Anything invented — here or
		// there — can be taken out again in one click from Settings, and the marker
		// is the only honest way to know which those are: the name and the group
		// are both things a person may edit.
		Origin: model.OriginExample,
	}
}

// Seed creates the demo sensors that are missing, gives each new one a synthetic
// history, and folds that history into the rollups.
//
// A name that is already in the table is left alone, so running it twice changes
// nothing: the second run creates no sensors, invents no probes and reports zero.
// That is what makes the Settings button safe to press again.
func Seed(ctx context.Context, st *store.Store, days int) (Result, error) {
	now := time.Now().UTC().Truncate(time.Minute)

	created, err := st.CreateMissingSensors(ctx, Sensors())
	if err != nil {
		return Result{}, err
	}

	byName := make(map[string]sensor, len(sensors))
	for _, d := range sensors {
		byName[d.name] = d
	}

	samples := 0
	for _, c := range created {
		d, ok := byName[c.Name]
		if !ok {
			continue
		}
		n, err := synthesise(ctx, st, c, d, now, days)
		if err != nil {
			return Result{}, err
		}
		samples += n
	}

	// Fold the new rows into the rollup tables so the longer ranges have data
	// immediately, instead of waiting for the next maintenance pass.
	if samples > 0 {
		if err := st.Maintain(ctx, now); err != nil {
			return Result{}, fmt.Errorf("roll up seeded history: %w", err)
		}
	}
	return Result{Sensors: len(created), Samples: samples}, nil
}

// synthesise writes probe history for one sensor and records the matching
// incidents. The generator is deterministic: the same command produces the same
// dashboard, which makes it usable in screenshots and tests.
func synthesise(ctx context.Context, st *store.Store, c model.Sensor, d sensor, now time.Time, days int) (int, error) {
	// Five-minute spacing keeps a week of history small while still giving the
	// charts something honest to aggregate.
	const spacing = 5 * time.Minute
	start := now.Add(-time.Duration(days) * 24 * time.Hour)
	rng := rand.New(rand.NewSource(1000 + c.ID)) //nolint:gosec // demo data, not security

	// An outage window is a set of intervals that must fail.
	type window struct{ from, to time.Time }
	windows := make([]window, 0, len(d.outages))
	for _, o := range d.outages {
		from := now.Add(-time.Duration(o.startHoursAgo * float64(time.Hour)))
		windows = append(windows, window{from: from, to: from.Add(time.Duration(o.minutes) * time.Minute)})
	}

	count := 0
	err := st.InTx(ctx, func(tx *store.Tx) error {
		for ts := start; ts.Before(now); ts = ts.Add(spacing) {
			failing := false
			for _, w := range windows {
				if !ts.Before(w.from) && ts.Before(w.to) {
					failing = true
					break
				}
			}
			// A slow drift plus jitter: enough texture that the min/max band has
			// something to show.
			drift := 12 * (1 + float64(ts.Unix()%3600)/3600)
			latency := d.baseMS + drift + rng.NormFloat64()*d.jitterMS
			if latency < 1 {
				latency = 1
			}

			res := model.Result{
				SensorID:  c.ID,
				TS:        ts,
				Success:   !failing,
				LatencyMS: round2(latency),
				Message:   d.successMsg,
			}
			if failing {
				res.Success = false
				res.LatencyMS = 0
				res.Message = d.failMsg
				res.StatusCode = 0
			} else if c.Type == model.SensorHTTP {
				res.StatusCode = 200
			}
			if err := tx.InsertResult(ctx, &res); err != nil {
				return err
			}
			count++
		}

		// Incidents are written to match the synthetic outages, so the incident
		// log and the uptime bars tell the same story.
		for _, w := range windows {
			id, err := tx.OpenIncident(ctx, c.ID, w.from, d.failMsg)
			if err != nil {
				return err
			}
			if err := tx.CloseIncident(ctx, id, w.to); err != nil {
				return err
			}
		}
		return nil
	})
	return count, err
}

// round2 keeps latencies at two decimals, matching what the probe code stores.
func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
