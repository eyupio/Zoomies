# Hand-off: the assistant (ZF-235), what is built and what is left

Written 9 October 2026; brought up to date against main at `37f7c97` on
10 October. The assistant is now called **Eli** in the product. This note is
the map of the whole package; `hand-off-ai.md` is the other session's record
of the Eli work with its verification notes, and the two agree where they
overlap. Read this, then `roadmap/plans/2026-10-09-zf-235e-eli.md` (design and
"As built" sections), then `docs/eli.md`.

## Where things stand

| Work | State | Pull requests |
| --- | --- | --- |
| Providers, adapters, dialer, Assistant settings page (slices 7a, 7b) | merged | #785 |
| Stateless chat `POST /assistant/chat` (the MVP) | merged | #799 |
| Provider presets (Ollama Cloud, OpenCode Go and Zen), model list from the provider | merged | #807, #813, #827 |
| Eli: the tool loop over the read-only fleet tools, the floating widget | merged | #811, #812, #810, #809 |
| Subscription kinds `claude_code`, `codex`, `copilot` (owner-only, host installs) | merged | #814, #815 |
| Personal providers, resizable and movable conversations, bounded PR repairs | merged | #819 |
| Providers and machines readable by Eli and MCP clients | merged | #821 |
| Redaction of credentials and email addresses (slice 7c, the redaction half) | merged | #823 |
| Movable panel, thinking indicator, playful personality | merged | #829 |
| Kennel Club Stage 5 (ZF-229e, deterministic fix by pull request) | not started | slice 7f needs it |

Main's CI runs are mostly cancelled because merges land faster than they
finish, so main's real state is thinly verified; the local gate is the one
that counts. Nothing of Eli has been run against a real model, a real Claude
Code, Codex or Copilot, or a real GitHub repair; every "as built" section in
the Eli plan says what rests on documentation alone.

## What exists, and where

* **`internal/assistant`**: `Provider` (`Chat` streaming with tool calls,
  usage, cancellation; `Check`), the `Fake`, `RunContractTests`, the dialer
  (`NewDialer`, `IsLocalAddress`), the kinds and `DefaultBaseURL`.
  `purity_test.go` holds it to the standard library and lets only
  `internal/assistant/kinds` import an adapter.
* **`internal/assistant/kinds`**: `Open(kind, Config)`, the one constructor.
  No proxy in local-only mode; no redirects followed.
* **`internal/assistant/provider`**: `sse.go`, `stream.go`, `errors.go`,
  `openai.go` (OpenAI-compatible: Ollama, LM Studio, vLLM, OpenRouter, Ollama
  Cloud, OpenCode Zen and Go, hosted OpenAI), `anthropic.go`, `models.go`
  (the `ModelLister` behind `POST /assistant/providers/models`), and the
  subscription runners `cli.go` (shared: two process slots, empty directory,
  process group, environment allowlist) with `claudecode.go`, `codex.go`,
  `copilot.go`. `assistanttest` holds the fake servers.
* **`internal/redact`**: pure; credential shapes and email addresses to
  markers. Applied in the controller to the whole conversation and to every
  tool result before the 16 KiB cut. `internal/prrepair` has its own narrower
  redaction for the repair worker.
* **`internal/config`**: `assistant.allow_private_provider` and
  `assistant.local_only` (platform scope, live), `CheckProviderURL`, the
  warning `egress.private_provider_allowed`. There are no limit settings yet.
* **`internal/store`**: migrations `0089_assistant_providers`,
  `0091_assistant_fleet_access`, `0092_assistant_provider_owner`,
  `0093_eli_repairs` (which rebuilds `assistant_providers` with `owner_id`
  and `fleet_access`, and adds `eli_github_identities`,
  `eli_repair_policies`, `eli_repairs`). Queries in
  `queries_assistant_providers.go` and the repair queries beside them.
* **`internal/controller`**: `assistant.go` (open a provider, check, record),
  `assistant_chat.go` (`ValidateAssistantChat`, `StartAssistantChat`, the
  system prompt, the limits: 40 messages, 8 KiB each, 32 KiB in all, 1024
  max tokens, five minutes), `assistant_tools.go` (the loop: six rounds,
  twelve calls, the fixed allowlist `AssistantFleetTools` of seventeen read
  tools called in process as the person, results fenced and capped),
  `assistant_models.go`, `eli_repairs.go` (the `@eli fix` and `@zoomies fix`
  comment trigger, the policy, the budget, the worker's admission).
* **`internal/api`**: `handlers_assistant.go` (the provider routes),
  `handlers_assistant_chat.go` (`POST /assistant/chat`, an event stream of
  `delta`, `usage`, `done` with the tools used and the redaction counts, and
  `error`), `assistant_toolbox.go`, `handlers_eli_repairs.go`
  (`/assistant/repairs`, `/repairs/settings`, `/repairs/consent`,
  `/repairs/policy`, `/repairs/identity`). Two provider scopes: the
  installation's under `/assistant/providers` (admin, `assistant.read` and
  `assistant.write`) and each person's under `/assistant/personal/providers`
  and `/assistant/personal/chat` (`assistant.own`, viewer, owner-checked).
  `assistant.chat` on the installation route is admin.
* **`web/src/lib/assistant`**: `EliWidget.svelte` (the floating button and
  panel on every page for any signed-in user, `E` opens it; movable to either
  side, four size presets, a resize handle, remembered per browser),
  `ConversationView.svelte`, `Markdown.svelte` with the repo's own parser
  (`markdown.ts`, no HTML injected), `EliThinking.svelte`, `EliAvatar.svelte`,
  `AskEli.svelte` with `prompts.ts` (page context handed to Eli from host,
  runner and problem views as an `EliContext`), `conversation.svelte.ts` (a
  module store: survives navigation, cleared on sign-out, never written to
  browser storage), `eli.svelte.ts`, `redaction.ts`, `tools.ts`, `movable.ts`.
* **`web/src/lib/settings`**: `AssistantPanel.svelte` (personal and
  installation provider scopes, the cards, the two switches, the repair
  policies and history, and `AssistantEliCard.svelte`),
  `AssistantProviderCard.svelte`, `AssistantProviderForm.svelte` (presets,
  the model list, the fleet-access switch, the key box write-only),
  `assistant.ts`. Specs in `web/tests/assistant.spec.ts`; units in
  `web/unit/assistant-*.test.ts`.
* **Docs**: `docs/eli.md` (set-up, subscriptions, what Eli can see, what is
  sent and hidden, limits, personal conversations, hybrid provider ownership,
  PR repairs), the Assistant rows on `docs/api-surface.md`,
  `docs/security.md` section 6 for the two switches.
* **The demo** seeds "Demo model (built in)" (kind `fake`, default, checked);
  with fleet access it reads the fleet and repeats what it found.

## Decisions in force

* Adapters are hand-rolled HTTP, no SDK. Subscriptions drive the vendor's
  own signed-in tool on a host install; the controller image cannot run them.
* Fleet data is off for every provider until an administrator turns on
  `fleet_access` on that provider; the fleet tools are read-only; a
  subscription kind never gets them.
* The subscription kinds are owner-only (`owner_id`): Test, update, make
  default, list models and chat refuse anyone else; delete stays allowed.
* Redaction runs for every provider, local ones included; counts, never
  contents, reach the answer and the audit row.
* Hybrid provider ownership (`docs/eli.md`): chat and Ask Eli use the
  person's personal default; a requested PR repair uses the requester's
  personal default; an unattended repair uses the installation provider the
  repository policy names. No fallback from a missing personal provider to
  an installation key when authentication is on.
* Provider settings and the installation scope are admin-only until step-up
  (slice 7h) exists; 9.9 then makes them browser-only and stepped-up.
* The private-address switch is the assistant's own, scoped to providers;
  the 422 names it in the `base_url` field error, and the egress check runs
  only when an address is saved. Local-only is enforced after resolution and
  drops the proxy.
* A draft check that names its saved row (`id` in the body) borrows the
  sealed key when the key box is blank.

## Mapped to the design's slices (agent-readiness.md 9.11)

| Slice | State |
| --- | --- |
| 7a, 7b providers and adapters | done, plus presets, the model list and three subscription kinds the design did not have |
| 7c data handling | redaction done (#823); **data classes, per-person limits, the drawer problem for a hosted provider with logs allowed: not done**. The `fleet_access` switch is the control until then. |
| 7d tool layer | read tools done, as an allowlist over the MCP tools called in process as the person; **mutate tools, the proposed-action card and `POST /assistant/actions/{id}/confirm`: not done** (Eli has no write tool) |
| 7e the panel | widget, conversation, page context, Markdown, movable panel done; **stored conversations with delete and a side list, context chips as removable objects, streaming over the SSE event bus as `assistant.delta`, slash shortcuts `/why` `/kennel` `/advise`, the "what was shared" view, the first-run cards on an empty provider list: not done** (the chat streams over its own response; the conversation lives in memory) |
| 7f Stage 5 and Propose fix | **Stage 5 not started**. The bounded PR repair (#819) is model-drafted, policy-gated, budgeted and never merges; it is closer to 7g than to 7f, and a finding's Propose fix does not call it. |
| 7g model-drafted patches | the PR repair worker is this in substance: strict JSON plan, six turns, eight edits, allowed paths with workflow files protected, policy and budget per repository. Not behind a per-repository allow list of findings as 9.8 words it, and not through a `VerifyEdit` gate from Kennel Club. |
| 7h step-up authentication | not started |
| 7i autonomy | automatic repair of a failed PR job is a separate opt-in on the policy with a budget; no levels, graduation, cooldown, circuit breaker, dry run, kill switch or activity page as 9.8 describes |
| 7j docs | `docs/eli.md` exists; `docs/security.md` has the switches; the problems-drawer warning and the FAQ entry are not written |
| 9.10 subscription gate | the owner decided: one person's own subscription through the vendor's tool, owner-only, on a host install. Routing other people through one plan stays refused. |

## Deferred minors from the 7a and 7b review, still open on main

1. `internal/assistant/dial.go`: the comment says its prefix list mirrors
   `internal/config/egress.go`'s; it omits NAT64 and 6to4 on purpose. Say so.
2. `handlers_assistant.go`, set-default: the re-read error is discarded; a nil
   row would panic in the view.
3. `provider/openai.go` Check: `GET /models` is a hard precondition; some
   gateways serve only chat completions.
4. `provider/errors.go`: a 401 with no key configured should say "needs an API
   key", not "refused the key".
5. `AssistantProviderCard.svelte`: Disabled and No key use the draining and
   pending status tones; `neutral` follows `web/AGENTS.md` more closely.
6. The Local badge is literal-only; a private hostname shows none.
7. `AssistantPanel.svelte`: toast titles are raw setting keys.
8. A base URL with userinfo is accepted and rendered back; refuse it.
9. If sealing fails after a create, a keyless row remains with a 500.
10. The demo's assistant seed is not idempotent against a half-seeded
    database.

## What a new session should do first

1. Read `hand-off-ai.md` sections 2b and 2c and the Eli plan's "As built"
   sections for what is unverified, then run the local gate (below) on main:
   its CI is mostly cancelled.
2. Fix the records: `roadmap/progress.md`'s ZF-235 row still describes only
   7a and 7b (the duplicate `not_started` row below it is removed in the
   pull request that carries this note), and `ROADMAP.md`'s "Started, 9
   October" paragraph under ZF-235 stops at #785. Both need #799 to #829.
3. Decide with the owner (clickable questions) what comes next, in this
   order of value:
   * the rest of 7c: data classes per provider (a migration), per-person
     limits as settings with problem codes, and the drawer problem for a
     hosted provider with fleet access on;
   * stored conversations (7e; a migration) and the "what was shared" view,
     which redaction now makes meaningful;
   * mutate tools with the confirmation card (7d), which is the first step
     towards any autonomy over the fleet;
   * Kennel Club Stage 5 (ZF-229e) and a finding's Propose fix (7f), which
     should reuse the repair worker's publication path;
   * step-up (7h) before provider settings go beyond admin cookies.
4. Try it against a real model once: `zoomies demo` is enough for the loop;
   Ollama at `http://127.0.0.1:11434/v1` needs
   `assistant.allow_private_provider` on. The subscription kinds need a host
   with the tool installed and signed in.

## How the work has been run

1. `superpowers:brainstorming` with the owner's decisions asked as clickable
   questions (`AskUserQuestion`), one at a time; a schema migration is asked
   about before it is written (the steward skill requires it).
2. `superpowers:writing-plans` to `roadmap/plans/<date>-<package>.md`, with
   Global Constraints, Review Focus and a Decisions section; commit it.
3. `superpowers:executing-plans` natively: TDD on every task (watch it fail),
   the ledger in `.superpowers/sdd/<plan>/progress.md`, a `Ruling:` line for
   every deviation. The other sessions also mutation-check each rule
   (remove or invert it, see the test fail) and say in the PR body what was
   not verified.
4. One fresh reviewer on the most capable model over the review package,
   then one fix pass (each fix red then green); minors to the ledger.
5. Push, open a draft pull request with a *How to verify* section, subscribe
   to it, arm one `send_later` check-in. The owner marks it ready and merges,
   often within seconds; merge conflicts and CI are the session's to fix.
6. Restart the branch from main, delete the plan workspace, report rulings
   and deferred minors.

## The local gate

`make build-nogui`, `make fmt`, `make lint`, `go test` over the touched
packages (the assistant tree, `internal/api`, `internal/controller`,
`internal/store`, `internal/config`, `internal/docs`, `cmd/zoomies`),
`cd web && npm run check && npm run test:unit`, `go mod tidy` leaving
`go.mod` unchanged, `python3 -m mkdocs build --strict` from the repo root.
After a spec edit: `go run internal/api/gen_openapi.go` and `make openapi`.
After a `docs/problem-codes.md` row: `make generate`.

## Gotchas that cost time

* `go build ./...` fails on a clean checkout until `make build-nogui` writes
  the placeholder webdist. `make build VERSION=dev` from the repo root makes
  the real build Playwright needs; `make build-nogui` puts the placeholder
  back.
* Playwright: either `PLAYWRIGHT_CHROMIUM=/opt/pw-browsers/chromium npx
  playwright test --project=chromium` (the other session's way) or a local
  wrapper config spreading the base one with
  `launchOptions.executablePath: '/opt/pw-browsers/chromium-1194/chrome-linux/chrome'`,
  deleted before committing. The default project is `chromium`; `mobile`
  runs the same specs on a phone. Playwright reuses a server already on its
  port outside CI, so kill a stray one or you test an old binary.
* Form fields: `Field` and `Input` link label to control only through a
  shared explicit `id`; without it the accessible name is the placeholder.
* Design tokens: `web/unit/design-tokens.test.ts` fails on any `--z-*` name
  not in `web/src/lib/styles/tokens.css` (text sizes are `--z-text-sm`,
  `base`, `lg`; there is no `--z-warning`).
* A merge conflict in `openapi_spec.go`, `catalog.json` or `schema.d.ts` is
  resolved by taking either side and regenerating.
* A new migration must also be appended to the shipped list in
  `internal/store/migrations_test.go`. The next number is 0094.
* A new setting needs the struct field, the registry row, a row in
  `docs/configuration.md`, its section in `SectionOrder`, and, if platform
  scoped, its section in `settings_scope_test.go`.
* No em dash anywhere (`internal/docs/no_em_dash_test.go`, and a spaced one
  has turned main red twice); British spelling; commit messages are
  imperative sentences with the two attribution trailers; no model
  identifiers in anything pushed.
* The Go build cache can fill the disk allowance (`go clean -cache`); the
  full race suite needs `-timeout 40m`.
* The Bash safety check refuses `rm` inside `sh -c`; run removals as plain
  commands.

## Open for the owner

* The order above: the rest of 7c, stored conversations, mutate tools with
  the card, Stage 5, step-up.
* The migrations those need (data classes on providers; conversations and
  messages; actions).
* Whether the repair worker's redaction moves onto `internal/redact`.
* Gate row 15's second-agent smoke test for the skills.
* Reconciling the design's 9.8 and 9.10 with what was decided for Eli
  (subscriptions through the vendor's tool, owner-only; repairs before
  Stage 5), so `roadmap/agent-readiness.md` says what the product does.
