package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Users
// ---------------------------------------------------------------------------

const userCols = `id, username, email, display_name, role, password_hash, oidc_subject,
	disabled, must_change_password, created_at, last_login_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var disabled, mustChange int
	var created int64
	var login sql.NullInt64
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.PasswordHash,
		&u.OIDCSubject, &disabled, &mustChange, &created, &login)
	if err != nil {
		return nil, err
	}
	u.Disabled, u.MustChangePassword = disabled == 1, mustChange == 1
	u.CreatedAt, u.LastLoginAt = at(created), atp(login)
	return &u, nil
}

// CreateUser inserts a local or OIDC-provisioned user. Usernames are stored
// lowercased so logins are case-insensitive.
func (s *Store) CreateUser(ctx context.Context, u *User) error {
	if u.ID == "" {
		u.ID = NewID(PrefixUser)
	}
	u.Username = strings.ToLower(strings.TrimSpace(u.Username))
	u.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO users (`+userCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		u.ID, u.Username, u.Email, u.DisplayName, string(u.Role), u.PasswordHash,
		u.OIDCSubject, boolInt(u.Disabled), boolInt(u.MustChangePassword),
		ms(u.CreatedAt), msp(u.LastLoginAt))
	return wrapWrite(err)
}

// GetUser returns a user by ID.
func (s *Store) GetUser(ctx context.Context, id string) (*User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %s: %w", id, ErrNotFound)
	}
	return u, err
}

// GetUserByUsername looks a user up for password login.
func (s *Store) GetUserByUsername(ctx context.Context, username string) (*User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`,
		strings.ToLower(strings.TrimSpace(username)))
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %q: %w", username, ErrNotFound)
	}
	return u, err
}

// GetUserByOIDCSubject looks a user up during an OIDC callback.
func (s *Store) GetUserByOIDCSubject(ctx context.Context, sub string) (*User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+userCols+` FROM users
		WHERE oidc_subject = ? AND oidc_subject != ''`, sub)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return u, err
}

// ListUsers returns every account, ordered by username.
func (s *Store) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns how many accounts exist. Zero means the instance has not
// been bootstrapped and the API should say so rather than 401 into a void.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// CountAdmins returns how many enabled admins exist, so the API can refuse to
// delete or demote the last one.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role='admin' AND disabled=0`).Scan(&n)
	return n, err
}

// UpdateUser persists profile and role changes.
func (s *Store) UpdateUser(ctx context.Context, u *User) error {
	res, err := s.exec(ctx, `UPDATE users SET username=?, email=?, display_name=?, role=?,
		password_hash=?, oidc_subject=?, disabled=?, must_change_password=?, last_login_at=?
		WHERE id=?`,
		strings.ToLower(strings.TrimSpace(u.Username)), u.Email, u.DisplayName, string(u.Role),
		u.PasswordHash, u.OIDCSubject, boolInt(u.Disabled), boolInt(u.MustChangePassword),
		msp(u.LastLoginAt), u.ID)
	if err != nil {
		return wrapWrite(err)
	}
	return affected(res, "user", u.ID)
}

// SetPassword stores a new argon2id hash and clears the change-on-login flag.
func (s *Store) SetPassword(ctx context.Context, userID, hash string) error {
	res, err := s.exec(ctx,
		`UPDATE users SET password_hash=?, must_change_password=0 WHERE id=?`, hash, userID)
	if err != nil {
		return err
	}
	return affected(res, "user", userID)
}

// TouchLogin records a successful authentication.
func (s *Store) TouchLogin(ctx context.Context, userID string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE users SET last_login_at=? WHERE id=?`, ms(now), userID)
	return err
}

// DeleteUser removes an account and cascades to its sessions.
func (s *Store) DeleteUser(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM users WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "user", id)
}

// ---------------------------------------------------------------------------
// Sessions
// ---------------------------------------------------------------------------

// CreateSession stores a browser session keyed on a hashed cookie value.
func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	if sess.ID == "" {
		sess.ID = NewID(PrefixSession)
	}
	sess.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO sessions (id, user_id, token_hash, user_agent, ip,
		created_at, expires_at) VALUES (?,?,?,?,?,?,?)`,
		sess.ID, sess.UserID, sess.TokenHash, sess.UserAgent, sess.IP,
		ms(sess.CreatedAt), ms(sess.ExpiresAt))
	return wrapWrite(err)
}

// GetSessionByTokenHash resolves a cookie to its session and user in one hop.
func (s *Store) GetSessionByTokenHash(ctx context.Context, hash string) (*Session, *User, error) {
	row := s.read.QueryRowContext(ctx, `SELECT s.id, s.user_id, s.token_hash, s.user_agent,
		s.ip, s.created_at, s.expires_at, `+prefixed(userCols, "u")+`
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token_hash = ?`, hash)

	var sess Session
	var u User
	var created, expires int64
	var disabled, mustChange int
	var uCreated int64
	var login sql.NullInt64
	err := row.Scan(&sess.ID, &sess.UserID, &sess.TokenHash, &sess.UserAgent, &sess.IP,
		&created, &expires,
		&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.PasswordHash,
		&u.OIDCSubject, &disabled, &mustChange, &uCreated, &login)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	sess.CreatedAt, sess.ExpiresAt = at(created), at(expires)
	u.Disabled, u.MustChangePassword = disabled == 1, mustChange == 1
	u.CreatedAt, u.LastLoginAt = at(uCreated), atp(login)
	return &sess, &u, nil
}

// prefixed qualifies a column list with a table alias.
func prefixed(cols, alias string) string {
	parts := strings.Split(cols, ",")
	for i, p := range parts {
		parts[i] = alias + "." + strings.TrimSpace(strings.ReplaceAll(p, "\n\t", ""))
	}
	return strings.Join(parts, ", ")
}

// DeleteSession logs one session out.
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.exec(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hash)
	return err
}

// DeleteUserSessions logs a user out everywhere, e.g. after a password change.
func (s *Store) DeleteUserSessions(ctx context.Context, userID string) error {
	_, err := s.exec(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	return err
}

// PruneSessions removes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

const tokenCols = `id, name, role, user_id, scopes, token_hash, prefix, revoked,
	created_at, expires_at, last_used_at`

func scanToken(sc interface{ Scan(...any) error }) (*APIToken, error) {
	var t APIToken
	var revoked int
	var created int64
	var expires, used sql.NullInt64
	err := sc.Scan(&t.ID, &t.Name, &t.Role, &t.UserID, &t.Scopes, &t.TokenHash, &t.Prefix,
		&revoked, &created, &expires, &used)
	if err != nil {
		return nil, err
	}
	t.Revoked = revoked == 1
	t.CreatedAt = at(created)
	t.ExpiresAt, t.LastUsedAt = atp(expires), atp(used)
	return &t, nil
}

// CreateAPIToken stores a hashed automation credential.
func (s *Store) CreateAPIToken(ctx context.Context, t *APIToken) error {
	if t.ID == "" {
		t.ID = NewID(PrefixToken)
	}
	t.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, string(t.Role), t.UserID, t.Scopes, t.TokenHash, t.Prefix,
		boolInt(t.Revoked), ms(t.CreatedAt), msp(t.ExpiresAt), msp(t.LastUsedAt))
	return wrapWrite(err)
}

// GetAPITokenByHash authenticates a bearer token.
func (s *Store) GetAPITokenByHash(ctx context.Context, hash string) (*APIToken, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_hash = ?`, hash)
	t, err := scanToken(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// ListAPITokens returns every token, newest first. Hashes never leave the store.
func (s *Store) ListAPITokens(ctx context.Context) ([]*APIToken, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+tokenCols+` FROM api_tokens ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*APIToken
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchAPIToken records that a token was just used.
func (s *Store) TouchAPIToken(ctx context.Context, id string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE api_tokens SET last_used_at=? WHERE id=?`, ms(now), id)
	return err
}

// RevokeAPIToken permanently disables a token without losing its audit trail.
func (s *Store) RevokeAPIToken(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `UPDATE api_tokens SET revoked=1 WHERE id=?`, id)
	if err != nil {
		return err
	}
	return affected(res, "api token", id)
}

// DeleteAPIToken removes a token row entirely.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM api_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "api token", id)
}

// ---------------------------------------------------------------------------
// Join tokens
// ---------------------------------------------------------------------------

const joinCols = `id, token_hash, prefix, created_by, labels, capacity, created_at,
	expires_at, used_at, used_by_id`

func scanJoin(sc interface{ Scan(...any) error }) (*JoinToken, error) {
	var t JoinToken
	var created, expires int64
	var used sql.NullInt64
	err := sc.Scan(&t.ID, &t.TokenHash, &t.Prefix, &t.CreatedBy, &t.Labels, &t.Capacity,
		&created, &expires, &used, &t.UsedByID)
	if err != nil {
		return nil, err
	}
	t.CreatedAt, t.ExpiresAt, t.UsedAt = at(created), at(expires), atp(used)
	return &t, nil
}

// CreateJoinToken mints a short-lived agent enrolment credential.
func (s *Store) CreateJoinToken(ctx context.Context, t *JoinToken) error {
	if t.ID == "" {
		t.ID = NewID(PrefixJoin)
	}
	t.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO join_tokens (`+joinCols+`) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.TokenHash, t.Prefix, t.CreatedBy, t.Labels, t.Capacity,
		ms(t.CreatedAt), ms(t.ExpiresAt), msp(t.UsedAt), t.UsedByID)
	return wrapWrite(err)
}

// ListJoinTokens returns outstanding and spent join tokens, newest first.
func (s *Store) ListJoinTokens(ctx context.Context) ([]*JoinToken, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+joinCols+` FROM join_tokens ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JoinToken
	for rows.Next() {
		t, err := scanJoin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetJoinToken returns one join token by ID, spent or not.
//
// It exists for the page that waits for a host to arrive: once the token is
// redeemed, used_by_id is the host the agent became, and that is the only link
// between the credential an operator handed out and the machine that used it.
func (s *Store) GetJoinToken(ctx context.Context, id string) (*JoinToken, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+joinCols+` FROM join_tokens WHERE id = ?`, id)
	t, err := scanJoin(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("join token %s: %w", id, ErrNotFound)
	}
	return t, err
}

// RedeemJoinToken atomically marks a join token used and returns it. A second
// attempt with the same token fails, which is what makes it single-use.
func (s *Store) RedeemJoinToken(ctx context.Context, hash, hostID string, now time.Time) (*JoinToken, error) {
	var out *JoinToken
	err := s.tx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+joinCols+` FROM join_tokens WHERE token_hash = ?`, hash)
		t, err := scanJoin(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if !t.Usable(now) {
			if t.UsedAt != nil {
				return errors.New("join token has already been used")
			}
			return errors.New("join token has expired")
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE join_tokens SET used_at=?, used_by_id=? WHERE id=?`,
			ms(now), hostID, t.ID); err != nil {
			return err
		}
		t.UsedAt, t.UsedByID = &now, hostID
		out = t
		return nil
	})
	return out, err
}

// DeleteJoinToken revokes an unused join token.
func (s *Store) DeleteJoinToken(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM join_tokens WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return affected(res, "join token", id)
}

// PruneJoinTokens removes expired, unused join tokens.
func (s *Store) PruneJoinTokens(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM join_tokens WHERE used_at IS NULL AND expires_at < ?`, ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// GetSetting returns one setting value, or "" if it is unset.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.read.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting upserts one setting.
func (s *Store) SetSetting(ctx context.Context, key, value string, secret bool) error {
	_, err := s.exec(ctx, `INSERT INTO settings (key, value, secret, updated_at) VALUES (?,?,?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, secret=excluded.secret,
		updated_at=excluded.updated_at`,
		key, value, boolInt(secret), ms(s.Now()))
	return err
}

// ListSettings returns every setting. Values of secret rows are blanked, so
// this is safe to hand to an API handler.
func (s *Store) ListSettings(ctx context.Context) ([]Setting, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT key, value, secret, updated_at FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Setting
	for rows.Next() {
		var st Setting
		var secret int
		var updated int64
		if err := rows.Scan(&st.Key, &st.Value, &secret, &updated); err != nil {
			return nil, err
		}
		st.Secret, st.UpdatedAt = secret == 1, at(updated)
		if st.Secret {
			st.Value = ""
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.exec(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}
