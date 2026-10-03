-- The IDs of GitHub Actions OIDC tokens already used for a Zoomies-only
-- upload. A token is valid for minutes and an upload is not idempotent, so
-- each ID is admitted once; rows are pruned after the token has expired.
CREATE TABLE ai_context_upload_tokens (
    token_id TEXT PRIMARY KEY,
    repository_id TEXT NOT NULL REFERENCES ai_context_repositories(id) ON DELETE CASCADE,
    expires_at INTEGER NOT NULL
);
CREATE INDEX idx_ai_context_upload_tokens_expiry ON ai_context_upload_tokens(expires_at);
