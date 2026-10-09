# Agent readiness: the refined plan

Version 2.1 · 8 October 2026 · a review and refinement of the owner's
*agent-readiness implementation plan* (v1, seven phases), reconciled against
`main` at `88c41f6`, [kennel-club.md](kennel-club.md) and the active roadmap.
Version 2.1 reads in #714, which landed the first workflow-file checks while
version 2 was being written.

This document is a plan, not code. Nothing in it is authorised until the
packages in section 7 are added to [ROADMAP.md](../ROADMAP.md) section 8 and
ordered in section 10, which is the sole delivery-order source of truth. Where
this document and the Kennel Club record disagree, the Kennel Club record
wins: it is the approved design for everything that reads a repository's
workflow files, and this plan extends it rather than building beside it.

## 1. The verdict in short

The plan's goals are right and the safety posture is unusually careful: stable
identifiers, deterministic diagnosis before any model, read-only until the
single opt-in exception, every mutation confirmed and audited, nothing hosted,
nothing proxied. Keep all of that.

What the plan gets wrong is its picture of the repository. It was written as
if Zoomies had no diagnosis, no size advice, no workflow checks and no catalog.
It has all four, in various states of completeness, and three of the plan's
seven phases would build a second copy of something that exists or is already
designed:

| The plan says | The repository has | What changes |
| --- | --- | --- |
| Phase 2: build `zoomies why` with classes, evidence and next steps | `GET /jobs/{id}/explanation` (`internal/controller/explain.go`, 374 lines) already answers "why is this job where it is" for queued, running, waiting and finished jobs, with a summary, detail and fix; the CLI, MCP `get_job` and the job drawer all show it. A closed fault taxonomy (`internal/store/faults.go`) names eleven fleet-side failure kinds, each with its own fix sentence | Phase 2 becomes *sharpening*: a `class`, structured evidence, a bounded log excerpt, a catalog link and ordered next steps on the existing payload, and a `why` verb that reads it. No second explainer |
| Phase 3: build observed-usage size advice | `GET /label-advice`, `zoomies jobs advice`, MCP `label_advice` and the `SizeAdvice` card already recommend `too_small`, `unguaranteed` or `too_large` from the 90th percentile of at least five measured runs (`internal/scheduler/advice.go`, `history.go`) | Phase 3 becomes a small *exposure* change: show the figures the advice rests on and say "not enough data" out loud |
| Phase 4: build `zoomies audit`, a standalone workflow auditor with its own rule table | `zoomies audit` **is already the audit-log command** (`cmd/zoomies/audit.go`). Kennel Club (`internal/kennel`) is the repository-standards feature: a registry with a catalogue endpoint, waivers, coverage, MCP read tools, UI and metrics. Since #714 it **already reads workflow files**: `ci.no_timeout`, `ci.no_concurrency`, `ci.action_not_pinned` and `token.permissions_unset` ship behind `kennel.workflow_checks`, as per-repository counts, plus ten `setup.*` presence checks behind `kennel.repository_setup`. Its design record specifies the rest of Stage 3 and fix-by-pull-request as Stage 5 | Phase 4 **deepens** what #714 shipped, per-file evidence, the security checks the record still owes, and a local, offline way to run the same checks. The plan's rule IDs become Kennel check codes. There is no second rule table and no second auditor |
| Phase 1: extend "the existing problem-code table" | There is no table. Each of the 377 documented codes is a `Problem{Code: …}` literal where it is raised; the only central maps are the audience map (`problems.go:129-284`) and the status-page sentences. `docs/problem-codes.md` is hand-written and a test keeps it in step with the literals both ways | Phase 1 generates the catalog from what exists (the documented table plus Kennel Club's registry) rather than migrating 377 call sites into a struct nobody asked for |
| Phase 7.6a: an autonomy ladder over model-generated patches | Kennel Club Stage 5 is a *deterministic* fix planner: line-level edits verified by re-parsing, numbers from the job's own history, no model anywhere | Autonomy, where it is built at all, is built over deterministic fixes first. A model-generated patch is a later rung with its own gate |

Two assumptions the plan makes about the platform are false today and must be
planned as work, not relied on:

* **Two-step verification does not gate settings changes.** It is a sign-in
  factor for local accounts (`internal/auth/two_step.go`); `PATCH /settings`
  checks only the admin role. "Honours two-step for settings changes" needs a
  step-up (re-authentication) mechanism that does not exist. Section 6.3 adds it.
* **MCP tools have no confirmation step.** `apply_remedy`, `rerun_job` and
  `drain_runner` run as soon as called; what protects the fleet is that they
  never pass `confirm=true`, require `expect`/`remedy_id` from a prior read, and
  are offered only with `--allow-actions` or the operator role. The plan's
  "proposed action card" therefore lives in the chat UI, not in MCP, and the
  assistant must never be wired to the MCP tool surface directly.

And three naming decisions, taken here so the work can start:

* `zoomies audit` stays what it is. The repository checks are `zoomies kennel …`
  on a controller and `zoomies kennel check [path]` offline. The skill is
  `zoomies-kennel`, not `zoomies-audit`.
* Rule IDs are Kennel Club check codes, `area.name`, as the registry already
  writes them (`ci.no_timeout`). The plan's `zoomies.workflow.<name>` prefix is
  dropped: the registry, the API, the metrics labels and the docs already use
  the shorter form, and a second scheme for the same thing is the drift the plan
  is trying to prevent. Problem codes keep theirs (`jobs.oom_killed`).
* The catalog is one JSON file, `catalog.json`, listing both families with a
  `kind` field, because an agent should fetch one thing.

## 2. Ground rules, kept and amended

Everything in the plan's section 0 stands, with these amendments.

**0.1, the clean-room rule.** Kept in full. One addition: the rule also binds
this document and every decision record written for the work. The guard
(section 3.5) is a Go test under `internal/docs`, which is where every other
"CI will fail you on this" rule lives, plus one CI step for the commit range,
because a Go test cannot see the pull request's commits.

**0.2, conventions.** Three repository facts the plan did not know:

* `ROADMAP.md` is the sole source of truth for current scope and order. Each
  phase below is written as a package with an ID so it can be added there; a
  change of design on the way is a decision record in `roadmap/decisions/`.
* `docs/cli.md` is hand-written, not generated. `TestEveryCommandIsDocumented`
  only checks that each top-level command is mentioned. The plan's "pinned
  command reference generated from the real binary" needs a generator first
  (section 3.6).
* Settings live in the database behind a registry in `internal/config/settings.go`;
  a new setting is a row there, a row in `docs/configuration.md`, and, when it
  can be wrong, a problem code. Secrets are sealed with the instance key by the
  caller before the store sees them, and are absent from the API rather than
  starred out. Every new setting in this plan follows that.

**0.3, definition of done.** Add: *a UI change ships with its Playwright spec in
both themes and at 412px, and with the screenshots `make screenshots` recaptures
if a documented page changed.* The repository already holds itself to this;
the plan should say so.

**Vocabulary.** Findings, explanations and catalog text are never dog-themed,
whatever the Appearance setting says; the Kennel Club record already fixes
this (its names are chrome; its findings are plain). "Kennel Club" and "Best in
show" are existing product names and stay.

## 3. Phase 1: identifiers and the catalog (ZF-230)

**Goal.** Everything Zoomies can say is wrong is addressable by a stable ID, and
one fetch tells an agent what every ID means.

### 3.1 What is stable already, and what is not

Problem codes are stable by policy (`docs/problem-codes.md` says so in its first
paragraph) and the two docs tests keep the list honest. Kennel check codes are a
closed set pinned by `registry_test.go`, with a `Version` that is bumped when a
check's meaning changes. Nothing needs renumbering. What is missing is a
machine-readable form and a *fix / verify* pair per entry.

### 3.2 Do not build a problem-code struct table

Migrating 377 inline literals into a central table buys nothing the docs test
does not already give, and risks a week of churn across `internal/controller`,
`internal/config` and `internal/provider`. Instead:

* The documented table is the source of a problem code's **title**, **severity**
  and **what to do** (its existing columns). Add one column, **verify**, in the
  same hand-written voice, only to the codes an agent is likely to act on: the
  `jobs.*`, `pool.*`, `host.*`, `runners.*` and `kennel.*` rows. The rest keep
  the two columns they have; the catalog carries `verify: null` for them.
* **Category** (`capacity`, `reliability`, `security`, `configuration`, `cost`)
  and **detection** (`static`, `runtime`) are derived, not typed: a validator
  code is `configuration`/`static`; a controller code's section heading gives
  its category. A small map in the generator, with a test that every code lands
  in exactly one.

### 3.3 The Kennel registry is the rule table

The `Check` struct (`internal/kennel/check.go:84-104`) already carries code, area,
severity, the *detects* sentence and the sources it needs. Add three fields the
plan asks for, as sentences in the registry so they cannot drift from the
evaluator:

* `Fix`: what to change, in the imperative.
* `Verify`: how to see it worked (usually "Recheck, and the finding closes" or
  "the next run of the workflow shows …").
* `Docs`: the anchor on the Kennel Club page.

`GET /kennel/checks` returns them. The per-finding text stays where it is (built
by each eval function), because a finding's sentence carries numbers and the
registry's does not.

### 3.4 Generated outputs

* **`docs/catalog.json`**, written by `make generate` (which already rewrites the
  naming catalogue) from the problem-codes table and `kennel.Checks()`. CI diffs
  it like every other generated file in `.github/CLAUDE.md`. Shape: `version`,
  `generated_from` (the commit), and `entries[]` of `{id, kind: problem|check,
  title, category, severity, detection, fix, verify, docs_html, docs_md}`. A
  JSON schema beside it, and a test that the file validates.
* **Markdown mirrors.** Not one page per rule: 377 pages is a maintenance
  burden and a worse read. The HTML and Markdown URLs point at *anchors* on two
  pages: `problem-codes.md` (exists) and `kennel-club.md` (the page the Kennel
  Club record already owes, with every check as a row). The site does not serve
  its Markdown sources (`hooks/seo.py` writes `llms.txt`, an index, and nothing
  more), so `docs_md` is the page's address in the repository on the published
  commit plus the anchor, and `llms.txt` gains a line for `catalog.json`.
* **`GET /api/v1/catalog`**, viewer role, the same JSON from a running
  controller, built at startup from the embedded table so it matches the binary
  rather than the site. A test asserts the two generators produce byte-identical
  output for the same commit.

### 3.5 The provenance guard

A test, `internal/docs/provenance_test.go`, that walks every tracked file and
fails on a forbidden term, case-insensitively. The terms are assembled from
fragments inside the test so the test never contains one whole. The owner
supplies the list out of band, as the plan says; the test file documents the
*shape* of the list (vendor name, skill names, command names, rule prefix) and
nothing else. A CI step in `ci.yml`'s `changes` job runs the same fragments over
`git log --format=%B base..head`, because a Go test cannot see the pull
request's range.

### 3.6 The command reference generator

Needed by Phase 5 and useful now: `zoomies commands --output json` prints every
command, subcommand, flag, example and exit code from the dispatch tables in
`cmd/zoomies/main.go`. `make generate` writes `skills/zoomies/reference.md` from
it; a test fails when the file and the binary disagree.

### 3.7 AI Context page

One section added to `docs/ai-context.md`, *For an agent operating the fleet*,
pointing at `catalog.json`, the Kennel Club page, the two skills and the MCP
tools, with a short prompt an operator can paste. Written in the page's own
voice; no new comparison content.

**Exit criterion.** `catalog.json` validates and CI refuses a stale one; an
unknown ID in a finding or an explanation is a test failure; the guard fails on
a seeded term in a temporary copy.

## 4. Phase 2: `why`: a sharper explanation (ZF-231)

**Goal.** One question, "why did this job fail, stall or run slow", answered
with a class, evidence a person can check, the catalog entry and ordered next
steps (from the CLI, the API, MCP and the job drawer) without calling a model.

### 4.1 Build on the existing explanation

`JobExplanation` keeps every field it has. It gains:

| Field | What it is |
| --- | --- |
| `class` | One of the closed set below |
| `confidence` | `high`, `medium` or `low`, with `reason` saying what data is missing when it is not high |
| `evidence[]` | Typed facts: `{kind, label, value, unit?, ref?}`, exit code, signal, memory limit and peak, queue wait, host state at the time, the pool's plan reason, the fault kind |
| `log_excerpt` | At most N lines around the decisive line, with line numbers and a note when the log is gone; `null` when there is nothing to show. Hostile output is passed through the same control-character scrub the CLI already applies (`hostile_output_test.go`) |
| `problem_code` / `check_code` | The catalog entry, when one applies |
| `next_steps[]` | Ordered, each `{text, kind: read|change|rerun, link?}` |

The classes map onto what the controller already knows rather than a new
taxonomy: `oom` (fault `out_of_memory`), `timeout` (conclusion `timed_out`, the
one case the explainer does not classify today), `cancelled`, `queued-unmatched`
(no pool claims the labels), `queued-blocked` (a pool with no host that can place
it), `queued-capacity` (at `max_runners` or starting), `runner-startup-failure`
(faults `image`, `registration`, `backend`, `config`, `container_conflict`),
`host-lost` (`host_lost`, a quiet heartbeat), `disk` (`out_of_disk`),
`workflow-failure` (the fleet did its part), `held-by-github` (deployment
review), `running`, `succeeded` and `unknown`. `unknown` must say what was
missing.

### 4.2 Surfaces

* **CLI.** `zoomies why <job>` as a top-level verb, and `zoomies jobs why` as its
  alias so it is found where jobs live. `<job>` is a job ID, a GitHub run or job
  URL (resolved through the jobs list by run ID), or `--latest-failed`, narrowed
  by `--repo` or `--pool`. Flags `--logs N`, `--no-logs`, and the standard
  `--output json`. Exit codes: 0 diagnosed, 1 other error, 2 job not found, 3
  not enough data (class `unknown`), documented in `docs/cli.md`.
* **API.** The same route, `GET /jobs/{id}/explanation`, with the new fields; a
  `?logs=N` parameter. No second route.
* **MCP.** `get_job` already returns the explanation; it gains the new fields.
  The `log_excerpt` and every evidence `value` that quotes a log line go in a
  separate content block marked untrusted, exactly as `kennel_repository` does.
  No new tool: an agent that asks "why" already calls `get_job`.
* **UI.** The job drawer already splits into `JobOutcome` (failed),
  `UnmatchedNote` and `JobWaiting` (pending). They become one **Why** section
  that leads with the verdict sentence, then a compact evidence list (label and
  value, monospace for the numbers), then next steps as buttons where an action
  exists (Re-run, Open the pool, Open the host, Copy the prompt) and plain rows
  where it does not. The log excerpt is a collapsed block with line numbers in
  the existing log viewer's tokens. A catalog chip links the problem code to its
  anchor. On a phone the evidence list is a definition list; no table.

### 4.3 Demo fleet

`SeedDemo` seeds an OOM job with `oom_killed`, a peak and a limit; a timed-out
job; an unmatched job (exists); a job blocked by a pool with no eligible host
(from the stuck fixture's blocked pool); and a registration failure (exists).
`zoomies why --latest-failed` against `zoomies demo` is the acceptance run.

### 4.4 Tests

Table-driven over fixtures per class, including OOM with the memory valve on
and with it exhausted; golden files for the human and JSON forms; a test that
every class with a catalog entry names one that exists in `catalog.json`; a
hostile-log fixture whose "instructions" survive only as scrubbed text.

**Exit criterion.** Each seeded failure in the demo returns its class with
`high` confidence; a job the fleet never touched returns `workflow-failure`,
never `unknown`.

## 5. Phase 3: show the figures behind size advice (ZF-236)

The advice exists. What an agent, and an operator, cannot see is the data it
rests on. This phase is deliberately small.

* The advice payload gains `observed: {runs, window, cpu: {p50, p95, max},
  memory_mb: {p50, p95, max}}`, `recommended_class`, `reason`, and
  `fits: {ok, missing}`, whether any host carries the recommended class, with
  the class named when none does. Fewer than `AdviceMinRuns` runs is a row with
  `state: not_enough_data` and the count, not an absence.
* `zoomies jobs advice --window 14d` and `--repo`. The window is bounded by
  retention and the payload says which bound applied.
* The `SizeAdvice` card shows the p95 and max beside the recommendation with
  the sample size in small text, and a "not enough data yet, N of 5 runs" row
  where that is the state.
* Docs: `auto-pools.md` gains *How the figures are computed* (the p90 of
  peaks, the ×1.2 memory headroom, the ×1.5 treatment of an OOM-killed run, the
  minimum of five runs) naming the constants, and *What is deliberately not
  inferred*.

Tests: fixtures for over-, right- and under-provisioned and sparse labels;
boundary tests at the named constants.

## 6. Phase 4: Kennel Club reads workflow files (ZF-229c, extended)

**Goal.** The plan's auditor, delivered as the stage of Kennel Club that was
already designed for it, plus an offline way to run it.

### 6.0 What #714 shipped, and what it leaves

#714 (8 October) put the registry at `Version = 3` with five areas:
`exposure`, `capacity`, `setup`, `ci`, `token`, and two opt-in settings, both
off by default and both needing Contents: read only for private repositories:

* `kennel.workflow_checks` reads up to fifty default-branch workflow files of
  up to 256 KiB each (`internal/github/kennel_workflows.go`), walks them with a
  bounded `yaml.Node` inspection, and keeps **counts only** in the snapshot
  (`kennel.WorkflowFacts`: files, no-timeout jobs, non-cancelling PR workflows,
  first-party and other unpinned uses, permission-less jobs). Four checks read
  those counts: `ci.no_timeout` (warning), `ci.no_concurrency` (info),
  `ci.action_not_pinned` (warning when a third-party action is unpinned, info
  otherwise) and `token.permissions_unset` (warning on a public repository).
* `kennel.repository_setup` reads one Git tree per repository and raises ten
  informational `setup.*` findings for missing community files, CODEOWNERS,
  an updater configuration or any workflow at all.

What it deliberately does not keep is the thing the plan's auditor is for: a
finding says *"3 executable job declarations have no timeout-minutes"* and
cannot say which file or which job, because no path, job name or line enters
the stored snapshot. That is the right posture for a hostile-input boundary,
and it is why the per-finding agent prompt (6.4) and the offline check (6.3)
cannot be built on the counts alone. The remainder of Phase 4 is therefore:

1. **Evidence with a location.** A second, typed evidence kind beside `pool`
   and `run`: `{file_sha, job_index, line}`; a blob SHA, not a path, so the
   closed-grammar gate in `refs.go` still holds and the UI resolves the SHA to
   a path only at render time from the inventory it already has. With it, a
   finding can name where, and `Recheck` after a fix closes exactly it.
2. **The checks the record still owes**: `ci.target_checkout_pr_head`,
   `ci.pins_without_updater` (now partly covered by `setup.dependency_updates`,
   so it narrows to "pinned but nothing moves the pins"), `ci.workflow_unreadable`
   (a file over the limits is a coverage gap today, not a finding), and the two
   new rows in 6.1.
3. **The parser moves** from `internal/github` to `internal/kennel/workflow`,
   as the record always intended, so the offline command can use it without a
   GitHub client, and so the fuzz target lives beside the evaluator.

All three landed in #760 and #761 (9 October), with `ci.target_checkout_pr_head`
placed in the exposure area as `exposure.target_checkout_pr_head`; 6.1's
`capacity.matrix_exceeds_pool`, 6.3 and 6.4 landed in #767, with the
conversion from parser facts to evaluator facts and the path gate shared by the
controller and the offline check in `internal/kennel/offline`.

### 6.1 The rule set, reconciled

The Kennel Club record's Stage 3 and 4 rows cover most of the plan's list. Each
of the plan's rules, and where it lands:

| Plan rule | Decision | Kennel code |
| --- | --- | --- |
| job-timeout | **Shipped in #714** as a count; gains location evidence (6.0) | `ci.no_timeout` |
| cancel-superseded | **Shipped in #714** as a count; gains location evidence | `ci.no_concurrency` |
| unpinned-actions | **Shipped in #714** as a count; gains location evidence and the updater half | `ci.action_not_pinned`, `ci.pins_without_updater` |
| pull_request_target | Still owed from the record's Stage 3 | `ci.target_checkout_pr_head` |
| token-permissions | **Shipped in #714** (file half); the repository-default half is Stage 4 | `token.permissions_unset`, `token.default_write` |
| self-hosted-fork-pr | **Already shipped** from observed runs; Stage 4 sharpens | `exposure.fork_code_ran`, `exposure.public_repo_weak_pool`, `exposure.fork_approval_weak` |
| label-matches-pool | **Already shipped** for the observed half | `capacity.unserved_label`; the static half (a `runs-on` in a file no pool serves) is a new Stage 3 row, `ci.label_unserved`, info |
| secret-exposure | New, Stage 3, conservative: a `${{ secrets.* }}` interpolated into a `run:` line or passed as a command-line argument. Never a general "secret-looking string" scan | `ci.secret_on_command_line`, warning |
| queue-sensitive-matrix | New, observed, Stage 1 data: a matrix whose jobs waited together on a pool smaller than the matrix | `capacity.matrix_exceeds_pool`, info |
| fixed-sleeps, shallow-checkout, dependency-cache, duplicate-setup | **Deferred.** Each is a heuristic with a high false-positive rate, which breaks the registry's precision-over-recall rule and would be the first findings an operator learns to ignore. Revisit as `info` hints with run-history evidence (a job that *is* slow) rather than file patterns alone | - |

Two registry rules to respect: only `exposure` checks may be errors
(`TestOnlyExposureChecksCanBeErrors`), so `ci.target_checkout_pr_head` on a
public repository is either placed in the exposure area or the test and the
`kennel.exposure` problem are revisited by a decision record, recommended: the
exposure area, because that is what it is. And `kennel.Version` (3 since #714)
is bumped again, because adding location evidence changes what a stored
evaluation holds.

### 6.2 Security and the plain scenario

Each security check's registry `Detects` sentence is written as the scenario
(who can do what, to what), its `Fix` as the change, and its `Docs` anchor
links the GitHub documentation page the rule rests on. Stage 3's parser limits,
the untrusted-data posture and the membership-only join to runs are kept as
designed.

### 6.3 Offline: `zoomies kennel check [path]`

The plan's "nothing is sent anywhere" story, which Kennel Club on a controller
cannot tell because it reads through GitHub. The same parser (today
`workflow.Inspect` in `internal/kennel/workflow`, moved there from
`internal/github` by 6.0) and the same `ci.*` and `token.*` evaluators run over a local
`.github/workflows`, with the fleet-dependent and `setup.*` checks reported as
*not checked here* rather than silently absent. `--output json`, `--code a,b`, `--severity`,
`--prompts`. Exit codes: 0 no findings at the asked severity, 1 error, 4
findings. The command is the thing the `zoomies-kennel` skill calls, and it
never talks to a controller unless `--controller` is given, in which case the
runtime checks join in from the API.

The other `zoomies kennel` verbs (`overview`, `repositories`, `repository
<id>`, `checks`, `recheck <id>`) are thin readers of the existing routes; none
exist today, and a fleet without a browser should have them.

### 6.4 The prompt per finding

`--prompts`, and a **Copy prompt for your coding agent** button on every
finding in the UI, render a template from the finding: the code, the file and
line, the evidence, the registry `Fix`, and an instruction to read the file's
history before proposing a minimal change. The template lives beside the
registry and is tested for every check. Evidence inside the prompt is quoted in
a fenced block headed as repository data, so the prompt itself keeps the
untrusted-data posture.

### 6.5 Tests

Everything the Kennel Club record lists for Stage 3 (fixtures per check,
negative fixtures, the fuzz target, the dogfooding test against this
repository's own workflows), plus golden JSON for `kennel check`, and the
catalog cross-check from Phase 1.

## 7. Phase 5: skills (ZF-233)

Two skills under `skills/` at the repository root: `skills/zoomies/` and
`skills/zoomies-kennel/`. (`.claude/skills/` already holds the repository's
*maintenance* skills, `babysit` and `steward`; product skills must not be
mixed in with them.) Each is self-contained, with frontmatter that the common
installers read and a test, `internal/docs/skills_test.go`, that checks
frontmatter, dangling references, absolute paths and length.

**`zoomies`.** Routing: live fleet, jobs and capacity → the CLI or MCP;
changing GitHub state → `gh`; workflow files → `zoomies-kennel`. Preflight:
`zoomies status` succeeds and names the controller, else stop and say which of
"not installed / not signed in / not reachable" it is. The pinned reference from
section 3.6. A *mutating commands* list (`drain`, `pools`, `hosts edit`,
`size-pins`, `tune`, `apply-remedy` and the rest) each requiring the agent to
show the command and ask before running it, in the same spirit as the MCP
tools' `expect` argument.

**`zoomies-kennel`.** Runs `zoomies kennel check --output json --prompts`,
summarises the findings with security first, asks which to act on, and then
uses each chosen finding's prompt. Never edits a workflow file without a
selection. States what it read and that nothing left the machine.

Install documentation in the README and `docs/cli.md`, using the path
`eyupio/zoomies`. Manual smoke test in two agents, recorded in the pull request.

## 8. Phase 6: documentation (ZF-234)

New: `docs/kennel-club.md` (owed by the Kennel record anyway; every check a row,
generated from the registry), a *Why a job failed* section on `queued-job.md`
or a sibling page, and the skills page. Updated: `ai-context.md`,
`connect-claude.md` (the richer `get_job`), `cli.md` (`why`, `kennel`,
`commands`), `api-surface.md`, `problem-codes.md` (the verify column), the FAQ
(two entries). The CLI reference is still hand-written at this point; the
generated `reference.md` is the skill's, and a test keeps the two from
contradicting each other on command names.

## 9. Phase 7: the assistant (ZF-235, in slices)

This is where the refinement is largest, because "it just works" and the
plan's two provider families pull in opposite directions.

### 9.1 Principles, kept

Operator-owned, off by default, deterministic first, untrusted input delimited,
a human confirms every mutation. All kept. Add one: **the assistant is a reader
of the same payloads the UI reads.** It calls the REST API as the signed-in
user, so it can never see or do more than that person, and every number it
quotes came from a route a test covers.

### 9.2 Providers: one family first

**Build family A (direct API) and defer family B (driving an agent CLI).**

The CLI bridge has the problem that the controller is a server process (in
the compose deployment it is a container with no agent CLI, no browser and no
interactive session) so "installed and signed in on the host" is rarely true
where the controller runs, and "the tool's own permission prompts left on"
cannot be honoured from a non-interactive process. Worse, it inverts the
direction the product already supports: a person's agent CLI already connects
*to* Zoomies over MCP with OAuth, acting as them, with their subscription and
their tool's own permission prompts. That is the subscription story, and it
works today.

So the subscription answer is: **use your agent from your machine, connected
to the controller over MCP** (section 9.9 keeps the gate on anything more). The
in-UI assistant is for the operator who wants an answer on the page in front of
them, and it talks to a model the operator configures:

* **OpenAI-compatible endpoint** first: base URL, optional key, model name.
  One adapter covers Ollama, LM Studio, vLLM, llama.cpp, OpenRouter and the
  gateways. It is the local path and the "it just works" path.
* **Anthropic API** and **OpenAI API** as thin adapters over the same interface.

The provider interface is small: `Chat(ctx, request) (stream, error)` with
tool calls, usage and cancellation, plus `Check(ctx)`. Contract tests run every
adapter against a fake server. Nothing else may import a provider package but
`internal/assistant`.

**Egress.** `security.allow_private_egress` exists and is off by default for
good reason. A provider at a loopback or private address is the *normal* case
for a local model, so the assistant's provider dial gets its own allow rule
scoped to the configured provider address, not a blanket relaxation, and the
Security page gets the row.

### 9.3 First-run experience

The panel's empty state is the setup, not a link to Settings:

1. Open the assistant. If no provider is configured, the panel shows three
   cards: **Local model** (it probes `http://localhost:11434` and the compose
   service name, and if something answers, fills the form and says what it
   found), **Anthropic**, **OpenAI or compatible**. An administrator can finish
   here; anyone else sees "an administrator sets this up" and who that is.
2. **Test** runs `Check` and a one-token completion, and reports the model,
   the latency and whether usage is reported, before **Save** is enabled.
3. The first conversation opens with the current page already attached as a
   context chip and three suggested questions that call the deterministic
   features: *Why did this job fail?*, *What does Kennel Club flag here?*,
   *Which pools are the wrong size?*

The demo fleet ships with a built-in fake provider so all of this can be
explored with no account and no network, and the demo's announcement says so.

### 9.4 Settings page

A new settings page, **Assistant**, in the Controller group of
`web/src/lib/settings/pages.ts`, admin only, listed with a lock for everyone
else as the rail already does. Providers as cards: name, kind, address, model,
last check and its result, default badge, **Test**, **Set as default**,
**Disable**, **Remove** (confirm dialog, typed name; the existing pattern).
The key field is write-only and shows "set, never shown" afterwards. Below the
cards: the data classes each provider may receive (section 9.6) as switches
with the default and the consequence in one line each; the **local models
only** switch; limits.

### 9.5 Tools and the confirmation card

The assistant's tools are a thin layer over REST, labelled `read` or `mutate`
in code, with a test that no `mutate` tool can execute without a recorded
confirmation. Read tools: the explanation, the catalog, Kennel findings, label
advice, problems, the fleet lists, a bounded runner log. Mutate tools in the
first release: re-run a job, drain a runner, apply a remedy. Pool and host
edits come later, with `expect`, as the MCP tools do.

A mutate call renders a **proposed action card** in the thread: what will
happen, to what, with the exact parameters, and **Confirm** / **Dismiss**. The
confirmation is a `POST /assistant/actions/{id}/confirm` carrying the action's
hash, so a card cannot be confirmed for different parameters than it showed.
The audit row names the user, the conversation, the message and the hash.

### 9.6 Data handling

As the plan says, with the pieces placed:

* **Redaction** is one tested table in `internal/assistant/redact.go`: GitHub's
  `***`, common token shapes, private keys, `Authorization:` headers, URLs with
  credentials. Applied to every tool result and to every attached context
  before it leaves the controller. The *what was shared* view shows the
  redacted form, so what the person sees is what the model saw.
* **Data classes** per provider: job logs, workflow file contents, repository
  names, host and network details. Defaults: a local provider gets all four; a
  hosted one gets none of the first two until switched on, and the switch's
  line says what it allows.
* **Local-only mode** is enforced in the dialer, by address class, and tested
  against a hostname that resolves to a public address.
* **Fork-originated data** never reaches a mutate tool or a fix flow; the job
  record already knows whether a run came from a fork.
* **Limits**: messages per user per hour, tokens per request, an optional
  monthly ceiling per instance, each a setting with a problem code when hit.
* **A problem** in the drawer when the assistant is on with a hosted provider
  and logs or files are allowed, in the same style as the dangerous toggles on
  the Security page, which is where it is also documented.

### 9.7 The panel

A `Drawer` (the component the problems drawer uses), `lg` width, opened from a
top-bar button, from the command palette (**Ask the assistant**) and with a
`g a` chord. Context chips for the current job, runner, host, pool, repository
or problem, added from the page and removable. Streaming over the existing SSE
stream as `assistant.delta` events scoped to the conversation; a stop button;
retry on the last message. Conversations are the user's own, stored in the
database with a delete, and listed in a side list that collapses on a phone.
Each answer carries the provider and model, the token usage where reported, and
an expandable **what was shared**. Slash shortcuts: `/why`, `/kennel`,
`/advise`. Light and dark from tokens only; the palette's `--z-*` status colours
are not reused for assistant state (an answer is information, which is
`--z-accent`). Keyboard: Enter sends, Shift-Enter newlines, Escape closes, the
card's Confirm is a real button in tab order. Budget: the panel is a lazy route
chunk under the 80 KB allowance and nothing is added to the shell.

### 9.8 Fix pull requests and autonomy

Reordered. **Deterministic fixes first, the model second, autonomy last, and
each on its own evidence.**

1. **Kennel Club Stage 5** as designed: a person previews and confirms a
   deterministic edit; one pull request per repository; never the default
   branch; never a merge. This needs no model and ships before any of the
   below. The assistant's *Propose fix* on a finding calls it.
2. **Model-drafted patches**, behind a per-repository allow list and off by
   default, only for findings Stage 5 has no planner for. The patch must apply
   cleanly, touch only allowed paths (workflow files by default), parse, and
   pass the evaluator with no new security finding; the same `VerifyEdit`
   gate. The diff is shown; confirmation opens the pull request with a plain
   description.
3. **Autonomy levels** (the plan's 7.6a), applied first to deterministic fixes
   only, because a ladder over edits a parser can verify is a ladder worth
   climbing. The levels, graduation rules, caps, cooldown, circuit breakers,
   dry-run mode, kill switch and activity page are kept as written. Level 3
   additionally requires the base branch to have protection with a required
   check, checked live. Extending autonomy to model-drafted patches is a
   separate decision record, taken on the activity page's evidence.

Two facts to plan around: the migration wizard has no global cap on open pull
requests and does not detect an existing one, so the shared pull-request
mechanics gain both before autonomy uses them; and a GitHub App cannot limit
write access to paths, so the allowed-paths rule is Zoomies' own and the
Security page says so.

### 9.9 Step-up authentication for governance

Autonomy and provider settings are the first settings whose change should cost
more than an admin session cookie. Add a **step-up**: a `POST /auth/step-up`
that re-checks the password (and the two-step code where enrolled) and marks
the session for ten minutes; the governed routes require it, and SSO accounts
are asked to sign in again. API tokens cannot step up, so these settings are
browser-only, which is the intent. This is new platform work and a package of
its own; without it the plan's "honours two-step" sentence is not true.

### 9.10 Gate on subscription sign-in

Kept as written. Until the owner's written confirmation exists, subscription
users use their own agent over MCP, and the docs say so plainly with the setup
steps that already exist on *Connect Claude*.

### 9.11 Slices

| Slice | Contents |
| --- | --- |
| 7a | provider interface, fake provider, demo support, settings page, sealed credentials |
| 7b | OpenAI-compatible adapter (Ollama first), Anthropic and OpenAI adapters, local-only enforcement, the scoped egress rule |
| 7c | redaction table, data classes, limits, audit rows, the problems-drawer warning |
| 7d | tool layer, read/mutate labelling, the confirmation card and its endpoint |
| 7e | the panel, chips, streaming, slash shortcuts, what-was-shared, conversations |
| 7f | Kennel Club Stage 5 (if not already landed) and *Propose fix* |
| 7g | model-drafted patches behind the allow list |
| 7h | step-up authentication |
| 7i | autonomy levels over deterministic fixes, activity page, kill switch |
| 7j | docs, FAQ, Security page |

## 10. Out of scope, confirmed

Everything the plan lists, plus: driving an agent CLI from the controller
(section 9.2), per-rule Markdown pages (section 3.4), a second rule table or
a second auditor (section 6), and any heuristic check without run-history
evidence (section 6.1).

## 11. Order

1. ZF-230 catalog, guard, command generator
2. ZF-231 `why`
3. ZF-236 size-advice figures
4. ZF-229c the rest of Kennel Club Stage 3 (location evidence, the owed
   checks, the parser move) with `zoomies kennel check`
5. ZF-233 skills
6. ZF-234 docs, foldable into each of the above
7. ZF-235 the assistant, 7a → 7j, with Stage 5 and step-up as their own
   pull requests

Each is one or more pull requests, each with its acceptance evidence and a
progress row, per the roadmap's delivery rules.

## 12. Found on the way, not part of this plan

`.github/workflows/open code.yml` runs an agent action with `contents: write`,
`pull-requests: write` and `id-token: write` on any issue or review comment
containing `/oc` or `/opencode`, with no check of the commenter's association.
Anyone who could comment could trigger it. That is this repository's own
instance of the plan's `pull_request_target` concern. Fixed alongside this
record: the job now also requires the commenter's association to be owner,
member or collaborator, so a stranger's comment is ignored. The shape (a
comment-triggered job holding write tokens with no association check) is a
candidate Kennel Club check for other people's repositories.
