-- Kennel Club tracks every repository it has a row for, unless somebody has said
-- it should not look at one. This is where that is written down.
--
-- A row here is a repository that is not tracked, and no row means tracked, so a
-- controller that never uses the switch behaves exactly as it did, and an older
-- release reading a newer database finds kennel_repositories as it knew it.
--
-- It is a table of its own, and not a column, for the same reason the waivers
-- are: it records a decision, and a decision carries who made it, when and why.
-- reason is the same length as a waiver's (ten to five hundred characters, held
-- by the controller) and there is no end date, because tracking is a state and
-- not a decision to be renewed. Stopping silences errors, so the person who did
-- it is named beside the switch for whoever finds the repository quiet.
--
-- The row goes when the repository's does through the installation, but not when
-- the ninety days run out: the prune leaves an untracked repository alone, so a
-- sandbox that was quiet for a quarter does not come back tracked, and read, the
-- day somebody pushes to it.
CREATE TABLE kennel_untracked (
    repository_pk    TEXT    PRIMARY KEY REFERENCES kennel_repositories(id) ON DELETE CASCADE,
    reason           TEXT    NOT NULL,
    created_by       TEXT    NOT NULL,
    created_by_name  TEXT    NOT NULL,
    created_at       INTEGER NOT NULL
);
