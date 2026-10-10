package store

import (
	"context"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// incidentFixture lays out three outages that differ in every way the filter can
// tell them apart: two sensors, both states, a note on one of them, and text that
// contains a percent sign and an underscore.
//
// Those last two are the point of the fixture. They are what an incident list
// collected over a few weeks of real life contains — "disk 92% full", a hostname
// like "nas_backup" — and they are exactly the characters that turn a search into
// a pattern match if nobody thought about it.
type incidentFixture struct {
	store   *Store
	router  model.Sensor
	printer model.Sensor

	// The three outages, in the order they started.
	closedPrinter int64 // resolved, cause and note both carry % and _
	closedRouter  int64 // resolved, plain text
	openRouter    int64 // still running
}

func newIncidentFixture(t *testing.T) *incidentFixture {
	t.Helper()
	ctx := context.Background()
	st := newTestStore(t)

	f := &incidentFixture{
		store:   st,
		router:  mustSensor(t, st, model.Sensor{Name: "Router", Type: model.SensorPing, Target: "10.0.0.1"}),
		printer: mustSensor(t, st, model.Sensor{Name: "Printer", Type: model.SensorTCP, Target: "10.0.0.9"}),
	}
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)

	open := func(sensor model.Sensor, at time.Time, cause string) int64 {
		t.Helper()
		id, err := st.OpenIncident(ctx, sensor.ID, at, cause)
		if err != nil {
			t.Fatalf("open incident on %s: %v", sensor.Name, err)
		}
		return id
	}

	f.closedPrinter = open(f.printer, base, "paper jam, 100%_sure")
	if err := st.AckIncident(ctx, f.closedPrinter, "homelab", "known, my_fault", base.Add(time.Minute)); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := st.CloseIncident(ctx, f.closedPrinter, base.Add(5*time.Minute)); err != nil {
		t.Fatalf("close: %v", err)
	}

	f.closedRouter = open(f.router, base.Add(time.Hour), "timeout after 5s")
	if err := st.CloseIncident(ctx, f.closedRouter, base.Add(time.Hour+5*time.Minute)); err != nil {
		t.Fatalf("close: %v", err)
	}

	f.openRouter = open(f.router, base.Add(2*time.Hour), "connection refused")
	return f
}

// ids reports which of the three fixture outages came back, as a set, so that a
// test can compare whole answers without depending on the order they are read in.
// Order is pinned separately: it is "newest first" and it is the same for every
// filter, which is why it does not belong in each case of the tables below.
func (f *incidentFixture) ids(t *testing.T, got []IncidentRow) map[int64]bool {
	t.Helper()
	out := map[int64]bool{}
	for _, row := range got {
		out[row.ID] = true
	}
	return out
}

func (f *incidentFixture) want(ids ...int64) map[int64]bool {
	out := map[int64]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}

func sameIDs(a, b map[int64]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for id := range a {
		if !b[id] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// the three readings of the list
// ---------------------------------------------------------------------------

// TestIncidentFilterByState walks the two booleans the filter line's segmented
// control sets.
//
// It runs against both list functions rather than one. They share
// applyIncidentFilter, and the test is as much a check that they still do as a
// check that the clause is right: the day somebody adds the next condition to only
// one of them, this is where it shows.
func TestIncidentFilterByState(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()

	for _, c := range []struct {
		what string
		f    IncidentFilter
		want map[int64]bool
	}{
		{"neither", IncidentFilter{Limit: 50}, f.want(f.closedPrinter, f.closedRouter, f.openRouter)},
		{"only open", IncidentFilter{Limit: 50, OnlyOpen: true}, f.want(f.openRouter)},
		{"only resolved", IncidentFilter{Limit: 50, OnlyResolved: true}, f.want(f.closedPrinter, f.closedRouter)},
	} {
		got, err := f.store.ListIncidentsWithNames(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: ListIncidentsWithNames: %v", c.what, err)
		}
		if !sameIDs(f.ids(t, got), c.want) {
			t.Errorf("%s: ListIncidentsWithNames returned %v, want %v", c.what, f.ids(t, got), c.want)
		}

		plain, err := f.store.ListIncidents(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: ListIncidents: %v", c.what, err)
		}
		ids := map[int64]bool{}
		for _, inc := range plain {
			ids[inc.ID] = true
		}
		if !sameIDs(ids, c.want) {
			t.Errorf("%s: ListIncidents returned %v, want %v", c.what, ids, c.want)
		}
	}
}

// TestIncidentFilterTextIsALiteral is the test for the one decision in this file
// that is easy to get wrong and expensive to get wrong.
//
// A "%" somebody typed is a percent sign, not "anything". If it were a wildcard,
// one keystroke would return the whole table and the person would have no way of
// telling that from having found everything — and "100%_sure" would match rows that
// contain no such text at all. The queries below are chosen so that a pattern match
// and a literal match give different answers, which is the only way a test can tell
// the two apart.
func TestIncidentFilterTextIsALiteral(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()

	for _, c := range []struct {
		query string
		want  map[int64]bool
		// why says what would have happened under a pattern match, so that a
		// failure explains itself rather than looking like a typo.
		why string
	}{
		{"paper", f.want(f.closedPrinter), "the cause"},
		{"refused", f.want(f.openRouter), "a word in one cause only"},
		{"my_fault", f.want(f.closedPrinter), "the acknowledgement note"},
		{"router", f.want(f.closedRouter, f.openRouter), "the sensor's name, which is only in the joined query"},
		{"10.0.0.9", f.want(f.closedPrinter), "the sensor's target, which is only in the joined query"},
		{"ROUTER", f.want(f.closedRouter, f.openRouter), "the same, in capitals: matching is case-insensitive"},

		// The four that a LIKE would answer differently.
		{"%", f.want(f.closedPrinter), `a bare percent sign; as a wildcard it would match all three`},
		{"_", f.want(f.closedPrinter), `a bare underscore; as a wildcard it matches any one character, so all three`},
		{"100%_sure", f.want(f.closedPrinter), "the literal text around both of them"},
		{"100%", f.want(f.closedPrinter), "a percent sign at the end of a number"},
		{"my_fa_lt", map[int64]bool{}, `the note with a character missing; as a pattern the underscore would fill it in`},

		{"nothing says this", map[int64]bool{}, "a word that appears nowhere"},
	} {
		got, err := f.store.ListIncidentsWithNames(ctx, IncidentFilter{Limit: 50, Query: c.query})
		if err != nil {
			t.Fatalf("query %q: %v", c.query, err)
		}
		if !sameIDs(f.ids(t, got), c.want) {
			t.Errorf("query %q (%s) returned %v, want %v", c.query, c.why, f.ids(t, got), c.want)
		}
	}

	// The unjoined query has no sensor to look at, so "router" finds nothing there.
	// It is the sensor detail window's list, where the sensor is already known and
	// typing its name would only ever return the same rows.
	plain, err := f.store.ListIncidents(ctx, IncidentFilter{Limit: 50, Query: "router"})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(plain) != 0 {
		t.Errorf("the unjoined query matched %d rows on a sensor name, want none", len(plain))
	}
	plain, err = f.store.ListIncidents(ctx, IncidentFilter{Limit: 50, Query: "my_fault"})
	if err != nil {
		t.Fatalf("ListIncidents: %v", err)
	}
	if len(plain) != 1 || plain[0].ID != f.closedPrinter {
		t.Errorf("the unjoined query matched %d rows on the note, want the one", len(plain))
	}
}

// TestIncidentFilterBySensorAndWindow checks the two narrowing conditions that are
// not text, and that the filter composes rather than overriding itself.
func TestIncidentFilterBySensorAndWindow(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()

	got, err := f.store.ListIncidentsWithNames(ctx, IncidentFilter{Limit: 50, SensorID: f.router.ID})
	if err != nil {
		t.Fatalf("by sensor: %v", err)
	}
	if !sameIDs(f.ids(t, got), f.want(f.closedRouter, f.openRouter)) {
		t.Errorf("by sensor returned %v", f.ids(t, got))
	}

	// The window is compared against when the outage started, not when it ended:
	// "what happened last night" is a question about the beginning of things.
	base := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	got, err = f.store.ListIncidentsWithNames(ctx, IncidentFilter{Limit: 50, Since: base.Add(90 * time.Minute)})
	if err != nil {
		t.Fatalf("by window: %v", err)
	}
	if !sameIDs(f.ids(t, got), f.want(f.openRouter)) {
		t.Errorf("by window returned %v", f.ids(t, got))
	}

	// All four at once, which is what the filter line actually sends when somebody
	// has narrowed everything: one sensor, one state, one word, one window.
	got, err = f.store.ListIncidentsWithNames(ctx, IncidentFilter{
		Limit:    50,
		SensorID: f.router.ID,
		OnlyOpen: true,
		Since:    base.Add(90 * time.Minute),
		Query:    "connection",
	})
	if err != nil {
		t.Fatalf("everything at once: %v", err)
	}
	if !sameIDs(f.ids(t, got), f.want(f.openRouter)) {
		t.Errorf("everything at once returned %v", f.ids(t, got))
	}

	// And the same filter with one word changed, so that the case above is not
	// passing because the conditions cancel each other out into "all".
	got, err = f.store.ListIncidentsWithNames(ctx, IncidentFilter{
		Limit:    50,
		SensorID: f.router.ID,
		OnlyOpen: true,
		Since:    base.Add(90 * time.Minute),
		Query:    "paper",
	})
	if err != nil {
		t.Fatalf("everything at once, other word: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a filter that names the printer's outage and the router's sensor returned %d rows", len(got))
	}
}

// TestIncidentFilterLimitIsHonoured pins the ceiling, because the dashboard asks
// for 200 and the store's default is 50: a filter that quietly returned the default
// would look like a shorter history rather than a bug.
func TestIncidentFilterLimitIsHonoured(t *testing.T) {
	f := newIncidentFixture(t)
	ctx := context.Background()

	got, err := f.store.ListIncidentsWithNames(ctx, IncidentFilter{Limit: 2})
	if err != nil {
		t.Fatalf("limit 2: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("limit 2 returned %d rows", len(got))
	}
	// Newest first, which is the order every caller of this reads in.
	if got[0].ID != f.openRouter || got[1].ID != f.closedRouter {
		t.Errorf("limit 2 returned %d, %d; want the two newest first", got[0].ID, got[1].ID)
	}
}
