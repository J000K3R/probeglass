// Package demo holds the dataset a homelab can be shown with before it has any
// history of its own, and the code that invents that history.
//
// It lives here rather than in the seed command because three callers need the same
// data: `probeglass seed --demo`, which fills a fresh database from the shell; the
// button in Settings that puts the example data back after somebody has removed it;
// and the setup screen, which gives the four sensors it creates a week of past so
// that a brand-new dashboard is not an empty grid. A second copy of the dataset would
// be a second thing to keep in step, and the two would drift the moment a sensor was
// added. Two of the three only want the history writer, which is why it is exported.
//
// Everything it writes carries model.OriginExample, which is what the removal
// button keys on. Nothing here reads or writes a sensor the operator made.
package demo

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// Result reports what Seed did, so a caller can say it out loud.
type Result struct {
	// Sensors is how many sensors it created. Zero means everything was already
	// there, which is what a second click on the button looks like.
	Sensors int `json:"sensors"`
	// Samples is how many probe results it invented.
	Samples int `json:"samples"`
	// Sites is how many places it created. Zero on a second run, and zero for a
	// place somebody had already made: an existing site is left exactly as it is.
	Sites int `json:"sites"`
}

// site is a place the dataset puts sensors. Coordinates are real places rather
// than 0/0, because a pin in the Gulf of Guinea is the one thing on the map that
// would look like a bug.
//
// The two of them are also far enough apart to be worth a map. The map starts out
// zoomed to whatever holds every marker on it, so two places in the same country
// give a view of one region with the coastlines far off the edge — which is a fine
// map and a poor demonstration of one. Berlin and Virginia start at a zoom where
// the world is the thing being drawn.
type site struct {
	name      string
	kind      model.SiteKind
	lat, lon  float64
	sortOrder int
}

var sites = []site{
	{name: "Homelab", kind: model.SiteLocal, lat: 52.5200, lon: 13.4050, sortOrder: 1},
	{name: "Rented server", kind: model.SiteExternal, lat: 39.0438, lon: -77.4874, sortOrder: 2},
}

// sensor is one entry of the demo dataset: a target plus the shape of its
// synthetic history. The numbers are deliberately boring — the point is a
// dashboard that looks like a homelab, not a benchmark.
//
// Two rules decide what may go in this table, and both come from the fact that
// these sensors are real rows the scheduler really probes:
//
//   - An enabled entry's target has to answer from here, always. The dashboard is
//     seeded with a week of green history, and a sensor that started failing a
//     minute after it was seeded would contradict the very history printed above
//     it. This is why every enabled target below is a public service.
//   - A homelab's own addresses — a NAS, a router, a printer — are therefore
//     disabled. They are what belongs at the two sites, and they are what gives
//     the map something to draw, but 192.168.1.10 does not answer on anybody
//     else's network. Paused, they place the pins without claiming anything about
//     a network this installation cannot see. Turning one on is a click.
type sensor struct {
	name       string
	sensorType model.SensorType
	target     string
	group      string
	// site is the name of the place this sensor belongs to, empty for the ones that
	// are simply somewhere on the internet.
	site string
	// paused is for the entries a fresh installation cannot reach. See above.
	paused     bool
	tags       []string
	baseMS     float64
	jitterMS   float64
	intervalS  int
	options    model.Options
	successMsg string
	failMsg    string
	// outages are the stretches of failing probes in this entry's invented past.
	// They are measured from the end of the window, so one that starts further back
	// than the window is long writes no probes and no incident: `--days 1` keeps only
	// the most recent of them, and an outage at 210 hours is invisible to the 7-day
	// default that `seed --demo` and the Settings button both use.
	outages []Outage
}

// history is the shape of this entry's invented past, as the writer wants it. The
// table stays the one place a demo sensor is described, and the window is the
// caller's to choose — `seed --demo --days` and the Settings button want different
// lengths out of the same entries.
func (d sensor) history(window time.Duration) History {
	return History{
		Window:     window,
		BaseMS:     d.baseMS,
		JitterMS:   d.jitterMS,
		SuccessMsg: d.successMsg,
		FailMsg:    d.failMsg,
		Outages:    d.outages,
	}
}

var sensors = []sensor{
	{
		name: "Example website", sensorType: model.SensorHTTP, target: "https://example.org",
		group: "Internet", tags: []string{"web", "public"},
		baseMS: 148, jitterMS: 45, intervalS: 60,
		options:    model.Options{"expected_status": "200-299", "cert_warn_days": 14},
		successMsg: "HTTP 200 OK", failMsg: "dial tcp: i/o timeout",
		outages: []Outage{
			{Before: 61 * time.Hour, Length: 26 * time.Minute, Cause: "HTTP 503 Service Unavailable"},
			{Before: 19 * time.Hour, Length: 7 * time.Minute, Cause: "dial tcp: i/o timeout"},
		},
	},
	{
		name: "Public resolver", sensorType: model.SensorDNS, target: "one.one.one.one",
		group: "Internet", tags: []string{"dns"},
		baseMS: 24, jitterMS: 11, intervalS: 60,
		options:    model.Options{"record_type": "A"},
		successMsg: "1 A record", failMsg: "no such host",
		outages: []Outage{
			{Before: 44 * time.Hour, Length: 11 * time.Minute, Cause: "DNS: server misbehaving"},
		},
	},
	{
		name: "Internet ping", sensorType: model.SensorPing, target: "1.1.1.1",
		group: "Network", tags: []string{"latency"},
		baseMS: 11, jitterMS: 5, intervalS: 30,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []Outage{
			{Before: 30 * time.Hour, Length: 4 * time.Minute, Cause: "100% packet loss"},
		},
	},
	{
		name: "Resolver port", sensorType: model.SensorTCP, target: "1.1.1.1:53",
		group: "Network", tags: []string{"tcp"},
		baseMS: 31, jitterMS: 9, intervalS: 120,
		successMsg: "connected in 31ms", failMsg: "connection refused",
		outages: []Outage{
			{Before: 92 * time.Hour, Length: 18 * time.Minute, Cause: "connection refused"},
			{Before: 8 * time.Hour, Length: 3 * time.Minute, Cause: "connection timed out"},
		},
	},
	{
		name: "Example website TLS", sensorType: model.SensorTLS, target: "example.org:443",
		group: "Internet", tags: []string{"tls", "public"},
		baseMS: 162, jitterMS: 52, intervalS: 300,
		options:    model.Options{"server_name": "example.org", "cert_warn_days": 14},
		successMsg: "TLS 1.3 · certificate valid for 82 day(s)", failMsg: "TLS certificate expires in 6 day(s)",
		outages: []Outage{},
	},
	{
		name: "Package mirror", sensorType: model.SensorHTTP, target: "https://deb.debian.org",
		group: "Internet", tags: []string{"web", "public"},
		baseMS: 121, jitterMS: 38, intervalS: 300,
		options:    model.Options{"expected_status": "200-399", "body_contains": "Debian"},
		successMsg: "HTTP 200 OK", failMsg: "body does not contain \"Debian\"",
		outages: []Outage{
			{Before: 128 * time.Hour, Length: 12 * time.Minute, Cause: "HTTP 502 Bad Gateway"},
		},
	},
	{
		name: "DNS over TLS", sensorType: model.SensorTLS, target: "1.1.1.1:853",
		group: "Internet", tags: []string{"tls", "dns"},
		baseMS: 44, jitterMS: 14, intervalS: 600,
		options:    model.Options{"server_name": "cloudflare-dns.com", "cert_warn_days": 21},
		successMsg: "TLS 1.3 · certificate valid for 61 day(s)", failMsg: "TLS handshake failed",
		outages: []Outage{},
	},

	// The homelab itself. Disabled — see the note on the sensor type.
	{
		name: "Router: ping", sensorType: model.SensorPing, target: "192.168.1.1",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"latency"},
		baseMS: 2, jitterMS: 1, intervalS: 30,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []Outage{
			{Before: 71 * time.Hour, Length: 9 * time.Minute, Cause: "100% packet loss"},
		},
	},
	{
		name: "NAS: SSH", sensorType: model.SensorTCP, target: "192.168.1.10:22",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"ssh", "storage"},
		baseMS: 4, jitterMS: 2, intervalS: 120,
		successMsg: "connected in 4ms", failMsg: "connection refused",
		outages: []Outage{
			{Before: 150 * time.Hour, Length: 42 * time.Minute, Cause: "connection refused"},
		},
	},
	{
		name: "NAS: web interface", sensorType: model.SensorHTTP, target: "http://192.168.1.10",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"web", "storage"},
		baseMS: 38, jitterMS: 12, intervalS: 300,
		options:    model.Options{"expected_status": "200-399"},
		successMsg: "HTTP 200 OK", failMsg: "dial tcp: i/o timeout",
		outages: []Outage{},
	},
	{
		name: "Printer: ink level", sensorType: model.SensorSNMP, target: "192.168.1.20",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"snmp"},
		baseMS: 17, jitterMS: 6, intervalS: 900,
		options: model.Options{
			"oid":       "1.3.6.1.2.1.43.11.1.1.9.1.1",
			"community": "public",
			"min":       10,
		},
		successMsg: "42", failMsg: "no such object",
		outages: []Outage{
			{Before: 160 * time.Hour, Length: 60 * time.Minute, Cause: "request timeout"},
		},
	},

	// A rented box somewhere else, which is the other reason to put sensors on a map.
	{
		name: "Rented server: SSH", sensorType: model.SensorTCP, target: "vps.example.net:22",
		group: "Rented server", site: "Rented server", paused: true, tags: []string{"ssh"},
		baseMS: 26, jitterMS: 8, intervalS: 120,
		successMsg: "connected in 26ms", failMsg: "no such host",
		outages: []Outage{
			{Before: 36 * time.Hour, Length: 15 * time.Minute, Cause: "connection timed out"},
		},
	},
	{
		name: "Rented server: web", sensorType: model.SensorHTTP, target: "https://vps.example.net",
		group: "Rented server", site: "Rented server", paused: true, tags: []string{"web"},
		baseMS: 174, jitterMS: 44, intervalS: 300,
		options:    model.Options{"expected_status": "200-399", "cert_warn_days": 21},
		successMsg: "HTTP 200 OK", failMsg: "no such host",
		outages: []Outage{},
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
		Enabled:  !d.paused,
		Group:    d.group,
		Site:     d.site,
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

	// The places come first. sensors.site is a foreign key, so a sensor pointing at
	// a site that does not exist yet is refused by the database rather than
	// silently stored without one.
	made, err := ensureSites(ctx, st)
	if err != nil {
		return Result{}, err
	}

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
		n, err := Write(ctx, st, c, d.history(time.Duration(days)*24*time.Hour))
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
	return Result{Sensors: len(created), Samples: samples, Sites: made}, nil
}

// ensureSites creates the places the dataset needs and returns how many it made.
//
// A site that is already there is left exactly as it is, and that is the whole
// contract: somebody who has moved the pin to where their house actually is, or
// renamed the place, must not have it moved back by a button that says it restores
// example data. The sensors then point at their site, which is the right outcome —
// the example sensor is on the operator's own map.
//
// A place it creates carries the example marker like the sensors do, so that
// removing the example data takes it along instead of leaving an empty pin behind.
func ensureSites(ctx context.Context, st *store.Store) (int, error) {
	made := 0
	for _, d := range sites {
		_, err := st.GetSite(ctx, d.name)
		switch {
		case err == nil:
			continue
		case !errors.Is(err, store.ErrNotFound):
			return made, err
		}
		lat, lon := d.lat, d.lon
		s := model.Site{
			Name:      d.name,
			Kind:      d.kind,
			Lat:       &lat,
			Lon:       &lon,
			SortOrder: d.sortOrder,
			Origin:    model.OriginExample,
		}
		if err := st.CreateSite(ctx, &s); err != nil {
			return made, err
		}
		made++
	}
	return made, nil
}

// Spacing is how far apart the invented probes are. Five minutes keeps a week of
// history small while still giving the charts something honest to aggregate.
const Spacing = 5 * time.Minute

// Outage is one stretch of failing probes in a sensor's invented past.
type Outage struct {
	// Before is how long before the end of the window the outage began.
	Before time.Duration
	// Length is how long it lasted. It decides how many probes fail, not what the
	// incident log says the duration was — that comes from the probes themselves.
	Length time.Duration
	// Cause is the message the failing probes carry, and the one the incident opens
	// with.
	Cause string
}

// History is the past a sensor is given before it has one of its own.
//
// It is a description rather than a recording, and a small one. What it takes to
// make a dashboard look like a homelab is a latency, some texture around it, and
// the two ways a probe can fail: a stretch of them, which is an outage, and a
// single one, which is all it takes for a sensor to read flaky.
type History struct {
	// Window is how far back the probes go. Nothing is written before it, and an
	// outage that ends before it is dropped rather than written as an incident with
	// no failing probe under it.
	Window time.Duration
	// BaseMS is the typical latency, and JitterMS how far a probe wanders around it.
	// An hourly drift is added on top, so the min/max band on a chart has something
	// to show.
	BaseMS, JitterMS float64
	// SuccessMsg and FailMsg are the messages the two kinds of probe carry.
	SuccessMsg, FailMsg string
	// Outages are the stretches of failing probes.
	Outages []Outage
	// Blips are single failing probes, each this long before the end of the window.
	// A blip writes no incident: an incident log with one-minute outages in it is
	// noise, and this is the one probe that is exactly what the yellow status means.
	//
	// Each is moved to the nearest probe, so the useful values are whole multiples
	// of Spacing — including Spacing itself, the last probe in the window, which is
	// what leaves a sensor flaky on a dashboard that has not yet probed it once.
	Blips []time.Duration
}

// Write invents history for one sensor and records the incidents that go with it.
// It returns how many probes it wrote.
//
// The generator is deterministic apart from the clock: the same sensor id produces
// the same series, so the same command produces the same dashboard, which is what
// makes it usable in screenshots and tests. Probes are spaced Spacing apart and end
// at the most recent whole minute.
//
// Every incident it writes is backed by the probes under it — it opens on the first
// probe that failed and closes on the one that found the service back — so the
// incident log and the uptime bars cannot tell two different stories. That is also
// why a window too short to contain an outage writes no incident for it instead of
// writing one that points at a time before its own history begins.
func Write(ctx context.Context, st *store.Store, c model.Sensor, h History) (int, error) {
	probes := int(h.Window/Spacing) - 1
	if probes < 1 {
		return 0, nil
	}
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-time.Duration(probes) * Spacing)
	rng := rand.New(rand.NewSource(1000 + c.ID)) //nolint:gosec // demo data, not security

	// An outage is an interval of probes that must fail. One that ends before the
	// window opens is dropped here: it is the case `seed --demo --days 1` used to get
	// wrong, writing incidents for outages three days old with no failing probe
	// anywhere near them.
	type window struct {
		from, to time.Time
		cause    string
	}
	windows := make([]window, 0, len(h.Outages))
	for _, o := range h.Outages {
		from := now.Add(-o.Before)
		to := from.Add(o.Length)
		if !to.After(start) {
			continue
		}
		windows = append(windows, window{from: from, to: to, cause: o.Cause})
	}

	// A blip is looked up by its distance from the end of the window, which is a
	// whole number of spacings for every probe in the series — so a blip placed
	// between two probes lands on the nearer one.
	oldest := time.Duration(probes) * Spacing
	blips := make(map[time.Duration]bool, len(h.Blips))
	for _, b := range h.Blips {
		if b < Spacing || b > oldest {
			continue
		}
		blips[b.Round(Spacing)] = true
	}

	count := 0
	err := st.InTx(ctx, func(tx *store.Tx) error {
		// The first and last probe that really failed inside each outage. The
		// incident is written from these and not from the interval, because the
		// interval is a wish and these are the rows the dashboard can see.
		first := make([]time.Time, len(windows))
		last := make([]time.Time, len(windows))

		// The newest probe is one spacing old and the oldest is probes spacings back.
		// That is deliberately one fewer than the window would hold: a week of history
		// written into a week of raw retention puts its very first probe exactly on the
		// cutoff, where the next maintenance pass prunes it — and writing rows whose
		// only certain future is deletion is silly.
		for i := 0; i < probes; i++ {
			ts := now.Add(-time.Duration(probes-i) * Spacing)

			in := -1
			for j, w := range windows {
				if !ts.Before(w.from) && ts.Before(w.to) {
					in = j
					break
				}
			}
			blip := in < 0 && blips[now.Sub(ts)]

			// A slow drift plus jitter: enough texture that the min/max band has
			// something to show.
			drift := 12 * (1 + float64(ts.Unix()%3600)/3600)
			latency := h.BaseMS + drift + rng.NormFloat64()*h.JitterMS
			if latency < 1 {
				latency = 1
			}

			res := model.Result{
				SensorID:  c.ID,
				TS:        ts,
				Success:   in < 0 && !blip,
				LatencyMS: round2(latency),
				Message:   h.SuccessMsg,
			}
			switch {
			case in >= 0:
				res.Success = false
				res.LatencyMS = 0
				res.Message = windows[in].cause
				res.StatusCode = 0
				if first[in].IsZero() {
					first[in] = ts
				}
				last[in] = ts
			case blip:
				res.Success = false
				res.LatencyMS = 0
				res.Message = h.FailMsg
				res.StatusCode = 0
			case c.Type == model.SensorHTTP:
				res.StatusCode = 200
			}
			if err := tx.InsertResult(ctx, &res); err != nil {
				return err
			}
			count++
		}

		for j := range windows {
			if first[j].IsZero() {
				// No probe in this outage failed, so there is nothing to report. An
				// incident here would be a claim about a moment this history does not
				// cover.
				continue
			}
			// The outage ended where a probe found the service back, which is where
			// the probe loop itself would have closed it. If the failures run to the
			// end of the window there is no such probe, and the last failure is as
			// far as this history can honestly see.
			to := last[j].Add(Spacing)
			if !to.Before(now) {
				to = last[j]
			}
			id, err := tx.OpenIncident(ctx, c.ID, first[j], windows[j].cause)
			if err != nil {
				return err
			}
			if err := tx.CloseIncident(ctx, id, to); err != nil {
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
