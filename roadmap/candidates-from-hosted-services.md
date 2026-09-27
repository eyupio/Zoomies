# Candidates from hosted runner services

Written 26 September 2026 against `main`. This is an analysis, not an
authorisation: nothing here is in [ROADMAP.md](../ROADMAP.md) until the owner
puts it there, and every ID below marked *proposed* is a suggestion, not an
assignment. It follows the method of the
[September comparison](competitive-review-2026-09.md): public documentation
compared with this repository's code and record, and "not verified" is never a
claim that something is absent.

## The comparator

[Latchkey](https://latchkey.dev) describes itself as managed, self-healing
GitHub Actions runners: runners it operates for its customers, with
dependency and layer caching, automatic retry of transient failures, warm
runners, CI analytics and notifications among its documented features. It is
a hosted service; Zoomies is self-hosted software that a team runs on its own
machines. The comparison is therefore of features, not of models: each
candidate below is judged by whether it makes a self-hosted fleet better while
keeping the principles in [architecture.md](../docs/architecture.md) — one
binary, no Kubernetes, no database server, safe defaults with every deviation
named at startup and in the problems drawer, a scheduler that explains its
decisions, and a [support matrix](support-and-measurement.md) that keeps
"tested" apart from "built".

## Checked again, 27 September 2026

Three of the entries below were out of date a day after they were written,
and the sections keep their original text so the reasoning stays readable:

* **#1, layer one.** ZF-212's recipes shipped on 26 September — Go, npm,
  pip, Maven and BuildKit `type=local` under "Using the pool cache from a
  workflow" in [configuration.md](../docs/configuration.md) — and the record
  marks it `done`. Layers two and three are unchanged.
* **#3.** "No age limit" was wrong. `scheduler.max_runner_lifetime`
  (default 6h, overridable per pool through `runner_settings`) drains any
  runner past that age that is not busy, warm minimums included, and the
  minimum is then refilled; `staleImage` drains a runner whose pool
  has moved to a different image. What the sketch has that this does not:
  age counted from idle rather than from creation, a replacement started
  *before* the drain so the pool never dips below `min_runners` for a tick,
  and a re-pulled tag with a new digest counting as stale. Worth doing only
  if one of those is asked for.
* **#8.** Built as sketched: `zoomies mcp`, a stdio MCP server that is a
  thin client of the REST API, read-only unless started with
  `--allow-actions`, with runner logs returned as a separate block marked
  untrusted. See [cli.md](../docs/cli.md#zoomies-mcp).

#11, invite links, is still open exactly as written.

## Summary

| # | Candidate | Status | Fit | Size |
| --- | --- | --- | --- | --- |
| 1 | Dependency and layer caching | Partly built; recipes shipped (ZF-212) | Modified | M (service) |
| 2 | Transient-failure retry | Fleet-caused re-run built (ZF-224); pattern retry new | Modified | M |
| 3 | Warm pools | Built, recycle included (`scheduler.max_runner_lifetime`) | Mostly met | S, if at all |
| 4 | VM-per-job isolation | Planned in outline (ZF-216 section 9, decision 28) | Yes, demand-gated | L |
| 5 | CI analytics | Largely built (Usage page, ZF-209, ZF-223) | Modified | S–M |
| 6 | Right-sizing suggestions | Data built; suggestion new | Yes | M |
| 7 | Notifications | New | Modified | M |
| 8 | MCP server | Built (`zoomies mcp`) | Modified | M |
| 9 | `zoomies run` | New | Modified | M |
| 10 | Image parity and software list | Partly built (inventory JSON, catalogue) | Yes | S–M |
| 11 | Team roles and invite links | Roles built (four, ZF-207); invites new | Modified | S |

## High value

### 1. Dependency and Docker layer caching

**Status: partly built, partly planned.** Built: the per-pool cache mounted at
`/opt/zoomies-cache` with `scope: pool | repository`, an enforced
`size_limit` and a chosen `source` (`cache:` in
[configuration.md](../docs/configuration.md), `internal/backend/cache.go`);
the kept tool cache, read-only shared at `/opt/zoomies-tools-shared` with a
writable per-runner view at `/opt/zoomies-tools`, filled from the toolchain
scan (`cache.tools`, `internal/backend/toolfarm.go`, `toolfill.go`); image
prewarm; the outer daemon's builder-cache target
`agent.docker_build_cache_mb` (`internal/backend/build_cache.go`); and
[persistent-caches.md](../docs/persistent-caches.md), which already documents
the registry-backed BuildKit cache from a workflow and the rule that "losing a
cache must only make a build slower". Planned: ZF-212, recipes for Go, npm,
Maven, pip and BuildKit `type=local` against the pool cache; its record says
the S3-compatible storage half "is a trust question before it is a feature"
and is not authorised. New: an `actions/cache`-compatible cache service and a
pull-through registry mirror.

**Fit: modified.** Worth doing, in three layers ordered by trust cost:

1. **ZF-212 as written** — recipes, no code. Cheapest and already approved in
   shape.
2. **A pull-through registry mirror per host** (proposed ZF-230). The agent
   already owns image pulls and prewarm; pointing a pool's DinD sidecar and
   the host daemon at a local mirror saves the same Docker Hub pull on every
   runner and avoids its rate limit. Read-only from a job's point of view, so
   it leaks nothing a job wrote.
3. **An `actions/cache`-compatible service** (proposed ZF-231), run by the
   agent on each host and reached by runners on the host's bridge, backed by
   the host's shared folder by default and optionally S3-compatible storage.
   The runner is told where it is through `ACTIONS_CACHE_URL` (or the v2
   service's equivalent), so workflows change nothing.

**Design sketch.** Agent: a small HTTP server implementing the cache
protocol's reserve, upload, commit and lookup calls, keyed by the pool's
existing cache identity (scope, pool ID, repository tuple), so the isolation
it offers is exactly the pool cache's and no weaker. Runner image: none; the
entrypoint already sets environment. Controller: pool field and a view of
bytes and hit rate. UI: a line on the pool's cache step and the host card.
Configuration: pool `cache.service: false` (per pool, stored with it, as
`cache.tools` is); host-level `agent.registry_mirror.enabled` /
`ZOOMIES_AGENT_REGISTRY_MIRROR_ENABLED` and
`agent.registry_mirror.max_bytes` / `ZOOMIES_AGENT_REGISTRY_MIRROR_MAX_BYTES`;
`cache.s3.endpoint`, `bucket`, `prefix` with `ZOOMIES_CACHE_S3_*` overrides,
credentials sealed like every other secret. Findings: `cache.service_shared`,
a warning when the service is on for a pool-scoped cache whose installation
spans an organisation (the same reasoning as the existing repository-cache
warning); `cache.s3_plain_http`, an error unless the endpoint is loopback.

**The no-leak promise.** A job's workspace, credentials and registration are
never cached, and that does not change. What a cache adds is a channel from
one job to a later one: a restored entry is whatever an earlier job
uploaded. The service must therefore (a) key strictly by the same identity as
the pool cache, so a repository-scoped pool never serves another repository's
entry; (b) keep GitHub's own branch rule — a pull request reads its base
branch's entries and writes only its own — using the job's `ref` the
controller already stores; (c) never be enabled by default, and say so as a
warning when turned on for a shared scope. Failure rule: every cache call has
a short timeout, and a failed lookup answers "miss" and a failed upload is
dropped and logged, so a broken cache slows a build and never fails it; the
agent raises a host problem (proposed `cache.service_degraded`) after
repeated failures rather than one line in a log.

**Size and dependencies.** Recipes S; mirror S–M (the registry protocol for a
read-through proxy is small enough for `net/http`, in the same spirit as
`dockerapi.go`); cache service M. S3 needs SigV4 signing: `internal/backup`
already ships to an S3-compatible remote, so reuse whatever it uses rather
than add a second client. No browser weight.

**Risks and open questions.** GitHub's cache protocol has changed version
before and is not a published contract; a break would silently become
"always miss". Cache poisoning from a pull request is the real threat, and
(b) above is the defence. A per-host cache does not follow a job to another
host; S3 fixes that at the cost of a network hop and a storage account the
operator owns.

**Testing.** Unit: a table of identity × ref reads and writes, and a fake
backend that times out, proving the job-facing answer is a miss. Runtime: the
repository's own CI already runs on a Zoomies fleet, so a cold-then-warm
build of this repository is the first real qualification; the support matrix
row stays "built" for S3 until a test talks to a real S3-compatible endpoint.

### 2. Transient-failure retry

**Status: partly built.** `scheduler.auto_rerun`, off by default and bounded
by `scheduler.auto_rerun_limit` (1–5), re-runs a job only when the fleet broke
it (`FleetFailed`), raising the `scheduler.auto_rerun_on` warning and counting
`zoomies_job_reruns_total{trigger="fleet_fault"}` (ZF-224,
[decision 0004](decisions/0004-the-fleet-may-re-run-a-job-it-broke.md)).
Separately, the runner image's own package installs retry a mirror mid-sync
(`deploy/runner-apt.sh`). New: retrying a *workflow's* failure because its log
matches a known transient pattern.

**Fit: modified.** Decision 0004's line is that a test that failed is never
re-run; a pattern-matched retry crosses it on purpose, so it needs its own
decision record and must be narrower than a hosted service's. Two levels were
weighed:

* **Step level** — a shell wrapper in the runner image retrying a command.
  Rejected as a default: it changes what a workflow's `run:` means, it cannot
  see into actions written in JavaScript, and a retried step with side effects
  (a push, a deploy) is exactly the masked failure to avoid. It is fine as an
  opt-in helper script a workflow calls by name (`zoomies-retry -- docker pull
  …`), shipped in the image and documented, because then the workflow's author
  chose it.
* **Job level** — after a failure, the controller reads the failed step's log
  tail through the existing log relay and, if it matches a configured pattern,
  asks GitHub for a re-run through the same call ZF-224 uses. Preferred: it
  reuses one tested path, the bound, the timeline entry and the metric.

**Design sketch.** Controller: a classifier over the last N lines of the
failed step, patterns from configuration, fixed and ordered; the match and
the pattern's name go into the job timeline as a `rerun_requested` entry with
*via transient retry* and into the audit log. Scheduler: no change — this is a
post-completion decision, not a scaling one, but its reason string follows the
same form. Configuration: `scheduler.transient_retry` /
`ZOOMIES_SCHEDULER_TRANSIENT_RETRY` (default off),
`scheduler.transient_retry_patterns` (a list; a documented default set for
registry timeouts, DNS resolution failure, `429`/rate-limit and TLS handshake
timeouts), sharing `auto_rerun_limit` so the two together can never exceed
one bound. Metric: `trigger="transient"` on the existing counter. Finding:
`scheduler.transient_retry_on`, a warning naming the cost (minutes, repeated
side effects, and that a real outage matching a pattern is retried before
anyone sees it). UI: the Jobs page shows the reason and the matched line.

**Size.** M. Standard library `regexp` suffices; no dependency.

**Risks and open questions.** Masking: a test that prints "connection
refused" because the code under test is broken matches a naïve pattern.
Mitigations are a short, anchored default list, matching only in the failed
step, and a per-pattern counter so an operator can see which one fires most.
Log access depends on the runner's logs still being reachable after the job;
if they are gone, the answer is "no retry". Open: whether the owner accepts
widening decision 0004 at all.

**Testing.** A table of log tails (transient, real failure that looks
transient, no log) against the classifier; the ZF-224 table extended with the
new trigger; the bound across a restart, as ZF-224 proved it.

### 3. Warm pools

**Status: built.** `min_runners` is "kept warm even with nothing queued"
([configuration.md](../docs/configuration.md#pool-settings)); the scheduler
clamps desired to it and never drains below it (`internal/scheduler/scheduler.go`,
`plan.Desired = clamp(max(p.MinRunners, …))`, and the surplus computed against
`max(plan.Desired, p.MinRunners)`), and says "pool minimum is N runners" as a
reason. The pool's `idle_timeout` drains surplus idle runners with a stated
reason; image prewarm keeps the image pulled; ZF-215 (section 9 of the
roadmap, demand-gated) covers scheduled readiness. Warm runners are counted
against the host reserve and placed like any other runner
([hosts-and-pools.md](../docs/hosts-and-pools.md)).

**What differs.** Two gaps. First, no age limit: an idle warm runner is kept
until it takes a job, however long. A registered runner holds a JIT
registration that GitHub may clean up after long disconnection, and a runner
built from an image that has since been re-pulled is stale. Second, elastic
CPU: an idle runner's CPU share is described in
[elastic-cpu.md](../docs/elastic-cpu.md#idle-runners); a warm minimum is
capacity reserved, and the Usage page already shows it as allocation without
execution.

**Fit: modified, small.** Add a recycle rather than a new concept: pool field
`max_idle_age` (default off), after which an idle runner is drained and
replaced, with the reason "idle for 6h, over the 6h recycle age"; the
replacement is started before the drain so the pool never dips below
`min_runners`. A time-of-day minimum belongs to ZF-215, not here.

**Design sketch.** Store: one pool column. Scheduler: a pure rule on
`IdleFor` and the injected clock, with its reason string. UI: one field on
the pool's size step. API/CLI: the pool shape. No new finding — recycling
weakens nothing — but the existing capacity problems cover a minimum that no
host can hold.

**Size.** S. No dependency.

**Risks.** Churn: a short age on a large minimum repeatedly pays the start
cost; the validator should refuse an age below the pool's `idle_timeout`.

**Testing.** Scheduler table tests with an injected clock are the whole
qualification; this is logic, not runtime.

### 4. Stronger isolation: VM per job

**Status: planned in outline, not authorised.** Section 9 keeps ZF-216's
surviving half, "a disposable-VM backend as its own design with a clear
isolation model", and decision 28's one-day spike, "a container per job on
Proxmox", both gated on a fleet with the hardware. The provider contract
(`internal/provider`, `RunContractTests`, ZF-214a) and the Proxmox provider
(ZF-214b, `implemented`) exist, and CLAUDE.md is explicit that "renting a
machine is not placing a runner": a provider supplies hosts; a backend places
runners.

**Fit: yes, demand-gated.** The honest isolation today is a container on a
shared kernel, and [security.md](../docs/security.md) says so. For public
repositories accepting pull requests from strangers, a VM per job is the
stronger answer. The design question is which side of the boundary it sits
on:

* **A backend** (`internal/backend`, proposed `microvm`): the agent starts a
  Firecracker or Kata micro-VM per runner on its own host. Keeps the provider
  contract untouched and reuses placement, reserves and elastic CPU. Needs
  KVM on the host and a root filesystem built from the published runner
  image.
* **A provider used per job** (Proxmox clone per job): each machine hosts one
  runner and is destroyed with it. Fits the contract's lifecycle but a clone
  takes minutes, and the machine loop is deliberately outside `Reconcile`;
  queue wait would be the clone time.

The backend route is the better fit for per-job lifetime; the provider route
is the better fit for "a clean machine an hour" and already mostly exists.

**Design sketch (backend).** Agent: a `microvm` backend behind the existing
interface, with the same probe-and-report `Preflight` style. Runner image: a
build target producing a kernel and root filesystem from
`deploy/Dockerfile.runner`. Controller/UI: backend option on the pool; the
host card reports KVM availability. Configuration: pool `backend: microvm`;
`agent.microvm.kernel` / `ZOOMIES_AGENT_MICROVM_KERNEL`. Findings: an error
when a pool asks for it on a host without `/dev/kvm`; an info finding
explaining that DinD inside the VM is the VM's own daemon.

**Size.** L. Firecracker is driven over a Unix-socket HTTP API, which fits the
hand-rolled pattern of `dockerapi.go` without an SDK; the VMM binary itself is
a host prerequisite, not a Go dependency, and must be recorded in
[dependencies.md](../docs/dependencies.md) as such.

**Risks and open questions.** Nested virtualisation on cloud VMs is often
unavailable; arm64 support; networking (a tap device per VM needs privilege
the rootless default does not have — this conflicts with the safe default and
must be named). The decision 28 spike should come first because it answers
the cheaper question.

**Testing.** Shape tests against a fake VMM socket are "built". It is "tested"
only when a job runs in a VM on a real KVM host in the harness; until then the
support matrix row says so.

## Medium value

### 5. CI analytics

**Status: largely built.** The Usage page groups runner-hours, jobs, success
rate, queue wait and estimated cost by pool, repository, workflow or
installation over any range, with an activity matrix and CSV
([ui.md](../docs/ui.md#usage), [usage-analytics.md](../docs/development/usage-analytics.md));
ZF-209 (`done`) keeps it past row retention with `runner_sessions` and
`usage_daily`; ZF-223 (`done`) adds the per-installation report with exact
percentiles. Prometheus carries `zoomies_job_duration_seconds`,
`zoomies_job_queue_wait_seconds`, `zoomies_jobs_total{conclusion}` and
`zoomies_job_failures_total{domain,fault}` ([metrics.md](../docs/metrics.md)).
Cost is a pool's `cost_per_runner_hour`; "Zoomies embeds no cloud prices".

**What differs.** Two things are missing: a duration trend per workflow and
job name (p50/p95 over time, to spot a build that got slower) and
mean time to recovery (from a workflow's first failure on a branch to its next
success). A saved-versus-GitHub-hosted figure is deliberately absent, and
[costs.md](../docs/costs.md) explains why the comparison is more than a rate.

**Fit: modified.** Add the trend and MTTR from stored rows. Offer the
comparison only as an operator-entered rate: a per-pool
`comparison_rate_per_minute` beside the existing cost rate, the assumption
printed next to every figure ("at a rate you entered of …; excludes your
hardware, power and time"), never a built-in number, consistent with the
no-embedded-prices rule.

**Design sketch.** Store: queries over `jobs` and the roll-up; MTTR needs the
job's branch, which webhook payloads carry. API: fields on `/usage` and
`/usage.csv`, documented in `api/openapi.yaml`. UI: a lazily loaded section of
the Usage route, inline SVG as today. No configuration key beyond the pool
field; no finding.

**Size.** S for trends, M with MTTR. No dependency.

**Risks.** MTTR over re-runs and matrix jobs is ambiguous; define it once in
`docs/metrics.md` before building it. The roll-up must gain whatever MTTR
needs or it will stop at row retention, which is what ZF-209 fixed.

**Testing.** ZF-223's standard: a fixture fleet whose figures match a hand
computation, and one window older than row retention.

### 6. Right-sizing suggestions

**Status: data built, suggestion new.** Each runner carries a resource sample
(CPU percent, memory bytes, `sampled_at`, the CPU allocation factor) from
heartbeats (`internal/controller/agents.go`, `views.go`, ZF-221); hosts
report usage and the Hosts page shows a recommended capacity
([hosts-and-pools.md](../docs/hosts-and-pools.md)); elastic CPU adjusts shares
at run time. Nothing recommends a pool's `resources` from what its jobs used.
(The `migrate.Suggest` in `internal/controller/migrations.go` maps hosted
labels to pools; it is not sizing.)

**Fit: yes.** Deterministic and explainable is the house style: a suggestion
is a rule over percentiles with the numbers shown.

**Design sketch.** Store: keep per-job peak CPU and memory at completion
(today only the latest sample is kept on the runner row; it would be lost with
the row). Controller: a pure function in the scheduler's spirit — for a pool
with at least N completed jobs in the window, suggest memory = p95 peak × 1.25
rounded up, CPU = p95 of sampled use; say "lower" only when p95 is under half
the limit, "raise" when out-of-memory faults (`fault="out_of_memory"`) are
present. Each suggestion is a sentence: "memory: 4 GiB → 2 GiB; 95 of 100
jobs peaked under 1.5 GiB over 14 days". UI: a note on the pool page with an
Apply button that goes through the ordinary pool edit (and its audit). API:
`GET /pools/{id}/sizing`. No new configuration; no finding — a suggestion
changes nothing until someone applies it.

**Size.** M (a migration, the rule, the page). No dependency.

**Risks.** Samples are periodic, so a short spike can be missed and a lowered
limit can then kill a job; the margin and the OOM rule are the guard, and the
suggestion must name its sample count. Elastic CPU makes "used" depend on
neighbours; report CPU as a suggestion only when elastic CPU is off or
report both.

**Testing.** A table of synthetic peaks to suggestions, including the too-few-
samples case; this is pure logic and needs no runtime qualification.

### 7. Notifications

**Status: new.** Zoomies publishes problems (the problems drawer,
`problems.updated` on `/api/v1/events`) and Prometheus metrics whose
documentation names what to alert on, but it sends nothing itself; alerting
is left to the operator's Prometheus and Alertmanager. No Slack, email or
outbound webhook exists.

**Fit: modified.** Many small fleets have no Alertmanager, so one outbound
channel is worth having — but only one generic mechanism, not a catalogue of
integrations. A signed JSON webhook covers generic receivers; Slack's incoming
webhooks accept a JSON `text` body, so a `format: slack` switch covers it
without a Slack dependency. Email adds an SMTP client and credentials and
should wait for a request.

**Design sketch.** Controller: a notifier subscribed to the event bus,
sending when a problem appears or clears and, optionally, on fleet-fault job
failures; debounced so a flapping host sends one message, with a retry queue
in the store (one writer, as ever). Configuration:
`notifications.webhooks[]` with `url`, `format: json | slack`, `min_severity:
warning | error`, `pools: []` (empty is all) — so per-pool filtering is a
filter on a global channel, not a setting on every pool; the URL is a secret
and sealed; `ZOOMIES_NOTIFICATIONS_WEBHOOK_URL` for the single-channel case.
Security: the outbound address guard from ZF-208 must apply, or a webhook URL
becomes a way to make the controller call an internal address. Findings:
`notifications.plain_http` (warning), `notifications.failing` (a problem when
deliveries keep failing). UI: a Settings panel with a "send a test" button.
Content: problem code, severity, the human message, a link — never a log
line or a secret.

**Size.** M. Standard library (`net/http`, `crypto/hmac`); no dependency.

**Risks.** Leaking repository names to a channel a wider audience reads;
state that the message carries what the problems drawer shows. Open: whether
a job's own failure (not the fleet's) belongs here at all — GitHub already
notifies authors, so the proposal is fleet faults only.

**Testing.** A test receiver asserting body, signature, debounce and
resend-after-restart; a guard test refusing a loopback URL where ZF-208 would.

### 8. MCP server for coding agents

**Status: new.** Everything it would expose already has a REST route in
[api-surface.md](../docs/api-surface.md): jobs and their timelines, logs,
problems, hosts, pools, and a status summary, behind tokens with role and
scope ([security.md](../docs/security.md)).

**Fit: modified.** Not inside the controller. The rule "nothing is reachable
from the UI that is not reachable from the REST API" generalises: an MCP
server should be a thin client of the REST API, so it adds no new authority
and no new code path to audit. The natural home is the existing binary as a
subcommand, `zoomies mcp`, speaking MCP over stdio to the agent that launched
it and calling the controller with a token, exactly as the CLI does.

**Design sketch.** CLI: `zoomies mcp` exposing read-only tools — list failed
jobs, get a job's timeline and log tail, list problems, fleet status — and,
only with `--allow-actions`, re-run a job or drain a runner, each of which the
token's role must already permit. Configuration: the CLI's existing controller
URL and token; recommend a `viewer` token narrowed by scope, and document it.
No server-side key, no finding — the controller sees an ordinary API client.
Log output passed to an agent must be marked as untrusted data, because a
workflow's log is text anyone who can open a pull request can write.

**Size.** M. The MCP stdio protocol is JSON-RPC and fits the standard
library; an SDK would be a dependency needing a
[dependencies.md](../docs/dependencies.md) row, and fifty lines of our own
probably do.

**Risks.** Prompt injection through logs (above); protocol churn. Open:
whether this belongs in the binary or as a separate small tool — the
one-binary principle argues for the subcommand.

**Testing.** A test that drives the subcommand over stdio against the API
test server, including a refusal when the token lacks the role.

## Nice to have

### 9. `zoomies run <cmd>`

**Status: new.** The CLI covers fleet administration
([cli.md](../docs/cli.md)); nothing runs an ad-hoc command.

**Fit: modified.** A runner is a GitHub runner: it only takes jobs GitHub
assigns. Running a command "on a fresh runner" either means dispatching a
workflow (a `workflow_dispatch` with the command as input, which needs a
workflow file in the repository and `actions: write`) or starting the runner
image as a plain container on a host without registering it. The second is
simpler and truer: `zoomies run --pool <p> -- <cmd>` asks the controller to
queue a `run_command` task for an agent on a host that can place the pool,
which starts the pool's image with its resources and cache, streams output
back through the existing log relay, and removes it.

**Design sketch.** Agent: one task kind, reusing the backend's create and the
`stream_logs` inversion (the controller never dials the agent). Controller:
the task, a placement through the normal host selection, audit entry. API: a
documented route; CLI: the subcommand. Security: this is arbitrary code on a
host, so it needs the `operator` role at least and its own audit action; a
warning is unnecessary because it is not a setting, but the route must be off
for a token narrowed to read scopes. Nothing from GitHub is injected; the
container has no registration and no GitHub token.

**Size.** M. No dependency.

**Risks.** It competes with runners for slots; it should count against the
pool's `max_runners`. Open: whether the owner wants a code-execution route on
the API at all.

**Testing.** Controller task tests with the fake backend; one run on the
repository's own fleet before it is called tested.

### 10. Image parity with `ubuntu-latest`

**Status: partly built.** The catalogue in `internal/naming/images.go` drives
the image table in [naming.md](../docs/naming.md), the Makefile and both
workflows; `zoomies-runner-full` adds the `setup-*` toolchains, pinned in
`deploy/toolcache.lock` (generated by `deploy/gen_toolcache_lock.go`); every
image writes `/usr/local/share/zoomies/installed-software.json`
(`deploy/runner-inventory.sh`), listing distribution packages and tool-cache
entries ([persistent-caches.md](../docs/persistent-caches.md)). What is
missing: that inventory is inside the image only, not published, and nothing
compares it with GitHub's published `ubuntu-24.04` software list.

**Fit: yes.** Exact parity is not a goal — GitHub's image is tens of
gigabytes — but an honest, published list and a stated difference are.

**Design sketch.** Release workflow: extract the inventory from each built
image and attach it to the release and to a generated docs page, with a test
in `internal/naming` in the catalogue's style that the page matches the
generator. A second generated table lists common `ubuntu-latest` tools and
whether each image has them, from a checked-in list rather than a network
fetch at build time. No controller, UI or configuration change.

**Size.** S for publishing, M with the comparison. No dependency.

**Risks.** The inventory must be taken from the image as pushed, not as
built locally, or the page drifts from what runs.

**Testing.** The generated-file test is the qualification, as it is for the
catalogue.

### 11. Team roles and invite links

**Status: roles built, invites new.** Four roles exist — `viewer`, `operator`,
`admin` and `platform`, the last added by ZF-207 (`done`) for the instance
where one team runs the controller and another uses it (`internal/store/models.go`,
`internal/auth/rbac.go`'s action-to-role table, [security.md](../docs/security.md#roles)).
OIDC can create accounts on first login when `allow_signup` is on
(`internal/auth/oidc.go`), tokens are role-capped and scoped, and a user can
never grant above their own role. There is no invite link: an administrator
creates a user with a password, or OIDC admits one.

**Fit: modified.** Owner/Admin/Member would be a renaming of a model that
already carries more meaning; do not change it. Invite links are the
missing piece for instances without OIDC.

**Design sketch.** Store: an `invites` table (prefix `inv_` in
`internal/store/ids.go`), a hashed single-use token, the role to grant, expiry
and creator. Auth: redeeming creates the account and consumes the token,
with the same "never above the creator's role" rule. API: create, list,
revoke; UI: a Users panel button that shows the link once. Configuration:
`auth.invite_ttl` / `ZOOMIES_AUTH_INVITE_TTL` (default 72h). Finding:
`auth.invite_ttl_long`, a warning above seven days. Audit: create, redeem,
revoke.

**Size.** S. No dependency; `internal/cryptox` already hashes tokens.

**Risks.** A link forwarded is an account granted; single use and short
expiry are the defence. Reuse the join-token pattern and its
`ErrJoinTokenUsed` / `ErrJoinTokenExpired` semantics.

**Testing.** Unit tests for expiry, reuse and role cap; a Playwright pass
redeeming a link.

## Recommended order

Section 10's chain comes first and is not displaced: ZF-207 through ZF-223
are the current stream, and critical defects interrupt it. Most of these
candidates are demand-gated in the same way section 9 is. Where the owner
chooses to take any of them up, this order follows value per unit of risk
and reuse of what exists:

1. **ZF-212 recipes (#1, layer one)** — already approved in shape, S, no
   code, and the fastest real build-time gain.
2. **Warm-runner recycle age (#3)** — S, pure scheduler logic, closes the
   one real gap in an existing feature.
3. **Published image inventory (#10)** — S, answers "what is in the image"
   honestly, and fits the generated-file discipline.
4. **Invite links (#11)** — S, finishes the ZF-207 identity work for
   instances without OIDC.
5. **Notifications (#7)** — M, after ZF-208 so the outbound guard exists to
   reuse.
6. **Analytics trends and MTTR (#5)** and **right-sizing (#6)** — M each,
   on ZF-209/ZF-223's ledger; right-sizing needs the per-job peak column
   first.
7. **Registry mirror, then the cache service (#1, layers two and three)** —
   M, after the recipes have measured what local caching already buys; the
   S3 half only after the owner decides the trust question ZF-212 raised.
8. **`zoomies mcp` (#8)** and **`zoomies run` (#9)** — M, useful but not
   asked for; `run` needs an explicit decision about a code-execution route.
9. **Transient retry (#2)** — M, only with a new decision record widening
   0004; the opt-in helper script can ship earlier on its own.
10. **VM per job (#4)** — L, after decision 28's spike and with a fleet that
    has KVM hosts; the support matrix row stays "built" until a job runs in a
    VM on real hardware.
