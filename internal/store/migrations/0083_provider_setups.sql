CREATE TABLE provider_setups (
    id TEXT PRIMARY KEY,
    token_hash BLOB NOT NULL,
    expires_at INTEGER NOT NULL,
    payload_enc BLOB
);
