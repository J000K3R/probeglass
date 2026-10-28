// Package store persists sensors, alert channels, probe results, rollups and
// incidents in SQLite.
//
// # Storage model
//
// Every probe writes one row into results. A background maintenance pass folds
// raw results into hourly and daily rollups. Queries then pick exactly one
// resolution for the requested window, so a sample is never counted twice:
//
//	window <= retention.raw     -> raw results      (exact, per-probe)
//	window <= retention.hourly  -> hourly rollups   (one row per hour)
//	anything longer             -> daily rollups    (one row per day)
//
// Raw results are deleted once they are older than retention.raw, after they
// have been rolled up. Daily rollups are kept forever: they are a few bytes per
// day and they are what makes multi-year uptime percentages possible.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver: no CGO, so cross-compiling stays trivial

	"github.com/j000k3r/probeglass/internal/model"
)

// timeFormat is a fixed-width UTC layout. Fixed width matters: it makes the
// TEXT timestamps sort chronologically under SQLite's default collation.
// time.RFC3339Nano must not be used here because it drops trailing zeros.
const timeFormat = "2006-01-02T15:04:05.000000000Z"

// dayFormat is the bucket key used by the daily rollup table.
const dayFormat = "2006-01-02"

// Store wraps the SQLite database.
type Store struct {
	db  *sql.DB
	log *slog.Logger

	// rawRetention and hourlyRetention decide which resolution answers a query.
	// They mirror the retention settings the maintenance pass enforces, so a
	// window is never answered from a table whose rows have already been pruned.
	rawRetention    time.Duration
	hourlyRetention time.Duration
}

// Open opens (and creates) the database at path and applies migrations.
func Open(path string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create data dir %s: %w", dir, err)
		}
	}

	dsn := "file:" + path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite allows one writer at a time. Serialising every statement through a
	// single connection removes SQLITE_BUSY handling entirely; the workload here
	// is a handful of sub-millisecond statements per second.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database %s: %w%s", path, err, unwritableHint(filepath.Dir(path)))
	}

	s := newStore(db, log)
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// Migration 5 does this once, which is once per installation. This does it on
	// every open, because a backup taken before that migration is a way for the row
	// to come back afterwards, and restoring one is an ordinary thing to do here.
	// It is cheap — one indexed lookup in a table with a few dozen rows.
	if note, err := retireDefaultTileURL(ctx, db); err != nil {
		log.Warn("could not check the stored map tile address", "err", err)
	} else if note != "" {
		log.Info("removed the map tile address this project no longer defaults to",
			"tile_url", model.RetiredTileURL)
	}
	return s, nil
}

// unwritableHint explains a failed open that was caused by permissions, and says
// nothing at all when it was not.
//
// SQLite reports every reason it cannot open a file as "unable to open database
// file (14)". A missing directory, a read-only file, a full disk and a directory the
// process does not own all read exactly the same, which is no help to somebody
// staring at a container that exits two seconds after it starts.
//
// The one that actually happens is the last: a bind mount brings the *host* folder's
// ownership along with it, and on a NAS that folder is usually created by root. The
// container is built for exactly that — it starts as root, hands the folder to
// PUID:PGID and then drops to it — so reaching this message means the drop never ran:
// the process was started with --user, or by a runtime that will not run it as root
// at all.
//
// Hence the probe rather than a guess: if a file can be created in the directory, the
// failure is something else entirely, and this returns nothing so that the message
// stays as SQLite worded it. A hint that is wrong half the time is worse than none.
//
// The uid is printed rather than the image's 500 written out, because the same message
// has to be true when the binary runs straight on a host — where the answer is that
// user's own uid, and telling somebody to chown their own folder to 500 would be
// nonsense. Inside the image the two are the same number, and it is the one the
// container documentation quotes.
func unwritableHint(dir string) string {
	f, err := os.CreateTemp(dir, ".probeglass-write-test-*")
	if err == nil {
		name := f.Name()
		f.Close()
		os.Remove(name)
		return ""
	}
	return fmt.Sprintf("; %s is not writable by uid %d, the user this process runs as."+
		" In a container the daemon starts as root, takes the data directory over and"+
		" then drops to PUID:PGID, so this means it did not start as root — drop --user,"+
		" or set PUID and PGID to whoever owns the folder, or hand it over yourself:"+
		" chown -R %d:%d <host folder>",
		dir, os.Getuid(), os.Getuid(), os.Getuid())
}

// OpenReadOnly opens an existing database for reading.
//
// It never creates the directory, never creates the file, never migrates and
// never changes the journal mode. That is the entire point: a diagnostic has to
// be able to describe a broken installation without repairing it, and a tool
// that silently fixes what it was asked to report is worse than no tool at all.
//
// The file must already exist — SQLite's mode=ro refuses to create one, which is
// the behaviour we want here. Every write on the returned handle fails with
// SQLITE_READONLY. That is deliberate too; do not relax this to Open.
//
// The DSN is deliberately not the one Open builds. A journal_mode pragma is a
// write, so carrying it over would make the connection fail to open outright
// rather than merely fail to write.
func OpenReadOnly(path string, log *slog.Logger) (*Store, error) {
	if log == nil {
		log = slog.Default()
	}

	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s read-only: %w", path, err)
	}
	return newStore(db, log), nil
}

// newStore fills in a handle with the defaults every Store shares.
func newStore(db *sql.DB, log *slog.Logger) *Store {
	return &Store{
		db:              db,
		log:             log,
		rawRetention:    7 * 24 * time.Hour,
		hourlyRetention: 90 * 24 * time.Hour,
	}
}

// LatestSchemaVersion is the schema version this binary migrates a database to.
// A database below it is migrated on the next start; one above it was written by
// a newer ProbeGlass than this binary.
func LatestSchemaVersion() int {
	if len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for tests and maintenance jobs.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the database file path as reported by SQLite.
func (s *Store) Path() string {
	var seq int
	var name, file string
	if err := s.db.QueryRowContext(context.Background(),
		"SELECT * FROM pragma_database_list").Scan(&seq, &name, &file); err != nil {
		return ""
	}
	return file
}

// ---------------------------------------------------------------------------
// time helpers
// ---------------------------------------------------------------------------

func fmtTime(t time.Time) string { return t.UTC().Format(timeFormat) }

func parseTime(s string) time.Time {
	t, err := time.Parse(timeFormat, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func parseTimePtr(s sql.NullString) *time.Time {
	if !s.Valid || s.String == "" {
		return nil
	}
	t := parseTime(s.String)
	if t.IsZero() {
		return nil
	}
	return &t
}

// ---------------------------------------------------------------------------
// transactions
// ---------------------------------------------------------------------------

// tx runs fn inside a transaction, rolling back on error or panic.
func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// execer is satisfied by both *sql.DB and *sql.Tx, which lets the write helpers
// serve single statements and bulk transactions alike.
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// InTx runs fn inside a single transaction.
//
// The daemon itself writes one row at a time and does not need this. Bulk
// callers do: importing or seeding thousands of probes in one transaction is
// orders of magnitude faster than thousands of autocommits, and it is atomic,
// so a failure halfway leaves no half-filled history behind.
func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	return s.tx(ctx, func(sqlTx *sql.Tx) error { return fn(&Tx{tx: sqlTx}) })
}

// Tx is the bulk-write handle handed to InTx callbacks.
type Tx struct{ tx *sql.Tx }

// InsertResult stores one probe outcome inside the transaction.
func (t *Tx) InsertResult(ctx context.Context, r *model.Result) error {
	return insertResult(ctx, t.tx, r)
}

// OpenIncident records the start of an outage inside the transaction.
func (t *Tx) OpenIncident(ctx context.Context, sensorID int64, startedAt time.Time, cause string) (int64, error) {
	return openIncident(ctx, t.tx, sensorID, startedAt, cause)
}

// CloseIncident resolves an outage inside the transaction.
func (t *Tx) CloseIncident(ctx context.Context, incidentID int64, resolvedAt time.Time) error {
	return closeIncident(ctx, t.tx, incidentID, resolvedAt)
}

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// ---------------------------------------------------------------------------
// migrations
// ---------------------------------------------------------------------------

type migration struct {
	version int
	name    string
	sql     string

	// after runs inside the same transaction as sql, once the statements above
	// have been applied. It exists for the small number of migrations that have
	// to look at the data they are migrating, which DDL alone cannot express.
	// The returned note describes what it did, for the log; empty means nothing
	// needed doing. If it returns an error the whole migration — statements
	// included — is rolled back, so a half-migrated database cannot happen.
	after func(ctx context.Context, tx *sql.Tx) (string, error)
}

var migrations = []migration{
	{
		version: 1,
		name:    "init",
		sql: `
CREATE TABLE IF NOT EXISTS sensors (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    name            TEXT    NOT NULL UNIQUE,
    type            TEXT    NOT NULL,
    target          TEXT    NOT NULL,
    interval_s      INTEGER NOT NULL DEFAULT 60,
    timeout_s       INTEGER NOT NULL DEFAULT 10,
    retries         INTEGER NOT NULL DEFAULT 1,
    enabled         INTEGER NOT NULL DEFAULT 1,
    grp             TEXT    NOT NULL DEFAULT '',
    tags            TEXT    NOT NULL DEFAULT '',
    options         TEXT    NOT NULL DEFAULT '{}',
    reminder_every_s INTEGER NOT NULL DEFAULT 0,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    managed         INTEGER NOT NULL DEFAULT 0,
    created_at      TEXT    NOT NULL,
    updated_at      TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS channels (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL UNIQUE,
    type       TEXT    NOT NULL,
    config     TEXT    NOT NULL DEFAULT '{}',
    enabled    INTEGER NOT NULL DEFAULT 1,
    managed    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE TABLE IF NOT EXISTS sensor_channels (
    sensor_id  INTEGER NOT NULL REFERENCES sensors(id)   ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    PRIMARY KEY (sensor_id, channel_id)
);

CREATE TABLE IF NOT EXISTS results (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sensor_id   INTEGER NOT NULL REFERENCES sensors(id) ON DELETE CASCADE,
    ts          TEXT    NOT NULL,
    ok          INTEGER NOT NULL,
    latency_ms  REAL    NOT NULL DEFAULT 0,
    status_code INTEGER NOT NULL DEFAULT 0,
    message     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_results_sensor_ts ON results(sensor_id, ts);

CREATE TABLE IF NOT EXISTS results_hourly (
    sensor_id   INTEGER NOT NULL,
    bucket      TEXT    NOT NULL,
    samples     INTEGER NOT NULL,
    ok          INTEGER NOT NULL,
    latency_sum REAL    NOT NULL DEFAULT 0,
    latency_min REAL    NOT NULL DEFAULT 0,
    latency_max REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY (sensor_id, bucket)
);

CREATE TABLE IF NOT EXISTS results_daily (
    sensor_id   INTEGER NOT NULL,
    bucket      TEXT    NOT NULL,
    samples     INTEGER NOT NULL,
    ok          INTEGER NOT NULL,
    latency_sum REAL    NOT NULL DEFAULT 0,
    latency_min REAL    NOT NULL DEFAULT 0,
    latency_max REAL    NOT NULL DEFAULT 0,
    PRIMARY KEY (sensor_id, bucket)
);

CREATE TABLE IF NOT EXISTS incidents (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    sensor_id   INTEGER NOT NULL REFERENCES sensors(id) ON DELETE CASCADE,
    started_at  TEXT    NOT NULL,
    resolved_at TEXT,
    cause       TEXT    NOT NULL DEFAULT '',
    acked_at    TEXT,
    acked_by    TEXT    NOT NULL DEFAULT '',
    ack_note    TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_incidents_sensor ON incidents(sensor_id, started_at);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT NOT NULL PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
`,
	},
	{
		version: 2,
		name:    "users",
		sql: `
CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL DEFAULT 'viewer'
                  CHECK (role IN ('admin','operator','viewer')),
    totp_secret   TEXT    NOT NULL DEFAULT '',
    totp_enabled  INTEGER NOT NULL DEFAULT 0,
    totp_step     INTEGER NOT NULL DEFAULT 0,
    token_epoch   INTEGER NOT NULL DEFAULT 0,
    disabled      INTEGER NOT NULL DEFAULT 0,
    prefs         TEXT    NOT NULL DEFAULT '{}',
    created_at    TEXT    NOT NULL,
    updated_at    TEXT    NOT NULL,
    last_login_at TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS recovery_codes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT    NOT NULL,
    used_at    TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_recovery_user ON recovery_codes(user_id, used_at);

ALTER TABLE sensors ADD COLUMN origin TEXT NOT NULL DEFAULT '';
`,
		after: adoptLegacyAuth,
	},
	{
		version: 3,
		name:    "sites",
		sql: `
CREATE TABLE IF NOT EXISTS sites (
    name       TEXT    NOT NULL PRIMARY KEY,
    kind       TEXT    NOT NULL DEFAULT 'local'
               CHECK (kind IN ('local','external')),
    lat        REAL,
    lon        REAL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    managed    INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

-- No DEFAULT and no NOT NULL on this column, and both are load-bearing. SQLite
-- only accepts a REFERENCES clause on ADD COLUMN while the default is NULL, and a
-- NOT NULL DEFAULT '' would make the empty string a parent key that no site can
-- ever satisfy — every sensor without a site would then fail to save.
--
-- The reference is to the name rather than to a rowid because a configuration
-- file can name a place but can never know a database id. ON UPDATE CASCADE makes
-- renaming a site one UPDATE that carries its sensors along; ON DELETE RESTRICT
-- makes deleting one that is still in use a refusal rather than a silent
-- reassignment to nowhere.
ALTER TABLE sensors ADD COLUMN site TEXT
    REFERENCES sites(name) ON UPDATE CASCADE ON DELETE RESTRICT;

-- SQLite does not index the child side of a foreign key by itself, and this one is
-- read on every summary (to group by site) and on every RESTRICT check.
CREATE INDEX IF NOT EXISTS idx_sensors_site ON sensors(site);
`,
	},
	{
		version: 4,
		name:    "site-origin",
		sql: `
-- The example dataset needs places to put its sensors, and "remove the example
-- data" must not leave those places behind as empty rows. Sensors have carried
-- this marker since the setup screen existed; sites did not, because until the
-- example data started creating them there was nothing to invent one.
--
-- NOT NULL DEFAULT '' is allowed here, unlike on the sensors.site column above:
-- that restriction applies to ADD COLUMN with a REFERENCES clause, and this
-- column references nothing. The empty string is the ordinary "made by hand".
ALTER TABLE sites ADD COLUMN origin TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 5,
		name:    "retire-osm-tiles",
		// Nothing to change about the shape of the database: this migration is
		// about a value that is in the settings table, and about a default that
		// used to be one. `SELECT 1` is here because Exec still has to run
		// something, and an empty statement is a parse error rather than a no-op.
		sql: `SELECT 1;`,
		after: func(ctx context.Context, tx *sql.Tx) (string, error) {
			return retireDefaultTileURL(ctx, tx)
		},
	},
	{
		version: 6,
		name:    "maintenance",
		sql: `
-- A period during which downtime is expected: the nightly snapshot, the Sunday
-- backup, the afternoon the disk gets swapped.
--
-- Exactly one of sensor_id and grp is set. A group is a string on the sensors
-- rather than a table of its own, so this column is a name and not a foreign key
-- — which is also why there is no ON DELETE here to worry about: a window whose
-- group no sensor carries matches nothing, and a window whose sensor is deleted
-- goes with it.
CREATE TABLE IF NOT EXISTS maintenance_windows (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    sensor_id  INTEGER REFERENCES sensors(id) ON DELETE CASCADE,
    grp        TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL CHECK (kind IN ('once','weekly')),

    -- 'once': two instants. 'weekly': a time of day as minutes past midnight, in
    -- the zone named below. Both shapes are stored as text and integer rather
    -- than as one shape with the other's columns empty, because a reader of this
    -- table should be able to see which kind a row is without decoding anything.
    starts_at  TEXT,
    ends_at    TEXT,
    weekdays   TEXT    NOT NULL DEFAULT '',
    start_min  INTEGER NOT NULL DEFAULT 0,
    end_min    INTEGER NOT NULL DEFAULT 0,
    tz         TEXT    NOT NULL DEFAULT 'UTC',

    note       TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 1,
    managed    INTEGER NOT NULL DEFAULT 0,
    origin     TEXT    NOT NULL DEFAULT '',
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_windows_sensor ON maintenance_windows(sensor_id);
CREATE INDEX IF NOT EXISTS idx_windows_group  ON maintenance_windows(grp);

-- A probe that landed inside a window, and therefore does not count towards
-- uptime. Recorded rather than not made at all: the chart should still show that
-- the box was away, and "expected" is a fact about the outage, not about the
-- measurement. The scheduler sets it at the moment the result is written, which
-- is the only moment that knows both the sensor and the clock.
ALTER TABLE results ADD COLUMN maint INTEGER NOT NULL DEFAULT 0;

-- And the same counts in every rollup table. This pair of columns is the whole
-- reason the feature needed a migration rather than a query: raw rows are pruned
-- after retention.raw, and everything older than that is answered from a rollup
-- row that was written once and can never be revisited. A maintenance sample that
-- is not carried into the rollup is a maintenance sample that stops being excluded
-- the day the raw rows fall off — which is to say, on the seventh day, silently,
-- and in the direction that makes a homelab look less reliable than it is.
--
-- Two columns and not one, because a window is a period and not a promise: the box
-- may come back early. Subtracting only the total would leave those samples counted
-- as successes that are no longer in the denominator, and an uptime of 103% is what
-- that looks like from the dashboard.
ALTER TABLE results_hourly ADD COLUMN maint_samples INTEGER NOT NULL DEFAULT 0;
ALTER TABLE results_hourly ADD COLUMN maint_ok      INTEGER NOT NULL DEFAULT 0;
ALTER TABLE results_daily  ADD COLUMN maint_samples INTEGER NOT NULL DEFAULT 0;
ALTER TABLE results_daily  ADD COLUMN maint_ok      INTEGER NOT NULL DEFAULT 0;
`,
	},

	{
		version: 7,
		name:    "excused",
		// The maintenance marker becomes the general one, because a second reason for
		// it arrived: a sensor whose parent is down is not at fault either.
		//
		// A rename and not a new pair of columns. The concept is one — this sample was
		// excused — and the arithmetic that has to know is one: SUM(ok)/SUM(samples),
		// answered from whichever of three tables the window's length selects. Two
		// parallel pairs would mean every one of those seven places asking twice, and
		// a second chance to get the same thing wrong.
		//
		// RENAME COLUMN is safe here in the way a plain ALTER never is: it moves the
		// values with the name, inside the same transaction as everything else in this
		// migration. A database that was at version 6 comes out of this with every
		// count it had, under a better name.
		sql: `
ALTER TABLE results RENAME COLUMN maint TO excused;
ALTER TABLE results_hourly RENAME COLUMN maint_samples TO excused_samples;
ALTER TABLE results_hourly RENAME COLUMN maint_ok      TO excused_ok;
ALTER TABLE results_daily  RENAME COLUMN maint_samples TO excused_samples;
ALTER TABLE results_daily  RENAME COLUMN maint_ok      TO excused_ok;

-- Why it was excused, on the raw row only.
--
-- The rollup counts only need to know *that* a sample was excused; the seven
-- arithmetic sites subtract it either way. What needs the reason is a person looking
-- at one probe: "this gap was the backup window" and "this gap was because the router
-- was away" are different stories about the same hole in a chart.
--
-- It is written on the row rather than derived at read time because the reason may be
-- gone by then — a maintenance window can be deleted, and its explanation with it. A
-- sample that keeps its reason can still be read after the window that caused it has
-- been forgotten.
ALTER TABLE results ADD COLUMN excused_reason TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 8,
		name:    "sensor-parent",
		sql: `
-- Which sensor this one sits behind, by name.
--
-- A name and not an id, for the reason sensors.site is a name: a configuration file
-- can name a sensor but can never know the ids in somebody's database, and one
-- conversion has to serve both the file and the API.
--
-- ON UPDATE CASCADE carries a rename, exactly as it does for a site. ON DELETE SET
-- NULL is the part that matters and is not a shrug: two paths delete sensors in bulk
-- — "remove the example data" and restoring a backup, which deletes row by row — and a
-- RESTRICT would make both fail the day an example sensor happened to be somebody's
-- parent. A child whose parent is gone is a child that reports for itself again, which
-- is the right answer and is not worth refusing a delete over.
--
-- The SQLite rule from migration 3 applies unchanged: ADD COLUMN with a REFERENCES
-- clause is accepted only while the default is NULL.
ALTER TABLE sensors ADD COLUMN parent TEXT
    REFERENCES sensors(name) ON UPDATE CASCADE ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_sensors_parent ON sensors(parent);
`,
	},
	{
		version: 9,
		name:    "groups-and-icons",
		sql: `
-- Settings for a group, which is still a word written on a sensor and not a row.
--
-- There is deliberately no foreign key from sensors.grp to this table, and that is the
-- whole design: a group exists because somebody typed its name, exactly as it does today.
-- This holds what is known *about* a group once somebody has said more — an icon, a colour,
-- a place in the order — and a group nobody has said anything about simply has no row
-- here. A sensor may therefore name a group this table has never heard of, which is the
-- ordinary state of a fresh installation and has to stay cheap.
--
-- The alternative — a real table with sensors.grp referencing it — would turn creating a
-- group into a step of its own and would reach into the configuration file, the backup,
-- the example data and every delete path. That is a different feature.
CREATE TABLE IF NOT EXISTS groups (
    name       TEXT    NOT NULL PRIMARY KEY,
    icon       TEXT    NOT NULL DEFAULT '',
    colour     TEXT    NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);

-- Icons somebody uploaded, held as bytes in this database rather than as files beside it.
--
-- The bytes are the point. "One SQLite file, and copying it is the whole backup" is a
-- promise this project makes in three places, and an icon kept on disk would quietly make
-- it false — the copy would restore without them. They are small; see MaxIconBytes in the
-- API, which caps what may be stored.
--
-- media_type and the two dimensions are recorded rather than worked out at read time.
-- Nothing needs them to serve the bytes, but they are what makes listing uploaded icons
-- possible without decoding every one, and what a later re-encode would want to know.
CREATE TABLE IF NOT EXISTS icons (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL DEFAULT '',
    media_type TEXT    NOT NULL,
    width      INTEGER NOT NULL,
    height     INTEGER NOT NULL,
    bytes      BLOB    NOT NULL,
    created_at TEXT    NOT NULL
);

-- And a symbol on the things somebody makes. The empty string is "none", which is what
-- every existing row gets and what the dashboard draws nothing for: an icon is never
-- required, and an installation that ignores the feature looks exactly as it does today.
ALTER TABLE sensors ADD COLUMN icon TEXT NOT NULL DEFAULT '';
ALTER TABLE sites   ADD COLUMN icon TEXT NOT NULL DEFAULT '';
`,
	},
	{
		version: 10,
		name:    "tag-settings",
		sql: `
-- Settings for a tag, in the shape a group's settings already have.
--
-- A tag is still a word in the comma-separated list on a sensor, not a row: the
-- sensors table has no foreign key here, a tag exists from the moment it is typed,
-- and a tag nobody has said anything about has no row at all. What this holds is
-- only what a word cannot say — an icon, a colour, a place in the order.
--
-- The shape mirrors groups deliberately, because the editor is the same dialog for
-- both. The two tables are separate because the API and the meaning are separate.
CREATE TABLE IF NOT EXISTS tags (
    name       TEXT    NOT NULL PRIMARY KEY,
    icon       TEXT    NOT NULL DEFAULT '',
    colour     TEXT    NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL
);
`,
	},
	{
		version: 11,
		name:    "escalation",
		sql: `
-- How long an incident may stay open before a second set of channels is told,
-- and which channels those are. The value is seconds; 0 is off, which is what
-- every sensor gets and what makes an installation that ignores the feature
-- behave exactly as it did.
ALTER TABLE sensors ADD COLUMN escalate_after_s INTEGER NOT NULL DEFAULT 0;

-- The second list. A separate table rather than a column so that the ordinary
-- channel list and this one are written, read and cascaded the same way.
CREATE TABLE IF NOT EXISTS sensor_escalation_channels (
    sensor_id  INTEGER NOT NULL REFERENCES sensors(id)  ON DELETE CASCADE,
    channel_id INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    PRIMARY KEY (sensor_id, channel_id)
);

-- When the escalation went out. Written once per incident, so a restart or a
-- second sweep cannot send the same message again.
ALTER TABLE incidents ADD COLUMN escalated_at TEXT;
`,
	},
	{
		version: 12,
		name:    "certificate-inventory",
		sql: `
-- The last certificate each sensor saw.
--
-- One row per sensor, not one per probe: a certificate is the same object for
-- months, and the question the inventory answers — which one runs out first? —
-- is asked about the object rather than about the probe that noticed it. A row
-- is rewritten by every handshake that presents a certificate, and deleted when
-- a sensor stops seeing one, so the table is exactly the inventory.
--
-- dns_names is newline-separated because a name cannot contain one; the serial
-- is hexadecimal and the fingerprint is SHA-256 of the DER bytes, lowercase hex.
-- warn_days is recorded with the observation rather than read from the sensor
-- later, because the threshold is part of what "late" meant when it was seen.
CREATE TABLE IF NOT EXISTS certificates (
    sensor_id   INTEGER PRIMARY KEY REFERENCES sensors(id) ON DELETE CASCADE,
    subject     TEXT    NOT NULL DEFAULT '',
    issuer      TEXT    NOT NULL DEFAULT '',
    serial      TEXT    NOT NULL DEFAULT '',
    fingerprint TEXT    NOT NULL DEFAULT '',
    dns_names   TEXT    NOT NULL DEFAULT '',
    not_before  TEXT    NOT NULL DEFAULT '',
    not_after   TEXT    NOT NULL DEFAULT '',
    tls_version TEXT    NOT NULL DEFAULT '',
    warn_days   INTEGER NOT NULL DEFAULT 0,
    observed_at TEXT    NOT NULL
);

-- The page sorts by expiry, and so does every query that asks about it.
CREATE INDEX IF NOT EXISTS certificates_not_after ON certificates(not_after);
`,
	},
	{
		version: 13,
		name:    "public-status-page",
		sql: `
-- The sensors a public status page shows.
--
-- A separate table rather than a column on sensors, because publication is a
-- fact about the page and not about the probe: the page can be switched off and
-- on without touching the sensors, and a sensor deleted anywhere takes its
-- publication with it. Only an id is stored — everything the page draws is read
-- from the sensor at request time, so renaming a sensor renames it in public.
CREATE TABLE IF NOT EXISTS status_sensors (
    sensor_id INTEGER PRIMARY KEY REFERENCES sensors(id) ON DELETE CASCADE
);
`,
	},
}

// rowExecer is the part of *sql.DB and *sql.Tx that the settings sweeps below use.
//
// The same code runs in both places — inside the migration transaction, and against
// the open database at startup — and neither of them cares which it was handed.
type rowExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// retireDefaultTileURL deletes a stored tile address that is the one this project
// used to ship as its default, and reports what it did for the log.
//
// The map form wrote the value it was displaying into the row when it was saved,
// so an installation that opened Settings → Map and pressed Save once has
// tile.openstreetmap.org in its settings table as if it had chosen it. With the
// default gone that row is all that is left of it, and leaving it would mean every
// one of those installations carried on asking a server that has told this kind of
// application to stop — and carried on being shown the block notice.
//
// Deleting rather than rewriting to a new default, because the new default is
// empty. An installation that genuinely wants those tiles can type the address
// back in; it will not be deleted again, since the migration only ever runs once.
func retireDefaultTileURL(ctx context.Context, db rowExecer) (string, error) {
	var value string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, model.SettingTileURL).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !model.IsRetiredTileURL(value) {
		return "", nil
	}

	// The attribution went with the address. A credit line for a tile server that
	// is no longer being asked for anything is a name on somebody's dashboard that
	// has nothing to justify it — and for OpenStreetMap specifically it is the
	// credit that their terms require *of people using their tiles*, which is not
	// this installation any more.
	if _, err := db.ExecContext(ctx,
		`DELETE FROM settings WHERE key IN (?, ?)`,
		model.SettingTileURL, model.SettingTileAttribution); err != nil {
		return "", err
	}
	return "removed the retired map tile address", nil
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version    INTEGER PRIMARY KEY,
        name       TEXT NOT NULL,
        applied_at TEXT NOT NULL
    )`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	// A file written before the rename still carries the old table names. The
	// migration below uses CREATE TABLE IF NOT EXISTS, so it would sail straight
	// past them and leave a database whose very first query fails with "no such
	// table: sensors" — true, but silent about why. The rename happened before
	// the first release, so there is no upgrade path to offer: say so plainly.
	legacy, err := s.tableExists(ctx, legacyTableName)
	if err != nil {
		return fmt.Errorf("inspect schema: %w", err)
	}
	if legacy {
		current, err := s.tableExists(ctx, "sensors")
		if err != nil {
			return fmt.Errorf("inspect schema: %w", err)
		}
		if !current {
			return fmt.Errorf("this database predates the rename of %s to sensors "+
				"(it still has a %s table), which cannot be upgraded in place: "+
				"delete the database file and start again, or restore a backup",
				legacyTableName, legacyTableName)
		}
	}

	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		var note string
		if err := s.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
			}
			if m.after != nil {
				n, err := m.after(ctx, tx)
				if err != nil {
					return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
				}
				note = n
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
				m.version, m.name, fmtTime(time.Now()))
			return err
		}); err != nil {
			return err
		}
		s.log.Info("applied database migration", "version", m.version, "name", m.name)
		if note != "" {
			s.log.Info("database migration noted", "version", m.version, "note", note)
		}
	}
	return nil
}

// legacyTableName is the table the sensors lived in before the rename. Spelled
// out here, away from the sweep that renamed everything else, so the guard
// cannot be renamed along with the thing it guards.
const legacyTableName = "checks"

// tableExists reports whether a table (or view, index or trigger) of that name
// is present in the database file.
func (s *Store) tableExists(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// HasTable reports whether a table of that name exists.
//
// It is exported for the read-only doctor, which is handed a database that may
// predate the migration that introduces the table it is about to ask for. Every
// other caller goes through Open, which migrates first and therefore always has
// the table.
func (s *Store) HasTable(ctx context.Context, name string) (bool, error) {
	return s.tableExists(ctx, name)
}

// SchemaVersion reports the highest applied migration.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}
