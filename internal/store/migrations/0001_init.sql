-- Zoomies initial schema.
--
-- Conventions used throughout:
--   * Timestamps are INTEGER Unix milliseconds (0 / NULL means "unset").
--   * Enumerations are TEXT with a CHECK constraint, so a corrupt write fails
--     at the database rather than silently poisoning the scheduler.
--   * Secrets are BLOB columns holding AES-256-GCM ciphertext; the schema never
--     stores a plaintext credential.

CREATE TABLE installations (
    id                  TEXT PRIMARY KEY,
    app_id              INTEGER NOT NULL,
    installation_id     INTEGER NOT NULL,
    target              TEXT    NOT NULL,
    target_type         TEXT    NOT NULL CHECK (target_type IN ('org','repo')),
    api_base_url        TEXT    NOT NULL DEFAULT '',
    upload_base_url     TEXT    NOT NULL DEFAULT '',
    private_key_enc     BLOB,
    webhook_secret_enc  BLOB,
    app_slug            TEXT    NOT NULL DEFAULT '',
    last_checked_at     INTEGER,
    last_error          TEXT    NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_installations_target ON installations(app_id, installation_id, target);

CREATE TABLE pools (
    id               TEXT PRIMARY KEY,
    name             TEXT    NOT NULL,
    installation_id  TEXT    NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    labels           TEXT    NOT NULL DEFAULT '[]',
    runner_group     TEXT    NOT NULL DEFAULT '',
    backend          TEXT    NOT NULL CHECK (backend IN ('docker','podman','process')),
    image            TEXT    NOT NULL DEFAULT '',
    runner_version   TEXT    NOT NULL DEFAULT '',
    min_runners      INTEGER NOT NULL DEFAULT 0,
    max_runners      INTEGER NOT NULL DEFAULT 1,
    idle_timeout_ms  INTEGER NOT NULL DEFAULT 300000,
    ephemeral        INTEGER NOT NULL DEFAULT 1,
    docker_mode      TEXT    NOT NULL DEFAULT 'none' CHECK (docker_mode IN ('none','dind','host-socket')),
    resources        TEXT    NOT NULL DEFAULT '{}',
    host_selector    TEXT    NOT NULL DEFAULT '{}',
    env              TEXT    NOT NULL DEFAULT '{}',
    run_as_root      INTEGER NOT NULL DEFAULT 0,
    enabled          INTEGER NOT NULL DEFAULT 1,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK (min_runners >= 0),
    CHECK (max_runners >= min_runners)
);
CREATE UNIQUE INDEX idx_pools_name ON pools(name);

CREATE TABLE hosts (
    id              TEXT PRIMARY KEY,
    name            TEXT    NOT NULL,
    address         TEXT    NOT NULL DEFAULT '',
    embedded        INTEGER NOT NULL DEFAULT 0,
    capacity        INTEGER NOT NULL DEFAULT 1,
    backends        TEXT    NOT NULL DEFAULT '[]',
    labels          TEXT    NOT NULL DEFAULT '{}',
    os              TEXT    NOT NULL DEFAULT '',
    arch            TEXT    NOT NULL DEFAULT '',
    version         TEXT    NOT NULL DEFAULT '',
    cordoned        INTEGER NOT NULL DEFAULT 0,
    token_hash      TEXT    NOT NULL DEFAULT '',
    last_heartbeat  INTEGER NOT NULL DEFAULT 0,
    created_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_hosts_name ON hosts(name);

CREATE TABLE runners (
    id               TEXT PRIMARY KEY,
    pool_id          TEXT    NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
    host_id          TEXT    NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
    name             TEXT    NOT NULL,
    state            TEXT    NOT NULL CHECK (state IN
                         ('provisioning','registering','idle','busy','draining','removed','failed')),
    github_runner_id INTEGER NOT NULL DEFAULT 0,
    container_id     TEXT    NOT NULL DEFAULT '',
    ephemeral        INTEGER NOT NULL DEFAULT 1,
    labels           TEXT    NOT NULL DEFAULT '[]',
    image            TEXT    NOT NULL DEFAULT '',
    runner_version   TEXT    NOT NULL DEFAULT '',
    current_job_id   TEXT    NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL,
    started_at       INTEGER,
    last_idle_at     INTEGER,
    finished_at      INTEGER,
    message          TEXT    NOT NULL DEFAULT '',
    jobs_handled     INTEGER NOT NULL DEFAULT 0,
    cpu_percent      REAL    NOT NULL DEFAULT 0,
    memory_bytes     INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_runners_name ON runners(name);
CREATE INDEX idx_runners_pool_state ON runners(pool_id, state);
CREATE INDEX idx_runners_host_state ON runners(host_id, state);
CREATE INDEX idx_runners_created ON runners(created_at DESC);

CREATE TABLE jobs (
    id             TEXT PRIMARY KEY,
    github_job_id  INTEGER NOT NULL,
    github_run_id  INTEGER NOT NULL DEFAULT 0,
    repo           TEXT    NOT NULL DEFAULT '',
    workflow       TEXT    NOT NULL DEFAULT '',
    job_name       TEXT    NOT NULL DEFAULT '',
    labels         TEXT    NOT NULL DEFAULT '[]',
    state          TEXT    NOT NULL CHECK (state IN ('queued','in_progress','completed')),
    conclusion     TEXT    NOT NULL DEFAULT '',
    pool_id        TEXT    NOT NULL DEFAULT '',
    runner_id      TEXT    NOT NULL DEFAULT '',
    runner_name    TEXT    NOT NULL DEFAULT '',
    html_url       TEXT    NOT NULL DEFAULT '',
    queued_at      INTEGER NOT NULL,
    started_at     INTEGER,
    completed_at   INTEGER,
    matched        INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX idx_jobs_github ON jobs(github_job_id);
CREATE INDEX idx_jobs_state_queued ON jobs(state, queued_at);
CREATE INDEX idx_jobs_pool ON jobs(pool_id, queued_at DESC);
CREATE INDEX idx_jobs_repo ON jobs(repo, queued_at DESC);
CREATE INDEX idx_jobs_queued_at ON jobs(queued_at DESC);

CREATE TABLE audit_events (
    id          TEXT PRIMARY KEY,
    actor_id    TEXT    NOT NULL DEFAULT '',
    actor_name  TEXT    NOT NULL DEFAULT '',
    actor_kind  TEXT    NOT NULL DEFAULT 'system',
    action      TEXT    NOT NULL,
    target_kind TEXT    NOT NULL DEFAULT '',
    target_id   TEXT    NOT NULL DEFAULT '',
    before      TEXT    NOT NULL DEFAULT '',
    after       TEXT    NOT NULL DEFAULT '',
    ip          TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_audit_created ON audit_events(created_at DESC);
CREATE INDEX idx_audit_target ON audit_events(target_kind, target_id, created_at DESC);
CREATE INDEX idx_audit_actor ON audit_events(actor_id, created_at DESC);

CREATE TABLE scaling_events (
    id         TEXT PRIMARY KEY,
    pool_id    TEXT    NOT NULL DEFAULT '',
    pool_name  TEXT    NOT NULL DEFAULT '',
    from_count INTEGER NOT NULL,
    to_count   INTEGER NOT NULL,
    reason     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_scaling_created ON scaling_events(created_at DESC);
CREATE INDEX idx_scaling_pool ON scaling_events(pool_id, created_at DESC);

CREATE TABLE webhook_deliveries (
    id          TEXT PRIMARY KEY,
    delivery_id TEXT    NOT NULL DEFAULT '',
    event       TEXT    NOT NULL DEFAULT '',
    action      TEXT    NOT NULL DEFAULT '',
    repo        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'accepted',
    error       TEXT    NOT NULL DEFAULT '',
    received_at INTEGER NOT NULL
);
CREATE INDEX idx_webhook_received ON webhook_deliveries(received_at DESC);
CREATE INDEX idx_webhook_status ON webhook_deliveries(status, received_at DESC);

CREATE TABLE users (
    id                   TEXT PRIMARY KEY,
    username             TEXT    NOT NULL,
    email                TEXT    NOT NULL DEFAULT '',
    display_name         TEXT    NOT NULL DEFAULT '',
    role                 TEXT    NOT NULL CHECK (role IN ('viewer','operator','admin')),
    password_hash        TEXT    NOT NULL DEFAULT '',
    oidc_subject         TEXT    NOT NULL DEFAULT '',
    disabled             INTEGER NOT NULL DEFAULT 0,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    created_at           INTEGER NOT NULL,
    last_login_at        INTEGER
);
CREATE UNIQUE INDEX idx_users_username ON users(username);
CREATE INDEX idx_users_oidc ON users(oidc_subject);

CREATE TABLE sessions (
    id         TEXT PRIMARY KEY,
    user_id    TEXT    NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash TEXT    NOT NULL,
    user_agent TEXT    NOT NULL DEFAULT '',
    ip         TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX idx_sessions_token ON sessions(token_hash);
CREATE INDEX idx_sessions_user ON sessions(user_id);

CREATE TABLE api_tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT    NOT NULL,
    role         TEXT    NOT NULL CHECK (role IN ('viewer','operator','admin')),
    user_id      TEXT    NOT NULL DEFAULT '',
    scopes       TEXT    NOT NULL DEFAULT '[]',
    token_hash   TEXT    NOT NULL,
    prefix       TEXT    NOT NULL DEFAULT '',
    revoked      INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER,
    last_used_at INTEGER
);
CREATE UNIQUE INDEX idx_api_tokens_hash ON api_tokens(token_hash);

CREATE TABLE join_tokens (
    id         TEXT PRIMARY KEY,
    token_hash TEXT    NOT NULL,
    prefix     TEXT    NOT NULL DEFAULT '',
    created_by TEXT    NOT NULL DEFAULT '',
    labels     TEXT    NOT NULL DEFAULT '{}',
    capacity   INTEGER NOT NULL DEFAULT 2,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER,
    used_by_id TEXT    NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX idx_join_tokens_hash ON join_tokens(token_hash);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT    NOT NULL DEFAULT '',
    secret     INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);

-- Rolling per-minute fleet samples that back the Overview sparklines. Old rows
-- are pruned by the controller so this table stays bounded.
CREATE TABLE fleet_samples (
    at             INTEGER PRIMARY KEY,
    queued_jobs    INTEGER NOT NULL DEFAULT 0,
    running_jobs   INTEGER NOT NULL DEFAULT 0,
    idle_runners   INTEGER NOT NULL DEFAULT 0,
    busy_runners   INTEGER NOT NULL DEFAULT 0,
    total_runners  INTEGER NOT NULL DEFAULT 0
);
