package store

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/j000k3r/probeglass/internal/model"
)

// Errors the user administration surfaces to the caller.
var (
	// ErrSetupTaken is returned by CreateFirstUser when an account already
	// exists. It is what keeps the public setup route from being a way to add a
	// second administrator to a running installation.
	ErrSetupTaken = errors.New("setup has already been completed")
	// ErrLastAdmin is returned when an operation would leave the installation
	// without an enabled administrator, which is irreversible without editing the
	// database by hand.
	ErrLastAdmin = errors.New("the last active administrator cannot be removed")
	// ErrSelf is returned when somebody tries to disable or delete their own
	// account. It is refused even when other administrators remain, because the
	// alternative is a support request.
	ErrSelf = errors.New("you cannot disable or delete your own account")
	// ErrUserExists is returned when the username is already taken. The column is
	// UNIQUE COLLATE NOCASE, so "Admin" collides with "admin".
	ErrUserExists = errors.New("that username is already taken")
)

const userColumns = `id, username, password_hash, role, totp_secret, totp_enabled,
	totp_step, token_epoch, disabled, prefs, created_at, updated_at, last_login_at`

func scanUser(sc rowScanner) (model.User, error) {
	var (
		u         model.User
		role      string
		enabled   int
		disabled  int
		prefs     string
		createdAt string
		updatedAt string
		lastLogin string
	)
	err := sc.Scan(&u.ID, &u.Username, &u.PasswordHash, &role, &u.TOTPSecret, &enabled,
		&u.TOTPLastStep, &u.TokenEpoch, &disabled, &prefs, &createdAt, &updatedAt, &lastLogin)
	if err != nil {
		return u, err
	}
	u.Role = model.Role(role)
	u.TOTPEnabled = enabled != 0
	u.Disabled = disabled != 0
	u.CreatedAt = parseTime(createdAt)
	u.UpdatedAt = parseTime(updatedAt)
	if lastLogin != "" {
		if t := parseTime(lastLogin); !t.IsZero() {
			u.LastLoginAt = &t
		}
	}
	u.Prefs = map[string]string{}
	if prefs != "" {
		if err := json.Unmarshal([]byte(prefs), &u.Prefs); err != nil {
			// A prefs blob written by an older version, or corrupted, must not take
			// the whole dashboard down: it is a handful of cosmetic settings, so it
			// is dropped and the defaults apply.
			u.Prefs = map[string]string{}
		}
	}
	return u, nil
}

// CountUsers returns how many accounts exist. Zero means setup has not run yet.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// GetUser loads one account by primary key.
func (s *Store) GetUser(ctx context.Context, id int64) (model.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// GetUserByName loads one account by username. The column is declared
// COLLATE NOCASE, so the comparison ignores case without a function call that
// would defeat the unique index.
func (s *Store) GetUserByName(ctx context.Context, username string) (model.User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE username = ?`, strings.TrimSpace(username)))
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ListUsers returns every account, administrators first and then alphabetically,
// with the number of unused recovery codes filled in.
func (s *Store) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users
		ORDER BY CASE role WHEN 'admin' THEN 0 WHEN 'operator' THEN 1 ELSE 2 END,
		         username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	users := []model.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One grouped query rather than one per user, the same shape as the channel
	// assignments on the sensor list.
	counts := map[int64]int{}
	codeRows, err := s.db.QueryContext(ctx,
		`SELECT user_id, COUNT(*) FROM recovery_codes WHERE used_at = '' GROUP BY user_id`)
	if err != nil {
		return nil, err
	}
	defer codeRows.Close()
	for codeRows.Next() {
		var id int64
		var n int
		if err := codeRows.Scan(&id, &n); err != nil {
			return nil, err
		}
		counts[id] = n
	}
	if err := codeRows.Err(); err != nil {
		return nil, err
	}
	for i := range users {
		users[i].RecoveryLeft = counts[users[i].ID]
	}
	return users, nil
}

// CreateFirstUser creates the first account on a fresh installation and fails
// with ErrSetupTaken if one already exists.
//
// The count and the insert share a transaction on purpose. Two setup requests
// arriving together would otherwise both see an empty table and both succeed,
// which is a second administrator nobody asked for.
func (s *Store) CreateFirstUser(ctx context.Context, u *model.User) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrSetupTaken
		}
		return insertUser(ctx, tx, u, time.Now().UTC())
	})
}

// CreateUser creates an account. The caller hashes the password; the store never
// sees one in the clear.
func (s *Store) CreateUser(ctx context.Context, u *model.User) error {
	return insertUser(ctx, s.db, u, time.Now().UTC())
}

func insertUser(ctx context.Context, ex execer, u *model.User, now time.Time) error {
	u.Username = strings.TrimSpace(u.Username)
	if !u.Role.Valid() {
		u.Role = model.RoleViewer
	}
	prefs, err := encodePrefs(u.Prefs)
	if err != nil {
		return err
	}
	u.CreatedAt, u.UpdatedAt = now, now
	res, err := ex.ExecContext(ctx, `
		INSERT INTO users (username, password_hash, role, totp_secret, totp_enabled,
			totp_step, token_epoch, disabled, prefs, created_at, updated_at, last_login_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,'')`,
		u.Username, u.PasswordHash, string(u.Role), u.TOTPSecret, boolInt(u.TOTPEnabled),
		u.TOTPLastStep, u.TokenEpoch, boolInt(u.Disabled), prefs, fmtTime(now), fmtTime(now))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return fmt.Errorf("%w (%q)", ErrUserExists, u.Username)
		}
		return err
	}
	if u.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	return nil
}

// encodePrefs renders the preference map as the JSON the prefs column holds.
func encodePrefs(prefs map[string]string) (string, error) {
	if len(prefs) == 0 {
		return "{}", nil
	}
	buf, err := json.Marshal(prefs)
	if err != nil {
		return "", fmt.Errorf("encode preferences: %w", err)
	}
	return string(buf), nil
}

// SetUserPrefs replaces one user's interface preferences.
//
// Unknown keys are dropped rather than stored: the set is small, fixed, and
// validated again by the API, and a preference that nothing reads is a row that
// nothing will ever clean up.
func (s *Store) SetUserPrefs(ctx context.Context, id int64, prefs map[string]string) error {
	clean := map[string]string{}
	for _, k := range model.PrefKeys {
		if v, ok := prefs[k]; ok {
			clean[k] = v
		}
	}
	blob, err := encodePrefs(clean)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE users SET prefs = ?, updated_at = ? WHERE id = ?`, blob, fmtTime(time.Now()), id)
	return err
}

// SetUserPref writes a single preference, leaving the others alone.
func (s *Store) SetUserPref(ctx context.Context, id int64, key, value string) error {
	u, err := s.GetUser(ctx, id)
	if err != nil {
		return err
	}
	if u.Prefs == nil {
		u.Prefs = map[string]string{}
	}
	u.Prefs[key] = value
	return s.SetUserPrefs(ctx, id, u.Prefs)
}

// SetUserRole changes a role, refusing to demote the last enabled administrator.
func (s *Store) SetUserRole(ctx context.Context, id int64, role model.Role) error {
	if !role.Valid() {
		return fmt.Errorf("unknown role %q", role)
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if role != model.RoleAdmin {
			if err := guardLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET role = ?, updated_at = ? WHERE id = ?`,
			string(role), fmtTime(time.Now()), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// SetUserDisabled blocks or unblocks an account and signs it out everywhere by
// bumping its token epoch, in the same statement so the two cannot drift apart.
func (s *Store) SetUserDisabled(ctx context.Context, actorID, id int64, disabled bool) error {
	if id == actorID && disabled {
		return ErrSelf
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if disabled {
			if err := guardLastAdmin(ctx, tx, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET disabled = ?, token_epoch = token_epoch + 1, updated_at = ?
			 WHERE id = ?`,
			boolInt(disabled), fmtTime(time.Now()), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// DeleteUser removes an account and, by cascade, its recovery codes.
func (s *Store) DeleteUser(ctx context.Context, actorID, id int64) error {
	if id == actorID {
		return ErrSelf
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := guardLastAdmin(ctx, tx, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// SetUserPassword stores a new password hash and signs the account out
// everywhere. Used both for a user's own change and for an administrator's
// reset, which are the same operation from here.
func (s *Store) SetUserPassword(ctx context.Context, id int64, hash string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, token_epoch = token_epoch + 1, updated_at = ?
			 WHERE id = ?`,
			hash, fmtTime(time.Now()), id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
}

// SetUserTOTP stores the second-factor state. Passing an empty secret clears the
// factor entirely, which is what disabling it means.
func (s *Store) SetUserTOTP(ctx context.Context, id int64, secret string, enabled bool, step int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_secret = ?, totp_enabled = ?, totp_step = ?, updated_at = ?
		 WHERE id = ?`,
		secret, boolInt(enabled), step, fmtTime(time.Now()), id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// RecordTOTPStep records that a code belonging to that time step was accepted.
// The step only ever moves forward, so a request that arrives out of order cannot
// reopen a step whose code has already been spent.
func (s *Store) RecordTOTPStep(ctx context.Context, id, step int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET totp_step = MAX(totp_step, ?) WHERE id = ?`, step, id)
	return err
}

// RecordUserLogin stamps the last successful sign-in.
func (s *Store) RecordUserLogin(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET last_login_at = ? WHERE id = ?`, fmtTime(at), id)
	return err
}

// guardLastAdmin refuses an operation on id when it would leave no enabled
// administrator behind. It is a no-op for anyone who is not one.
func guardLastAdmin(ctx context.Context, tx *sql.Tx, id int64) error {
	var role string
	var disabled int
	err := tx.QueryRowContext(ctx,
		`SELECT role, disabled FROM users WHERE id = ?`, id).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if model.Role(role) != model.RoleAdmin || disabled != 0 {
		return nil
	}
	var others int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM users
		WHERE role = 'admin' AND disabled = 0 AND id <> ?`, id).Scan(&others); err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

func mustAffect(res sql.Result) error {
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// recovery codes
// ---------------------------------------------------------------------------

// recoveryAlphabet is Crockford-ish: the digits and letters that are hard to mix
// up when someone reads a code off a printout. Thirty-two symbols, so one
// character is exactly five bits and the arithmetic stays honest.
const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// RecoveryCodeLen is how many characters a recovery code has. Sixteen characters
// of five bits each is eighty bits, which is far past what is worth guessing and
// still short enough to copy by hand.
const RecoveryCodeLen = 16

// NewRecoveryCodes returns n fresh one-time codes, formatted for a human.
//
// The caller shows them once and hands them back to ReplaceRecoveryCodes, which
// hashes them. They exist in the clear for exactly that long.
func NewRecoveryCodes(n int) ([]string, error) {
	codes := make([]string, 0, n)
	for i := 0; i < n; i++ {
		code, err := newRecoveryCode()
		if err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, nil
}

func newRecoveryCode() (string, error) {
	var raw strings.Builder
	for i := 0; i < RecoveryCodeLen; i++ {
		// A uniform index into the alphabet. math/big rather than a modulo of a
		// random byte: 256 is not a multiple of 32 here only by luck, and the day
		// somebody edits the alphabet that luck runs out.
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(recoveryAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generate recovery code: %w", err)
		}
		raw.WriteByte(recoveryAlphabet[idx.Int64()])
	}
	return groupRecoveryCode(raw.String()), nil
}

// groupRecoveryCode renders sixteen characters as four groups of four, which is
// what makes them readable off a screen without losing your place.
func groupRecoveryCode(code string) string {
	var out strings.Builder
	for i, r := range code {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// NormaliseRecoveryCode strips what a human adds when copying: case, spaces and
// the grouping dashes.
func NormaliseRecoveryCode(code string) string {
	s := strings.ToUpper(strings.TrimSpace(code))
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

// hashRecoveryCode binds a code to one account.
//
// SHA-256, not bcrypt. Eighty random bits have nothing to guess, so there is
// nothing for a slow hash to protect, and eight bcrypt comparisons would add two
// seconds to every recovery sign-in and to every enrolment. The user id in the
// preimage means a hash lifted from one account cannot be replayed against
// another.
func hashRecoveryCode(userID int64, code string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("pg-recovery-v1:%d:%s", userID, NormaliseRecoveryCode(code))))
	return hex.EncodeToString(sum[:])
}

// ReplaceRecoveryCodes hashes and stores a fresh set, discarding any that were
// there before. Returns how many are now valid.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID int64, codes []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
			return err
		}
		now := fmtTime(time.Now())
		for _, code := range codes {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO recovery_codes (user_id, code_hash, used_at, created_at)
				 VALUES (?,?,'',?)`,
				userID, hashRecoveryCode(userID, code), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// ConsumeRecoveryCode checks a code and, if it is unused, marks it used. It
// returns whether the code was valid.
//
// Used codes are kept rather than deleted so that the table still says the code
// existed and when it was spent. Every unused code is compared — not stopping at
// the first match would be pointless, but stopping early would leak the position
// of a match through timing, and hmac.Equal on a fixed-length digest is constant
// time, which makes the whole scan so.
func (s *Store) ConsumeRecoveryCode(ctx context.Context, userID int64, code string, at time.Time) (bool, error) {
	want := hashRecoveryCode(userID, code)
	found := false
	err := s.tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx,
			`SELECT id, code_hash FROM recovery_codes WHERE user_id = ? AND used_at = ''`, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		var match int64
		for rows.Next() {
			var id int64
			var hash string
			if err := rows.Scan(&id, &hash); err != nil {
				return err
			}
			if hmac.Equal([]byte(hash), []byte(want)) {
				match = id
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if match == 0 {
			return nil
		}
		found = true
		_, err = tx.ExecContext(ctx,
			`UPDATE recovery_codes SET used_at = ? WHERE id = ?`, fmtTime(at), match)
		return err
	})
	return found, err
}

// DeleteRecoveryCodes removes every code belonging to one account.
func (s *Store) DeleteRecoveryCodes(ctx context.Context, userID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID)
	return err
}

// CountRecoveryCodes reports how many codes are still unused.
func (s *Store) CountRecoveryCodes(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at = ''`, userID).Scan(&n)
	return n, err
}

// ---------------------------------------------------------------------------
// adopting a pre-multi-user installation
// ---------------------------------------------------------------------------

// adoptLegacyAuth turns the single global dashboard password of an installation
// that predates user accounts into an administrator, in the same transaction that
// creates the users table.
//
// It only ever runs when there are no users at all, so it cannot overwrite an
// installation that has already been through this. The old settings are deleted
// afterwards: two sources of truth for who may sign in is exactly the situation
// this migration exists to end.
func adoptLegacyAuth(ctx context.Context, tx *sql.Tx) (string, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return "", nil
	}

	var hash string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = ?`, model.SettingLegacyPasswordHash).Scan(&hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", err
	}
	if hash == "" {
		return "", nil
	}

	prefs, err := adoptLegacyPrefs(ctx, tx)
	if err != nil {
		return "", err
	}

	admin := &model.User{
		Username:     "admin",
		PasswordHash: hash,
		Role:         model.RoleAdmin,
		Prefs:        prefs,
	}
	if err := insertUser(ctx, tx, admin, time.Now().UTC()); err != nil {
		return "", err
	}

	// The ui.* namespace was the global half of the interface settings; it now
	// lives on the user row, so the rows go. So does the password.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM settings WHERE key = ? OR key LIKE 'ui.%'`,
		model.SettingLegacyPasswordHash); err != nil {
		return "", err
	}

	return fmt.Sprintf("moved the dashboard password into the administrator account %q", admin.Username), nil
}

// adoptLegacyPrefs reads the global ui.* settings into a preference map, keyed
// the way the per-user preferences are keyed.
func adoptLegacyPrefs(ctx context.Context, tx *sql.Tx) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM settings WHERE key LIKE 'ui.%'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	prefs := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		name := strings.TrimPrefix(key, "ui.")
		for _, known := range model.PrefKeys {
			if name == known {
				prefs[name] = value
			}
		}
	}
	return prefs, rows.Err()
}
