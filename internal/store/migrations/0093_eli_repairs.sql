-- Preserve existing API and subscription ownership while separating personal defaults.
CREATE TABLE assistant_providers_personal (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    kind TEXT NOT NULL,
    base_url TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    key_enc BLOB,
    enabled INTEGER NOT NULL DEFAULT 1,
    fleet_access INTEGER NOT NULL DEFAULT 0,
    is_default INTEGER NOT NULL DEFAULT 0,
    last_check TEXT,
    last_checked_at INTEGER,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(owner_id, name)
);
INSERT INTO assistant_providers_personal SELECT id, owner_id, name, kind, base_url, model,
    key_enc, enabled, fleet_access, is_default, last_check, last_checked_at, created_at, updated_at
    FROM assistant_providers;
DROP TABLE assistant_providers;
ALTER TABLE assistant_providers_personal RENAME TO assistant_providers;
CREATE TRIGGER assistant_personal_owner_deleted AFTER DELETE ON users
BEGIN
    DELETE FROM assistant_providers WHERE owner_id = OLD.id;
END;
CREATE UNIQUE INDEX assistant_providers_one_default ON assistant_providers(owner_id) WHERE is_default = 1;

-- An administrator verifies the account link; a commenter cannot claim somebody else's key.
CREATE TABLE eli_github_identities (
    user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    github_user_id INTEGER NOT NULL UNIQUE,
    github_login TEXT NOT NULL,
    confirmed INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
CREATE TABLE eli_repair_policies (
    repo TEXT PRIMARY KEY,
    installation_id TEXT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL REFERENCES assistant_providers(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 0,
    automatic INTEGER NOT NULL DEFAULT 0,
    allow_workflows INTEGER NOT NULL DEFAULT 0,
    daily_limit INTEGER NOT NULL DEFAULT 5 CHECK(daily_limit BETWEEN 1 AND 50),
    updated_at INTEGER NOT NULL
);
CREATE TABLE eli_repairs (
    id TEXT PRIMARY KEY,
    dedup_key TEXT NOT NULL UNIQUE,
    installation_id TEXT NOT NULL,
    repo TEXT NOT NULL,
    pull_number INTEGER NOT NULL DEFAULT 0,
    job_id INTEGER NOT NULL DEFAULT 0,
    run_id INTEGER NOT NULL DEFAULT 0,
    head_sha TEXT NOT NULL DEFAULT '',
    commit_sha TEXT NOT NULL DEFAULT '',
    user_id TEXT NOT NULL DEFAULT '',
    github_user_id INTEGER NOT NULL DEFAULT 0,
    github_login TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    trigger TEXT NOT NULL,
    requester_admin INTEGER NOT NULL DEFAULT 0,
    instruction TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'queued',
    message TEXT NOT NULL DEFAULT '',
    comment_id INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX eli_repairs_queue ON eli_repairs(state, created_at);
