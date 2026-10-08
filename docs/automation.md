---
icon: material/file-document-outline
title: Automating a self-hosted Zoomies runner fleet
description: >-
  Install, configure, monitor and back up a Zoomies runner fleet from scripts.
  Read the proposed automation contract and its current stability limits.
---

# Automation contract

Zoomies can be installed, claimed, watched, backed up and retired with no
person at the UI. This page lists what a tool doing that (a
configuration-management module, an installer, an operator running many
installations from one script) may rely on. It is **proposed** in
[decision 0005](https://github.com/eyupio/zoomies/blob/main/roadmap/decisions/0005-a-stable-automation-contract.md)
and not yet a promise; until it is accepted, treat it as the list the promise
will cover.

## Stability

Within a major version, everything listed here keeps its path, method,
required role and every documented field and meaning. New optional fields
and new routes may appear; ignore fields you do not know. A listed item is
changed incompatibly or removed only after it has been marked deprecated
(in the release notes, on this page and, for a route, in `api/openapi.yaml`)
for at least one minor release and ninety days, whichever is longer, with its
replacement already shipped. Anything not on this page carries no such
promise. Every route is described in full in the [API reference](api-surface.md).

## What you can rely on

| Need | Surface |
| --- | --- |
| Know the process is serving | `GET /healthz`: 200 once serving. The container health check. |
| Know it is ready: migrated, database answering, not held for recovery | `GET /readyz`: 503 when not; `schema.applied` and `schema.latest` are the schema version. |
| Know whether anyone has claimed it | `bootstrap_required` on `/readyz`, and on `GET /api/v1/meta` with the version. |
| Create the first account with no human step | `ZOOMIES_BOOTSTRAP_ADMIN` with `ZOOMIES_BOOTSTRAP_TOKEN_FILE`; see [configuration](configuration.md). Remove both once the instance is claimed. |
| Mint and revoke API tokens | `GET`, `POST /api/v1/tokens`, `DELETE /api/v1/tokens/{id}`. Rotate by minting the new token, switching, then revoking the old. |
| Read and change settings | `GET`, `PATCH /api/v1/settings`; `GET /api/v1/settings/export` and `POST /api/v1/settings/import` with `dry_run`. |
| Know what is wrong | `GET /api/v1/problems`; every code is in [problem codes](problem-codes.md). |
| Collect a support bundle | `GET /api/v1/diagnostics/bundle`. |
| Read usage for a window | `GET /api/v1/usage` and `/usage.csv`; `history_from` says where the figures begin. |
| Back up, and check a remote holds the copy | `GET /api/v1/backups` (the list, the schedule and the remotes), `GET /api/v1/backups/remotes/{name}/copies`; see [backup and restore](backup-and-restore.md). |
| Move or retire an installation | `POST /api/v1/installations/{id}/export`, `POST /api/v1/installations/import`, `DELETE /api/v1/installations/{id}?purge=true`. |
| Move pools between instances | `GET /api/v1/pools/export`, `POST /api/v1/pools/import` with `dry_run`. |
| Know whether it is held for recovery, and lift it | `GET /api/v1/recovery`, `POST /api/v1/recovery/unfence`. |
| Stop it for an upgrade | `SIGTERM`; see [upgrading](upgrading.md). |

## Open questions

These are gaps an unattended caller meets today. The contract is not
accepted until each is closed or written here as a known limit.

- There is no `GET /api/v1/backups/remotes`. The remotes are read from
  `GET /api/v1/backups`; a request for the missing route is matched as a
  backup id and answered with a 400 rather than a 404.
- `/readyz` gives no machine-readable reason for a 503. A held instance
  carries `fenced: true`; a database that is not answering carries only a
  prose `message`. A stable `reason` field would let a caller act on each
  without reading English.
- Rotating an API token is three calls, not one audited action, so there is
  a window with two valid tokens and no record tying them together.
- On a new instance `history_from.runners` starts at the runner retention
  window, so a usage window longer than `retention.runners` is incomplete
  from the first day.
- What a `SIGTERM` waits for, and for how long, is not stated as a number an
  orchestrator can set its stop timeout from.
