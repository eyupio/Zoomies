package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ---------------------------------------------------------------------------
// OAuth for MCP
// ---------------------------------------------------------------------------

// How an OAuth client came to exist.
const (
	// OAuthClientDynamic registered itself through RFC 7591.
	OAuthClientDynamic = "dynamic"
	// OAuthClientMetadata is a Client ID Metadata Document: its client_id is
	// the https URL its metadata was fetched from.
	OAuthClientMetadata = "metadata"
	// OAuthClientAdmin was created by an administrator, for a client that is
	// given its ID -- and, if it is confidential, its secret -- by hand.
	OAuthClientAdmin = "admin"
)

// The kinds of credential a grant has outstanding.
const (
	OAuthCode    = "code"
	OAuthAccess  = "access"
	OAuthRefresh = "refresh"
)

// OAuthClient is an application that may ask a person for a grant.
type OAuthClient struct {
	ID string `json:"id"`
	// ClientID is what the client sends: the row ID for a registered or an
	// administrator's client, and the document URL for a metadata client.
	ClientID     string      `json:"client_id"`
	Kind         string      `json:"kind"`
	Name         string      `json:"name"`
	ClientURI    string      `json:"client_uri,omitempty"`
	RedirectURIs StringSlice `json:"redirect_uris"`
	// SecretHash is empty for a public client. It never leaves the store in
	// a response: the JSON tag is there so that a careless encode redacts.
	SecretHash      string     `json:"-"`
	SecretPrefix    string     `json:"secret_prefix,omitempty"`
	SecretRotatedAt *time.Time `json:"secret_rotated_at,omitempty"`
	CreatedBy       string     `json:"created_by,omitempty"`
	CreatedIP       string     `json:"created_ip,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	RevokedAt       *time.Time `json:"revoked_at,omitempty"`
}

// Confidential reports whether the client has a secret to prove at the token
// endpoint.
func (c *OAuthClient) Confidential() bool { return c.SecretHash != "" }

// Revoked reports whether the client has been turned away for good.
func (c *OAuthClient) Revoked() bool { return c.RevokedAt != nil }

// OAuthRequest is an authorisation request that has been checked and waits for
// the person to decide.
type OAuthRequest struct {
	ID            string
	ClientID      string
	RedirectURI   string
	State         string
	CodeChallenge string
	Resource      string
	Scope         string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

// OAuthGrant is one person's consent for one client: an MCP connection.
type OAuthGrant struct {
	ID            string
	ClientID      string
	UserID        string
	Role          Role
	Scope         string
	Resource      string
	CreatedIP     string
	CreatedAt     time.Time
	LastUsedAt    *time.Time
	RevokedAt     *time.Time
	RevokedReason string

	// Filled by the listing queries, for a page that names both ends.
	ClientName string
	ClientKind string
	Username   string
}

// Revoked reports whether the connection has been ended.
func (g *OAuthGrant) Revoked() bool { return g.RevokedAt != nil }

// OAuthToken is a hashed code, access token or refresh token.
type OAuthToken struct {
	TokenHash     string
	GrantID       string
	Kind          string
	RedirectURI   string
	CodeChallenge string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	UsedAt        *time.Time
}

const oauthClientCols = `id, client_id, kind, name, client_uri, redirect_uris, secret_hash, secret_prefix,
	secret_rotated_at, created_by, created_ip, created_at, last_used_at, revoked_at`

func scanOAuthClient(sc interface{ Scan(...any) error }) (*OAuthClient, error) {
	var c OAuthClient
	var created int64
	var rotated, used, revoked sql.NullInt64
	err := sc.Scan(&c.ID, &c.ClientID, &c.Kind, &c.Name, &c.ClientURI, &c.RedirectURIs, &c.SecretHash,
		&c.SecretPrefix, &rotated, &c.CreatedBy, &c.CreatedIP, &created, &used, &revoked)
	if err != nil {
		return nil, err
	}
	c.CreatedAt = at(created)
	c.SecretRotatedAt, c.LastUsedAt, c.RevokedAt = atp(rotated), atp(used), atp(revoked)
	return &c, nil
}

// CreateOAuthClient stores a client. A registered or an administrator's client
// takes its row ID as its client_id; a metadata client arrives with its URL.
func (s *Store) CreateOAuthClient(ctx context.Context, c *OAuthClient) error {
	if c.ID == "" {
		c.ID = NewID(PrefixOAuthClient)
	}
	if c.ClientID == "" {
		c.ClientID = c.ID
	}
	c.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO oauth_clients (`+oauthClientCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.ClientID, c.Kind, c.Name, c.ClientURI, c.RedirectURIs, c.SecretHash, c.SecretPrefix,
		msp(c.SecretRotatedAt), c.CreatedBy, c.CreatedIP, ms(c.CreatedAt), msp(c.LastUsedAt), msp(c.RevokedAt))
	return wrapWrite(err)
}

// UpdateOAuthMetadataClient refreshes what a metadata document says about its
// client -- its name and redirect URIs can change between fetches -- without
// touching whether it has been revoked.
func (s *Store) UpdateOAuthMetadataClient(ctx context.Context, id, name, clientURI string, redirects []string) error {
	res, err := s.exec(ctx, `UPDATE oauth_clients SET name=?, client_uri=?, redirect_uris=? WHERE id=? AND kind=?`,
		name, clientURI, StringSlice(redirects), id, OAuthClientMetadata)
	if err != nil {
		return err
	}
	return affected(res, "oauth client", id)
}

// GetOAuthClient finds a client by its row ID.
func (s *Store) GetOAuthClient(ctx context.Context, id string) (*OAuthClient, error) {
	return s.oauthClientWhere(ctx, `id = ?`, id)
}

// GetOAuthClientByClientID finds a client by the identifier it presents.
func (s *Store) GetOAuthClientByClientID(ctx context.Context, clientID string) (*OAuthClient, error) {
	return s.oauthClientWhere(ctx, `client_id = ?`, clientID)
}

func (s *Store) oauthClientWhere(ctx context.Context, where, arg string) (*OAuthClient, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients WHERE `+where, arg)
	c, err := scanOAuthClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("oauth client %s: %w", arg, ErrNotFound)
	}
	return c, err
}

// FindOAuthRegistration returns the live self-registered client that
// described itself exactly as this one does -- the same name, client URI and
// set of redirect URIs -- or ErrNotFound. It is what lets a client that
// registers afresh on every start be handed the registration it already has,
// rather than leaving a new row behind each time.
func (s *Store) FindOAuthRegistration(ctx context.Context, name, clientURI string, redirects []string) (*OAuthClient, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients
		WHERE kind=? AND revoked_at IS NULL AND name=? AND client_uri=? ORDER BY created_at DESC, id`,
		OAuthClientDynamic, name, clientURI)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	want := sortedCopy(redirects)
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		if slices.Equal(sortedCopy(c.RedirectURIs), want) {
			return c, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("oauth registration %q: %w", name, ErrNotFound)
}

func sortedCopy(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

// ListOAuthClients returns every client, newest first.
func (s *Store) ListOAuthClients(ctx context.Context) ([]*OAuthClient, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT `+oauthClientCols+` FROM oauth_clients ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OAuthClient
	for rows.Next() {
		c, err := scanOAuthClient(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountOAuthGrantsByClient returns how many live connections each client has,
// for the clients list.
func (s *Store) CountOAuthGrantsByClient(ctx context.Context) (map[string]int, error) {
	rows, err := s.read.QueryContext(ctx, `SELECT client_id, COUNT(*) FROM oauth_grants
		WHERE revoked_at IS NULL AND last_used_at IS NOT NULL GROUP BY client_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SetOAuthClientSecret replaces a client's secret. The old one stops working
// in the same write, which is what rotating a leaked secret has to mean.
func (s *Store) SetOAuthClientSecret(ctx context.Context, id, hash, prefix string) error {
	res, err := s.exec(ctx, `UPDATE oauth_clients SET secret_hash=?, secret_prefix=?, secret_rotated_at=?
		WHERE id=? AND revoked_at IS NULL`, hash, prefix, ms(s.Now()), id)
	if err != nil {
		return err
	}
	return affected(res, "oauth client", id)
}

// TouchOAuthClient records that a client was just used.
func (s *Store) TouchOAuthClient(ctx context.Context, id string) error {
	_, err := s.exec(ctx, `UPDATE oauth_clients SET last_used_at=? WHERE id=?`, ms(s.Now()), id)
	return err
}

// RevokeOAuthClient turns a client away for good and ends every connection it
// holds, in one transaction: a client that is revoked while a token it holds
// still answers has not been revoked.
func (s *Store) RevokeOAuthClient(ctx context.Context, id string) (int64, error) {
	now := ms(s.Now())
	var ended int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE oauth_clients SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, now, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM oauth_clients WHERE id=?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("oauth client %s: %w", id, ErrNotFound)
			}
			return fmt.Errorf("oauth client %s is already revoked: %w", id, ErrConflict)
		}
		res, err = tx.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=?, revoked_reason='client revoked'
			WHERE client_id=? AND revoked_at IS NULL`, now, id)
		if err != nil {
			return err
		}
		ended, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE grant_id IN (SELECT id FROM oauth_grants WHERE client_id=?)`, id)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM oauth_requests WHERE client_id=?`, id)
		return err
	})
	return ended, err
}

// CreateOAuthRequest stores a checked authorisation request.
func (s *Store) CreateOAuthRequest(ctx context.Context, r *OAuthRequest) error {
	if r.ID == "" {
		r.ID = NewID(PrefixOAuthRequest)
	}
	r.CreatedAt = s.Now()
	_, err := s.exec(ctx, `INSERT INTO oauth_requests (id, client_id, redirect_uri, state, code_challenge, resource, scope,
		created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		r.ID, r.ClientID, r.RedirectURI, r.State, r.CodeChallenge, r.Resource, r.Scope, ms(r.CreatedAt), ms(r.ExpiresAt))
	return wrapWrite(err)
}

// GetOAuthRequest returns a waiting request, or ErrNotFound once it has
// expired or been decided.
func (s *Store) GetOAuthRequest(ctx context.Context, id string) (*OAuthRequest, error) {
	var r OAuthRequest
	var created, expires int64
	err := s.read.QueryRowContext(ctx, `SELECT id, client_id, redirect_uri, state, code_challenge, resource, scope,
		created_at, expires_at FROM oauth_requests WHERE id = ? AND expires_at > ?`, id, ms(s.Now())).
		Scan(&r.ID, &r.ClientID, &r.RedirectURI, &r.State, &r.CodeChallenge, &r.Resource, &r.Scope, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("oauth request %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, r.ExpiresAt = at(created), at(expires)
	return &r, nil
}

// TakeOAuthRequest removes a waiting request and reports whether this caller
// was the one that removed it, so that two clicks on Allow -- or Allow and
// Deny racing -- decide it once.
func (s *Store) TakeOAuthRequest(ctx context.Context, id string) (bool, error) {
	res, err := s.exec(ctx, `DELETE FROM oauth_requests WHERE id=? AND expires_at > ?`, id, ms(s.Now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// ApproveOAuthRequest turns a waiting request into a grant and its one
// authorisation code, in a single transaction that also removes the request,
// so a request is approved at most once.
func (s *Store) ApproveOAuthRequest(ctx context.Context, requestID string, g *OAuthGrant, code *OAuthToken) error {
	now := s.Now()
	if g.ID == "" {
		g.ID = NewID(PrefixOAuthGrant)
	}
	g.CreatedAt = now
	code.GrantID, code.Kind, code.CreatedAt = g.ID, OAuthCode, now
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM oauth_requests WHERE id=? AND expires_at > ?`, requestID, ms(now))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("oauth request %s: %w", requestID, ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO oauth_grants (id, client_id, user_id, role, scope, resource, created_ip, created_at)
			VALUES (?,?,?,?,?,?,?,?)`, g.ID, g.ClientID, g.UserID, string(g.Role), g.Scope, g.Resource, g.CreatedIP, ms(now)); err != nil {
			return err
		}
		return insertOAuthToken(ctx, tx, code)
	})
}

func insertOAuthToken(ctx context.Context, tx *sql.Tx, t *OAuthToken) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO oauth_tokens (token_hash, grant_id, kind, redirect_uri, code_challenge,
		created_at, expires_at) VALUES (?,?,?,?,?,?,?)`,
		t.TokenHash, t.GrantID, t.Kind, t.RedirectURI, t.CodeChallenge, ms(t.CreatedAt), ms(t.ExpiresAt))
	return err
}

// GetOAuthToken looks a code or token up by its hash, spent or not.
func (s *Store) GetOAuthToken(ctx context.Context, hash string) (*OAuthToken, error) {
	var t OAuthToken
	var created, expires int64
	var used sql.NullInt64
	err := s.read.QueryRowContext(ctx, `SELECT token_hash, grant_id, kind, redirect_uri, code_challenge, created_at, expires_at, used_at
		FROM oauth_tokens WHERE token_hash = ?`, hash).
		Scan(&t.TokenHash, &t.GrantID, &t.Kind, &t.RedirectURI, &t.CodeChallenge, &created, &expires, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("oauth token: %w", ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	t.CreatedAt, t.ExpiresAt, t.UsedAt = at(created), at(expires), atp(used)
	return &t, nil
}

// SpendOAuthToken exchanges a code or a refresh token for a fresh access and
// refresh pair, in one transaction that marks the old one used only if nobody
// has yet. It returns ErrConflict when the old one was already spent -- the
// caller treats that as a replay -- and ErrNotFound when it does not exist,
// has expired, or belongs to a revoked grant.
func (s *Store) SpendOAuthToken(ctx context.Context, hash string, issue ...*OAuthToken) error {
	now := s.Now()
	return s.tx(ctx, func(tx *sql.Tx) error {
		var grantID string
		var used sql.NullInt64
		var expires int64
		err := tx.QueryRowContext(ctx, `SELECT t.grant_id, t.used_at, t.expires_at FROM oauth_tokens t
			JOIN oauth_grants g ON g.id = t.grant_id
			WHERE t.token_hash = ? AND t.kind IN ('code','refresh') AND g.revoked_at IS NULL`, hash).Scan(&grantID, &used, &expires)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("oauth token: %w", ErrNotFound)
		}
		if err != nil {
			return err
		}
		if used.Valid {
			return fmt.Errorf("oauth token already used: %w", ErrConflict)
		}
		if expires <= ms(now) {
			return fmt.Errorf("oauth token expired: %w", ErrNotFound)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_tokens SET used_at=? WHERE token_hash=?`, ms(now), hash); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET last_used_at=? WHERE id=?`, ms(now), grantID); err != nil {
			return err
		}
		for _, t := range issue {
			t.GrantID, t.CreatedAt = grantID, now
			if err := insertOAuthToken(ctx, tx, t); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteOAuthToken removes one access or refresh token, for RFC 7009
// revocation of a single credential.
func (s *Store) DeleteOAuthToken(ctx context.Context, hash string) error {
	_, err := s.exec(ctx, `DELETE FROM oauth_tokens WHERE token_hash=?`, hash)
	return err
}

// RevokeOAuthGrant ends a connection and every code and token under it. It
// returns ErrNotFound for a grant that does not exist and ErrConflict for one
// that was already ended.
func (s *Store) RevokeOAuthGrant(ctx context.Context, id, reason string) error {
	now := ms(s.Now())
	return s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=?, revoked_reason=? WHERE id=? AND revoked_at IS NULL`, now, reason, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM oauth_grants WHERE id=?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("mcp connection %s: %w", id, ErrNotFound)
			}
			return fmt.Errorf("mcp connection %s is already revoked: %w", id, ErrConflict)
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE grant_id=?`, id)
		return err
	})
}

// RevokeUserOAuthGrants ends every connection one account holds, and every code
// and token under them, in one transaction. It returns the IDs it ended, so
// the caller can forget them elsewhere and say how many there were.
func (s *Store) RevokeUserOAuthGrants(ctx context.Context, userID, reason string) ([]string, error) {
	now := ms(s.Now())
	var ids []string
	err := s.tx(ctx, func(tx *sql.Tx) error {
		ids = nil
		rows, err := tx.QueryContext(ctx, `SELECT id FROM oauth_grants WHERE user_id=? AND revoked_at IS NULL ORDER BY id`, userID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE oauth_grants SET revoked_at=?, revoked_reason=? WHERE id=?`, now, reason, id); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_tokens WHERE grant_id=?`, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

const oauthGrantCols = `g.id, g.client_id, g.user_id, g.role, g.scope, g.resource, g.created_ip, g.created_at,
	g.last_used_at, g.revoked_at, g.revoked_reason, c.name, c.kind, COALESCE(u.username, '')`

const oauthGrantFrom = ` FROM oauth_grants g JOIN oauth_clients c ON c.id = g.client_id LEFT JOIN users u ON u.id = g.user_id`

func scanOAuthGrant(sc interface{ Scan(...any) error }) (*OAuthGrant, error) {
	var g OAuthGrant
	var created int64
	var used, revoked sql.NullInt64
	err := sc.Scan(&g.ID, &g.ClientID, &g.UserID, &g.Role, &g.Scope, &g.Resource, &g.CreatedIP, &created,
		&used, &revoked, &g.RevokedReason, &g.ClientName, &g.ClientKind, &g.Username)
	if err != nil {
		return nil, err
	}
	g.CreatedAt = at(created)
	g.LastUsedAt, g.RevokedAt = atp(used), atp(revoked)
	return &g, nil
}

// GetOAuthGrant returns one connection.
func (s *Store) GetOAuthGrant(ctx context.Context, id string) (*OAuthGrant, error) {
	row := s.read.QueryRowContext(ctx, `SELECT `+oauthGrantCols+oauthGrantFrom+` WHERE g.id = ?`, id)
	g, err := scanOAuthGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("mcp connection %s: %w", id, ErrNotFound)
	}
	return g, err
}

// ListOAuthGrants returns connections, newest first: one person's when userID
// is set, everybody's when it is empty. A consent whose code was never
// exchanged is not a connection yet and is left out, as are ended ones.
func (s *Store) ListOAuthGrants(ctx context.Context, userID string) ([]*OAuthGrant, error) {
	q := `SELECT ` + oauthGrantCols + oauthGrantFrom + ` WHERE g.revoked_at IS NULL AND g.last_used_at IS NOT NULL`
	var args []any
	if userID != "" {
		q += ` AND g.user_id = ?`
		args = append(args, userID)
	}
	rows, err := s.read.QueryContext(ctx, q+` ORDER BY g.created_at DESC, g.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*OAuthGrant
	for rows.Next() {
		g, err := scanOAuthGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// OAuthAccessGrant is what an access token resolves to: the token, its grant and
// the account and client behind them, in one read.
type OAuthAccessGrant struct {
	Token      OAuthToken
	Grant      OAuthGrant
	ClientName string
	ClientGone bool
	User       *User
}

// ResolveOAuthAccess finds the grant, client and account behind an access
// token's hash, or ErrNotFound when there is no such token.
func (s *Store) ResolveOAuthAccess(ctx context.Context, hash string) (*OAuthAccessGrant, error) {
	t, err := s.GetOAuthToken(ctx, hash)
	if err != nil {
		return nil, err
	}
	if t.Kind != OAuthAccess {
		return nil, fmt.Errorf("oauth token is a %s, not an access token: %w", t.Kind, ErrNotFound)
	}
	g, err := s.GetOAuthGrant(ctx, t.GrantID)
	if err != nil {
		return nil, err
	}
	c, err := s.GetOAuthClient(ctx, g.ClientID)
	if err != nil {
		return nil, err
	}
	u, err := s.GetUser(ctx, g.UserID)
	if err != nil {
		return nil, err
	}
	return &OAuthAccessGrant{Token: *t, Grant: *g, ClientName: c.Name, ClientGone: c.Revoked(), User: u}, nil
}

// TouchOAuthGrant records that a connection was just used.
func (s *Store) TouchOAuthGrant(ctx context.Context, id string, now time.Time) error {
	_, err := s.exec(ctx, `UPDATE oauth_grants SET last_used_at=? WHERE id=?`, ms(now), id)
	return err
}

// PruneOAuth removes what has expired: waiting requests, spent and unspent
// codes and tokens past their lifetime, consents whose code was never
// exchanged, and self-registered clients that never completed a sign-in.
//
// The last is the one that matters for size. A client that registers
// dynamically does so on every fresh connection, and one abandoned at the
// consent screen would otherwise stay in the clients list for good.
func (s *Store) PruneOAuth(ctx context.Context, now time.Time) (int64, error) {
	var total int64
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM oauth_requests WHERE expires_at < ?`, []any{ms(now)}},
		{`DELETE FROM oauth_tokens WHERE expires_at < ?`, []any{ms(now)}},
		{`DELETE FROM oauth_grants WHERE last_used_at IS NULL AND created_at < ?`, []any{ms(now.Add(-time.Hour))}},
		{`DELETE FROM oauth_clients WHERE kind = 'dynamic' AND last_used_at IS NULL AND created_at < ?
			AND NOT EXISTS (SELECT 1 FROM oauth_grants g WHERE g.client_id = oauth_clients.id)`, []any{ms(now.Add(-24 * time.Hour))}},
	} {
		res, err := s.exec(ctx, q.sql, q.args...)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}
