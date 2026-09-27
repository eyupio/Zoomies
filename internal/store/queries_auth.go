package store

import (
	"context"
	"database/sql"
	"encoding/json"
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

// CountOutstandingJoinTokens counts join tokens that could still enrol a host:
// unused and unexpired. It is what limits.join_tokens is measured against,
// because a spent or lapsed token holds nothing an attacker could use and the
// row count would refuse an operator for history.
func (s *Store) CountOutstandingJoinTokens(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM join_tokens WHERE used_at IS NULL AND expires_at >= ?`, ms(s.Now())).Scan(&n)
	return n, err
}

// CountAdmins returns how many enabled admins exist, so the API can refuse to
// delete or demote the last one.
func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	// Platform counts: it is above administrator, so an instance whose only
	// remaining account holds it is not an instance nobody can administer.
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role IN ('admin','platform') AND disabled=0`).Scan(&n)
	return n, err
}

// CountPlatform counts the enabled accounts that may operate the process
// itself. An administrator cannot stand in for one, which is why it is
// counted separately from CountAdmins.
func (s *Store) CountPlatform(ctx context.Context) (int, error) {
	var n int
	err := s.read.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE role='platform' AND disabled=0`).Scan(&n)
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

// UserPreferences returns the account's private UI preferences. No row is the
// ordinary state for an account that has never changed a persisted layout.
func (s *Store) UserPreferences(ctx context.Context, userID string) (json.RawMessage, error) {
	var raw string
	err := s.read.QueryRowContext(ctx,
		`SELECT preferences FROM user_preferences WHERE user_id = ?`, userID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return json.RawMessage(`{}`), nil
	}
	if err != nil {
		return nil, err
	}
	if !json.Valid([]byte(raw)) {
		return nil, fmt.Errorf("preferences for user %s are not valid JSON", userID)
	}
	return json.RawMessage(raw), nil
}

// SetUserPreferences replaces the account's private UI preferences. The API
// validates and bounds the document; the store keeps it opaque so UI choices
// do not become domain fields or migrations of their own.
func (s *Store) SetUserPreferences(ctx context.Context, userID string, value json.RawMessage) error {
	_, err := s.exec(ctx, `INSERT INTO user_preferences (user_id, preferences, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(user_id) DO UPDATE SET
		preferences=excluded.preferences, updated_at=excluded.updated_at`,
		userID, string(value), ms(s.Now()))
	return wrapWrite(err)
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

// DeleteUserSessionsExcept logs a user out everywhere but the session whose
// cookie hashes to keep -- the browser that made the change which called for
// it. An empty keep ends them all.
func (s *Store) DeleteUserSessionsExcept(ctx context.Context, userID, keep string) error {
	_, err := s.exec(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash != ?`, userID, keep)
	return err
}

// PruneSessions removes expired sessions, and the expired sign-ins that were
// waiting for a second step and never became one.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.exec(ctx, `DELETE FROM sessions WHERE expires_at < ?`, ms(now))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	res, err = s.exec(ctx, `DELETE FROM sign_in_challenges WHERE expires_at < ?`, ms(now))
	if err != nil {
		return n, err
	}
	m, err := res.RowsAffected()
	return n + m, err
}

// ---------------------------------------------------------------------------
// API tokens
// ---------------------------------------------------------------------------

const tokenCols = `id, name, role, owner_role, user_id, scopes, token_hash, prefix, revoked,
	created_at, expires_at, last_used_at`

func scanToken(sc interface{ Scan(...any) error }) (*APIToken, error) {
	var t APIToken
	var revoked int
	var created int64
	var expires, used sql.NullInt64
	err := sc.Scan(&t.ID, &t.Name, &t.Role, &t.OwnerRole, &t.UserID, &t.Scopes, &t.TokenHash, &t.Prefix,
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
	_, err := s.exec(ctx, `INSERT INTO api_tokens (`+tokenCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.Name, string(t.Role), string(t.OwnerRole), t.UserID, t.Scopes, t.TokenHash, t.Prefix,
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

// DeleteAPIToken removes a token's row for good. It refuses a token that could
// still authenticate -- not revoked and not past its expiry -- with
// ErrConflict. The condition is in the DELETE itself rather than checked first,
// so a live credential can never vanish from the list while it still works:
// deleting is tidying up after a revocation, never a quieter way to do one.
func (s *Store) DeleteAPIToken(ctx context.Context, id string) error {
	res, err := s.exec(ctx, `DELETE FROM api_tokens WHERE id = ?
		AND (revoked = 1 OR (expires_at IS NOT NULL AND expires_at < ?))`, id, ms(s.Now()))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 1 {
		return nil
	}
	var exists int
	if err := s.read.QueryRowContext(ctx, `SELECT COUNT(*) FROM api_tokens WHERE id = ?`, id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("api token %s: %w", id, ErrNotFound)
	}
	return fmt.Errorf("api token %s is still active; revoke it before deleting it: %w", id, ErrConflict)
}

// RevokeAPITokensForUser revokes every token an account owns, and reports how
// many it revoked.
//
// It is what makes disabling or deleting an account mean something. A session
// dies with the row it points at, but a token is a bearer credential with its
// own role on it: nothing about the account is consulted when one is presented,
// so a departed administrator's token outlived the account it was issued to
// until this was called alongside.
//
// Tokens with no owner -- the empty user_id a token created out of band
// carries -- belong to nobody and are left alone.
func (s *Store) RevokeAPITokensForUser(ctx context.Context, userID string) (int64, error) {
	if userID == "" {
		return 0, nil
	}
	res, err := s.exec(ctx, `UPDATE api_tokens SET revoked=1 WHERE user_id=? AND revoked=0`, userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// Join tokens
// ---------------------------------------------------------------------------

const joinCols = `id, token_hash, prefix, created_by, labels, capacity, created_at,
	expires_at, used_at, used_by_id, machine_id, expected_name`

func scanJoin(sc interface{ Scan(...any) error }) (*JoinToken, error) {
	var t JoinToken
	var created, expires int64
	var used sql.NullInt64
	err := sc.Scan(&t.ID, &t.TokenHash, &t.Prefix, &t.CreatedBy, &t.Labels, &t.Capacity,
		&created, &expires, &used, &t.UsedByID, &t.MachineID, &t.ExpectedName)
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
	_, err := s.exec(ctx, `INSERT INTO join_tokens (`+joinCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.TokenHash, t.Prefix, t.CreatedBy, t.Labels, t.Capacity,
		ms(t.CreatedAt), ms(t.ExpiresAt), msp(t.UsedAt), t.UsedByID, t.MachineID, t.ExpectedName)
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

// JoinClaim is what the agent redeeming a token says it is: the host row the
// join will land on, and the name it asked for.
//
// The name is carried because it is the only part of the claim a scoped token
// can be checked against. The host id is minted by the controller for this
// join, so it proves nothing about who is joining.
type JoinClaim struct {
	HostID string
	Name   string
}

// RedeemJoinToken atomically marks a join token used and returns it. A second
// attempt with the same token fails, which is what makes it single-use.
//
// It takes the claim rather than a bare host id so that a token minted for one
// machine cannot enrol another, and the scope check runs inside the same
// transaction that spends the token: a credential that lives inside a guest is
// readable by more people than one an operator pastes, and checking the scope
// after the redemption would leave a race in which a copied token spends
// itself first and is refused second.
//
// A token with no scope -- the ones an operator hands out by hand -- is
// unaffected, because the person who pasted it chose the name themselves.
func (s *Store) RedeemJoinToken(ctx context.Context, hash string, claim JoinClaim, now time.Time) (*JoinToken, error) {
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
				return ErrJoinTokenUsed
			}
			return ErrJoinTokenExpired
		}
		if t.ExpectedName != "" && t.ExpectedName != claim.Name {
			return fmt.Errorf("%w: this token was minted for %q and was offered by %q",
				ErrJoinTokenScope, t.ExpectedName, claim.Name)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE join_tokens SET used_at=?, used_by_id=? WHERE id=?`,
			ms(now), claim.HostID, t.ID); err != nil {
			return err
		}
		t.UsedAt, t.UsedByID = &now, claim.HostID
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

// RevokeUnusedJoinToken serialises revocation with redemption. A token already
// used is retained as enrolment evidence and must never be replaced.
func (s *Store) RevokeUnusedJoinToken(ctx context.Context, id string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		t, err := scanJoin(tx.QueryRowContext(ctx, `SELECT `+joinCols+` FROM join_tokens WHERE id=?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if t.UsedAt != nil {
			return ErrJoinTokenUsed
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM join_tokens WHERE id=?`, id)
		return err
	})
}
