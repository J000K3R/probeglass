package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

// Icons somebody uploaded, held as bytes in this database rather than as files
// beside it.
//
// The bytes are the point. "One SQLite file, and copying it is the whole backup" is
// a promise this project makes in three places, and an icon kept on disk would
// quietly make it false — the copy would restore without them. They are small:
// internal/icon caps one at 64 KiB, and a hundred of them are six megabytes.
//
// The drawings that ship inside the binary are not here at all. They are files
// under internal/web/assets/icons and are chosen by name, which is why a reference
// carries a prefix: `builtin:router` is a name this program knows and
// `upload:7` is a row in this installation's database, and a bare `router` that
// could be either would be ambiguous the first time somebody uploaded an icon and
// called it that.

const iconColumns = `id, name, media_type, width, height, length(bytes), created_at`

func scanIcon(sc rowScanner) (model.Icon, error) {
	var (
		i         model.Icon
		createdAt string
	)
	if err := sc.Scan(&i.ID, &i.Name, &i.MediaType, &i.Width, &i.Height, &i.Size, &createdAt); err != nil {
		return i, err
	}
	i.CreatedAt = parseTime(createdAt)
	return i, nil
}

// ListIcons returns every uploaded icon, newest first, each with the number of
// things that point at it.
//
// Newest first because the question the list answers is "which one do I want"
// right after somebody uploaded one, and a set of icons is otherwise an alphabet
// nobody named. The counts come from one query each rather than one per icon.
func (s *Store) ListIcons(ctx context.Context) ([]model.Icon, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+iconColumns+` FROM icons ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []model.Icon{}
	for rows.Next() {
		i, err := scanIcon(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	uses, err := s.iconUses(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Uses = uses[out[i].ID].Total()
	}
	return out, nil
}

// GetIcon returns one uploaded icon including its bytes.
func (s *Store) GetIcon(ctx context.Context, id int64) (model.Icon, []byte, error) {
	var (
		i         model.Icon
		createdAt string
		bytes     []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, media_type, width, height, bytes, created_at FROM icons WHERE id = ?`,
		id).Scan(&i.ID, &i.Name, &i.MediaType, &i.Width, &i.Height, &bytes, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return i, nil, ErrNotFound
	}
	if err != nil {
		return i, nil, err
	}
	i.CreatedAt = parseTime(createdAt)
	i.Size = len(bytes)
	return i, bytes, nil
}

// CreateIcon stores an uploaded image.
//
// The bytes are read here and not by the caller: internal/icon is what decides
// what an image may be, and an upload that skipped it would be a file stored
// unread and then drawn as a broken image somewhere nobody can explain. What comes
// back in i is what the database recorded — the media type and the dimensions come
// from the decode, never from the request, because a filename and a Content-Type
// header are both chosen by whoever is uploading.
func (s *Store) CreateIcon(ctx context.Context, i *model.Icon, data []byte) error {
	info, err := icon.Decode(data)
	if err != nil {
		return err
	}
	i.Name = strings.TrimSpace(i.Name)
	if len(i.Name) > 64 {
		return errors.New("an icon name may be at most 64 characters")
	}
	i.MediaType, i.Width, i.Height = info.MediaType, info.Width, info.Height
	i.Size = len(data)
	i.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO icons (name, media_type, width, height, bytes, created_at)
		VALUES (?,?,?,?,?,?)`,
		i.Name, i.MediaType, i.Width, i.Height, data, fmtTime(i.CreatedAt))
	if err != nil {
		return err
	}
	i.ID, err = res.LastInsertId()
	return err
}

// IconInUseError says an uploaded icon is still the symbol of something.
//
// The API turns it into a 409 and repeats the counts. Deleting an icon is a
// reasonable thing to want — a picture somebody uploaded twice, or one they have
// gone off — and "four sensors and one site still wear it" is the answer that lets
// them decide whether to take it off those first or to accept that they will draw
// nothing. Nothing breaks either way: the reference is a string and not a foreign
// key, so an icon that is gone simply draws nothing.
type IconInUseError struct {
	ID                     int64
	Sensors, Sites, Groups int
}

func (e *IconInUseError) Error() string {
	return fmt.Sprintf("icon %d is still used by %d sensor(s), %d site(s) and %d group(s)",
		e.ID, e.Sensors, e.Sites, e.Groups)
}

// Total is how many things point at the icon altogether.
func (e IconInUseError) Total() int { return e.Sensors + e.Sites + e.Groups }

// DeleteIcon removes an uploaded image.
//
// It refuses while something still wears it, and the refusal carries the counts.
// The alternative — deleting it and letting the sensors that pointed at it quietly
// draw nothing — would be a change nobody asked for on rows they were not editing.
// clear is the way to say they meant it: the rows are emptied and the image goes,
// all inside one transaction, so an interrupted delete cannot leave an image taken
// off some of its users and not others.
func (s *Store) DeleteIcon(ctx context.Context, id int64, clear bool) error {
	if _, _, err := s.GetIcon(ctx, id); err != nil {
		return err
	}
	ref := icon.UploadRef(id)
	if !clear {
		uses, err := s.iconUses(ctx)
		if err != nil {
			return err
		}
		if u, ok := uses[id]; ok && u.Total() > 0 {
			inUse := u
			inUse.ID = id
			return &inUse
		}
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if clear {
			for _, table := range []string{"sensors", "sites", "groups"} {
				if _, err := tx.ExecContext(ctx,
					`UPDATE `+table+` SET icon = '' WHERE icon = ?`, ref); err != nil {
					return err
				}
			}
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM icons WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// iconUses counts, for every uploaded icon, what points at it.
//
// One query per table and not one per icon: the picker lists every uploaded icon
// on every open of the sensor form, and a query each would be a round of them for
// a number that is usually zero.
//
// The reference is `upload:7`, so the id is what follows the prefix — read with
// SUBSTR rather than by matching each id in turn, so that one pass over a table
// answers for all of them. A row whose icon column holds something that is not an
// upload reference at all is not counted here; nothing may write one, and the
// count exists to warn before a delete rather than to inventory a hand-edited file.
func (s *Store) iconUses(ctx context.Context) (map[int64]IconInUseError, error) {
	out := map[int64]IconInUseError{}
	for _, t := range []struct {
		table string
		where func(*IconInUseError) *int
	}{
		{"sensors", func(u *IconInUseError) *int { return &u.Sensors }},
		{"sites", func(u *IconInUseError) *int { return &u.Sites }},
		{"groups", func(u *IconInUseError) *int { return &u.Groups }},
	} {
		counts, err := s.iconUsesIn(ctx, t.table)
		if err != nil {
			return nil, err
		}
		for id, n := range counts {
			u := out[id]
			u.ID = id
			*t.where(&u) = n
			out[id] = u
		}
	}
	return out, nil
}

// iconUsesIn is the one-table half of the count. The table name is a constant of
// the caller's list and never anything a request can reach.
func (s *Store) iconUsesIn(ctx context.Context, table string) (map[int64]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT CAST(SUBSTR(icon, ?) AS INTEGER), COUNT(*) FROM `+table+`
		 WHERE icon LIKE ? GROUP BY icon`,
		len(icon.PrefixUpload)+1, icon.PrefixUpload+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// ValidateIconRef refuses a reference that points at nothing.
//
// The shape is internal/icon's question and it answers it first. What is left is
// this installation's own: an `upload:` id that is not in this database — a
// reference copied out of somebody else's backup, or an image that was deleted —
// would draw nothing and say nothing, which is why the check happens where the
// value is written rather than where it is drawn. A built-in name needs no such
// second look: it is a file in the binary, and every installation has it.
func (s *Store) ValidateIconRef(ctx context.Context, ref string) error {
	if err := icon.Check(ref); err != nil {
		return err
	}
	id := icon.UploadID(ref)
	if id == 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM icons WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("there is no uploaded icon %s", ref)
	}
	return nil
}
