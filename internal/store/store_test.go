package store

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// discardLog is the logger every test hands to Open, which is happy with one it
// never uses.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := Open(path, discardLog())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func mustSensor(t *testing.T, st *Store, c model.Sensor) model.Sensor {
	t.Helper()
	if err := st.CreateSensor(context.Background(), &c); err != nil {
		t.Fatalf("create sensor %q: %v", c.Name, err)
	}
	return c
}

// ---------------------------------------------------------------------------
// the timestamp format everything else depends on
// ---------------------------------------------------------------------------

// TestTimeFormatRoundTrips pins the assumption that a formatted timestamp parses
// back to exactly the same instant.
func TestTimeFormatRoundTrips(t *testing.T) {
	cases := []time.Time{
		time.Date(2026, 10, 8, 19, 30, 0, 0, time.UTC),
		time.Date(2026, 10, 8, 19, 30, 0, 500_000_000, time.UTC),
		time.Date(2026, 1, 1, 0, 0, 0, 1, time.UTC),
		time.Date(2026, 12, 31, 23, 59, 59, 999_999_999, time.UTC),
	}
	for _, want := range cases {
		got := parseTime(fmtTime(want))
		if !got.Equal(want) {
			t.Errorf("round trip of %s produced %s", want, got)
		}
		if n := len(fmtTime(want)); n != len(timeFormat) {
			t.Errorf("formatted %s has width %d, want %d: %q",
				want, n, len(timeFormat), fmtTime(want))
		}
	}
}

// TestTimeFormatSortsChronologically is the reason timeFormat exists. Fixed
// width is not cosmetic: SQLite compares TEXT byte by byte, so a layout whose
// width varies with the value sorts incorrectly.
func TestTimeFormatSortsChronologically(t *testing.T) {
	early := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	late := time.Date(2026, 10, 8, 12, 0, 0, 500_000_000, time.UTC)

	if fmtTime(early) >= fmtTime(late) {
		t.Errorf("fixed-width format does not sort: %q >= %q", fmtTime(early), fmtTime(late))
	}

	// The same pair under RFC3339Nano sorts the wrong way round, because the
	// trailing zeros of the first value are dropped and 'Z' (0x5A) compares
	// greater than '.' (0x2E). This is the trap the fixed layout avoids.
	if early.Format(time.RFC3339Nano) < late.Format(time.RFC3339Nano) {
		t.Skip("RFC3339Nano happened to sort correctly on this pair")
	}
}

func TestFmtTimeIsUTC(t *testing.T) {
	loc := time.FixedZone("CEST", 2*3600)
	local := time.Date(2026, 10, 8, 21, 0, 0, 0, loc)
	got := fmtTime(local)
	if want := "2026-10-08T19:00:00.000000000Z"; got != want {
		t.Errorf("fmtTime(%s) = %q, want %q", local, got, want)
	}
}

// ---------------------------------------------------------------------------
// retention and rollups
// ---------------------------------------------------------------------------

func TestRollupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	now := time.Now().UTC().Truncate(time.Hour)
	// Two complete hours of history, so the rollup has something to fold.
	for i := 0; i < 20; i++ {
		r := model.Result{SensorID: c.ID, TS: now.Add(-2*time.Hour + time.Duration(i)*time.Minute), Success: i%5 != 0, LatencyMS: 10 + float64(i)}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	for run := 1; run <= 3; run++ {
		if err := st.Maintain(ctx, time.Now().UTC()); err != nil {
			t.Fatalf("maintain run %d: %v", run, err)
		}
		var samples, ok int
		if err := st.DB().QueryRowContext(ctx,
			`SELECT samples, ok FROM results_hourly WHERE sensor_id = ?`, c.ID).Scan(&samples, &ok); err != nil {
			t.Fatalf("read rollup after run %d: %v", run, err)
		}
		// Re-running must replace the aggregate, never accumulate it.
		if samples != 20 {
			t.Fatalf("run %d: rolled up %d samples, want 20", run, samples)
		}
		if ok != 16 {
			t.Fatalf("run %d: rolled up %d successes, want 16", run, ok)
		}
	}
}

func TestDailyRollupSurvivesRawPruning(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	st.SetRetention(time.Hour, 30*24*time.Hour)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	// Yesterday at noon, so the twelve probes sit unambiguously inside one
	// completed day whenever this test runs. A relative offset is not enough:
	// "twenty hours ago" is the day before yesterday in the morning and today
	// just after midnight, and today's bucket is not rolled up yet — which made
	// this test fail between 20:00 and 24:00 UTC.
	old := time.Now().UTC().Truncate(24 * time.Hour).Add(-12 * time.Hour)
	for i := 0; i < 12; i++ {
		r := model.Result{SensorID: c.ID, TS: old.Add(time.Duration(i) * time.Minute), Success: true, LatencyMS: 25}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := st.Maintain(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("maintain: %v", err)
	}

	var raw int
	if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&raw); err != nil {
		t.Fatalf("count raw: %v", err)
	}
	if raw != 0 {
		t.Fatalf("raw retention of 1h left %d rows", raw)
	}

	// Summed rather than read from one row: the window may straddle midnight UTC,
	// which splits the twelve probes across two daily buckets.
	var daily int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(samples), 0) FROM results_daily WHERE sensor_id = ?`, c.ID).Scan(&daily); err != nil {
		t.Fatalf("daily rollup missing after pruning: %v", err)
	}
	if daily != 12 {
		t.Fatalf("daily rollup holds %d samples, want 12", daily)
	}
}

// TestSeriesPicksOneResolution guards the "never count a probe twice" rule: a
// window answered from hourly rollups must not also add the raw rows beneath it.
func TestSeriesPicksOneResolution(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	st.SetRetention(2*time.Hour, 48*time.Hour)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	now := time.Now().UTC()
	// Six hours back, one probe per minute.
	for i := 0; i < 360; i++ {
		r := model.Result{SensorID: c.ID, TS: now.Add(-6*time.Hour + time.Duration(i)*time.Minute), Success: true, LatencyMS: 30}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if err := st.Maintain(ctx, now); err != nil {
		t.Fatalf("maintain: %v", err)
	}

	// A short window must be answered from raw rows.
	short, err := st.Series(ctx, c.ID, now.Add(-90*time.Minute), now, 10)
	if err != nil {
		t.Fatalf("short series: %v", err)
	}
	shortTotal := sumTotals(short)
	if shortTotal == 0 || shortTotal > 95 {
		t.Errorf("short window counted %d probes, want roughly 90 from raw data", shortTotal)
	}

	// The long window spans the whole six hours. Whichever table answers it, the
	// total must stay near 360 — double counting would roughly double it.
	long, err := st.Series(ctx, c.ID, now.Add(-6*time.Hour), now, 20)
	if err != nil {
		t.Fatalf("long series: %v", err)
	}
	longTotal := sumTotals(long)
	if longTotal < 280 || longTotal > 380 {
		t.Errorf("long window counted %d probes, want roughly 360 (double counting would inflate it)", longTotal)
	}
}

func sumTotals(points []model.SeriesPoint) int {
	total := 0
	for _, p := range points {
		total += p.Total
	}
	return total
}

func TestSeriesKeepsGapsAsGaps(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	now := time.Now().UTC()
	// One probe now, nothing for the rest of the window.
	r := model.Result{SensorID: c.ID, TS: now.Add(-time.Minute), Success: true, LatencyMS: 42}
	if err := st.InsertResult(ctx, &r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	points, err := st.Series(ctx, c.ID, now.Add(-6*time.Hour), now, 12)
	if err != nil {
		t.Fatalf("series: %v", err)
	}
	if len(points) != 12 {
		t.Fatalf("got %d buckets, want exactly 12", len(points))
	}
	empty := 0
	for _, p := range points {
		if p.Total == 0 {
			empty++
		}
	}
	if empty != 11 {
		t.Errorf("%d empty buckets, want 11: a gap must not read as a probe", empty)
	}
}

// ---------------------------------------------------------------------------
// state reconstruction after a restart
// ---------------------------------------------------------------------------

func TestTrailingFailuresFeedsCrashRecovery(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	now := time.Now().UTC()
	lastSuccess := now.Add(-10 * time.Minute)
	rows := []model.Result{
		{TS: lastSuccess, Success: true, LatencyMS: 20},
		{TS: now.Add(-8 * time.Minute), Success: false},
		{TS: now.Add(-7 * time.Minute), Success: false},
		{TS: now.Add(-6 * time.Minute), Success: false},
	}
	for _, r := range rows {
		r.SensorID = c.ID
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	count, firstFailure, gotLastSuccess, err := st.TrailingFailures(ctx, c.ID, 10)
	if err != nil {
		t.Fatalf("trailing failures: %v", err)
	}
	if count != 3 {
		t.Errorf("trailing failure count = %d, want 3", count)
	}
	if !firstFailure.Equal(now.Add(-8*time.Minute).Truncate(time.Second)) &&
		firstFailure.Sub(now.Add(-8*time.Minute)) > time.Second {
		t.Errorf("first failure = %s, want about %s", firstFailure, now.Add(-8*time.Minute))
	}
	if gotLastSuccess.Sub(lastSuccess).Abs() > time.Second {
		t.Errorf("last success = %s, want about %s", gotLastSuccess, lastSuccess)
	}
}

// ---------------------------------------------------------------------------
// sensors, channels and their relationship
// ---------------------------------------------------------------------------

func TestSensorChannelLinksRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	ch := model.Channel{Name: "alerts", Type: "ntfy", Config: map[string]string{"topic": "homelab"}}
	if err := st.CreateChannel(ctx, &ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	c := mustSensor(t, st, model.Sensor{
		Name: "web", Type: model.SensorHTTP, Target: "https://example.org",
		Channels: []int64{ch.ID},
	})

	got, err := st.GetSensor(ctx, c.ID)
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if len(got.Channels) != 1 || got.Channels[0] != ch.ID {
		t.Fatalf("sensor channels = %v, want [%d]", got.Channels, ch.ID)
	}

	// Replacing the links must not leave the old ones behind.
	if err := st.UpdateSensor(ctx, &model.Sensor{
		ID: c.ID, Name: "web", Type: model.SensorHTTP, Target: "https://example.org",
	}); err != nil {
		t.Fatalf("update sensor: %v", err)
	}
	got, err = st.GetSensor(ctx, c.ID)
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if len(got.Channels) != 0 {
		t.Errorf("channels after clearing = %v, want none", got.Channels)
	}

	uses, err := st.CountSensorsUsingChannel(ctx, ch.ID)
	if err != nil {
		t.Fatalf("count uses: %v", err)
	}
	if uses != 0 {
		t.Errorf("channel still reported as used by %d sensors", uses)
	}
}

func TestDuplicateSensorNameIsRejected(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	err := st.CreateSensor(ctx, &model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://other.example"})
	if err == nil {
		t.Fatal("creating a second sensor with the same name succeeded")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error %q does not mention the conflict", err)
	}
}

func TestDeletingSensorCascades(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	c := mustSensor(t, st, model.Sensor{Name: "web", Type: model.SensorHTTP, Target: "https://example.org"})

	if _, err := st.OpenIncident(ctx, c.ID, time.Now().UTC().Add(-time.Hour), "boom"); err != nil {
		t.Fatalf("open incident: %v", err)
	}
	r := model.Result{SensorID: c.ID, TS: time.Now().UTC(), Success: false}
	if err := st.InsertResult(ctx, &r); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if err := st.DeleteSensor(ctx, c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, table := range []string{"results", "incidents"} {
		var n int
		if err := st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s still holds %d rows after the sensor was deleted", table, n)
		}
	}
}

func TestSetSensorOrder(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	a := mustSensor(t, st, model.Sensor{Name: "a", Type: model.SensorHTTP, Target: "https://a.example"})
	b := mustSensor(t, st, model.Sensor{Name: "b", Type: model.SensorHTTP, Target: "https://b.example"})

	if err := st.SetSensorOrder(ctx, []int64{b.ID, a.ID}); err != nil {
		t.Fatalf("set order: %v", err)
	}
	list, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].Name != "b" {
		t.Fatalf("order = %v, want b first", names(list))
	}
}

func names(sensors []model.Sensor) []string {
	out := make([]string, len(sensors))
	for i, c := range sensors {
		out[i] = c.Name
	}
	return out
}

func TestSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	if got := st.Setting(ctx, "ui.theme", "auto"); got != "auto" {
		t.Errorf("missing setting = %q, want the default %q", got, "auto")
	}
	if err := st.SetSetting(ctx, "ui.theme", "dark"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got := st.Setting(ctx, "ui.theme", "auto"); got != "dark" {
		t.Errorf("setting = %q, want dark", got)
	}
	if err := st.DeleteSetting(ctx, "ui.theme"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := st.Setting(ctx, "ui.theme", "auto"); got != "auto" {
		t.Errorf("after delete = %q, want the default", got)
	}
}

func TestEnsureSecretIsStable(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	first, err := st.EnsureSecret(ctx, model.SettingSessionSecret)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	second, err := st.EnsureSecret(ctx, model.SettingSessionSecret)
	if err != nil {
		t.Fatalf("ensure again: %v", err)
	}
	// It must be preserved across calls: a rotating session secret would log
	// every user out on restart.
	if first != second {
		t.Fatalf("secret changed between calls: %q then %q", first, second)
	}
	if len(first) != 64 {
		t.Errorf("secret is %d hex characters, want 64 for 32 bytes", len(first))
	}
}

// ---------------------------------------------------------------------------
// read-only opening, the guarantee weatherglass doctor rests on
// ---------------------------------------------------------------------------

// TestOpenReadOnlyReadsWithoutWriting is the contract doctor depends on: the
// handle answers every read the report needs and refuses every write. If the
// second half ever stops holding, doctor stops being a diagnostic and becomes a
// repair tool.
func TestOpenReadOnlyReadsWithoutWriting(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "test.db")

	w, err := Open(path, discardLog())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	mustSensor(t, w, model.Sensor{Name: "gateway", Type: model.SensorHTTP, Target: "http://example.org"})
	if err := w.SetSetting(ctx, model.SettingLegacyPasswordHash, "hash"); err != nil {
		t.Fatalf("set setting: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := OpenReadOnly(path, discardLog())
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer st.Close()

	sensors, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if len(sensors) != 1 || sensors[0].Name != "gateway" {
		t.Fatalf("sensors = %+v, want the one written above", sensors)
	}
	if got, err := st.GetSetting(ctx, model.SettingLegacyPasswordHash); err != nil || got != "hash" {
		t.Errorf("setting = %q, %v; want %q", got, err, "hash")
	}
	if v, err := st.SchemaVersion(ctx); err != nil || v != LatestSchemaVersion() {
		t.Errorf("schema = %d, %v; want %d", v, err, LatestSchemaVersion())
	}
	if _, err := st.ProbeCount(ctx); err != nil {
		t.Errorf("probe count: %v", err)
	}

	// Every write has to fail. The driver reports SQLITE_READONLY, but pinning
	// its wording would tie this test to modernc.org/sqlite, so only the refusal
	// itself is asserted.
	if err := st.SetSetting(ctx, "ui.theme", "dark"); err == nil {
		t.Error("SetSetting succeeded on a read-only handle")
	}
	c := model.Sensor{Name: "other", Type: model.SensorHTTP, Target: "http://example.org"}
	if err := st.CreateSensor(ctx, &c); err == nil {
		t.Error("CreateSensor succeeded on a read-only handle")
	}
	if _, err := st.DB().ExecContext(ctx, "CREATE TABLE probe_table (x)"); err == nil {
		t.Error("DDL succeeded on a read-only handle")
	}
}

// TestOpenReadOnlyRefusesToCreateTheFile: mode=ro does not create, which is why
// doctor can point at the wrong path without leaving a database behind. A file
// that appears here would be a database the daemon then treats as real.
func TestOpenReadOnlyRefusesToCreateTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.db")

	st, err := OpenReadOnly(path, discardLog())
	if err == nil {
		st.Close()
		t.Fatal("OpenReadOnly created a database that did not exist")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("after a failed read-only open, %s exists (stat: %v)", path, statErr)
	}
}

// TestOpenReadOnlyRejectsGarbage: a file that is not a database has to be an
// error, not a silently empty one. Otherwise the report would say "0 sensors"
// about a file that is simply damaged, which is the opposite of the answer
// somebody ran doctor to get.
func TestOpenReadOnlyRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "garbage.db")
	if err := os.WriteFile(path, make([]byte, 2048), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	st, err := OpenReadOnly(path, discardLog())
	if err == nil {
		st.Close()
		t.Fatal("OpenReadOnly accepted a file that is not a database")
	}
}

// TestLegacyDatabaseIsRefused: a file written before the rename still has its
// data in a `checks` table. Migration uses CREATE TABLE IF NOT EXISTS, so
// without the preflight it would be accepted and then fail on the first query
// with "no such table: sensors" — a message that names the symptom and not the
// cause. The database is opened here for real, so the guard is exercised on the
// path a user actually takes.
func TestLegacyDatabaseIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	// Build the shape of a pre-rename file: a schema_migrations table claiming
	// version 1, and a checks table where the data still lives.
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	_, err = raw.Exec(`
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL);
INSERT INTO schema_migrations (version, name, applied_at) VALUES (1, 'init', '2026-01-01T00:00:00Z');
CREATE TABLE checks (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE);
`)
	if err != nil {
		t.Fatalf("build legacy database: %v", err)
	}
	_ = raw.Close()

	st, err := Open(path, discardLog())
	if err == nil {
		_ = st.Close()
		t.Fatal("a database from before the rename was opened instead of refused")
	}
	if !strings.Contains(err.Error(), "predates the rename") {
		t.Errorf("error = %q, want it to explain that the file is from before the rename", err)
	}
}

// ---------------------------------------------------------------------------
// upgrading an installation, and the schema that comes out of it
// ---------------------------------------------------------------------------

// v1Database writes a database exactly as version 1 of the schema left it: the
// first migration applied, nothing after it, and the migration table saying so.
// Everything the later migrations add — the users, the origin column — is absent,
// which is the state every existing installation is in.
func v1Database(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	defer db.Close()

	if _, err := db.Exec(`
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at TEXT NOT NULL);
`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	m := migrations[0]
	if _, err := db.Exec(m.sql); err != nil {
		t.Fatalf("apply migration %d (%s): %v", m.version, m.name, err)
	}
	if _, err := db.Exec(
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("record migration %d: %v", m.version, err)
	}
}

func sensorsColumns(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.DB().Query(`SELECT name FROM pragma_table_info('sensors') ORDER BY name`)
	if err != nil {
		t.Fatalf("read the sensors columns: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column name: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the sensors columns: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("sensors has no columns at all, which means the table was not found")
	}
	return out
}

// An existing installation and a fresh one must end up with the same sensors
// table. They get there by different routes — the old file by ALTER TABLE, the new
// one by CREATE followed by ALTER TABLE — and if those two ever diverge, the
// queries that name origin work on exactly one of them.
//
// The tempting shortcut is to add the column to the v1 CREATE statement instead.
// That looks identical on a fresh file and leaves every existing installation
// without it, which is the failure this test exists to prevent.
func TestAnUpgradedDatabaseHasTheSameSensorsColumnsAsAFreshOne(t *testing.T) {
	dir := t.TempDir()

	fresh, err := Open(filepath.Join(dir, "fresh.db"), discardLog())
	if err != nil {
		t.Fatalf("open a fresh database: %v", err)
	}
	defer fresh.Close()
	want := sensorsColumns(t, fresh)

	old := filepath.Join(dir, "old.db")
	v1Database(t, old)

	upgraded, err := Open(old, discardLog())
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer upgraded.Close()

	if got := sensorsColumns(t, upgraded); !slices.Equal(got, want) {
		t.Errorf("an upgraded database has sensors %v, a fresh one %v.\n"+
			"The two must be identical: the same queries run against both.", got, want)
	}
	if !slices.Contains(want, "origin") {
		t.Errorf("sensors has no origin column (%v), so the example data cannot be told "+
			"apart from everything else", want)
	}
	if v, err := upgraded.SchemaVersion(context.Background()); err != nil || v != LatestSchemaVersion() {
		t.Errorf("after upgrading, schema = %d, %v; want %d", v, err, LatestSchemaVersion())
	}
}

// An installation that has been running for months has a password and no users.
// Upgrading it must not lock anybody out: the password it already trusts becomes
// the administrator's, and the settings that held it are removed so that "who may
// sign in" has exactly one answer from then on.
func TestUpgradingAdoptsTheOldPasswordAndTheOldPreferences(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	v1Database(t, path)

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := raw.Exec(`
INSERT INTO settings (key, value, updated_at) VALUES
    ('auth.password_hash', 'the-old-bcrypt-hash', '2026-01-01T00:00:00Z'),
    ('ui.theme', 'dark', '2026-01-01T00:00:00Z'),
    ('ui.mode', 'simple', '2026-01-01T00:00:00Z'),
    ('ui.theme_colour_of_the_day', 'chartreuse', '2026-01-01T00:00:00Z'),
    ('retention.raw', '168h', '2026-01-01T00:00:00Z');
`); err != nil {
		t.Fatalf("seed the old settings: %v", err)
	}
	_ = raw.Close()

	st, err := Open(path, discardLog())
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()

	admin, err := st.GetUserByName(ctx, "admin")
	if err != nil {
		t.Fatalf("no administrator was created from the old password: %v", err)
	}
	if admin.Role != model.RoleAdmin {
		t.Errorf("role = %q, want admin: the account that replaces the only password must "+
			"be able to do everything that password could", admin.Role)
	}
	if admin.PasswordHash != "the-old-bcrypt-hash" {
		t.Errorf("the stored hash changed to %q; the existing password has to keep working",
			admin.PasswordHash)
	}

	// The preferences came across, and only the ones that are still settings.
	if admin.Pref(model.PrefTheme) != "dark" || admin.Pref(model.PrefMode) != "simple" {
		t.Errorf("preferences = %v, want the old theme and mode carried over", admin.Prefs)
	}
	if _, ok := admin.Prefs["theme_colour_of_the_day"]; ok {
		t.Errorf("an unknown ui.* key was carried into the preferences: %v", admin.Prefs)
	}
	if admin.Pref(model.PrefLanguage) != model.DefaultPrefs()[model.PrefLanguage] {
		t.Errorf("language = %q, want the default: an unset preference must fall back",
			admin.Pref(model.PrefLanguage))
	}

	// The old rows are gone, and the ones that were never auth or ui stay.
	if got := st.Setting(ctx, model.SettingLegacyPasswordHash, ""); got != "" {
		t.Errorf("the legacy password is still stored as %q; there is to be one source of "+
			"truth for who can sign in", got)
	}
	if got := st.Setting(ctx, "ui.theme", ""); got != "" {
		t.Errorf("the global ui.theme row survived: %q", got)
	}
	if got := st.Setting(ctx, "retention.raw", ""); got != "168h" {
		t.Errorf("retention.raw = %q, want it untouched: the migration moves auth and ui, "+
			"nothing else", got)
	}
}

// Running it twice must not happen, but the guard is worth pinning: the migration
// only fires on an empty users table, so a restart cannot rename or duplicate an
// administrator somebody has since created.
func TestAdoptingTheOldPasswordDoesNotRunTwice(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")
	v1Database(t, path)

	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO settings (key, value, updated_at)
		 VALUES ('auth.password_hash', 'first-hash', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = raw.Close()

	st, err := Open(path, discardLog())
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer st.Close()

	mine := &model.User{Username: "someone", PasswordHash: "second-hash", Role: model.RoleAdmin}
	if err := st.CreateUser(ctx, mine); err != nil {
		t.Fatalf("create user: %v", err)
	}
	// Put the legacy setting back, as a half-finished upgrade or a hand-edited file
	// would leave it, and migrate again.
	if err := st.SetSetting(ctx, model.SettingLegacyPasswordHash, "straggler"); err != nil {
		t.Fatalf("restore the legacy setting: %v", err)
	}
	if err := st.migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}

	n, err := st.CountUsers(ctx)
	if err != nil {
		t.Fatalf("count users: %v", err)
	}
	if n != 2 {
		t.Errorf("%d accounts after a second migrate, want the 2 that were there", n)
	}
	if _, err := st.GetUserByName(ctx, "admin"); err != nil {
		t.Errorf("the administrator disappeared: %v", err)
	}
	if _, err := st.GetUserByName(ctx, "someone"); err != nil {
		t.Errorf("the account created afterwards disappeared: %v", err)
	}
}

// TestManagedSensorTakesOverARowThatAlreadyExists is the sensor half of the site
// rule over in sites_test.go, and it was broken in exactly the same way.
//
// A sensor declared in the configuration file is read-only in the dashboard, and
// every guard that enforces that — edit, pause, move, delete — reads the managed
// column back out of the table. Only the insert ever wrote that column, so a
// sensor that existed before the file named it was never marked: it was made in
// the dashboard first, written into the file afterwards, and the dashboard could
// go on editing it. Every such edit looked as though it had worked, and the next
// apply quietly put it back.
func TestManagedSensorTakesOverARowThatAlreadyExists(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// Made in the dashboard first, which is the ordinary way to end up with the
	// row before the file has an opinion about it.
	made := mustSensor(t, st, model.Sensor{
		Name: "router", Type: model.SensorPing, Target: "192.0.2.1",
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true,
	})
	if made.Managed {
		t.Fatal("a sensor made in the dashboard came back managed")
	}

	// Now the file declares it.
	if err := st.UpsertManagedSensor(ctx, &model.Sensor{
		Name: "router", Type: model.SensorPing, Target: "192.0.2.2",
		Interval: 60, Timeout: 5, Retries: 1, Enabled: true,
	}); err != nil {
		t.Fatalf("upsert the managed sensor over an existing row: %v", err)
	}

	got, err := st.GetSensor(ctx, made.ID)
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if !got.Managed {
		t.Error("after the file took over the sensor it is still managed=false, " +
			"so the dashboard can still edit and delete it")
	}
	if got.Target != "192.0.2.2" {
		t.Errorf("target = %q, want the one the file declares", got.Target)
	}

	// And an ordinary save must not be able to set it free again, or the first
	// edit in the dashboard would undo the guard for good.
	got.Target = "192.0.2.3"
	if err := st.UpdateSensor(ctx, &got); err != nil {
		t.Fatalf("ordinary save of a managed sensor: %v", err)
	}
	again, err := st.GetSensor(ctx, made.ID)
	if err != nil {
		t.Fatalf("get sensor: %v", err)
	}
	if !again.Managed {
		t.Error("an ordinary save set a managed sensor free")
	}
}

// TestOpenNamesTheCauseWhenTheDirectoryIsNotWritable covers the failure a first
// install in a container actually hits: the image runs as uid 500, the bind mount on
// the host is owned by whoever created it, and the database cannot be created inside it.
// SQLite calls that "unable to open database file (14)" and says nothing else.
//
// Root ignores directory permissions altogether, so on a run as root there is no way to
// arrange the failure at all — the test skips rather than pretend otherwise.
func TestOpenNamesTheCauseWhenTheDirectoryIsNotWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes into a directory that has no write bit set")
	}

	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatalf("make a directory with no write bit: %v", err)
	}
	// t.TempDir's cleanup has to be able to take it away again.
	t.Cleanup(func() { os.Chmod(dir, 0o700) })

	_, err := Open(filepath.Join(dir, "weatherglass.db"), discardLog())
	if err == nil {
		t.Fatal("opening a database in a directory with no write bit succeeded")
	}
	if !strings.Contains(err.Error(), "not writable by uid") {
		t.Errorf("the error leaves the reader with SQLite's number and no cause: %v", err)
	}
	// Naming the uid is the point: it is what a person compares against the folder's
	// owner to see the mismatch for themselves.
	if want := fmt.Sprintf("uid %d", os.Getuid()); !strings.Contains(err.Error(), want) {
		t.Errorf("the error does not name %q, the uid that has to be able to write: %v", want, err)
	}
	if !strings.Contains(err.Error(), "chown -R") {
		t.Errorf("the error names the cause but not the fix: %v", err)
	}
}

// The other half of that promise, and the reason the probe exists at all: when
// permissions are not the problem, the message is left exactly as SQLite worded it.
// A hint that is wrong half the time teaches people to ignore it, and this one sits on
// the path of every start.
func TestUnwritableHintStaysQuietWhenTheDirectoryIsWritable(t *testing.T) {
	if got := unwritableHint(t.TempDir()); got != "" {
		t.Errorf("unwritableHint on a writable directory = %q, want nothing", got)
	}
}
