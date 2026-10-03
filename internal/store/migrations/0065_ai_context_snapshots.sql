-- Bounded source blobs share the existing transactional backup and deletion
-- lifecycle. No separate filesystem copy can go missing during restoration.
CREATE TABLE ai_context_snapshots (
 repository_id TEXT NOT NULL REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
 digest TEXT NOT NULL,
 source_commit TEXT NOT NULL,
 config_hash TEXT NOT NULL,
 body BLOB NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(repository_id,digest)
);
CREATE INDEX idx_ai_context_snapshot_latest ON ai_context_snapshots(repository_id,created_at DESC);
CREATE TABLE ai_context_freshness (
 repository_id TEXT PRIMARY KEY REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
 state TEXT NOT NULL,
 desired_commit TEXT NOT NULL DEFAULT '',
 published_commit TEXT NOT NULL DEFAULT '',
 digest TEXT NOT NULL DEFAULT '',
 checked_at INTEGER NOT NULL,
 failure TEXT NOT NULL DEFAULT ''
);
