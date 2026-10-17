package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/j000k3r/probeglass/internal/model"
)

// Groups are the settings of a group, and not the groups themselves.
//
// A group still exists because somebody typed its name on a sensor: there is no
// row anywhere that has to be created first, no foreign key from `sensors.grp`,
// and nothing to keep in step when a sensor is renamed or deleted. What is here is
// what a word cannot say on its own — an icon, a colour, a place in the order — and
// a group nobody has said anything about has no row here at all, which is the
// ordinary state of a fresh installation.
//
// The alternative — a real table that sensors reference — would turn creating a
// group into a step of its own, and it would reach into the configuration file, the
// backup, the example data and every delete path. That is a different feature.

const groupColumns = `name, icon, colour, sort_order, created_at, updated_at`

func scanGroup(sc rowScanner) (model.Group, error) {
	var (
		g         model.Group
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&g.Name, &g.Icon, &g.Colour, &g.SortOrder, &createdAt, &updatedAt); err != nil {
		return g, err
	}
	g.CreatedAt = parseTime(createdAt)
	g.UpdatedAt = parseTime(updatedAt)
	return g, nil
}

// ListGroups returns every group this installation knows about, in display order.
//
// Two sources, merged: the rows here, which carry what somebody has said about a
// group, and the names on the sensors, which are what makes a group exist at all.
// A group can therefore be in this list with no settings — the ordinary case — and
// a settings row whose last sensor was moved away is still listed, because
// forgetting what somebody chose should be a decision and not a side effect of
// editing a sensor.
func (s *Store) ListGroups(ctx context.Context) ([]model.Group, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+groupColumns+` FROM groups ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Group{}
	at := map[string]int{}
	for rows.Next() {
		g, err := scanGroup(rows)
		if err != nil {
			return nil, err
		}
		at[g.Name] = len(out)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts, err := s.SensorCountByGroup(ctx)
	if err != nil {
		return nil, err
	}
	for name, n := range counts {
		if i, ok := at[name]; ok {
			out[i].SensorCount = n
			continue
		}
		out = append(out, model.Group{Name: name, SensorCount: n})
	}
	// Sorted once more, because the names that arrived from the sensors were
	// appended after the rows that were already in order. A group with settings of
	// its own sorts by that settings row's place in the order; one without sorts by
	// its name, beside the rest.
	sortGroups(out)
	return out, nil
}

// GetGroup loads one group's settings. A group with no settings is ErrNotFound,
// which the API reports as a group that is known only by name — the same state
// every group starts in.
func (s *Store) GetGroup(ctx context.Context, name string) (model.Group, error) {
	g, err := scanGroup(s.db.QueryRowContext(ctx,
		`SELECT `+groupColumns+` FROM groups WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrNotFound
	}
	if err != nil {
		return g, err
	}
	n, err := s.CountSensorsInGroup(ctx, name)
	if err != nil {
		return g, err
	}
	g.SensorCount = n
	return g, nil
}

// NormaliseGroup fills in what can be filled in without an opinion.
func NormaliseGroup(g *model.Group) {
	g.Name = strings.TrimSpace(g.Name)
	g.Icon = strings.TrimSpace(g.Icon)
	g.Colour = strings.TrimSpace(g.Colour)
}

// ValidateGroupName rejects names that cannot be used as they stand.
//
// The name is the identity of the group and a path segment in the API, exactly as
// a site's is, so a slash would produce a URL that can never be called and a
// control character one that cannot be typed. What is deliberately not checked is
// whether a sensor uses the name: a group's settings may be written before the
// first sensor carries the word, and that is a sensible order to do it in.
func ValidateGroupName(name string) error {
	if name == "" {
		return errors.New("a group needs a name")
	}
	if len(name) > 64 {
		return errors.New("a group name may be at most 64 characters")
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			return fmt.Errorf("a group name may not contain %q", string(r))
		case unicode.IsControl(r):
			return errors.New("a group name may not contain control characters")
		}
	}
	return nil
}

// SetGroup writes a group's settings, creating the row if this is the first thing
// anybody has said about it.
//
// An upsert rather than a create and an update, because the two are the same thing
// from the dashboard's side: a group exists from the moment it is typed, and the
// first save is not a different act from the second. The row's own timestamps are
// kept the way the other tables keep them — created_at is written once.
func (s *Store) SetGroup(ctx context.Context, g *model.Group) error {
	NormaliseGroup(g)
	if err := ValidateGroupName(g.Name); err != nil {
		return err
	}
	if g.Colour != "" && !model.ValidAccent(g.Colour) {
		return fmt.Errorf("%q is not a colour this dashboard knows; use one of %s or none",
			g.Colour, strings.Join(model.Accents, ", "))
	}
	if err := s.ValidateIconRef(ctx, g.Icon); err != nil {
		return err
	}
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO groups (name, icon, colour, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			icon = excluded.icon,
			colour = excluded.colour,
			sort_order = excluded.sort_order,
			updated_at = excluded.updated_at`,
		g.Name, g.Icon, g.Colour, g.SortOrder, fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	g.UpdatedAt = now
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		// The upsert always affects a row, so this cannot happen; it is here so
		// that a future change to the statement cannot silently do nothing.
		return ErrNotFound
	}
	// created_at is only known after the write: an insert just set it and an
	// update left the old one alone, and the caller gets the stored answer rather
	// than the one it guessed.
	stored, err := s.GetGroup(ctx, g.Name)
	if err != nil {
		return err
	}
	g.CreatedAt, g.UpdatedAt, g.SensorCount = stored.CreatedAt, stored.UpdatedAt, stored.SensorCount
	return nil
}

// ForgetGroup removes what was said about a group and leaves the group alone.
//
// The sensors keep the word, so the group is still there afterwards — it is a
// heading on the dashboard with no icon and no colour of its own, which is exactly
// what it was before anybody opened the editor. Deleting the sensors instead would
// be deleting what is being monitored, and nothing about a symbol should do that.
// The count comes back so that the answer can say how many sensors are still in it.
func (s *Store) ForgetGroup(ctx context.Context, name string) (int, error) {
	if _, err := s.GetGroup(ctx, name); err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM groups WHERE name = ?`, name); err != nil {
		return 0, err
	}
	return s.CountSensorsInGroup(ctx, name)
}

// CountSensorsInGroup reports how many sensors carry a group's name.
func (s *Store) CountSensorsInGroup(ctx context.Context, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sensors WHERE grp = ?`, name).Scan(&n)
	return n, err
}

// SensorCountByGroup counts the sensors in every group in one query, for the
// headings that say how much is behind a word.
func (s *Store) SensorCountByGroup(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT grp, COUNT(*) FROM sensors WHERE grp <> '' GROUP BY grp`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// sortGroups puts groups in the order they are shown: the ones with a place of
// their own first, by that place, then everything else by name.
//
// A group nobody has arranged keeps its alphabetical seat rather than being pushed
// to the end, which is what sorting by sort_order alone would do — every group
// starts at 0, and the order would otherwise be whichever the database happened to
// return. The sort is stable so that two groups with the same place stay in the
// order they were put in, which is alphabetical.
func sortGroups(groups []model.Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		a, b := groups[i], groups[j]
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
