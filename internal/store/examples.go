package store

import (
	"context"
	"database/sql"

	"github.com/j000k3r/probeglass/internal/model"
)

// ExampleData is what a bulk removal of the invented sensors would take with it.
type ExampleData struct {
	Sensors   int `json:"sensors"`
	Results   int `json:"results"`
	Incidents int `json:"incidents"`
	// Sites is how many invented places went with them. It is usually smaller than
	// Sensors and often zero, because a place the operator has meanwhile put a
	// sensor of their own at stays.
	Sites int `json:"sites"`
}

// CountExampleData reports how much example data there is.
//
// It is a read, so the dashboard can decide whether to offer the removal button at
// all, and say how many sensors would go. A button that is always there and
// usually does nothing is a button people stop reading.
func (s *Store) CountExampleData(ctx context.Context) (ExampleData, error) {
	var out ExampleData
	for _, q := range []struct {
		dst *int
		sql string
	}{
		{&out.Sensors, `SELECT COUNT(*) FROM sensors WHERE origin = ?`},
		{&out.Results, `SELECT COUNT(*) FROM results WHERE sensor_id IN
			(SELECT id FROM sensors WHERE origin = ?)`},
		{&out.Incidents, `SELECT COUNT(*) FROM incidents WHERE sensor_id IN
			(SELECT id FROM sensors WHERE origin = ?)`},
		{&out.Sites, `SELECT COUNT(*) FROM sites WHERE origin = ? AND NOT EXISTS
			(SELECT 1 FROM sensors WHERE sensors.site = sites.name AND sensors.origin <> ?)`},
	} {
		if err := s.db.QueryRowContext(ctx, q.sql,
			model.OriginExample, model.OriginExample).Scan(q.dst); err != nil {
			return ExampleData{}, err
		}
	}
	return out, nil
}

// DeleteExampleData removes every sensor the setup or `seed --demo` invented, and
// everything that belongs to it. The returned counts are what was there before, so
// the caller can say how much went.
//
// The predicate is the origin marker, never the name or the group. Both of those
// are things the operator can edit — somebody who renames "Public DNS (example)"
// to "Public DNS" has not thereby asked for it to be kept forever, and somebody who
// puts their own router in a group called "Examples" has not asked for it to be
// deleted.
//
// Places go too, on the same marker, but only the ones this leaves empty. An
// example site the operator has since put a sensor of their own at is a place
// somebody is using, and deleting their sensor's site would be the removal
// reaching beyond what it was asked to remove. The site count is therefore taken
// with the same "no sensor that stays behind" test before anything is deleted,
// which is also what CountExampleData reports, so the number on the button and the
// number in the answer agree.
func (s *Store) DeleteExampleData(ctx context.Context) (ExampleData, error) {
	var removed ExampleData
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM sensors WHERE origin = ?),
			(SELECT COUNT(*) FROM results WHERE sensor_id IN
				(SELECT id FROM sensors WHERE origin = ?)),
			(SELECT COUNT(*) FROM incidents WHERE sensor_id IN
				(SELECT id FROM sensors WHERE origin = ?)),
			(SELECT COUNT(*) FROM sites WHERE origin = ? AND NOT EXISTS
				(SELECT 1 FROM sensors WHERE sensors.site = sites.name AND sensors.origin <> ?))`,
			model.OriginExample, model.OriginExample, model.OriginExample,
			model.OriginExample, model.OriginExample)
		if err := row.Scan(&removed.Sensors, &removed.Results, &removed.Incidents,
			&removed.Sites); err != nil {
			return err
		}
		if removed.Sensors == 0 && removed.Sites == 0 {
			return nil
		}

		// The rollups have no foreign key onto sensors, so unlike results and
		// incidents they do not follow by cascade.
		for _, table := range []string{"results_hourly", "results_daily"} {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM `+table+` WHERE sensor_id IN
					(SELECT id FROM sensors WHERE origin = ?)`,
				model.OriginExample); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sensors WHERE origin = ?`, model.OriginExample); err != nil {
			return err
		}
		// After the sensors, so the NOT EXISTS above is evaluated against the state
		// it was counted in and the two cannot disagree.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM sites WHERE origin = ? AND NOT EXISTS
				(SELECT 1 FROM sensors WHERE sensors.site = sites.name)`,
			model.OriginExample); err != nil {
			return err
		}
		// And the windows the dataset arranged. A window that names a sensor has
		// already gone with it through the foreign key; one that names a group has
		// nothing to follow, which is what the marker is for.
		_, err := tx.ExecContext(ctx,
			`DELETE FROM maintenance_windows WHERE origin = ?`, model.OriginExample)
		return err
	})
	return removed, err
}

// CreateMissingSensors inserts the sensors whose name is not already taken, in a
// single transaction, and returns the ones it created.
//
// The check and the writes share one transaction so that two racing callers — two
// clicks on the same button, or two administrators at once — cannot both find a
// name free and write it twice. A name that is already taken is skipped rather
// than being an error: sensors.name is unique, and somebody who happens to have a
// sensor of their own called "Example website" must not make the whole request
// fail.
//
// It is the write half of putting the example data back; DeleteExampleData is the
// other direction, and both key on the origin marker rather than on a name.
func (s *Store) CreateMissingSensors(ctx context.Context, in []model.Sensor) ([]model.Sensor, error) {
	created := make([]model.Sensor, 0, len(in))
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for i := range in {
			c := in[i]
			var taken int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM sensors WHERE name = ?`, c.Name).Scan(&taken); err != nil {
				return err
			}
			if taken > 0 {
				continue
			}
			if err := insertSensor(ctx, tx, &c); err != nil {
				return err
			}
			created = append(created, c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}
