# Zoomies AI Context handoff

Updated: 3 October 2026. Branch: `feature/ai-context-maintenance`. Latest checkpoint is at the end of this file.
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

## New PR after merging #572: verified Both-mode ingestion

PR #572 is merged at `fe6239b1ed6741634a8553adf02757ae2b713f72`. This increment is published in [PR #576](https://github.com/eyupio/zoomies/pull/576), based on that merged main, not stacked on the old PR.

Implemented read-only verification of the saved setup PR merge, live GitHub Contents access, stable numeric repository identity and trusted default branch. Exact managed workflow/configuration/generator package/lock content must still match the approved configuration. Generated output is read through immutable Git objects, must have exactly the complete managed layout and notice, and is bounded before blob fetch/decoding. The manifest hash and strict snapshot validation are followed by matching every included file to its original regular Git blob in the pinned source commit. Recheck the trusted ref before admission; enforce configured exclusions again. A forged self-consistent source digest is insufficient.

Migration 0065 stores Both-mode snapshot payloads and freshness in SQLite. This intentionally replaces the original separate-directory storage proposal: snapshot insertion, retention, quota, freshness and availability commit atomically, existing backups restore source and metadata together, and repository deletion cascades to retained source. The 256 MiB guard counts retained encoded payload only, not physical SQLite/WAL/backup size. Existing 16 MiB snapshot, 12 MiB source, 1 MiB file and 5,000-file bounds apply. Tied timestamps preserve the just-published snapshot; quota/CAS failures roll back retention too. Cached rechecks validate integrity and update freshness without rewriting source.

A separate loop starts after 15 seconds and checks at most 20 explicitly submitted proposals per batch, then waits five minutes; it runs outside the scheduling lock. A shared one-slot gate bounds verification concurrency, and background/manual checks have a 45-second deadline. Failure cleanup has its own five-second bound. Failure closes availability and retains the last valid snapshot; unmerged setup is shown as awaiting merge. Repository GET/list metadata now includes freshness. The administrator-only POST `/ai-context/repositories/{id}/recheck` checks existing output without triggering Actions or writing a branch. The UI shows verification state, last checked time, last verified commit and safe failure reason, with a recheck control.

Validation: full store/GitHub/aicontext package tests passed; focused race tests passed, including snapshot admission, original Git-blob forgery, exclusions, merge/base checks, live permission revocation, workflow drift, stale output, retention ties, quota rollback, independent backup restore, deletion cascade and corruption. API OpenAPI/role/scope/context race tests passed (63.310s). Full staticcheck passed; Svelte check has zero errors/warnings; changed-file ESLint/Prettier and production builds passed. Combined real-controller/fake-GitHub AI Context/connect browser journeys passed all six tests (10.4s). New freshness UI metadata/recheck replies are browser fixtures; server ingestion and recheck use real handlers and fake GitHub in Go integration tests. Desktop dark and 375px light screenshots were inspected. This is not a live GitHub Actions or assistant-client pilot, and full repository-suite success is not claimed.

Next: authorised compact REST/MCP source retrieval must check explicit caller grants before and after live verification and bound encoded responses; do not expose store snapshots directly. The controller's verified-snapshot helper repeats live access/setup checks, but has no source REST/MCP route yet. Repository-only transient retrieval remains unavailable; background verification reports this explicitly and stores no source for that destination. Zoomies-only OIDC ingestion, generated-branch push enqueueing, repair/upgrade proposals, pause/removal lifecycle improvements, Enterprise workflow templates and live Jiggered/Claude/ChatGPT acceptance remain unfinished. Do not mark the complete AI Context feature or live acceptance gates complete.

Published implementation commit: `b562a552a13906a774d6473bf5e2322240564466`; implementation tree: `8e8ced4832bbfb66266ff8a64f0884fa8224cc9b`, exactly matching the validated local implementation tree. CI was queued when publication was checked; no remote CI success is claimed. This publication note is a documentation-only follow-up.

## Compact authorised REST/MCP retrieval checkpoint — 3 October 2026

PR #576 is merged at `5e633cd6fed4b69b038cda859747b442298a2c04`. The local baseline was compared with the attachment and the merged remote tree; the merged dependency lock updates were synchronised before testing. Baseline tree: `7cf72bb4c8efcf67846b79697113bed2951a436f`. This continuation is a new branch, `feature/ai-context-source-retrieval`.

Implemented four read-only source endpoints under `/ai-context/source/{id}/`: overview, read, search and pack. Explicit caller membership/connection consent is checked before network verification, after verification and before writing source. Credentials are revalidated too: a removed session/API token or OAuth access token cannot finish an in-flight source read. In-process MCP keeps OAuth credentials off REST and revalidates them through their original MCP request. Inaccessible repositories return a source-free 404; unowned automation tokens receive 403. Failed live GitHub checks never fall back to retained source. Responses are no-store.

`internal/aicontext/retrieval.go` bounds the fully escaped JSON page, rather than only source bytes: 1,024–24,000 bytes, default 8,000. Overview pages at most 100 file summaries; search returns at most 12 literal matches with original line numbers; packs select up to six unique safe paths and share their budget. Read/pack excerpts preserve UTF-8 byte offsets and carry explicit per-file continuation. Every source response identifies its immutable commit and snapshot. Non-zero continuation offsets require the returned commit; changes return 409 instead of mixing versions. MCP has an additional 32,000-byte encoded tool-result ceiling; callers can reduce their budget/page on refusal. Token counts are not measured.

MCP exposes `context_overview`, `context_read`, `context_search` and `context_pack` through REST only. Known-file reads need no discovery call. Overview without a repository discovers only the current credential's eligible repositories; connections see their explicitly consented subset, not all of their owner's memberships. Discovery is bounded and does not imply that later live verification will succeed. Existing connections still start with zero source repositories, and removing/re-adding membership does not restore consent.

Changed paths: `internal/aicontext/{retrieval,retrieval_test}.go`; `internal/store/queries_ai_context{,_test}.go`; `internal/api/{handlers_ai_context_source,ai_context_source_test,handlers_ai_context,auth,mcp,router,api_test}.go`; `internal/mcp/{context,context_test,tools,server}.go`; OpenAPI and both generated clients; architecture/API/assistant documentation and the implementation plan. No new dependencies, migrations or UI source changes.

Validation:
- Full `go test -race ./internal/aicontext ./internal/mcp -count=1`: passed. Real Repomix generation was not rerun; its optional CLI integration remains outside this increment's evidence.
- Targeted context/source/membership/self-consent store/auth race tests: passed (store 41.920s, auth 5.842s).
- API context, source, authorisation-role/scope/contract and MCP OAuth regression tests with `-race`: passed (124.595s).
- Additional source race run: passed (8.765s), including access-token deletion while GitHub verification is in progress, membership revocation during verification, explicit connection consent, all four MCP tools, direct REST refusal of OAuth tokens, GitHub permission removal, known-file reads and commit-pinned continuations.
- Regenerated-contract/spec checks after removing unrelated formatting changes: passed. Full MCP race tests were rechecked after enforcing discovery budgets.
- `go build ./...`, targeted `go vet`, full staticcheck, Go formatting and `git diff --check`: passed. Svelte check: zero errors/warnings. No browser UI changed, so no new browser/visual pilot is claimed.
- The existing full-repository outbound-test restriction remains; full repository-suite success is not claimed.

Next concrete action: implement repository-only transient retrieval through the same trusted verification and caller gates, without retaining source, then the context browser and measured live assistant pilot. Generated-branch push enqueueing, repair/upgrade proposals, pause/removal lifecycle, Enterprise templates, Zoomies-only OIDC ingestion and assistant-written artifacts remain outstanding. No live Jiggered setup/Actions or Claude/ChatGPT pilot was performed. Do not mark phase 4 acceptance or the whole feature complete.

Published in [PR #577](https://github.com/eyupio/zoomies/pull/577), based on merged main `5e633cd6fed4b69b038cda859747b442298a2c04`. Implementation commit: `8000fd5f54e680ed9ebdcbfc11fcc8f41dd4e55e`; implementation tree: `85842edd68c1a9b46b3b57449618ad78d9ddb4d8`, exactly matching the validated local tree. The user explicitly approved publication on 3 October 2026. The final focused source race recheck passed (8.863s). This publication note is a documentation-only follow-up; remote CI and live acceptance success are not claimed.

## Live Zoomies setup failure and assistant guidance checkpoint — 3 October 2026

The user enabled AI Context on eyupio/zoomies through PR #578, merged at d35fb6189e47d6411fe3fb31b58da3d54df380b2. Actions run 37110107009 failed in Generate bounded source context and never reached publication. Reproduced the first refusal locally: the initial generator checked a 2,223,635-byte brand PDF against the text-file limit before binary classification. Fix reads at most 1 MiB + 1 byte from each immutable blob, skips NUL/non-UTF-8 binary data first, then retains the 1 MiB text-file limit. Oversized UTF-8 samples use an incremental decoder, so a sample boundary cannot misclassify text as binary. An oversized asset whose bounded sample looks like text is conservatively refused rather than reading an unbounded blob.

A second real-repository failure was the 12 MiB aggregate source limit: this checkout contains roughly 18.5 MB of eligible text. Increase the bounded source/snapshot limits to 24/32 MiB in generation, publication and Go admission; the 5,000-file limit, 1 MiB file limit, compact retrieval budgets and 256 MiB retained-payload quota stay unchanged. The real pinned Repomix 1.18.1 generator completed locally against the repository. The installed Zoomies workflow is updated too. Controlled generation refusals expose only fixed actionable messages; subprocess/JSON diagnostics remain suppressed. Exact initial workflow templates are retained as recognised legacy templates, so deploying the new controller does not treat unchanged earlier setups as arbitrary drift. Custom edits remain refused.

Managed CLAUDE.md/AGENTS.md instructions now name Repository/Zoomies/Both, the generated branch/path or verified database copy, MCP discovery/search/read/pack, commit pinning, freshness and explicit connection consent. Instructions are generic for any AI prompt. The wizard has Connect your assistant and Copy AI instructions; the AI Context cards retain instructions and badge Markdown after setup, including explicitly authorised reader metadata. No automatic assistant connection is claimed. A README badge checkbox defaults to enabled; readme_badge=false omits initial README changes, preserves existing text and is included in the saved configuration/hash. Nil preserves earlier configuration hashes. The existing Zoomies badge is retained.

Validation: full aicontext race suite with real pinned Repomix passed; focused context/setup/storage race checks passed; AI Context/OpenAPI API race checks passed (26.497s), including a separate instruction-membership revocation check (3.178s). Targeted vet passed. Svelte check: zero errors/warnings; changed-file lint/formatting and production builds passed. Real-controller/fake-GitHub Playwright journeys passed (2 tests, 8.2s), including copy actions after leaving the wizard and 375px overflow checks. No repaired live Actions run or live assistant-client pilot is claimed. Deployment must include the new admission limits before Zoomies can ingest its larger snapshot.

Product correction still outstanding: user-level repository enablement is required for the core feature; the current administrator gate is not the intended final design. The installation model has no owner/user association and Zoomies users have no verified GitHub identity. Do not simply change context.configure to viewer: that would grant every account configuration/write access to all shared installation repositories. Next implement scoped user-owned installations or verified GitHub repository-management identity, filtered discovery/configuration and owner controls, without exposing the global user directory or inheriting source consent. Then finish repository-only transient retrieval, Zoomies-only uploads and live pilot work. This increment delivers the failure fix, assistant guidance and badge controls, not the self-service ownership model.

Published this checkpoint by updating PR #577, based on main after the live setup merge. Remote implementation commit: 7a0328c8794c5589cbd87155098c6b00ecf132f4. Tree: 3303c86754195867ef82fa79117860a52878a590, exactly matching the validated local implementation. The installed workflow's final local pilot generated 1,508 files, 18,545,226 source bytes and 19,724,376 encoded bytes. Both final browser journeys passed (8.2s). Remote CI and the repaired live Actions run remain unverified. User-level enablement is still the next product requirement to implement safely.

## Automatic badge and maintenance checkpoint — 3 October 2026

User correction: the README AI Context badge is automatic, like the CI migration badge. Removed the setup checkbox and always add/update the marked badge on GitHub's rendered Markdown README. The legacy readme_badge field remains decodable to preserve earlier configuration hashes; new wizard saves and maintenance clear it. Existing user text is preserved. A missing/non-Markdown README remains untouched, matching CI migration behaviour.

Add Reinstall / repair, Amend and Remove on submitted AI Context repository cards, with a separate review screen. Reinstall refreshes recognised managed files and increments setup_generation so merging an otherwise identical setup triggers generation again. Amend reviews destination, exclusion and retention changes. The previous proposal must be merged or closed first; an uncertain pending publication resumes its durable review instead of being replaced. All maintenance previews pin source commit, repository identity, old proposal hash, revision, mode, desired configuration and complete file contents. Applying a stale approval is refused. Claims atomically freeze the replacement config and close source availability before GitHub writes; publication uses the existing leased/idempotent Git tree/commit/PR path with operation-specific titles. No schema migration or dependency was added.

Removal immediately disables Zoomies ingestion and purges local snapshots, source membership and per-connection consent. The reviewed removal PR deletes recognised operational files and removes only marked sections from README/CLAUDE.md/AGENTS.md. Ordinary surrounding text is preserved; ambiguous markers, non-regular files, binary/unbounded content and unknown operational ownership are refused. The historical generated output branch remains. Merge the removal PR to stop the repository workflow. Reinstall remains available after the removal proposal is merged/closed and does not resurrect reader/connection grants. Revision guards prevent an old in-flight ingestion from republishing source after removal.

Live evidence: PR #577 is merged, main b9489f738d3afa98559634786d4e16c87dd78a44. The subsequent Zoomies AI Context run https://github.com/eyupio/zoomies/actions/runs/37112029319 succeeded in both generation and publication. The earlier run 37110107009 remains historical failure. The deployed controller/database was not inspected, so no claim that the live WebUI has these maintenance controls or the latest admission limits. Merge and deploy this follow-up to expose recovery controls.

Validation: maintenance API lifecycle passed through reinstall → amend → remove → reinstall, including lost GitHub PR responses, same-PR retries, rejected open proposals, wrong approval hashes, preview non-mutation, generation changes, file deletion and user README preservation. Source/planner/GitHub/store race checks and targeted AI Context/OpenAPI API race tests passed. Removal store checks cover atomic conflict rollback, cached source/member/connection-consent purge and rejection of old or disabled publications. Targeted vet passed. Svelte check reported zero errors/warnings, production builds and lint passed. Three real-controller/fake-GitHub Playwright journeys passed (9.9s), including maintenance settings/review, publication retry, removal effects, safe rendering and 375px layout. Mobile screenshot visually inspected. The broader API suite was blocked by automatic approval review for its external tailcat.dev request; no bypass attempted.

Still outstanding from the product correction: safely scoped user-level enablement/ownership (current admin gate remains), repository-only transient retrieval, Zoomies-only uploads and live assistant pilot. This maintenance increment does not claim those are complete.

Published the maintenance checkpoint in PR #579: https://github.com/eyupio/zoomies/pull/579. Implementation commit 2ec3829f1e7c05d57b86379af10037dc4bf47a59 has tree bde4e5cccc80dff32303a32c65f941f28e81c361, exactly matching the validated local code. The PR is open; merge/deployment and remote CI completion are not claimed.

## Repository-only transient retrieval checkpoint — 3 October 2026

Repository-only output now serves source through the same four REST/MCP routes. `Controller.verifyAIContext` (behind `RefreshAIContext` and `VerifiedAIContextSnapshot`) runs the unchanged live access, merge, workflow and publication checks. For Both it still stores the snapshot. For Repository it returns the verified snapshot in memory for that request only and calls the new `store.ConfirmAIContextTransient`, which marks the repository available and writes freshness (commit and digest) but no payload row. The digest is the hash of the marshalled snapshot, so page identity matches Both. Zoomies-only still reports unavailable. A failed check closes availability and says no copy is kept.

Cost to know: each repository-only request, and each five-minute background check, downloads the generated branch (up to the 32 MiB snapshot bound) because nothing is cached. If that proves heavy, add a cheap "published commit unchanged" probe before the full read; deliberately not done here.

Changed paths: `internal/controller/ai_context_ingestion.go`, `internal/store/queries_ai_context_snapshots.go`, `internal/api/ai_context_source_test.go`. No migration, dependency, OpenAPI or UI change.

Validation: `go test -race -count=1 -run 'TestRepositoryOnlyRetrieval|TestContextSource|TestContextMCP' ./internal/api` passed (18.2s). The new test checks the read succeeds, freshness is recorded with no stored snapshot, the page's snapshot id equals the recorded digest, and removing GitHub Contents access stops serving. `go build ./...`, vet of controller/store/api and gofmt passed. Full-suite and store/auth regressions were not rerun; the earlier outbound-test restriction still applies. No live pilot.

Next concrete action: user-level enablement/ownership (see the product correction above), then Zoomies-only OIDC upload, the context browser and the live assistant pilot.

## Installation owners checkpoint — 3 October 2026

User decision: non-admins enable repositories through **installation ownership** delegated by an administrator. There is no GitHub identity link yet. Migration 0066 adds `ai_context_installation_owners`, which cascades on installation or user deletion; a disabled user owns nothing. The new `context.manage` action is viewer-level and only a coarse gate. Every configuration handler then calls `contextInstallationAccess` / `contextRepositoryAccess` (`internal/api/handlers_ai_context_owners.go`): administrators pass, signed-in owners pass for their own installation, and everyone else, including all tokens and OAuth connections, gets a 404. The 404 does not reveal whether an installation or draft exists. Owners list only their own installations' repositories. They may add or remove only themselves as readers: the members GET shows them only themselves, and a PUT keeps everyone else's membership. Ownership grants no source access or connection consent. Admin-only `GET/PUT /ai-context/installations/{id}/owners` (max 50, explicit array) and `GET /ai-context/installations` (manageable list) are new. `TestRoleAuthority` has an explicit `ownershipChecked` exemption for `context.manage`.

UI: the AI Context page uses `/ai-context/installations` to decide whether to show configuration controls. Admins get an "Installation owners" panel and dialog. The setup route admits owners. For owners, the wizard skips the user directory and offers only "Me" as a reader.

Validation:
- Go `-race` passed: API owner, AI Context, source, role, scope and spec tests (127.9s); store owner, context and migration tests (79.6s).
- auth and docs package tests passed; vet and gofmt clean.
- Svelte check found 0 errors; prettier and eslint clean.
- Playwright `ai-context.spec.ts` passed all 4 tests against the real binary, including the new admin owner-delegation journey. Run it with `PLAYWRIGHT_CHROMIUM=/opt/pw-browsers/chromium-1194/chrome-linux/chrome`.
- The owner-side UI (non-admin session) has no browser test because the connect project runs with auth off. The owner's permissions are covered by Go integration tests.

Next: user-facing docs for AI Context (setup, ownership, readers, connecting assistants, screenshots), then Zoomies-only OIDC upload and the live pilot.
