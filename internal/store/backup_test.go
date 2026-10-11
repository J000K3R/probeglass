package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// ---------------------------------------------------------------------------
// a populated installation to export
// ---------------------------------------------------------------------------

// backupFixture is one installation with something in every table a backup names.
type backupFixture struct {
	store   *Store
	sensor  model.Sensor
	channel model.Channel
	site    model.Site
	user    model.User
	// sampleHour is the completed hour the probe history sits in. It is a whole
	// hour in the past so that Maintain folds it into exactly one rollup row.
	sampleHour time.Time
	probes     int
	failures   int
}

// newBackupFixture builds an installation that has history: two complete hours of
// probes, folded into rollups, one closed incident, one alert channel wired to one
// sensor, one place, one account and one setting that is not a default.
//
// Retention is shortened on purpose. The default raw window is days long, and a test
// that means to prove "the hourly numbers survive a restore" has to be reading them
// from the hourly table rather than from raw rows that happen to still be there.
func newBackupFixture(t *testing.T) *backupFixture {
	t.Helper()
	ctx := context.Background()
	st := newTestStore(t)
	st.SetRetention(2*time.Hour, 48*time.Hour)

	f := &backupFixture{store: st}

	f.site = model.Site{Name: "Rack A", Kind: "local"}
	if err := st.CreateSite(ctx, &f.site); err != nil {
		t.Fatalf("create site: %v", err)
	}

	f.channel = model.Channel{
		Name: "telegram", Type: "telegram", Enabled: true,
		Config: map[string]string{"bot_token": "123456:AA-secret", "chat_id": "-100200"},
	}
	if err := st.CreateChannel(ctx, &f.channel); err != nil {
		t.Fatalf("create channel: %v", err)
	}

	f.sensor = mustSensor(t, st, model.Sensor{
		Name: "Router", Type: model.SensorPing, Target: "10.0.0.1",
		Site: "Rack A", Tags: []string{"lan", "core"},
		Channels: []int64{f.channel.ID},
		Options:  map[string]any{"count": float64(3)},
	})
	// A second sensor with no history at all, so that "it came back" is not only
	// ever tested on the one sensor that has rows.
	mustSensor(t, st, model.Sensor{Name: "Printer", Type: model.SensorTCP, Target: "10.0.0.9:9100"})

	f.user = model.User{Username: "homelab", Role: model.RoleAdmin, PasswordHash: "$2a$10$notarealhash"}
	if err := st.CreateUser(ctx, &f.user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if err := st.SetSetting(ctx, model.SettingTileURL, "https://tiles.example.org/{z}/{x}/{y}.png"); err != nil {
		t.Fatalf("set setting: %v", err)
	}

	f.sampleHour = time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)
	f.probes, f.failures = 60, 9
	for i := 0; i < f.probes; i++ {
		r := model.Result{
			SensorID:  f.sensor.ID,
			TS:        f.sampleHour.Add(time.Duration(i) * time.Minute),
			Success:   i%7 != 0,
			LatencyMS: 12 + float64(i),
			Message:   "probe from the fixture",
		}
		if err := st.InsertResult(ctx, &r); err != nil {
			t.Fatalf("insert result %d: %v", i, err)
		}
	}

	incidentAt := f.sampleHour.Add(5 * time.Minute)
	id, err := st.OpenIncident(ctx, f.sensor.ID, incidentAt, "no reply from 10.0.0.1")
	if err != nil {
		t.Fatalf("open incident: %v", err)
	}
	if err := st.CloseIncident(ctx, id, incidentAt.Add(20*time.Minute)); err != nil {
		t.Fatalf("close incident: %v", err)
	}

	if err := st.Maintain(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("maintain: %v", err)
	}

	// The fixture is worthless if the rollup is empty: the whole point is that the
	// history travels as aggregates.
	var samples int
	if err := st.DB().QueryRowContext(ctx,
		`SELECT COALESCE(SUM(samples),0) FROM results_hourly WHERE sensor_id = ?`, f.sensor.ID).Scan(&samples); err != nil {
		t.Fatalf("read rollup: %v", err)
	}
	if samples != f.probes {
		t.Fatalf("fixture rolled up %d samples, want %d — retention is wrong", samples, f.probes)
	}
	return f
}

// uptime reads the numbers a dashboard would show, over a window long enough to be
// answered from the hourly rollups rather than from raw rows.
//
// The sensor is looked up by name in the store being asked, not taken from the
// fixture. In the store that was restored into, Router is a different row with a
// different id, and asking about the id it had *before* the restore would answer
// "no probes at all" — which reads exactly like the history having been lost.
func (f *backupFixture) uptime(t *testing.T, st *Store) UptimeStats {
	t.Helper()
	ctx := context.Background()
	sensors, err := st.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	id := int64(0)
	for _, s := range sensors {
		if s.Name == f.sensor.Name {
			id = s.ID
		}
	}
	if id == 0 {
		t.Fatalf("%s is not in the store being asked about", f.sensor.Name)
	}
	now := time.Now().UTC()
	stats, err := st.Uptime(ctx, id, now.Add(-6*time.Hour), now)
	if err != nil {
		t.Fatalf("uptime: %v", err)
	}
	return stats
}

func (f *backupFixture) export(t *testing.T, opts ExportOptions) []byte {
	t.Helper()
	if opts.AppVersion == "" {
		opts.AppVersion = "test"
	}
	var buf bytes.Buffer
	counts, err := f.store.Export(context.Background(), &buf, opts)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if counts.Sensors == 0 || counts.Settings == 0 {
		t.Fatalf("export counted %+v, which cannot be right", counts)
	}
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// what is in the file
// ---------------------------------------------------------------------------

// TestExportSaysWhatItContains pins the shape of the document: the marker a reader
// checks first, and the two scopes as two different sets of keys.
//
// The keys are read from the raw JSON rather than from the struct, because the
// question is what is *in the file*. A pointer field left nil because of a mistake
// would still be absent from the JSON, and that is the difference between "the file
// says this table is empty" and "the file does not mention this table", which is what
// the restore is built on.
func TestExportSaysWhatItContains(t *testing.T) {
	f := newBackupFixture(t)

	t.Run("config names no history", func(t *testing.T) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(f.export(t, ExportOptions{Scope: BackupScopeConfig}), &doc); err != nil {
			t.Fatalf("the export is not valid JSON: %v", err)
		}
		for _, key := range []string{"settings", "sites", "channels", "sensors", "users"} {
			if _, ok := doc[key]; !ok {
				t.Errorf("a config backup does not mention %q", key)
			}
		}
		for _, key := range []string{"incidents", "results_hourly", "results_daily"} {
			if _, ok := doc[key]; ok {
				t.Errorf("a config backup mentions %q, so restoring one would empty it", key)
			}
		}
		var format string
		if err := json.Unmarshal(doc["format"], &format); err != nil || format != BackupFormat {
			t.Errorf("format = %q (%v), want %q", format, err, BackupFormat)
		}
		var scope string
		if err := json.Unmarshal(doc["scope"], &scope); err != nil || scope != BackupScopeConfig {
			t.Errorf("scope = %q (%v), want %q", scope, err, BackupScopeConfig)
		}
	})

	t.Run("history names no raw probe", func(t *testing.T) {
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(f.export(t, ExportOptions{Scope: BackupScopeHistory, Days: 30}), &doc); err != nil {
			t.Fatalf("the export is not valid JSON: %v", err)
		}
		for _, key := range []string{"incidents", "results_hourly", "results_daily", "sensors"} {
			if _, ok := doc[key]; !ok {
				t.Errorf("a history backup does not mention %q", key)
			}
		}
		// The raw probe table stays out on purpose: it is the largest table by far and
		// the one part of the history that is also recomputed by the search.
		if _, ok := doc["results"]; ok {
			t.Error(`a history backup names the raw "results" table`)
		}
		var hourly []BackupRollup
		if err := json.Unmarshal(doc["results_hourly"], &hourly); err != nil {
			t.Fatalf("decode hourly rollups: %v", err)
		}
		var samples int
		for _, r := range hourly {
			if r.Sensor == "Router" {
				samples += r.Samples
			}
		}
		if samples != f.probes {
			t.Errorf("the file carries %d samples for Router, want %d", samples, f.probes)
		}
	})
}

// TestExportKeepsSecretsInTheClear is unpleasant on purpose.
//
// Everywhere else in this program a channel's config leaves the server with its
// secrets replaced by StoredMarker. A backup that did that would restore a channel
// that never sends, and nobody would find out until the first outage was over. So
// the masking deliberately does not apply here — and this test is what stops somebody
// "tidying up" that oversight later without thinking about it.
func TestExportKeepsSecretsInTheClear(t *testing.T) {
	f := newBackupFixture(t)
	raw := string(f.export(t, ExportOptions{Scope: BackupScopeConfig}))

	if !strings.Contains(raw, "123456:AA-secret") {
		t.Error("the bot token is not in the backup, so a restored channel would never send")
	}
	if strings.Contains(raw, "__stored__") {
		t.Error("the backup carries a masked secret instead of the real one")
	}
	if !strings.Contains(raw, "$2a$10$notarealhash") {
		t.Error("the password hash is not in the backup, so nobody could sign in after a restore")
	}
}

// TestExportRejectsNonsense makes sure the route can answer a bad query with a
// message instead of an empty file. The store writes its opening brace only after
// these checks, which is what lets the route still send an error page.
func TestExportRejectsNonsense(t *testing.T) {
	f := newBackupFixture(t)
	for _, opts := range []ExportOptions{
		{Scope: "everything"},
		{Scope: BackupScopeHistory, Days: -1},
		{Scope: BackupScopeHistory, Days: maxBackupDays + 1},
	} {
		var buf bytes.Buffer
		if _, err := f.store.Export(context.Background(), &buf, opts); err == nil {
			t.Errorf("%+v was accepted", opts)
		}
		if buf.Len() != 0 {
			t.Errorf("%+v wrote %d bytes before refusing", opts, buf.Len())
		}
	}
}

// ---------------------------------------------------------------------------
// the round trip
// ---------------------------------------------------------------------------

// TestExportRestoreRoundTrip is the promise the whole feature makes: take everything
// out of one installation, put it into an empty one, and have the same installation.
func TestExportRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newBackupFixture(t)
	before := f.uptime(t, f.store)

	file := f.export(t, ExportOptions{Scope: BackupScopeHistory, Days: 30})

	// A new installation: same schema, nothing in it.
	fresh := newTestStore(t)
	fresh.SetRetention(2*time.Hour, 48*time.Hour)
	result, err := fresh.Restore(ctx, bytes.NewReader(file), RestoreOptions{SkipSafetyCopy: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}

	if result.Counts.Sensors != 2 || result.Counts.Channels != 1 || result.Counts.Sites != 1 {
		t.Errorf("restored %+v, want 2 sensors, 1 channel, 1 site", result.Counts)
	}
	if result.Counts.Incidents != 1 {
		t.Errorf("restored %d incidents, want 1", result.Counts.Incidents)
	}
	if result.Counts.Settings == 0 {
		t.Error("restored no settings at all")
	}

	// The account, with the hash that lets somebody sign in with the old password.
	user, err := fresh.GetUserByName(ctx, "homelab")
	if err != nil {
		t.Fatalf("the restoring account did not come back: %v", err)
	}
	if user.PasswordHash != "$2a$10$notarealhash" || user.Role != model.RoleAdmin {
		t.Errorf("restored account is %+v, want the hash and role from the file", user)
	}

	// The channel, with the token that makes it send.
	channel, err := fresh.GetChannelByName(ctx, "telegram")
	if err != nil {
		t.Fatalf("the channel did not come back: %v", err)
	}
	if channel.Config["bot_token"] != "123456:AA-secret" {
		t.Errorf("restored channel config = %v, want the real token", channel.Config)
	}

	// The sensor, wired to the channel, in its place, with its tags and options.
	sensors, err := fresh.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if len(sensors) != 2 {
		t.Fatalf("restored %d sensors, want 2", len(sensors))
	}
	var router model.Sensor
	for _, s := range sensors {
		if s.Name == "Router" {
			router = s
		}
	}
	if router.ID == 0 {
		t.Fatal("Router did not come back")
	}
	if router.Site != "Rack A" || model.JoinTags(router.Tags) != "lan,core" {
		t.Errorf("restored Router has site %q and tags %q", router.Site, model.JoinTags(router.Tags))
	}
	assigned, err := fresh.ChannelsForSensor(ctx, router.ID)
	if err != nil {
		t.Fatalf("channels for sensor: %v", err)
	}
	if len(assigned) != 1 || assigned[0].Name != "telegram" {
		t.Errorf("restored Router is wired to %v, want telegram", assigned)
	}

	// The setting that is not a default.
	if got := fresh.Setting(ctx, model.SettingTileURL, ""); got != "https://tiles.example.org/{z}/{x}/{y}.png" {
		t.Errorf("restored tile URL = %q", got)
	}

	// The numbers, which is what the history is for. Read from the hourly rollups on
	// both sides, because that is the only place the probes still exist after the
	// round trip.
	after := f.uptime(t, fresh)
	if after.Total != before.Total || after.OK != before.OK {
		t.Errorf("uptime after restore is %+v, before it was %+v", after, before)
	}
	if after.Total != f.probes || after.OK != f.probes-f.failures {
		t.Errorf("uptime is %+v, want %d probes of which %d failed", after, f.probes, f.failures)
	}

	// The outage log.
	incidents, err := fresh.ListIncidentsWithNames(ctx, IncidentFilter{})
	if err != nil {
		t.Fatalf("list incidents: %v", err)
	}
	if len(incidents) != 1 || incidents[0].SensorName != "Router" {
		t.Fatalf("restored incidents = %+v, want one on Router", incidents)
	}
	if incidents[0].ResolvedAt == nil || incidents[0].Cause != "no reply from 10.0.0.1" {
		t.Errorf("restored incident = %+v", incidents[0])
	}

	// And the one thing a restore must not do: double the history. Maintain folds
	// raw rows into the rollups again; with no raw rows left it must leave the
	// restored aggregates exactly where they were.
	if err := fresh.Maintain(ctx, time.Now().UTC()); err != nil {
		t.Fatalf("maintain after restore: %v", err)
	}
	if again := f.uptime(t, fresh); again.Total != before.Total {
		t.Errorf("after a maintain the restored history reads %d probes, want %d", again.Total, before.Total)
	}
}

// TestRestoreOfAConfigBackupKeepsTheLocalHistory is the reason sensors are matched by
// name and updated in place rather than deleted and re-inserted.
//
// A configuration backup names no history at all. Restoring one is the "I broke the
// settings, put yesterday's back" case, and it must not take the outage log of the
// sensors it mentions down with it. If the sensor were deleted and re-created, the
// foreign key with ON DELETE CASCADE would do exactly that — and the damage would be
// invisible, because the configuration would look perfectly correct afterwards.
func TestRestoreOfAConfigBackupKeepsTheLocalHistory(t *testing.T) {
	ctx := context.Background()
	f := newBackupFixture(t)
	before := f.uptime(t, f.store)

	file := f.export(t, ExportOptions{Scope: BackupScopeConfig})

	// The same installation restores it over itself, as it would over a mistake.
	result, err := f.store.Restore(ctx, bytes.NewReader(file), RestoreOptions{SkipSafetyCopy: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.Counts.Incidents != 0 || result.Counts.Hourly != 0 {
		t.Errorf("a config restore reported %+v, want no history at all", result.Counts)
	}

	if incidents, err := f.store.ListIncidents(ctx, IncidentFilter{}); err != nil {
		t.Fatalf("list incidents: %v", err)
	} else if len(incidents) != 1 {
		t.Errorf("the config restore left %d incidents, want the 1 that was already here", len(incidents))
	}

	// The sensor keeps its identity, which is what the history hangs from.
	sensors, err := f.store.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	for _, s := range sensors {
		if s.Name == "Router" && s.ID != f.sensor.ID {
			t.Errorf("Router came back with id %d instead of %d, so its history is orphaned",
				s.ID, f.sensor.ID)
		}
	}
	if after := f.uptime(t, f.store); after.Total != before.Total {
		t.Errorf("uptime after a config restore is %+v, before it was %+v", after, before)
	}
}

// ---------------------------------------------------------------------------
// what is refused
// ---------------------------------------------------------------------------

// TestRestoreRefusesWhatItCannotRead walks the files that must not be restored, and
// checks after each one that nothing changed. A refusal that has already written half
// the file is not a refusal.
func TestRestoreRefusesWhatItCannotRead(t *testing.T) {
	ctx := context.Background()

	valid := func(t *testing.T) []byte {
		f := newBackupFixture(t)
		return f.export(t, ExportOptions{Scope: BackupScopeConfig})
	}

	cases := []struct {
		what string
		file func(t *testing.T) []byte
	}{
		{"an empty body", func(*testing.T) []byte { return nil }},
		{"something that is not JSON", func(*testing.T) []byte { return []byte("PK\x03\x04 this is a zip") }},
		{"JSON that is not a backup", func(*testing.T) []byte { return []byte(`{"hello":"world"}`) }},
		{"a file that stops in the middle", func(t *testing.T) []byte {
			file := valid(t)
			return file[:len(file)/2]
		}},
		{"a file from a newer format", func(t *testing.T) []byte {
			return rewrite(t, valid(t), func(doc map[string]json.RawMessage) {
				doc["format_version"] = json.RawMessage(`99`)
			})
		}},
		{"a file from a newer schema", func(t *testing.T) []byte {
			return rewrite(t, valid(t), func(doc map[string]json.RawMessage) {
				doc["schema_version"] = json.RawMessage(`99`)
			})
		}},
		{"a file that names no table", func(t *testing.T) []byte {
			return []byte(`{"format":"probeglass-backup","format_version":1,"schema_version":3}`)
		}},
		{"a file with no administrator left", func(t *testing.T) []byte {
			return rewrite(t, valid(t), func(doc map[string]json.RawMessage) {
				doc["users"] = json.RawMessage(`[]`)
			})
		}},
		{"history for a sensor that does not exist", func(t *testing.T) []byte {
			f := newBackupFixture(t)
			return rewrite(t, f.export(t, ExportOptions{Scope: BackupScopeHistory, Days: 30}),
				func(doc map[string]json.RawMessage) {
					doc["incidents"] = json.RawMessage(
						`[{"sensor":"Ghost","started_at":"2026-10-01T00:00:00Z","cause":"x"}]`)
				})
		}},
	}

	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			f := newBackupFixture(t)
			before, err := f.store.ListSensors(ctx)
			if err != nil {
				t.Fatalf("list sensors: %v", err)
			}

			_, err = f.store.Restore(ctx, bytes.NewReader(c.file(t)), RestoreOptions{SkipSafetyCopy: true})
			if err == nil {
				t.Fatal("the file was accepted")
			}
			if !errors.Is(err, ErrBadBackup) {
				t.Errorf("the failure is %v, which does not read as a complaint about the file", err)
			}

			after, listErr := f.store.ListSensors(ctx)
			if listErr != nil {
				t.Fatalf("list sensors after the refusal: %v", listErr)
			}
			if len(after) != len(before) {
				t.Fatalf("the installation has %d sensors after the refusal, it had %d before",
					len(after), len(before))
			}
			for i := range after {
				if after[i].Name != before[i].Name || after[i].ID != before[i].ID {
					t.Fatalf("sensor %d changed from %+v to %+v", i, before[i], after[i])
				}
			}
		})
	}
}

// rewrite decodes a document, hands it to fn and encodes it again.
func rewrite(t *testing.T, file []byte, fn func(map[string]json.RawMessage)) []byte {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(file, &doc); err != nil {
		t.Fatalf("decode the file to rewrite it: %v", err)
	}
	fn(doc)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode the rewritten file: %v", err)
	}
	return out
}

// TestRestoreWritesASafetyCopyBeforeTouchingAnything checks the way back.
//
// The copy is taken before the transaction, and it is the only undo the dashboard
// has. If it cannot be written, nothing is restored — which is the point: a restore
// that fails halfway with no copy would leave an installation that is neither the old
// one nor the new one.
func TestRestoreWritesASafetyCopyBeforeTouchingAnything(t *testing.T) {
	ctx := context.Background()
	f := newBackupFixture(t)
	file := f.export(t, ExportOptions{Scope: BackupScopeConfig})

	result, err := f.store.Restore(ctx, bytes.NewReader(file), RestoreOptions{})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if result.SafetyCopy == "" {
		t.Fatal("no safety copy was written")
	}
	if filepath.Dir(result.SafetyCopy) != filepath.Dir(f.store.Path()) {
		t.Errorf("the safety copy landed in %q, not beside the database in %q",
			filepath.Dir(result.SafetyCopy), filepath.Dir(f.store.Path()))
	}
	if !strings.HasPrefix(filepath.Base(result.SafetyCopy), "probeglass-pre-restore-") {
		t.Errorf("the safety copy is named %q", filepath.Base(result.SafetyCopy))
	}

	// A directory that cannot be written must stop the restore before it starts.
	// The store would otherwise have replaced the accounts and then noticed.
	blocked := filepath.Join(t.TempDir(), "no-such-directory", "deeper")
	before, err := f.store.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if _, err := f.store.Restore(ctx, strings.NewReader(string(file)),
		RestoreOptions{SafetyCopyDir: blocked}); err == nil {
		t.Fatal("the restore ran although the safety copy could not be written")
	} else if errors.Is(err, ErrBadBackup) {
		t.Errorf("a missing directory was reported as a bad file: %v", err)
	}
	after, err := f.store.ListSensors(ctx)
	if err != nil {
		t.Fatalf("list sensors: %v", err)
	}
	if len(after) != len(before) {
		t.Error("the failed safety copy still changed the installation")
	}
}

// TestRestoreReportsTheTokenEpoch pins the one security consequence a restore can
// have that is not obvious from the dashboard: accounts carry the epoch their
// sessions were signed out under, and bringing an older file back lowers it again.
func TestRestoreReportsTheTokenEpoch(t *testing.T) {
	ctx := context.Background()
	f := newBackupFixture(t)
	file := f.export(t, ExportOptions{Scope: BackupScopeConfig})

	// "Sign out everywhere", after the file was written. A password change is how
	// that happens, and it is the case the notice exists for.
	if err := f.store.SetUserPassword(ctx, f.user.ID, "$2a$10$achangedpasswordhash"); err != nil {
		t.Fatalf("change the password: %v", err)
	}

	result, err := f.store.Restore(ctx, bytes.NewReader(file), RestoreOptions{SkipSafetyCopy: true})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !result.TokenEpochNotice {
		t.Error("restoring an older file did not report that it undoes a sign-out")
	}

	// Restoring the same file again, now that the epoch is the file's again, is not
	// a notice: nothing was undone the second time.
	again, err := f.store.Restore(ctx, bytes.NewReader(file), RestoreOptions{SkipSafetyCopy: true})
	if err != nil {
		t.Fatalf("second restore: %v", err)
	}
	if again.TokenEpochNotice {
		t.Error("a restore that undoes nothing still reported a token epoch notice")
	}
}
