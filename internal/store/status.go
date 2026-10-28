package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/j000k3r/probeglass/internal/model"
)

// StatusSensors returns the sensors the public status page publishes, in the
// order of their ids. The page draws them by name, so the order here only has to
// be stable.
func (s *Store) StatusSensors(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT sensor_id FROM status_sensors ORDER BY sensor_id`)
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

// SetStatusSensors replaces the whole published set in one transaction.
//
// Replace rather than add and remove per checkbox: the panel saves the list it
// is showing, and a request that arrives with a sensor missing is a sensor the
// operator unticked. Doing it as one statement means the page never shows a
// half-applied selection while a request is in flight.
func (s *Store) SetStatusSensors(ctx context.Context, ids []int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM status_sensors`); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO status_sensors (sensor_id) VALUES (?)`, id); err != nil {
				// A foreign-key failure here means an id that is not a sensor.
				// It is the caller's to report as a bad request; the message
				// names the id so the dashboard log is useful.
				return fmt.Errorf("publish sensor %d: %w", id, err)
			}
		}
		return nil
	})
}

// StatusPageSettings returns the two installation-wide facts the page needs:
// the share token (empty when the page is off) and whether causes are shown.
func (s *Store) StatusPageSettings(ctx context.Context) (token string, showCauses bool) {
	token = s.Setting(ctx, model.SettingStatusToken, "")
	showCauses = strings.TrimSpace(s.Setting(ctx, model.SettingStatusCauses, "")) == "1"
	return token, showCauses
}
