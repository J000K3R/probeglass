package store

import (
	"context"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// A maintenance window is a promise about two numbers and one silence: the failed
// probes inside it do not open an incident, and they do not count against uptime.
// The first belongs to the scheduler and is tested there. What is tested here is
// the second, and the half of it that is easy to get wrong — that it survives the
// rollup.

// windowFor builds an enabled one-off window covering [from, to) for one sensor.
func windowFor(t *testing.T, st *Store, c model.Sensor, from, to time.Time) model.MaintenanceWindow {
	t.Helper()
	w := model.MaintenanceWindow{
		SensorID: c.ID,
		Kind:     model.WindowOnce,
		StartsAt: &from,
		EndsAt:   &to,
		Timezone: "UTC",
		Note:     "test",
		Enabled:  true,
	}
	if err := st.CreateWindow(context.Background(), &w); err != nil {
		t.Fatalf("create window: %v", err)
	}
	return w
}

// TestAMaintenanceWindowLeavesTheUptimeAlone is the whole point of the feature, shown
// as an A/B: two sensors fail exactly as often, and only one of them was failing
// inside a window.
//
// The two figures differ by the thing the feature is for. Ten failed probes are ten
// failed probes; five of them were expected, and the sensor that was expected to be
// down reads as five failures rather than ten.
//
// Note what drives the difference: the marker on the row, written by the scheduler
// at the moment the probe ran. Nothing here reads the window table, and that is
// deliberate — see the note on CreateWindow: a window applies from the moment it is
// saved, and the samples written before it existed are not rewritten.
func TestAMaintenanceWindowLeavesTheUptimeAlone(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	planned := mustSensor(t, st, model.Sensor{
		Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local", Group: "homelab",
	})
	unplanned := mustSensor(t, st, model.Sensor{
		Name: "web", Type: model.SensorHTTP, Target: "https://example.org",
	})

	// Yesterday at noon, so that nothing here depends on when the test runs: the
	// bucket is complete whatever the hour is.
	base := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	const perWindow = 5
	for i := 0; i < perWindow*2; i++ {
		for c, expected := range map[int64]bool{planned.ID: i >= perWindow, unplanned.ID: false} {
			r := model.Result{
				SensorID: c, TS: base.Add(time.Duration(i) * time.Minute),
				Success: false, Excused: expected,
			}
			if err := st.InsertResult(ctx, &r); err != nil {
				t.Fatalf("insert: %v", err)
			}
		}
	}

	from, to := base.Add(-time.Hour), base.Add(time.Hour)

	got, err := st.Uptime(ctx, planned.ID, from, to)
	if err != nil {
		t.Fatalf("uptime: %v", err)
	}
	if got.Total != 5 {
		t.Errorf("the sensor that failed inside a window counted %d probes, want 5 — "+
			"the five that were expected are not part of what is being asked about",
			got.Total)
	}
	if got.Excused != 5 {
		t.Errorf("it reported %d probes as maintenance, want 5", got.Excused)
	}
	if got.OK != 0 || got.Down != 5 {
		t.Errorf("it read %d ok of %d down, want 0 and 5", got.OK, got.Down)
	}

	// The control, which is what says the number above came from the marker rather
	// than from there being fewer probes to count.
	control, err := st.Uptime(ctx, unplanned.ID, from, to)
	if err != nil {
		t.Fatalf("uptime for the control sensor: %v", err)
	}
	if control.Total != 10 || control.Excused != 0 {
		t.Errorf("the sensor with the same ten failures and no window counted %d probes "+
			"of which %d maintenance, want 10 and 0", control.Total, control.Excused)
	}

	// And the two figures are the same shape otherwise, which is what makes this a
	// comparison at all.
	if got.Percent != control.Percent {
		t.Errorf("both sensors failed every probe, and they read %.1f%% and %.1f%%",
			got.Percent, control.Percent)
	}
}

// TestAMaintenanceWindowSurvivesTheRollup is the half that a naive implementation
// gets wrong a week after it looks as though it works.
//
// Everything above is answered from the raw rows while they are still there. Once
// retention.raw has passed they are gone, and the only surviving record of that
// hour is one row in results_hourly that was written once. If the maintenance count
// is not carried into it, the window stops being excluded on exactly the day the
// raw rows fall off — silently, and in the direction that makes an installation look
// worse than it is.
func TestAMaintenanceWindowSurvivesTheRollup(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	// A raw retention of an hour, so that Maintain() prunes what it has just
	// folded and the assertion below cannot accidentally be reading raw rows.
	st.SetRetention(time.Hour, 30*24*time.Hour)
	c := mustSensor(t, st, model.Sensor{
		Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local",
	})

	base := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	const perWindow = 6
	for i := 0; i < perWindow*2; i++ {
		r := model.Result{SensorID: c.ID, TS: base.Add(time.Duration(i) * time.Minute), Success: false}
		if i >= perWindow {
			r.Excused = true
		}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	// Narrow enough to be answered from the raw table, which retention.raw has
	// just been set to an hour — a two-hour window would be answered from the
	// hourly rollup, which is the thing this test is trying to do without.
	raw, err := st.Uptime(ctx, c.ID, base.Add(-time.Minute), base.Add(13*time.Minute))
	if err != nil {
		t.Fatalf("uptime from raw: %v", err)
	}
	if raw.Total != perWindow || raw.Excused != perWindow {
		t.Fatalf("reading the raw rows directly gave %d probes of which %d "+
			"maintenance, want %d and %d", raw.Total, raw.Excused, perWindow, perWindow)
	}

	if err := st.Maintain(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	var remaining int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&remaining); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("raw retention of an hour left %d rows; this test would be reading "+
			"the rows it is meant to be missing", remaining)
	}

	// A month, which is long enough that granularityFor picks the daily rollup
	// rather than the hourly one — the tier that is kept forever, and therefore the
	// one that has to be right.
	monthly, err := st.Uptime(ctx, c.ID, base.Add(-30*24*time.Hour), base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("uptime from the rollup: %v", err)
	}
	if monthly.Total != raw.Total || monthly.OK != raw.OK || monthly.Excused != raw.Excused {
		t.Errorf("the daily rollup reads %d probes / %d ok / %d maintenance, and the raw "+
			"rows read %d / %d / %d before they were pruned.\nThe maintenance count has "+
			"to be carried into every rollup row: this is the tier older than "+
			"retention.raw, and there is nothing left to recompute it from.",
			monthly.Total, monthly.OK, monthly.Excused, raw.Total, raw.OK, raw.Excused)
	}
}

// TestAMaintenanceProbeThatSucceededIsStillLetGo checks the case that makes the
// subtraction need two counters rather than one.
//
// A window is a period, not a promise, and the box may come back early. Taking the
// maintenance probes out of the denominator alone would leave those successes in the
// numerator with nothing below them, and the card would read 103%.
func TestAMaintenanceProbeThatSucceededIsStillLetGo(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local"})

	base := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	// Four probes, all marked maintenance, three of which succeeded — the box came
	// back in the middle of the window and nobody had ended it yet.
	for i := 0; i < 4; i++ {
		r := model.Result{
			SensorID: c.ID, TS: base.Add(time.Duration(i) * time.Minute),
			Success: i > 0, Excused: true,
		}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := st.Uptime(ctx, c.ID, base.Add(-time.Hour), base.Add(time.Hour))
	if err != nil {
		t.Fatalf("uptime: %v", err)
	}
	if got.Total != 0 || got.OK != 0 {
		t.Errorf("read %d probes of which %d ok, want nothing counted at all — every "+
			"probe was inside the window", got.Total, got.OK)
	}
	if got.Percent > 100 {
		t.Errorf("uptime reads %.1f%%, which is above 100 and therefore wrong whatever "+
			"the numbers behind it are", got.Percent)
	}

	// The same through the rollup, where the two counters are separate columns and
	// the mistake is a different one to make.
	if err := st.Maintain(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("maintain: %v", err)
	}
	rolled, err := st.Uptime(ctx, c.ID, base.Add(-30*24*time.Hour), base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("uptime from the rollup: %v", err)
	}
	if rolled.Percent > 100 {
		t.Errorf("after the rollup the uptime reads %.1f%%", rolled.Percent)
	}
}

// TestAWindowOnAGroupCoversTheWholeGroup holds the second half of what a window can
// name. A group is a string on the sensors rather than a table, so this is the test
// that says the string is matched and not the id.
func TestAWindowOnAGroupCoversTheWholeGroup(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	inGroup := mustSensor(t, st, model.Sensor{
		Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local", Group: "homelab",
	})
	elsewhere := mustSensor(t, st, model.Sensor{
		Name: "vps", Type: model.SensorHTTP, Target: "https://example.org", Group: "rented",
	})

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	w := model.MaintenanceWindow{
		Group: "homelab", Kind: model.WindowOnce, StartsAt: &from, EndsAt: &to,
		Timezone: "UTC", Enabled: true,
	}
	if err := st.CreateWindow(ctx, &w); err != nil {
		t.Fatalf("create window: %v", err)
	}

	windows, err := st.ListWindows(ctx)
	if err != nil {
		t.Fatalf("list windows: %v", err)
	}
	at := time.Now().UTC()
	if !ActiveWindows(windows, inGroup, at) {
		t.Errorf("a window on the group %q does not cover a sensor in it", inGroup.Group)
	}
	if ActiveWindows(windows, elsewhere, at) {
		t.Errorf("a window on the group %q covers a sensor in the group %q — a group name "+
			"is matched, and one that does not match silences nothing rather than "+
			"everything", inGroup.Group, elsewhere.Group)
	}
}

// TestAWindowThatIsOffCoversNothing is the switch the form offers, and the reason it
// has to be checked before anything else: nothing else in this file is a promise
// about silence.
func TestAWindowThatIsOffCoversNothing(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local"})

	from := time.Now().UTC().Add(-time.Hour)
	to := time.Now().UTC().Add(time.Hour)
	w := windowFor(t, st, c, from, to)
	if !w.Covers(c, time.Now().UTC()) {
		t.Fatalf("a fresh window does not cover the sensor it names")
	}

	w.Enabled = false
	if err := st.UpdateWindow(ctx, w.ID, &w); err != nil {
		t.Fatalf("update window: %v", err)
	}
	windows, err := st.ListWindows(ctx)
	if err != nil {
		t.Fatalf("list windows: %v", err)
	}
	if ActiveWindows(windows, c, time.Now().UTC()) {
		t.Errorf("a window that was switched off still silences the sensor")
	}
}

// TestValidateWindowRefusesWhatCannotBeUsed walks the refusals that matter, and the
// two shapes are held to different ones.
func TestValidateWindowRefusesWhatCannotBeUsed(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "nas", Type: model.SensorHTTP, Target: "http://nas.local"})

	now := time.Now().UTC()
	later := now.Add(time.Hour)
	earlier := now.Add(-time.Hour)

	cases := []struct {
		name string
		w    model.MaintenanceWindow
	}{
		{"neither a sensor nor a group", model.MaintenanceWindow{
			Kind: model.WindowOnce, StartsAt: &earlier, EndsAt: &later, Timezone: "UTC"}},
		{"a sensor and a group at once", model.MaintenanceWindow{
			SensorID: c.ID, Group: "homelab", Kind: model.WindowOnce,
			StartsAt: &earlier, EndsAt: &later, Timezone: "UTC"}},
		{"a one-off window with no end", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowOnce, StartsAt: &earlier, Timezone: "UTC"}},
		{"a one-off window that ends before it begins", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowOnce, StartsAt: &later, EndsAt: &earlier, Timezone: "UTC"}},
		{"a weekly window with no days", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowWeekly, StartMin: 60, EndMin: 120, Timezone: "UTC"}},
		{"a weekly window on a day that does not exist", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowWeekly, Weekdays: []int{7},
			StartMin: 60, EndMin: 120, Timezone: "UTC"}},
		{"a weekly window covering all 24 hours", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowWeekly, Weekdays: []int{1},
			StartMin: 300, EndMin: 300, Timezone: "UTC"}},
		{"a time zone this build does not know", model.MaintenanceWindow{
			SensorID: c.ID, Kind: model.WindowOnce, StartsAt: &earlier, EndsAt: &later,
			Timezone: "Mars/Olympus"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := st.CreateWindow(ctx, &tc.w)
			if err == nil {
				t.Fatalf("stored a window with %s", tc.name)
			}
		})
	}

	// And the one that is allowed, because a window that covers nothing is a
	// nuisance while a window that covers everything is an installation that has
	// stopped alerting: a group no sensor carries.
	ok := model.MaintenanceWindow{
		Group: "nobody-uses-this", Kind: model.WindowOnce,
		StartsAt: &earlier, EndsAt: &later, Timezone: "UTC", Enabled: true,
	}
	if err := st.CreateWindow(ctx, &ok); err != nil {
		t.Errorf("a window on a group no sensor carries was refused: %v\nIt silences "+
			"nothing, which is what a typo should do, and it is visible in the list.", err)
	}
}

// TestAWindowPastMidnightBelongsToTheDayBefore is the case a weekly form invites and
// a naive implementation gets wrong.
//
// "Sunday 23:00 to 01:00" is most of Sunday night. A window that only compared the
// clock would silence nothing after midnight, and Monday morning would report an
// outage the operator had explicitly said to expect.
func TestAWindowPastMidnightBelongsToTheDayBefore(t *testing.T) {
	// 2026-10-18 is a Sunday.
	sunday := time.Date(2026, 10, 18, 22, 0, 0, 0, time.UTC)
	// Addressed to a sensor id, so that these cases turn on the clock alone and
	// not on whether the sensor matches.
	w := model.MaintenanceWindow{
		SensorID: 1, Kind: model.WindowWeekly, Weekdays: []int{0},
		StartMin: 23 * 60, EndMin: 60, Timezone: "UTC", Enabled: true,
	}
	c := model.Sensor{ID: 1}

	if !w.Covers(c, sunday.Add(time.Hour)) { // Sunday 23:00
		t.Errorf("23:00 on the named day is not inside a 23:00-01:00 window")
	}
	if !w.Covers(c, sunday.Add(2*time.Hour)) { // Monday 00:00
		t.Errorf("00:00 the next morning is not inside a 23:00-01:00 window on the " +
			"day before; a window that ends when the clock passes midnight is not " +
			"a window anybody wants")
	}
	if !w.Covers(c, sunday.Add(2*time.Hour+59*time.Minute)) { // Monday 00:59
		t.Errorf("00:59 the next morning is not inside a 23:00-01:00 window")
	}
	if w.Covers(c, sunday.Add(3*time.Hour)) { // Monday 01:00
		t.Errorf("01:00 is past the end of a 23:00-01:00 window")
	}
	if w.Covers(c, sunday.Add(25*time.Hour)) { // Monday 23:00 — not a named day
		t.Errorf("23:00 on a day that was not named is inside the window")
	}
}

// TestAWindowIsReadInItsOwnZone holds the one place a zone other than UTC appears.
//
// Everything else in this program stamps UTC, and a person is not UTC: "Sunday at
// three" means three on their wall, and the answer has to change when the clocks do.
func TestAWindowIsReadInItsOwnZone(t *testing.T) {
	w := model.MaintenanceWindow{
		SensorID: 1, Kind: model.WindowWeekly, Weekdays: []int{0},
		StartMin: 3 * 60, EndMin: 4 * 60, Timezone: "Europe/Berlin", Enabled: true,
	}
	c := model.Sensor{ID: 1}

	// Sunday 03:30 in Berlin, in October, which is 01:30 UTC.
	berlin := time.Date(2026, 10, 18, 1, 30, 0, 0, time.UTC)
	if !w.Covers(c, berlin) {
		t.Errorf("03:30 Berlin time is not inside a 03:00-04:00 window read in " +
			"Europe/Berlin; the zone in the row is what the boundaries mean")
	}
	// The same instant would be 01:30 in a UTC window, which is outside it. The
	// point is that these two disagree.
	utc := w
	utc.Timezone = "UTC"
	if utc.Covers(c, berlin) {
		t.Errorf("the window covers the instant whether it is read as Berlin or as " +
			"UTC, so the zone is not being used at all")
	}
}

// TestDeletingTheExampleDataLetsARealSensorGo is the promise the documentation makes
// about the one-click removal, and the reason the parent column says ON DELETE SET NULL
// rather than RESTRICT.
//
// Two things have to be true at once, and they pull in opposite directions. Example
// sensors that depend on each other must both go — they are one invented story, and
// leaving half of it behind would be a dashboard with a check hanging off nothing. A
// sensor the operator made and pointed at an example one must not go: it is theirs, it
// is watching something real, and the only thing that has changed about it is that
// whatever it sat behind is no longer there to excuse it.
//
// RESTRICT would make the bulk delete fail; CASCADE would take the operator's sensor
// with it. SET NULL is the only one of the three that does what the button promises.
func TestDeletingTheExampleDataLetsARealSensorGo(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// The invented pair, marked the way the setup marks what it creates.
	parent := mustSensor(t, st, model.Sensor{
		Name: "Rented server: SSH", Type: model.SensorTCP, Target: "vps.example.net:22",
		Enabled: true, Origin: model.OriginExample,
	})
	child := mustSensor(t, st, model.Sensor{
		Name: "Rented server: web", Type: model.SensorHTTP, Target: "https://vps.example.net",
		Enabled: true, Origin: model.OriginExample, Parent: parent.Name,
	})

	// And one of the operator's own, sitting behind an invented sensor.
	mine := mustSensor(t, st, model.Sensor{
		Name: "My NAS", Type: model.SensorHTTP, Target: "http://nas.lan",
		Enabled: true, Parent: parent.Name,
	})

	deleted, err := st.DeleteExampleData(ctx)
	if err != nil {
		t.Fatalf("delete the example data: %v", err)
	}
	if deleted.Sensors != 2 {
		t.Errorf("removed %d example sensor(s), want 2", deleted.Sensors)
	}

	// Both halves of the invented story are gone.
	for _, name := range []string{parent.Name, child.Name} {
		if _, err := st.GetSensorByName(ctx, name); err == nil {
			t.Errorf("%q is still there after the example data was removed", name)
		}
	}

	// And the operator's sensor is not, and is no longer pointing at something that
	// does not exist — which would be a foreign key violation the next write would
	// trip over, or a parent that silently excuses it forever.
	got, err := st.GetSensorByName(ctx, mine.Name)
	if err != nil {
		t.Fatalf("the sensor made by hand was removed with the example data: %v", err)
	}
	if got.Parent != "" {
		t.Errorf("it still names %q as its parent, and that sensor is gone", got.Parent)
	}
	if !got.Enabled {
		t.Errorf("it came out disabled")
	}
}
