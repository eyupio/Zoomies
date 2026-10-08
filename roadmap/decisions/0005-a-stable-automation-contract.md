# 0005: Name the routes unattended automation may rely on, and promise their stability

**Status**: proposed. It waits for the owner to accept it, change it or
reject it.

**Date**: 2026-09-26, proposed by an implementing session.

## Context

Zoomies is increasingly provisioned and run by something other than a person
at the UI: configuration-management tools, installers, one-click templates,
and operators running many installations from one script. Each of them has
to answer the same questions without a human step, is it serving, is it
ready, has anyone claimed it, who am I, what is wrong, is it backed up, how
do I take its pools and installations elsewhere, and how do I stop it for an
upgrade. [docs/api-surface.md](../../docs/api-surface.md) documents every
route, but nothing says which of them an external caller may build on, what
may change under it, or how a change is announced.

Most of what such a caller needs already exists: the environment bootstrap
(`ZOOMIES_BOOTSTRAP_ADMIN` with `ZOOMIES_BOOTSTRAP_TOKEN_FILE`) and
`bootstrap_required` on `/readyz`; `GET /api/v1/meta`; `/api/v1/tokens`;
settings read, export and import; `/api/v1/problems`; the diagnostics bundle;
`/api/v1/usage` with `history_from`; the per-installation report; backups;
installation export, import and purge; pools export and import; and the
recovery fence. Reading them as a caller with no UI found two gaps:

* There is no `GET /api/v1/backups/remotes`. The remotes are carried inside
  `GET /api/v1/backups`, and a request for the missing route is matched by
  `/backups/{id}` and answered with a 400 about the id's shape rather than a
  404.
* `/readyz` gives no machine-readable reason for a 503. A fenced instance
  says `fenced: true`; a database that is not answering says only a prose
  `message`, so a caller that wants to act differently on each has to infer
  one from the absence of the other.

## Decision

Publish [docs/automation.md](../../docs/automation.md) as the automation
contract: the routes, environment variables and signals listed there are
stable for an unattended caller within a major version. Within one, a listed
route keeps its path, method, required role and every documented field and
meaning; new optional fields and new routes may appear, and a caller must
ignore fields it does not know. A listed item is removed or changed
incompatibly only after it has been marked deprecated in the release notes,
in `docs/automation.md` and, where it is a route, in `api/openapi.yaml`, for
at least one minor release and ninety days, whichever is longer, with its
replacement already shipped. Anything not listed there carries no such
promise, however long it has existed.

## Consequences

* Every change to a listed route is reviewed against the page as well as the
  spec, and a deprecation is a change to three files, not one.
* The two gaps above, and the open questions on the contract page, are
  settled before the contract is accepted, or accepted as known limits and
  written there.
* Routes the UI alone relies on stay free to change, which is the point of
  listing rather than promising everything.
