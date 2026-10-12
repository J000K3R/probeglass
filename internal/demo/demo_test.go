package demo

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "demo.db")
	st, err := store.Open(path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestSensorsAreUsableAndMarked is the contract the rest of the product leans
// on: everything this package hands out is a real sensor, and every one of them
// carries the marker the removal button keys on. A missing marker would leave an
// invented sensor that nothing can take out again.
func TestSensorsAreUsableAndMarked(t *testing.T) {
	list := Sensors()
	if len(list) == 0 {
		t.Fatal("the dataset is empty")
	}
	for _, c := range list {
		if c.Name == "" || c.Target == "" {
			t.Errorf("a sensor is missing its name or target: %+v", c)
		}
		if !c.Type.Valid() {
			t.Errorf("%q has the unknown type %q", c.Name, c.Type)
		}
		if c.Origin != model.OriginExample {
			t.Errorf("%q is not marked as example data", c.Name)
		}
		if c.Interval <= 0 {
			t.Errorf("%q has no interval, so it would never be probed", c.Name)
		}
	}
}

// TestSensorsCopiesTheOptions guards the one mutable thing on the value: the
// options map. A caller that edits what it was given must not be able to reach
// into the package-level table, or the second call would write something else
// than the first.
func TestSensorsCopiesTheOptions(t *testing.T) {
	first := Sensors()
	touched := false
	for i := range first {
		if first[i].Options == nil {
			continue
		}
		for k := range first[i].Options {
			first[i].Options[k] = "clobbered"
			touched = true
			break
		}
		if touched {
			break
		}
	}
	if !touched {
		t.Skip("no sensor in the dataset has options to touch")
	}

	second := Sensors()
	for _, c := range second {
		for k, v := range c.Options {
			if v == "clobbered" {
				t.Fatalf("editing a returned sensor changed the table: %s.%s = %v", c.Name, k, v)
			}
		}
	}
}

// TestSeedCreatesSensorsAndHistory is the whole package in one call, and the
// second call is the half that matters: the Settings button may be pressed
// twice, and the second press must be free.
func TestSeedCreatesSensorsAndHistory(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	res, err := Seed(ctx, st, 7)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Sensors != len(Sensors()) {
		t.Errorf("created %d sensor(s), want the whole dataset (%d)", res.Sensors, len(Sensors()))
	}
	if res.Samples == 0 {
		t.Error("no history was invented; the dataset is supposed to arrive with a week of it")
	}

	counts, err := st.CountExampleData(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Sensors != res.Sensors {
		t.Errorf("the table holds %d example sensor(s), the seed reported %d",
			counts.Sensors, res.Sensors)
	}
	if counts.Results != res.Samples {
		t.Errorf("the table holds %d result(s), the seed reported %d",
			counts.Results, res.Samples)
	}
	if counts.Incidents == 0 {
		t.Error("no incidents were written; the outages in the dataset are what the " +
			"incident log is meant to show")
	}

	again, err := Seed(ctx, st, 7)
	if err != nil {
		t.Fatalf("second seed: %v", err)
	}
	if again.Sensors != 0 || again.Samples != 0 {
		t.Errorf("the second run wrote %+v; it has to write nothing", again)
	}
}

// TestAShortWindowBacksEveryIncidentWithAProbe covers the bug `seed --demo --days 1`
// used to have, which was the kind that makes a log untrustworthy rather than merely
// incomplete.
//
// The outages in the table are described as "so many hours before now", and the
// incidents used to be written straight out of those offsets, whatever the window
// was. With a window shorter than the offset that produced incidents dated *before*
// the history they were meant to explain, with not a single failing probe anywhere
// near them — the incident list and the uptime bars above it told two different
// stories about the same week.
//
// An incident is now written from the probes that really failed, first one to last, so
// an outage the window does not reach produces nothing at all.
func TestAShortWindowBacksEveryIncidentWithAProbe(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	const days = 1
	if _, err := Seed(ctx, st, days); err != nil {
		t.Fatalf("seed: %v", err)
	}

	oldest := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	incidents, err := st.ListIncidents(ctx, store.IncidentFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	if len(incidents) == 0 {
		t.Fatal("a one-day window wrote no incident at all, but the dataset's newest " +
			"outages are inside it and they are what the bars are meant to show")
	}

	for _, inc := range incidents {
		to := time.Now().UTC()
		if inc.ResolvedAt != nil {
			to = *inc.ResolvedAt
		}
		if inc.StartedAt.Before(oldest) {
			t.Errorf("incident %d on sensor %d starts at %s, before the %d-day window "+
				"opened at %s — it points at a moment this history does not cover",
				inc.ID, inc.SensorID, inc.StartedAt, days, oldest)
		}

		samples, err := st.Samples(ctx, inc.SensorID, inc.StartedAt, to.Add(time.Second))
		if err != nil {
			t.Fatalf("samples for incident %d: %v", inc.ID, err)
		}
		failed := 0
		for _, s := range samples {
			if s.OK == 0 {
				failed++
			}
		}
		if failed == 0 {
			t.Errorf("incident %d on sensor %d claims an outage from %s to %s with no "+
				"failed probe in it", inc.ID, inc.SensorID, inc.StartedAt, to)
		}
	}
}

// TestAnOutageThatHasNotEndedIsLeftOpen is the pair to the test above, and the one
// the demo's red pin stands on. An outage that is over before the window ends is
// closed by the probe that found the service back; one whose end reaches the newest
// probe has no such probe, and closing it there would be a claim the history cannot
// back — the service may well still be down. So the incident stays open, and a
// sensor restored from this history reads as down rather than as up.
func TestAnOutageThatHasNotEndedIsLeftOpen(t *testing.T) {
	ctx := context.Background()

	write := func(name string, o Outage) (*store.Store, model.Sensor) {
		t.Helper()
		st := testStore(t)
		c := model.Sensor{
			Name: name, Type: model.SensorHTTP, Target: "https://vps.example.net",
			Interval: int(Spacing / time.Second), Timeout: 5, Retries: 1, Enabled: true,
			Origin: model.OriginExample,
		}
		store.NormaliseSensor(&c)
		if err := st.CreateSensor(ctx, &c); err != nil {
			t.Fatalf("create sensor: %v", err)
		}
		if _, err := Write(ctx, st, c, History{
			Window: 48 * time.Hour, BaseMS: 100, JitterMS: 20,
			SuccessMsg: "HTTP 200 OK", FailMsg: "no such host",
			Outages: []Outage{o},
		}); err != nil {
			t.Fatalf("write: %v", err)
		}
		return st, c
	}

	// Ninety minutes of failures ending at the newest probe in the series.
	st, c := write("Ongoing", Outage{
		Before: 90 * time.Minute, Length: 90 * time.Minute, Cause: "no such host",
	})
	open, err := st.ListIncidents(ctx, store.IncidentFilter{SensorID: c.ID, OnlyOpen: true})
	if err != nil {
		t.Fatalf("list ongoing incidents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d ongoing incident(s) for an outage that reaches the newest probe, want 1",
			len(open))
	}
	if age := time.Since(open[0].StartedAt); age < 89*time.Minute || age > 91*time.Minute {
		t.Errorf("the incident starts %s ago, want the first failing probe, at 90 minutes",
			age.Round(time.Minute))
	}
	last, err := st.LastResult(ctx, c.ID)
	if err != nil || last == nil {
		t.Fatalf("last result: %v, %v", last, err)
	}
	if last.Success {
		t.Error("the newest probe succeeded, so a sensor restored from this history " +
			"would read as up and the pin would be green")
	}

	// The same outage moved a day back is over, and is closed by the probe that found
	// the service back.
	st2, c2 := write("Recovered", Outage{
		Before: 24 * time.Hour, Length: 90 * time.Minute, Cause: "no such host",
	})
	resolved, err := st2.ListIncidents(ctx, store.IncidentFilter{SensorID: c2.ID, OnlyResolved: true})
	if err != nil {
		t.Fatalf("list resolved incidents: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("%d resolved incident(s) for an outage that ended a day ago, want 1", len(resolved))
	}
}

// TestTheDatasetHasOnePinInEachState is the demo's own claim, checked where the
// claim is made. The map draws one pin per place and colours it by the worst sensor
// standing on it, so three pins in three colours takes three states: a place where
// everything is up, a place where everything is paused, and a place with something
// down on it. The demo on the website had one colour and no map worth looking at,
// and this is the test that keeps it that way.
func TestTheDatasetHasOnePinInEachState(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	if _, err := Seed(ctx, st, 7); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Which place each sensor stands on, and whether it is switched on.
	byName := map[string]sensor{}
	all := map[string]int{}
	enabled := map[string]int{}
	for _, d := range sensors {
		byName[d.name] = d
		all[d.site]++
		if !d.paused {
			enabled[d.site]++
		}
	}

	// Green: a place every sensor on which is enabled and answering.
	// Grey: one where nothing is switched on, which is a homelab's own addresses.
	// Red: one with an ongoing outage on it, from here.
	open, err := st.ListIncidentsWithNames(ctx, store.IncidentFilter{OnlyOpen: true, Limit: 100})
	if err != nil {
		t.Fatalf("list ongoing incidents: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("%d ongoing incident(s) in the example data, want exactly 1 — the one "+
			"that draws the red pin", len(open))
	}
	down, ok := byName[open[0].SensorName]
	if !ok {
		t.Fatalf("the ongoing incident is on %q, which is not a sensor in the dataset",
			open[0].SensorName)
	}
	if down.paused {
		t.Errorf("%q is paused, so its site reads as grey rather than red", down.name)
	}

	green, gray := "", ""
	for name, n := range all {
		// A tagged switch on the count rather than on both ends of the range: a place
		// with some switched on and some not is neither pin, and is deliberately left
		// to fall through.
		switch enabled[name] {
		case n:
			green = name
		case 0:
			gray = name
		}
	}
	if green == "" {
		t.Error("no place has every sensor switched on, so no pin is green")
	}
	if gray == "" {
		t.Error("no place has every sensor paused, so no pin is grey")
	}
	if green == down.site {
		t.Errorf("the place with the ongoing outage is %q, which is also the one that is "+
			"meant to be all green", green)
	}
	if gray == down.site {
		t.Errorf("the place with the ongoing outage is %q, which is also the one that is "+
			"meant to be all grey", gray)
	}
	if gray == green && gray != "" {
		t.Errorf("one place cannot be both the green pin and the grey one: %q", gray)
	}
}

// TestABlipIsOneProbeAndNoIncident pins the difference between the two ways a
// synthetic probe fails. An outage is a stretch of them and belongs in the incident
// log; a blip is one probe, writes nothing, and is what leaves a sensor reading
// flaky. Writing an incident for a single failed probe would fill the log with
// one-minute outages nobody can act on.
func TestABlipIsOneProbeAndNoIncident(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	c := model.Sensor{
		Name: "Blip", Type: model.SensorPing, Target: "192.0.2.1",
		Interval: 60, Timeout: 5, Retries: 2, Enabled: true,
		Origin: model.OriginExample,
	}
	store.NormaliseSensor(&c)
	if err := st.CreateSensor(ctx, &c); err != nil {
		t.Fatalf("create sensor: %v", err)
	}

	// Two blips: the most recent probe and one a day back.
	n, err := Write(ctx, st, c, History{
		Window: 48 * time.Hour, BaseMS: 5, JitterMS: 2,
		SuccessMsg: "pong", FailMsg: "no reply",
		Blips: []time.Duration{Spacing, 24 * time.Hour},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != int(48*time.Hour/Spacing)-1 {
		t.Errorf("wrote %d probe(s), want %d", n, int(48*time.Hour/Spacing)-1)
	}

	incidents, err := st.ListIncidents(ctx, store.IncidentFilter{SensorID: c.ID, Limit: 10})
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	if len(incidents) != 0 {
		t.Errorf("%d incident(s) were written for two single failed probes: %+v",
			len(incidents), incidents)
	}

	// Both blips landed, on probes and not between them.
	samples, err := st.Samples(ctx, c.ID, time.Now().UTC().Add(-47*time.Hour), time.Now().UTC())
	if err != nil {
		t.Fatalf("samples: %v", err)
	}
	failed := 0
	for _, s := range samples {
		if s.OK == 0 {
			failed++
		}
	}
	if failed != 2 {
		t.Errorf("%d failed probe(s) in the series, want the 2 blips", failed)
	}

	// The newest one is the one that matters: it is what a sensor restored from this
	// history reads as, and it is the whole point of a blip.
	last, err := st.LastResult(ctx, c.ID)
	if err != nil || last == nil {
		t.Fatalf("last result: %v, %v", last, err)
	}
	if last.Success {
		t.Error("the most recent probe succeeded, so the sensor would read green; a blip " +
			"at the end of the window is what leaves it flaky")
	}
}

// TestSeedPlacesItsSensorsOnTheMap is about the half of the dataset that is not a
// sensor. The example checks need somewhere to live, and the map draws places —
// without a placed site the map is an empty rectangle no matter how many sensors
// were invented, which is exactly what the dashboard looked like before this.
func TestSeedPlacesItsSensorsOnTheMap(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	res, err := Seed(ctx, st, 3)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Sites != len(sites) {
		t.Errorf("created %d site(s), want %d", res.Sites, len(sites))
	}

	list, err := st.ListSites(ctx)
	if err != nil {
		t.Fatalf("list sites: %v", err)
	}
	placed := 0
	for _, s := range list {
		if !s.Placed() {
			t.Errorf("site %q has no coordinates, so the map cannot draw it", s.Name)
			continue
		}
		if *s.Lat < -90 || *s.Lat > 90 || *s.Lon < -180 || *s.Lon > 180 {
			t.Errorf("site %q is off the planet: %g,%g", s.Name, *s.Lat, *s.Lon)
		}
		placed++
	}
	if placed == 0 {
		t.Fatal("no site is placed; the map would be empty")
	}

	// A sensor whose site does not exist is refused by the foreign key, so this is
	// also the check that the places were written before the sensors that point at
	// them.
	for _, c := range Sensors() {
		if c.Site == "" {
			continue
		}
		if _, err := st.GetSite(ctx, c.Site); err != nil {
			t.Errorf("sensor %q names the site %q, which was not created: %v", c.Name, c.Site, err)
		}
	}
}

// TestSeedLeavesAnExistingSiteAlone is the promise the Settings button makes when
// it says it only restores what is missing. Somebody who has moved "Homelab" to
// where their house actually is must not have it moved back.
func TestSeedLeavesAnExistingSiteAlone(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	lat, lon := 48.1372, 11.5756 // Munich, which is where this operator lives.
	theirs := model.Site{Name: "Homelab", Kind: model.SiteExternal, Lat: &lat, Lon: &lon}
	if err := st.CreateSite(ctx, &theirs); err != nil {
		t.Fatalf("create: %v", err)
	}

	res, err := Seed(ctx, st, 3)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if res.Sites != len(sites)-1 {
		t.Errorf("created %d site(s); the one that already existed is not one of them", res.Sites)
	}
	after, err := st.GetSite(ctx, "Homelab")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Kind != model.SiteExternal || *after.Lat != lat {
		t.Errorf("the seed overwrote a site somebody made: %+v", after)
	}
	if after.Origin == model.OriginExample {
		t.Error("a site the operator made came back marked as example data, " +
			"so the removal button would take it")
	}
}

// TestSeedRemovalTakesItsPlacesBack is the pair to the store-level test, through
// the dataset that actually ships: seed, remove, and the installation is back to
// where it started rather than holding two empty pins.
func TestSeedRemovalTakesItsPlacesBack(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	if _, err := Seed(ctx, st, 3); err != nil {
		t.Fatalf("seed: %v", err)
	}
	removed, err := st.DeleteExampleData(ctx)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if removed.Sites != len(sites) {
		t.Errorf("removed %d site(s), want %d", removed.Sites, len(sites))
	}
	left, err := st.ListSites(ctx)
	if err != nil {
		t.Fatalf("list sites: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("%d site(s) outlived the example data: %+v", len(left), left)
	}
}

// TestPrivateTargetsArePaused is the rule that keeps a seeded dashboard honest a
// minute after it was seeded. Every enabled example sensor is probed for real, so
// its target has to answer from wherever this runs — otherwise the week of green
// history printed above it is contradicted by a red sensor.
//
// A homelab's own addresses are what belongs at the two example sites, and none of
// them answers on anybody else's network. So they are paused: they place the pins
// without claiming anything about a network this installation cannot see, and the
// sensor list shows them as paused, which is the truth.
func TestPrivateTargetsArePaused(t *testing.T) {
	placed := 0
	for _, d := range sensors {
		if !d.paused {
			if privateTarget(t, d.target) {
				t.Errorf("%q is enabled and points at %q, which cannot answer from here; "+
					"it would contradict its own invented history within a minute",
					d.name, d.target)
			}
			continue
		}
		if d.site == "" {
			t.Errorf("%q is paused and belongs to no site, so it is a grey row "+
				"that shows nothing", d.name)
		}
		placed++
	}
	if placed == 0 {
		t.Error("nothing in the dataset is placed, so the map has nothing to draw")
	}
}

// privateTarget reports whether a target names an address that only exists inside
// somebody's own network.
//
// A name counts when it is obviously local (.local, .lan) or has no dot at all.
// vps.example.net is deliberately not one of these: it does not resolve, but that
// is a different failure, and treating an unresolvable public name as private
// would let a genuine mistake through the check above.
func privateTarget(t *testing.T, target string) bool {
	t.Helper()
	host := target
	if u, err := url.Parse(target); err == nil && u.Host != "" {
		host = u.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return !strings.Contains(host, ".") ||
			strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".lan")
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast()
}
