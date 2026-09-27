package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ---------------------------------------------------------------------------
// Two-step verification
// ---------------------------------------------------------------------------

// Sign-in challenge purposes. A challenge is the state between a password
// that was right and a session that has not been minted yet.
const (
	// ChallengeVerify waits for a code from an account that has two-step on.
	ChallengeVerify = "verify"
	// ChallengeEnrol waits for an account that has none to set it up, because
	// security.require_two_step says every local account must.
	ChallengeEnrol = "enrol"
)

// TwoStep is one account's authenticator enrolment. The secret is sealed
// with the instance key; the store never sees it in the clear.
type TwoStep struct {
	UserID    string
	SecretEnc []byte
	// EnabledAt is nil while the enrolment waits for its first code.
	EnabledAt *time.Time
	// LastStep is the most recent time step a code was accepted for.
	LastStep  int64
	CreatedAt time.Time
}

// Enabled reports whether sign-in asks this account for a code.
func (t *TwoStep) Enabled() bool { return t != nil && t.EnabledAt != nil }

// SignInChallenge is a sign-in that has passed the password and waits for its
// second step.
type SignInChallenge struct {
	TokenHash string
	UserID    string
	Purpose   string
	IP        string
	UserAgent string
	Failures  int
	CreatedAt time.Time
	ExpiresAt time.Time
}

// GetTwoStep returns the account's enrolment, confirmed or pending, or
// ErrNotFound when it has none.
func (s *Store) GetTwoStep(ctx context.Context, userID string) (*TwoStep, error) {
	var t TwoStep
	var enabled sql.NullInt64
	var created int64
	err := s.read.QueryRowContext(ctx, `SELECT user_id, secret_enc, enabled_at, last_step, created_at
		FROM user_two_step WHERE user_id = ?`, userID).
		Scan(&t.UserID, &t.SecretEnc, &enabled, &t.LastStep, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("two-step for %s: %w", userID, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	t.EnabledAt, t.CreatedAt = atp(enabled), at(created)
	return &t, nil
}

// TwoStepEnabledUsers returns the IDs of every account with two-step on, for
// the users list, which would otherwise ask once per row.
func (s *Store) TwoStepEnabledUsers(ctx context.Context) (map[string]bool, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT user_id FROM user_two_step WHERE enabled_at IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// BeginTwoStep stores a new, unconfirmed secret for an account that has
// two-step off, replacing any enrolment it started and abandoned. An account
// that has it on is refused with ErrConflict: replacing a working secret must
// go through turning it off, which asks for the password and a code.
func (s *Store) BeginTwoStep(ctx context.Context, userID string, secretEnc []byte) error {
	res, err := s.exec(ctx, `INSERT INTO user_two_step (user_id, secret_enc, enabled_at, last_step, created_at)
		VALUES (?, ?, NULL, 0, ?)
		ON CONFLICT(user_id) DO UPDATE SET secret_enc=excluded.secret_enc, last_step=0,
			created_at=excluded.created_at
		WHERE user_two_step.enabled_at IS NULL`, userID, secretEnc, ms(s.Now()))
	if err != nil {
		return wrapWrite(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("two-step for %s is already on: %w", userID, ErrConflict)
	}
	return nil
}

// ConfirmTwoStep turns a pending enrolment on, records the step its first
// code was accepted for, and replaces the account's recovery codes -- in one
// transaction, so there is never an account with two-step on and no way back
// in. It returns ErrConflict when there is no pending enrolment to confirm.
func (s *Store) ConfirmTwoStep(ctx context.Context, userID string, step int64, codeHashes []string) error {
	now := ms(s.Now())
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE user_two_step SET enabled_at=?, last_step=?
			WHERE user_id=? AND enabled_at IS NULL`, now, step, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("no two-step enrolment is waiting for %s: %w", userID, ErrConflict)
		}
		return replaceRecoveryCodes(ctx, tx, userID, codeHashes, now)
	})
}

// AcceptTwoStepStep records that a code for this time step was accepted, and
// reports false when this step, or a later one, already was. It is a single
// conditional write, so two requests racing with the same code cannot both
// win.
func (s *Store) AcceptTwoStepStep(ctx context.Context, userID string, step int64) (bool, error) {
	res, err := s.exec(ctx, `UPDATE user_two_step SET last_step=?
		WHERE user_id=? AND enabled_at IS NOT NULL AND last_step < ?`, step, userID, step)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteTwoStep turns two-step off: the secret and every recovery code go.
// It reports whether there was anything to remove.
func (s *Store) DeleteTwoStep(ctx context.Context, userID string) (bool, error) {
	var removed bool
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM user_two_step WHERE user_id=?`, userID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		removed = n > 0
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id=?`, userID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM sign_in_challenges WHERE user_id=?`, userID)
		return err
	})
	return removed, err
}

// ReplaceRecoveryCodes discards every recovery code the account has, spent or
// not, and stores these.
func (s *Store) ReplaceRecoveryCodes(ctx context.Context, userID string, codeHashes []string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		return replaceRecoveryCodes(ctx, tx, userID, codeHashes, ms(s.Now()))
	})
}

func replaceRecoveryCodes(ctx context.Context, tx *sql.Tx, userID string, codeHashes []string, now int64) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM user_recovery_codes WHERE user_id=?`, userID); err != nil {
		return err
	}
	for _, h := range codeHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO user_recovery_codes (user_id, code_hash, used_at, created_at)
			VALUES (?, ?, NULL, ?)`, userID, h, now); err != nil {
			return wrapWrite(err)
		}
	}
	return nil
}

// UseRecoveryCode spends one recovery code, and reports false when it does
// not exist or was spent already. The spend is one conditional write, so a
// code used twice at once is accepted once.
func (s *Store) UseRecoveryCode(ctx context.Context, userID, codeHash string) (bool, error) {
	res, err := s.exec(ctx, `UPDATE user_recovery_codes SET used_at=?
		WHERE user_id=? AND code_hash=? AND used_at IS NULL`, ms(s.Now()), userID, codeHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RemainingRecoveryCodes counts the account's unspent recovery codes.
func (s *Store) RemainingRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM user_recovery_codes
		WHERE user_id=? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

// CreateSignInChallenge stores a pending sign-in. Any earlier challenge for
// the same account is left alone: two browsers may each be half-way through.
func (s *Store) CreateSignInChallenge(ctx context.Context, c *SignInChallenge) error {
	c.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO sign_in_challenges (token_hash, user_id, purpose, ip, user_agent,
		failures, created_at, expires_at) VALUES (?,?,?,?,?,0,?,?)`,
		c.TokenHash, c.UserID, c.Purpose, c.IP, c.UserAgent, ms(c.CreatedAt), ms(c.ExpiresAt))
	return wrapWrite(err)
}

// GetSignInChallenge returns a pending sign-in by the hash of its cookie.
// Expiry is the caller's to judge, against its own clock.
func (s *Store) GetSignInChallenge(ctx context.Context, tokenHash string) (*SignInChallenge, error) {
	var c SignInChallenge
	var created, expires int64
	err := s.read.QueryRowContext(ctx, `SELECT token_hash, user_id, purpose, ip, user_agent, failures,
		created_at, expires_at FROM sign_in_challenges WHERE token_hash = ?`, tokenHash).
		Scan(&c.TokenHash, &c.UserID, &c.Purpose, &c.IP, &c.UserAgent, &c.Failures, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt, c.ExpiresAt = at(created), at(expires)
	return &c, nil
}

// FailSignInChallenge counts a wrong code against a pending sign-in and
// returns the new count.
func (s *Store) FailSignInChallenge(ctx context.Context, tokenHash string) (int, error) {
	var n int
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE sign_in_challenges SET failures = failures + 1
			WHERE token_hash=?`, tokenHash); err != nil {
			return err
		}
		err := tx.QueryRowContext(ctx, `SELECT failures FROM sign_in_challenges WHERE token_hash=?`, tokenHash).Scan(&n)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return n, err
}

// DeleteSignInChallenge ends a pending sign-in, finished or abandoned.
func (s *Store) DeleteSignInChallenge(ctx context.Context, tokenHash string) error {
	_, err := s.exec(ctx, `DELETE FROM sign_in_challenges WHERE token_hash=?`, tokenHash)
	return err
}
