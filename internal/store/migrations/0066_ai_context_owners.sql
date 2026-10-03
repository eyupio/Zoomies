-- An installation owner may enable AI Context for that installation's
-- repositories without being a fleet administrator. Ownership grants no source
-- access: readers and connection consent stay explicit and separate.
CREATE TABLE ai_context_installation_owners (
    installation_id TEXT NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    PRIMARY KEY (installation_id, user_id)
);
CREATE INDEX idx_ai_context_owner_user ON ai_context_installation_owners(user_id);
