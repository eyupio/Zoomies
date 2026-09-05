ALTER TABLE pools ADD COLUMN pull_policy TEXT NOT NULL DEFAULT 'if-not-present'
  CHECK (pull_policy IN ('if-not-present','always','pinned-only'));
ALTER TABLE runners ADD COLUMN image_digest TEXT NOT NULL DEFAULT '';

CREATE TABLE pool_prewarms (
  pool_id TEXT NOT NULL REFERENCES pools(id) ON DELETE CASCADE,
  host_id TEXT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  image TEXT NOT NULL,
  state TEXT NOT NULL CHECK (state IN ('pending','succeeded','failed')),
  digest TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', updated_at INTEGER NOT NULL,
  PRIMARY KEY(pool_id, host_id)
);
