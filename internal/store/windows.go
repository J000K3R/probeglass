package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// Maintenance windows: the periods during which a sensor is expected to be down.
//
// Two shapes share one table, and the columns of the shape that is not in use are
// simply empty. They are kept apart rather than folded into one representation —
// "a time of day in a zone, on chosen weekdays" and "two instants" have nothing in
// common but the word window, and a row that had to be decoded before a reader
// could tell which of the two it was would be a row nobody reads correctly.
//
// A window names one sensor or one group, never both and never neither. A group is
// a string on the sensors rather than a table of its own, so the group column is a
// name and not a foreign key: a window for a group that no sensor carries matches
// nothing, which is what a typo should do.

const windowColumns = `id, sensor_id, grp, kind, starts_at, ends_at, weekdays,
	start_min, end_min, tz, note, enabled, managed, origin, created_at, updated_at`

func scanWindow(sc rowScanner) (model.MaintenanceWindow, error) {
	var (
		w                    model.MaintenanceWindow
		sensorID             sql.NullInt64
		kind                 string
		startsAt, endsAt     sql.NullString
		weekdays             string
		enabled, managed     int
		createdAt, updatedAt string
	)
	if err := sc.Scan(&w.ID, &sensorID, &w.Group, &kind, &startsAt, &endsAt, &weekdays,
		&w.StartMin, &w.EndMin, &w.Timezone, &w.Note, &enabled, &managed, &w.Origin,
		&createdAt, &updatedAt); err != nil {
		return w, err
	}
	// A NULL sensor_id is a group window, and 0 is how the model spells it. The
	// two are the same answer to "which sensor", so they are folded together here
	// rather than tracked separately everywhere else.
	if sensorID.Valid {
		w.SensorID = sensorID.Int64
	}
	w.Kind = model.WindowKind(kind)
	w.StartsAt = parseTimePtr(startsAt)
	w.EndsAt = parseTimePtr(endsAt)
	w.Weekdays = parseWeekdays(weekdays)
	w.Enabled = enabled != 0
	w.Managed = managed != 0
	w.CreatedAt = parseTime(createdAt)
	w.UpdatedAt = parseTime(updatedAt)
	return w, nil
}

// Weekdays are stored as a short list of digits — "0,3,6" — rather than as seven
// boolean columns or a bitmask. Seven columns would be seven chances to disagree
// with each other and a WHERE clause nobody wants to write; a bitmask would be a
// number that has to be decoded to be believed. `sort.Ints` on the way in means the
// stored string is the same for the same set however the form listed it, which is
// what makes a duplicate detectable by comparing strings.
func parseWeekdays(s string) []int {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 0 || n > 6 {
			// An unreadable value is dropped rather than guessed at. A window
			// with no days left covers nothing, which is the safe direction: the
			// other one is a window that covers every day.
			continue
		}
		out = append(out, n)
	}
	sort.Ints(out)
	return slices.Compact(out)
}

func formatWeekdays(days []int) string {
	if len(days) == 0 {
		return ""
	}
	clean := slices.Clone(days)
	sort.Ints(clean)
	clean = slices.Compact(clean)
	parts := make([]string, 0, len(clean))
	for _, d := range clean {
		parts = append(parts, strconv.Itoa(d))
	}
	return strings.Join(parts, ",")
}

// NormaliseWindow fills in what can be filled in without an opinion: the note and
// the group are trimmed, the weekdays are put in order, and a window with no zone
// is read as UTC rather than refused.
//
// UTC is the right default and not a shrug. Everything else in this program stamps
// its times in UTC, and a zone nobody chose is a guess about where somebody lives —
// a guess that is wrong twice a year for most of Europe. UTC at least says "nobody
// said", and the dashboard fills the browser's zone in when a window is created.
func NormaliseWindow(w *model.MaintenanceWindow) {
	w.Group = strings.TrimSpace(w.Group)
	w.Note = strings.TrimSpace(w.Note)
	w.Timezone = strings.TrimSpace(w.Timezone)
	if w.Timezone == "" {
		w.Timezone = "UTC"
	}
	if len(w.Weekdays) > 0 {
		clean := slices.Clone(w.Weekdays)
		sort.Ints(clean)
		w.Weekdays = slices.Compact(clean)
	}
	// An empty note is not an error and should not be a NULL either: the column is
	// NOT NULL DEFAULT '', and a caller that hands over a struct built by hand
	// would otherwise write the zero value into a NOT NULL column and fail.
	if w.Kind == "" {
		w.Kind = model.WindowOnce
	}
}

// ValidateWindow rejects what would store a window that cannot be used.
//
// The asymmetry to notice is that a window covering nothing is allowed while a
// window covering everything is not. A group name no sensor carries, or a weekly
// window whose days are all out of range, silences nothing — annoying, visible the
// moment somebody looks at the list, and harmless. A window that spans all of time
// is an installation that has quietly stopped alerting, and that is the one failure
// this feature can cause that nobody would notice until they needed it.
func ValidateWindow(w model.MaintenanceWindow) error {
	if !w.Kind.Valid() {
		return fmt.Errorf("unknown window kind %q", string(w.Kind))
	}
	if (w.SensorID == 0) == (w.Group == "") {
		return errors.New("a window is for one sensor or for one group, not both and not neither")
	}
	if len(w.Group) > 64 {
		return errors.New("a group name may be at most 64 characters")
	}
	if len(w.Note) > 200 {
		return errors.New("a note may be at most 200 characters")
	}

	switch w.Kind {
	case model.WindowOnce:
		if w.StartsAt == nil || w.EndsAt == nil {
			return errors.New("a one-off window needs a beginning and an end")
		}
		if !w.EndsAt.After(*w.StartsAt) {
			return errors.New("a one-off window has to end after it begins")
		}
	case model.WindowWeekly:
		if len(w.Weekdays) == 0 {
			return errors.New("a weekly window needs at least one day")
		}
		for _, d := range w.Weekdays {
			if d < 0 || d > 6 {
				return fmt.Errorf("%d is not a weekday; 0 is Sunday and 6 is Saturday", d)
			}
		}
		if w.StartMin < 0 || w.StartMin >= 1440 {
			return errors.New("a window has to start within the day it starts on")
		}
		if w.EndMin <= 0 || w.EndMin > 1440 {
			return errors.New("a window has to end within the day after the one it starts on")
		}
		if w.StartMin == w.EndMin {
			// All 24 hours of the named days. Refused rather than accepted,
			// because it is almost always a form that was filled in wrong, and
			// because "silence this sensor entirely" has a better answer already:
			// pause it, which says so in the dashboard.
			return errors.New("a window may not cover all 24 hours; pause the sensor instead")
		}
	}

	if _, err := time.LoadLocation(w.Timezone); err != nil {
		return fmt.Errorf("%q is not a time zone this build knows", w.Timezone)
	}
	return nil
}

// ListWindows returns every window, newest first within the sensors they name.
//
// Read whole rather than per sensor. The scheduler asks "is this sensor inside a
// window" once per probe and the answer has to be cheap; the whole list is a handful
// of rows for any real installation, and holding it in memory is what lets that
// question be answered without a query per check.
func (s *Store) ListWindows(ctx context.Context) ([]model.MaintenanceWindow, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+windowColumns+` FROM maintenance_windows ORDER BY kind, grp, sensor_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // a read-only cursor; the query error is what matters

	out := []model.MaintenanceWindow{}
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// GetWindow loads one window by id.
func (s *Store) GetWindow(ctx context.Context, id int64) (model.MaintenanceWindow, error) {
	w, err := scanWindow(s.db.QueryRowContext(ctx,
		`SELECT `+windowColumns+` FROM maintenance_windows WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	return w, err
}

// CreateWindow inserts a window.
//
// Saving a window does not reach back into the past. The maintenance marker is
// written onto a probe result by the scheduler at the moment the probe runs, and a
// window that is saved at noon does not change the numbers for the morning — only
// for everything from here on. Rewriting the rows already stored was the other way
// to build this and was left out on purpose: it would mean a bulk UPDATE over the
// raw retention on every save, and it would make the same period read two different
// ways depending on when somebody happened to declare it. A window is a statement
// about the future, and the honest thing is to let it be one.
func (s *Store) CreateWindow(ctx context.Context, w *model.MaintenanceWindow) error {
	NormaliseWindow(w)
	if err := ValidateWindow(*w); err != nil {
		return err
	}
	now := time.Now().UTC()
	w.CreatedAt, w.UpdatedAt = now, now
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO maintenance_windows (
			sensor_id, grp, kind, starts_at, ends_at, weekdays, start_min, end_min,
			tz, note, enabled, managed, origin, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		nullInt(w.SensorID), w.Group, string(w.Kind), fmtTimePtr(w.StartsAt), fmtTimePtr(w.EndsAt),
		formatWeekdays(w.Weekdays), w.StartMin, w.EndMin, w.Timezone, w.Note,
		boolInt(w.Enabled), boolInt(w.Managed), w.Origin, fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return err
	}
	w.ID = id
	return nil
}

// UpdateWindow writes a window, addressed by id.
//
// A window has no name, so unlike a site it has no identity to rename and nothing
// follows it: the id is the whole of what it is addressed by. That is also why
// nothing here has to worry about what happens to the sensors — a window is read by
// them, not owned by them.
func (s *Store) UpdateWindow(ctx context.Context, id int64, w *model.MaintenanceWindow) error {
	return s.updateWindow(ctx, id, w, nil)
}

// updateWindow is UpdateWindow with the one thing the apply path needs and an
// ordinary edit must not have: the ability to write a row the configuration file
// owns. See updateSite for why the flag is a pointer.
func (s *Store) updateWindow(ctx context.Context, id int64, w *model.MaintenanceWindow, managed *bool) error {
	NormaliseWindow(w)
	if err := ValidateWindow(*w); err != nil {
		return err
	}
	existing, err := s.GetWindow(ctx, id)
	if err != nil {
		return err
	}
	if existing.Managed && managed == nil {
		return fmt.Errorf("maintenance window %d is declared in the configuration file "+
			"and cannot be changed here", id)
	}
	flag := existing.Managed
	if managed != nil {
		flag = *managed
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		UPDATE maintenance_windows SET
			sensor_id=?, grp=?, kind=?, starts_at=?, ends_at=?, weekdays=?,
			start_min=?, end_min=?, tz=?, note=?, enabled=?, managed=?, origin=?, updated_at=?
		WHERE id = ?`,
		nullInt(w.SensorID), w.Group, string(w.Kind), fmtTimePtr(w.StartsAt), fmtTimePtr(w.EndsAt),
		formatWeekdays(w.Weekdays), w.StartMin, w.EndMin, w.Timezone, w.Note,
		boolInt(w.Enabled), boolInt(flag), existing.Origin, fmtTime(now), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	w.ID = id
	w.CreatedAt = existing.CreatedAt
	w.UpdatedAt = now
	// As in updateSite: the struct that was handed in says what the row says, so a
	// caller that answers with it reports the flag that was written rather than the
	// one it guessed.
	w.Managed = flag
	w.Origin = existing.Origin
	return nil
}

// DeleteWindow removes a window.
//
// No cascade to think about and no refusal to make: a window owns nothing and
// nothing owns it, and the sensors it named carry on exactly as they did.
func (s *Store) DeleteWindow(ctx context.Context, id int64) error {
	w, err := s.GetWindow(ctx, id)
	if err != nil {
		return err
	}
	if w.Managed {
		return fmt.Errorf("maintenance window %d is declared in the configuration "+
			"file and cannot be deleted here", id)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM maintenance_windows WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ActiveWindows returns the windows in force for a sensor at an instant.
//
// The scheduler calls this once per probe and the store calls it once per sample,
// and both are asking the same question of the same row, so the answer is computed
// in one place — model.MaintenanceWindow.Covers — rather than twice.
func ActiveWindows(windows []model.MaintenanceWindow, c model.Sensor, at time.Time) bool {
	for _, w := range windows {
		if w.Covers(c, at) {
			return true
		}
	}
	return false
}

// nullInt writes 0 as SQL NULL, so that "no sensor" is a NULL and not a row id that
// no sensor has — the difference matters for the foreign key, which would refuse a
// zero that pointed at nothing.
func nullInt(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// fmtTimePtr renders a nil time as SQL NULL and a real one in the fixed-width UTC
// form every other timestamp in this database uses.
func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}
