-- Notes an assistant writes about a repository: reports and plans, versioned,
-- attributed and kept apart from the source they describe. Readers of the
-- repository read them; publishing needs membership and, for an MCP
-- connection, its owner's separate consent below. Source consent alone never
-- lets a connection write.
ALTER TABLE ai_context_connection_repositories ADD COLUMN publish INTEGER NOT NULL DEFAULT 0 CHECK(publish IN (0,1));

CREATE TABLE ai_context_artifacts (
    id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
    slug TEXT NOT NULL,
    version INTEGER NOT NULL CHECK(version > 0),
    kind TEXT NOT NULL CHECK(kind IN ('report','plan','note')),
    title TEXT NOT NULL,
    body TEXT NOT NULL,
    -- The verified commit the repository's context was at when this was
    -- published: what the author could have read, not a claim it makes.
    source_commit TEXT NOT NULL DEFAULT '',
    author_user_id TEXT REFERENCES users(id) ON DELETE SET NULL,
    author_name TEXT NOT NULL,
    via_kind TEXT NOT NULL CHECK(via_kind IN ('user','token','connection')),
    via_name TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE(repository_id, slug, version)
);
CREATE INDEX idx_ai_context_artifacts_latest ON ai_context_artifacts(repository_id, slug, version DESC);
