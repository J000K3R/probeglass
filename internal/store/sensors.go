package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

const sensorColumns = `id, name, type, target, interval_s, timeout_s, retries, enabled,
	grp, site, parent, icon, tags, options, reminder_every_s, escalate_after_s, sort_order, managed, origin, created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSensor(sc rowScanner) (model.Sensor, error) {
	var (
		c         model.Sensor
		typ       string
		site      sql.NullString
		parent    sql.NullString
		tags      string
		options   string
		enabled   int
		managed   int
		createdAt string
		updatedAt string
	)
	err := sc.Scan(&c.ID, &c.Name, &typ, &c.Target, &c.Interval, &c.Timeout, &c.Retries,
		&enabled, &c.Group, &site, &parent, &c.Icon, &tags, &options, &c.ReminderEvery,
		&c.EscalateAfter, &c.SortOrder, &managed, &c.Origin, &createdAt, &updatedAt)
	if err != nil {
		return c, err
	}
	c.Type = model.SensorType(typ)
	// The column is nullable, and its NULL is the ordinary state "no site" — so it
	// becomes the empty string here and never travels further as a Null.
	c.Site = site.String
	// The same for the parent: NULL is "no parent" and becomes the empty string.
	c.Parent = parent.String
	c.Enabled = enabled != 0
	c.Managed = managed != 0
	c.Tags = model.SplitTags(tags)
	c.Options = model.Options{}
	if options != "" {
		if err := json.Unmarshal([]byte(options), &c.Options); err != nil {
			return c, fmt.Errorf("sensor %d: corrupt options JSON: %w", c.ID, err)
		}
	}
	c.CreatedAt = parseTime(createdAt)
	c.UpdatedAt = parseTime(updatedAt)
	return c, nil
}

// ValidateSensor rejects a parent that is not there, and a chain that comes back to
// where it started.
//
// It needs the store, which is why it is here and not in NormaliseSensor: neither the
// normaliser nor sensor.Validate can see another row, and both of the things that can
// go wrong with a parent are about other rows. A sensor whose parent does not exist
// would never be excused by anything — the walk stops, the sensor is judged on its own
// probes — so this is not a safety check. It is the difference between a typo being
// reported and a typo being a sensor that silently does not do what it was told.
//
// A cycle is the other way round and is a safety check, in the sense that matters:
// nothing breaks, but the chain has no top, and "is anything up there down" has no
// answer. The walk is bounded as well, so a database that acquired a cycle by hand
// stops rather than spins.
func (s *Store) ValidateSensor(ctx context.Context, c model.Sensor) error {
	if c.Parent == "" {
		return nil
	}
	if c.Parent == c.Name {
		return fmt.Errorf("sensor %q cannot depend on itself", c.Name)
	}

	// Seen by name, so that a chain that reuses a name is caught even when the sensor
	// itself is being renamed and its id has not moved.
	seen := map[string]bool{c.Name: true}
	name := c.Parent
	for depth := 0; name != ""; depth++ {
		if depth >= model.MaxParentDepth {
			return fmt.Errorf("the chain of parents starting at %q is more than %d deep",
				c.Parent, model.MaxParentDepth)
		}
		if seen[name] {
			return fmt.Errorf("sensor %q would end up in a circle of parents: %q "+
				"already appears in the chain above it", c.Name, name)
		}
		seen[name] = true

		parent, err := s.GetSensorByName(ctx, name)
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("parent sensor %q does not exist", name)
		}
		if err != nil {
			return err
		}
		// The id catches a rename: the sensor's row is still there under its old
		// name, and the walk would otherwise not recognise it as the one being saved.
		if (c.ID != 0 && parent.ID == c.ID) || parent.Name == c.Name {
			return fmt.Errorf("sensor %q cannot depend on %q, which already depends on it",
				c.Name, name)
		}
		name = parent.Parent
	}
	return nil
}

// NormaliseSensor fills in defaults and removes invalid values so that anything
// reaching the database is coherent.
func NormaliseSensor(c *model.Sensor) {
	c.Name = strings.TrimSpace(c.Name)
	// A sensor never sits behind itself, whatever the caller sent. The check that
	// matters is ValidateSensor's, which can see the whole chain; this is the one
	// that costs nothing and makes the field's empty string mean what it says.
	c.Parent = strings.TrimSpace(c.Parent)
	c.Target = strings.TrimSpace(c.Target)
	c.Group = strings.TrimSpace(c.Group)
	c.Site = strings.TrimSpace(c.Site)
	c.Icon = strings.TrimSpace(c.Icon)
	c.Tags = model.SplitTags(model.JoinTags(c.Tags))
	if c.Interval <= 0 {
		c.Interval = 60
	}
	if c.Interval < 5 {
		c.Interval = 5
	}
	if c.Timeout <= 0 {
		c.Timeout = 10
	}
	if c.Timeout > 600 {
		c.Timeout = 600
	}
	if c.Retries < 1 {
		c.Retries = 1
	}
	if c.ReminderEvery < 0 {
		c.ReminderEvery = 0
	}
	if c.EscalateAfter < 0 {
		c.EscalateAfter = 0
	}
	if c.EscalateAfter > 30*24*60*60 {
		c.EscalateAfter = 30 * 24 * 60 * 60
	}
	if c.Options == nil {
		c.Options = model.Options{}
	}
}

// ListSensors returns every sensor ordered for display.
func (s *Store) ListSensors(ctx context.Context) ([]model.Sensor, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+sensorColumns+` FROM sensors ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sensors := []model.Sensor{}
	byID := map[int64]int{}
	for rows.Next() {
		c, err := scanSensor(rows)
		if err != nil {
			return nil, err
		}
		byID[c.ID] = len(sensors)
		sensors = append(sensors, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach channel assignments in one extra query rather than one per sensor.
	links, err := s.allSensorChannels(ctx)
	if err != nil {
		return nil, err
	}
	for sensorID, ids := range links {
		if i, ok := byID[sensorID]; ok {
			sensors[i].Channels = ids
		}
	}
	escalation, err := s.allSensorEscalationChannels(ctx)
	if err != nil {
		return nil, err
	}
	for sensorID, ids := range escalation {
		if i, ok := byID[sensorID]; ok {
			sensors[i].EscalateChannels = ids
		}
	}
	for i := range sensors {
		if sensors[i].Channels == nil {
			sensors[i].Channels = []int64{}
		}
		if sensors[i].EscalateChannels == nil {
			sensors[i].EscalateChannels = []int64{}
		}
	}
	return sensors, nil
}

func (s *Store) allSensorChannels(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sensor_id, channel_id FROM sensor_channels ORDER BY sensor_id, channel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var cid, chid int64
		if err := rows.Scan(&cid, &chid); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], chid)
	}
	return out, rows.Err()
}

func (s *Store) allSensorEscalationChannels(ctx context.Context) (map[int64][]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sensor_id, channel_id FROM sensor_escalation_channels ORDER BY sensor_id, channel_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var cid, chid int64
		if err := rows.Scan(&cid, &chid); err != nil {
			return nil, err
		}
		out[cid] = append(out[cid], chid)
	}
	return out, rows.Err()
}

// GetSensor loads one sensor by id.
func (s *Store) GetSensor(ctx context.Context, id int64) (model.Sensor, error) {
	c, err := scanSensor(s.db.QueryRowContext(ctx,
		`SELECT `+sensorColumns+` FROM sensors WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	ids, err := s.SensorChannelIDs(ctx, id)
	if err != nil {
		return c, err
	}
	c.Channels = ids
	escalation, err := s.SensorEscalationChannelIDs(ctx, id)
	if err != nil {
		return c, err
	}
	c.EscalateChannels = escalation
	return c, nil
}

// GetSensorByName loads one sensor by its unique name.
func (s *Store) GetSensorByName(ctx context.Context, name string) (model.Sensor, error) {
	c, err := scanSensor(s.db.QueryRowContext(ctx,
		`SELECT `+sensorColumns+` FROM sensors WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateSensor inserts a sensor and its channel assignments.
func (s *Store) CreateSensor(ctx context.Context, c *model.Sensor) error {
	return s.tx(ctx, func(tx *sql.Tx) error { return insertSensor(ctx, tx, c) })
}

// CreateSensors writes several sensors in one transaction. A template is either
// applied completely or not at all: a half-made device would leave the dashboard
// with three of a QNAP's four checks and no way to tell which one is missing.
func (s *Store) CreateSensors(ctx context.Context, sensors []*model.Sensor) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, c := range sensors {
			if err := insertSensor(ctx, tx, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// insertSensor writes one sensor row and its channel assignments inside the given
// transaction.
//
// It exists so that the ordinary path and the bulk one — restoring the example
// data writes several rows — cannot drift apart. The transaction is the caller's,
// because CreateSensor may not open a second one and a bulk caller needs all of
// its rows to land or none.
func insertSensor(ctx context.Context, tx *sql.Tx, c *model.Sensor) error {
	NormaliseSensor(c)
	options, err := json.Marshal(c.Options)
	if err != nil {
		return fmt.Errorf("encode options: %w", err)
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now

	res, err := tx.ExecContext(ctx, `
		INSERT INTO sensors (name, type, target, interval_s, timeout_s, retries, enabled,
			grp, site, parent, icon, tags, options, reminder_every_s, escalate_after_s,
			sort_order, managed, origin, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Name, string(c.Type), c.Target, c.Interval, c.Timeout, c.Retries, boolInt(c.Enabled),
		c.Group, nullIfEmpty(c.Site), nullIfEmpty(c.Parent), c.Icon, model.JoinTags(c.Tags), string(options),
		c.ReminderEvery, c.EscalateAfter, c.SortOrder, boolInt(c.Managed), c.Origin,
		fmtTime(now), fmtTime(now))
	if err != nil {
		return wrapSensorErr(err, c)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	c.ID = id
	if err := setSensorChannels(ctx, tx, id, c.Channels); err != nil {
		return err
	}
	return setSensorEscalationChannels(ctx, tx, id, c.EscalateChannels)
}

// UpdateSensor writes an existing sensor and its channel assignments.
//
// origin is deliberately absent from the SET list. It records where a sensor came
// from, which is a fact about the past: editing an example sensor must not turn it
// into a hand-made one, or "remove the example data" would stop finding it.
//
// managed is absent for a different and sharper reason: it says the configuration
// file owns this row, and the guards that stop a file-owned sensor from being
// edited, paused or deleted all read it back from the table. A write that took it
// from whatever the caller happened to hand in could clear it, and the sensor
// would quietly become the dashboard's again — until the next apply reverted
// whatever had been changed.
func (s *Store) UpdateSensor(ctx context.Context, c *model.Sensor) error {
	return s.updateSensor(ctx, c, nil)
}

// updateSensor is UpdateSensor with the one thing the apply path needs and an
// ordinary edit must not have: the ability to write the managed flag. A nil
// pointer means "leave the column as it is", a value means "write this".
func (s *Store) updateSensor(ctx context.Context, c *model.Sensor, managed *bool) error {
	NormaliseSensor(c)
	options, err := json.Marshal(c.Options)
	if err != nil {
		return fmt.Errorf("encode options: %w", err)
	}
	now := time.Now().UTC()
	c.UpdatedAt = now

	return s.tx(ctx, func(tx *sql.Tx) error {
		if managed == nil {
			// Read rather than assumed. The callers that pass nothing hand in
			// differently built values — the dashboard edits a sensor on top of
			// the row it just read, the pause button writes back the row it just
			// loaded, and neither should have to remember this one column for the
			// guards to keep working.
			var current int
			err := tx.QueryRowContext(ctx,
				`SELECT managed FROM sensors WHERE id = ?`, c.ID).Scan(&current)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			flag := current != 0
			managed = &flag
		}
		c.Managed = *managed
		res, err := tx.ExecContext(ctx, `
			UPDATE sensors SET name=?, type=?, target=?, interval_s=?, timeout_s=?, retries=?,
				enabled=?, grp=?, site=?, parent=?, icon=?, tags=?, options=?, reminder_every_s=?,
				escalate_after_s=?, sort_order=?, managed=?, updated_at=?
			WHERE id = ?`,
			c.Name, string(c.Type), c.Target, c.Interval, c.Timeout, c.Retries,
			boolInt(c.Enabled), c.Group, nullIfEmpty(c.Site), nullIfEmpty(c.Parent), c.Icon,
			model.JoinTags(c.Tags), string(options), c.ReminderEvery, c.EscalateAfter,
			c.SortOrder, boolInt(*managed), fmtTime(now), c.ID)
		if err != nil {
			return wrapSensorErr(err, c)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		if err := setSensorChannels(ctx, tx, c.ID, c.Channels); err != nil {
			return err
		}
		return setSensorEscalationChannels(ctx, tx, c.ID, c.EscalateChannels)
	})
}

// DeleteSensor removes a sensor and everything that belongs to it.
//
// The raw results, the incidents and the channel assignments follow by cascade.
// The hourly and daily rollups do not: they were created without a foreign key,
// because a rollup outlives the raw samples it summarises. So they are deleted
// here by hand. Leaving them behind would be invisible — nothing would ever read
// a rollup whose sensor is gone — but it would mean the file never shrinks, and
// the settings screen offers a button that promises exactly that.
func (s *Store) DeleteSensor(ctx context.Context, id int64) error {
	return s.deleteSensors(ctx, `id = ?`, id)
}

// deleteSensors removes every sensor matching a WHERE clause and the rollups that
// hang off them. what is the predicate, spelled with the table's own column names
// because that is where the clause ends up.
func (s *Store) deleteSensors(ctx context.Context, what string, args ...any) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{"results_hourly", "results_daily", "certificates"} {
			q := `DELETE FROM ` + table + ` WHERE sensor_id IN (SELECT id FROM sensors WHERE ` + what + `)`
			if _, err := tx.ExecContext(ctx, q, args...); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM sensors WHERE `+what, args...)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// UpsertManagedSensor creates or updates a sensor that is declared in the config
// file. Everything except the name is taken from the spec.
func (s *Store) UpsertManagedSensor(ctx context.Context, c *model.Sensor) error {
	existing, err := s.GetSensorByName(ctx, c.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		c.Managed = true
		return s.CreateSensor(ctx, c)
	case err != nil:
		return err
	}
	c.ID = existing.ID
	c.CreatedAt = existing.CreatedAt
	c.Managed = true
	c.SortOrder = existing.SortOrder
	// The flag goes in explicitly. The row was there before the file declared it —
	// made in the dashboard, or created by an earlier apply and then edited — and
	// every guard against changing a file-owned sensor reads the column, so a row
	// that stayed unmanaged would be editable out from under the file.
	return s.updateSensor(ctx, c, &c.Managed)
}

// SetSensorOrder rewrites sort_order to match the given id order.
func (s *Store) SetSensorOrder(ctx context.Context, ids []int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sensors SET sort_order = ? WHERE id = ?`, i, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// SensorChannelIDs returns the channel ids assigned to a sensor.
func (s *Store) SensorChannelIDs(ctx context.Context, sensorID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_id FROM sensor_channels WHERE sensor_id = ? ORDER BY channel_id`, sensorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func setSensorChannels(ctx context.Context, tx *sql.Tx, sensorID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sensor_channels WHERE sensor_id = ?`, sensorID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sensor_channels (sensor_id, channel_id) VALUES (?, ?)`, sensorID, id); err != nil {
			return wrapUnique(err, "channel assignment", "")
		}
	}
	return nil
}

// SensorEscalationChannelIDs returns the channels an escalation of this sensor
// goes to, in id order.
func (s *Store) SensorEscalationChannelIDs(ctx context.Context, sensorID int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT channel_id FROM sensor_escalation_channels WHERE sensor_id = ? ORDER BY channel_id`, sensorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func setSensorEscalationChannels(ctx context.Context, tx *sql.Tx, sensorID int64, ids []int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM sensor_escalation_channels WHERE sensor_id = ?`, sensorID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO sensor_escalation_channels (sensor_id, channel_id) VALUES (?, ?)`, sensorID, id); err != nil {
			return wrapUnique(err, "escalation channel assignment", "")
		}
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// wrapUnique turns a UNIQUE constraint failure into a friendlier message.
func wrapUnique(err error, kind, name string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "UNIQUE constraint failed") {
		if name == "" {
			return fmt.Errorf("%s references a row that does not exist", kind)
		}
		return fmt.Errorf("%s %q already exists", kind, name)
	}
	return err
}

// nullIfEmpty turns an empty string into SQL NULL.
//
// It is needed for exactly one column. sensors.site references sites(name) and is
// nullable on purpose: NULL is the ordinary state "this sensor has no site", while
// an empty string would be a foreign key pointing at a site named "" that cannot
// exist — so every sensor saved without a site would be refused.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// wrapSensorErr turns the two ways a sensor write can be rejected on integrity
// into messages that say which field is at fault.
//
// The foreign key on sensors can only be the site — it is the only one that table
// has — so a foreign key failure here always means the same thing, and it is worth
// saying plainly rather than passing SQLite's sentence through. The API turns
// these into a 400, which is what a mistyped site name is: a bad request, not a
// server that fell over.
func wrapSensorErr(err error, c *model.Sensor) error {
	if err == nil {
		return nil
	}
	if c.Site != "" && strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		return fmt.Errorf("site %q does not exist", c.Site)
	}
	return wrapUnique(err, "sensor", c.Name)
}
