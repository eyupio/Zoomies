# Eli review: what was built, what competitors ship, what to do next

Reviewed 10 October 2026, against the code on `main` after #847 and the public
documentation of the products named. This is a reading of code and
documentation, not a benchmark or a user study, and nothing in Eli has yet been
run against a real model, so every judgement about answer quality is provisional
until the evaluation harness in track E exists. "Not found" is not a claim that
a feature is absent.

## What Eli is today

Eli is two subsystems that share only the provider abstraction and the
`assistant_providers` table.

**Chat.** The browser keeps the conversation in memory only
(`web/src/lib/assistant/conversation.svelte.ts`) and sends the whole history to
`POST /assistant/personal/chat`, which streams frames back. The controller
validates it (40 messages, 32 KiB), redacts it, chooses a provider and runs the
tool loop in `internal/controller/assistant_tools.go`: six rounds, twelve calls,
30 seconds per call, 16 KiB per result, every result fenced as the fleet's data.
The toolbox is the MCP server itself, built in process with the chatting
person's identity and no leave to offer a tool that changes anything, then
narrowed to the names in `AssistantFleetTools`. A test forces every new MCP read
tool to be put on that list or named as left off. Nothing is duplicated between
Eli and `/mcp`, which is the right shape to build on.

**PR repairs.** A worker in `internal/controller/eli_repairs.go` takes a
`/eli fix` comment, or a failed PR job under an opt-in policy, reads the PR's
source through the GitHub App, asks the model for a strict JSON plan of at most
eight edits (`internal/prrepair`), pushes one non-force commit and watches the
checks for an hour. It never merges, never runs code and refuses forks, closed
PRs and default branches.

What it does not have:

* **Situational context.** The system prompt is two static strings. The model
  is not told who is asking, which page they are on, what the problems drawer
  shows, how big the fleet is or what time it is. It does not receive the MCP
  server's own instructions or the tools' titles.
* **Memory.** No conversation is stored; a reload ends it, and a long
  investigation ends at the 40-message wall.
* **Reach.** Ask Eli exists on problem items, runner detail and host detail.
  It is absent from the job drawer, pools, Kennel findings, repositories,
  providers and the Usage page.
* **Tools for subscription providers.** Claude Code, Codex and Copilot run as
  subprocesses with the conversation flattened to text, and are never offered
  the fleet.
* **Any write.** Slice 7d's mutate tools and confirmation card, 7e's stored
  conversations, 7f's deterministic Kennel fixes, 7h's step-up and 7i's autonomy
  are unstarted.
* **Evidence.** Nothing has been run against a real model
  (`roadmap/plans/2026-10-09-zf-235e-eli.md`).

### Defects found, and fixed with this review

1. The fence kept the **first** 16 KiB of a tool result. For a runner's log that
   threw away the end, where the failure is, while the MCP tool had been careful
   to keep the end. The fence now keeps the end, with `mcp.KeepEnd`.
2. `CallTool` joined a tool's content blocks into one text, so the block a tool
   sets apart as a stranger's words (a job's log excerpt, a repository's
   evidence) collapsed into the fence with the notice that warned of it. Blocks
   now reach Eli apart and are fenced one by one.
3. The Ask Eli prompt told the model the snapshot was "not live access" even
   when its provider had fleet tools, which talked it out of checking. It now
   says the facts are what the page showed and that the tools are the live view.
4. A person with no default provider could chat (the page picks the first
   usable one) but every repair they asked for failed for want of a default. The
   controller now follows the page's rule.
5. The catalog, which the docs tell an agent to read first, had no MCP tool, so
   an agent connected through OAuth (good on `/mcp` only) could not read it.
   `get_catalog` wraps `GET /catalog` as an index by category, one entry by
   code, or one category with titles, and is on Eli's list.
6. Comments and docs that described the chat before redaction and tools existed
   were corrected, with the counts in `hand-off-ai.md` and the ZF-235 paragraph.

## What competitors ship

| Capability | StarSling | Blacksmith | Depot (for reference) | Zoomies and Eli |
| --- | --- | --- | --- | --- |
| A chat over fleet state for a person | None | None | None; Sherlock is a button that analyses one job | **Yes**, as the signed-in person, read-only |
| Why a job failed, without a model | `sling why`, server-side classification | A PR comment with the parsed test failures | `depot ci diagnose --output json`, model-written, with `next_commands` | `zoomies why` and `GET /jobs/{id}/explanation`: a closed set of classes, confidence, typed evidence, the catalog code and ordered next steps |
| An agent-first CLI | `sling --agent`: JSON, no prompts, pinned flags and exit codes | JSON by default, organisation tokens for agents | `--output json` on the diagnose, logs and ssh commands | `--output json` exists; the contract (no prompts off a TTY, stable exit codes, a `next_commands` field) is not written down as one |
| Installable skills | Four MIT skills, `npx skills add starslingdev/skills` | A generated skill with Testbox | Four skills, `npx skills add depot/skills` | Two skills in `skills/`, copied by hand; `npx skills add eyupio/zoomies` is in a plan and in no published doc |
| An MCP server | None | None official; a community one scrapes a browser cookie | None official; two community servers | **Yes**: stdio and Streamable HTTP, OAuth 2.1, 27 to 31 tools. WarpBuild's hosted MCP is the only vendor one, write-capable, with no documented guard rails |
| Autonomous fixes | Optimisation PRs verified against a control arm with a confidence interval, and memory across runs | Codesmith: `@codesmith` on a PR or in Slack, pushes fixes while CI fails, pauses at $200, never merges | A fix-CI recipe with an attempt cap | Eli repairs: mention or policy, budgeted per repository and person, never merges |
| Failure fingerprints and flakiness | None | Flaky and slow tests in the UI, undocumented | A hash of error type, message and first frame | None |
| Alerts into chat tools | None | Slack monitors on result, log pattern and duration | None | None |
| Analytics a model could narrate | Telemetry in ClickHouse the agent queries | Logs, VM metrics, test analytics, p99 and cost by repository | Analytics in the API and CLI, CSV export | Stats, job stats, the usage and cost ledger, samples, Prometheus; **usage and cost have no MCP tool** |

Sources, read on 10 October 2026: starsling.dev and docs.starsling.dev (the
agent-loop, sling CLI, skills and pre-seed posts), docs.blacksmith.sh (CLI,
Testbox, Codesmith, observability), the TechCrunch report of Blacksmith's
Series B, depot.dev's changelog and coding-agents guide, warpbuild.com's MCP
page and comparison, namespace.so's changelog, runs-on.com, and the community
MCP servers on GitHub.

**Worth copying.** Bounded JSON payloads with suggested next commands instead
of raw logs. Skills that teach the CLI's exact command surface, installed by
one command. Explicit attempt caps and never-merge rules. A comment on the PR,
posted by the platform, that says what failed.

**Worth avoiding.** A write-capable MCP server with no documented guard rails.
An undocumented API that forces a cookie-scraping community server. Agents an
operator can neither configure nor see.

**Eli's position.** No competitor has a conversational assistant over fleet
state. StarSling and Blacksmith sell hosted runners and bet on agents that run
CI; Depot explains one job at a time. The ground Eli holds, and should keep, is
the operator's assistant that reads the whole fleet as that person, explains
with the controller's own deterministic reasons, and, once 7d lands, proposes a
priced remedy with the confirmation step MCP alone cannot offer.

## What to do, in five tracks

### A. Fix what is broken

Done with this review; see the defects above.

### B. Better answers without new permissions

* **Situational context in the system prompt**, built per request: who is
  asking and their role; the page they are on (the widget sends it); the
  problems drawer's counts by severity and the top three codes; hosts, pools,
  queued and running; the clock; whether fleet tools are on and which. Under a
  kilobyte. Derive the tool guidance from `mcp.Instructions` and the tools'
  titles, so the two surfaces cannot drift.
* **Ask Eli wherever a reason exists**: the job drawer (class, confidence,
  evidence and next steps from the explanation), pool detail (utilisation,
  minimum, queue wait), a Kennel finding (code and fix; each finding already
  carries an agent prompt) and the Usage page. One component, four call sites.
* **Explain this job** as a one-click action on a failed job: the deterministic
  explanation (`internal/controller/explain.go`) narrated with next steps. This
  is Depot's analyse button with a better input.
* **Stored conversations** (7e): a table, a side list, delete and resume, the
  "what was shared" view, and `/why <job>`, `/kennel <repo>` and `/advise`.
  This is the largest usability gap.
* **Starter questions from state**, computed from the problems and stats the
  page already holds, not a fixed list of eight.

### C. MCP enhancements, for Eli and external agents alike

* **Read tools for what is missing**, each viewer-gated like its route:
  `get_usage` (`/usage`, with cost where the grouping can carry it),
  `runner_timeline`, `list_workflow_runs`, `scaling_events`, `kennel_checks`
  and, for administrators, `list_audit`. Usage and cost are the loudest gap:
  every competitor narrates cost and Zoomies has the ledger.
* **Operator mutations with `expect`**: `cancel_job`, `cordon_host` (today
  only inside the administrator's `edit_host`), `kennel_recheck` and
  `set_size_pin`, under the rule that no tool ever sends `confirm`.
* **MCP resources and prompts.** Serve the catalog, the problem-code page and
  each skill's `SKILL.md` as resources, and `why_did_job_fail`,
  `review_fleet_health` and `size_this_workflow` as prompts that name the tools
  to use. `internal/mcp/server.go` handles four methods; these are two more,
  advertised in `capabilities`.
* **`outputSchema` and `structuredContent`** on `get_job`, `fleet_status`,
  `list_problems` and `job_stats`, keeping the text blocks for older clients.
* **The OAuth operator cap** (`internal/auth/mcp_oauth.go`) predates the
  administrator tools: lift it for an administrator's connection when
  `security.mcp_admin_tools` is on, or document it. Decide, do not leave the
  comment.
* **Fleet tools for subscription providers through MCP itself.** Claude Code
  takes `--mcp-config` and `--strict-mcp-config`; point it at a loopback
  `/mcp` with a short-lived token minted for the chat, scoped to Eli's read
  tools and the person's identity, revoked when the subprocess exits. Codex
  takes `--config mcp_servers.*`. That removes "cannot be given Eli's tools
  yet" for the providers most operators will actually use, with the same
  allowlist, audit rows and user-agent tagging the HTTP transport has. Copilot
  stays without tools until its CLI documents an equivalent. This needs a
  design review of the token minting before it starts.

### D. Skills and agent tooling

* **Publish the skills the way the market expects**: `npx skills add
  eyupio/zoomies`, a plugin manifest, and the skills page in the site's
  navigation. ZF-233's plan names the command; no published page does.
* **Generate an MCP tool reference into the skill** from `mcp.Definitions()`,
  with a staleness test like `TestTheCommandReferenceMatchesTheBinary`, so an
  agent that only has MCP learns the surface and the ask-first rule for every
  action tool.
* **A `zoomies-fix-ci` skill**: `zoomies why`, then a fleet failure stops and
  reports the problem code and remedy without touching code, and a workflow
  failure gets at most three attempts and a question before secrets, workflow
  files or a PR. `Job.FleetFailed()` is what makes this loop safer than the
  competitors'.
* **An agent contract for the CLI**, written down once: `--output json` on
  every read, stable exit codes, no prompt when stdin is not a terminal, and a
  `next_commands` field on `zoomies why` mirroring the explanation's
  `next_steps`.
* **Failure fingerprints**: a hash of the fault class, the normalised decisive
  log line and the job name, stored on the job and exposed in `get_job` and as
  a `job_stats` grouping. It answers "seen 14 times this week, first on
  Tuesday" and gives a flakiness signal without a test-result parser.
* **A platform comment on fleet-domain failures**, Blacksmith's pattern turned
  to Zoomies' strength: when a job fails with a fleet fault, the App says on
  the PR that the runner's host was lost and not the change, with the problem
  code, so nobody debugs their code for an hour. Opt-in per installation,
  rate-limited, text from the catalog, never model-written.

### E. Measure before trusting

* **An evaluation harness for the tool loop**: recorded scenarios (an
  unmatched label, an OOM job, a lost host, a pool at its minimum, a Kennel
  exposure finding) seeded into a `:memory:` store, questions with the expected
  tool calls and the facts an answer must contain, run against the fake provider
  in CI and against a real provider by hand (`make eval-eli`). Report tool-choice
  accuracy and how often the model said it did not know when a tool had the
  answer. Until this exists nothing in track B can be ranked with confidence.
* **Which tools the model reaches for**, aggregated from the audit rows that
  already name them, shown on the Assistant settings page.

## Order

1. Track A, this review's pull request.
2. Track C's usage tool, resources and prompts, and track D's generated tool
   reference, one pull request each.
3. Track B's situational prompt, the Ask Eli call sites and Explain this job.
4. Track E's harness, then a run against a real provider before any further
   prompt work.
5. Track B's stored conversations, after the owner's retention decision.
6. Track C's subscription providers over MCP and the operator mutations, after
   the token-minting design review.
7. Track D's published skills, the fix-CI skill, fingerprints and the fleet-failure
   comment.

Deferred on purpose: 7d's mutate tools and card wait on the owner's ordering of
7d, 7e and 7h; alerts into Slack or a webhook wait on a ruling under the egress
and delivery rules; 7f is ZF-229e's own package; a sandbox on CI hardware is not
a goal.

## Decisions for the owner

1. The order of 7d (mutate tools and the card), 7e (stored conversations) and
   7h (step-up).
2. Whether outbound alerting (Slack, a generic webhook) is allowed under
   `assistant.local_only` and delivery rules 15 and 16.
3. Whether the OAuth operator cap stays, or lifts for administrators.
4. Retention for stored conversations and for failure fingerprints.
