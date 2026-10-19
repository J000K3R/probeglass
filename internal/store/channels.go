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

const channelColumns = `id, name, type, config, enabled, managed, created_at, updated_at`

func scanChannel(sc rowScanner) (model.Channel, error) {
	var (
		c         model.Channel
		cfg       string
		enabled   int
		managed   int
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&c.ID, &c.Name, &c.Type, &cfg, &enabled, &managed, &createdAt, &updatedAt); err != nil {
		return c, err
	}
	c.Enabled = enabled != 0
	c.Managed = managed != 0
	c.Config = map[string]string{}
	if cfg != "" {
		if err := json.Unmarshal([]byte(cfg), &c.Config); err != nil {
			return c, fmt.Errorf("channel %d: corrupt config JSON: %w", c.ID, err)
		}
	}
	c.CreatedAt = parseTime(createdAt)
	c.UpdatedAt = parseTime(updatedAt)
	return c, nil
}

// NormaliseChannel trims whitespace and drops empty config values.
func NormaliseChannel(c *model.Channel) {
	c.Name = strings.TrimSpace(c.Name)
	c.Type = strings.ToLower(strings.TrimSpace(c.Type))
	if c.Config == nil {
		c.Config = map[string]string{}
	}
	for k, v := range c.Config {
		c.Config[k] = strings.TrimSpace(v)
	}
}

// ListChannels returns every configured alert channel.
func (s *Store) ListChannels(ctx context.Context) ([]model.Channel, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels ORDER BY name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChannel loads one channel by id.
func (s *Store) GetChannel(ctx context.Context, id int64) (model.Channel, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// GetChannelByName loads one channel by its unique name.
func (s *Store) GetChannelByName(ctx context.Context, name string) (model.Channel, error) {
	c, err := scanChannel(s.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CreateChannel inserts a channel.
func (s *Store) CreateChannel(ctx context.Context, c *model.Channel) error {
	NormaliseChannel(c)
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return fmt.Errorf("encode channel config: %w", err)
	}
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO channels (name, type, config, enabled, managed, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?)`,
		c.Name, c.Type, string(cfg), boolInt(c.Enabled), boolInt(c.Managed),
		fmtTime(now), fmtTime(now))
	if err != nil {
		return wrapUnique(err, "channel", c.Name)
	}
	c.ID, _ = res.LastInsertId()
	return nil
}

// UpdateChannel writes an existing channel.
func (s *Store) UpdateChannel(ctx context.Context, c *model.Channel) error {
	NormaliseChannel(c)
	cfg, err := json.Marshal(c.Config)
	if err != nil {
		return fmt.Errorf("encode channel config: %w", err)
	}
	now := time.Now().UTC()
	c.UpdatedAt = now
	res, err := s.db.ExecContext(ctx, `
		UPDATE channels SET name=?, type=?, config=?, enabled=?, updated_at=? WHERE id=?`,
		c.Name, c.Type, string(cfg), boolInt(c.Enabled), fmtTime(now), c.ID)
	if err != nil {
		return wrapUnique(err, "channel", c.Name)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteChannel removes a channel and its sensor assignments.
func (s *Store) DeleteChannel(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM channels WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpsertManagedChannel creates or updates a channel declared in the config
// file. The name is the identity; everything else comes from the spec.
func (s *Store) UpsertManagedChannel(ctx context.Context, c *model.Channel) error {
	existing, err := s.GetChannelByName(ctx, c.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		c.Managed = true
		return s.CreateChannel(ctx, c)
	case err != nil:
		return err
	}
	c.ID = existing.ID
	c.CreatedAt = existing.CreatedAt
	c.Managed = true
	return s.UpdateChannel(ctx, c)
}

// ChannelsForSensor returns the enabled channels assigned to a sensor.
func (s *Store) ChannelsForSensor(ctx context.Context, sensorID int64) ([]model.Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prefixed(channelColumns, "ch")+`
		FROM channels ch
		JOIN sensor_channels cc ON cc.channel_id = ch.id
		WHERE cc.sensor_id = ? AND ch.enabled = 1
		ORDER BY ch.name COLLATE NOCASE ASC`, sensorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EscalationChannelsForSensor is ChannelsForSensor for the second alert list.
func (s *Store) EscalationChannelsForSensor(ctx context.Context, sensorID int64) ([]model.Channel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+prefixed(channelColumns, "ch")+`
		FROM channels ch
		JOIN sensor_escalation_channels ec ON ec.channel_id = ch.id
		WHERE ec.sensor_id = ? AND ch.enabled = 1
		ORDER BY ch.name COLLATE NOCASE ASC`, sensorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ChannelIDsByNames resolves config-file channel names to ids.
func (s *Store) ChannelIDsByNames(ctx context.Context, names []string) ([]int64, []string, error) {
	ids := []int64{}
	missing := []string{}
	for _, n := range names {
		c, err := s.GetChannelByName(ctx, strings.TrimSpace(n))
		if errors.Is(err, ErrNotFound) {
			missing = append(missing, n)
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, c.ID)
	}
	return ids, missing, nil
}

// CountSensorsUsingChannel reports how many sensors alert through a channel.
func (s *Store) CountSensorsUsingChannel(ctx context.Context, channelID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sensor_channels WHERE channel_id = ?`, channelID).Scan(&n)
	return n, err
}

// prefixed rewrites a bare column list into one qualified by a table alias.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
