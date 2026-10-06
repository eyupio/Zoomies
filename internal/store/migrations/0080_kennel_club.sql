-- Kennel Club: what this fleet has worked out about each repository it serves,
-- and the decisions a person has recorded about it.
--
-- Two tables, both new, so nothing existing is rebuilt and an older release
-- reading a newer database finds the tables it knows exactly as they were.
--
-- kennel_repositories holds the last evaluation of one repository, not the
-- material it was made from. The raw facts -- a workflow's text, a setting's
-- value -- are never kept: they are untrusted, and an evaluation is a few
-- hundred bytes of findings where the facts would be a copy of somebody's
-- repository. What is kept is what lets the page render the moment the
-- controller restarts, lets "publish only on change" have a baseline, and lets
-- the run history the checks read resume where it stopped instead of reading
-- it again.
--
-- evaluation_json is the whole of kennel.Evaluation, and coverage_json says how
-- far each source could be read. The four open_* and waived counts are the same
-- findings counted, kept as columns so the list can be sorted and filtered
-- without parsing a document per row. watermark_json is the controller's own:
-- the highest run it has examined and the few runs that mattered.
--
-- A repository is identified by GitHub's own numeric ID on its host, never its
-- name: a rename must not lose its waivers. installation_id is only who reads
-- it now, and moves if a repository installation takes over from an
-- organisation one. A row is kept for ninety days after the repository was
-- last served, so a quiet repository keeps its waivers; the controller prunes
-- it after that.
CREATE TABLE kennel_repositories (
    id                TEXT    PRIMARY KEY,
    github_host       TEXT    NOT NULL,
    repository_id     INTEGER NOT NULL,
    installation_id   TEXT    NOT NULL REFERENCES installations(id) ON DELETE CASCADE,
    full_name         TEXT    NOT NULL,
    visibility        TEXT    NOT NULL DEFAULT '',
    state             TEXT    NOT NULL DEFAULT 'pending',
    evaluator_version INTEGER NOT NULL DEFAULT 0,
    evaluated_at      INTEGER,
    next_due_at       INTEGER NOT NULL DEFAULT 0,
    inputs_digest     TEXT    NOT NULL DEFAULT '',
    coverage_json     TEXT    NOT NULL DEFAULT '{}',
    evaluation_json   TEXT    NOT NULL DEFAULT '{}',
    watermark_json    TEXT    NOT NULL DEFAULT '{}',
    open_errors       INTEGER NOT NULL DEFAULT 0,
    open_warnings     INTEGER NOT NULL DEFAULT 0,
    open_infos        INTEGER NOT NULL DEFAULT 0,
    waived            INTEGER NOT NULL DEFAULT 0,
    last_served_at    INTEGER NOT NULL,
    UNIQUE (github_host, repository_id)
);
CREATE INDEX idx_kennel_repositories_due ON kennel_repositories(next_due_at);
CREATE INDEX idx_kennel_repositories_installation ON kennel_repositories(installation_id);

-- A waiver is a person saying "I have looked at this, and it is acceptable
-- here". It is not a dismissal: a dismissal hides a notification from one
-- account, and a waiver changes what the fleet's record says about a
-- repository, so it carries who made it, why, and when it ends.
--
-- expires_at is NOT NULL on purpose: a decision that never ends is a decision
-- nobody is asked to make again. severity is the finding's severity when it was
-- waived, so that a finding which has since got worse is not covered by a
-- decision about a milder one. (repository, code, subject) is unique; waiving
-- the same finding again renews the one waiver rather than stacking a second.
CREATE TABLE kennel_waivers (
    id              TEXT    PRIMARY KEY,
    repository_pk   TEXT    NOT NULL REFERENCES kennel_repositories(id) ON DELETE CASCADE,
    code            TEXT    NOT NULL,
    subject         TEXT    NOT NULL DEFAULT '',
    severity        TEXT    NOT NULL,
    reason          TEXT    NOT NULL,
    created_by      TEXT    NOT NULL,
    created_by_name TEXT    NOT NULL,
    created_at      INTEGER NOT NULL,
    expires_at      INTEGER NOT NULL,
    UNIQUE (repository_pk, code, subject)
);
