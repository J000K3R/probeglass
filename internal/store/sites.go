package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/j000k3r/weatherglass/internal/model"
)

// Sites are the places sensors live: the homelab, or a server somebody rents.
//
// A site is optional in the strong sense. A sensor without one behaves exactly as
// it did before sites existed, and a site without coordinates is a perfectly good
// grouping — it simply has nothing to draw on the map, which is why the two
// coordinate columns are nullable and 0 is left to mean 0.

const siteColumns = `name, kind, lat, lon, sort_order, managed, origin, created_at, updated_at`

func scanSite(sc rowScanner) (model.Site, error) {
	var (
		s         model.Site
		kind      string
		lat, lon  sql.NullFloat64
		managed   int
		createdAt string
		updatedAt string
	)
	if err := sc.Scan(&s.Name, &kind, &lat, &lon, &s.SortOrder, &managed, &s.Origin,
		&createdAt, &updatedAt); err != nil {
		return s, err
	}
	s.Kind = model.SiteKind(kind)
	// NULL is "not placed". It is deliberately not folded into 0: 0/0 is a real
	// coordinate in the Gulf of Guinea, and a site there is a site.
	if lat.Valid {
		s.Lat = &lat.Float64
	}
	if lon.Valid {
		s.Lon = &lon.Float64
	}
	s.Managed = managed != 0
	s.CreatedAt = parseTime(createdAt)
	s.UpdatedAt = parseTime(updatedAt)
	return s, nil
}

// NormaliseSite fills in what can be filled in without an opinion: the name is
// trimmed and a missing kind becomes the homelab, which is what most sites are.
func NormaliseSite(s *model.Site) {
	s.Name = strings.TrimSpace(s.Name)
	if s.Kind == "" {
		s.Kind = model.SiteLocal
	}
}

// ValidateSite rejects what would store a site that cannot be used.
//
// Coordinates out of range are refused rather than clamped. Clamping would move
// the pin to a place nobody asked for and say nothing about it; a latitude of 91
// is a typo, and the honest answer to a typo is to say so.
func ValidateSite(s model.Site) error {
	if err := ValidateSiteName(s.Name); err != nil {
		return err
	}
	if !s.Kind.Valid() {
		return fmt.Errorf("unknown site kind %q", string(s.Kind))
	}
	if s.Lat != nil && (*s.Lat < -90 || *s.Lat > 90) {
		return fmt.Errorf("latitude %g is outside -90..90", *s.Lat)
	}
	if s.Lon != nil && (*s.Lon < -180 || *s.Lon > 180) {
		return fmt.Errorf("longitude %g is outside -180..180", *s.Lon)
	}
	if (s.Lat == nil) != (s.Lon == nil) {
		return errors.New("a site needs both coordinates or neither")
	}
	return nil
}

// ValidateSiteName rejects names that cannot be used as they stand.
//
// The name is three things at once: the identity of the site, the value every
// sensor stores, and a path segment in the API. A slash would therefore produce a
// URL that can never be called, and a control character would produce one that
// cannot be typed — both are worth refusing at the door rather than discovering
// later, when the site is already in use and cannot be renamed out of the way
// without moving its sensors.
func ValidateSiteName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("a site needs a name")
	}
	if len(name) > 64 {
		return errors.New("a site name may be at most 64 characters")
	}
	for _, r := range name {
		switch {
		case r == '/' || r == '\\':
			return fmt.Errorf("a site name may not contain %q", string(r))
		case unicode.IsControl(r):
			return errors.New("a site name may not contain control characters")
		}
	}
	return nil
}

// SiteInUseError says a site still has sensors on it.
//
// The API turns it into a 409 and repeats the count. "Delete this site" is a
// reasonable thing to want; "four sensors are still there" is the answer that
// lets somebody decide whether to move them or to let go of them.
type SiteInUseError struct {
	Name  string
	Count int
}

func (e *SiteInUseError) Error() string {
	return fmt.Sprintf("site %q still has %d sensor(s)", e.Name, e.Count)
}

// ListSites returns every site in display order.
func (s *Store) ListSites(ctx context.Context) ([]model.Site, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+siteColumns+` FROM sites ORDER BY sort_order ASC, name COLLATE NOCASE ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Site{}
	for rows.Next() {
		site, err := scanSite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, site)
	}
	return out, rows.Err()
}

// GetSite loads one site by name.
func (s *Store) GetSite(ctx context.Context, name string) (model.Site, error) {
	site, err := scanSite(s.db.QueryRowContext(ctx,
		`SELECT `+siteColumns+` FROM sites WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return site, ErrNotFound
	}
	return site, err
}

// CreateSite inserts a site.
func (s *Store) CreateSite(ctx context.Context, site *model.Site) error {
	NormaliseSite(site)
	if err := ValidateSite(*site); err != nil {
		return err
	}
	now := time.Now().UTC()
	site.CreatedAt, site.UpdatedAt = now, now
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sites (name, kind, lat, lon, sort_order, managed, origin, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		site.Name, string(site.Kind), floatOrNull(site.Lat), floatOrNull(site.Lon),
		site.SortOrder, boolInt(site.Managed), site.Origin, fmtTime(now), fmtTime(now))
	if err != nil {
		return wrapUnique(err, "site", site.Name)
	}
	return nil
}

// UpdateSite writes a site, possibly under a new name.
//
// The name is the identity, so a rename is an UPDATE of the primary key and the
// sensors follow on their own: the foreign key is declared ON UPDATE CASCADE,
// which is the whole reason for pointing at the name instead of at a row id. No
// sensor is touched here, and none can be missed.
//
// A site the configuration file owns is refused: nothing about it is this
// function's to change. Only the apply path may write such a row, and it comes in
// through updateSite below.
func (s *Store) UpdateSite(ctx context.Context, oldName string, site *model.Site) error {
	return s.updateSite(ctx, oldName, site, nil)
}

// updateSite is UpdateSite with the two things the apply path needs and an
// ordinary edit must not have: the ability to write the managed flag, and with it
// the ability to write a row the file owns at all.
//
// `managed` is passed as a pointer because nil and false mean different things
// here. Nil means the caller is an ordinary edit: the column is left as it is, so
// a save can neither take a site over for the configuration file nor set one free
// from it — and a row the file already owns is refused outright rather than
// changed and quietly put back by the next apply. A value is only handed in by
// UpsertManagedSite, and then the flag lands in the same statement as the rest of
// the row.
func (s *Store) updateSite(ctx context.Context, oldName string, site *model.Site, managed *bool) error {
	NormaliseSite(site)
	if err := ValidateSite(*site); err != nil {
		return err
	}
	existing, err := s.GetSite(ctx, oldName)
	if err != nil {
		return err
	}
	// A site the configuration file owns is not edited from here at all — not
	// renamed, not given new coordinates, not changed in kind. The dashboard
	// already draws it with its buttons disabled and the documentation says the
	// file owns the name, the kind and the coordinates; refusing only the rename
	// would leave the API allowing what both of those say it will not, and the
	// edit would look as though it had worked until the next apply put it back.
	//
	// The apply path is the exception, and `managed` is how it says so: it is the
	// one caller that hands a value in, because writing a row the file owns is
	// exactly its job.
	if existing.Managed && managed == nil {
		return fmt.Errorf("site %q is declared in the configuration file and "+
			"cannot be changed here", oldName)
	}
	flag := existing.Managed
	if managed != nil {
		flag = *managed
	}
	now := time.Now().UTC()
	// origin is carried over from the existing row rather than taken from the
	// argument, exactly like the managed flag above and for the same reason: who
	// created a site is not something an edit gets to change. A caller holding a
	// Site it built itself would otherwise clear the marker on every save, and the
	// site would quietly stop being example data.
	res, err := s.db.ExecContext(ctx, `
		UPDATE sites SET name=?, kind=?, lat=?, lon=?, sort_order=?, managed=?, origin=?, updated_at=?
		WHERE name = ?`,
		site.Name, string(site.Kind), floatOrNull(site.Lat), floatOrNull(site.Lon),
		site.SortOrder, boolInt(flag), existing.Origin, fmtTime(now), oldName)
	if err != nil {
		return wrapUnique(err, "site", site.Name)
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	site.CreatedAt = existing.CreatedAt
	site.UpdatedAt = now
	// The row says what the column says, and so does the struct that was handed
	// in — a caller that goes on to answer with it (the API does) would otherwise
	// report the flag it guessed rather than the one that was just written.
	site.Managed = flag
	site.Origin = existing.Origin
	return nil
}

// DeleteSite removes a site.
//
// A site that still has sensors is refused unless unassign is set, in which case
// the sensors are let go of in the same transaction — they keep working, they
// simply stop having a place. Both halves are one statement each inside one
// transaction so that a failure cannot leave sensors pointing at a site that is
// already gone.
func (s *Store) DeleteSite(ctx context.Context, name string, unassign bool) error {
	site, err := s.GetSite(ctx, name)
	if err != nil {
		return err
	}
	if site.Managed {
		return fmt.Errorf("site %q is declared in the configuration file and "+
			"cannot be deleted here", name)
	}
	if !unassign {
		used, err := s.CountSensorsAtSite(ctx, name)
		if err != nil {
			return err
		}
		if used > 0 {
			return &SiteInUseError{Name: name, Count: used}
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if unassign {
			if _, err := tx.ExecContext(ctx,
				`UPDATE sensors SET site = NULL WHERE site = ?`, name); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM sites WHERE name = ?`, name)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// CountSensorsAtSite reports how many sensors are placed at one site.
func (s *Store) CountSensorsAtSite(ctx context.Context, name string) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sensors WHERE site = ?`, name).Scan(&n)
	return n, err
}

// SensorCountBySite counts the sensors at every site in one query, for the
// headings that say how much is behind a place.
func (s *Store) SensorCountBySite(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT site, COUNT(*) FROM sensors WHERE site IS NOT NULL GROUP BY site`)
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

// EnsureSite creates a site if it is not there yet and does nothing if it is.
//
// A configuration file names the site its sensors belong to, but it cannot know
// whether somebody already made that site by hand — or gave it coordinates. So the
// file may only guarantee that the row exists, never overwrite what is in it.
func (s *Store) EnsureSite(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	if err := ValidateSiteName(name); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sites (name, kind, lat, lon, sort_order, managed, created_at, updated_at)
		VALUES (?,?,NULL,NULL,0,0,?,?)
		ON CONFLICT(name) DO NOTHING`,
		name, string(model.SiteLocal), fmtTime(now), fmtTime(now))
	return err
}

// UpsertManagedSite creates or updates a site declared in the configuration file.
// The name is the identity; everything else comes from the spec.
func (s *Store) UpsertManagedSite(ctx context.Context, site *model.Site) error {
	NormaliseSite(site)
	if err := ValidateSite(*site); err != nil {
		return err
	}
	existing, err := s.GetSite(ctx, site.Name)
	switch {
	case errors.Is(err, ErrNotFound):
		site.Managed = true
		return s.CreateSite(ctx, site)
	case err != nil:
		return err
	}
	// The row is already there, which is the ordinary case for a site that was
	// made in the dashboard first or merely named by a sensor, and the file only
	// now has an opinion about it. Taking it over means the flag has to be
	// written: it is what refuses every edit and the delete afterwards, and a site
	// that stays unmanaged would be renamed out from under the file by the next
	// person in the dashboard, leaving the file pointing at a name that is gone.
	site.Managed = true
	return s.updateSite(ctx, existing.Name, site, &site.Managed)
}

// floatOrNull turns a nil coordinate into SQL NULL and a real one into its value.
func floatOrNull(f *float64) any {
	if f == nil {
		return nil
	}
	return *f
}
