# Hand-off: the assistant (ZF-235), slices 7c to 7j

Written 9 October 2026 at the end of the session that delivered ZF-229c,
ZF-233, ZF-234 and the first pull request of ZF-235. This is what a new
session needs to carry on, in the order it will need it.

## Where things stand

| Package | State | Pull requests |
| --- | --- | --- |
| ZF-229c part one and two (Kennel Club Stage 3, `zoomies kennel`) | merged | #761, #767 |
| ZF-233 (skills for a coding agent) | merged | #773 |
| ZF-234 (the documentation) | merged | #775 |
| ZF-235 slices 7a and 7b (providers, adapters, dialer, settings page) | merged | #785, main at `dd88bd3` |
| ZF-235 slices 7c to 7j | not started | |
| ZF-229e (Kennel Club Stage 5, the deterministic fix planner) | open, owned elsewhere | slice 7f needs it |

The design for every remaining slice is `roadmap/agent-readiness.md` section 9
(9.5 to 9.11). The plan that built 7a and 7b, with its decisions and review
focus, is `roadmap/plans/2026-10-09-zf-235a-assistant-providers.md`. The
record rows are `roadmap/progress.md` (ZF-235, `in_progress`) and
`ROADMAP.md` (the "Started, 9 October" paragraph under ZF-235).

## What 7a and 7b built, and where

* `internal/assistant`: `Provider` (`Chat` streaming with tool calls, usage,
  cancellation; `Check`), `Request`, `Message`, `Tool`, `ToolCall`, `Event`,
  `Stream`, the `Fake`, `RunContractTests` (every adapter passes it), the
  dialer `NewDialer(localOnly)` and `IsLocalAddress`, the `Kind` constants
  and `DefaultBaseURL`. `purity_test.go` holds the package to the standard
  library and lets only `internal/assistant/kinds` import an adapter.
* `internal/assistant/kinds`: `Open(kind, Config)` is the one constructor.
  It builds the HTTP client (the dialer, no proxy in local-only mode, no
  redirects followed).
* `internal/assistant/provider`: `ReadEvents` (SSE), `NewStream`,
  `StatusError` (never carries the request), `OpenAICompatible` (Ollama, LM
  Studio, vLLM, OpenRouter, hosted OpenAI) and `Anthropic` (Messages API over
  plain HTTP). `assistanttest.NewOpenAI(t)` and `NewAnthropic(t)` are fake
  servers the API tests also use.
* `internal/config`: `Assistant{AllowPrivateProvider, LocalOnly}`, the
  registry rows `assistant.allow_private_provider` and `assistant.local_only`
  (platform scope, live), `CheckProviderURL`, and the startup warning
  `egress.private_provider_allowed`.
* `internal/store`: migration `0089_assistant_providers.sql`,
  `AssistantProvider`, the queries in `queries_assistant_providers.go`
  (key by its own setter; one default by partial unique index; deleting the
  default leaves none).
* `internal/controller/assistant.go`: `OpenAssistantProvider` (opens the key,
  applies live local-only), `CheckAssistantProvider`, `CheckAssistantDraft`,
  `RecordAssistantProviderCheck`; `AssistantProviderView` in `views.go`.
* `internal/api/handlers_assistant.go` and the router block: nine admin routes
  under `/api/v1/assistant/providers` (list, create, draft check, kinds, get,
  patch, delete with the typed name, check, default). RBAC actions
  `assistant.read` and `assistant.write`, both admin. The spec is in
  `api/openapi.yaml` (tag `assistant`); `docs/api-surface.md` has the rows.
* `web/src/lib/settings/AssistantPanel.svelte`, `AssistantProviderCard.svelte`,
  `AssistantProviderForm.svelte`, `assistant.ts`; the page entry in
  `pages.ts` (Controller group, `needs: 'admin'`); the client functions at the
  end of `web/src/lib/api/client.ts`; `web/tests/assistant.spec.ts` and
  `web/unit/assistant-check-summary.test.ts`.
* The demo seeds "Demo model (built in)" (kind `fake`, default, checked) in
  `internal/controller/seed.go`; `zoomies demo` says so. The fake kind can be
  created only when `ZOOMIES_SEED_DEMO` asked for the demo
  (`controller.DemoRequested()`).

## Decisions in force (rulings made, not revisited)

* Adapters are hand-rolled HTTP, no SDK: the repo's dependency rule wins over
  the Claude API reference's default.
* Provider settings are admin-only until the step-up slice (7h) exists; then
  9.9 says they become browser-only and stepped-up.
* The egress rule is the assistant's own switch, scoped to providers;
  `security.allow_private_egress` is not consulted for them. The refusal is
  a 422 naming the switch in the `base_url` field error (no separate code);
  the egress check runs only when an address is saved, so Disable, Rename
  and re-keying work after the switch is turned off.
* Local-only is enforced in the dialer after resolution and also drops the
  proxy.
* A draft check that names its saved row (`id` in the body) borrows the sealed
  key when the key box is blank; this is what Test in the Edit dialog sends.
* A disabled provider may be the default, and disabling the default leaves a
  disabled default. The panel slice decides what "no usable default" means
  to a person and shows it.
* The two switches are platform-scoped behind an admin page; an admin
  without platform rights gets the settings API's refusal as a toast.

## Deferred minors from the reviewer (not fixed, fair game for 7c to 7e)

1. `internal/assistant/dial.go`: the comment says the prefix list mirrors
   `internal/config/egress.go`'s; it omits NAT64 and 6to4 on purpose. Say so.
2. `handlers_assistant.go`, set-default: the re-read error is discarded; a nil
   row would panic in the view. Use `s.internal`.
3. `provider/openai.go` Check: `GET /models` is a hard precondition; some
   gateways serve only chat completions. Treat a 404 there as unknown.
4. `provider/errors.go`: a 401 with no key configured should say "needs an API
   key", not "refused the key".
5. `AssistantProviderCard.svelte`: Disabled and No key use the draining and
   pending status tones; `neutral` follows `web/AGENTS.md` more closely.
6. The Local badge is literal-only; a private hostname shows none. A word on
   the card would stop its absence reading as a warning.
7. `AssistantPanel.svelte`: toast titles are raw setting keys; use the rows'
   labels.
8. A base URL with userinfo is accepted and rendered back; refuse it.
9. If sealing fails after a create, a keyless row remains with a 500.
10. The demo's assistant seed is not idempotent against a half-seeded
    database; `refuseSeedOnRealState` does not look at the table.

## The remaining slices, with what each needs

Each slice is one pull request, in this order, unless the owner says
otherwise. 9.11 is the table; the sections named are the design.

* **7c, data handling (9.6)**: `internal/assistant/redact.go`, one tested
  table (GitHub's `***`, token shapes, private keys, `Authorization:`
  headers, URLs with credentials) applied to every tool result and attached
  context before it leaves the controller. Data classes per provider (job
  logs, workflow file contents, repository names, host and network details;
  a local provider gets all four, a hosted one gets none of the first two
  until switched on): new columns on `assistant_providers`, which is a
  migration, so ask the owner before writing it (steward rule). Limits
  (messages per user per hour, tokens per request, optional monthly ceiling)
  as settings with a problem code each when hit. A problem in the drawer
  when the assistant is on with a hosted provider and logs or files are
  allowed, in the style of the dangerous toggles; document it in
  `docs/security.md` section 6 and `docs/problem-codes.md`. Audit rows for
  what was shared.
* **7d, the tool layer (9.5)**: `internal/assistant` tools as a thin layer
  over REST, labelled `read` or `mutate` in code, with a test that no mutate
  tool can execute without a recorded confirmation. Read tools: the
  explanation (`zoomies why`), the catalogue, Kennel findings, label advice,
  problems, the fleet lists, a bounded runner log. Mutate tools: re-run a
  job, drain a runner, apply a remedy. The proposed-action card's endpoint
  `POST /assistant/actions/{id}/confirm` carrying the action's hash; the
  audit row names user, conversation, message and hash. The tools must call
  the REST API as the signed-in user (9.1), which is what `internal/mcp`
  already does through its `mcp.API` interface; mirror that, do not reach
  the store.
* **7e, the panel (9.7)**: a `Drawer` (the problems drawer's component),
  `lg` width, from a top-bar button, the command palette ("Ask the
  assistant") and a `g a` chord. Context chips for the current job, runner,
  host, pool, repository or problem. Streaming over the existing SSE stream
  as `assistant.delta` events scoped to the conversation (every `*.updated`
  event is a `GET` shape; publish through the controller's helpers). Stop
  and retry. Conversations stored per user with delete and a side list that
  collapses on a phone (a migration: ask first). Provider and model on each
  answer, token usage where reported, an expandable "what was shared" in the
  redacted form. Slash shortcuts `/why`, `/kennel`, `/advise`. Tokens only;
  answer state is `--z-accent`, never a runner status colour. Keyboard:
  Enter sends, Shift-Enter newlines, Escape closes, Confirm is a real button.
  A lazy route chunk under the 80 KB allowance. The first-run cards (9.3:
  Local model probing `localhost:11434` and the compose service name,
  Anthropic, OpenAI or compatible) and the three suggested questions live
  here, not on the settings page.
* **7f, Kennel Club Stage 5 and Propose fix (9.8 item 1)**: only after
  ZF-229e lands; its planner is owned elsewhere. The assistant's Propose fix
  on a finding calls it.
* **7g, model-drafted patches (9.8 item 2)**: behind a per-repository allow
  list, off by default, only for findings Stage 5 has no planner for, through
  the same `VerifyEdit` gate.
* **7h, step-up authentication (9.9)**: `POST /auth/step-up` re-checking the
  password and two-step code, marking the session for ten minutes; the
  governed routes (providers, autonomy) require it; API tokens cannot step
  up. Platform work, its own pull request.
* **7i, autonomy levels (9.8 item 3)**: over deterministic fixes only, with
  the activity page and kill switch; level 3 needs base-branch protection
  checked live. The migration wizard first needs a global cap on open pull
  requests and detection of an existing one.
* **7j, docs, FAQ, Security page.**
* **9.10 stays closed**: subscription users use their own agent over MCP
  until the owner's written confirmation exists.

## How the work has been run

1. `superpowers:brainstorming` with the owner's decisions asked as clickable
   questions (`AskUserQuestion`), one at a time: what the pull request
   covers, any schema migration (the steward skill requires asking), then
   the short in-chat design for approval.
2. `superpowers:writing-plans` to `roadmap/plans/<date>-<package>.md`, with
   Global Constraints, Review Focus, and a Decisions section; commit it.
3. `superpowers:executing-plans` natively: TDD on every task (watch it
   fail), the ledger in `.superpowers/sdd/<plan>/progress.md`, a `Ruling:`
   line for every deviation.
4. One fresh reviewer on the most capable model over the review package,
   then one fix pass (each fix red then green), minors to the ledger.
5. Push, open a draft pull request with a *How to verify* section,
   subscribe to it, arm one `send_later` check-in. The owner marks it
   ready and merges; merge conflicts and CI are the session's to fix.
6. Restart the branch from main (`git fetch origin main && git remote prune
   origin && git checkout -B claude/brave-ride-o421h9 origin/main && git
   push -u origin claude/brave-ride-o421h9`), delete the plan workspace,
   report rulings and deferred minors.

## Gotchas that cost time

* `go build ./...` fails on a clean checkout until `make build-nogui` writes
  the placeholder webdist. `make build VERSION=dev` must run from the repo
  root, and Playwright needs that real build; `make build-nogui` replaces
  it with the placeholder again.
* Playwright: write a local wrapper `web/playwright.local.config.ts` that
  spreads the base config and sets
  `launchOptions.executablePath: '/opt/pw-browsers/chromium-1194/chrome-linux/chrome'`,
  run with `--config playwright.local.config.ts --project chromium` (and
  `--project mobile`), delete the wrapper before committing (prettier lints
  it). The default project is named `chromium`, not `desktop`.
* Form fields: the shared `Field` and `Input` link their label to the
  control only through an explicit shared `id`; without it the accessible
  name is the placeholder and `getByRole('textbox', { name })` fails.
* Design tokens: `web/unit/design-tokens.test.ts` fails on any `--z-*` name
  that does not exist in `web/src/lib/styles/tokens.css` (text sizes are
  `--z-text-sm/base/lg`, there is no `--z-warning`).
* Generated files: `go run internal/api/gen_openapi.go` and `make openapi`
  after any spec edit; `make generate` after any `docs/problem-codes.md`
  row; a merge conflict in `openapi_spec.go`, `catalog.json` or
  `schema.d.ts` is resolved by taking either side and regenerating.
* A new migration must also be appended to the shipped list in
  `internal/store/migrations_test.go`.
* A new setting needs: the struct field, the registry row, a row in
  `docs/configuration.md`, its section in `SectionOrder`, and, if
  platform-scoped, its section in `settings_scope_test.go`'s list.
* `mkdocs build --strict` must run from the repo root (`python3 -m mkdocs`).
* The full race suite needs `-timeout 40m`; the Go build cache can fill the
  disk allowance (`go clean -cache` frees it).
* The Bash safety check refuses `rm` inside `sh -c`; run removals as plain
  commands.
* No em dash anywhere (`internal/docs/no_em_dash_test.go`); British
  spelling; commit messages are imperative sentences with no prefix and the
  two attribution trailers the harness asks for; no model identifiers in
  anything pushed.
* The owner tends to mark the pull request ready and merge it while the
  main test matrix is still running; main's own run on the merge commit is
  what to watch afterwards.

## Open for the owner

* Whether 7c starts now, or ZF-229e (Stage 5) first.
* The migrations 7c and 7e need (data classes on providers; conversations
  and messages; actions).
* Gate row 15's second-agent smoke test for the skills remains the owner's.
* The written confirmation 9.10 waits on.
