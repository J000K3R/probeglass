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

// Tags are the settings of a tag, not the tags themselves.
//
// A tag still exists because somebody typed it into a sensor's tag list: there is no
// row anywhere that has to be created first, no foreign key from `sensors.tags`, and
// nothing to keep in step when a sensor is saved or deleted. What is here is what a
// word cannot say on its own — an icon, a colour, a place in the order — and a tag
// nobody has said anything about has no row here at all.
//
// This is deliberately the same shape as groups. The two are separate tables because
// they are separate words on a sensor and separate endpoints, but the editor is one
// dialog for both and the reasoning is the same one.

const tagColumns = `name, icon, colour, sort_order, created_at, updated_at`

func scanTag(sc rowScanner) (model.Tag, error) {
	var (
		t         model.Tag
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&t.Name, &t.Icon, &t.Colour, &t.SortOrder, &createdAt, &updatedAt); err != nil {
		return t, err
	}
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	return t, nil
}

// ListTags returns every tag this installation knows about, in display order.
//
// Two sources, merged: the rows here, which carry what somebody has said about a
// tag, and the words in the sensors' tag lists, which are what makes a tag exist at
// all. A tag can therefore be in this list with no settings — the ordinary case —
// and a settings row whose last sensor was changed is still listed, because
// forgetting what somebody chose should be a decision and not a side effect.
func (s *Store) ListTags(ctx context.Context) ([]model.Tag, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+tagColumns+` FROM tags ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Tag{}
	at := map[string]int{}
	for rows.Next() {
		t, err := scanTag(rows)
		if err != nil {
			return nil, err
		}
		at[t.Name] = len(out)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	counts, err := s.SensorCountByTag(ctx)
	if err != nil {
		return nil, err
	}
	for name, n := range counts {
		if i, ok := at[name]; ok {
			out[i].SensorCount = n
			continue
		}
		out = append(out, model.Tag{Name: name, SensorCount: n})
	}
	sortTags(out)
	return out, nil
}

// GetTag loads one tag's settings. A tag with no settings is ErrNotFound, which the
// API reports as a tag that is known only by its word — the same state every tag
// starts in.
func (s *Store) GetTag(ctx context.Context, name string) (model.Tag, error) {
	t, err := scanTag(s.db.QueryRowContext(ctx,
		`SELECT `+tagColumns+` FROM tags WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	n, err := s.CountSensorsWithTag(ctx, name)
	if err != nil {
		return t, err
	}
	t.SensorCount = n
	return t, nil
}

// NormaliseTag fills in what can be filled in without an opinion.
func NormaliseTag(t *model.Tag) {
	t.Name = strings.TrimSpace(t.Name)
	t.Icon = strings.TrimSpace(t.Icon)
	t.Colour = strings.TrimSpace(t.Colour)
}

// ValidateTagName rejects names that cannot be used as they stand.
//
// A comma is refused for a reason a group name does not have: the tags live in one
// comma-separated column, so a tag containing a comma would be two tags the next
// time the sensor was read. A slash would produce an API path that can never be
// called, and a control character one that cannot be typed.
func ValidateTagName(name string) error {
	if name == "" {
		return errors.New("a tag needs a name")
	}
	if len(name) > 64 {
		return errors.New("a tag name may be at most 64 characters")
	}
	for _, r := range name {
		switch {
		case r == ',':
			return errors.New("a tag name may not contain a comma")
		case r == '/' || r == '\\':
			return fmt.Errorf("a tag name may not contain %q", string(r))
		case unicode.IsControl(r):
			return errors.New("a tag name may not contain control characters")
		}
	}
	return nil
}

// SetTag writes a tag's settings, creating the row if this is the first thing anybody
// has said about it. The upsert has the same shape and the same reason as SetGroup.
func (s *Store) SetTag(ctx context.Context, t *model.Tag) error {
	NormaliseTag(t)
	if err := ValidateTagName(t.Name); err != nil {
		return err
	}
	if t.Colour != "" && !model.ValidAccent(t.Colour) {
		return fmt.Errorf("%q is not a colour this dashboard knows; use one of %s or none",
			t.Colour, strings.Join(model.Accents, ", "))
	}
	if err := s.ValidateIconRef(ctx, t.Icon); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tags (name, icon, colour, sort_order, created_at, updated_at)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(name) DO UPDATE SET
			icon = excluded.icon,
			colour = excluded.colour,
			sort_order = excluded.sort_order,
			updated_at = excluded.updated_at`,
		t.Name, t.Icon, t.Colour, t.SortOrder, fmtTime(now), fmtTime(now))
	if err != nil {
		return err
	}
	stored, err := s.GetTag(ctx, t.Name)
	if err != nil {
		return err
	}
	t.CreatedAt, t.UpdatedAt, t.SensorCount = stored.CreatedAt, stored.UpdatedAt, stored.SensorCount
	return nil
}

// ForgetTag removes what was said about a tag and leaves the word on the sensors.
func (s *Store) ForgetTag(ctx context.Context, name string) (int, error) {
	if _, err := s.GetTag(ctx, name); err != nil && !errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM tags WHERE name = ?`, name); err != nil {
		return 0, err
	}
	return s.CountSensorsWithTag(ctx, name)
}

// CountSensorsWithTag reports how many sensors carry a tag.
func (s *Store) CountSensorsWithTag(ctx context.Context, name string) (int, error) {
	counts, err := s.SensorCountByTag(ctx)
	if err != nil {
		return 0, err
	}
	return counts[name], nil
}

// SensorCountByTag counts the sensors carrying every tag in one pass.
//
// The tags are a comma-separated column rather than a join table, so the count is
// done by reading the column and splitting it. A sensor that somehow names the same
// tag twice counts once, which is what JoinTags writes in the first place.
func (s *Store) SensorCountByTag(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT tags FROM sensors WHERE tags <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, tag := range model.SplitTags(raw) {
			if seen[tag] {
				continue
			}
			seen[tag] = true
			out[tag]++
		}
	}
	return out, rows.Err()
}

// sortTags puts tags in the order they are shown: the ones with a place of their own
// first, by that place, then everything else by name. The same rule as sortGroups.
func sortTags(tags []model.Tag) {
	sort.SliceStable(tags, func(i, j int) bool {
		a, b := tags[i], tags[j]
		if a.SortOrder != b.SortOrder {
			return a.SortOrder < b.SortOrder
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
}
