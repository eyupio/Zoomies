# Zoomies AI Context handoff

Updated: 2 October 2026. Branch: `feature/ai-context-access`.
Foundation PR: https://github.com/eyupio/zoomies/pull/570 (merged).
Continuation draft PR: https://github.com/eyupio/zoomies/pull/571
Published implementation commit: `50098edf15de94562e54d60adedde8ebffa5df7d`.
Continuation implementation commit: `b464e598` (local).
Initial implementation commit: `1fc40892f593d97f40d43cd2212b51ae181634a6`.
Base commit: `90a7463890631be139fb00c72e82351995c565ea`.

## Goal and source of truth

Implement [the agreed plan](docs/ai-context-implementation-plan.md). The user supplied a repository context export to reduce repeated source reads; use it as navigation and verify changed files against the checkout. Keep this file current at each implementation checkpoint.

## Current state

- First phase-1 foundation increment merged in PR #570. End-to-end feature readiness is not claimed.
- Continuation adds stable numeric repository IDs in GitHub discovery and the fake, separate source-read/setup permission probes, admin-only discovery/draft/list/config endpoints and generated OpenAPI/TypeScript contracts. Discovery probes the client directly; it must not call fleet verification, which can create runner groups.
- Draft creation resolves host/name/default branch from the live installation selection, refuses archived/inaccessible repositories and missing Contents read permission, remains unavailable, and grants no source membership. Duplicate drafts return 409. Configuration saves use revision checks and keep the discovered source branch. Metadata lists are paged (50 default, 100 maximum); discovery is bounded to 500 with a conservative capped signal.
- `internal/aicontext`: branding constants, config/branch validation and config hash; host/installation/repository identity; bounded snapshot validation; compact Unicode source reads and literal search; private atomic digest-addressed disk storage.
- `internal/store`: migration `0063_ai_context.sql`, restart-safe repository config with revision checks, explicit user membership and per-connection consent. Removed membership also clears app consent, so adding a user back cannot resurrect old grants.
- `internal/auth`: separate context actions, repository-specific access checks and signed-in-owner consent service. No automatic source privilege for existing fleet/MCP grants or unowned tokens.
- Contracts use `internal/aicontext` rather than `internal/context` to avoid confusion with Go's standard context package.
- No feature deployed or repositories automatically enabled.

## Decisions

- Reuse Zoomies' Go REST/MCP/OAuth infrastructure; no separate Python service.
- New source privileges must not be inherited by existing fleet viewer/MCP grants.
- Repository output and Both precede Zoomies-only OIDC uploads.
- Branding: `zoomies-ai-context` workflow/branch/artifact, `.zoomies/ai-context/` output, README Actions badge.
- Main navigation: AI Context; primary action: Enable repositories.

## Validation

- Go 1.27.1 installed at `/tmp/zoomies-go/go/bin/go` for this workspace; prepend that directory to PATH.
- `go build ./...`: passed (after `make ui-stub`; no UI source changed).
- `go test -race ./internal/aicontext`: passed, including storage tampering/restart/concurrent publication and bounded literal search.
- Targeted foundation tests with `-race` in internal/store and internal/auth: passed, including explicit consent, rollback of failed grant updates, live revocation, stale configuration and restart/cascade checks.
- `go vet ./internal/aicontext ./internal/store ./internal/auth`: passed; aicontext was rechecked after literal search was added.
- `go test -race -run TestAnAgentCannotGrantItselfSourceConsent ./internal/auth`: passed.
- Formatting and `git diff --check`: passed.
- Full store/auth regression run is pending. Full `go test ./...` was blocked by automatic approval review because a test attempted HTTPS to `tailcat.dev` with an unidentified payload; do not bypass that rejection. Build and isolated foundation tests are unaffected. Full-suite completion is not claimed.

## Remaining phases

1. Finish phase 1: expose authorised configuration/list/access APIs, wire explicit repository selections into OAuth consent/connections UI, and add permission probes. The current functions are foundations and are not HTTP endpoints yet.
2. Managed setup PRs, workflow/config/instruction/badge templates and wizard.
3. Durable ingestion, refresh/reconciliation, retention and freshness.
4. Compact MCP retrieval and live client pilot.
5. Zoomies-only OIDC upload.
6. Assistant-written artifacts.

## Resume efficiently

Read this file, then the relevant phase in the plan. Follow root/scoped CLAUDE.md and docs/architecture.md. SQL belongs only in internal/store; MCP tools use REST only. Never call a phase complete without its acceptance checks. Update this file with changed paths, exact test results, PR/commit and next concrete action.

Next concrete action: build AI Context navigation and the Enable repositories wizard against the discovery/draft/list/config/membership endpoints, including resumable drafts and explicit reader selection. Connection source consent is now editable under Settings → MCP connections. Initial OAuth approval still grants no source repositories; consider inline repository consent only with atomic approval/consent persistence, not a second best-effort call after issuing the authorisation code. Do not silently grant GitHub write permissions. Discovery currently scans at most 500 repositories; installations above that ceiling need real continuation/search support before claiming organisation-wide setup. DiskStorage is not wired to controller startup/backup/retention yet; Overview returns a full metadata list and the eventual endpoint must page it. Read's budget is source bytes, not escaped protocol bytes or exact model tokens.

## Continuation checkpoint

Changed paths: `internal/github/{client,migrate,fake,fake_migrate}.go`, `internal/controller/ai_context.go`, `internal/api/{router,handlers_ai_context,ai_context_test}.go`, `internal/store/queries_ai_context.go` and its tests, `api/openapi.yaml`, generated OpenAPI/TypeScript clients, `docs/api-surface.md` and this handoff.

Validation for this increment:
- `go test -race ./internal/github -count=1`: passed.
- `go test -race ./internal/aicontext -count=1`: passed.
- `go test -race ./internal/api -run 'TestAIContext|TestMigrationPlan' -count=1`: passed, including capped discovery, role refusals, drafts, missing source permission, archived repositories and stale configuration.
- `go test -race ./internal/store -run TestContext -count=1`: passed, including bounded pages.
- `go build ./...`, `go vet ./internal/github ./internal/controller ./internal/api ./internal/store`, formatting and `git diff --check`: passed.
- Full `go test -race ./internal/auth -count=1` regression: passed (222.226s). Full store regression timed out at 600s while starting `TestAnUnfinishedJobHoldsTheRollUpOnlyUntilItWouldBePruned`; that test passes in the subsequent targeted race run. Full store-suite success is not claimed. Full repository test suite was not retried; the previous outbound-test restriction still applies.

Phase 1 is still incomplete: live GitHub permission/removal reconciliation and verified repository enablement remain. The connection consent UI/endpoints and repository membership administration endpoints are now implemented. No navigation/wizard UI, managed setup PR, ingestion or MCP retrieval is shipped by this checkpoint. Drafts remain unavailable until a later verified enablement path is implemented.

Publication status: published through the authenticated GitHub connection after explicit user approval. The published implementation tree `6b13619c4575a07eba16fdba5c5e0fd754743884` exactly matches the validated local tree. PR #571 is a draft implementation checkpoint.

## Source consent checkpoint (stacked on PR #571)

- Admin-only GET/PUT membership endpoints explicitly assign source readers. Removing membership clears the person's existing connection grants; re-adding membership does not restore consent.
- Signed-in-owner GET/PUT connection repository endpoints check live user/client/connection state. API tokens and MCP access tokens cannot use them to widen their own privileges. Only available repositories with current membership appear; names/configuration belonging to other users are not returned.
- Paged choices return the complete eligible selection separately so editing one page preserves off-page consent. Selection writes require an explicit array; missing/null fields never mean revoke all. The owner can explicitly clear all grants.
- Settings → MCP connections adds Source access controls with a paged, accessible dialog, selected count, Remove all, cancellation, retry and an honest unavailable-repositories empty state. OAuth approval explains that source grants start empty. This checkpoint does not claim source retrieval or verified repository enablement.

Changed paths: `internal/store/queries_ai_context.go` and tests; `internal/api/{handlers_ai_context,router,ai_context_test}.go`; `api/openapi.yaml` and generated clients; `web/src/lib/{api/client,mcp/ConnectionsTable,mcp/SourceAccessDialog,settings/McpConnectionsPanel}.svelte/ts`; `web/src/routes/McpConsent.svelte`; `web/tests/mcp-oauth.spec.ts`; `docs/api-surface.md`.

Validation:
- Targeted API AI Context/connection-source consent tests with `-race`: passed.
- Targeted store context/membership/revocation/choice tests plus the test running at the full-suite timeout with `-race -timeout=90s`: passed (23.371s).
- Targeted auth source-access/self-consent refusal tests with `-race`: passed.
- `go build ./...`, binary build and targeted `go vet`: passed.
- Svelte check: zero errors/warnings; changed-file ESLint/Prettier: passed; production app/status builds pass their size budgets.
- MCP OAuth Playwright suite against an isolated real controller: five tests passed, including source dialog at desktop/375px, focus return, explicit empty state and off-page choice preservation. Paged UI data uses a browser route fixture; permission/ownership/revocation tests use the real API/store.
- Desktop/mobile dialog screenshots visually inspected; no clipping or horizontal overflow. The test sign-in helper now sends its same-origin Origin header after bootstrap instead of relying on a cookie-authenticated POST with no Origin.
- Formatting and `git diff --check`: passed.

Remaining: navigation/wizard, managed setup templates/PRs, verified availability and live GitHub access removal wiring, ingestion/storage integration, compact MCP source tools, OIDC upload and assistant artifacts. Do not mark phase 1 or the feature complete.

Published source-consent implementation commit: `e1a732992cdf39ce025d979ee4bb6ef727696c89` on PR #571. Published tree `faf9bcb0087991dcfe634cc42edf414269ef9de1` exactly matches the validated local implementation tree. The PR title/description now cover both preparation APIs and explicit source consent.

## Navigation and preparation wizard checkpoint (PR #571)

AI Context is now a lazy-loaded navigation section with keyboard and command-palette entries. Administrators can search/filter paged drafts and resume them; readers see only available repositories with explicit membership. Added administrator-only single-draft and stable source-identity lookup endpoints and a separate bounded reader list. Discovery supplies canonical exclusion/retention defaults.

The six-step preparation wizard supports repository selection, readiness checks, destination, exclusions/retention, explicit source readers, review and resumable draft saves. No repositories/readers are preselected. Zoomies-only is disabled pending secure uploads. Partial saves retain successes and retry failures; existing drafts must be resumed before their saved configuration/readers can be changed. Saved drafts remain unavailable and do not create setup PRs or source grants for app connections.

Validation: targeted API race tests passed (14.901s); targeted store race tests passed (12.115s), including literal search escaping and reader access boundaries. Svelte check had zero errors/warnings; production UI/status build, changed-file lint/formatting, binary build and targeted go vet passed. A new Playwright test passed against the real controller and fake GitHub (1.2s): two repositories, simulated temporary second-save failure, retry only failures, persisted retention, unavailable drafts and same-page draft resumption. Desktop review and 375px configuration screenshots were inspected; no horizontal overflow. Test is included in the connect project.

Next concrete action: implement managed repository setup templates and reviewed setup PR creation from these drafts, with pinned workflow actions, explicit write-permission checks, safe retry/reconciliation and verified availability. Live GitHub access removal, ingestion/storage integration, MCP retrieval, OIDC uploads and assistant artifacts remain. Earlier full-suite limitations still apply; do not mark the feature complete.
