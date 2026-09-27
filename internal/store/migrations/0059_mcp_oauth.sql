-- The controller as its own OAuth 2.1 authorisation server for /mcp, so that
-- an MCP client -- Claude among them -- can be added by URL and signed in
-- through the browser rather than handed a pasted API token.
--
-- Nothing here is a credential in the clear: client secrets, authorisation
-- codes, access tokens and refresh tokens are all stored as SHA-256 hashes,
-- exactly as API tokens are, so a copy of the database is not a way in.

-- A client that may ask for a grant. kind says how it came to exist:
-- 'dynamic' registered itself (RFC 7591), 'metadata' is a Client ID Metadata
-- Document whose client_id is the https URL it was fetched from, and 'admin'
-- was created by an administrator for a client that is given its ID and
-- secret by hand. secret_hash is empty for a public client, which proves
-- itself with PKCE alone. A revoked client keeps its row so that the audit
-- trail and the connections list can still name it.
CREATE TABLE oauth_clients (
    id                TEXT PRIMARY KEY,
    client_id         TEXT    NOT NULL UNIQUE,
    kind              TEXT    NOT NULL CHECK (kind IN ('dynamic','metadata','admin')),
    name              TEXT    NOT NULL,
    client_uri        TEXT    NOT NULL DEFAULT '',
    redirect_uris     TEXT    NOT NULL DEFAULT '[]',
    secret_hash       TEXT    NOT NULL DEFAULT '',
    secret_prefix     TEXT    NOT NULL DEFAULT '',
    secret_rotated_at INTEGER,
    created_by        TEXT    NOT NULL DEFAULT '',
    created_ip        TEXT    NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    last_used_at      INTEGER,
    revoked_at        INTEGER
);

-- An authorisation request that has been checked and is waiting for the
-- person to sign in and decide. It lives in the database rather than in the
-- browser so that nothing the consent page is shown -- the client, the
-- redirect, the resource -- can be edited on its way there.
CREATE TABLE oauth_requests (
    id             TEXT PRIMARY KEY,
    client_id      TEXT    NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    redirect_uri   TEXT    NOT NULL,
    state          TEXT    NOT NULL DEFAULT '',
    code_challenge TEXT    NOT NULL,
    resource       TEXT    NOT NULL,
    scope          TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL
);
CREATE INDEX idx_oauth_requests_expiry ON oauth_requests(expires_at);

-- A connection: one person's consent for one client, at a role no higher than
-- their own. Every code and token descends from exactly one, so revoking it --
-- by the person, by an administrator, or because a refresh token was replayed
-- -- ends the whole family at once.
CREATE TABLE oauth_grants (
    id             TEXT PRIMARY KEY,
    client_id      TEXT    NOT NULL REFERENCES oauth_clients(id) ON DELETE CASCADE,
    user_id        TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role           TEXT    NOT NULL,
    scope          TEXT    NOT NULL DEFAULT '',
    resource       TEXT    NOT NULL,
    created_ip     TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    last_used_at   INTEGER,
    revoked_at     INTEGER,
    revoked_reason TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX idx_oauth_grants_user ON oauth_grants(user_id);
CREATE INDEX idx_oauth_grants_client ON oauth_grants(client_id);

-- Authorisation codes, access tokens and refresh tokens, one table because
-- they share a shape and a fate. A code or refresh token that has been spent
-- keeps its row with used_at set until it expires: that is what lets a second
-- presentation be recognised as a replay, which revokes the grant, rather
-- than answered as a token nobody has heard of.
CREATE TABLE oauth_tokens (
    token_hash     TEXT PRIMARY KEY,
    grant_id       TEXT    NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL CHECK (kind IN ('code','access','refresh')),
    redirect_uri   TEXT    NOT NULL DEFAULT '',
    code_challenge TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    used_at        INTEGER
);
CREATE INDEX idx_oauth_tokens_grant ON oauth_tokens(grant_id);
CREATE INDEX idx_oauth_tokens_expiry ON oauth_tokens(expires_at);
