package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/j000k3r/probeglass/internal/icon"
	"github.com/j000k3r/probeglass/internal/model"
)

// StatusBackground returns what the database knows about the page's photograph,
// or false when there is none. It reads no bytes: the panel needs the dimensions
// and the size for its line, and the page needs the digest for its URL.
func (s *Store) StatusBackground(ctx context.Context) (model.StatusBackground, bool, error) {
	var (
		bg        model.StatusBackground
		createdAt string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT media_type, width, height, length(bytes), digest, created_at
		FROM status_background WHERE id = 1`).Scan(
		&bg.MediaType, &bg.Width, &bg.Height, &bg.Size, &bg.Digest, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.StatusBackground{}, false, nil
	}
	if err != nil {
		return model.StatusBackground{}, false, err
	}
	bg.CreatedAt = parseTime(createdAt)
	return bg, true, nil
}

// StatusBackgroundBytes returns the image itself. It is the one place that reads
// a photograph out of the database, and it is called once per public page view at
// most — the browser caches the bytes for a day behind a versioned URL.
func (s *Store) StatusBackgroundBytes(ctx context.Context) (model.StatusBackground, []byte, bool, error) {
	var (
		bg        model.StatusBackground
		data      []byte
		createdAt string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT media_type, width, height, digest, bytes, created_at
		FROM status_background WHERE id = 1`).Scan(
		&bg.MediaType, &bg.Width, &bg.Height, &bg.Digest, &data, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.StatusBackground{}, nil, false, nil
	}
	if err != nil {
		return model.StatusBackground{}, nil, false, err
	}
	bg.Size = len(data)
	bg.CreatedAt = parseTime(createdAt)
	return bg, data, true, nil
}

// SetStatusBackground stores one image, replacing whatever was there. The bytes
// are validated with the larger background limits and stored unchanged: the
// picture is shown at its own quality, not shrunk to a symbol.
func (s *Store) SetStatusBackground(ctx context.Context, bg *model.StatusBackground, data []byte) error {
	info, err := icon.DecodeWithin(data, icon.BackgroundLimits)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	*bg = model.StatusBackground{
		MediaType: info.MediaType,
		Width:     info.Width,
		Height:    info.Height,
		Size:      len(data),
		Digest:    backgroundDigest(data),
		CreatedAt: now,
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO status_background (id, media_type, width, height, digest, bytes, created_at)
		VALUES (1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			media_type = excluded.media_type,
			width      = excluded.width,
			height     = excluded.height,
			digest     = excluded.digest,
			bytes      = excluded.bytes,
			created_at = excluded.created_at`,
		bg.MediaType, bg.Width, bg.Height, bg.Digest, data, fmtTime(now))
	return err
}

// DeleteStatusBackground removes the photograph. ErrNotFound says there was none,
// which lets the handler answer 404 to a stale button rather than pretend.
func (s *Store) DeleteStatusBackground(ctx context.Context) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM status_background WHERE id = 1`)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// backgroundDigest is the SHA-256 of the stored bytes, lowercase hex.
func backgroundDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
