// Package demo holds the dataset a homelab can be shown with before it has any
// history of its own, and the code that invents that history.
//
// It lives here rather than in the seed command because three callers need the same
// data: `probeglass seed --demo`, which fills a fresh database from the shell; the
// button in Settings that puts the example data back after somebody has removed it;
// and the setup screen, which gives the four sensors it creates a week of past so
// that a brand-new dashboard is not an empty grid, and which takes the two places the
// sensors stand on from here for the same reason. A second copy of the dataset would
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

	"github.com/j000k3r/probeglass/internal/icon"
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
// The three of them are also far enough apart to be worth a map. The map starts out
// zoomed to whatever holds every marker on it, so two places in the same country
// give a view of one region with the coastlines far off the edge — which is a fine
// map and a poor demonstration of one. Berlin, Virginia and Frankfurt start at a
// zoom where the world is the thing being drawn, and the three of them are the
// three colours a pin can be: see the table of sensors below for which is which.
type site struct {
	name      string
	kind      model.SiteKind
	lat, lon  float64
	sortOrder int
	// icon is the symbol the heading of this place wears, as a built-in reference.
	// A place carries one for the same reason a sensor does, and it is drawn where
	// places are drawn: the tree and the wall, in front of the heading the machines
	// under it are gathered by.
	icon string
}

var sites = []site{
	{name: "Homelab", kind: model.SiteLocal, lat: 52.5200, lon: 13.4050, sortOrder: 1,
		icon: icon.BuiltinRef("server")},
	{name: "Rented server", kind: model.SiteExternal, lat: 39.0438, lon: -77.4874, sortOrder: 2,
		icon: icon.BuiltinRef("cloud")},
	{name: "Frankfurt", kind: model.SiteExternal, lat: 50.1109, lon: 8.6821, sortOrder: 3,
		icon: icon.BuiltinRef("globe")},
}

// Places returns these three, ready to be written to the sites table.
//
// The setup screen creates the same three places, and it asks here rather than keeping
// a second list of its own. Two lists of the same places drift, and this particular drift
// is one somebody would see: the button that loads the example data leaves a place that
// is already there exactly as it is, so a fresh install whose places had other names
// would end up with six pins, two of them on top of each other.
func Places() []model.Site {
	out := make([]model.Site, 0, len(sites))
	for _, d := range sites {
		out = append(out, d.place())
	}
	return out
}

// place is this row as a row for the sites table, the example marker included.
func (d site) place() model.Site {
	lat, lon := d.lat, d.lon
	return model.Site{
		Name:      d.name,
		Kind:      d.kind,
		Lat:       &lat,
		Lon:       &lon,
		Icon:      d.icon,
		SortOrder: d.sortOrder,
		Origin:    model.OriginExample,
	}
}

// sensor is one entry of the demo dataset: a target plus the shape of its
// synthetic history. The numbers are deliberately boring — the point is a
// dashboard that looks like a homelab, not a benchmark.
//
// Three states, and this table carries one of each. The map draws one pin per place
// and colours it by the worst sensor standing on it, so a demonstration in which
// every pin is grey demonstrates nothing — which is exactly what the demo on the
// website used to be. The three states come from the fact that these sensors are
// real rows the scheduler really probes:
//
//   - Green — enabled, and its target answers from here, always. A sensor that
//     started failing a minute after it was seeded would contradict the very history
//     printed above it, and this is why the enabled public checks are public
//     services. They stand on the site called Frankfurt.
//   - Grey — disabled. A homelab's own addresses — a NAS, a router, a printer — are
//     paused rather than enabled, because 192.168.1.10 does not answer on anybody
//     else's network. Paused, they place the pin without claiming anything about a
//     network this installation cannot see, and the sensor list says "paused", which
//     is the truth. Turning one on is a click.
//   - Red — enabled, and failing on purpose. See the note on "Rented server: web":
//     its target is a name that really does not resolve, so the demo and a real
//     installation tell the same story about it, and its invented history ends in an
//     outage that is still running. It is the one entry here that an installation
//     will act on by itself — an incident is opened and, with a channel configured, a
//     message goes out about it. The example data is one click from gone.
type sensor struct {
	name       string
	sensorType model.SensorType
	target     string
	group      string
	// site is the name of the place this sensor belongs to. Every entry has one: a
	// sensor without a place is one the map does not draw, and the map is half of
	// what this dataset is for.
	site string
	// parent names another of these that this one sits behind. While that one is down
	// this one is excused: no incident of its own, no alert, and the checks it fails in
	// the meantime do not count against its uptime. The pair below is the demonstration
	// of it, and the reason the red pin on the map reports one outage rather than two.
	parent string
	// icon is the symbol this entry wears, as a built-in reference. The demo set
	// carries them because it is what the documentation's pictures and the website's
	// clickable demo are drawn from, and a feature that appears in none of those is a
	// feature nobody discovers.
	icon string
	// logo names one of the pictures in logos.go, when this entry should wear one of
	// those instead: the picker offers drawings and uploaded images, and a dataset of
	// drawings only would show half of it. Seed writes the upload reference — the row
	// the picture lands in is one only the database can number.
	logo string
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
		group: "Internet", site: "Frankfurt", tags: []string{"web", "public"}, icon: icon.BuiltinRef("cloud"),
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
		group: "Internet", site: "Frankfurt", tags: []string{"dns"}, icon: icon.BuiltinRef("globe"),
		logo:   "cloudflare",
		baseMS: 24, jitterMS: 11, intervalS: 60,
		options:    model.Options{"record_type": "A"},
		successMsg: "1 A record", failMsg: "no such host",
		outages: []Outage{
			{Before: 44 * time.Hour, Length: 11 * time.Minute, Cause: "DNS: server misbehaving"},
		},
	},
	{
		name: "Internet ping", sensorType: model.SensorPing, target: "1.1.1.1",
		group: "Network", site: "Frankfurt", tags: []string{"latency"}, icon: icon.BuiltinRef("wifi"),
		baseMS: 11, jitterMS: 5, intervalS: 30,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []Outage{
			{Before: 30 * time.Hour, Length: 4 * time.Minute, Cause: "100% packet loss"},
		},
	},
	{
		name: "Resolver port", sensorType: model.SensorTCP, target: "1.1.1.1:53",
		group: "Network", site: "Frankfurt", tags: []string{"tcp"}, icon: icon.BuiltinRef("plug"),
		baseMS: 31, jitterMS: 9, intervalS: 120,
		successMsg: "connected in 31ms", failMsg: "connection refused",
		outages: []Outage{
			{Before: 92 * time.Hour, Length: 18 * time.Minute, Cause: "connection refused"},
			{Before: 8 * time.Hour, Length: 3 * time.Minute, Cause: "connection timed out"},
		},
	},
	{
		name: "Example website TLS", sensorType: model.SensorTLS, target: "example.org:443",
		group: "Internet", site: "Frankfurt", tags: []string{"tls", "public"}, icon: icon.BuiltinRef("certificate"),
		baseMS: 162, jitterMS: 52, intervalS: 300,
		options:    model.Options{"server_name": "example.org", "cert_warn_days": 14},
		successMsg: "TLS 1.3 · certificate valid for 82 day(s)", failMsg: "TLS certificate expires in 6 day(s)",
		outages: []Outage{},
	},
	{
		name: "Package mirror", sensorType: model.SensorHTTP, target: "https://deb.debian.org",
		group: "Internet", site: "Frankfurt", tags: []string{"web", "public"}, icon: icon.BuiltinRef("database"),
		logo:   "debian",
		baseMS: 121, jitterMS: 38, intervalS: 300,
		options:    model.Options{"expected_status": "200-399", "body_contains": "Debian"},
		successMsg: "HTTP 200 OK", failMsg: "body does not contain \"Debian\"",
		outages: []Outage{
			{Before: 128 * time.Hour, Length: 12 * time.Minute, Cause: "HTTP 502 Bad Gateway"},
		},
	},
	{
		name: "DNS over TLS", sensorType: model.SensorTLS, target: "1.1.1.1:853",
		group: "Internet", site: "Frankfurt", tags: []string{"tls", "dns"}, icon: icon.BuiltinRef("lock"),
		baseMS: 44, jitterMS: 14, intervalS: 600,
		options:    model.Options{"server_name": "cloudflare-dns.com", "cert_warn_days": 21},
		successMsg: "TLS 1.3 · certificate valid for 61 day(s)", failMsg: "TLS handshake failed",
		outages: []Outage{},
	},

	// The homelab itself. Disabled — see the note on the sensor type.
	{
		name: "Router: ping", sensorType: model.SensorPing, target: "192.168.1.1",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"latency"}, icon: icon.BuiltinRef("router"),
		baseMS: 2, jitterMS: 1, intervalS: 30,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []Outage{
			{Before: 71 * time.Hour, Length: 9 * time.Minute, Cause: "100% packet loss"},
		},
	},
	{
		name: "NAS: SSH", sensorType: model.SensorTCP, target: "192.168.1.10:22",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"ssh", "storage"}, icon: icon.BuiltinRef("disk"),
		baseMS: 4, jitterMS: 2, intervalS: 120,
		successMsg: "connected in 4ms", failMsg: "connection refused",
		outages: []Outage{
			{Before: 150 * time.Hour, Length: 42 * time.Minute, Cause: "connection refused"},
		},
	},
	{
		name: "NAS: web interface", sensorType: model.SensorHTTP, target: "http://192.168.1.10",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"web", "storage"}, icon: icon.BuiltinRef("nas"),
		logo:   "truenas",
		baseMS: 38, jitterMS: 12, intervalS: 300,
		options:    model.Options{"expected_status": "200-399"},
		successMsg: "HTTP 200 OK", failMsg: "dial tcp: i/o timeout",
		outages: []Outage{},
	},
	{
		name: "Printer: ink level", sensorType: model.SensorSNMP, target: "192.168.1.20",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"snmp"}, icon: icon.BuiltinRef("printer"),
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

	{
		// The seventh sensor type, and the one entry that is paused for a second reason
		// as well: a Docker sensor reads the Engine over the unix socket, which is there
		// inside the container that runs this and nowhere else. Paused, it places a card
		// and claims nothing, which is the honest thing for a check that would fail on
		// every machine this dataset is ever loaded onto.
		name: "NAS: container", sensorType: model.SensorDocker, target: "nextcloud",
		group: "Homelab", site: "Homelab", paused: true, tags: []string{"docker", "storage"}, icon: icon.BuiltinRef("container"),
		logo:   "docker",
		baseMS: 9, jitterMS: 3, intervalS: 300,
		successMsg: "running, healthy", failMsg: "container not running",
		outages: []Outage{
			{Before: 118 * time.Hour, Length: 6 * time.Minute, Cause: "container not running"},
		},
	},

	{
		// A third entry in this group, paused, and it is here for a reason worth
		// writing down: a place whose sensors are all switched on is the place the map
		// draws green, and this one is the place the map draws red. One sensor that
		// claims nothing is what keeps those two apart — and a ping is the honest one
		// to leave off, because a hoster's box very often filters ICMP and this dataset
		// cannot tell a firewall from an outage.
		name: "Rented server: ping", sensorType: model.SensorPing, target: "vps.example.net",
		group: "Rented server", site: "Rented server", paused: true, tags: []string{"latency"}, icon: icon.BuiltinRef("server"),
		baseMS: 31, jitterMS: 9, intervalS: 60,
		options:    model.Options{"count": 3},
		successMsg: "3/3 replies, 0% loss", failMsg: "100% packet loss",
		outages: []Outage{},
	},

	// A rented box somewhere else, which is the other reason to put sensors on a map.
	{
		name: "Rented server: SSH", sensorType: model.SensorTCP, target: "vps.example.net:22",
		group: "Rented server", site: "Rented server", tags: []string{"ssh"}, icon: icon.BuiltinRef("cpu"),
		baseMS: 26, jitterMS: 8, intervalS: 120,
		successMsg: "connected in 26ms", failMsg: "no such host",
		outages: []Outage{
			{Before: 36 * time.Hour, Length: 15 * time.Minute, Cause: "connection timed out"},
			// And the outage this dataset is arranged around: the box went away half an
			// hour ago and has not come back. It is the parent of the entry below, so
			// this is where the incident is opened — and the only place it is.
			{Before: 30 * time.Minute, Length: 30 * time.Minute, Cause: "no such host"},
		},
	},
	{
		name: "Rented server: web", sensorType: model.SensorHTTP, target: "https://vps.example.net",
		group: "Rented server", site: "Rented server", parent: "Rented server: SSH", tags: []string{"web"}, icon: icon.BuiltinRef("monitor"),
		baseMS: 174, jitterMS: 44, intervalS: 300,
		options:    model.Options{"expected_status": "200-399", "cert_warn_days": 21},
		successMsg: "HTTP 200 OK", failMsg: "no such host",
		// The one entry here that is enabled and supposed to fail, and the reason it
		// is the web check rather than the SSH one beside it: it is the pin the map
		// draws red.
		//
		// vps.example.net is in RFC 2606's reserved space, so it really does not
		// resolve — the invented history and the probes that follow it agree, and the
		// demo on the website and an installation of this running on a real network
		// tell the same story about it. What a real installation will do with it is
		// open an incident and, if a channel is set up, send a message: it is the price
		// of a red pin that is not staged, and the example data is one click from gone.
		//
		// Before and Length are the same ninety minutes on purpose. That puts the whole
		// outage inside the window with its first failing probe ninety minutes back, and
		// its last one on the newest probe in the series — which is what leaves the
		// incident open rather than closed, and the sensor down rather than up, when the
		// demo's scheduler restores from this history. At 300s per probe that is
		// eighteen failures in a row.
		outages: []Outage{
			{Before: 90 * time.Minute, Length: 90 * time.Minute, Cause: "dial tcp: lookup vps.example.net: no such host"},
		},
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
		Icon:     d.icon,
		Interval: d.intervalS,
		Timeout:  10,
		Retries:  1,
		Enabled:  !d.paused,
		Group:    d.group,
		Site:     d.site,
		Parent:   d.parent,
		Tags:     d.tags,
		Options:  opts,
		// The same marker the setup screen writes. Anything invented — here or
		// there — can be taken out again in one click from Settings, and the marker
		// is the only honest way to know which those are: the name and the group
		// are both things a person may edit.
		Origin: model.OriginExample,
	}
}

// parentHistoryEndsDown reports whether the dataset says this sensor is out at the end
// of its invented history — which is the same question the scheduler asks of the live
// status, asked of the table instead.
//
// The dataset is the only thing that knows here: Seed writes history and incidents
// directly, without a probe loop, so there is no state to look at and no clock to ask.
func parentHistoryEndsDown(d sensor) bool {
	h := d.history(7 * 24 * time.Hour)
	if len(h.Outages) == 0 {
		return false
	}
	last := h.Outages[len(h.Outages)-1]
	return last.Before <= last.Length
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

	// The uploaded pictures before the sensors that wear them: an icon column holds
	// upload:<id>, and a row the dataset has not added yet is a reference to nothing.
	logoRefs, err := EnsureLogos(ctx, st)
	if err != nil {
		return Result{}, err
	}
	wanted := Sensors()
	for i := range sensors {
		if sensors[i].logo == "" {
			continue
		}
		if ref, ok := logoRefs[sensors[i].logo]; ok {
			wanted[i].Icon = ref
		}
	}

	created, err := st.CreateMissingSensors(ctx, wanted)
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

	// An outage on a sensor that sits behind another one that is also out is not an
	// outage of its own, and the incident Write just opened for it is a claim the
	// dataset does not mean to make. It is closed here rather than left to the
	// scheduler, because the scheduler's own answer to it — close it with a note on the
	// next probe — would leave the first thing a reader sees be an incident that is
	// about to vanish.
	//
	// Only the still-running ones, which is what an open row means: the outages that
	// ended inside the window are history, and history keeps its incidents.
	for _, c := range created {
		if c.Parent == "" {
			continue
		}
		parent, ok := byName[c.Parent]
		if !ok || !parentHistoryEndsDown(parent) {
			continue
		}
		if err := st.CloseOpenIncidents(ctx, c.ID, now); err != nil {
			return Result{}, fmt.Errorf("close the incident behind %q: %w", c.Name, err)
		}
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
		s := d.place()
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
//
// Where the two ends up leave it is what decides the incident. An outage that is
// over before the window ends is closed by the probe that found the service back,
// and the sensor reads as recovered. One whose end reaches the newest probe in the
// window has no such probe: its incident is left open and the sensor reads as down,
// which is what a red pin on the map is drawn from.
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

		// The series ends one Spacing before now — see the note on the loop below — so
		// this is the newest moment this history has an opinion about.
		newest := now.Add(-Spacing)
		for j := range windows {
			if first[j].IsZero() {
				// No probe in this outage failed, so there is nothing to report. An
				// incident here would be a claim about a moment this history does not
				// cover.
				continue
			}
			id, err := tx.OpenIncident(ctx, c.ID, first[j], windows[j].cause)
			if err != nil {
				return err
			}
			// An outage ends where a probe found the service back, and that is where the
			// probe loop itself would have closed it: the failures stop at last[j], so
			// the probe that ended them stands one Spacing later.
			//
			// If the end of the outage reaches the newest probe in the series, none of
			// that has happened yet. The outage is still running as far as this history
			// can see, so the incident stays open. It used to be closed on the last
			// failure, which is a claim nothing here can make — the service may well
			// still be down — and it is why a sensor restored from this data reads as
			// up when it should read as down. See the "Rented server: web" entry, which
			// is the one stretch of invented history that ends this way.
			if newest.Before(windows[j].to) {
				continue
			}
			if err := tx.CloseIncident(ctx, id, last[j].Add(Spacing)); err != nil {
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
