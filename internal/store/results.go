package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/j000k3r/weatherglass/internal/model"
)

// UptimeStats summarises a window for one or more sensors.
type UptimeStats struct {
	Total   int     `json:"total"`   // probes observed
	OK      int     `json:"ok"`      // successful probes
	Down    int     `json:"down"`    // failed probes
	Percent float64 `json:"percent"` // OK / Total * 100, 100 when Total is 0
	Latency float64 `json:"latency_ms"`
}

// granularity selects which table answers a query.
type granularity int

const (
	granRaw granularity = iota
	granHourly
	granDaily
)

// SetRetention configures the raw and hourly retention windows. It must be
// called before queries run so that the resolution picked for a window matches
// the data that is actually still stored.
func (s *Store) SetRetention(raw, hourly time.Duration) {
	if raw > 0 {
		s.rawRetention = raw
	}
	if hourly > 0 {
		s.hourlyRetention = hourly
	}
}

// Retention reports the configured retention windows.
func (s *Store) Retention() (raw, hourly time.Duration) {
	return s.rawRetention, s.hourlyRetention
}

func (s *Store) granularityFor(window time.Duration) granularity {
	switch {
	case window <= s.rawRetention:
		return granRaw
	case window <= s.hourlyRetention:
		return granHourly
	default:
		return granDaily
	}
}

// table returns the source table and its timestamp column for a granularity.
func (g granularity) table() (table, timeCol string) {
	switch g {
	case granHourly:
		return "results_hourly", "bucket"
	case granDaily:
		return "results_daily", "bucket"
	default:
		return "results", "ts"
	}
}

// InsertResult stores one probe outcome.
func (s *Store) InsertResult(ctx context.Context, r *model.Result) error {
	return insertResult(ctx, s.db, r)
}

// insertResult is the shared implementation behind InsertResult and Tx.
func insertResult(ctx context.Context, ex execer, r *model.Result) error {
	res, err := ex.ExecContext(ctx, `
		INSERT INTO results (sensor_id, ts, ok, latency_ms, status_code, message)
		VALUES (?,?,?,?,?,?)`,
		r.SensorID, fmtTime(r.TS), boolInt(r.Success), r.LatencyMS, r.StatusCode, r.Message)
	if err != nil {
		return err
	}
	r.ID, _ = res.LastInsertId()
	return nil
}

const resultColumns = `id, sensor_id, ts, ok, latency_ms, status_code, message`

func scanResult(sc rowScanner) (model.Result, error) {
	var (
		r    model.Result
		ts   string
		ok   int
		code int
	)
	if err := sc.Scan(&r.ID, &r.SensorID, &ts, &ok, &r.LatencyMS, &code, &r.Message); err != nil {
		return r, err
	}
	r.TS = parseTime(ts)
	r.Success = ok != 0
	r.StatusCode = code
	return r, nil
}

// RecentResults returns the newest results for a sensor, oldest first.
func (s *Store) RecentResults(ctx context.Context, sensorID int64, limit int) ([]model.Result, error) {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+resultColumns+` FROM results WHERE sensor_id = ?
		 ORDER BY ts DESC LIMIT ?`, sensorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse into chronological order for direct charting.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// LastResult returns the most recent result for a sensor, or nil when none
// exists. Used at startup to resume the previous status without re-alerting.
func (s *Store) LastResult(ctx context.Context, sensorID int64) (*model.Result, error) {
	r, err := scanResult(s.db.QueryRowContext(ctx,
		`SELECT `+resultColumns+` FROM results WHERE sensor_id = ?
		 ORDER BY ts DESC LIMIT 1`, sensorID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// LastResults returns the newest N results per sensor in one query, for the
// dashboard sparklines. The inner query is windowed per sensor so the result
// stays small no matter how many probes are stored.
func (s *Store) LastResults(ctx context.Context, perSensor int) (map[int64][]model.Result, error) {
	if perSensor <= 0 || perSensor > 500 {
		perSensor = 40
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+resultColumns+` FROM (
			SELECT `+resultColumns+`,
			       ROW_NUMBER() OVER (PARTITION BY sensor_id ORDER BY ts DESC) AS rn
			FROM results
		) WHERE rn <= ? ORDER BY sensor_id, ts ASC`, perSensor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64][]model.Result{}
	for rows.Next() {
		r, err := scanResult(rows)
		if err != nil {
			return nil, err
		}
		out[r.SensorID] = append(out[r.SensorID], r)
	}
	return out, rows.Err()
}

// TrailingFailures reports how many probes failed in the unbroken run at the
// end of a sensor's history, when that run started, and when the sensor last
// succeeded. The scheduler uses it to rebuild its state after a restart, so an
// outage that began while Weatherglass was down is neither lost nor invented.
func (s *Store) TrailingFailures(ctx context.Context, sensorID int64, limit int) (count int, firstFailure, lastSuccess time.Time, err error) {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT ts, ok FROM results WHERE sensor_id = ? ORDER BY ts DESC LIMIT ?`, sensorID, limit)
	if err != nil {
		return 0, time.Time{}, time.Time{}, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			ts string
			ok int
		)
		if err := rows.Scan(&ts, &ok); err != nil {
			return 0, time.Time{}, time.Time{}, err
		}
		if ok != 0 {
			if lastSuccess.IsZero() {
				lastSuccess = parseTime(ts)
			}
			break
		}
		count++
		firstFailure = parseTime(ts)
	}
	return count, firstFailure, lastSuccess, rows.Err()
}

// ---------------------------------------------------------------------------
// aggregation
// ---------------------------------------------------------------------------

func buildStats(total, ok int, latencySum float64) UptimeStats {
	st := UptimeStats{Total: total, OK: ok, Down: total - ok}
	if total > 0 {
		st.Percent = float64(ok) / float64(total) * 100
	} else {
		st.Percent = 100
	}
	if ok > 0 {
		st.Latency = latencySum / float64(ok)
	}
	return st
}

// Uptime aggregates one sensor over [from, to).
func (s *Store) Uptime(ctx context.Context, sensorID int64, from, to time.Time) (UptimeStats, error) {
	g := s.granularityFor(to.Sub(from))
	table, timeCol := g.table()
	q := `SELECT COUNT(*), COALESCE(SUM(ok),0), COALESCE(SUM(latency_ms),0)
	      FROM ` + table + ` WHERE sensor_id = ? AND ` + timeCol + ` >= ? AND ` + timeCol + ` < ?`
	if g != granRaw {
		q = `SELECT COALESCE(SUM(samples),0), COALESCE(SUM(ok),0), COALESCE(SUM(latency_sum),0)
		     FROM ` + table + ` WHERE sensor_id = ? AND ` + timeCol + ` >= ? AND ` + timeCol + ` < ?`
	}
	var total, ok int
	var latSum float64
	if err := s.db.QueryRowContext(ctx, q, sensorID, fmtTime(from), fmtTime(to)).
		Scan(&total, &ok, &latSum); err != nil {
		return UptimeStats{}, err
	}
	return buildStats(total, ok, latSum), nil
}

// UptimeBySensor aggregates every sensor over [from, to) in a single query.
func (s *Store) UptimeBySensor(ctx context.Context, from, to time.Time) (map[int64]UptimeStats, error) {
	table, timeCol := s.granularityFor(to.Sub(from)).table()
	sel := `COUNT(*), COALESCE(SUM(ok),0), COALESCE(SUM(latency_ms),0)`
	if s.granularityFor(to.Sub(from)) != granRaw {
		sel = `COALESCE(SUM(samples),0), COALESCE(SUM(ok),0), COALESCE(SUM(latency_sum),0)`
	}
	q := `SELECT sensor_id, ` + sel + ` FROM ` + table + `
	      WHERE ` + timeCol + ` >= ? GROUP BY sensor_id`

	rows, err := s.db.QueryContext(ctx, q, fmtTime(from))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]UptimeStats{}
	for rows.Next() {
		var (
			id     int64
			total  int
			ok     int
			latSum float64
		)
		if err := rows.Scan(&id, &total, &ok, &latSum); err != nil {
			return nil, err
		}
		out[id] = buildStats(total, ok, latSum)
	}
	return out, rows.Err()
}

// SummarySeries is the whole installation bucketed by the hour over [from, to),
// oldest first, for the small curves in the dashboard header.
//
// It reads the hourly rollup rather than the raw table, which is a deliberate
// exception to granularityFor: a day is wanted here as about twenty-four points,
// and the rollup is already that shape. Asking the raw table for a day and
// folding it in Go would do a thousand times the work for the same picture — and
// the picture would grow with how often the probes run.
//
// The consequence, which is worth knowing: the newest hour is missing until the
// rollup has run for it. The curve therefore ends at the last completed hour,
// while the figure beside it counts right up to now. A curve is a shape, not a
// reading; the number is the reading.
func (s *Store) SummarySeries(ctx context.Context, from, to time.Time) ([]model.SummaryPoint, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket, COALESCE(SUM(samples),0), COALESCE(SUM(ok),0), COALESCE(SUM(latency_sum),0)
		FROM results_hourly
		WHERE bucket >= ? AND bucket < ? GROUP BY bucket ORDER BY bucket`,
		fmtTime(from), fmtTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.SummaryPoint{}
	for rows.Next() {
		var (
			bucket string
			total  int
			ok     int
			latSum float64
		)
		if err := rows.Scan(&bucket, &total, &ok, &latSum); err != nil {
			return nil, err
		}
		point := model.SummaryPoint{TS: parseBucket(bucket), Probes: total}
		if total > 0 {
			point.Uptime = float64(ok) / float64(total) * 100
		}
		if ok > 0 {
			point.Latency = latSum / float64(ok)
		}
		out = append(out, point)
	}
	return out, rows.Err()
}

// Samples returns the time buckets that answer a window, using exactly one
// resolution so that no probe is counted twice.
func (s *Store) Samples(ctx context.Context, sensorID int64, from, to time.Time) ([]model.Sample, error) {
	switch s.granularityFor(to.Sub(from)) {
	case granRaw:
		return s.rawSamples(ctx, sensorID, from, to)
	case granHourly:
		return s.bucketSamples(ctx, "results_hourly", sensorID, from, to)
	default:
		return s.bucketSamples(ctx, "results_daily", sensorID, from, to)
	}
}

func (s *Store) rawSamples(ctx context.Context, sensorID int64, from, to time.Time) ([]model.Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, ok, latency_ms FROM results
		WHERE sensor_id = ? AND ts >= ? AND ts < ? ORDER BY ts`, sensorID, fmtTime(from), fmtTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Sample{}
	for rows.Next() {
		var (
			ts  string
			ok  int
			lat float64
		)
		if err := rows.Scan(&ts, &ok, &lat); err != nil {
			return nil, err
		}
		out = append(out, model.Sample{
			TS:       parseTime(ts),
			OK:       ok,
			Total:    1,
			LatencyS: lat * float64(ok),
			LatMin:   lat,
			LatMax:   lat,
		})
	}
	return out, rows.Err()
}

func (s *Store) bucketSamples(ctx context.Context, table string, sensorID int64, from, to time.Time) ([]model.Sample, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT bucket, samples, ok, latency_sum, latency_min, latency_max FROM `+table+`
		WHERE sensor_id = ? AND bucket >= ? AND bucket < ? ORDER BY bucket`,
		sensorID, fmtTime(from), fmtTime(to))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Sample{}
	for rows.Next() {
		var (
			bucket string
			sm     model.Sample
		)
		if err := rows.Scan(&bucket, &sm.Total, &sm.OK, &sm.LatencyS, &sm.LatMin, &sm.LatMax); err != nil {
			return nil, err
		}
		sm.TS = parseBucket(bucket)
		out = append(out, sm)
	}
	return out, rows.Err()
}

// parseBucket accepts both the hourly and the daily key layout.
func parseBucket(s string) time.Time {
	if t, err := time.Parse(timeFormat, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse(dayFormat, s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

// Series buckets a window into exactly n slots. Slots without data come back
// with Total == 0 so the UI can render an explicit gap instead of a fake zero.
func (s *Store) Series(ctx context.Context, sensorID int64, from, to time.Time, n int) ([]model.SeriesPoint, error) {
	if n < 1 {
		n = 1
	}
	if n > 2000 {
		n = 2000
	}
	span := to.Sub(from)
	if span <= 0 {
		return nil, fmt.Errorf("invalid window: %s", span)
	}

	samples, err := s.Samples(ctx, sensorID, from, to)
	if err != nil {
		return nil, err
	}

	type acc struct {
		ok, total int
		latSum    float64
		min, max  float64
		hasData   bool
	}
	slots := make([]acc, n)
	width := span / time.Duration(n)

	for _, sm := range samples {
		idx := int(sm.TS.Sub(from) / width)
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		a := &slots[idx]
		a.hasData = true
		a.ok += sm.OK
		a.total += sm.Total
		a.latSum += sm.LatencyS
		if sm.LatMin > 0 && (a.min == 0 || sm.LatMin < a.min) {
			a.min = sm.LatMin
		}
		if sm.LatMax > a.max {
			a.max = sm.LatMax
		}
	}

	out := make([]model.SeriesPoint, n)
	for i, a := range slots {
		p := model.SeriesPoint{TS: from.Add(time.Duration(i) * width), Total: a.total, OK: a.ok}
		if a.hasData {
			p.AvgMS = a.latSum / float64(maxInt(a.total, 1))
			p.MinMS = a.min
			p.MaxMS = a.max
			if a.total > 0 {
				p.UptimePC = float64(a.ok) / float64(a.total) * 100
			} else {
				p.UptimePC = 100
			}
		}
		out[i] = p
	}
	return out, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---------------------------------------------------------------------------
// maintenance: rollups and retention
// ---------------------------------------------------------------------------

// Maintain folds raw results into the hourly and daily rollups and then drops
// data that is past its retention. It is idempotent: rollup writes replace the
// bucket contents instead of adding to them, so re-running it is harmless.
func (s *Store) Maintain(ctx context.Context, now time.Time) error {
	hourCutoff := now.UTC().Truncate(time.Hour)
	dayCutoff := now.UTC().Truncate(24 * time.Hour)

	if err := s.rollup(ctx, "results_hourly", "substr(ts,1,13) || ':00:00.000000000Z'", hourCutoff); err != nil {
		return fmt.Errorf("hourly rollup: %w", err)
	}
	if err := s.rollup(ctx, "results_daily", "substr(ts,1,10)", dayCutoff); err != nil {
		return fmt.Errorf("daily rollup: %w", err)
	}

	rawCutoff := fmtTime(now.Add(-s.rawRetention))
	hourlyCutoff := fmtTime(now.Add(-s.hourlyRetention))

	if _, err := s.db.ExecContext(ctx, `DELETE FROM results WHERE ts < ?`, rawCutoff); err != nil {
		return fmt.Errorf("prune raw results: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM results_hourly WHERE bucket < ?`, hourlyCutoff); err != nil {
		return fmt.Errorf("prune hourly rollups: %w", err)
	}
	// Daily rollups are kept forever: a row per sensor per day is negligible and
	// it is what makes multi-year uptime reporting possible.

	//nolint:misspell // "optimize" is the pragma's spelling in SQLite, not ours
	if _, err := s.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		s.log.Debug("pragma optimize failed", "error", err) //nolint:misspell // as above
	}
	return nil
}

func (s *Store) rollup(ctx context.Context, table, bucketExpr string, cutoff time.Time) error {
	// The bucket expression builds a fixed-width key from the ISO timestamp:
	// substr('2026-10-08T19:50:01.000000000Z',1,13) => '2026-10-08T19'.
	// Only rows from *complete* buckets pass the cutoff, and the upsert replaces
	// the aggregate rather than accumulating, which keeps this idempotent.
	q := `
	INSERT INTO ` + table + ` (sensor_id, bucket, samples, ok, latency_sum, latency_min, latency_max)
	SELECT sensor_id, ` + bucketExpr + `, COUNT(*), COALESCE(SUM(ok),0),
	       COALESCE(SUM(latency_ms),0), COALESCE(MIN(latency_ms),0), COALESCE(MAX(latency_ms),0)
	FROM results WHERE ts < ?
	GROUP BY sensor_id, ` + bucketExpr + `
	ON CONFLICT(sensor_id, bucket) DO UPDATE SET
	    samples     = excluded.samples,
	    ok          = excluded.ok,
	    latency_sum = excluded.latency_sum,
	    latency_min = excluded.latency_min,
	    latency_max = excluded.latency_max`
	_, err := s.db.ExecContext(ctx, q, fmtTime(cutoff))
	return err
}

// ProbeCount reports how many raw results are stored, for the settings page.
func (s *Store) ProbeCount(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM results`).Scan(&n)
	return n, err
}

// DataSpan reports the oldest and newest raw result timestamps.
func (s *Store) DataSpan(ctx context.Context) (oldest, newest time.Time, err error) {
	var o, n sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT MIN(ts), MAX(ts) FROM results`).Scan(&o, &n)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if o.Valid {
		oldest = parseTime(o.String)
	}
	if n.Valid {
		newest = parseTime(n.String)
	}
	return oldest, newest, nil
}
