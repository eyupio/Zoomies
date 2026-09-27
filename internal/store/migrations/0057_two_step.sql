-- Optional two-step verification (TOTP) for local password accounts.
--
-- Everything lives in tables of its own rather than in columns on users: an
-- account that never turns it on carries no row, the upgrade touches no
-- existing row, and deleting an account takes its second factor with it.

-- The authenticator secret, sealed with the instance key. A row whose
-- enabled_at is NULL is an enrolment somebody started and has not yet
-- confirmed with a code; it asks nothing of them at sign-in. last_step is the
-- most recent 30-second time step accepted, so a code seen once -- over a
-- shoulder, in a phishing proxy's log -- is refused the second time.
CREATE TABLE user_two_step (
    user_id    TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    secret_enc BLOB    NOT NULL,
    enabled_at INTEGER,
    last_step  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

-- Single-use recovery codes, stored as SHA-256 hashes. A spent code keeps its
-- row, with used_at set, so the account page can say how many are left.
CREATE TABLE user_recovery_codes (
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    code_hash  TEXT    NOT NULL,
    used_at    INTEGER,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (user_id, code_hash)
);

-- A sign-in that has passed the password and is waiting for its second step:
-- a code ('verify'), or enrolment when the instance requires two-step and the
-- account has none yet ('enrol'). It is keyed on the hash of a cookie that
-- only the browser which typed the password holds, and it expires in minutes.
CREATE TABLE sign_in_challenges (
    token_hash TEXT PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    purpose    TEXT    NOT NULL CHECK (purpose IN ('verify','enrol')),
    ip         TEXT    NOT NULL DEFAULT '',
    user_agent TEXT    NOT NULL DEFAULT '',
    failures   INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_sign_in_challenges_expiry ON sign_in_challenges(expires_at);
