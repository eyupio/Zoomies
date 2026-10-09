# Hand-off: in-product updating (ZF-232), continuing in a new session

Written 9 October 2026 (afternoon, UTC). Read this first, then `roadmap/in-product-updates.md` (the design), `roadmap/decisions/0011-*` (the privilege decision) and `roadmap/in-product-updates-plan.md` (the 31-task plan). The detailed ledger of every ruling is `hand-off-auto-update-ledger.md` beside this file's committed copy (branch `claude/zf232-handoff`), because the working ledger lives in `.superpowers/` which is git-ignored and does not survive a container.

## What this is

Zoomies can now update itself from the web UI, in three modes (`updates.mode`: `off | manual | auto`). A root-owned systemd path unit plus oneshot service (the "update helper", installed by the host's owner) runs `zoomies upgrade --version <tag> --non-interactive --config-dir <dir>` for a validated `request.json` that the unprivileged service writes into an "update folder". The controller updates itself (platform role), updates remote hosts through their agents (admin), and, in `auto`, a pure planner decides one step at a time.

## State of play

Merged into `main` (all merged by the owner, jnnngs, not by me):

* Part 1 (#719, #734): settings, status view, release list, read-only Settings → Updates panel.
* Part 2 (#743): the helper (wire documents, hostile request reader, units, install/remove, offers at upgrade/init/join).
* Part 3 (#757, follow-up #759): `update_attempts` table (migration 0086), the controller updates itself, routes `POST /api/v1/updates/check` (admin) and `/updates/controller` (platform), problems, CLI `zoomies updates status|check|apply`, the Update button.
* Part 4 (#786, follow-ups #789): agents write the helper request and report back; the controller updates a host (`POST /api/v1/hosts/{id}/update`, admin); host card Update button; the "manual, with the reason" unsupported state; the drill with a job running; the manual real-systemd checklist `roadmap/in-product-updates-manual-check.md`.

Updated at the third pause (9 October 2026, 15:05Z). Everything through 5.3 is on `main`: #797, #801 (Part 5 through Task 5.3, merged by the owner as work in progress) and #803 (the 5.3 review fixes: release-build gate in `hostCanSelfUpdate`, the failure cap read per target, the rollout re-check before a host is asked, the minors). No Part 5 branch or PR is open.

**Part 5 so far:** 5.1 (`update_rollouts`, migration 0090), 5.2 (`internal/updates/plan.go`, `Decide`), 5.3 (`internal/controller/updates_loop.go` and `updates_auto.go`, `StartHostRollout`/`ResumeRollout`/`CancelRollout`, `auth.AutoUpdateIdentity()`, `UpdatesView.Rollout`). Auto mode should stay off until Part 5 is finished and the manual real-systemd check has been run.

Known gaps for the PR bodies: retention (90 days) can reset the two-failures cap once both failed rows are pruned; the update status reads the planner inputs on every render; a failed manual press on a member host halts the rollout on purpose.

**Next:** start a branch off `main` (for example `claude/zf232-5-routes`). Task 5.4 (rollout routes and CLI; the HTTP handler audits a person's press per R4; validate `host_ids`; map a bare `store.ErrNotFound` to a message naming the id), Task 5.5 (rollout progress in the UI), the Part 5 opus review, then a draft PR. Make the wording that says auto does nothing (`updatesAutoNotYet`, `web/src/lib/hosts/no-report.ts`, docs) true. Then Part 6: 6.1 (rollout-restart drill and `test/upgrade/helper-check.sh` against a fake systemctl), 6.2 (documentation and screenshots), the Part 6 review, and a final seam review of the whole feature.

## Carries for the remaining tasks (important)

* Wording: the config clause (`updatesAutoNotYet`, "auto takes no release by itself yet"), `web/src/lib/hosts/no-report.ts:113` and the docs say auto does nothing; Part 5 must make them true (reword when auto acts).
* Planner contract (from the 5.2 review): `Rollout.Trigger`, `Rollout.HostIDs` (nil = every host behind), `Rollout.Since`, `HostFacts.Ended []Attempt`, `Snapshot.LastRollout` (do not auto-restart a rollout a person cancelled), `Snapshot.Helper` ready|missing|unsupported, a zero `RequestedAt` never times out at once. The builder must pass these truthfully and count failures against the CURRENT target only.
* Rollout membership must be persisted (edit the unshipped migration `0090_update_rollouts.sql` in place only while it has not merged; check `git log origin/main -- internal/store/migrations/0090*`; migration numbers: main's highest was 0089 when 5.1 landed, re-check at every use).
* The root helper's path unit has `StartLimitBurst=5` in 10 minutes: never write more than one start per pass; single flight and the 30-minute retry wait keep it far below.
* Time-outs (90 minutes) are procedural and mode-independent (ruling R10); the planner emits `ActionTimeOut` through the same idempotent closer; fenced controllers do nothing.
* The controller audits only the planner's own actions (`auth.AutoUpdateIdentity()`); the HTTP handler audits a human's action (ruling R4); the helper's text reaches the platform role only (views have a `.For(platform)` filter; the host stream has `hostsFor`).
* Known gaps to put in the PR bodies: a fenced controller or a failed store read drops a host report (attempt times out at 90 minutes); nothing has run under real systemd; the unsupported-reason is kept in memory only; a Docker daemon with user-namespace remapping and a service that runs as root still show as "missing"; with the mode off a failed attempt shows a disabled "Try again" with no reason (one reason field); a Windows agent's card still shows a `sudo` command; commit trailers name two different models.

## How I was working (process, so the next session can match it)

* Subagent-driven development (the superpowers skill): a fresh implementer per task (sonnet for mechanical, opus for trust boundaries), a task reviewer after each (opus for security-relevant), fix rounds (resume the implementer with the findings), a part-level opus review of the whole range before the PR. Rulings are recorded as `Ruling Rn:` lines in the ledger.
* Every task: tests first, then mutation-check each rule (break it, a named test must fail, restore) and list the failing test names in the report.
* Repo rules that bit repeatedly: no em dash and no ` -- ` stand-in in prose or UI strings (`internal/docs/no_em_dash_test.go`); British spelling; tests must not depend on `TMPDIR`, uid or the real host (CI's TMPDIR is under `/home`); Windows CI (skip POSIX-mode, `#!/bin/sh`, link assertions); never write a literal U+202E in a test (staticcheck ST1018: use `‮`); generated files (`internal/api/openapi_spec.go`, `web/src/lib/api/schema.d.ts`, `internal/catalog/catalog.json`, `skills/zoomies/reference.md`) are regenerated with `go run internal/api/gen_openapi.go`, `make openapi`, `make generate`, never by hand; a squash-merge means conflicts in those are solved by regenerating; never symlink `node_modules` into a worktree (`npm ci` follows it and empties the original); the local disk is small: `go clean -cache` frees ~25 GB.
* Git: all my branches are `claude/zf232-*`; the owner squash-merges PRs and often merges before my review finishes, so findings from a late review go in a follow-up PR off `main`. Pushing/merging my own PRs is authorised when ALL checks are green ("merge when green"), never over red. CI notes: the Go 1.27.1 `govulncheck` failure was fixed on `main` (CI now builds with 1.27.2); a re-run of an old run cancels the newest one (concurrency group), so re-run only the head's run; deleting remote branches is refused (403), so the stray remote branch `claude/zf232-3-controller` remains.
* The user's decisions (via questions): keep stacking Parts 4-6; an explicit "manual, with the reason" state for platforms that can never run the helper (done in Part 4); a manual real-systemd checklist for a spare host (done); the Go toolchain bump was already done on `main` by the owner.

## First steps for the next session

1. `git fetch origin`, read `gh`-less: use the GitHub MCP tools to see the state of #797 and any new PRs; check `main`'s highest migration number.
2. Check whether `claude/zf232-5-auto` has unpushed/uncommitted 5.3 work (it may not survive); otherwise re-create from the plan.
3. Push the planner fix commit, get a scoped opus re-review of it, then finish 5.3 (review), 5.4, 5.5, Part 5 review, open the Part 5 PR (base `main`), fix findings, then Part 6.
4. Run the manual checklist on a real systemd host before anyone sets `auto` somewhere that matters.
