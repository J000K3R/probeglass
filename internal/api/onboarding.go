package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/j000k3r/probeglass/internal/config"
	"github.com/j000k3r/probeglass/internal/demo"
	"github.com/j000k3r/probeglass/internal/model"
	"github.com/j000k3r/probeglass/internal/store"
)

// ---------------------------------------------------------------------------
// guided tour
// ---------------------------------------------------------------------------

// tourVersionMax is the highest tour version this binary knows about. It is
// written by the frontend after a tour run finishes, and bounded here so a
// crafted request cannot fill the settings table with an arbitrary integer.
//
// It deliberately is not the number of steps: the tour grows, steps are
// reordered, and a step that only applies in advanced mode is not shown to
// everyone. All that has to survive a page reload is "which run of the tour has
// this installation seen", so one monotonic number is enough.
const tourVersionMax = 1000

// handleSetTourVersion answers POST /api/onboarding/tour.
//
// It gets its own endpoint rather than going through PUT /api/settings because
// that route validates values against a fixed whitelist of words, and this is a
// number that has to be able to grow. Sharing the route would mean loosening the
// whitelist for everything.
func (s *Server) handleSetTourVersion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version int `json:"version"`
	}
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: %s", err.Error())
		return
	}
	if in.Version < 0 || in.Version > tourVersionMax {
		writeError(w, http.StatusBadRequest, "invalid tour version")
		return
	}
	p, ok := principalOf(r)
	if !ok || p.User == nil {
		// The tour is something a person sits through, and the progress is theirs.
		// The API key has nobody behind it to record progress for.
		writeError(w, http.StatusBadRequest, "the API key has no tour progress to record")
		return
	}
	ctx := r.Context()
	if err := s.store.SetUserPref(ctx, p.User.ID, model.PrefTourVersion, strconv.Itoa(in.Version)); err != nil {
		s.fail(w, err, http.StatusInternalServerError, "could not save the tour progress")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"version": in.Version})
}

// ---------------------------------------------------------------------------
// example sensors
// ---------------------------------------------------------------------------

// exampleSensor is one of the sensors a fresh installation starts with, together
// with the past it is given so that the dashboard is not empty when it is first
// looked at.
type exampleSensor struct {
	Name   string
	Type   model.SensorType
	Target string
	// Site is the place this one stands on, empty for the ones that stand nowhere in
	// particular. The name has to be one of demo.Places(): the places are created
	// before the sensors, because a sensor naming a place that does not exist is
	// refused by the database.
	Site    string
	History demo.History
}

// exampleSensorInterval is how often the examples probe. Five minutes is slow
// enough that nobody's first ten minutes are spent watching a dashboard refresh,
// and fast enough that the first result arrives while they are still looking.
const exampleSensorInterval = 300

// exampleSensors returns the four sensors a fresh installation starts with.
//
// The point is that a brand-new dashboard is not an empty grid. Somebody who has
// just set a password has no sensor, no history and no reason to trust the tool
// yet, and four editable examples — each a different type, each with a week of
// past — show what the thing does and how it is configured far better than a
// paragraph of text could. The history is invented, and saying so is what the
// "(example)" in every name is for.
//
// The first one is ProbeGlass itself. That is the important one: it is reached
// over loopback, so it works in a homelab with no internet and no other host to
// point at, and the dashboard therefore always has at least one green sensor to
// show. A first-run dashboard that is entirely grey or entirely red teaches the
// wrong thing about whether the product works.
//
// The other three are the three shapes a real installation runs into, and two of
// them are on purpose not green:
//
//   - The example website carries one closed outage a few hours back. Without it
//     the uptime bars are a solid green block and the incident log is empty, and
//     somebody would have to take it on faith that either works.
//   - The internet gateway's last probe failed, which is what the flaky status
//     looks like: a sensor that has not answered yet but has not used up its
//     retries either. 192.168.1.1 genuinely does not answer on most networks, so
//     this is a true statement about the installation rather than a decoration —
//     and one click on a working gateway address turns it green.
//
// They belong to no alert channel. A default that can fire a real message into
// somebody's chat room before they have configured a single thing is not a
// helpful default.
//
// Two of them stand on a place as well — "Homelab" for the two that are about this
// machine and "Rented server" for the two that are about something out there —
// because otherwise the map is the one screen a new installation has nothing to say
// about at all. Both are places the demo data set also uses, with the same names and
// coordinates, so that loading that data later folds into them instead of drawing a
// second pin beside each of them. Those places carry the example marker, which is
// what makes them removable in one click along with the sensors.
//
// The demo set knows a third place, "Frankfurt", which the four examples here do not
// stand on and which the setup screen therefore does not create: a fresh
// installation's map would have a named pin on it with nothing on the pin. What that
// place is for is the other half of the demo — the public checks, and a pin that is
// green rather than grey.
func exampleSensors(dialAddr string) []exampleSensor {
	window := time.Duration(exampleDays) * 24 * time.Hour
	// The one failure each of the last-probe and the outage is described against.
	// demo.Write moves a blip to the nearer probe, so a whole spacing means "the
	// most recent probe there is".
	last := demo.Spacing

	// The two places, by the names demo.Places() writes them under. Spelled out here
	// because that is what a sensor column holds — a name, not an id.
	const (
		here  = "Homelab"
		other = "Rented server"
	)

	return []exampleSensor{
		{
			Name: "This dashboard (example)", Type: model.SensorHTTP,
			Target: "http://" + dialAddr + "/healthz",
			Site:   here,
			History: demo.History{
				Window: window, BaseMS: 3, JitterMS: 1,
				SuccessMsg: "HTTP 200 OK", FailMsg: "dial tcp: connection refused",
			},
		},
		{
			Name: "Internet gateway (example)", Type: model.SensorPing,
			Target: "192.168.1.1",
			Site:   here,
			History: demo.History{
				Window: window, BaseMS: 2, JitterMS: 1,
				SuccessMsg: "3/3 replies, 0% loss", FailMsg: "100% packet loss",
				Blips: []time.Duration{last},
			},
		},
		{
			Name: "Public DNS (example)", Type: model.SensorDNS,
			Target: "one.one.one.one",
			Site:   other,
			History: demo.History{
				Window: window, BaseMS: 24, JitterMS: 11,
				SuccessMsg: "1 A record", FailMsg: "no such host",
			},
		},
		{
			Name: "Example website (example)", Type: model.SensorHTTP,
			Target: "https://example.org",
			Site:   other,
			History: demo.History{
				Window: window, BaseMS: 148, JitterMS: 45,
				SuccessMsg: "HTTP 200 OK", FailMsg: "dial tcp: i/o timeout",
				Outages: []demo.Outage{{
					Before: 5 * time.Hour, Length: 15 * time.Minute,
					Cause: "dial tcp: i/o timeout",
				}},
			},
		},
	}
}

// exampleSiteNames returns the places the example sensors stand on, each once, in
// the order they are named.
//
// The setup screen creates these and not every place the demo data set knows about,
// and that is the whole point of the function: the demo set describes a homelab and
// the world it reaches out to, and it has a place on the map for the checks that go
// out to the internet. A fresh installation has four sensors standing on two of
// those places, so creating the third would put a named pin with nothing on it on
// the very first map anybody looks at.
func exampleSiteNames(dialAddr string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ex := range exampleSensors(dialAddr) {
		if ex.Site == "" || seen[ex.Site] {
			continue
		}
		seen[ex.Site] = true
		out = append(out, ex.Site)
	}
	return out
}

// createExampleSensors seeds the four examples with a week of invented past, and
// reports how many it created.
//
// Two conditions have to hold, and both are about not overruling the user:
//
//   - The configuration file declares no sensors. An installation driven from
//     YAML is a deliberate one, and adding four sensors to it would put rows in
//     the database that the next start would not remove.
//   - The table is empty. This makes the call idempotent, which matters because
//     the setup endpoint is reachable more than once in the window before the
//     password is stored.
//
// A failure here is never fatal to the setup: the password is the thing the user
// asked to set, and it has already been written by the time this runs. The error
// is returned so the caller can log it, and the caller ignores it.
//
// The order below is the part that matters, and each step is there because of
// what the next one would otherwise see:
//
//  0. Create the places the sensors stand on. They are what the map draws, and
//     they have to exist before the first sensor names one. Only the ones actually
//     named are made — see exampleSiteNames.
//  1. Create the sensors disabled. Enabling one hands it to the scheduler, and a
//     real probe landing in the middle of this would put a live result next to a
//     week of invented ones — and, on the gateway, in front of the failed probe
//     that is the whole reason it reads flaky.
//  2. Write the history.
//  3. Fold it into the rollups, so the 24h/7d/30d columns and the sparklines have
//     numbers to show immediately instead of after the next maintenance pass.
//  4. Enable them and reconcile. This is the step whose absence made the whole
//     thing inert: the daemon is already running by the time somebody sets a
//     password, so nothing but this ever told it these sensors exist. They used to
//     sit in the table unprobed until the container was restarted.
func (s *Server) createExampleSensors(ctx context.Context, dialAddr string) (int, error) {
	if len(s.cfg.Sensors) > 0 {
		return 0, nil
	}
	existing, err := s.store.ListSensors(ctx)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	// The places come first: a sensor names its site, and a name that is not in the
	// sites table yet is refused by the database rather than stored without one. A
	// place that is already there is left as it is, which is the same contract the
	// demo data set follows — somebody who moved the pin to where they really are
	// must not have it moved back.
	known := map[string]model.Site{}
	for _, place := range demo.Places() {
		known[place.Name] = place
	}
	for _, name := range exampleSiteNames(dialAddr) {
		place, ok := known[name]
		if !ok {
			return 0, fmt.Errorf("an example sensor stands on the place %q, which the "+
				"demo data set does not know", name)
		}
		if _, err := s.store.GetSite(ctx, name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		p := place
		if err := s.store.CreateSite(ctx, &p); err != nil {
			return 0, err
		}
	}

	examples := exampleSensors(dialAddr)
	created := make([]model.Sensor, 0, len(examples))
	for _, ex := range examples {
		c := model.Sensor{
			Name:     ex.Name,
			Type:     ex.Type,
			Target:   ex.Target,
			Site:     ex.Site,
			Interval: exampleSensorInterval,
			Timeout:  10,
			Retries:  2,
			Enabled:  false,
			Group:    "Examples",
			Options:  model.Options{},
			// The mark that makes "remove all the example data" possible later. It
			// is set here and by `seed --demo`, and by nothing else; a sensor the
			// operator typed in by hand leaves it empty and is therefore never
			// touched by the bulk delete.
			Origin: model.OriginExample,
		}
		store.NormaliseSensor(&c)
		if err := s.store.CreateSensor(ctx, &c); err != nil {
			return len(created), err
		}
		created = append(created, c)
	}

	for i := range created {
		if _, err := demo.Write(ctx, s.store, created[i], examples[i].History); err != nil {
			return len(created), fmt.Errorf("invent history for %s: %w", created[i].Name, err)
		}
	}
	// Enabled before the rollups, and that order is the whole point.
	//
	// Rolling up a week of invented history for four sensors is the most expensive
	// thing this function does — one INSERT ... SELECT over several thousand rows on
	// the single connection the store allows — and it is a courtesy: without it the
	// long ranges are empty until the next maintenance pass, five minutes later. The
	// enabling below is not a courtesy. A sensor that is left disabled is a sensor the
	// scheduler never hears about: the dashboard shows it as pending and nothing ever
	// probes it.
	//
	// The caller swallows an error from here on purpose — a first run must not fail
	// because a nicety did — so a rollup that timed out used to take the sensors with
	// it, silently, and leave exactly that inert dashboard. Doing the part that matters
	// first means the worst a slow rollup can cost is the rollups.
	for i := range created {
		created[i].Enabled = true
		if err := s.store.UpdateSensor(ctx, &created[i]); err != nil {
			return len(created), err
		}
	}

	if err := s.store.Maintain(ctx, time.Now().UTC()); err != nil {
		return len(created), fmt.Errorf("roll up the example history: %w", err)
	}

	// Reconcile reads the history back — it is what turns the gateway's last failed
	// probe into the flaky status and the other three into green — and starts their
	// probe loops. It is deliberately the last thing here: an example sensor that is
	// enabled but not reconciled is a row nothing will ever probe.
	if err := s.sched.Reconcile(ctx); err != nil {
		return len(created), fmt.Errorf("hand the example sensors to the scheduler: %w", err)
	}
	return len(created), nil
}

// exampleDialAddr is where the examples are told to reach this daemon. It is the
// same address the doctor and the health check use, so a sensor pointing at the
// dashboard cannot disagree with the health check about where the dashboard is.
func exampleDialAddr(cfg *config.Config) string {
	return config.DialAddr(cfg.Listen)
}
