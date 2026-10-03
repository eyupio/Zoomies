# Zoomies AI Context handoff

Updated: 2 October 2026. Branch: `feature/ai-context-managed-files`.
Foundation PR: https://github.com/eyupio/zoomies/pull/570 (merged).
Previous continuation PR: https://github.com/eyupio/zoomies/pull/571 (merged).
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

Next concrete action at that checkpoint: implement managed repository setup templates and reviewed setup PR creation from these drafts, with pinned workflow actions, explicit write-permission checks, safe retry/reconciliation and verified availability. See the latest checkpoint below for current work. Live GitHub access removal, ingestion/storage integration, MCP retrieval, OIDC uploads and assistant artifacts remain. Earlier full-suite limitations still apply; do not mark the feature complete.

Published wizard implementation commit: `52f022f275ca1db49612cb81fe89126258076b1e` on draft PR #571. Published tree `77f3ec61576e5a06f7445fa21efc2bccbfb9d82c` exactly matches the validated local implementation tree.

## Managed setup file planning checkpoint

PR #571 is merged (merge commit `3a6c457c03b031e42e877b3db6c8f60120ece09a`). Read the supplied Repomix export and verified the changed existing files against live main before continuing.

Added `internal/aicontext/setup.go` and `setup_test.go`: deterministic configuration/instruction/badge planning; original blob identities for later optimistic publication; bounded UTF-8 input; Markdown README selection; Enterprise badge links; exact idempotent retries; preserved user text/CRLF. Unknown ownership, custom settings, newer template versions, changed sections, duplicate/reversed/incomplete markers, unsafe paths and missing blob identities refuse the entire plan. Missing READMEs are not invented. Zoomies-only remains unavailable.

Validation: `go test -race ./internal/aicontext -count=1` passed (1.364s); `go vet ./internal/aicontext` passed; gofmt and `git diff --check` passed. Tests include preservation/retries, conflict refusal, custom config, Enterprise badges and unavailable destination. No API/UI changes or external workflow pilot in this increment. Earlier full-suite limitations still apply.

Next concrete action: pinned workflow/generator template and commit-pinned regular-file GitHub reads; use the planner from admin preview/setup endpoints. Add live write checks, base-commit revalidation, durable setup PR identity and existing-PR reconciliation before exposing Create setup PRs in the wizard. Wire verified availability only after merge and validated generation. Managed PR publication, ingestion, MCP source tools and the remaining phases are not complete.

Published continuation PR: https://github.com/eyupio/zoomies/pull/572. Implementation commit: `6bbdebcf97d1b2f720bb627d84d4b97af239abbf`; tree `685a05e250d3cf9b73b2bd75a942c4a15321a38b` exactly matches the validated local implementation tree. PR targets merged main; no repository was enabled. This publication note is a documentation-only follow-up.

## Managed workflow and reviewed setup PR checkpoint (stacked on PR #572)

Implemented the next managed setup increment on `feature/ai-context-managed-files`:

- Embedded pinned workflow/toolchain templates for repository and Both output. Actions pins were checked against their official GitHub tag refs. Repomix is pinned to 1.18.1 with its integrity-locked npm tree; installation uses a temporary tool directory and disables lifecycle scripts. No repository build or install script runs.
- Generation stages bounded regular Git blobs from the exact trusted source commit, applies mandatory credential-path exclusions and configured exclusions, invokes Repomix secret scanning with private logs, and preserves original bytes/line numbers after verifying Repomix's boundary trimming. Empty, omitted, oversized or modified packs fail before publication. Snapshots pass the existing Go Decode/Match contract. Token counts are explicitly not measured.
- A separate contents-write job validates artifact identity, paths, file hashes, size and source freshness. It atomically moves the owned generated branch without force, preserves its prior valid output on failure, and refuses unowned branches or extra user files. Git history is preserved; Zoomies snapshot retention awaits ingestion.
- Commit-pinned Git tree/blob reads refuse symlinks, submodules, unsafe parents and oversized setup files. The GitHub-rendered Markdown README badge goes near the leading title; user text, line endings and executable modes are preserved. Conflicts and unknown ownership refuse the entire proposal.
- Admin-only GET/POST setup endpoints return proposed contents and require explicit revision/plan-hash approval. Migration write permissions are checked live. The complete setup is one Git tree/commit; the publisher never uses the older sequential migration writer. Durable migration 0064 stores the reviewed proposal before writes, freezes configuration, leases publication and recovers the same branch/PR after restart or a lost response. Existing edited branches and closed uncertain PRs are preserved. Unrelated source changes do not prevent recovery of an already-created exact proposal.
- The wizard saves drafts, shows copyable accessible file previews and offers Create setup PRs as a separate explicit action. Partial failures retain successes; retries submit only failures. Setup state and PR links are visible on bounded admin repository pages. No repository is automatically enabled and no app/source consent is created by PR publication.
- Added a dedicated CI generator contract job. OpenAPI, generated clients, API/UI/dependency documentation and stale front-page navigation counts are updated.

Validation:
- `go test -race ./internal/aicontext ./internal/github ./internal/docs -count=1` passed; the aicontext run set `ZOOMIES_TEST_REPOMIX_CLI` and exercised real Repomix generation. Python publication boundary tests also run from the Go suite.
- Targeted API/store context tests with `-race` passed, including explicit review, role denial, stale source/configuration, frozen configuration, live permission refusal, one-PR retries, lease ownership/expiry and metadata pages. Additional lost-response/closed-PR/user-edited-branch tests passed. Durable setup restart/cascade verification passed with the other setup store tests (9.688s).
- Five Python publication boundary tests passed: corrupt hashes/symlinks, superseded source, complete atomic publication, unowned/user files and non-force updates.
- `go build ./...`, binary build and targeted `go vet` passed. Svelte check: zero errors/warnings; changed-file ESLint/Prettier and production UI/status builds passed their size budgets.
- The real-controller/fake-GitHub browser flow passed at desktop and 375px, including draft partial retry, escaped source preview, no horizontal overflow and PR retry-only-failures. Setup preview/publication replies use browser fixtures because the fake API's loopback host intentionally fails the production Enterprise template gate. The actual setup APIs/publisher are exercised against fake GitHub in Go integration tests. This browser test is not a live GitHub Actions pilot.
- Desktop/mobile preview and results screenshots were visually inspected. The previous general full-suite restrictions still apply; full repository-suite success is not claimed.

Limitations and next concrete action:

Finish verified merge/access reconciliation and durable branch ingestion, then authorised compact REST/MCP retrieval and the live client pilot. Add workflow repair/upgrade proposals and recovery when an abandoned pending proposal has no safe branch to resume. Submitted state is persisted PR-creation state, not live merge/freshness verification. GitHub Enterprise artifact templates and Zoomies-only OIDC uploads remain unavailable. No Jiggered setup PR or live Actions run was created by this increment; no live Claude/ChatGPT retrieval was established. Do not mark phase 1, phase 2 acceptance gates or the full feature complete. Remaining phases 3–6 still apply.

Published on existing PR https://github.com/eyupio/zoomies/pull/572. Implementation commit: `3cd5280130a14f747fa26d42d93b8f078d2f3396`; tree `cd85aee7d74f8e9590c252571bd151ec5ef42932` exactly matches the validated local implementation tree. The PR title/body now describe the complete managed setup increment. This publication note is a documentation-only follow-up. The feature and live acceptance gates remain unfinished as described above.


## PR #572 CI regression fixes

The first full CI run found missing integration checks from the AI Context work: new OpenAPI paths included `/api/v1` despite the server URL already supplying it, role metadata and the hand-written route authorisation table were incomplete, migration 0064 was absent from the fixed shipped-name list, and staticcheck found two capitalised errors and a redundant declaration. Corrected the contract and regenerated both clients, added every context/connection selection route to the role walk, and explicitly verified that source membership refuses bearer tokens even with matching scopes. Removed the unused context.refresh action until its endpoint exists, rather than offering a permission with no behaviour. Added migration 0064 to the ledger test and fixed the staticcheck findings.

The AI Context browser test also left its installation in the shared connect controller. The following invalid-key connection was refused as a duplicate and then verified the older healthy installation. The AI Context test now removes only the installation it created in a finally block; the existing connection test remains unchanged. The combined AI Context/connect browser run passed all five tests (8.5s), reproducing the original test order. Svelte check, changed-spec ESLint/Prettier, targeted store/auth/context/GitHub race tests, full staticcheck and binary build passed. The combined API contract/role/scope race tests passed (42.531s); targeted AI Context API regression tests passed (18.573s). Full repository-suite success and live AI Context acceptance are not claimed.


## AI Context UX and UI polish review

Reviewed the new repository list, six-step preparation flow, partial-failure review, file previews and published results against the shared component/token contract. The main defects were nested review scrolling on mobile, stretched full-width desktop actions, indistinguishable neutral status badges, simultaneous loading spinners on unrelated actions, no focus handoff into review, and exclusion errors reported under retention.

Applied scoped polish: page-level scrolling for review results; compact grouped card actions and full-width mobile publication actions; central status metadata with text, tone and shape; operation-specific progress/loading and a ready/published count; review-heading focus; copy controls and monospaced file paths; new-tab external PR links; field-specific validation and exclusion help; a selected-repository review list; reader search empty messages; and clear-filter recovery plus accurate reader empty-state copy on the repository list. These changes preserve explicit setup approval and partial retry behaviour.

Validation: Svelte check has zero errors/warnings; changed-file ESLint/Prettier passed; production builds pass budgets (app shell 103.1 KB/200 KB, status 18.3 KB/30 KB); binary build passed. Combined real-controller/fake-GitHub AI Context/connect browser journeys passed all five tests (9.4s), including review focus, accessible validation attribution, mobile no horizontal overflow, copy-control visibility, ordinary result scrolling, partial retries, and external link target. Desktop/mobile screenshots in light/dark themes were visually inspected. Setup preview/write replies remain browser fixtures as described above; this is UI validation, not a live setup workflow pilot. The remaining feature and acceptance work is unchanged.
