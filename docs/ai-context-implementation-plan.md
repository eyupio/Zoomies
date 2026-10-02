# Zoomies AI Context — implementation plan

Date: 2 October 2026. Status: proposed implementation; no Zoomies changes made by this plan.

## Goal and agreed scope

Enable a repository once in Zoomies, approve a setup PR, and have current code context generated automatically. Claude and ChatGPT should retrieve it through Zoomies’ existing authenticated MCP endpoint, subject to live client validation. Output destinations are Zoomies, the repository, or both. Repository files, Actions jobs, outputs and the README badge carry Zoomies branding. Repomix remains credited as the generator.

Default proposal: Both, using a dedicated generated branch as the portable copy and Zoomies storage for efficient retrieval. The wizard explains the extra source copy and lets the operator choose another destination. Do not enable repositories or broaden source access silently.

Success means one app connection to Zoomies, one setup PR per repository, automatic refresh afterwards, clear freshness and failures, and no dependency on an active runner for retrieval.

## Source review and reusable foundations

Reviewed eyupio/zoomies main on 2 October 2026, tree 90a7463890631be139fb00c72e82351995c565ea. Review covered source and existing tests; no live app interoperability was established.

| Foundation | Evidence | Reuse and limitation |
| --- | --- | --- |
| Remote MCP and REST integration | internal/api/mcp.go; internal/mcp/http.go | Extend existing /mcp and route permission checks; transport is stateless request/response |
| OAuth and connections | internal/auth/mcp_oauth.go; internal/api/handlers_mcp_oauth.go | Reuse discovery, PKCE, consent, token rotation and revocation; current consent grants viewer/operator rather than repository access |
| GitHub App and repository discovery | internal/github/client.go; internal/github/migrate.go | Reuse installation credentials and repository discovery; add commit-pinned context ingestion |
| App manifest | internal/github/manifest.go | Default lacks Contents permission; migration-enabled apps request Contents/workflows/PR write |
| Webhooks | internal/controller/webhooks.go | Existing handler acts on workflow_job; add a distinct verified context-refresh path |
| MCP tools | internal/mcp/tools.go; internal/mcp/server.go | Existing tools serve fleet data; protocol currently advertises tools only |
| Log relay | internal/controller/logs.go | Live logs depend on hosts; build dedicated durable storage for context |
| Authorisation | internal/auth/rbac.go | Add explicit context actions and repository checks; fleet viewer is not source access |

Primary source: https://github.com/eyupio/zoomies/tree/main

## Repository branding and ownership

| Item | Managed name/path |
| --- | --- |
| Workflow | .github/workflows/zoomies-ai-context.yml |
| Actions display name | Zoomies AI Context |
| Generated branch | zoomies-ai-context |
| Generated files on that branch | .zoomies/ai-context/ |
| Configuration on default branch | zoomies-ai-context.config.json |
| Actions artifact | zoomies-ai-context |
| Setup PR title | Enable Zoomies AI Context |
| Instruction guidance | Zoomies AI Context section in CLAUDE.md and AGENTS.md |
| README badge label | Zoomies AI Context |

Generated manifest identifies Zoomies manager version, workflow template version, pinned Repomix version, repository identity, source branch/commit, configuration hash, generated time, file hashes and token-count method. Generated files include a managed notice and are not hand-edited.

Use managed markers around README/instruction sections. Preserve existing text and custom configuration; refuse ambiguous ownership or conflicting pre-existing files. Setup is idempotent and detects an existing open setup PR. Template upgrades arrive as reviewable PRs, never silent rewrites.

## README badge

Ship a standard GitHub Actions workflow badge labelled “Zoomies AI Context”, linked to its Actions page. This avoids requiring a public Zoomies endpoint or leaking private repository metadata. Example for Jiggered:

```markdown
[![Zoomies AI Context](https://github.com/jnnngs/jiggered/actions/workflows/zoomies-ai-context.yml/badge.svg)](https://github.com/jnnngs/jiggered/actions/workflows/zoomies-ai-context.yml)
```

Place it in a managed block near existing badges, with a short link to usage instructions. State explicitly that it reflects workflow status, not proof that context is fresh or an app is connected. Verify rendering when logged in/out for private repositories; do not promise private badges are publicly visible.

Optional later: Zoomies-branded SVG freshness badge with disabled-by-default public publication. A public badge may expose only an approved minimal state (ready/stale/failed/disabled), never source, private repository names, commit IDs or storage URLs. An anonymous badge cannot inherit a viewer’s private permissions; this is a deliberate publication setting. Never put credentials in badge URLs.

## Output destinations

| Destination | Generation/publication | Retrieval |
| --- | --- | --- |
| Repository | Workflow generates and publishes a dedicated branch | GitHub download; Zoomies MCP can read validated branch data with a bounded cache |
| Zoomies | Workflow generates and uploads to Zoomies | Durable controller-managed storage and existing MCP |
| Both | Workflow publishes branch; Zoomies ingests validated output | GitHub portability plus durable Zoomies retrieval |

Repository mode does not require durable Zoomies copies, but MCP still needs GitHub read access. Zoomies-only mode needs a secure upload mechanism; implement Actions OIDC with short-lived upload credentials before calling this mode complete. Both mode initially avoids upload credentials by ingesting the generated branch.

Snapshots are immutable and commit-addressed. Publish the manifest last or atomically move a validated snapshot into place. Never publish partial packs. Separate desired source commit, published source commit and last failure. Preserve the previous valid snapshot after failure, with its stale state visible.

## Architecture and data contract

Add focused context packages rather than mixing indexing into scheduler code:

- internal/context: snapshot validation, exclusions, bounded search/read, pack selection and generation contracts.
- internal/controller: refresh queue, reconciliation, per-installation quota/backoff and publication orchestration.
- internal/store: repository configurations, snapshot metadata, refresh state and access grants.
- internal/api: context REST routes and setup/upgrade PR operations.
- internal/mcp: thin context tools calling the authorised routes.
- web: setup wizard, repository context page, access controls and failures.

Keep metadata in SQLite and blobs in a configured context directory for the first implementation. Introduce a storage interface so object storage can follow. Specify backup inclusion and deletion behaviour; do not assume existing database backups capture filesystem blobs. Bound total storage, snapshot count, file size, decompressed size and concurrent work.

Snapshot identity includes GitHub host, installation, repository ID, source commit and configuration hash. Repository names alone are insufficient across GitHub Enterprise hosts or renames. Source packs preserve original content and line numbers; compressed summaries remain navigation aids rather than editing evidence.

## Generation and automatic refresh

Workflow triggers: relevant default-branch pushes, configuration changes and workflow_dispatch. Generated-branch commits never trigger recursive generation. Use a dedicated concurrency group and skip superseded source commits. Manual refresh remains available.

Run pinned Repomix in an isolated Actions job with explicit include/exclude rules, secret scanning and size bounds. Do not run repository build/install scripts or expose generator output in logs. Pin third-party Actions to immutable SHAs. Keep generation read-only; grant contents:write only to the separate publication job where repository output is selected.

For the first Both-mode implementation, use the generated-branch push to enqueue ingestion after adding the appropriate App subscription and handler. Reconciliation periodically checks only enabled repositories to recover missed events, permission changes and workflow drift. Resolve the installation/repository independently of untrusted event assertions; verify signatures and ignore unknown repositories. Respect shared GitHub quotas and keep refresh work lower priority than runner operations.

As an alternative where App event changes are unavailable, reconciliation supports the pilot with an explicitly documented freshness interval. It must not pretend to be instant push refresh.

## Permissions and trust boundaries

- Add context.read, context.configure, context.refresh and artifact.publish actions, plus repository membership checks. Deny source access until explicitly granted.
- Extend OAuth consent with source-access permission and selected repositories. Existing grants receive no new source privileges. Check current membership on every request; repository removal/permission revocation immediately blocks retained data.
- Reuse short-lived GitHub App credentials. Add optional Contents:read to App setup. Existing installation owners must accept permission changes; show the exact missing permission and its effect.
- Creating workflow setup PRs needs the existing migration write permissions. Use the existing machinery but do not enable broader permissions for all installations by default.
- Index only the configured trusted branch initially. Fork PRs must not publish authoritative snapshots or gain upload credentials.
- Treat source and AI-written artifacts as untrusted data. Prevent traversal, unsafe archive extraction, symlink escape and unsafe HTML rendering. Secret scanning is defence in depth; do not guarantee it catches every secret.
- Audit context reads with caller, repository, snapshot, operation and response size; do not log source, credentials or raw searches that may contain sensitive material.

## MCP retrieval and token efficiency

Proposed tools: context_overview, context_search, context_read and context_pack. Overview also discovers repositories authorised for this connection; preserve a small tool surface.

All calls accept repository identity and optional expected source commit. Responses carry minimal commit/freshness metadata, stable pagination and explicit truncation. No forced overview call for a known-file edit. Search is bounded literal search first; add richer indexing only after measuring need. Packs support a named area or validated file selection, bounded by a configurable response budget.

Keep metadata once per response; avoid verbose JSON per source line and repeated timestamps/hashes in bulk pages. Default to compact text with file/range headers. Bulk retrieval remains bounded and pageable; links to downloads are not a substitute for content an app can actually retrieve. Tools-only retrieval fits the existing protocol; MCP resources are optional later.

Benchmark targeted edits, multi-file debugging and broad reviews. Count full serialized responses and tool arguments, identify tokenizer and baseline, and show regressions. The earlier 49% targeted/18% mixed figures are illustrative, not release targets or billing claims. Verify that excerpts contain enough evidence for the task; include output quality and follow-up retrieval when measuring live tasks.

## UX and connectivity

Add a dedicated **AI Context** main navigation item at `/ai-context`, with an **Enable repositories** primary action opening `/ai-context/setup`. This is a first-class feature alongside fleet views, not a hidden settings toggle. Match existing Zoomies navigation, icons, spacing, colours, responsive layouts and accessibility conventions. Describe the feature as “Prepare repositories for AI coding assistants”; do not imply enabling it runs an AI agent or changes application code.

The landing page lists configured repositories with visibility, installation, destination, setup state, freshness and last run. Support search and filters for installation/state. Empty state explains the outcome and leads directly to the wizard. Read-only users see only repositories they may access; configuration controls require context.configure. Offer an authorised repository-level entry point from existing repository views where available, with the repository preselected.

### Enable repositories wizard

| Step | User decisions and feedback |
| --- | --- |
| 1 — Choose repositories | Choose GitHub installation; search public/private repositories; select one or several; label archived/inaccessible/already-enabled repositories; do not preselect everything |
| 2 — Check readiness | Probe required permissions, workflow availability and repository write restrictions; show exact blockers and remediation links; recheck without losing selections |
| 3 — Choose output | Repository, Zoomies or Both; concise benefits and source-copy implications; disable unavailable modes with an explanation; default to Both |
| 4 — Configure context | Default source branch, sensible exclusions, refresh settings, retention and optional advanced controls; exclude secrets/generated/dependency files by default |
| 5 — Choose access | Explicit Zoomies users/groups as supported by the implementation and source access for app connections; GitHub installation visibility alone grants no user access |
| 6 — Review setup | Preview workflow/config paths, generated branch, README badge and instruction changes; identify preserved/conflicting files; show required permissions and one PR per repository |
| 7 — Create setup PRs | Open managed setup PRs and display per-repository result/links; retain partial successes and retry only failures; opening a PR does not mean the feature is operational |

No manual token pasting for the initial repository/Both flow. Make the final action **Create setup PRs**, not “Enable AI” while merge is still outstanding. Save resumable configuration drafts server-side subject to access checks, with no credentials in browser storage. Back/Next navigation preserves inputs; server validation remains authoritative. Support keyboard focus, labelled controls, step/error announcements and a mobile single-column layout. Multi-repository setup shares defaults, permits repository overrides and exposes partial failures without duplicate PRs.

### Activation and ongoing management

Track each repository through Draft → Needs attention / Awaiting merge → Building → Ready. Later states include Stale, Failed, Paused and Access removed. Reconciliation drives state; browser polling/event updates must not be the only activation mechanism. Provide **Open PR**, **Recheck**, **Refresh**, **Repair setup**, **Upgrade workflow**, **Manage access**, **Pause** and **Remove** where applicable and authorised. Show the last successful snapshot separately from the last failed attempt.

After the first successful generation, show a clear “Repository context is ready” milestone and an **Connect an assistant** guide using the existing MCP connections page. Track connector readiness separately from repository readiness: a successful workflow is not evidence that Claude/ChatGPT is connected. Copyable MCP URL and a connection check should not expose bearer tokens. Existing connections requiring new source consent get a clear reconnect/grant-access instruction.

Repair and upgrade actions open reviewable PRs; never silently overwrite user workflows. Pausing stops refresh but does not automatically revoke access to retained snapshots; explain this and offer explicit revoke/delete controls. Removal distinguishes disabling automation, revoking access, deleting retained Zoomies data and removing managed repository files via PR. Repository outputs remain until their removal PR is merged.

Repository page shows latest source/indexed commits, age, size, token counts, last run, destination and retention. Refresh and repair actions are explicit. Connector guidance uses the existing Zoomies /mcp URL and connection settings. Validate Claude and ChatGPT independently against the operator’s actual client availability; code-level OAuth tests do not establish live compatibility. A reachable HTTPS controller and one initial app authorisation remain necessary.

## Delivery phases and acceptance gates

| Phase | Deliverables | Completion gate |
| --- | --- | --- |
| 1 — Contracts and access | Schema, repository ACLs, OAuth consent extension, permission probes, storage contract | Existing MCP grants cannot read source; cross-repository access tests pass |
| 2 — Managed repository setup | AI Context navigation, Enable repositories wizard, Both/repository workflow templates, config, instruction sections, README badge, setup/upgrade PRs | Accessible mobile wizard; resumable setup and partial retries; idempotent setup preserves user files; Jiggered setup PR builds/publishes successfully |
| 3 — Durable ingestion | Validated branch ingestion, push handling, reconciliation, freshness, quotas/retention | Restart-safe storage; incomplete/stale/racing publications handled; revocation blocks reads |
| 4 — MCP and UI pilot | Compact tools, context browser, failures, token comparison | Authorised retrieval works in live Claude and ChatGPT where available; targeted and broad measurements reported |
| 5 — Zoomies-only output | Actions OIDC exchange and scoped uploads | Valid workflow/repository/ref claims enforced; replay, expiry and wrong-destination uploads rejected |
| 6 — Assistant artifacts | Separate publish permission, versioned reports/plans, safe rendering and MCP reference tools | Explicit uploads attributed; source/AI distinction and access/retention enforced |

Phases 1–4 form the first usable pilot. All three output destinations are the planned release scope, but Zoomies-only is unavailable until phase 5 passes. Phase 6 is a follow-on rather than a dependency for code-context use.

## Validation and operational checks

Test permission denial, old OAuth grants, repository revocation, multiple installations, public/private repositories and Enterprise host identity. Test generated-branch loops, duplicate events, missed events, two competing builds, GitHub rate limiting and failed publication. Validate manifests, file hashes, commit provenance, exclusions, archive bounds and atomic storage. Test setup PR conflict detection, existing badge blocks and instruction preservation.

Add wizard end-to-end coverage for the empty state, repository filtering, missing permissions, unavailable output modes, multi-repository partial failure, draft resumption, existing PR detection and activation after merge. Verify viewer restrictions, keyboard/mobile flow, and separate repository/connector readiness. Exercise pause, access revocation, repair and removal semantics.

Run existing Zoomies MCP/OAuth/API tests alongside new context integration tests. Add one workflow end-to-end pilot on Jiggered and live connector checks. Measure controller latency during indexing, disk exhaustion handling and cleanup. Downloads and badges must not leak private content through anonymous routes, cache keys or redirects.

Rollback: disable refresh, revoke source grants and remove managed workflow via PR. Retained snapshots stay inaccessible according to policy and can be deleted independently. Existing fleet tools and scheduling must continue working.

## Jiggered transition

Keep PR https://github.com/jnnngs/jiggered/pull/55 as a reference until Zoomies templates and ingestion work. Reuse useful generator/validation concepts, replace standalone Python MCP/OAuth hosting with Zoomies integration, and apply agreed branding. Do not merge a second standalone service solely to satisfy the pilot. Review differences before replacing existing setup files; move Jiggered to the managed workflow through a concrete setup PR.

## Decisions recorded

Agreed: Zoomies-branded repository setup and outputs; README badge; dedicated AI Context navigation and Enable repositories wizard; automatic refresh; private/public repository support; existing Zoomies MCP endpoint; selectable output destinations.

Recommended implementation defaults: Both destination, dedicated zoomies-ai-context branch, standard Actions badge, trusted default branch only, explicit repository grants, compact retrieval, immutable snapshots, isolated generation. Retention limits and storage caps should be configurable and selected from pilot measurements rather than invented capacity promises.
