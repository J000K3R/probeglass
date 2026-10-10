package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

const incidentColumns = `id, sensor_id, started_at, resolved_at, cause,
	acked_at, acked_by, ack_note`

func scanIncident(sc rowScanner) (model.Incident, error) {
	var (
		i          model.Incident
		startedAt  string
		resolvedAt sql.NullString
		ackedAt    sql.NullString
	)
	if err := sc.Scan(&i.ID, &i.SensorID, &startedAt, &resolvedAt, &i.Cause,
		&ackedAt, &i.AckedBy, &i.AckNote); err != nil {
		return i, err
	}
	i.StartedAt = parseTime(startedAt)
	i.ResolvedAt = parseTimePtr(resolvedAt)
	i.AckedAt = parseTimePtr(ackedAt)
	return i, nil
}

// OpenIncident records the start of a new outage.
func (s *Store) OpenIncident(ctx context.Context, sensorID int64, startedAt time.Time, cause string) (int64, error) {
	return openIncident(ctx, s.db, sensorID, startedAt, cause)
}

func openIncident(ctx context.Context, ex execer, sensorID int64, startedAt time.Time, cause string) (int64, error) {
	res, err := ex.ExecContext(ctx,
		`INSERT INTO incidents (sensor_id, started_at, cause) VALUES (?,?,?)`,
		sensorID, fmtTime(startedAt), cause)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CloseIncident resolves an outage.
func (s *Store) CloseIncident(ctx context.Context, incidentID int64, resolvedAt time.Time) error {
	return closeIncident(ctx, s.db, incidentID, resolvedAt)
}

func closeIncident(ctx context.Context, ex execer, incidentID int64, resolvedAt time.Time) error {
	_, err := ex.ExecContext(ctx,
		`UPDATE incidents SET resolved_at = ? WHERE id = ? AND resolved_at IS NULL`,
		fmtTime(resolvedAt), incidentID)
	return err
}

// CloseOpenIncidents resolves every still-open incident of a sensor. It is used
// to repair state after a crash and when a sensor recovers without us having
// tracked an incident id.
func (s *Store) CloseOpenIncidents(ctx context.Context, sensorID int64, resolvedAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET resolved_at = ? WHERE sensor_id = ? AND resolved_at IS NULL`,
		fmtTime(resolvedAt), sensorID)
	return err
}

// OpenIncidentFor returns the running incident of a sensor, or nil.
func (s *Store) OpenIncidentFor(ctx context.Context, sensorID int64) (*model.Incident, error) {
	i, err := scanIncident(s.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM incidents
		 WHERE sensor_id = ? AND resolved_at IS NULL ORDER BY started_at DESC LIMIT 1`, sensorID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// LastIncident returns the most recent incident of a sensor, open or closed.
func (s *Store) LastIncident(ctx context.Context, sensorID int64) (*model.Incident, error) {
	i, err := scanIncident(s.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM incidents
		 WHERE sensor_id = ? ORDER BY started_at DESC LIMIT 1`, sensorID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// IncidentFilter narrows an incident query.
type IncidentFilter struct {
	// SensorID limits the result to one sensor. 0 means all sensors.
	SensorID int64
	// Since limits the result to incidents that started at or after this time.
	Since time.Time
	// OnlyOpen returns nothing but ongoing outages.
	OnlyOpen bool
	// OnlyResolved returns nothing but outages that have already ended. It is the
	// other half of OnlyOpen, and the dashboard's filter line has a button for each
	// of the three readings; the two cannot both be set, and setting neither means
	// both are returned.
	OnlyResolved bool
	// Query keeps only the outages whose text mentions a string: what the check
	// said, the note somebody left, and — where the sensor is joined in — the
	// sensor's own name and target. It is matched literally and case-insensitively,
	// the same way the search box matches, so that a "%" somebody typed is a
	// percent sign rather than a wildcard. See internal/store/search.go for why
	// `instr` and not `LIKE`.
	Query string
	Limit int
}

// applyIncidentFilter appends the WHERE clauses an IncidentFilter asks for and
// returns the statement and its arguments.
//
// Both list functions build their filter the same way, and this is that way: one
// place to add the next condition, so the two cannot drift into answering
// differently from one another. `alias` is the prefix the caller's table carries,
// empty for the unjoined query.
func applyIncidentFilter(q string, args []any, f IncidentFilter, alias string) (string, []any) {
	if f.SensorID > 0 {
		q += ` AND ` + alias + `sensor_id = ?`
		args = append(args, f.SensorID)
	}
	if !f.Since.IsZero() {
		q += ` AND ` + alias + `started_at >= ?`
		args = append(args, fmtTime(f.Since))
	}
	if f.OnlyOpen {
		q += ` AND ` + alias + `resolved_at IS NULL`
	}
	if f.OnlyResolved {
		q += ` AND ` + alias + `resolved_at IS NOT NULL`
	}
	if text := strings.TrimSpace(f.Query); text != "" {
		q += ` AND (instr(lower(` + alias + `cause), lower(?)) > 0` +
			` OR instr(lower(` + alias + `ack_note), lower(?)) > 0`
		args = append(args, text, text)
		if alias != "" {
			// The joined query can also look at what the sensor is called, which is
			// how somebody remembers an outage: "the time the NAS went".
			q += ` OR instr(lower(c.name), lower(?)) > 0` +
				` OR instr(lower(c.target), lower(?)) > 0`
			args = append(args, text, text)
		}
		q += `)`
	}
	return q, args
}

// ListIncidents returns incidents newest first.
func (s *Store) ListIncidents(ctx context.Context, f IncidentFilter) ([]model.Incident, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 50
	}
	q := `SELECT ` + incidentColumns + ` FROM incidents WHERE 1=1`
	args := []any{}
	q, args = applyIncidentFilter(q, args, f, "")
	q += ` ORDER BY started_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Incident{}
	for rows.Next() {
		i, err := scanIncident(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// ListIncidentsWithNames returns incidents together with the sensor they belong
// to, which is what the global incident log on the dashboard needs.
func (s *Store) ListIncidentsWithNames(ctx context.Context, f IncidentFilter) ([]IncidentRow, error) {
	if f.Limit <= 0 || f.Limit > 1000 {
		f.Limit = 50
	}
	q := `SELECT i.id, i.sensor_id, i.started_at, i.resolved_at, i.cause,
	             i.acked_at, i.acked_by, i.ack_note,
	             COALESCE(c.name,''), COALESCE(c.type,''), COALESCE(c.target,'')
	      FROM incidents i LEFT JOIN sensors c ON c.id = i.sensor_id WHERE 1=1`
	args := []any{}
	q, args = applyIncidentFilter(q, args, f, "i.")
	q += ` ORDER BY i.started_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []IncidentRow{}
	for rows.Next() {
		var (
			r          IncidentRow
			startedAt  string
			resolvedAt sql.NullString
			ackedAt    sql.NullString
		)
		if err := rows.Scan(&r.ID, &r.SensorID, &startedAt, &resolvedAt, &r.Cause,
			&ackedAt, &r.AckedBy, &r.AckNote,
			&r.SensorName, &r.SensorType, &r.SensorTarget); err != nil {
			return nil, err
		}
		r.StartedAt = parseTime(startedAt)
		r.ResolvedAt = parseTimePtr(resolvedAt)
		r.AckedAt = parseTimePtr(ackedAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// IncidentRow is an incident joined with its sensor's display fields.
type IncidentRow struct {
	model.Incident
	SensorName   string `json:"sensor_name"`
	SensorType   string `json:"sensor_type"`
	SensorTarget string `json:"sensor_target"`
}

// CountOpenIncidents reports how many outages are currently running.
func (s *Store) CountOpenIncidents(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM incidents WHERE resolved_at IS NULL`).Scan(&n)
	return n, err
}

// AckIncident acknowledges an ongoing outage: the sensor is still probed, but
// repeat alerts stop until it recovers.
//
// Only an unresolved outage can be acknowledged, and only once — acknowledging
// twice is a no-op that keeps the first note and the first name, because the
// point of the record is that somebody took responsibility for this outage when
// it started, not who clicked last.
//
// A resolved outage is refused rather than silently accepted: "acknowledge what
// is down" has no meaning once nothing is.
func (s *Store) AckIncident(ctx context.Context, incidentID int64, by, note string, at time.Time) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET acked_at = ?, acked_by = ?, ack_note = ?
		 WHERE id = ? AND resolved_at IS NULL AND acked_at IS NULL`,
		fmtTime(at), by, truncate(note, 500), incidentID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// Nothing matched. Say which of the two reasons it was.
		var resolved sql.NullString
		var acked sql.NullString
		err := s.db.QueryRowContext(ctx,
			`SELECT resolved_at, acked_at FROM incidents WHERE id = ?`,
			incidentID).Scan(&resolved, &acked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if resolved.Valid {
			return fmt.Errorf("incident %d is already resolved", incidentID)
		}
		return fmt.Errorf("incident %d is already acknowledged", incidentID)
	}
	return nil
}

// UnackIncident takes an acknowledgement back, so reminders resume immediately.
// The note is cleared with it: half an acknowledgement left behind would show up
// in the UI as a name next to no timestamp.
func (s *Store) UnackIncident(ctx context.Context, incidentID int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE incidents SET acked_at = NULL, acked_by = '', ack_note = ''
		 WHERE id = ? AND acked_at IS NOT NULL`, incidentID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		if _, err := s.IncidentByID(ctx, incidentID); err != nil {
			return err
		}
		return fmt.Errorf("incident %d is not acknowledged", incidentID)
	}
	return nil
}

// IncidentByID returns one incident, open or closed.
func (s *Store) IncidentByID(ctx context.Context, incidentID int64) (*model.Incident, error) {
	i, err := scanIncident(s.db.QueryRowContext(ctx,
		`SELECT `+incidentColumns+` FROM incidents WHERE id = ?`, incidentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// truncate caps a free-text field at n bytes. The note is a human's one-liner
// about an outage, not a document, and an unbounded field is a way to make the
// incident list unreadable.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// IncidentSummary aggregates downtime for one sensor since a point in time.
type IncidentSummary struct {
	Count      int     `json:"count"`
	DownMillis int64   `json:"down_ms"`
	DownPct    float64 `json:"down_pct"`
}

// SummariseIncidents reports outage count and total downtime since a point in
// time. The percentage is relative to the window that was asked for.
func (s *Store) SummariseIncidents(ctx context.Context, sensorID int64, since, until time.Time) (IncidentSummary, error) {
	rows, err := s.ListIncidents(ctx, IncidentFilter{SensorID: sensorID, Since: since, Limit: 1000})
	if err != nil {
		return IncidentSummary{}, err
	}
	var sum IncidentSummary
	for _, i := range rows {
		start := i.StartedAt
		end := until
		if i.ResolvedAt != nil {
			end = *i.ResolvedAt
		}
		if start.Before(since) {
			start = since
		}
		if end.After(until) {
			end = until
		}
		if d := end.Sub(start); d > 0 {
			sum.Count++
			sum.DownMillis += d.Milliseconds()
		}
	}
	if window := until.Sub(since); window > 0 {
		sum.DownPct = float64(sum.DownMillis) / float64(window.Milliseconds()) * 100
	}
	return sum, nil
}
