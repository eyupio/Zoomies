-- Source access is an explicit opt-in, independent of fleet viewer authority.
-- No existing user, token or OAuth connection gains a source grant here.
CREATE TABLE ai_context_repositories (
    id TEXT PRIMARY KEY,
    installation_id TEXT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    github_host TEXT NOT NULL,
    repository_id INTEGER NOT NULL CHECK(repository_id > 0),
    full_name TEXT NOT NULL,
    config_json TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1 CHECK(revision > 0),
    available INTEGER NOT NULL DEFAULT 0 CHECK(available IN (0,1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    UNIQUE(github_host, installation_id, repository_id)
);
CREATE INDEX idx_ai_context_installation ON ai_context_repositories(installation_id);

CREATE TABLE ai_context_members (
    repository_id TEXT NOT NULL REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY(repository_id, user_id)
);
CREATE INDEX idx_ai_context_members_user ON ai_context_members(user_id, repository_id);

-- A person's membership and their consent for this particular connection must
-- both exist. Restoring membership must not restore a consent they revoked.
CREATE TABLE ai_context_connection_repositories (
    grant_id TEXT NOT NULL REFERENCES oauth_grants(id) ON DELETE CASCADE,
    repository_id TEXT NOT NULL REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
    PRIMARY KEY(grant_id, repository_id)
);
