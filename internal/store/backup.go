package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// Backup and restore, the JSON way.
//
// # Why JSON and not a copy of the database
//
// A byte-exact copy of the SQLite file — `VACUUM INTO`, or simply copying it while
// the daemon is stopped — is the better backup for one purpose: getting the whole
// installation back, exactly, in seconds. It stays the recommended way to keep a
// copy, and the documentation says so.
//
// It cannot express what people actually ask for next, which is "the settings and
// the sensors, without the history" and "with the history, but only the last thirty
// days". A file copy has no way to leave something out. A filtered copy into a
// second database could, but it needs a second file, a second set of tables and its
// own cleanup path — a lot of machinery for a result nobody can look at.
//
// So: a document. It says exactly what it contains, it can be read and checked
// with a text editor, and if the worst happens it can be repaired by hand.
//
// # Secrets are in it, in the clear
//
// A backup that replaced the Telegram bot token with `__stored__` would restore a
// channel that never sends, and nobody would notice until the first outage was
// over. So the masking that applies everywhere else deliberately does not apply
// here, and that is stated openly in SECURITY.md and in the dashboard, not in a
// footnote.
//
// # The raw probe table is not in it
//
// Seven days of raw results for forty sensors is a few hundred thousand rows —
// several times the size of the database, as JSON, for the one part of the history
// that is also the least useful. The hourly and daily rollups come along instead,
// and those are what every uptime figure and every graph beyond seven days is
// computed from: the numbers are right after a restore. What is lost is said out
// loud: the individual probe messages of the last few days are no longer found by
// the search.

const (
	// BackupFormat is the marker every file has to carry. A file without it is not
	// read at all, however much its contents look like a backup.
	BackupFormat = "probeglass-backup"
	// BackupFormatVersion is the version of the document layout. A file from a
	// newer layout is refused rather than half-read.
	BackupFormatVersion = 1

	// BackupScopeConfig and BackupScopeHistory are the two sizes of backup.
	BackupScopeConfig  = "config"
	BackupScopeHistory = "history"

	// maxBackupDays bounds the history window. The daily rollups are kept forever,
	// so "everything kept" is the unbounded choice and this is the largest number
	// of days anybody can ask for.
	maxBackupDays = 3650
)

// ErrBadBackup marks a failure that is the uploaded file's fault: it is not a backup,
// it is one from a newer version, or it names rows that cannot be put anywhere.
//
// It exists so that the route can answer "this file" with a 400 and everything else
// with a 500. Both are "the restore did not happen", and to the person who clicked
// the button they are not the same thing at all: one means they picked the wrong
// file, the other means the server could not write to its own disk and there is
// nothing they can do about it from the dashboard.
var ErrBadBackup = errors.New("not a usable backup")

// BackupDoc is the envelope of a backup file, for reading.
//
// Every table is a pointer, and that is the whole point of the type: it is how the
// restore tells "the file says there is nothing here" from "the file does not
// mention this at all". The first means empty the table; the second means leave it
// alone. A plain slice cannot carry that difference, and a nil check on a slice can
// never tell an empty array from an absent key.
type BackupDoc struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"format_version"`
	SchemaVersion int    `json:"schema_version"`
	AppVersion    string `json:"app_version"`
	CreatedAt     string `json:"created_at"`
	Scope         string `json:"scope"`
	Days          int    `json:"days"`

	Settings  *map[string]string `json:"settings"`
	Sites     *[]BackupSite      `json:"sites"`
	Groups    *[]BackupGroup     `json:"groups"`
	Tags      *[]BackupTag       `json:"tags"`
	Icons     *[]BackupIcon      `json:"icons"`
	Channels  *[]BackupChannel   `json:"channels"`
	Sensors   *[]BackupSensor    `json:"sensors"`
	Users     *[]BackupUser      `json:"users"`
	Incidents *[]BackupIncident  `json:"incidents"`
	Hourly    *[]BackupRollup    `json:"results_hourly"`
	Daily     *[]BackupRollup    `json:"results_daily"`
}

// BackupSite is one place.
type BackupSite struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Lat       *float64 `json:"lat"`
	Lon       *float64 `json:"lon"`
	Icon      string   `json:"icon,omitempty"`
	SortOrder int      `json:"sort_order"`
	Managed   bool     `json:"managed"`
}

// BackupGroup is what somebody has said about a group: its symbol, its colour and
// its place in the order. The group itself is a word on a sensor and is not in this
// document at all — restoring the sensors restores the groups with them.
type BackupGroup struct {
	Name      string `json:"name"`
	Icon      string `json:"icon,omitempty"`
	Colour    string `json:"colour,omitempty"`
	SortOrder int    `json:"sort_order"`
}

// BackupTag is what somebody has said about a tag, in the same shape as a group's
// settings. Like a group, the tag itself is a word on a sensor and is not in this
// document: restoring the sensors restores the tags with them.
type BackupTag struct {
	Name      string `json:"name"`
	Icon      string `json:"icon,omitempty"`
	Colour    string `json:"colour,omitempty"`
	SortOrder int    `json:"sort_order"`
}

// BackupIcon is one uploaded image, bytes and all, base64 in the JSON.
//
// The bytes travel because a backup that left them behind would not be the whole
// installation: the file would restore the sensors that wear an uploaded icon and
// none of the icons, and every one of them would draw nothing with no explanation
// anywhere. They are small — internal/icon caps one at 64 KiB — and a backup is
// already a document people open in an editor rather than mail around.
//
// The id is in the file and is written back as it stands, which is the one place in
// this document where a row keeps its id. It has to: a sensor's icon column holds
// `upload:7` as text, so a restore that renumbered the images would leave every
// reference pointing at whatever happened to land in that row.
type BackupIcon struct {
	ID        int64  `json:"id"`
	Name      string `json:"name,omitempty"`
	MediaType string `json:"media_type"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Data      []byte `json:"data"`
	CreatedAt string `json:"created_at"`
}

// BackupChannel is one alert channel, credentials and all.
type BackupChannel struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Config  map[string]string `json:"config"`
	Enabled bool              `json:"enabled"`
	Managed bool              `json:"managed"`
}

// BackupSensor is one sensor, with the names of the channels that alert for it.
type BackupSensor struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Target   string `json:"target"`
	Interval int    `json:"interval_s"`
	Timeout  int    `json:"timeout_s"`
	Retries  int    `json:"retries"`
	Enabled  bool   `json:"enabled"`
	Group    string `json:"group"`
	Site     string `json:"site"`
	// Parent is the name of the sensor this one sits behind, and travels with the
	// document for the same reason Site does: a backup that lost it would restore a
	// sensor that alerts for itself while its router is down.
	Parent        string         `json:"parent"`
	Icon          string         `json:"icon,omitempty"`
	Tags          []string       `json:"tags"`
	Options       map[string]any `json:"options"`
	ReminderEvery int            `json:"reminder_every_s"`
	// EscalateAfter and EscalateChannels are the second alert list: how long an
	// open incident may wait before these channels are told once.
	EscalateAfter    int      `json:"escalate_after_s,omitempty"`
	EscalateChannels []string `json:"escalate_channels,omitempty"`
	SortOrder        int      `json:"sort_order"`
	Managed          bool     `json:"managed"`
	Origin           string   `json:"origin"`
	Channels         []string `json:"channels"`
}

// BackupUser is one account, complete: password hash, second factor and the
// one-time codes. Accounts are in the backup on purpose — a restore that brought
// back the sensors and made everybody create a new password would not be a restore.
type BackupUser struct {
	Username     string            `json:"username"`
	PasswordHash string            `json:"password_hash"`
	Role         string            `json:"role"`
	TOTPSecret   string            `json:"totp_secret"`
	TOTPEnabled  bool              `json:"totp_enabled"`
	TOTPStep     int64             `json:"totp_step"`
	TokenEpoch   int64             `json:"token_epoch"`
	Disabled     bool              `json:"disabled"`
	Prefs        map[string]string `json:"prefs"`
	Recovery     []BackupRecovery  `json:"recovery_codes"`
}

// BackupRecovery is one used or unused one-time sign-in code, as its hash.
type BackupRecovery struct {
	CodeHash string `json:"code_hash"`
	UsedAt   string `json:"used_at"`
}

// BackupIncident is one outage. The sensor is named rather than numbered, because
// an id means nothing once the file is on another installation.
type BackupIncident struct {
	Sensor     string `json:"sensor"`
	StartedAt  string `json:"started_at"`
	ResolvedAt string `json:"resolved_at"`
	Cause      string `json:"cause"`
	AckedAt    string `json:"acked_at"`
	AckedBy    string `json:"acked_by"`
	AckNote    string `json:"ack_note"`
}

// BackupRollup is one hourly or daily bucket.
type BackupRollup struct {
	Sensor     string  `json:"sensor"`
	Bucket     string  `json:"bucket"`
	Samples    int     `json:"samples"`
	OK         int     `json:"ok"`
	LatencySum float64 `json:"latency_sum"`
	LatencyMin float64 `json:"latency_min"`
	LatencyMax float64 `json:"latency_max"`
}

// Counts is how many rows each table contributed. It is what goes into the log and
// into the sentence the dashboard shows — never the contents.
type Counts struct {
	Settings  int `json:"settings"`
	Sites     int `json:"sites"`
	Groups    int `json:"groups"`
	Tags      int `json:"tags"`
	Icons     int `json:"icons"`
	Channels  int `json:"channels"`
	Sensors   int `json:"sensors"`
	Users     int `json:"users"`
	Incidents int `json:"incidents"`
	Hourly    int `json:"results_hourly"`
	Daily     int `json:"results_daily"`
}

// ExportOptions is what the caller asks for.
type ExportOptions struct {
	// Scope is BackupScopeConfig or BackupScopeHistory.
	Scope string
	// Days bounds the history. Zero or less with the history scope means everything
	// the installation still keeps.
	Days int
	// AppVersion is written into the file so that a reader can tell what wrote it.
	AppVersion string
}

// Export writes a backup as one JSON document.
//
// It writes table by table rather than assembling the document in memory and
// encoding it in one go. A backup with history is a few megabytes, and the daemon
// has a scheduler running behind it; the memory does not need to be spent, and the
// streaming encoder costs nothing to use.
//
// The document is written for reading, not for compactness: a backup is a thing
// people open in an editor to see what is in it, and that is half of why this is
// JSON rather than a second SQLite file.
func (s *Store) Export(ctx context.Context, w io.Writer, opts ExportOptions) (Counts, error) {
	var counts Counts
	if opts.Scope == "" {
		opts.Scope = BackupScopeConfig
	}
	if opts.Scope != BackupScopeConfig && opts.Scope != BackupScopeHistory {
		return counts, fmt.Errorf("unknown backup scope %q", opts.Scope)
	}
	if opts.Days < 0 || opts.Days > maxBackupDays {
		return counts, fmt.Errorf("days must be between 0 and %d", maxBackupDays)
	}

	jw := &jsonWriter{w: w}
	jw.raw("{")
	jw.field("format", BackupFormat)
	jw.field("format_version", BackupFormatVersion)
	jw.field("schema_version", LatestSchemaVersion())
	jw.field("app_version", opts.AppVersion)
	jw.field("created_at", time.Now().UTC().Format(time.RFC3339))
	jw.field("scope", opts.Scope)
	if opts.Scope == BackupScopeHistory {
		jw.field("days", opts.Days)
	}

	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := exportSettings(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportGroups(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportTags(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportIcons(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportSites(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportChannels(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportSensors(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if err := exportUsers(ctx, tx, jw, &counts); err != nil {
			return err
		}
		if opts.Scope != BackupScopeHistory {
			return nil
		}
		return exportHistory(ctx, tx, jw, opts, &counts)
	})
	if err != nil {
		return Counts{}, err
	}

	jw.raw("\n}\n")
	if jw.err != nil {
		return Counts{}, jw.err
	}
	return counts, nil
}

// eachRow hands every row of an open result set to fn, and closes the set when it
// is done with it — on the ordinary path, on a scan that fails, and on fn's own
// error.
//
// The close belongs here rather than in a defer in the caller because several of
// these callers go on to run further statements. There is one connection, and a
// result set left open holds its statement; the next query would wait on it. The
// alternative — `rows.Close()` written out after the loop, plus a second copy on
// the scan-error path — is the shape that eventually grows a leak.
//
// It is not a generic function because `sql.Rows` cannot be returned from one, so
// the caller keeps its own loop body and Scan call. What is centralised is the part
// that must not be got wrong.
func eachRow(rows *sql.Rows, fn func(*sql.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func exportSettings(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return err
	}
	defer rows.Close()

	first := true
	jw.section("settings")
	jw.raw("{")
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return err
		}
		if !first {
			jw.raw(",")
		}
		first = false
		jw.raw("\n    ")
		jw.value(key)
		jw.raw(": ")
		jw.value(value)
		counts.Settings++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	jw.close("}", first)
	return nil
}

func exportGroups(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, icon, colour, sort_order FROM groups
		ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	jw.section("groups")
	jw.raw("[")
	first := true
	for rows.Next() {
		var g BackupGroup
		if err := rows.Scan(&g.Name, &g.Icon, &g.Colour, &g.SortOrder); err != nil {
			return err
		}
		jw.item(first, g)
		first = false
		counts.Groups++
	}
	jw.close("]", first)
	return rows.Err()
}

func exportTags(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, icon, colour, sort_order FROM tags
		ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	jw.section("tags")
	jw.raw("[")
	first := true
	for rows.Next() {
		var t BackupTag
		if err := rows.Scan(&t.Name, &t.Icon, &t.Colour, &t.SortOrder); err != nil {
			return err
		}
		jw.item(first, t)
		first = false
		counts.Tags++
	}
	jw.close("]", first)
	return rows.Err()
}

// exportIcons writes the uploaded images, bytes included.
//
// The bytes make this the one section of the document that is not a sentence per
// row: each image is base64 in the JSON, and one icon is up to eighty-eight
// kilobytes of it. The writer holds one row at a time and never the section, so the
// memory this costs is one icon rather than all of them.
func exportIcons(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, name, media_type, width, height, bytes, created_at FROM icons
		 ORDER BY id ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	jw.section("icons")
	jw.raw("[")
	first := true
	for rows.Next() {
		var i BackupIcon
		if err := rows.Scan(&i.ID, &i.Name, &i.MediaType, &i.Width, &i.Height,
			&i.Data, &i.CreatedAt); err != nil {
			return err
		}
		jw.item(first, i)
		first = false
		counts.Icons++
	}
	jw.close("]", first)
	return rows.Err()
}

func exportSites(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, kind, lat, lon, icon, sort_order, managed FROM sites
		ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	jw.section("sites")
	jw.raw("[")
	first := true
	for rows.Next() {
		var (
			site     BackupSite
			lat, lon sql.NullFloat64
			managed  int
		)
		if err := rows.Scan(&site.Name, &site.Kind, &lat, &lon, &site.Icon,
			&site.SortOrder, &managed); err != nil {
			return err
		}
		if lat.Valid {
			site.Lat = &lat.Float64
		}
		if lon.Valid {
			site.Lon = &lon.Float64
		}
		site.Managed = managed != 0
		jw.item(first, site)
		first = false
		counts.Sites++
	}
	jw.close("]", first)
	return rows.Err()
}

func exportChannels(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT name, type, config, enabled, managed FROM channels
		ORDER BY name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	jw.section("channels")
	jw.raw("[")
	first := true
	for rows.Next() {
		var (
			channel BackupChannel
			config  string
			enabled int
			managed int
		)
		if err := rows.Scan(&channel.Name, &channel.Type, &config, &enabled, &managed); err != nil {
			return err
		}
		// The stored config holds the real token. It travels as it is; see the
		// paragraph on secrets at the top of this file.
		channel.Config = map[string]string{}
		if config != "" {
			if err := json.Unmarshal([]byte(config), &channel.Config); err != nil {
				return fmt.Errorf("channel %q: corrupt config JSON: %w", channel.Name, err)
			}
		}
		channel.Enabled = enabled != 0
		channel.Managed = managed != 0
		jw.item(first, channel)
		first = false
		counts.Channels++
	}
	jw.close("]", first)
	return rows.Err()
}

func exportSensors(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, name, type, target, interval_s, timeout_s, retries, enabled, grp,
		       COALESCE(site,''), COALESCE(parent,''), icon, tags, options, reminder_every_s,
		       escalate_after_s, sort_order, managed, origin
		FROM sensors ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		id     int64
		sensor BackupSensor
	}
	all := []row{}
	for rows.Next() {
		var (
			r       row
			site    string
			parent  string
			tags    string
			options string
			enabled int
			managed int
		)
		if err := rows.Scan(&r.id, &r.sensor.Name, &r.sensor.Type, &r.sensor.Target,
			&r.sensor.Interval, &r.sensor.Timeout, &r.sensor.Retries, &enabled,
			&r.sensor.Group, &site, &parent, &r.sensor.Icon, &tags, &options, &r.sensor.ReminderEvery,
			&r.sensor.EscalateAfter, &r.sensor.SortOrder, &managed, &r.sensor.Origin); err != nil {
			return err
		}
		r.sensor.Enabled = enabled != 0
		r.sensor.Managed = managed != 0
		r.sensor.Site = site
		r.sensor.Parent = parent
		r.sensor.Tags = model.SplitTags(tags)
		r.sensor.Options = map[string]any{}
		if options != "" {
			if err := json.Unmarshal([]byte(options), &r.sensor.Options); err != nil {
				return fmt.Errorf("sensor %q: corrupt options JSON: %w", r.sensor.Name, err)
			}
		}
		r.sensor.Channels = []string{}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Closed before the second query: there is one connection, and a query open on
	// it while another is asked for would wait for itself.
	rows.Close()

	// The alert channels of a sensor are a mapping table, so they are a second
	// query. It is run once for the whole list rather than one per sensor: an
	// installation has dozens of sensors, and dozens of round trips through the
	// single connection is exactly the kind of thing that turns a two-second backup
	// into a slow one.
	links, err := tx.QueryContext(ctx, `
		SELECT sc.sensor_id, c.name
		FROM sensor_channels sc JOIN channels c ON c.id = sc.channel_id
		ORDER BY c.name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	byName := map[int64][]string{}
	if err := eachRow(links, func(r *sql.Rows) error {
		var sensorID int64
		var name string
		if err := r.Scan(&sensorID, &name); err != nil {
			return err
		}
		byName[sensorID] = append(byName[sensorID], name)
		return nil
	}); err != nil {
		return err
	}
	// By index, not by value: `for _, r := range all` hands out a copy, and writing
	// into the copy assigns the channel list to a struct that is thrown away at the
	// end of the iteration. Every sensor would then be exported with no channels at
	// all — a backup that restores an installation in which nothing alerts.
	for i := range all {
		if names := byName[all[i].id]; len(names) > 0 {
			all[i].sensor.Channels = names
		}
	}

	// The escalation list is the same mapping, in its own table.
	escalations, err := tx.QueryContext(ctx, `
		SELECT ec.sensor_id, c.name
		FROM sensor_escalation_channels ec JOIN channels c ON c.id = ec.channel_id
		ORDER BY c.name COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	byEscalation := map[int64][]string{}
	if err := eachRow(escalations, func(r *sql.Rows) error {
		var sensorID int64
		var name string
		if err := r.Scan(&sensorID, &name); err != nil {
			return err
		}
		byEscalation[sensorID] = append(byEscalation[sensorID], name)
		return nil
	}); err != nil {
		return err
	}
	for i := range all {
		if names := byEscalation[all[i].id]; len(names) > 0 {
			all[i].sensor.EscalateChannels = names
		}
	}

	jw.section("sensors")
	jw.raw("[")
	first := true
	for _, r := range all {
		jw.item(first, r.sensor)
		first = false
		counts.Sensors++
	}
	jw.close("]", first)
	return nil
}

func exportUsers(ctx context.Context, tx *sql.Tx, jw *jsonWriter, counts *Counts) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, username, password_hash, role, totp_secret, totp_enabled, totp_step,
		       token_epoch, disabled, prefs
		FROM users ORDER BY username COLLATE NOCASE ASC`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type row struct {
		id   int64
		user BackupUser
	}
	all := []row{}
	for rows.Next() {
		var (
			r           row
			totpEnabled int
			disabled    int
			prefs       string
		)
		if err := rows.Scan(&r.id, &r.user.Username, &r.user.PasswordHash, &r.user.Role,
			&r.user.TOTPSecret, &totpEnabled, &r.user.TOTPStep, &r.user.TokenEpoch,
			&disabled, &prefs); err != nil {
			return err
		}
		r.user.TOTPEnabled = totpEnabled != 0
		r.user.Disabled = disabled != 0
		r.user.Prefs = map[string]string{}
		if prefs != "" {
			if err := json.Unmarshal([]byte(prefs), &r.user.Prefs); err != nil {
				return fmt.Errorf("user %q: corrupt prefs JSON: %w", r.user.Username, err)
			}
		}
		r.user.Recovery = []BackupRecovery{}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	codes, err := tx.QueryContext(ctx,
		`SELECT user_id, code_hash, used_at FROM recovery_codes ORDER BY id ASC`)
	if err != nil {
		return err
	}
	byUser := map[int64][]BackupRecovery{}
	if err := eachRow(codes, func(r *sql.Rows) error {
		var userID int64
		var code BackupRecovery
		if err := r.Scan(&userID, &code.CodeHash, &code.UsedAt); err != nil {
			return err
		}
		byUser[userID] = append(byUser[userID], code)
		return nil
	}); err != nil {
		return err
	}
	for i := range all {
		if list := byUser[all[i].id]; len(list) > 0 {
			all[i].user.Recovery = list
		}
	}

	jw.section("users")
	jw.raw("[")
	first := true
	for _, r := range all {
		jw.item(first, r.user)
		first = false
		counts.Users++
	}
	jw.close("]", first)
	return nil
}

// exportHistory adds the outages and the rollups. It is the half of the backup that
// only the history scope carries.
func exportHistory(ctx context.Context, tx *sql.Tx, jw *jsonWriter, opts ExportOptions, counts *Counts) error {
	since := ""
	if opts.Days > 0 {
		since = fmtTime(time.Now().UTC().AddDate(0, 0, -opts.Days))
	}

	incidents, err := tx.QueryContext(ctx, `
		SELECT COALESCE(s.name,''), i.started_at, COALESCE(i.resolved_at,''), i.cause,
		       COALESCE(i.acked_at,''), i.acked_by, i.ack_note
		FROM incidents i JOIN sensors s ON s.id = i.sensor_id
		WHERE (? = '' OR i.started_at >= ?)
		ORDER BY i.started_at ASC`, since, since)
	if err != nil {
		return err
	}
	jw.section("incidents")
	jw.raw("[")
	first := true
	if err := eachRow(incidents, func(r *sql.Rows) error {
		var incident BackupIncident
		if err := r.Scan(&incident.Sensor, &incident.StartedAt, &incident.ResolvedAt,
			&incident.Cause, &incident.AckedAt, &incident.AckedBy, &incident.AckNote); err != nil {
			return err
		}
		jw.item(first, incident)
		first = false
		counts.Incidents++
		return nil
	}); err != nil {
		return err
	}
	jw.close("]", first)

	for _, table := range []struct {
		name  string
		key   string
		total *int
	}{
		{"results_hourly", "results_hourly", &counts.Hourly},
		{"results_daily", "results_daily", &counts.Daily},
	} {
		rows, err := tx.QueryContext(ctx, `
			SELECT COALESCE(s.name,''), r.bucket, r.samples, r.ok,
			       r.latency_sum, r.latency_min, r.latency_max
			FROM `+table.name+` r JOIN sensors s ON s.id = r.sensor_id
			WHERE (? = '' OR r.bucket >= ?)
			ORDER BY r.bucket ASC`, since, since)
		if err != nil {
			return err
		}
		jw.section(table.key)
		jw.raw("[")
		first := true
		if err := eachRow(rows, func(r *sql.Rows) error {
			var rollup BackupRollup
			if err := r.Scan(&rollup.Sensor, &rollup.Bucket, &rollup.Samples, &rollup.OK,
				&rollup.LatencySum, &rollup.LatencyMin, &rollup.LatencyMax); err != nil {
				return err
			}
			jw.item(first, rollup)
			first = false
			*table.total++
			return nil
		}); err != nil {
			return err
		}
		jw.close("]", first)
	}
	return nil
}

// ---------------------------------------------------------------------------
// restoring
// ---------------------------------------------------------------------------

// RestoreOptions says what to do about the copy taken before the restore.
type RestoreOptions struct {
	// SafetyCopyDir is where the byte-exact copy of the database is written before
	// anything is replaced. Empty means the directory the database lives in.
	SafetyCopyDir string
	// SkipSafetyCopy is for tests, which restore into a scratch directory and have
	// nothing worth preserving. It must never be set from a request.
	SkipSafetyCopy bool
}

// RestoreResult is what the restore did, in numbers.
type RestoreResult struct {
	Counts Counts `json:"counts"`
	// SafetyCopy is the file the previous state was written to, if one was written.
	SafetyCopy string `json:"safety_copy,omitempty"`
	// TokenEpochNotice is set when the file brings accounts whose sessions were
	// signed out after the file was written. Restoring such a file brings those
	// sessions back, which is a security decision, so it is returned rather than
	// only logged.
	TokenEpochNotice bool `json:"token_epoch_notice,omitempty"`
}

// Restore reads a backup and puts it in place of what is here.
//
// # The order, and why it is that order
//
//  1. Read and check the whole document. Nothing is touched until the file has
//     said what it is and which schema it came from. A file from a newer version
//     is refused with a sentence that says so, rather than half-read into a
//     database that then disagrees with itself.
//  2. Copy the database, byte for byte, with `VACUUM INTO`. This is the way back
//     when a restore was the wrong idea, and it is also the only way back — the
//     dashboard has no undo. If the copy cannot be taken, nothing is restored. It
//     runs outside the transaction below because `VACUUM` cannot run inside one.
//  3. Replace what the file names, in one transaction. The store runs with a single
//     connection, so a concurrent /api/summary is held until the transaction ends
//     and then sees the whole of the new state. It can never see half of it.
//
// # What "replace" means
//
// Sensors, channels, sites and accounts are matched by name, not by the ids they
// had in the file. A sensor that exists here already is updated where it stands, so
// its id — and the history hanging off it — survives; one that does not exist yet is
// inserted. A sensor the file does not name is removed, and its history goes with
// it, because a backup that does not mention a sensor is a statement that there was
// no such sensor.
//
// That is what keeps the promise of the config scope: restoring an older
// configuration does not throw away the outage log of the sensors it does mention.
//
// A name in the history that no sensor here carries is an error. A file that talks
// about sensors this installation has never had is not a file this installation can
// be restored from, and dropping those rows silently would be the same as inventing
// a history.
func (s *Store) Restore(ctx context.Context, r io.Reader, opts RestoreOptions) (RestoreResult, error) {
	var result RestoreResult

	// Streaming decode, no io.ReadAll: the body is already capped by the caller, and
	// a decoder over the reader keeps the bytes from being held twice.
	var doc BackupDoc
	dec := json.NewDecoder(r)
	if err := dec.Decode(&doc); err != nil {
		// The read error is kept underneath: a body over the caller's limit arrives
		// here as http.MaxBytesError, and the route has to be able to tell that
		// apart from a file that is simply not JSON.
		return result, fmt.Errorf("%w: this is not a ProbeGlass backup: %w", ErrBadBackup, err)
	}
	if err := checkBackupDoc(&doc); err != nil {
		return result, err
	}

	if !opts.SkipSafetyCopy {
		copyPath, err := s.safetyCopy(opts.SafetyCopyDir)
		if err != nil {
			return result, fmt.Errorf("could not copy the database before restoring, so nothing was restored: %w", err)
		}
		result.SafetyCopy = copyPath
	}

	err := s.tx(ctx, func(tx *sql.Tx) error {
		counts, notice, err := restoreAll(ctx, tx, doc)
		if err != nil {
			return err
		}
		result.Counts = counts
		result.TokenEpochNotice = notice
		return nil
	})
	if err != nil {
		return RestoreResult{}, err
	}
	return result, nil
}

// checkBackupDoc refuses a file that is not one of ours before anything is written.
//
// Every complaint here is about the file, so every one of them is ErrBadBackup: the
// route turns that into a 400 and shows the sentence as it stands. The sentences are
// written to be read by whoever chose the file, which is why they say what is wrong
// with it rather than which check failed.
func checkBackupDoc(doc *BackupDoc) error {
	if doc.Format != BackupFormat {
		return fmt.Errorf("%w: this file is not a ProbeGlass backup (it says %q)",
			ErrBadBackup, doc.Format)
	}
	if doc.FormatVersion > BackupFormatVersion {
		return fmt.Errorf("%w: this backup was written in format version %d, and this version reads up to %d: "+
			"update ProbeGlass and try again", ErrBadBackup, doc.FormatVersion, BackupFormatVersion)
	}
	if doc.SchemaVersion > LatestSchemaVersion() {
		return fmt.Errorf("%w: this backup comes from a newer database schema (%d, this is %d): "+
			"update ProbeGlass and try again", ErrBadBackup, doc.SchemaVersion, LatestSchemaVersion())
	}
	if doc.Settings == nil && doc.Sites == nil && doc.Channels == nil && doc.Sensors == nil &&
		doc.Users == nil && doc.Incidents == nil && doc.Hourly == nil && doc.Daily == nil {
		return fmt.Errorf("%w: this backup is empty, it names no table at all", ErrBadBackup)
	}
	return nil
}

// safetyCopy writes the current database to a new file beside it and returns the
// path.
//
// `VACUUM INTO` rather than a file copy. It takes a read lock rather than guessing
// at the WAL, it compacts while it writes, and it is the same instruction the
// documentation gives for a hand-made copy — so there is one way to copy this
// database and the program uses it too.
func (s *Store) safetyCopy(dir string) (string, error) {
	current := s.Path()
	if current == "" {
		return "", errors.New("the database path could not be determined")
	}
	if dir == "" {
		dir = filepath.Dir(current)
	}
	name := fmt.Sprintf("probeglass-pre-restore-%s.db", time.Now().UTC().Format("20060102-150405"))
	target := filepath.Join(dir, name)

	// Removed first: VACUUM INTO refuses to overwrite, and a leftover file from an
	// earlier restore in the same second would otherwise fail this one.
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, err := s.db.ExecContext(context.Background(), `VACUUM INTO ?`, target); err != nil {
		return "", err
	}
	return target, nil
}

// restoreAll replaces every table the document names, inside the caller's
// transaction.
func restoreAll(ctx context.Context, tx *sql.Tx, doc BackupDoc) (Counts, bool, error) {
	var counts Counts
	notice := false

	if doc.Settings != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings`); err != nil {
			return counts, false, err
		}
		for key, value := range *doc.Settings {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO settings (key, value, updated_at) VALUES (?,?,?)`,
				key, value, fmtTime(time.Now().UTC())); err != nil {
				return counts, false, fmt.Errorf("setting %q: %w", key, err)
			}
			counts.Settings++
		}
	}

	if doc.Users != nil {
		var err error
		notice, err = restoreUsers(ctx, tx, *doc.Users, &counts)
		if err != nil {
			return counts, false, err
		}
	}

	// Icons and groups first: nothing points at them through a foreign key, so
	// their order among themselves does not matter — but a sensor restored a moment
	// later should find the image it names already in the table rather than the
	// other way round, and reading the statements in the order they are applied is
	// worth more than the microseconds it costs.
	if doc.Groups != nil {
		if err := restoreGroups(ctx, tx, *doc.Groups, &counts); err != nil {
			return counts, false, err
		}
	}

	if doc.Tags != nil {
		if err := restoreTags(ctx, tx, *doc.Tags, &counts); err != nil {
			return counts, false, err
		}
	}

	if doc.Icons != nil {
		if err := restoreIcons(ctx, tx, *doc.Icons, &counts); err != nil {
			return counts, false, err
		}
	}

	// Sites before sensors: a sensor's site is a foreign key into this table, and
	// SQLite checks it at the end of the statement rather than deferring it to the
	// commit.
	if doc.Sites != nil {
		if err := restoreSites(ctx, tx, *doc.Sites, &counts); err != nil {
			return counts, false, err
		}
	}

	if doc.Channels != nil {
		if err := restoreChannels(ctx, tx, *doc.Channels, &counts); err != nil {
			return counts, false, err
		}
	}

	sensorIDs := map[string]int64{}
	if doc.Sensors != nil {
		if err := restoreSensors(ctx, tx, *doc.Sensors, sensorIDs, &counts); err != nil {
			return counts, false, err
		}
	}

	if doc.Incidents != nil {
		if err := restoreIncidents(ctx, tx, *doc.Incidents, sensorIDs, &counts); err != nil {
			return counts, false, err
		}
	}

	for _, table := range []struct {
		name   string
		rollup []BackupRollup
		total  *int
	}{
		{"results_hourly", derefRollups(doc.Hourly), &counts.Hourly},
		{"results_daily", derefRollups(doc.Daily), &counts.Daily},
	} {
		if table.rollup == nil {
			continue
		}
		if err := restoreRollups(ctx, tx, table.name, table.rollup, sensorIDs, table.total); err != nil {
			return counts, false, err
		}
	}

	return counts, notice, nil
}

func derefRollups(list *[]BackupRollup) []BackupRollup {
	if list == nil {
		return nil
	}
	return *list
}

// restoreUsers replaces the accounts and their one-time codes.
//
// # The accounts are matched by name, like everything else
//
// An account that is already here is updated where it stands and keeps its row id. That
// is not tidiness: the session cookie carries the account's id and the epoch it was
// issued under, so an account that is deleted and re-inserted under a new id signs its
// owner out — including the administrator who is at that moment restoring their own
// backup, which is the ordinary case for this screen. Keeping the id keeps them signed
// in, which is what the plan for this feature says should happen.
//
// Re-using an id is not a risk worth guarding against separately. The table declares
// AUTOINCREMENT, so a deleted account's id is never handed to another account for the
// life of the database; a cookie left over from an account this file does not name
// finds no row and is refused. That is the deletion working, not a hole.
//
// The boolean it returns says whether this file takes sign-ins back: an epoch lower
// than the one in the database means sessions that were ended after the file was
// written are valid again.
func restoreUsers(ctx context.Context, tx *sql.Tx, users []BackupUser, counts *Counts) (bool, error) {
	type account struct {
		id    int64
		epoch int64
	}
	current := map[string]account{}
	rows, err := tx.QueryContext(ctx, `SELECT id, username, token_epoch FROM users`)
	if err != nil {
		return false, err
	}
	if err := eachRow(rows, func(r *sql.Rows) error {
		var name string
		var a account
		if err := r.Scan(&a.id, &name, &a.epoch); err != nil {
			return err
		}
		current[strings.ToLower(name)] = a
		return nil
	}); err != nil {
		return false, err
	}

	// Deleting every account and inserting the file's back would break the one rule
	// the user store enforces everywhere else — there is always an administrator.
	// The file's own accounts are what is being restored, so the check is not
	// skipped, it is applied to the file: a restore that would leave no administrator
	// is refused.
	if err := checkAdminSurvives(users); err != nil {
		return false, err
	}

	// The file is the whole truth about which accounts exist, so anything it does not
	// name goes — and the one-time codes of those accounts go with them, through the
	// foreign key.
	named := make(map[string]bool, len(users))
	for _, user := range users {
		named[strings.ToLower(user.Username)] = true
	}
	for name, a := range current {
		if named[name] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, a.id); err != nil {
			return false, err
		}
	}

	notice := false
	now := time.Now().UTC()
	for _, user := range users {
		prefs, err := json.Marshal(user.Prefs)
		if err != nil {
			return false, fmt.Errorf("user %q: %w", user.Username, err)
		}
		existing, exists := current[strings.ToLower(user.Username)]
		if exists && user.TokenEpoch < existing.epoch {
			notice = true
		}

		var userID int64
		if exists {
			// created_at and last_login_at are not in the file — it says nothing about
			// when an account was made or when it last signed in, and writing "now" over
			// both would invent two facts. They are left as they are, like every other
			// column of a table the file does not name.
			if _, err := tx.ExecContext(ctx, `
				UPDATE users SET password_hash = ?, role = ?, totp_secret = ?, totp_enabled = ?,
				                 totp_step = ?, token_epoch = ?, disabled = ?, prefs = ?,
				                 updated_at = ?
				 WHERE id = ?`,
				user.PasswordHash, user.Role, user.TOTPSecret, boolToInt(user.TOTPEnabled),
				user.TOTPStep, user.TokenEpoch, boolToInt(user.Disabled), string(prefs),
				fmtTime(now), existing.id); err != nil {
				return false, fmt.Errorf("user %q: %w", user.Username, err)
			}
			userID = existing.id
		} else {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO users (username, password_hash, role, totp_secret, totp_enabled,
				                   totp_step, token_epoch, disabled, prefs, created_at,
				                   updated_at, last_login_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,'')`,
				user.Username, user.PasswordHash, user.Role, user.TOTPSecret, boolToInt(user.TOTPEnabled),
				user.TOTPStep, user.TokenEpoch, boolToInt(user.Disabled), string(prefs),
				fmtTime(now), fmtTime(now))
			if err != nil {
				return false, fmt.Errorf("user %q: %w", user.Username, err)
			}
			if userID, err = res.LastInsertId(); err != nil {
				return false, err
			}
		}

		// The codes are replaced rather than merged: the file's list is what was valid
		// when it was written, and an old code that survives a restore is a way back in
		// that nobody knows about.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return false, err
		}
		for _, code := range user.Recovery {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO recovery_codes (user_id, code_hash, used_at, created_at) VALUES (?,?,?,?)`,
				userID, code.CodeHash, code.UsedAt, fmtTime(now)); err != nil {
				return false, fmt.Errorf("recovery code of %q: %w", user.Username, err)
			}
		}
		counts.Users++
	}
	return notice, nil
}

// checkAdminSurvives refuses a restore that would leave the installation with no
// administrator left to sign in as.
func checkAdminSurvives(users []BackupUser) error {
	for _, user := range users {
		if user.Role == string(model.RoleAdmin) && !user.Disabled {
			return nil
		}
	}
	return fmt.Errorf("%w: this backup holds no enabled administrator account, so restoring it would lock everybody out of the dashboard", ErrBadBackup)
}

func restoreSites(ctx context.Context, tx *sql.Tx, sites []BackupSite, counts *Counts) error {
	// Sensors whose site is not in the file would block the delete below through the
	// foreign key. Clearing the column first is the honest order: the sites are being
	// replaced, and a sensor that is about to be given its site back by name gets it
	// a few statements later.
	if _, err := tx.ExecContext(ctx, `UPDATE sensors SET site = NULL WHERE site IS NOT NULL`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sites`); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, site := range sites {
		if site.Name == "" {
			return fmt.Errorf("%w: a site in this backup has no name", ErrBadBackup)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sites (name, kind, lat, lon, icon, sort_order, managed, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			site.Name, site.Kind, site.Lat, site.Lon, site.Icon, site.SortOrder,
			boolToInt(site.Managed), fmtTime(now), fmtTime(now)); err != nil {
			return fmt.Errorf("site %q: %w", site.Name, err)
		}
		counts.Sites++
	}
	return nil
}

// restoreGroups replaces what the file says about groups.
//
// Nothing has to be cleared first, which is the pleasant half of a group being a
// word on a sensor rather than a row: no foreign key points here, so the rows can be
// replaced in any order and a sensor whose group the file does not mention simply
// has no settings, exactly as it would on a fresh installation.
func restoreGroups(ctx context.Context, tx *sql.Tx, groups []BackupGroup, counts *Counts) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM groups`); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, g := range groups {
		if g.Name == "" {
			return fmt.Errorf("%w: a group in this backup has no name", ErrBadBackup)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO groups (name, icon, colour, sort_order, created_at, updated_at)
			VALUES (?,?,?,?,?,?)`,
			g.Name, g.Icon, g.Colour, g.SortOrder, fmtTime(now), fmtTime(now)); err != nil {
			return fmt.Errorf("group %q: %w", g.Name, err)
		}
		counts.Groups++
	}
	return nil
}

// restoreTags replaces what the file says about tags. Like groups, nothing has to be
// cleared first: no foreign key points here, so a sensor whose tag the file does not
// mention simply has no settings for it.
func restoreTags(ctx context.Context, tx *sql.Tx, tags []BackupTag, counts *Counts) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags`); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, t := range tags {
		if t.Name == "" {
			return fmt.Errorf("%w: a tag in this backup has no name", ErrBadBackup)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tags (name, icon, colour, sort_order, created_at, updated_at)
			VALUES (?,?,?,?,?,?)`,
			t.Name, t.Icon, t.Colour, t.SortOrder, fmtTime(now), fmtTime(now)); err != nil {
			return fmt.Errorf("tag %q: %w", t.Name, err)
		}
		counts.Tags++
	}
	return nil
}

// restoreIcons replaces the uploaded images, ids and all.
//
// The id is written back as the file has it, which is the one row in this document
// that keeps its identity through a restore. It has to be: a sensor's icon column
// says `upload:7` as plain text, with no foreign key and nothing to re-link it, so
// an image that came back under a different number would leave every reference
// pointing at whatever else landed there. The bytes are not re-validated — this is a
// file this program wrote, and refusing half of it because an image is larger than
// today's cap would be a restore that did not happen for a reason nobody could act
// on.
//
// A sensor whose icon the file does not carry keeps the reference it has, and it
// draws nothing. That is the same state an icon reaches by being deleted, and it is
// why the delete endpoint asks first: the reference is only a string.
func restoreIcons(ctx context.Context, tx *sql.Tx, icons []BackupIcon, counts *Counts) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM icons`); err != nil {
		return err
	}
	for _, i := range icons {
		if len(i.Data) == 0 {
			return fmt.Errorf("%w: icon %d in this backup has no image", ErrBadBackup, i.ID)
		}
		createdAt := i.CreatedAt
		if createdAt == "" {
			createdAt = fmtTime(time.Now().UTC())
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO icons (id, name, media_type, width, height, bytes, created_at)
			VALUES (?,?,?,?,?,?,?)`,
			i.ID, i.Name, i.MediaType, i.Width, i.Height, i.Data, createdAt); err != nil {
			return fmt.Errorf("icon %d: %w", i.ID, err)
		}
		counts.Icons++
	}
	return nil
}

func restoreChannels(ctx context.Context, tx *sql.Tx, channels []BackupChannel, counts *Counts) error {
	// sensor_channels cascades with the channels, which is right: the file says what
	// the links are, and the sensors are re-linked below from the same file.
	if _, err := tx.ExecContext(ctx, `DELETE FROM channels`); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, channel := range channels {
		if channel.Name == "" {
			return fmt.Errorf("%w: a channel in this backup has no name", ErrBadBackup)
		}
		config, err := json.Marshal(channel.Config)
		if err != nil {
			return fmt.Errorf("channel %q: %w", channel.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channels (name, type, config, enabled, managed, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?)`,
			channel.Name, channel.Type, string(config), boolToInt(channel.Enabled),
			boolToInt(channel.Managed), fmtTime(now), fmtTime(now)); err != nil {
			return fmt.Errorf("channel %q: %w", channel.Name, err)
		}
		counts.Channels++
	}
	return nil
}

// restoreSensors replaces the sensor list, keeping the id of every sensor that is
// already here under the same name.
//
// Keeping the id is the whole reason this is an update rather than a delete and an
// insert. `results` and `incidents` hang off `sensors.id` with ON DELETE CASCADE, so
// dropping the rows and putting them back would take the entire history of every
// sensor with it — and the config scope of a backup exists precisely so that
// restoring an older configuration does not cost you your outage log.
func restoreSensors(ctx context.Context, tx *sql.Tx, sensors []BackupSensor, ids map[string]int64, counts *Counts) error {
	existing := map[string]int64{}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM sensors`)
	if err != nil {
		return err
	}
	if err := eachRow(rows, func(r *sql.Rows) error {
		var id int64
		var name string
		if err := r.Scan(&id, &name); err != nil {
			return err
		}
		existing[strings.ToLower(name)] = id
		return nil
	}); err != nil {
		return err
	}

	named := map[string]bool{}
	for _, sensor := range sensors {
		named[strings.ToLower(sensor.Name)] = true
	}
	// A sensor the file does not name is gone, and its history with it. That is what
	// replacing a table means, and the alternative — leaving it behind — would make a
	// restore an incomplete merge that nobody could predict.
	for lower, id := range existing {
		if named[lower] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sensors WHERE id = ?`, id); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	for _, sensor := range sensors {
		if sensor.Name == "" || sensor.Type == "" {
			return fmt.Errorf("%w: a sensor in this backup has no name or no type", ErrBadBackup)
		}
		tags := model.JoinTags(sensor.Tags)
		sensor.Options = normaliseOptions(sensor.Options)
		options, err := json.Marshal(sensor.Options)
		if err != nil {
			return fmt.Errorf("sensor %q: %w", sensor.Name, err)
		}
		interval, timeout, retries := sensor.Interval, sensor.Timeout, sensor.Retries
		if interval <= 0 {
			interval = 60
		}
		if timeout <= 0 {
			timeout = 10
		}
		if retries < 0 {
			retries = 0
		}
		site := sql.NullString{String: sensor.Site, Valid: sensor.Site != ""}

		id, exists := existing[strings.ToLower(sensor.Name)]
		if exists {
			if _, err := tx.ExecContext(ctx, `
				UPDATE sensors SET type=?, target=?, interval_s=?, timeout_s=?, retries=?,
				       enabled=?, grp=?, site=?, parent=?, tags=?, options=?, reminder_every_s=?,
				       escalate_after_s=?, sort_order=?, managed=?, origin=?, updated_at=?
				WHERE id=?`,
				sensor.Type, sensor.Target, interval, timeout, retries, boolToInt(sensor.Enabled),
				sensor.Group, site, nullIfEmpty(sensor.Parent), tags, string(options),
				sensor.ReminderEvery, sensor.EscalateAfter, sensor.SortOrder,
				boolToInt(sensor.Managed), sensor.Origin, fmtTime(now), id); err != nil {
				return fmt.Errorf("sensor %q: %w", sensor.Name, err)
			}
		} else {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO sensors (name, type, target, interval_s, timeout_s, retries, enabled,
				                     grp, site, parent, icon, tags, options, reminder_every_s,
				                     escalate_after_s, sort_order, managed, origin, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
				sensor.Name, sensor.Type, sensor.Target, interval, timeout, retries,
				boolToInt(sensor.Enabled), sensor.Group, site, nullIfEmpty(sensor.Parent),
				sensor.Icon, tags, string(options), sensor.ReminderEvery, sensor.EscalateAfter,
				sensor.SortOrder, boolToInt(sensor.Managed), sensor.Origin, fmtTime(now), fmtTime(now))
			if err != nil {
				return fmt.Errorf("sensor %q: %w", sensor.Name, err)
			}
			if id, err = res.LastInsertId(); err != nil {
				return err
			}
		}
		ids[strings.ToLower(sensor.Name)] = id
		counts.Sensors++

		if err := relinkChannels(ctx, tx, id, sensor.Channels); err != nil {
			return fmt.Errorf("sensor %q: %w", sensor.Name, err)
		}
		if err := relinkEscalationChannels(ctx, tx, id, sensor.EscalateChannels); err != nil {
			return fmt.Errorf("sensor %q: %w", sensor.Name, err)
		}
	}
	return nil
}

// relinkEscalationChannels is relinkChannels for the second list.
func relinkEscalationChannels(ctx context.Context, tx *sql.Tx, sensorID int64, names []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sensor_escalation_channels WHERE sensor_id = ?`, sensorID); err != nil {
		return err
	}
	for _, name := range names {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sensor_escalation_channels (sensor_id, channel_id)
			SELECT ?, id FROM channels WHERE name = ? COLLATE NOCASE`, sensorID, name)
		if err != nil {
			return fmt.Errorf("escalation channel %q: %w", name, err)
		}
	}
	return nil
}

// relinkChannels sets the alert channels of one sensor from a list of names.
func relinkChannels(ctx context.Context, tx *sql.Tx, sensorID int64, names []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sensor_channels WHERE sensor_id = ?`, sensorID); err != nil {
		return err
	}
	for _, name := range names {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO sensor_channels (sensor_id, channel_id)
			SELECT ?, id FROM channels WHERE name = ? COLLATE NOCASE`, sensorID, name)
		if err != nil {
			return fmt.Errorf("channel %q: %w", name, err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			// A sensor pointing at a channel that is not in the file. Left as it is
			// rather than refused: the sensor is still a sensor, and a missing alert
			// channel is visible in the dashboard, whereas refusing the whole file
			// over it would leave the person with nothing restored at all.
			continue
		}
	}
	return nil
}

func restoreIncidents(ctx context.Context, tx *sql.Tx, incidents []BackupIncident, ids map[string]int64, counts *Counts) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM incidents`); err != nil {
		return err
	}
	for _, incident := range incidents {
		sensorID, ok := ids[strings.ToLower(incident.Sensor)]
		if !ok {
			return fmt.Errorf("%w: this backup mentions the sensor %q, which this installation does not have", ErrBadBackup, incident.Sensor)
		}
		var resolved, acked any
		if incident.ResolvedAt != "" {
			resolved = incident.ResolvedAt
		}
		if incident.AckedAt != "" {
			acked = incident.AckedAt
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO incidents (sensor_id, started_at, resolved_at, cause, acked_at, acked_by, ack_note)
			VALUES (?,?,?,?,?,?,?)`,
			sensorID, incident.StartedAt, resolved, incident.Cause, acked,
			incident.AckedBy, incident.AckNote); err != nil {
			return fmt.Errorf("incident of %q: %w", incident.Sensor, err)
		}
		counts.Incidents++
	}
	return nil
}

func restoreRollups(ctx context.Context, tx *sql.Tx, table string, rollups []BackupRollup, ids map[string]int64, total *int) error {
	// The table name is a constant of this function's two call sites and never a
	// value from the request; it is concatenated because SQLite does not accept a
	// placeholder for a table name.
	if table != "results_hourly" && table != "results_daily" {
		return fmt.Errorf("unknown rollup table %q", table)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
		return err
	}
	for _, rollup := range rollups {
		sensorID, ok := ids[strings.ToLower(rollup.Sensor)]
		if !ok {
			return fmt.Errorf("%w: this backup mentions the sensor %q, which this installation does not have", ErrBadBackup, rollup.Sensor)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+` (sensor_id, bucket, samples, ok, latency_sum, latency_min, latency_max)
			 VALUES (?,?,?,?,?,?,?)`,
			sensorID, rollup.Bucket, rollup.Samples, rollup.OK,
			rollup.LatencySum, rollup.LatencyMin, rollup.LatencyMax); err != nil {
			return fmt.Errorf("rollup of %q: %w", rollup.Sensor, err)
		}
		*total++
	}
	return nil
}

// normaliseOptions turns the decoded JSON into the shape the rest of the store
// expects, where a `map[string]any` came back from `encoding/json` with float64
// where an int used to be.
func normaliseOptions(options map[string]any) map[string]any {
	if options == nil {
		return map[string]any{}
	}
	for key, value := range options {
		if f, ok := value.(float64); ok && f == float64(int64(f)) {
			options[key] = int64(f)
		}
	}
	return options
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// writing JSON by hand
// ---------------------------------------------------------------------------

// jsonWriter writes a JSON document one piece at a time.
//
// `encoding/json` encodes a value; it cannot write half of one and come back
// later. A backup is written table by table so that the document never exists in
// memory in full, so the punctuation is written by hand — and this type is where
// that is done once rather than in six loops.
type jsonWriter struct {
	w   io.Writer
	err error
	// fields counts the top-level keys written so far. It is how the writer knows
	// whether the next one needs a comma in front of it.
	fields int
	// buf holds one value while it is encoded, and is reused for the next one. It
	// never holds more than a single row: the document as a whole is still written
	// straight out to w.
	buf bytes.Buffer
}

func (j *jsonWriter) raw(s string) {
	if j.err != nil {
		return
	}
	_, j.err = io.WriteString(j.w, s)
}

// fieldStart writes what goes in front of every top-level key: the comma that
// separates it from the one before it, and the indent.
//
// The comma is written here rather than after the value, and that is not a matter of
// taste. Which field is last depends on the scope — a config backup stops after the
// accounts, a history backup carries on into the outages and the rollups — so a comma
// written behind the value would leave every one of those branches responsible for
// knowing whether it is the final one. Written in front, no branch has to know
// anything: the writer counts.
func (j *jsonWriter) fieldStart() {
	if j.fields > 0 {
		j.raw(",")
	}
	j.fields++
	j.raw("\n  ")
}

// section opens a top-level key whose value the caller writes piece by piece — an
// object of settings, or an array of rows.
func (j *jsonWriter) section(name string) {
	j.fieldStart()
	j.value(name)
	j.raw(": ")
}

// field writes a complete key–value pair.
func (j *jsonWriter) field(name string, value any) {
	j.section(name)
	j.value(value)
}

// item writes one element of an array, with the comma between element n and n+1.
func (j *jsonWriter) item(first bool, value any) {
	if !first {
		j.raw(",")
	}
	j.raw("\n    ")
	j.value(value)
}

// close ends an array or an object that section opened. `empty` says whether anything
// was written between the brackets, which is the one case where the closing character
// has nothing to be put on a line of its own below.
func (j *jsonWriter) close(bracket string, empty bool) {
	if !empty {
		j.raw("\n  ")
	}
	j.raw(bracket)
}

// value encodes one JSON value on its own.
//
// HTML escaping is off: this is a file people open in an editor, and a sensor
// called `A & B <router>` should read as itself. Nothing written here is ever put
// into a page as markup.
//
// `json.Encoder` appends a newline after every value, which is its own idea of
// formatting and not this file's: the punctuation here is placed by hand, and a
// newline arriving in the middle of it — between a key and its colon, say — lands
// where it was not asked for. So the newline is cut off again. One value is buffered
// to do that, never the document, and the buffer is reused, so the cost is a copy of
// the largest single row rather than of the whole backup.
func (j *jsonWriter) value(v any) {
	if j.err != nil {
		return
	}
	j.buf.Reset()
	enc := json.NewEncoder(&j.buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		j.err = err
		return
	}
	if n := j.buf.Len(); n > 0 && j.buf.Bytes()[n-1] == '\n' {
		j.buf.Truncate(n - 1)
	}
	_, j.err = j.buf.WriteTo(j.w)
}
