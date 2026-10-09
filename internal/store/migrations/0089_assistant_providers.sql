-- The assistant's providers: which models the controller may talk to.
--
-- One row per provider rather than assistant.* settings keys, because the
-- page shows them as cards with a default, a check result and later their own
-- data classes, and a settings row cannot hold a list. The key is sealed with
-- the instance key and written by its own setter, as a Proxmox credential is.
-- last_check is the JSON the check returned, opaque to the store; the partial
-- unique index is what makes "at most one default" a fact the database holds
-- rather than one every writer has to remember.
CREATE TABLE assistant_providers (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    kind            TEXT NOT NULL,
    base_url        TEXT NOT NULL DEFAULT '',
    model           TEXT NOT NULL DEFAULT '',
    key_enc         BLOB,
    enabled         INTEGER NOT NULL DEFAULT 1,
    is_default      INTEGER NOT NULL DEFAULT 0,
    last_check      TEXT,
    last_checked_at INTEGER,
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX assistant_providers_one_default ON assistant_providers (is_default) WHERE is_default = 1;
