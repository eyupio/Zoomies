-- Persist the reviewed proposal before crossing GitHub's write boundary.
-- A lost response resumes the same head rather than opening another PR.
CREATE TABLE ai_context_setups (
    repository_id TEXT PRIMARY KEY REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
    revision INTEGER NOT NULL,
    plan_hash TEXT NOT NULL,
    plan_json TEXT NOT NULL,
    state TEXT NOT NULL CHECK(state IN ('pending','awaiting_merge')),
    pr_number INTEGER NOT NULL DEFAULT 0,
    pr_url TEXT NOT NULL DEFAULT '',
    lease_token TEXT NOT NULL DEFAULT '',
    lease_until INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
