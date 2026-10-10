# Seamless "Add a host" for Windows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** On Windows, choosing the OS in Hosts, Add a host, copying one command and pasting it into an elevated PowerShell window enrols the machine, for direct and Tailcat controllers and for the release and `dev` channels.

**Architecture:** A new `install.ps1` at the repo root, published beside `install.sh`, installs `zoomies.exe` and runs the existing `zoomies agent join`. The server renders the PowerShell form in the same code path as the POSIX one (`internal/api/handlers_hosts.go`), additively, so channel pinning and the unpublished-build note behave identically. The UI and CLI read the new field.

**Tech Stack:** Go (API, CLI), PowerShell 5.1+ (Pester, PSScriptAnalyzer), Svelte 5 (Playwright), MkDocs, GitHub Actions.

**Spec:** the pasted prompt `zoomies-windows-add-host-prompt.md`.

## Global Constraints

- British spelling, no em dashes, commit messages are sentence-case imperative sentences with no prefix.
- No new dependencies without a row in `docs/dependencies.md`.
- Do not claim Windows is qualified; do not change the support-matrix status.
- Windows CI legs stay on GitHub-hosted runners (a test enforces this).
- The join token is never written to disk, logs, audit rows or echoed; only its hash is stored.
- API change is additive: `commands: { linux, windows }` beside the existing `command`.
- The command the UI generates never contains `-Yes`.
- Only `windows_amd64` is published; refuse ARM64 and 32-bit.

## Review Focus

- A controller URL or token containing `'`, `$`, backtick or `"` must stay one literal PowerShell argument (single quotes, `'` doubled).
- A controller on a local or unset external URL keeps the `https://<this-controller>` placeholder in the Windows command too.
- An unpublished (local) build gets the same `version_note` and no `-Version` pin in the Windows command.
- `agent join` on an enrolled host prompts or fails without `--yes`; the script must never reach it on a plain re-run.
- Non-elevated or ARM64 runs must stop before anything is downloaded or written.

## Findings from reading the code

- `release.yml` builds with `make dist`, which already produces `zoomies_windows_amd64.exe` and lists it in `checksums.txt`; the upload glob `dist/zoomies_*` includes it. `dev-binaries` in `ci.yml` must be checked for the same.
- `installer.Join` on re-join asks "Replace them and join again?" and errors under `--non-interactive` unless `--yes`. So it is not idempotent without a flag; it replaces credentials and leaves the old host offline.
- The Windows service is registered via `sc.exe` (`internal/installer/service_scm.go`).

## Tasks

### Task 1: Server renders the Windows command

**Files:** modify `internal/api/handlers_hosts.go`; test `internal/api/join_command_test.go`.

**Interfaces:**
- Produces: `func powershellArgument(value string) string`; `func (s *Server) joinCommandWindows(token, controllerURL string) string`; response field `Commands struct{ Linux, Windows string }` serialised as `commands`; existing `command` unchanged and equal to `commands.linux`.

- [ ] Write failing tests: `TestPowershellArgumentKeepsHostileValuesLiteral` (inputs with `'`, `$(x)`, backtick, `"`, newline; output is `'...'` with `'` doubled), `TestWindowsJoinCommandPinsTheChannelLikeTheLinuxOne` (pinned and unpinned builds, direct and `tailcat://`, placeholder when the external URL is local), and a handler test that `commands.windows` is present and the token appears only in the response.
- [ ] Run `go test ./internal/api -run 'Powershell|WindowsJoin'`; expect FAIL.
- [ ] Implement. Shape: `& ([scriptblock]::Create((irm https://zoomies.sh/install.ps1))) -Mode agent -Controller '<url>' -JoinToken '<token>' [-Version <tag>]`. Reject or neutralise newlines (a newline inside single quotes is still literal in PowerShell, but strip CR/LF defensively).
- [ ] Run the tests; expect PASS. Commit: "Render the Windows join command beside the Linux one".

### Task 2: OpenAPI and generated client

**Files:** modify `api/openapi.yaml` (createJoinToken response), regenerate `web/src/lib/api/schema.d.ts` with `make openapi`.

- [ ] Add `commands` (object, `linux` and `windows` strings) and describe it; keep `command`.
- [ ] Run `make openapi`, then the API shape tests (`go test ./internal/api`). Commit: "Describe the per-OS join commands in the API contract".

### Task 3: CLI prints the Windows form

**Files:** modify `cmd/zoomies/hosts.go`, `cmd/zoomies/types.go` (`joinTokenItem`), `skills/zoomies/reference.md` (regenerated), tests in `cmd/zoomies/`.

- [ ] Failing test: `TestJoinTokenCreatePrintsTheWindowsCommandForOSWindows`; `--os` accepts `linux`, `windows`, `all` (default `linux`, so current output is unchanged); an unknown value is a usage error naming the choices.
- [ ] Implement, regenerate the skill reference, run `go test ./cmd/zoomies`. Commit: "Let zoomies hosts join-token create print the Windows command".

### Task 4: `install.ps1`

**Files:** create `install.ps1`, `test/install/install.Tests.ps1` (Pester), `.github/workflows/ci.yml` (PSScriptAnalyzer + Pester + Windows smoke job on `windows-latest`), `.github/workflows/docs.yml` (publish and `cmp`), `mkdocs.yml`/`hooks/seo.py` (llms.txt entry), `.github/AGENTS.md` (files CI diffs).

**Interfaces:** parameters `-Mode`, `-Controller`, `-JoinToken`, `-Version` (`latest`, `vX.Y.Z`, `dev`), `-Uninstall`, `-Yes`, `-Force`; a testable function layer guarded so dot-sourcing does not run `Main`.

- [ ] Pester tests first: refuses ARM64 and 32-bit before any download; refuses non-elevated; version resolution (`latest`, `1.2.3` to `v1.2.3`, `dev`); URL `https://github.com/eyupio/zoomies/releases/download/<tag>/zoomies_windows_amd64.exe`; checksum mismatch aborts; the token never appears in any output stream; re-run on an enrolled host does not call `agent join` without `-Force`.
- [ ] Implement in order: preflight, elevation, confirmation (`-Yes`), download + SHA-256 against `checksums.txt` (abort on mismatch or missing entry), install to `%ProgramFiles%\Zoomies`, machine PATH, `agent join ... --backend process` (token passed on the command line to a child process only, never written), verify service Running and Automatic, ACL check/fix on `%ProgramData%\zoomies` (SYSTEM and Administrators only), print locations and the `os=windows` next step, upgrade path (stop, replace, start), `-Uninstall`.
- [ ] Failures exit non-zero with: token expired or used, controller unreachable, service failed (tail of `zoomies-agent.log`).
- [ ] Smoke job: serve a fake controller and release assets locally, run the script, assert `Get-Service zoomies-agent` is Running. Commit: "Add install.ps1 so a Windows host joins with one pasted command".

### Task 5: Add a host dialog

**Files:** modify `web/src/lib/hosts/AddHostFlow.svelte`; create `web/src/lib/hosts/windows.ts` (the single `WINDOWS_QUALIFIED = false` constant referencing the support matrix, plus OS default from `navigator.userAgent`); tests `web/unit/windows-host.test.ts`, `web/tests/hosts-windows.spec.ts`.

- [ ] Unit tests: OS default from platform; qualified constant drives the "not yet qualified" line.
- [ ] Implement an OS radio group (Linux / macOS, Windows) beside the connection choice; Windows shows `commands.windows`, the line "Run this in PowerShell as administrator.", and the process-backend, fresh work directory, trusted repositories and security-page statement. Post-enrolment: recognise `os=windows` with the `process` backend and, when no pool matches, suggest `--host-selector os=windows`.
- [ ] Playwright: OS switch, both connections, copy, light and dark, phone width. Commit: "Offer a Windows command in the Add a host dialog".

### Task 6: Docs and roadmap

**Files:** `README.md`, `docs/quickstart.md`, `docs/hosts-and-pools.md`, `docs/private-hosts.md`, `docs/home-lab.md`, `docs/faq.md`, `docs/security.md` link, `roadmap/support-and-measurement.md` (manual validation checklist a to f and the evidence needed), `ROADMAP.md`, `docs/dependencies.md` (only if a tool is added), generated `llms.txt` hook.

- [ ] Fix the README requirements contradiction and runner-image list from the same source the site uses.
- [ ] Run `go test ./internal/docs/...` (no em dashes, links). Commit: "Document the Windows join command and fix the README's platform list".

### Task 7: Verify

- [ ] `make build-nogui && make lint && make test`; `make test-ui` if a browser is available; Pester and PSScriptAnalyzer if `pwsh` is available.
- [ ] Open a draft PR with decisions, unverified items and answers to the report-back questions (service account, `agent join` idempotence, Tailcat on Windows, Defender and SmartScreen, winget or Scoop).

## Self-review

- Coverage: spec sections 1 to 4, tests, constraints and the maintainer checklist each map to a task above.
- Not verifiable here: real Windows behaviour (service account, Tailcat, SmartScreen). These go in the PR as unverified, not claimed.
