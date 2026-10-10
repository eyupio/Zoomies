# Hand-off: where the work stands (9 October 2026)

For the next session. Read this, then `CLAUDE.md`, then the plan or PR you are continuing.
Everything below is checked against the repository and GitHub as of the end of this session; where something was
not verified it says so.

## The standing instructions from the owner

* "Continue stacking the work and merge when green." In practice the owner marks a draft ready and merges it within
  seconds, often before CI has run, so **local verification is the real gate**. Run the affected packages' tests, the
  web lint, and `make generate` / `go run internal/api/gen_openapi.go` / `cd web && npm run generate:api` yourself.
* "Ask me any decisions with clickable answers": use AskUserQuestion for choices only the owner can make.
* Open every PR as a **draft**, then call `subscribe_pr_activity`. Keep a `send_later` safety-net check-in (50 minutes).
* Voice: British spelling, comments explain why, **no em dashes and no `--` stand-ins**, commit messages are
  imperative sentences with no prefix. Commits end with the two trailer lines the harness gives
  (`Co-Authored-By: ...` and `Claude-Session: ...`); PR bodies end with the generated-with line and the session URL.
  Never put a model identifier in anything pushed.
* Every code change is **mutation-checked** (remove or invert the rule, see the test fail). PR bodies must say what was
  **not** verified. A script pattern that works: replace an exact string, run the targeted tests, restore the file;
  mutants must compile (use `if false && ...` forms).
* Never mention the product the Kennel Club design was inspired by (clean-room rule).

## Environment quirks that cost time

* `go build ./...` fails on a clean checkout until `make build-nogui` writes the embedded UI placeholder.
* **The disk filled up** (the Go build cache reached 22 GB). `go clean -cache` fixes it.
* The pre-installed Chromium is build 1194 but this Playwright wants 1243, so `npx playwright test` fails to launch.
  The environment note says to launch with `executablePath: '/opt/pw-browsers/chromium'` instead of installing. A temporary
  config outside the repo that imports `web/playwright.config.ts` and overrides `use.launchOptions.executablePath` should
  do it; I had not got that far. **No Playwright spec written this session has been run.** CI is the check.
* `web/tests/providers.spec.ts` failed `prettier --check` on main (from #791) and `web/src/lib/api/schema.d.ts` was not
  regenerated after #791; PR #795 carries both fixes. Until #795 merges, `npm run lint` is red on main.
* Main's CI runs are mostly **cancelled** because merges land faster than CI runs, so main's real state is thinly verified.
* The worktree I used is `/tmp/claude-0/.../scratchpad/wt-kennel` and is **ephemeral**; everything worth keeping is pushed.

## What is finished and merged

* **ZF-229d (Kennel Club Stage 4)** is delivered: #779 (plan), #780 (reader), #782 (checks), #784 (settings read behind
  `kennel.settings_checks`), #787 (required-check read, `store.KennelJobsSeen`), #790 (Administration-read option for new
  organisation Apps), #792 (Protection tab), #793 (record in `roadmap/kennel-club.md`, `ROADMAP.md`, `roadmap/progress.md`).
  #781 fixed the docs shard in CI. None of it has run against a real GitHub installation (policy words, protection and rules
  answer shapes, `administration: read` in a manifest are from documentation and go-github types).
* The assistant's providers and settings page (#785, another session): provider interface, fake, three adapters,
  sealed keys, dialer, Assistant settings page. Design: `roadmap/agent-readiness.md` section 9; plan:
  `roadmap/plans/2026-10-09-zf-235a-assistant-providers.md`.

## Open work

### 1. PR #795: Contents-read option for new Apps (merged)
Merged by the owner after main was merged into it. It adds `ManifestOptions.KennelFiles` (`contents: read`, never lowering
the migration wizard's write), the installer question, `kennel_files` on `POST /installations/manifest`, a Connect dialog
switch and docs, and it fixed main's lint (`providers.spec.ts`, `schema.d.ts`). Its Playwright spec was never run locally.

### 2. The assistant chat MVP (the owner asked for "an MVP as soon as we can, then build on top")
Merged as #799 (the owner marked it ready and merged it within seconds, so its CI never ran to the end). Goal: set up a model on Settings, Assistant and talk to it.

Built and checked:
* `internal/controller/assistant_chat.go`: `ValidateAssistantChat` (roles `user`/`assistant` only, ends with a user turn,
  40 messages, 8 KiB each, 32 KiB total, UTF-8, trimmed), `StartAssistantChat` (named or default enabled provider, the
  fixed system prompt that says it cannot see the fleet, 8192 max tokens, 5 minute timeout, tool calls dropped),
  `ErrAssistantNoModel`, `AssistantChatInvalid`.
* `internal/api/handlers_assistant_chat.go`: `POST /api/v1/assistant/chat`, admin only (`auth.ActionAssistantChat`),
  answers as `text/event-stream` with `delta`, `usage`, `done`, `error` frames. 422 names the message, 409 when no model,
  502 `assistant.provider_failed` (new error code, in OpenAPI, `docs/api-surface.md`) when the model will not answer.
  Stateless: the page holds the history and sends it each time.
* `api/openapi.yaml` + regenerated `openapi_spec.go` and `web/src/lib/api/schema.d.ts`; `docs/api-surface.md` row.
* Tests: `internal/api/handlers_assistant_chat_test.go` (stream, admin-only, no model, the refusal table, 502 without the
  key, named provider), `internal/controller/assistant_chat_test.go` (limits are inclusive, trimming, dropped tool call).
  The test harness got a `readStream` option on `request` because it deliberately does not read event-stream bodies.
  **Mutation checks: 11 on the API/controller behaviour and 6 on the limits, all caught** (the one survivor, tool calls
  passed on, got its own controller test).
* UI: `web/src/lib/api/assistantStream.ts` (SSE frame parser, unit tests in `web/unit/assistant-stream.test.ts`),
  `streamAssistantChat` in `web/src/lib/api/client.ts`, `web/src/lib/settings/AssistantChat.svelte` mounted in
  `AssistantPanel.svelte` (Enter sends, Stop, New conversation, answers rendered as text only), two Playwright specs
  appended to `web/tests/assistant.spec.ts` (the demo's built-in model answers "The built-in model heard: ..." and the
  second question carries the first; a 502 shows in an alert).
* Web `eslint`, `svelte-check` (0/0), prettier and the 693 unit tests pass (except the `providers.spec.ts` prettier
  failure that is on main).

Not done:
* **The Playwright specs have never been run** and the binary was not run by hand against a real model. Next step: get a
  browser working (see above) or rely on CI, and try it once against Ollama (needs `assistant.allow_private_provider`).
* #799 is merged. Its two Playwright specs in `web/tests/assistant.spec.ts` have still never been seen to run; the first real
  run is on main. Check main's CI for `assistant.spec.ts`, or run it locally once a browser works.
* `docs/ui.md` has no sentence about the chat; the plan file `roadmap/plans/2026-10-09-zf-235c-chat-mvp.md` was not
  written; `roadmap/progress.md` ZF-235 row needs the new slice.
* No audit rows, no per-person limits, no redaction, no stored conversations, no tools: those are deliberately later.

What follows (design slices in `roadmap/agent-readiness.md` section 9.11): 7c redaction, data classes, limits, audit rows and
the problems-drawer warning (do this before sending any fleet data to a hosted model); 7d tool layer and confirmation
card; 7e the real panel (drawer, chips, stored conversations, what-was-shared); 7f Kennel Club Stage 5 and "Propose fix";
7g model-drafted patches; 7h step-up authentication; 7i autonomy and the kill switch; 7j docs.

### 2b. Provider presets and the pulled model list (branch `claude/zf-235d-presets`)
The Provider field is a dropdown whose first choice is Ollama Cloud (`https://ollama.com/v1`), then OpenCode Go
(`https://opencode.ai/zen/go/v1`), a local or other OpenAI-compatible server, Anthropic and OpenAI. Choosing one fills the
address and name where they are empty or still the previous preset's. The model is a dropdown filled from
`POST /assistant/providers/models` (optional `assistant.ModelLister`, implemented by both adapters and the fake), loaded
when the key field loses focus or on a button. The Ollama Cloud address is from Ollama's documentation; the OpenCode Go
address and key page are from third-party guides and **unverified**, and models it serves over the Anthropic Messages
format are not supported. The chat box is on Settings, Assistant, below the cards; making it easier to find is undecided.

### 2c. Eli (read this first for the assistant)
The assistant is named **Eli** (Extremely Lively Intelligence). Plan: `roadmap/plans/2026-10-09-zf-235e-eli.md`. Built:
* The conversation (`web/src/lib/assistant/`): our own Markdown parser (`markdown.ts`, no HTML is ever injected, 17 parser
  mutants killed), `ConversationView`, `Conversation` (a module-level store), `EliWidget` (floating button and panel, `E`
  opens it, mounted in `App.svelte` when `eli.available`: administrator and an enabled default provider).
* The tool loop (`internal/controller/assistant_tools.go`): up to 6 rounds and 12 calls, a fixed allowlist of 18 read tools
  (`AssistantFleetTools`), called in process as the person chatting (`inProcessAPI` with `direct`), results fenced and capped.
  `assistant_providers.fleet_access` (migration 0091) is a per-provider switch, **off by default**; with it off the model gets
  no tools. A test makes every new MCP read tool be put on the list or named as left off.
* Audit: `assistant.chat` (provider, model, fleet_access, tools, outcome, never the words) and `assistant.provider.fleet_access`.
* The browser specs now run locally: `PLAYWRIGHT_CHROMIUM=/opt/pw-browsers/chromium npx playwright test --project=chromium`.
  Playwright reuses a server already on port 8099 outside CI, so kill a stray one or you test an old binary.
* Subscription providers: `claude_code`, `codex` and `copilot` (`internal/assistant/provider/{claudecode,codex,copilot,cli}.go`,
  one shared runner in `cli.go`). Owner-only (`assistant_providers.owner_id`, migration 0092), no address, no key, no fleet access,
  refused under `assistant.local_only`, host installs only (the controller image is distroless). Claude Code is run with every
  tool off; **Codex (read-only sandbox) and Copilot (nothing pre-approved) cannot be, by the owner's decision their tool activity is
  ignored by the parser**, which is not the same as them not acting. Tested only against stand-in scripts: **never run against a real
  Claude Code, Codex, Copilot or subscription**, so press Test on a machine that has one first. Codex's API-key refusal and Copilot's
  stdin, exit codes and tool behaviour under `-p` rest on guesses where the docs are silent. OpenCode is an API-key preset only
  (Zen and Go).
* Redaction (7c): `internal/redact` hides known credential shapes and email addresses from everything sent to a model (the
  conversation, and every tool result before the 16 KiB cut), for every provider; counts only reach the `done` frame, the widget
  and the audit row. Not done: `prrepair.Redact` still has its own narrower list.
* Not done: per-person limits, stored conversations, write tools and the confirmation card.
* Nothing has been tried against a real model: how well Ollama Cloud and others choose tools is unknown.

### 3. Smaller loose ends
* Stage 3 debt: a missing Contents permission shows as "unavailable, this GitHub does not offer it", which is misleading;
  fixing it means telling a missing permission from a missing endpoint.
* Kennel Club Stage 5 (ZF-229e, fix by pull request) is not started; the owner chose Stage 4 over it earlier.
* Owner decisions still outstanding: the provenance-guard term list; whether to start more of ZF-235 beyond this MVP.
* No documentation screenshot exists for the Protection tab (`make screenshots`).
