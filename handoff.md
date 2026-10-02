# Zoomies AI Context handoff

Updated: 2 October 2026. Branch: `feature/ai-context-access`.
Foundation PR: https://github.com/eyupio/zoomies/pull/570 (merged).
Continuation PR: publication authorised by the user; being created.
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

Next concrete action: expose explicit source membership and signed-in-owner connection consent endpoints, then wire repository selections into OAuth/connections UI. Build the AI Context navigation and wizard against the discovery/draft/list/config endpoints. Do not silently grant GitHub write permissions. Discovery currently scans at most 500 repositories; installations above that ceiling need real continuation/search support before claiming organisation-wide setup. DiskStorage is not wired to controller startup/backup/retention yet; Overview returns a full metadata list and the eventual endpoint must page it. Read's budget is source bytes, not escaped protocol bytes or exact model tokens.

## Continuation checkpoint

Changed paths: `internal/github/{client,migrate,fake,fake_migrate}.go`, `internal/controller/ai_context.go`, `internal/api/{router,handlers_ai_context,ai_context_test}.go`, `internal/store/queries_ai_context.go` and its tests, `api/openapi.yaml`, generated OpenAPI/TypeScript clients, `docs/api-surface.md` and this handoff.

Validation for this increment:
- `go test -race ./internal/github -count=1`: passed.
- `go test -race ./internal/aicontext -count=1`: passed.
- `go test -race ./internal/api -run 'TestAIContext|TestMigrationPlan' -count=1`: passed, including capped discovery, role refusals, drafts, missing source permission, archived repositories and stale configuration.
- `go test -race ./internal/store -run TestContext -count=1`: passed, including bounded pages.
- `go build ./...`, `go vet ./internal/github ./internal/controller ./internal/api ./internal/store`, formatting and `git diff --check`: passed.
- Full auth/store regression run in progress. Full repository test suite was not retried; the previous outbound-test restriction still applies.

Phase 1 is still incomplete: source consent UI/endpoints, repository membership administration, and live verification/revocation wiring remain. No navigation/wizard UI, managed setup PR, ingestion or MCP retrieval is shipped by this checkpoint. Drafts remain unavailable until a later verified enablement path is implemented.

Publication status: the user explicitly authorised PR creation. CLI push has no configured credentials; publish the validated tree using the authenticated GitHub connector.
