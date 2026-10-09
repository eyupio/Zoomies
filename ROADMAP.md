# Zoomies follow-on roadmap

Version 3.6 · 8 October 2026 · derived from the owner's
[follow-on roadmap v1.0](roadmap/source/2026-09-06-follow-on-roadmap-v1.0.md),
reconciled against `main` at `6d12a72` on 6 September, at `9a80b31`
on 19 September and at `88c41f6` on 8 October, when the owner set a new primary target, withdrew the
real-use qualification programme that version 2 was built around, and the
fifty-six pull requests merged since version 2.40 were read back into it.

This is the sole active roadmap and delivery-order source of truth for the
next programme: make Zoomies the controller a team can be given, with the
team's own machines as its runner hosts (dependable, bounded at every edge,
operable without a shell on either side) while the self-hosted product it
already is keeps getting better. The ordered delivery plan in section 10
supersedes all earlier ordering.

[IMPLEMENTATION_PLAN.md](IMPLEMENTATION_PLAN.md) is a finished code-review
record, and [roadmap/source/](roadmap/source/) contains historical inputs.
They explain why work existed, but neither may add, reorder or authorise
current work. [roadmap/progress.md](roadmap/progress.md) is the corresponding
status/evidence record: it records delivery of packages defined here but does
not define a competing plan. New or changed delivery work belongs in this
file first.

## 1. Where it stands

Zoomies is stable in real use. Three full releases shipped in a week
(`v1.0.0` on 13 September, `V1.1.0` on 15 September and `v1.2.0` on 18
September, which is what `:latest` resolves to now) and since 18 September
this repository's own CI, CodeQL, fuzzing and release set-up jobs run on a
Zoomies fleet (`zoomies-linux-x64`) through the build under test, with only
the arm64 and Windows legs left on GitHub's runners, and, since 19 September,
the Scorecard job, whose publishing service accepts results from GitHub's own
Ubuntu runners alone and had refused every run from the fleet; a test in
`internal/docs` keeps CI on the fleet and another keeps Scorecard off it. The
owner has been running it across
repositories since at least 9 September
([rc1-triage.md](roadmap/rc1-triage.md)). The seven-day observation window,
the two hundred attempts and the second operator that version 2 built its
release gate around never happened and are no longer wanted: the product is
proving itself by carrying work, every day, on the thing it is for.

Since version 2.40 (15 September), fifty-six pull requests (#293 to #350)
landed. Roughly in the order they matter to this document:

* **Backups grew into a subsystem.** Scheduled copies (`backup.interval`,
  `backup.keep`), S3-compatible offsite destinations with a hand-rolled
  SigV4 client and sealed credentials (migration `0036`), a staged restore
  applied by restarting the controller, a Backups settings page, eighteen
  `/backups*` routes under three new actions, and a configuration export and
  import with a dry run (#297, #304, #306, #308, #312). ZF-203's text had cut
  every one of those; the package is closed as delivered, and larger.
* **Every runner has a size, and a pool can lend it more.** A pool that sets
  no limits now means one slot's share of whichever host it lands on,
  enforced as a real cgroup limit; a fixed size is the alternative; per-pool
  overrides of the fleet's timings (migration `0037`); a docker-in-docker
  pair is one slot again; and elastic CPU bursting lends unpromised host CPU
  to a busy runner and takes it back under pressure (migration `0039`, #350).
  The wizard asks how much of a pool the operator wants to decide.
* **Runner start-up under load is bounded.** Everything ZF-221 listed as
  prepared is merged, and more: a FIFO start queue with a cooldown,
  Docker readiness before a runner registers, per-installation registration
  admission (`scheduler.registration_concurrency`), queued starts that expire
  at the provision deadline, owned-container conflict recovery, a runner's
  resource sample with its age on the API, and deferred registration cleanup
  recovered by housekeeping (#343, #344, #345, #347, #348).
* **Failures say whose they are.** `fault_kind` on jobs and runners
  (migrations `0034`, `0035`) tells a fleet fault from a workflow's own
  failure, the Overview counts them apart, and a job can be rerun from the
  drawer (#295). A cancelled workflow run now cancels its runners promptly,
  including the ones still provisioning (#348).
* **Private hosts recover by themselves.** A Tailcat host re-announces itself
  on every dial, so it survives a controller restart without anyone
  restarting it, and a host that lost its private address is told to mint a
  new command rather than failing to dial (#327, #329).
* **The installer meets the machine as it is.** It offers to install Docker
  or Podman when it finds neither, delegates cgroup controllers to a rootless
  daemon at set-up, pins `agent.work_dir` so a joined host survives dropping
  root, and takes a host's capacity from the machine it measured (#311, #322,
  #323, #324, #326).
* **The UI has a settings rail, grouped navigation, per-account table
  layouts (migration `0040`), a fleet-chosen queue warning threshold and a
  redrawn usage report**, and the event bus numbers events where they are
  published and leaves a reconnecting subscriber room after its replay.
* **Security**: the OIDC state cache is capped, a password hash nothing could
  have produced is refused rather than allowed to panic, and runner
  containers no longer set `no-new-privileges`, so the image's passwordless
  `sudo` works while the dropped capabilities remain the boundary.
* **Releases**: assets upload one at a time, and the `dev` channel is
  published in a way a slow upload cannot defeat.

One thing the reconciliation found still needs the owner's hand (section
11): `V1.1.0` (capital V) is published as a full release with no assets,
because the release workflow's tag guard matched `v*` case-sensitively and so
built nothing for it. The guard now refuses such a tag out loud (ZF-005, 19
September); the release and its tag are the owner's to delete or leave. The
other, `deploy/marketplace/release.env` pinning `v1.0.0`, was repinned to
`v1.2.0` with ZF-005, so a marketplace deployment gets the pressure holds,
the throttle ladder, the start-up fixes and elastic CPU.

## 2. The primary target

The programme's primary target is **an instance operated on a team's behalf,
where the team connects its own runner hosts.** Two parties share one
controller. The **platform** runs the process: the machine it binds on, its
TLS, its database and encryption key, its timers, upgrades, backups and
recovery. The **fleet** is the team the instance serves: their GitHub App
installation, their pools, the hosts they join through the outbound agent
(directly over HTTPS, or through a private connection when a machine cannot
reach the public address) and their own users and API tokens. The controller
runs with `agent.embedded: false`; every runner is on a machine the fleet
owns. One instance serves one fleet.

Everything the design already insists on is what makes this shape work.
Agents connect outbound only, so a host behind the fleet's NAT needs no
inbound rule. A job is attributed to one installation and a runner is minted
in that installation alone. An agent is confined to the agent API and to its
own host's rows; a report about another host's runner is refused. Version
skew excludes a host rather than refusing it, and the host is handed the
command that fixes it. Backups leave the machine on a schedule. What the
shape lacks is the line between the two parties. Today the fleet's `admin`
is the top rung: it reads the platform's bind address, TLS file paths and
database path on `GET /settings`; changes `server.bind`, `server.tls.*`,
`server.trusted_proxies`, `security.disable_auth`, `oidc.*` and every timer
through `PATCH /settings`, because migration `0031` made almost every key an
instance setting the database holds; takes a support bundle with the
process's paths, OS and heap in it; lifts the recovery fence; and sees
`bind.public_no_tls`, `crypto.*` and `controller.lease_lost` in a problems
drawer whose fix sends it to a log it cannot read. Nothing bounds what one
fleet's agents can do to the process. The usage figures the two parties
would reconcile are recomputed from rows the prune loop deletes, on a
retention the fleet can shorten. And the first administrator is still created
by reading a setup token out of the controller's log, or from an answer file
that carries a password.

The packages that close those gaps already existed under decision 27,
written for a platform team operating an instance for a product team, which
is this shape with this trust boundary: **ZF-207** (two audiences), **ZF-208**
(limits at the edges), **ZF-209** (a usage ledger), **ZF-210** (unattended
bootstrap; export and purge). They move to the front, and each is re-read in
section 8 against what the code does now, which in ZF-207's case is further
from the package than when it was written. Around them: the remaining half
of **ZF-221**, because a host the platform cannot log into has to explain
its own runtime failures; the private-host packages **ZF-401 to ZF-404**,
because the fleet's hosts are the untrusted edge and the join is the
onboarding; **ZF-222**, because the fleet's developers have no account;
**ZF-213**, because the fleet's pools are the one thing that should move
between instances as a file.

Three rules bind the target, and they are delivery rules 3, 15 and 16. One
instance per fleet and one trust domain per instance: nothing here
separates two fleets inside one process or one SQLite file, and nothing
will, because the pure scheduler, the single writer and "every `*.updated`
is the `GET` shape" all assume one fleet. The platform's hand on a fleet's
host is the agent and nothing else. And none of this is documented: the
public story stays the self-hosted, bring-your-own-infrastructure one, every
package ships as a self-hosted feature with a self-hosted reason (a
platform team operating an instance for a product team is real, and is the
reason given) and this file is the only place the target is named.

## 3. How to use this document

Each work package keeps the ID the source roadmap gave it, so historic
material can be read side by side without becoming an alternative
instruction. Each active package names its **classification** (*existing,
needs validation*; *extension*; *new*), what to **implement**, what
**acceptance** means, its **dependencies**, its **size** (S under a day of one
session, M a few days, L a week or more, XL a phase in itself), and the
**session** that should do it per
[roadmap/agent-models.md](roadmap/agent-models.md).

Section 6 lists what is delivered, with a line each; the
[work-package record](roadmap/progress.md) carries the evidence. Section 7
lists what version 3.0 withdrew and what, if anything, each withdrawal
costs. Section 8 is the active work, in full. Section 9 is what is kept for
the day somebody asks. Section 10 orders it.

The decisions in section 4 are the ones only the owner can take. Each has a
recommendation, and the packages below are written as if the recommendation
were accepted; a different answer changes the package it names and nothing
else.

## 4. Decisions for the owner

Numbered as they always were, because the numbers are the index that the
[decision records](roadmap/decisions/) and the progress record cite. A
decision that has been taken or overtaken says so in a line and keeps its
number.

### Programme

1. **Where this record lives.** `ROADMAP.md` and `roadmap/` at the root,
   outside the published site, as
   [decision 0001](roadmap/decisions/0001-planning-documents-live-beside-the-code.md)
   says. *Accepted; unchanged.*
2. **Which model drives which stage.** Tiered by what the stage risks, per
   [decision 0002](roadmap/decisions/0002-choose-the-model-by-what-the-stage-risks.md)
   and [roadmap/agent-models.md](roadmap/agent-models.md). *Unchanged.*
3. **The reference configuration.** *Withdrawn on 19 September 2026.* The
   controller's reference configuration is the operated instance itself, and
   the agent-side table in
   [roadmap/support-and-measurement.md](roadmap/support-and-measurement.md),
   which says per host OS and backend what has actually run, stays as
   documentation. Nobody records a reference host's versions any more.
4. **Two assignments, not one.** *Superseded* by section 10 since version
   2.34; both assignments' packages are delivered. Kept for the history.
5. **Keep this plan scoped to self-hosted development.** *Replaced by
   decision 30.*
6. **Gate F as measured.** The definitions are built into the product
   (`Job.EligibleAt`, `create_task_issued_at`, `fault_kind` on jobs and
   runners, a `cleaned_up_at` that needs both the host's and GitHub's
   confirmation) and stay in
   [roadmap/support-and-measurement.md](roadmap/support-and-measurement.md)
   and `docs/metrics.md` as the definitions of scheduling latency, of a
   Zoomies-caused failure and of a denominator. *The gate itself is
   withdrawn* (section 7); the definitions are what ZF-223 would report per
   installation.
7. **The schema rule.** Never edit or rename a shipped migration; the next
   file takes the next unused prefix alone (`0041` as this is written); prefer
   adding a column; a data-preserving rebuild in a new file is acceptable
   when SQLite forces it, with the reason in the file header. *Accepted;
   unchanged.* ZF-207 needs such a rebuild, because `users.role` and
   `api_tokens.role` carry a `CHECK` naming the three roles.
8. **What `:latest` means.** *Taken by events.* The release workflow moves
   `:latest` only on a full release (`v1.2.0` moved it on 18 September) and
   the join command pins the controller's own version, so an untagged pool
   follows the newest full release and a joining host is told which build
   to install. Nothing is left to decide.
9. **A tagged pre-release; prereleases marked; immutable releases.**
   *Discharged.* The upgrade job runs from the latest published release on
   every pull request; `v0.1-alpha`, `v0.2-beta` and the three release
   candidates are marked prereleases on GitHub. Whether immutable releases
   are enabled is one repository setting to check (section 11).
10. **The Enterprise Server claim.** *Taken.* README and FAQ say designed
    for it and not yet verified against one. Verification waits for a fleet
    that has one, and is not roadmap work.
11. **Real-runtime evidence in CI.** *Half taken.* The fake-GitHub drill tier
    runs every drill on every pull request, on amd64 and on arm64, and the
    whole CI runs on a Zoomies fleet. The credentialed nightly `e2e.yml` is
    withdrawn (section 7).
12. **Owner actions now.** *Discharged in substance.* The owner deployed
    `main`, used it, released from it and moved CI onto it; the written
    version record and the second operator's session are withdrawn.

### Per package

Decisions 13 to 22 were taken as recommended and shipped; the migrations
and the progress log are their record, and
[roadmap/decisions/README.md](roadmap/decisions/README.md) says why they
have no file of their own yet. They are kept here verbatim because this
list is the only account of what was decided.

13. **ZF-101, the authoritative installation for a job.** The installation
    found by the job's repository (repository target before organisation
    target), not the one whose secret verified the delivery, which the code
    deliberately lets be another installation's; jobs no installation covers
    are recorded and marked ineligible with a reason, never rejected; the
    migration backfills every unfinished row, waiting as well as queued and
    in-progress; the rate-limit hold is per installation only;
    `installation_id` is exposed read-only on the job. *Taken.*
14. **ZF-102, how far to go.** No durable task queue (stamp the issue time
    on the row); adopt live workloads on agent start; a state-directory lock
    plus a controller lease with `--takeover`; a duplicated-agent fence that
    detects and warns rather than refuses; no resurrection of a terminal row
    when a lost host returns; the existing rate-bounded retry accepted as
    "bounded". *Taken.*
15. **ZF-103, the reservation model.** Split in two: 103a is the reporting
    half and 103b the admission half. A pool that sets no limits reserves
    its host's allocatable share per slot, so a host carrying only such
    pools admits exactly what it admitted before the upgrade; a DinD pool is
    charged twice its limits; the host reserve is per host with a small
    documented floor; an agent that predates the fields is placed by slots
    with a visible badge; the `process` backend's non-enforcement is
    documented and warned about, not fixed here; disk is a gate, not an
    evictor. *Taken*, then refined by the sizing work of 16 and 17
    September: a defaulted pool's share is now enforced as a cgroup limit
    and a docker-in-docker pair on such a pool splits one slot rather than
    doubling it.
16. **ZF-104.** Streams re-check their credential on each heartbeat and end;
    a join token may not replace a host that is still heartbeating unless it
    is cordoned; the log viewer opens only `http(s)` links; forwarded-proto
    is believed only from a trusted proxy; per-identity stream caps wait for
    ZF-105 and evidence; the join route gets the login limiter. *Taken*, with
    one deviation recorded in the progress log: the join route got a counter
    of its own at the same setting, so an attacker hammering `/agent/join`
    cannot lock administrators out of the page they would use to stop it.
17. **ZF-105.** Failed cleanup lives on the runner row, not a new
    operations table; no maximum job duration (a drain timeout instead);
    the cache prune is guarded at runtime; Zoomies never deletes images and
    says so; disk-low handling lands with ZF-103; the ownership of
    `zoomies-*` registrations across two instances sharing one organisation
    is documented now and designed in ZF-101. *Taken.*
18. **ZF-201.** Verify reports the installation's repository selection and
    first names rather than taking a repository as input; a test-only fake
    GitHub program under `test/` rather than a hidden subcommand in the
    shipped binary; the real-job proof is the end-to-end test asserting the
    page shapes, not a browser-driven real job; Add-a-host shows a resume
    notice, never the token. *Taken.*
19. **ZF-202.** One JSON bundle from one admin route under a new
    `diagnostics.read` action; never workflow log bodies; the per-job
    explanation is its own endpoint rather than a field on every event
    frame; the stale-poller signals are fleet-wide; seeded failures come
    from an opt-in fixture, not the demo seed. *Taken.* ZF-207 moves the
    full bundle behind the platform role and gives the fleet one without
    the process in it.
20. **ZF-203.** Backup is a local command that opens the file, not an API
    route; the key is excluded unless asked for, with its fingerprint always
    in the manifest; restore invalidates sessions and unused join tokens by
    default, with flags for API and agent tokens; the store refuses a
    database newer than the binary; the fence lifts through one audited
    admin route. *Taken, then overtaken on 16 September*: backups have an
    API, a page, a schedule and offsite destinations now, and the command is
    one client of the shared `internal/backup` package. The key rule, the
    refusals and the fence are unchanged.
21. **ZF-204.** Protocol must match and an agent may lag one minor release;
    an agent found incompatible at heartbeat is flagged and excluded from
    placement like a cordon, never sent into a restart loop; the store takes
    a `VACUUM INTO` copy before pending migrations, using ZF-203's primitive;
    build-provenance attestations rather than signing keys; a GitHub-hosted
    path for the release and site workflows selectable by dispatch input;
    Dependabot weekly and grouped, actions pinned to commits. *Taken*, with
    the correction the progress log records: "one minor release" is not a
    rule the code can enforce, so what is enforced is the protocol, and lag
    beyond that is shown.
22. **ZF-205.** Audit rows stay unpruned, with a size-awareness problem;
    the load fixture is a test-only generator, never a knob on the demo
    seed; the p95 figures are recorded evidence, not a pull-request gate;
    the poller-pause gauge is fleet-wide; no stream caps until the drills
    show growth. *Taken*, except that the pause gauge carries the
    installation, because ZF-101 had already made the hold per installation.

### Noted, not yet due

23. **Licence and contributor terms.** Keep the AGPL-3.0 licence visible
    and document contributor expectations. No licence change is part of this
    programme. *Unchanged.*
24. **Whether the controller may dial a machine.** Refined. The controller
    now does dial one kind of machine, a private *provider*'s API, through
    `zoomies gateway` (ZF-214b), and that is an infrastructure API, not an
    agent; "the controller never dials an agent" holds everywhere it is
    stated. For the primary target it hardens into delivery rule 16: the
    platform never dials a fleet's host, so an SSH bootstrap is not optional
    but out, and ZF-402 is withdrawn with it. *Recommend: accept.*
25. **Host stewardship and bounded housekeeping.** The non-invasive default
    is the promise a fleet relies on when it runs the platform's enrolment
    command on its own machine, and the code keeps it with one exception the
    reconciliation found: `agent.docker_build_cache_mb` (default 5120) has
    every Docker-backed agent ask the daemon to prune *unused* builder cache
    above that size every five minutes, daemon-wide, not only layers Zoomies
    built. It is bounded and it touches nothing in use, and on a shared
    daemon it is still somebody else's cache. *Recommend: keep the default,
    name it on the security and private-hosts pages as the one thing the
    agent does to a shared daemon, and make `0` the shared-daemon advice*
    (ZF-404). ZF-404b, the opt-in dedicated-host maintenance slice, is
    deferred indefinitely: the hosts are the fleet's machines, and a
    platform upgrading or rebooting them is a liability, not a feature.
    Narrowed on 8 October 2026 by
    [decision 0011](roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md):
    updating the agent's own binary, on a host that opted in, is ZF-232; the
    rest stays deferred.
26. **Windows runners, and which kind.** *Taken on 12 September 2026* as
    processes on a Windows host, per
    [decision 0003](roadmap/decisions/0003-windows-runners-are-processes-on-a-host.md).
    Built, vetted and unit-tested; the first job on a Windows host anyone
    kept will be a fleet's, and the support-matrix row moves when it
    happens rather than gating anything.
27. **Separate platform administration from fleet operation.** *Now the
    primary target;* see decision 30. One instance remains one trust domain.
28. **A container per job on Proxmox, from the runner image we already
    publish.** Deferred. Not before a fleet with Proxmox asks for it, and not
    before the one-day spike the recommendation describes.
29. **Whether any fleet fact may be read without an account.** ZF-222's
    name-free, banded status projection, off by default. On the primary
    target the audience with no account is the fleet's own developers, whose
    jobs queue on hosts their team owns, and the projection is built on the
    fleet half of ZF-207's problem split, so it cannot carry a platform
    finding. *Recommend: adopt now, off by default, sequenced after ZF-207.*

### Taken on 19 September 2026, and the two it opens

30. **The primary target.** An instance operated on a team's behalf, where
    the team connects its own runner hosts; section 2 defines it. *Taken by
    the owner.* It is not documented on the site, in the README or in the UI,
    and no page names a plan, a tier or a hosted service; delivery rule 15.
31. **What a fleet may change on an operated instance.** ZF-207 splits the
    settings registry a fourth way. A key is *platform-scoped* when it
    changes what the process binds, trusts, stores, logs or dials from its
    own machine, or how much of that machine it spends: `server.*`,
    `security.*`, `log.*`, `backup.*`, `retention.*`, `updates.*`,
    `metrics.public`, `capacity_demand.*`, the `limits.*` ZF-208 adds, and
    the embedded agent's `agent.embedded`, `agent.backend`, `agent.work_dir`
    and `agent.docker_host`. A key is *fleet-scoped* when it changes how the
    fleet's own runners are placed, timed and sized: `scheduler.*`,
    `runners.*`, `images.*`, `github.*`, `oidc.*`, `ui.*` and the rest of
    `agent.*`. `database.*` and the encryption key stay bootstrap-only, as
    now. On a single-team instance nothing changes, because the account that
    installed it becomes the platform. *Recommend: adopt this split; a key
    the owner would move changes one table in `internal/config/settings.go`
    and nothing else.*
32. **The fleet's secrets under the platform's key.** An installation's
    private key and webhook secret are sealed with the instance's encryption
    key, which the platform holds; every backup carries them sealed, and a
    backup the fleet downloads is unusable without a key only the platform
    has. That is how every self-hosted instance already works and it is the
    right model (the process has to read the key to mint credentials) but
    it means a fleet cannot leave with its App on its own. *Recommend:
    accept the model, and give ZF-210b's per-installation export a
    passphrase re-seal, using the argon2id-and-AES-GCM pattern
    `backup_remotes.passphrase_enc` already uses, so offboarding never needs
    the instance key.*

## 5. Delivery rules

The source roadmap's rules, corrected where the repository already answers
them, with three added for the primary target. `CLAUDE.md` is the agent
guidance file (there is no `AGENTS.md`) and it is tested: its layout block
and `README.md`'s must name every top-level directory. Read it,
`docs/architecture.md` and `docs/upgrading.md` before anything structural.

1. Preserve the single binary, SQLite, the pure scheduler and the
   controller-and-agent shape. No Kubernetes, no broker, no database
   service, no microservices.
2. Host provisioning and runner execution are different things. The
   backend interface is the per-host execution contract; the provider
   contract (`internal/provider`) is the controller-side one for renting a
   machine, and it may import the store's domain types and nothing else.
3. One instance is one administrative trust domain, and one instance serves
   one fleet. Two installations of that fleet still need correct target
   scoping, which is ZF-101. Nothing separates two fleets inside one process
   or one database, and no package may try: the pure scheduler, the single
   writer and the event stream's "every `*.updated` is the `GET` shape" all
   assume one fleet, and a second would have to be a second instance.
4. The UI, the CLI and any bundle are clients of the same REST API; nothing
   is reachable from one that is not reachable from the others. Build UI
   workflows on validated application operations; never duplicate
   orchestration in Svelte or in a command.
5. Network side effects are restart-safe, bounded and observable. Durable
   identity, ownership checks and reconciliation; never a claim of
   exactly-once across GitHub, agents and providers.
6. Reuse the components, the tokens, the generated clients, the error
   shapes, the auth and the audit conventions. Every endpoint change updates
   `api/openapi.yaml`, regenerates both clients and touches
   `docs/api-surface.md`; every new problem code, metric and command needs
   its row on the page that `internal/docs` tests.
7. The schema rule is decision 7: never edit or rename a shipped
   migration; the next file takes the next unused prefix alone; add columns
   by preference; rebuild a table in a new file only when SQLite forces it,
   saying why in the header. Two tests are part of that rule rather than a
   consequence of it. Every new file is appended to `shippedMigrations`
   (`internal/store/migrations_test.go`), which says so when it fails. One
   that touches `jobs` must also be added to
   `TestTheJobsRebuildKeepsEveryRowAndItsIndexes`
   (`internal/store/store_test.go`), which unwinds every later migration
   that touched that table; forgotten, it fails on a `SELECT` under "the
   completed row did not survive the rebuild", which blames the rebuild
   rather than the omission.
8. Small commits, small pull requests, one behaviour each, an imperative
   sentence in plain prose as the message (no `feat:` prefix, which one
   commit on 19 September carried and which the next reader should not copy)
   British spelling in prose, a test that reads as a sentence about the
   behaviour, and `make lint` and `make test` green before a push.
   Behavioural tests for concrete new failure modes, never tests that
   repeat implementation details.
9. New capabilities start disabled until their acceptance criteria are met.
   A disabled feature starts nothing, creates nothing and needs no
   credential on an existing installation.
10. Every dependency carries a one-line reason in `docs/dependencies.md`.
    A library for a VM runtime is reasonable and gets its row and a
    security note; a plugin framework for one integration is not.
11. Logs and bundles are sensitive. Platform-managed credentials are
    redacted by key name (`auth.Redact`); arbitrary secrets in workflow
    output cannot be recognised and `docs/security.md` says so; the bundle
    therefore never carries log bodies.
12. Never auto-upgrade a host, remove a customer-owned machine or weaken a
    trust check as a recovery shortcut. An update an operator has switched on
    is not a recovery shortcut; see
    [decision 0011](roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md).
13. A skipped test is not a pass. A harness that cannot run reports "not
    run" or "blocked", and a gate that counts it as passed is lying.
14. A behavioural test is kept only once it has been run against the code
    with the rule it asserts removed and seen to fail; an assertion that
    cannot be made to fail is deleted rather than shipped, and the pull
    request says which assertions were checked this way.
15. **The primary target is not documented.** Nothing under `docs/`, in
    `README.md`, `SUPPORT.md` or the UI describes an instance operated on a
    team's behalf, names a plan or a tier, or distinguishes a platform
    operator from a fleet owner in words a visitor would read as an offer.
    `SUPPORT.md`'s "no paid tier" sentence stays true as written. Every
    package below ships as a self-hosted feature with a self-hosted reason
    (a platform team running an instance for a product team, which is real)
    and is off or invisible on an instance that has only one team: a
    fleet admin on an instance with no separate platform identity sees
    exactly what it sees today, because the account that installed it is
    the platform.
16. **The platform's hand on a fleet's host is the agent, and nothing
    else.** No SSH, no controller-initiated dial to a host, no OS package,
    firewall or reboot, and no pruning beyond what the join command and the
    security page say the agent owns: its service, its work directory, the
    containers carrying its labels, its per-pool cache directory and the
    bounded builder-cache target of decision 25.
17. **Real-use confirmation is not a gate.** A package is complete when its
    code is merged with tests that were seen to fail, CI is green and its
    acceptance holds in a fixture or a drill. Evidence from a real fleet is
    welcome and recorded when it arrives, and it never blocks a release or
    a package. The two drill tiers stay, because they are regression
    coverage, not confirmation.

### The work-package record

[roadmap/progress.md](roadmap/progress.md) has one row per package with its
classification, status, dependencies, the session that did it and the
evidence. Statuses are `not_started`, `in_progress`, `implemented`, `done`,
`validated`, `blocked`, `superseded` and, from version 3.0, `withdrawn`,
each defined in the record itself. `done` (every pull request the package
names merged and its acceptance holding) is the terminal status; `validated`
is kept for the rows that already carry evidence in
[roadmap/validation/](roadmap/validation/) and is no longer a rung any
package has to reach (rule 17). `withdrawn` is for work version 3.0 stopped
authorising, with the reason and what it costs; `superseded` links to the
thing that meets the same criteria. Keep it current in the pull request
that changes it. Small design decisions go in
[roadmap/decisions/](roadmap/decisions/), gate evidence in
[roadmap/validation/](roadmap/validation/).

## 6. Delivered

One line per package, with what shipped and the pull requests that carried
it; the [work-package record](roadmap/progress.md) holds the evidence and
the log holds the reasoning. Every package here is `done` unless the line
says otherwise, and where version 2 left a real-use clause open it is named
in section 7 rather than here.

### Phase 0, the baseline

* **ZF-001 and ZF-002**: the reconciliation, the support matrix and the
  measurement contract, with the code slice that puts the migration ledger
  on `/readyz` (`schema.applied`, `schema.latest`), the `waiting` and
  `approved` timeline kinds and the four-way completed split. Extended on 16
  September by `fault_kind` on jobs and runners (migrations `0034`, `0035`,
  #295), which made the contract's fleet-fault-versus-workflow-fault line a
  counted column.
* **ZF-003**: every action commit-pinned and tested for it, Dependabot
  weekly and grouped, `govulncheck` on every change and weekly; CodeQL,
  Scorecard, fuzzing and package cleanup workflows followed
  ([scorecard-hardening.md](roadmap/validation/scorecard-hardening.md)).
* **ZF-004**: six narrowings from reading the instance as a service; a
  demo id recognised by shape, installation targets folded, a held
  installation spending no more quota, `history_from` on the usage report,
  `retention.scaling_events`, and an unsigned webhook refused before its
  body is read. The first package written from the primary target's
  question.
* **ZF-005**: the three pages corrected against the record (`docs/index.md`
  and the support matrix say what runs where now that CI runs in containers
  on a Zoomies fleet, and `docs/proxmox.md` says the harness has not been
  run) the marketplace package repinned to `v1.2.0`, and the release
  workflow's tag guard made loud (19 September).

### Phase 1, correctness and security under failure

* **ZF-101**: a job belongs to one installation (migration `0012`),
  `scheduler.Eligible` before labels on every path, per-installation poll
  freshness and rate-limit holds, `pool.runner_group_unresolved` (#99, #100,
  #101).
* **ZF-102**: invariants written and pinned, the state-directory lock and
  controller lease with `--takeover` (`0013`), adoption on agent start,
  `task_issued_at` and `host.duplicate_agent` (`0014`), the seven-boundary
  restart table (#82, #103, #104, #105). Since: GitHub calls no longer run
  under `reconcileMu`, and the log relay no longer queues tasks for a host
  that never comes back (#299).
* **ZF-103**: hosts report CPUs, memory and disk (`0017`), the scheduler
  places by them and says "short of memory", operators set a reserve and
  see what is promised away (#115, #118). Since: every runner has a size
  (`runners.default_cpus`, `runners.default_memory_mb`; #305), per-pool
  timing overrides (`0037`, #311), a stranding edit answers 409 (#302), a
  docker-in-docker pair is one slot (#314), the runner's own resource
  sample reaches the API (`0038`, #344), and elastic CPU bursting (`0039`,
  #350).
* **ZF-104**: every identity class walked positive and negative, the log
  relay bound to its host, streams that end on revocation, hostile input
  through the pages and the log viewer, forwarded-proto believed only from
  a trusted proxy (#90, #116, #118, #119, #120). Since: the OIDC state cache
  is capped (#317) and an impossible password hash fails closed (#309).
* **ZF-105**: failed cleanup on the runner row and in the drawer (`0015`,
  `0022`, `0024`), orphaned sidecars, the cache prune guard, a drain
  timeout, GitHub's backoff honoured per installation, the poll-shedding
  channel finally set (#76, #108, #110, #111, #113, #120). Since: a busy
  registration is deferred cleanup rather than a lost permission (#342),
  housekeeping recovers deferred registration cleanup (#344), and a
  cancelled run stops its provisioning runners (#348).

### Phase 2, operable and usable

* **ZF-201**: the journey's three defects, verify's repository selection, a
  standalone fake GitHub for Playwright and the connect-fail-then-recover
  spec. Since: the pool wizard's automatic and advanced paths (#311) and the
  installer's Docker-or-Podman offer (#322).
* **ZF-202**: partial-failure tolerance in the drawer, the poller made
  visible, the runner's stages, `GET /jobs/{id}/explanation` rendered by the
  drawer and the CLI, the `ZOOMIES_SEED_STUCK` fixture and its Playwright
  project, and the admin-only, capped, secret-free support bundle. Since:
  fault categories and a rerun button (#295).
* **ZF-203**: the store primitives and startup guards, `zoomies backup`,
  `zoomies restore` with every refusal before anything moves, the fence, and
  the mechanical drill in CI. Since, and beyond the package as written: the
  shared `internal/backup` package, scheduled copies, a staged restore
  applied by restart, eighteen `/backups*` routes under `backups.read`,
  `backups.write` and `backups.restore`, the Backups page, S3-compatible
  offsite destinations sealed under the instance key (`0036`), and a
  configuration export and import with a dry run (#297, #304, #306, #308,
  #312).
* **ZF-204**: protocol checked on every heartbeat with an incompatible host
  excluded like a cordon, skew visible through one comparison, schema
  safety tested from both old releases, backup-before-migrate, provenance
  attestations, OCI labels, the published-tag guard, a GitHub-hosted
  dispatch path, and `make test-upgrade` from the latest published release
  on every pull request. Since: assets upload one at a time and the `dev`
  channel survives a slow upload (#315, #316).
* **ZF-205**: the four series, the registry-wide label rule, prune tests,
  UI honesty about the window, the ten-thousand-job load fixture and its
  record ([load-7d9441b.md](roadmap/validation/load-7d9441b.md)), the audit
  index it found. Since: a fleet-chosen queue warning threshold (#330), the
  usage report on the shared trend plot (#300), and the event bus fixes of
  #309.
* **ZF-206** (`implemented`): the Windows agent builds, installs as a
  service through `sc.exe`, unpacks the `.zip` runner behind the same
  traversal refusal, kills a job's tree through a job object and retries a
  locked work directory. Nothing has joined a Windows host or run a job on
  one; section 9 says what happens when a fleet does.

### The September packages

* **ZF-218** (`implemented`): `deploy/marketplace/`: the pinned release
  contract and image lock, the cloud-config renderer, the answer template,
  the first-boot script, five certificate arrangements, the operator and
  partner guide and `SUPPORT.md`. The pin is stale (section 11).
* **ZF-220**: usage sampled through heartbeats (`0028`), a headroom score,
  pending-start accounting, pressure holds with hysteresis, the throttle
  ladder (`0029`), host history for the utilisation chart (`0032`), the
  host card's explanations and five gauges. Since: reserves that scale with
  the machine, a busy daemon retried and then avoided (#338), and
  `agent.bootstrap_cpu_grace` (#343).
* **ZF-221** (`in_progress`; section 8): every bullet version 2.40 listed as
  prepared, plus registration admission, the FIFO start queue, queued-start
  deadlines, conflict recovery, deferred cleanup recovery and prewarm
  telemetry (#343, #344, #345, #347). What remains is the durable
  runtime-incident record.
* **ZF-214a**: the provider contract, its fake, the conformance suite, the
  `providers` and `machines` schema (`0030`), the machine loop on its own
  clock, the pure machine-demand calculation and the scale-from-zero signal.
  **ZF-214b** (`implemented`): the Proxmox provider end to end, its preflight
  and discovery, ownership marks, the REST and CLI surface, the Providers and
  Machines pages, `zoomies gateway` for a private provider (`0033`), the
  runbook and the qualification harness. The harness has not run against a
  cluster, and section 7 says why that no longer gates anything.
* **ZF-301a and ZF-301b**: the real-GitHub harness made honest (it can no
  longer skip green), and the drill tier (the built binary as controller
  and as a remote agent joined by token, a real workload on the machine)
  green on every pull request on amd64 and arm64.
* **ZF-302**, five of six: the controller and agent killed mid-job, a dead
  Docker socket named, a rate-limited installation standing down and coming
  back, a failing JIT endpoint leaving nothing behind, and the
  restore-and-rollback drill. Each found something worth the row it wrote
  (change record, versions 2.28 to 2.32).
* **Tailcat private hosts** (the delivered half of ZF-401 to ZF-404): a
  private connection built into the agent and the controller, the Add-a-host
  choice, the observed connection badge, `hosts join-token create
  --connection tailcat`, two real-relay tests in the ordinary suite, and the
  two recovery fixes of 18 September (#327, #329).

## 7. Withdrawn

Version 3.0 stops authorising the following. Each entry says what it was
for and what withdrawing it costs, so that the decision is one somebody can
disagree with later rather than a silent omission. The code, harnesses and
records they produced stay where they are.

* **Gate F**, the trusted-workload beta gate: the seven-day window, the two
  hundred attempts with three over-capacity bursts, the twenty failures and
  twenty cancellations, the readiness verdict. *Cost*: no written beta
  verdict. *What survives*: every definition it forced into the product
  (decision 6), the restore drill on every pull request, and the fact that
  the product carries this repository's CI.
* **ZF-303**, the readiness record and the second operator. *Cost*: the
  second operator's set-up-and-diagnose session, which was real usability
  feedback; the first fleet onboarding onto an operated instance is that
  session now. *What survives*: the evidence helper's substance becomes
  ZF-223, a report per installation that the platform owes a fleet.
* **ZF-301c**, the credentialed real-GitHub scenarios and the nightly
  `e2e.yml` with a protected environment and a tunnel, with the `e2e.yml`
  half of decision 11. *Cost*: a run link per adversarial scenario
  (cancellation, an intended failure, a DinD build, a service container)
  against real GitHub. *What survives*: the harness under `test/e2e` and
  `make test-e2e` for a developer with an App; the drill tier covers the
  same fault shapes against the fake; CI on the fleet exercises real
  delivery, real Docker and DinD on every pull request.
* **ZF-302's two remaining halves**, the small-filesystem drill and the
  daemon-stopped-then-restored drill, which needed the reference host.
  *Cost*: a recorded answer to what the fleet does when a host's disk fills
  or its daemon restarts. *What survives*: the product surfaces both through
  `host.*` problems and the disk gate; the drill tier now runs on a
  Docker-backed runner, so whoever wants the second drill can write it
  without a reference host.
* **ZF-219**, the pristine-VPS verification and the friendly-provider
  pilot. *Cost*: no independent operator has followed `docs/marketplace.md`
  end to end, and it says so. *What survives*: the rendering tests in
  `internal/docs` and the evidence template
  ([marketplace-deployment.md](roadmap/validation/marketplace-deployment.md))
  with every row `not run`, which is honest.
* **ZF-211's measurement half**, the reproducible cold-and-warm workload
  with p50 and p95 per stage. *Cost*: no quotable queue-to-start figure with
  its setup. *What survives*: the histograms and stored timestamps that
  would produce one, and the documentation half of the package, which
  became ZF-005 and is done.
* **The live-qualification clauses** of ZF-220 (a live fleet before release
  inclusion), ZF-221 (bursts of 1, 4, 8 and 16 on a reference Docker host;
  the runner-and-sidecar budget evaluation), ZF-214b (twenty cycles on a
  disposable cluster before the provider is called qualified) and ZF-206
  (a job on a Windows host before the package closes). *Cost*: the support
  matrix keeps saying "built, not run" for Windows and Proxmox, and
  `docs/proxmox.md` stopped saying "qualified" (ZF-005). *What survives*:
  the harnesses, which a fleet with a cluster or a Windows host can run, and
  whose rows are welcome.
* **ZF-203's GitHub half** (sign in, re-join an agent, run a job after
  lifting the fence, recorded once), **ZF-204's** "first real drill recorded
  in `roadmap/validation/`", **ZF-201's** real-credential run of the Go
  end-to-end tier, and **ZF-001's** reference-host version record. *Cost*:
  none that a CI drill or the upgrade job does not already cover.
* **Decisions 3, 4, 9, 10 and 12** as owner actions, and **decision 11's**
  nightly half, each marked in section 4.
* **ZF-402**, resumable operation ids, cancellation of an in-flight
  enrolment and the optional SSH bootstrap. *Cost*: none for the primary
  target. The enrolment already fails closed (a tunnel that will not start
  mints no token, an unused token can be revoked, and a re-join on a host
  that already joined needs `--yes`) and the platform never dials a fleet's
  host (rule 16), so an SSH bootstrap contradicts the target rather than
  serving it.
* **ZF-404b**, dedicated-host maintenance windows, controller-driven agent
  upgrades and retention-driven cleanup of a host. Deferred indefinitely
  (decision 25) rather than withdrawn: it stays in section 9 with its
  reason.

## 8. Active packages

In the order section 10 sequences them. Each was re-read against `main` at
`9a80b31`; where the code has moved since the package was written, the
package says so and describes the work from where the code is now.

### ZF-207: two audiences for one instance

**Classification: extension; M, and the gate the primary target waits on.**
Not started, and further from done than when it was written, because the
instance-settings work moved the boundary the wrong way for this shape.
What the code does today: `store.Role` has three values and `users.role` and
`api_tokens.role` each carry a `CHECK` naming them (`0001_init.sql`); the
three-role vocabulary is repeated in the CLI's `--role`, the OIDC group
mapping, `web/src/lib/roles.ts`, `types.ts` and every `x-zoomies-role` in the
OpenAPI document. `settings.read`, `settings.write`, `diagnostics.read`,
`recovery.write` and the three `backups.*` actions all sit at `admin`, as do
`/settings/export` and `/settings/import`. Migration `0031` and
`internal/config/settings.go` split keys into *bootstrap* (the database path
and the encryption key), *local* (a standalone agent's own keys) and
*instance* (everything else, `server.bind`, `server.tls.*`,
`server.trusted_proxies`, `security.disable_auth`, `security.rate_limit_logins`
and `oidc.*` included) and any admin writes an instance key through
`PATCH /settings`; the validator refuses only a change that would stop the
next start. `GET /settings` returns `config_path`, `database_path` and the
subscriber count; the bundle carries the paths, the OS, the CPU count and
the heap. `Controller.Problems()` builds one list, every validator warning
and error first, and `problems.updated` reaches every subscriber; the only
per-identity redaction on the stream is the pool `env` blanking in
`sse.go`, which is the pattern to copy. "This controller" appears on
fifty-three lines under `web/src` and "the controller log" in nine places,
`ErrorState` among them. The audit list withholds `before` and `after` by
role and never the `ip`. The About panel already hides the database path
below `admin`, and the Settings pages a role cannot use are shown locked
with a reason, on purpose.

**Do, in five pull requests:**

1. **The role.** A fourth role, `platform`, above `admin`, in `store.Role`
   and its rank, in a migration that rebuilds the two `CHECK` constraints
   (a data-preserving rebuild under decision 7, reason in the header), in
   the CLI's `--role`, in an `oidc.platform_groups` mapping, in the two
   TypeScript vocabularies and in the OpenAPI document. The migration
   promotes the oldest enabled `admin` to `platform`, because on every
   existing single-team instance the person who installed it is the
   platform and the alternative (an instance in which nobody can change a
   timer) is a lock-out. Every bootstrap path creates `platform`: the
   setup-token page (whoever can read the log is the platform), the answer
   file and ZF-210a's environment variables. An admin created by one is
   not. `ErrLastAdmin` gains a sibling: the last enabled `platform` cannot
   be demoted or deleted.
2. **The actions.** `platform.settings.read`, `platform.settings.write`,
   `platform.diagnostics.read`, `platform.recovery.write` and
   `platform.backups.read`, `.write`, `.restore`, with `GET`/`PATCH
   /settings` for platform-scoped keys, `/settings/export`,
   `/settings/import`, `/diagnostics/bundle`, `/recovery/unfence` and every
   `/backups*` route behind them. Backups move whole: a backup is the
   process's database sealed under the process's key, and a fleet that
   wants its data has ZF-210b. The walk tests gain the fourth fixture, and
   `TestEveryActionHasARole` keeps holding.
3. **The settings scope.** A fourth `config.Scope`, `ScopePlatform`, on the
   keys decision 31 lists. `Stored()` stays true for them, so the export,
   the import and the file seed keep working; `editable()` requires
   `platform`; a fleet identity's `GET /settings` omits platform-scoped
   keys, `config_path`, `database_path` and the subscriber count entirely,
   and the Configuration page renders what it is given. On a single-team
   instance the platform account sees what admin sees today.
4. **The problems split.** `Audience` (`platform` or `fleet`) on
   `controller.Problem`, set where each is made: every validator finding,
   the lease, loop panics, the update check, the capacity-demand receiver,
   the `backup.*` problems and the private-connection listener are the
   platform's; the fleet's are the rest. `GET /problems` filters by identity
   and `problems.updated` is rendered twice and chosen per subscriber, the
   way pool `env` values already are. ZF-222 builds on the fleet half.
   The full bundle is `platform.diagnostics.read`; `admin` gets a fleet
   bundle from the same handler with the instance section reduced to the
   version and the build, because "send me a bundle" is the platform's
   first support question and the fleet has to be able to answer it.
5. **Tokens, copy and audit.** An `owner_role` column on `api_tokens`, in
   the same migration as the role: a token minted by a `platform` identity
   is invisible to and irrevocable by anyone below `platform`, which is
   where the platform's metrics scraper token lives. "This controller"
   becomes Zoomies or the fleet; the nine "controller log" sentences become
   one `supportHint()` on each side that says to quote the request ID to
   whoever operates the instance; a test in `internal/docs` fails on either
   phrase in `web/src` outside an allowlist. The audit `ip` is blanked below
   `admin`, in the same place the documents are. The locked-with-a-reason
   Settings pages stay as they are: the package once asked for absence, the
   code chose a reason, and the reason is better.

**Accept when:** a viewer, an operator and an admin fixture each see no bind
address, file path, key location, process figure or another installation's
example in any page, response or event frame, with a Playwright test per
role; only `platform` can change a platform-scoped key, lift the fence, take
the full bundle, run a restore or list a platform token; an instance
upgraded from today has exactly one `platform` identity afterwards and it is
the account that bootstrapped it; the OpenAPI document, both clients,
`docs/security.md`'s role table and `docs/api-surface.md` say the same
thing; `zoomies users` and the Users page can create the fourth role and
say what it is for, in self-hosted words.

Depends on ZF-202 (done). Size M, larger than written: the scope split and
the schema rebuild are the work. Session: Claude Fable 5.1 at `high` for the
scope split and the migration, Claude Opus 5 at `high` for the rest.
Decisions: 27, 30, 31.

### ZF-208: limits at the public and agent edges

**Classification: extension; S to M, the second gate.** Not started. Since
it was written, one edge limit landed, the OIDC pending-state cache is
capped at 1024 with a 429 (#317), and within-tier fairness landed with
#345, so the create-budget item is the tier boundary alone. What the code
does today: only `/agent/join` is rate-limited among the agent routes;
heartbeat, results and report carry the one-megabyte body cap and nothing
else, and a report is a bare array with no count cap; `PollTasks` counts
polls in flight fleet-wide and sheds only polls that found nothing, from
256 held to fifteen seconds at 512, so one host's polls slow every host's;
the log relay is exempt from the body limit by design; no `limits.*` key
exists and nothing bounds hosts, pools, join tokens or event subscribers;
the webhook body is five megabytes. And a fleet admin can point `oidc.issuer`,
`capacity_demand.destination_url`, `github.api_base_url`,
`agent.runner_download_url`, a backup remote's endpoint or a provider's
endpoint at the process machine's metadata service or its LAN, because the
only address check is loopback on providers.

**Do, in one pull request per item:**

1. A per-host token bucket on heartbeat, report and results, reusing
   `auth.RateLimiter` keyed by host id and sized from
   `agent.heartbeat_interval` so a well-behaved agent never meets it; one
   in-flight task poll per host, a second answered at once with an empty
   batch; a cap on runners per report; a per-stream byte budget on the log
   relay that drops rather than blocks, as the relay already does.
2. Fleet-wide ceilings as configuration (`limits.hosts`, `limits.pools`,
   `limits.runners`, `limits.join_tokens`, `limits.event_subscribers`)
   platform-scoped under decision 31, each zero by default meaning
   unlimited, each refused at the handler with a message naming the
   ceiling, each a warning in the validator when set on a loopback bind,
   where it protects nothing.
3. The create budget shared fairly across pools of *different* priority
   before priority decides within a tier: a highest-priority pool may take
   the whole tick only when no lower tier has demand it has waited a full
   interval for. The scaling reason says when a pool was deferred by this.
4. The webhook body cap lowered from five megabytes to one; a
   `workflow_job` is tens of kilobytes.
5. An outbound address guard: one `config.CheckOutboundURL` refusing
   loopback, link-local and private-range destinations for the six settings
   above unless a platform-scoped `security.allow_private_egress` is set,
   called from the validator for the four keys and from the backup-remote
   and provider validators; a finding `egress.private_target` (error) with
   its row in `docs/problem-codes.md` and a paragraph in `docs/security.md`.
   On a single-team instance whose OIDC issuer is on the LAN, the operator
   sets the one key and the finding names it.

**Accept when:** each limit has a test that reaches it; in the drill tier,
one agent hammering heartbeats and polls leaves another host's task delivery
within one poll interval, asserted rather than recorded; a private-range
issuer is refused by name and admitted by the setting.

Items 1, 3, 4 and 5 depend on nothing; item 2's scope depends on ZF-207.
Size S to M. Session: Claude Sonnet 5 at `high`; Claude Opus 5 at `high` for
the address guard, where a wrong list is a hole. Decisions: 27, 30, 31.

### ZF-209: a metering ledger the usage report can stand on

**Classification: extension; M.** Not started. The seam is as it was: the
usage report is computed at read time from `jobs`, `runners` and
`usage_capacity_samples`, ZF-004 made it say where those rows begin, and
the prune loop rolls nothing up before it deletes. Since it was written the
report has been redrawn (#300) and `history_from` is on the JSON response
but not on `/usage.csv`. On the primary target these are the figures the
two parties reconcile, and today they are truncated at `retention.runners`
(seven days) by a key the fleet can shorten.

**Do:**

1. A `runner_sessions` table written once, when a runner's cleanup is
   confirmed (`cleaned_up_at` in its `0022` sense, host removal and
   GitHub's absence both seen) with runner, pool, host, installation, the
   job it ran if any, started, registered, finished, and the pool's cost
   rate at the time. Never updated; its own `retention.runner_sessions`,
   a year by default, platform-scoped with the other retention keys.
2. A `usage_daily` roll-up per pool, host and installation, produced by the
   prune loop *before* it deletes the rows it is computed from, so the
   report is complete for every day the roll-up covers however short the
   row retention is. Integer seconds and integer minor currency units keep
   sums exact.
3. `/usage` reads the roll-up for the days it covers and the rows for the
   rest, `history_from` becomes the roll-up's start rather than the rows',
   and `/usage.csv` gains the same field. `docs/metrics.md`'s "what happened
   last month is your scraper's problem" sentence says what is kept now.

**Accept when:** a 90-day report taken with seven-day runner retention
matches, to the second, one taken with retention off; a runner session is
written exactly once across a controller restart mid-cleanup (the
controller-restart drill); the roll-up is reproducible from the sessions
table alone.

Depends on ZF-105 and ZF-205 (done) and on ZF-207 for the scope of the
retention keys. Size M. Session: Claude Opus 5 at `high`. Decisions: 27, 30.

### ZF-210: unattended provisioning and lifecycle

Split as version 2.34 split it, without changing the ID: **210a** is
bootstrap and readiness, **210b** is export and purge. The parent is
complete only when both meet acceptance.

#### ZF-210a: bootstrap and readiness

**Classification: extension; S.** In progress, because its fourth item
landed before the package existed: `zoomies init --answers FILE` implies
`--non-interactive`, `--print-answers` writes the template, the file
refuses rather than guesses at anything missing, and `mode: controller`,
`github.skip` and `pool.skip` already describe a controller-only instance;
the marketplace bootstrap depends on it. Two things it does not do: a
containerised deployment skips the administrator by design and ends in the
browser, and the first administrator the file creates writes no audit row.
`bootstrap_required` is on `/api/v1/meta` and not on `/readyz`, so a
provisioner waits on two endpoints. Every page that describes an unattended
install still tells the reader to read the setup token out of the log.

**Do:**

1. `ZOOMIES_BOOTSTRAP_ADMIN` with `ZOOMIES_BOOTSTRAP_PASSWORD_FILE` or
   `ZOOMIES_BOOTSTRAP_TOKEN_FILE`, read once at controller start beside
   `printSetupToken`: when the users table is empty they create the first
   identity as `platform` (ZF-207), audited as `auth.bootstrap` with actor
   `system`, and once any user exists they are ignored with a warning
   finding that names them. The token variant mints a platform API token
   instead of a password, so a provisioner can drive the instance's API
   with no browser at all. The setup-token flow stays for a person, and
   the answer file's `admin` keys now audit the row they create.
2. `bootstrap_required` on `/readyz` beside `fenced`, so one endpoint
   answers "can I use this yet".
3. A committed controller-only answers template (`agent.embedded: false`,
   `github.skip`, `pool.skip`, TLS from files, the external URL) beside the
   marketplace one, tested the way `internal/docs` tests that one; the
   `agent.*` settings section hidden when the instance reports
   `agent.none`; `docker-compose.yml`, `docs/configuration.md`,
   `docs/marketplace.md` and `docs/security.md` describe the environment
   variables as the unattended path and stop telling every reader to scrape
   the log. The self-hosted reason is on the page already: a compose file
   or a Terraform module has nobody watching.

**Accept when:** a compose file brings up a controller, a platform
identity and one joined agent with no human step and no log scraping, as a
fixture test; the setup-token page still works for a person; the audit log
shows who bootstrapped and how, on every path.

Depends on ZF-207 for the role. Size S. Session: Claude Sonnet 5 at `high`.
Decisions: 27, 30.

#### ZF-210b: export and purge of one installation

**Classification: extension; M.** Not started. `DELETE /installations/{id}`
removes the installation, its pools and their runners, and leaves jobs,
deliveries, scaling events and capacity samples behind by design; the
configuration export moves settings, not history; the whole-database backup
is sealed under a key the fleet does not hold (decision 32).

**Do:**

1. `zoomies export --installation ID` writes everything about one
   installation (its pools, runners, jobs, deliveries, scaling events, the
   sessions from ZF-209 and the audit rows that name any of them) as one
   archive, and with `--passphrase-file` re-seals that installation's
   private key and webhook secret under the passphrase, using the
   argon2id-and-AES-GCM pattern `backup_remotes.passphrase_enc` already
   uses, so a fleet can take its App to another instance without the
   instance key.
2. `DELETE /installations/{id}?purge=true` removes the same set rather than
   only the rows a foreign key reaches. Both are audited; neither touches
   another installation's rows, and a test with two installations side by
   side proves it byte for byte.

**Accept when:** export then purge of one installation leaves the other's
rows and figures byte-identical; an exported archive restores onto a fresh
instance under the passphrase and the installation verifies against
GitHub.

Depends on ZF-209 for the sessions in the archive and on ZF-207 (the export
is a fleet action; the purge is admin with confirmation). Size M. Session:
Claude Opus 5 at `high`. Decisions: 27, 32.

### ZF-221: runner resilience and efficient startup, the remainder

**Classification: extension; S to M.** Everything version 2.40 listed as
prepared is merged, and so is most of what it listed as remaining: the
registration budget is `scheduler.registration_concurrency`; a runner's
resource sample carries `sampled_at` and the host card says "measured … ago"
with a `usage_fresh` flag and a gauge, so cached readings are no longer
presented as current; ambiguous create and start responses are reconciled
against labels and ownership before anything is retried
(`container_conflict.go`); and the runner-and-sidecar share was re-cut
(#336). Two things remain, one of them only if asked for. On the primary
target the second matters more than anywhere: the platform supports a host
it cannot reach, and has only what the agent reports.

**Do:**

1. **Durable runtime incidents and visible recovery.** The agent's runtime
   cooldown (consecutive failures, the kind of the last one, the retry
   time) lives in the agent's memory and surfaces as a log line. Report it
   in the heartbeat; keep it on the host row beside `usage` and `throttle`
   as one JSON column (next free prefix); render it on the host view and the
   host card ("runtime recovering: third failure, retrying in 40 s") and as
   `host.runtime_recovering` in the drawer with the fix the agent itself
   would give; count it. A start that fails because an image will not pull
   names the registry host and the pool, as `host.image_pull_failed`, from
   the prewarm and start results the agent already sends; a fleet host
   whose egress blocks `ghcr.io` otherwise shows only runners that never
   register.
2. **A configurable active-create budget**, only if the operated instance's
   own figures show the one-at-a-time foreground start is what a host is
   waiting on. Not authorised on speculation.

**Accept when:** a runtime failure on a host is visible on the host card
and in the drawer within one heartbeat with its retry time, survives a
controller restart, and clears on the next success; an image that cannot
pull names the registry and the pool within one start attempt; nothing new
is presented as current without its age.

Depends on ZF-220 (done). Size S to M. Session: Claude Opus 5 at `high`.
Decisions: none.

### ZF-401 to ZF-404: the fleet's own hosts

The private-host flow is delivered (section 6) and the acceptance version 2
wrote for it (a private host joins, reconnects after a controller or agent
restart, and can be drained and revoked from the UI) is met by merged,
tested code: the two real-relay tests, cordon and remove on the host card,
join-token revocation, and a host deletion that revokes the agent's
credential because the token hash lives on the host row. What is left is
small, and it is re-scoped for a fleet whose hosts join a controller it does
not run.

#### ZF-401: onboarding a private host, what is left

**Classification: extension; S.** The relay region is resolved once, when
the listener first starts, and sealed into the identity; if that relay is
unreachable later the listener cannot start, and the only sign is a log
line. Custom relays are not exposed, and the docs say the hosted relays are
rate-limited and keep metadata.

**Do:** a `tailcat.unavailable` warning raised by a gather section in
`Problems()` from the listener's state, with the platform audience under
ZF-207; re-run the relay expansion when the node fails to start, with the
sealed region as first choice; and a sentence on the private-hosts page
that a controller with a public agent endpoint needs a private connection
only for hosts that cannot reach it, which is the self-hosted truth, and
on the primary target the normal case.

**Accept when:** a controller started with an unreachable sealed relay
raises the problem within a minute and recovers without a restart once a
relay answers, in a test against the local relay the suite already runs.

Depends on ZF-207 for the audience. Size S. Session: Claude Sonnet 5 at
`high`.

#### ZF-403: revocation, what is left

**Classification: extension; S, demand-gated.** Closed as implemented;
one follow-on. Every private host of an instance holds the same tunnel
address as a persistent capability, and deleting a host revokes its Zoomies
credential but not that address, so an off-boarded machine can still reach
the listener and then fail to authenticate. Rotating the identity
invalidates every private host at once, which is why it is not automatic.

**Do, when a fleet asks:** one audited platform action that rotates the
identity, re-mints a join command for every private host and lists them,
with the page saying what it will cost.

Depends on ZF-207 (a platform action). Size S. Decisions: none.

#### ZF-404: what the agent owns on a host, written down

**Classification: extension; S; documentation and one default.** The
code is non-invasive already (nothing in the tree upgrades a package,
touches a firewall, reboots, or prunes containers, images or volumes) and
the reconciliation found the one exception decision 25 names, the
daemon-wide builder-cache prune. The promise is not written anywhere a
fleet would read before running the enrolment command.

**Do:** a "what the agent owns" section on `docs/security.md` and the
private-hosts page (its service and user, `agent.work_dir`, the containers
carrying its labels, its per-pool cache directory, and the builder-cache
target with the shared-daemon advice of decision 25) linked from the
Add-a-host page's command step; and the agent's own tests pinned to it
where they can be, so that a future prune that widens fails a test that
names the promise.

**Accept when:** the pages say it, the Add-a-host page links it, and
`internal/docs` keeps the section present.

Depends on decision 25. Size S. Session: Claude Sonnet 5 at `high`.

### ZF-222: a status view for the audience that cannot sign in

**Classification: extension; M. Decision 29, recommended adopted.** Every
fleet fact needs `viewer`. The developer whose job has queued for twenty
minutes has no account and GitHub tells them only "queued"; the controller
knows whether it is capacity, labels, a lost permission or a webhook secret
that stopped verifying, and none of it reaches them. On the primary target
that developer is on the fleet's team, and the projection is built on the
fleet half of ZF-207's split, so it cannot carry a platform finding.

The cost is disclosure, so the projection is name-free and banded rather
than trimmed: proving a body contains no names is a test, and keeping a
view's prose from naming things is a promise renewed every time somebody
edits it.

**Do:**

1. `GET /api/v1/status`, a projection rather than a view: the one place in
   the API that is deliberately not a resource's `GET` shape. `state`
   (`healthy`, `degraded` or `blocked`, from the highest severity among the
   fleet's problems), `since`, `version`, and counts as bands (`none`,
   `few`, `many`, `backed_up`) rather than integers, with the median and
   p95 queue wait rounded to the minute. Its `reasons` carry `code`,
   `severity` and `since` and nothing else. A public sentence per code lives
   beside the operator's in `docs/problem-codes.md`, tested in both
   directions by `internal/docs` the way that page already is.
2. `status.mode: off | authenticated | public`, off by default, with
   `ZOOMIES_STATUS_MODE`, platform-scoped. `public` raises a `status.public`
   warning naming what becomes readable without an account, and an error
   when the bind is not loopback and TLS is off. `off` means all three
   routes answer 404.
3. `/status` as its own Vite entry (`web/status.html`) with its own, much
   smaller budget in `web/vite.config.ts`, polling `/api/v1/status` every
   thirty seconds and never opening `/api/v1/events`, because every frame
   on that bus is a resource view and carries names.
4. The three states map onto the existing `danger`, `pending` and `idle`
   tokens; `docs/ui-guidelines.md` records the mapping.
5. `GET /status.svg`, the fleet's state as a self-contained badge on the
   `docs/badge.svg` pattern, for a team's wiki or a repository README.
6. The paperwork an endpoint change carries: `api/openapi.yaml` and both
   generated clients, rows in `docs/api-surface.md` and
   `docs/configuration.md`, a dangerous-toggle section in `docs/security.md`,
   and the page in `docs/ui.md`.

**Accept when:** a fixture fleet whose every pool, host, repository and
runner is named something distinctive produces a `/api/v1/status` body and a
rendered `/status` page containing none of those names, asserted by a test
that searches each response for every fixture name; each of the four
reasons a job waits produces a distinguishable public state; every problem
code has a public sentence; `status.mode: public` on a public bind without
TLS refuses to start and names which setting to change; the default leaves
all three routes 404; the app shell's gzipped size is unchanged; Playwright
covers the page signed out, on a phone and through the accessibility pass.

Depends on ZF-207 and ZF-202. Size M. Session: Claude Opus 5 at `high`;
the disclosure boundary is the work and the page is the easy half.
Decisions: 29, 31.

### ZF-213: pool configuration as code

**Classification: extension; M.** In progress, because half of it landed
under another name: `GET /settings/export` and `POST /settings/import`
(#297) round-trip every non-default instance setting as a versioned,
secret-free document, plan each key as `change`, `unchanged`, `unset` or
`refused` with the current and incoming values, apply as one change or none,
dry-run, and skip; repeat apply is idempotent by construction. That is every
property the package asked for, for the wrong object: the document carries
no pools, and on the primary target the instance settings are the platform's
while the pools are the one thing the fleet owns and would want as a file,
to move between a self-hosted instance and an operated one, or between two
of theirs. The reusable-workflow and matrix clause is already met by the
migration planner's stated refusal and per-job overrides.

**Do:** a `pools` export and import pair modelled exactly on
`handlers_settings_transfer.go` (a version field, secret-free by
construction since pools hold no secrets, installations referenced by
target rather than by `ins_` id so a document moves between instances, a
plan by pool name with `create`, `change`, `unchanged` and `refused`,
`dry_run`, `skip`, one change or none) reusing `validatePoolInput` per pool
and `HostCouldRun` for the stranding check, with the CLI verbs, the page's
dialog and the rows in `docs/backup-and-restore.md` beside the settings
half.

**Accept when:** export then import onto a fresh instance with the same
installation target reproduces every pool's platform, labels, limits and
runner settings; repeat apply changes nothing; a conflicting edit produces a
readable plan; absent fields have the semantics the settings import
already documents; dry-run creates no resources.

Depends on ZF-201 and the settings transfer (done). Size M. Session:
Claude Opus 5 at `high`. Decisions: none.

### ZF-215: capacity fallback and scheduled readiness

**Classification: extension; M; demand-gated.** Nothing of its own exists;
what it was told to reconcile (`min_runners`, `idle_timeout`, priority,
the per-pool timing overrides of `0037`, prewarm and its telemetry, elastic
CPU) is all on `main`. `bestPool` picks one pool per job and a job whose
pool is full waits there; no pool has a schedule. On the primary target both
halves are cost levers on machines the fleet pays for: a schedule keeps
`min_runners` warm only in the fleet's working hours, and fallback lets a
heterogeneous fleet absorb a full pool.

**Do, when a fleet asks:** a time-bounded `min_runners` with an explicit
timezone; an opt-in per-pool `fallback_to` list consulted only when the
claimed pool reports held or failing, never crossing installation, OS,
architecture or trust policy, with the reason string saying so; idle cost
shown beside the schedule. Provider fallback stays gated on a qualified
provider.

**Accept when:** scarce capacity follows the declared policy with a visible
reason; no duplicate jobs or retry storm in the drill tier; schedule timezone
and idle cost are explicit.

Depends on ZF-208. Size M. Session: Claude Opus 5 at `high`.

### ZF-212: cache recipes

**Classification: documentation; S; demand-gated.** The caches exist and
are instrumented (the per-pool cache directory with its scope and size
limit, the builder-cache target, image prewarm with its two series) and
nothing publishes a recipe for using them from a workflow. The S3-compatible
cache storage half is a trust question before it is a feature on the
primary target, because it would route a fleet's cache traffic to storage
the platform configures, and it is not authorised.

**Do, when a fleet asks:** recipes for Go, npm, Maven and pip against
`/opt/zoomies-cache` and for BuildKit `--cache-to`/`--cache-from
type=local`, on the configuration page beside "The pool cache".

Depends on nothing. Size S. Session: Claude Sonnet 5 at `high`.

### ZF-223: a report per installation

**Classification: new; M; after ZF-209.** What ZF-303's evidence helper
would have computed once for a readiness record is what a platform owes a
fleet every month: per installation over a window, the counts the contract
names (observed, eligible, created for, ran here, ran elsewhere, platform
fault, cleanup pending and converged) and exact p50 and p95 of eligible to
first create task, create to registered, and cleanup convergence, from the
stored timestamps rather than bucketed histograms. Every timestamp and
fault kind it needs is in the product; ZF-209's roll-up is what keeps the
counts past retention.

**Do:** `GET /installations/{id}/report?window=` and a section on the
Usage page, with the definitions linked to `docs/metrics.md`; `/usage.csv`
gains the counts.

**Accept when:** the figures for a fixture fleet match a hand computation
from its rows; a window older than the row retention still answers from
the roll-up; nothing in the body names a repository the identity may not
read.

Depends on ZF-209. Size M. Session: Claude Opus 5 at `high`. Decisions: 6.

### ZF-224: auto-recovery on a lost runner

**Classification: extension; S; delivered 22 September, alongside ZF-207.**
A runner that dies under a job fails it in a way GitHub cannot tell from a
test failure. Zoomies already knows better (`FleetFailed` is the split the
Jobs page draws) and #406 gave an operator a button. This makes the choice
a setting rather than a standing refusal, on the owner's instruction and
recorded as [decision 0004](roadmap/decisions/0004-the-fleet-may-re-run-a-job-it-broke.md).

**Done:** `scheduler.auto_rerun`, off by default, re-runs a job whose runner
died under it, bounded by `scheduler.auto_rerun_limit` (1–5, default 1)
counted from GitHub's own run attempt; the `scheduler.auto_rerun_on` warning;
a timeline entry marked *via recovery*; and
`zoomies_job_reruns_total{pool,trigger}`, which also labels the button's
re-runs so the two are comparable.

**Accepted because:** a table test covers off, on, a workflow's own failure,
the bound reached and a higher bound; a repeated delivery buys no second
re-run; the validator refuses a limit outside 1–5; the default configuration
draws no new finding.

Depends on nothing. Size S. Decisions: 0004.

### ZF-225: two-step sign-in for local accounts

**Classification: extension; M; proposed and delivered 27 September.**
A local account signs in with a password and nothing else, so a password
reused on a site that leaked is the whole of an account's defence. Single
sign-on accounts already get a second factor from their identity provider;
the accounts the installer and the users page make had no way to have one.

**Done:** optional TOTP (RFC 6238: SHA-1, six digits, thirty seconds, one
step either side) that each account turns on from its account page; a QR
code drawn by the controller with an in-repository encoder, the key as text,
a confirming code, and ten single-use recovery codes shown once. The key is
sealed with the instance key; recovery codes are stored hashed; the last
accepted time step is recorded so a code is accepted once. Sign-in answers a
right password with a five-minute pending sign-in bound to the browser, whose
code attempts are charged to the existing sign-in limits. Turning it off and
issuing new recovery codes take the password and a code. An administrator
resets it for a lost phone from the users page or `zoomies users
reset-two-step`, audited as `user.two_step_reset`. `security.require_two_step`
(off by default) makes every password account enrol at its next sign-in, with
the `two_step.required` info finding. Single sign-on accounts and API tokens
are not asked, and the documentation says why.

**Accepted because:** RFC 6238's vectors, the QR encoder against the
standard's worked Reed-Solomon example and format tables, and a round trip of
every supported version; a replayed code, a spent recovery code, an expired or
skipped code step, and code failures reaching the sign-in limit are each
refused in a test; the administrator's reset writes its audit row; the
requirement forces enrolment and leaves single sign-on sessions working; the
migration applies to a database with accounts and sessions in it; and a
Playwright spec turns it on, signs in with a code and a recovery code, and
turns it off.

Depends on nothing. Size M.

### ZF-226: placement from job history

**Classification: extension; M; proposed and delivered 3 October.** A
runner is sized for the average job, so one heavy job on a small host was
killed for memory and retried onto the same host.

**Done:** each job's peak CPU and memory is recorded; placement and sizing
use the heaviest job waiting in a pool, and a host too small for it is held
off with the reason stated. `scheduler.history_sizing` is `off`, `shadow` or
`on` and starts in `shadow`. The job page, `job_stats` and the MCP tools
show peak usage, and a job killed for running out of memory raises
`jobs.oom_killed`. `job_stats` can group by controller version.

**Accepted because:** the scheduler stays pure and its decisions are table
tests; shadow mode records what `on` would have done and changes nothing.

Depends on ZF-220. Size M.

### ZF-227: AI Context

**Classification: new; L; in progress from 29 September.** Let an assistant
connected over MCP read a repository's source, within consent the
repository's owner gave, so an assistant spends fewer tokens finding its way.

**Done:** the foundations; the consent wizard and per-repository consent;
setup pull requests with verification against the trusted branch; retrieval
(`context_overview`, `context_read`, `context_search`, `context_pack`);
repair, amend and remove; reinstall; repository-only, owners and OIDC
uploads; token-savings and discoverability documentation.

**In flight:** assistant-written notes behind a separate `context.publish`
consent, GitHub Enterprise Server and a 503 naming the key host (#586);
failure reasons that name the fix and a resync with the trusted branch
(#591).

Depends on ZF-228. Size L.

### ZF-228: connecting an assistant and owning your tokens

**Classification: extension; M; proposed and delivered 27 to 30 September.**
Pasting a token into an assistant was the only way in.

**Done:** the controller is its own OAuth 2.1 authorisation server for
`/mcp`, so an assistant is added by address and signs in through the browser;
a client-metadata fetch refuses private ranges; rotating a secret asks for
confirmation; two-step sign-in is honoured. Owner decisions taken: the
single sign-on button text, hiding the password form while single sign-on
works, and discovery retried in the background; `security.mcp_oauth` on a
public address needs `server.external_url`; a client that registers again
gets its registration back, at 100 an hour; a password change and "sign out
other sessions" end the connections; every user manages their own tokens,
capped at their role (`tokens.own`); spent tokens can be deleted.

Depends on ZF-225. Size M.

### ZF-229: Kennel Club

**Classification: new; L; in progress from 1 October.** How the repositories
the fleet serves measure up against what affects CI and the fleet. Designed
and staged in [roadmap/kennel-club.md](roadmap/kennel-club.md), which is the
record for it; this entry is the roadmap's pointer.

**Done:** Stage 1 (ZF-229a), the evaluator and its six observed checks, the
store, the GitHub reader, the loop, problems, metrics, REST, MCP read tools
and the UI (#661, #669, #670); per-repository tracking (#699, #701); the
first of Stage 3 (ZF-229c) in #714, `ci.no_timeout`, `ci.no_concurrency`,
`ci.action_not_pinned` and `token.permissions_unset` behind
`kennel.workflow_checks`, as per-repository counts, and ten `setup.*`
presence checks behind `kennel.repository_setup`; part one of the rest of
ZF-229c as [agent-readiness.md](roadmap/agent-readiness.md) section 6
re-cuts it: the parser moved to `internal/kennel/workflow` with a fuzz
target and a dogfooding test over this repository's own workflows, location
evidence on every workflow finding (a blob SHA, a job index and a line, with
the path shown only where it passes a grammar), waivers that lapse when the
file changes, and the owed checks `exposure.target_checkout_pr_head`,
`ci.workflow_unreadable`, `ci.pins_without_updater`, `ci.label_unserved` and
`ci.secret_on_command_line` (#761); part two, which finishes ZF-229c:
`capacity.matrix_exceeds_pool`, a prompt on every open finding with the
page's Copy button, `zoomies kennel` with its five readers, and
`zoomies kennel check [path]`, the same checks over a checkout with nothing
sent anywhere, held by golden JSON (#767).

**Open:** Stage 4 (ZF-229d, repository settings); Stage 5 (ZF-229e, fix by
pull request); the documentation page.

Depends on ZF-227. Size L.

### ZF-230: a catalog of everything Zoomies can say is wrong

**Classification: extension; M; proposed 8 October.** Problem codes and
Kennel check codes are stable, but nothing machine-readable lists them with
what to do and how to see that it worked, and an agent has to scrape two
pages to learn them.
[agent-readiness.md](roadmap/agent-readiness.md) section 3 is the design.

**Do:** `docs/catalog.json`, generated by `make generate` from the
problem-codes table and `kennel.Checks()`, with a JSON schema and a CI diff;
`Fix`, `Verify` and `Docs` on the Kennel registry; a *verify* column for the
operator-facing problem codes; `GET /api/v1/catalog`; `zoomies commands
--output json` and the generated command reference; the provenance guard in
`internal/docs` and its commit-range step; the *for an agent* section of the
AI Context page.

**Not:** a struct table for problem codes (the inline literals and the docs
tests stay); a Markdown page per code.

**Accepted because:** the catalog validates and CI refuses a stale one; an ID
in a finding or an explanation that is not in it fails a test; the guard
fails on a seeded term.

Depends on nothing. Size M.

### ZF-231: why a job failed, with evidence

**Classification: extension; M; proposed 8 October.** `GET
/jobs/{id}/explanation` says what is happening and what to do, but not which
class of failure it is, what the evidence was, or where in the catalog to
read on; a timed-out job is not classified at all.
[agent-readiness.md](roadmap/agent-readiness.md) section 4 is the design.

**Do:** `class`, `confidence`, typed `evidence`, a bounded `log_excerpt`,
`problem_code`/`check_code` and ordered `next_steps` on the existing
payload; `zoomies why <job>` (and `jobs why`) taking an ID, a GitHub run or
job URL or `--latest-failed`, with documented exit codes; the richer
`get_job` over MCP with the excerpt in an untrusted block; the job drawer's
**Why** section; demo seeds for every class.

**Accepted because:** each seeded failure in `zoomies demo` returns its class
at high confidence; a job the fleet never touched is `workflow-failure`,
never `unknown`; a hostile log survives only as scrubbed text.

Depends on ZF-230. Size M.

### ZF-232: updating from the web UI

**Classification: extension; L; proposed 8 October, design approved by the
owner; nothing built.** The controller says a release exists and each host's
card prints a command, but every update is still somebody running
`sudo zoomies upgrade`, on the controller and then on each agent host.

**Implement:** `updates.mode` (`off`, `manual` or `auto`; off by default) and
`updates.soak`; a root-owned helper on each host, installed with that host's
consent, that runs the existing `zoomies upgrade` for one validated release
tag; the controller and each host updated as recorded attempts; and a pure
planner that rolls hosts to the controller's release one at a time and halts
at the first failure. The design is
[roadmap/in-product-updates.md](roadmap/in-product-updates.md) and the plan
[roadmap/in-product-updates-plan.md](roadmap/in-product-updates-plan.md). The
decision, which narrows decision 25, is
[decision 0011](roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md).

**Accepted because:** a request can only name a published release; the mode is
off by default and creates nothing; a host is updated only if someone on it
installed the helper; the planner is a table test; and every refusal and
failure has a test that fails with its rule removed.

Depends on ZF-207 and ZF-404. Size L.

### ZF-233: skills for a coding agent

**Classification: new; S; proposed 8 October; implemented 9 October.**
`skills/zoomies/` (#768) and `skills/zoomies-kennel/`, with the generated
command reference from ZF-230 pinned and tested, lint tests in `internal/docs`
and `cmd/zoomies` that hold every skill to what an installer needs, every
command it names to the binary, and the zoomies skill's three lists to every
command there is, and install docs in the README and `docs/cli.md`.
[agent-readiness.md](roadmap/agent-readiness.md) section 7 is the design.

**Open:** gate row 15's second agent for the kennel skill: one agent's smoke
test is recorded in its pull request; the owner runs the second.

Depends on ZF-230, ZF-229c. Size S.

### ZF-234: the documentation for ZF-229 to ZF-233

**Classification: extension; S; proposed 8 October.** `docs/kennel-club.md`
(owed by the Kennel Club record), the *why* section, the skills page, the
FAQ entries, and the CLI, API, AI Context and Connect Claude updates.
Foldable into each package's pull requests.

**Done, 9 October:** most of section 8 landed with the packages it documents;
the remainder, *Ask the fleet why* on the queued-job page, two FAQ entries,
and every subcommand on the CLI page held there by a test, is in its own pull
request. Gate row 16 is met by the strict site build and the two tests that
hold the CLI page to the binary.

Depends on ZF-230 to ZF-233. Size S.

### ZF-235: an assistant in the UI, on the operator's own model

**Classification: new; XL; proposed 8 October, not yet authorised to start.**
A chat panel backed by a model the operator configures, reading the same
payloads the UI reads, confirming every mutation, with deterministic
Kennel fixes before any model-drafted patch and autonomy over those first.
[agent-readiness.md](roadmap/agent-readiness.md) section 9 is the design,
in ten slices (7a to 7j); the step-up authentication slice (7h) is platform
work every governed setting needs, and the subscription sign-in gate (9.10)
stays closed until the owner's written confirmation exists.

Depends on ZF-230 to ZF-233, ZF-229e. Size XL.

### ZF-236: the figures behind size advice

**Classification: extension; S; proposed 8 October.** Label advice says
*too small* or *too large* without showing the runs it rests on.
[agent-readiness.md](roadmap/agent-readiness.md) section 5 is the design.

**Do:** `observed` percentiles, sample size, window, `recommended_class`,
`fits` and a `not_enough_data` state on the advice payload; `--window` and
`--repo` on `jobs advice`; the figures on the `SizeAdvice` card; the
constants named in `auto-pools.md`.

Depends on ZF-226. Size S.

## 9. Kept for the day somebody asks

Nothing here is authorised by planning alone. Each starts when a fleet
asks for it or the evidence arrives, and each keeps its ID and its
acceptance from the version that wrote it.

* **ZF-206, Windows**: the remaining evidence is a fleet's. When one joins
  a Windows host, the support-matrix row moves and the first job's row is
  recorded; Windows on arm64 has digests and no build, and stays that way
  until asked.
* **ZF-214c**, a second provider: chosen from demonstrated demand and
  repeatable test access, proven against `RunContractTests` and the same
  reconciler, bootstrap and pages with no controller-specific branch. The
  optional GARM experiment (two days at most) stays optional and non-gating.
* **ZF-216**: the validation halves are withdrawn (section 7). What
  survives as product work, demand-gated: GPU admission that prevents
  oversubscription when a fleet's GPU host is shared by pools, and a
  disposable-VM backend as its own design with a clear isolation model.
  Neither before a fleet with the hardware. The isolation work is staged so
  each step is taken only if the one before it falls short, and the design
  sketch is candidate 4 in
  [candidates-from-hosted-services.md](roadmap/candidates-from-hosted-services.md):
  1. *A sandboxed OCI runtime per pool* (size S): a pool `runtime` field
     (`runsc` for gVisor, `kata` for Kata Containers) passed through as the
     Engine API's `HostConfig.Runtime`. Same runner images, same caches and
     the same backend; a guest kernel per job without a new code path. The
     validator refuses a runtime the host's daemon does not list.
  2. *Decision 28's spike*, below.
  3. *A native `microvm` backend on Firecracker* (size L), only if the two
     above do not meet a fleet's need. It buys a guest kernel per job and
     `dind` without a privileged sidecar, and costs `/dev/kvm` on the host
     (rare on cloud VMs without nested virtualisation), a tap device per VM
     that the rootless default cannot create (a warning finding and a
     `security.md` row, never silent), a kernel and root filesystem built
     per image variant and architecture, no GPU passthrough, and caches
     that must move from bind mounts to virtio-fs or block devices. Its one
     advantage over Kata is snapshot-and-restore for pre-warmed runners,
     which matters only at scale.

  Docker stays the default in every case: most fleets run their own code on
  cloud VMs without KVM, and VM isolation is a pool's choice, not the
  product's.
* **ZF-217**, the scale-set assessment: a time-boxed decision record with a
  disposable prototype, never a second scheduler. Not before a measured
  reason.
* **ZF-404b**: deferred indefinitely under decision 25. Its controller-driven
  agent upgrades are narrowed by
  [decision 0011](roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md):
  updating the agent's own binary, on a host that opted in, is ZF-232.
* **ZF-218**: complete, the repin having landed with ZF-005; a second
  provider or an official marketplace submission is not planned.
* **Decision 28's spike**: a container per job on Proxmox from the published
  runner image, one day, only after a fleet with Proxmox exists.

## 10. Ordered delivery plan

Consult [progress.md](roadmap/progress.md) before starting; an old
description of a missing feature is not evidence it remains missing, and
every package in section 8 was re-read on 19 September against the code.

| Order | Work | Exit criterion |
| --- | --- | --- |
| 0 | ZF-005 documentation corrections and the marketplace repin, done, 19 September | The three pages agree with the record; `release.env` pins the current full release |
| 1 | ZF-207 two audiences: the role and its migration, the actions, the settings scope, the problems split, tokens, copy and audit | The per-role Playwright assertions hold; a single-team instance is unchanged; an upgraded instance has one platform identity |
| 1b | ZF-224 auto-recovery on a lost runner, off by default | A job the fleet broke is re-run once on its own; a test that failed never is; the bound holds across a restart |
| 2 | ZF-208 edge limits and the outbound address guard | Every limit has a test that reaches it; the hostile-agent drill holds |
| 3 | ZF-210a environment bootstrap, readiness, the controller-only template | A compose file brings up a controller, a platform identity and a joined agent with no human step |
| 4 | ZF-209 the usage ledger | The 90-day equality test; one session per runner across a restart |
| 5 | ZF-221 durable runtime incidents and the image-pull signal | A runtime failure is on the card and in the drawer within one heartbeat and survives a restart |
| 6 | ZF-401 the relay problem code; ZF-404 what the agent owns, written down | The listener's failure is a problem, not a log line; the promise is on the pages the join links |
| 7 | ZF-222 the status view, if decision 29 is adopted | The fixture-name test; a default install still serves nothing new |
| 8 | ZF-213 pools as a file | Export, plan, dry-run and apply for pools, modelled on the settings pair |
| 9 | ZF-210b export with re-seal, and purge | Two installations side by side; an export restores elsewhere under its passphrase |
| 10 | ZF-223 a report per installation | Matches a hand computation; answers past retention from the roll-up |
| 10b | ZF-225 two-step sign-in for local accounts, delivered, 27 September | A password alone no longer signs in an account that has turned it on; a lost phone has an audited way back |
| 11 | ZF-230 the catalog, the guard and the command generator | `catalog.json` validates and CI diffs it; the guard fails on a seeded term |
| 12 | ZF-231 `why` | Every demo failure classed at high confidence; `unknown` says what was missing |
| 13 | ZF-236 the figures behind size advice | The payload shows the runs it rests on; sparse data is a state, not an absence |
| 14 | ZF-229c, the rest: location evidence, the owed checks, `zoomies kennel check` | A finding names its file and job; the offline check and the controller agree on this repository's own workflows |
| 15 | ZF-233 the skills | The pinned reference matches the binary; a smoke test in two agents is recorded |
| 16 | ZF-234 the documentation | `mkdocs build --strict` passes; every new route and command is on its page |
| 17 | ZF-235 the assistant, slice by slice, after an owner decision to start | Each slice's acceptance in the design's section 9 |
| - | ZF-215, ZF-212, ZF-403's rotation, and section 9 | When a fleet asks |

**Keep one stream moving.** The primary target's packages are a chain
(ZF-207 first, because ZF-208's ceilings, ZF-209's retention keys, ZF-210a's
role, ZF-222's audience and ZF-401's problem all hang off it) and the
self-hosted product keeps improving through them, because every one of them
is a self-hosted feature. Critical correctness and security defects
interrupt the chain. Real-use evidence from any fleet is recorded when it
arrives and never waited for.

## 11. Owner inputs

| Input | Needed for | Current treatment |
| --- | --- | --- |
| `V1.1.0`, a full release with no assets because the tag's capital letter defeated the workflow's `v*` guard | Whoever reads the releases page | Delete the release and its tag, or leave it. The guard now refuses a tag that does not begin with a lower-case `v` out loud rather than building nothing (ZF-005, 19 September); the release itself is the owner's |
| Whether immutable releases are enabled in the repository settings | Decision 9's last residue | Check once; not roadmap work |
| Decisions 29, 31 and 32 | ZF-222; the settings scope in ZF-207; the re-seal in ZF-210b | Written as if the recommendations were accepted; a different answer changes the package it names |
| A fleet with a Windows host, a Proxmox cluster or a GPU | The section 9 rows | Recorded when it happens; never waited for |
| The primary target's first fleet | The usability feedback ZF-303's second operator was for | Their onboarding is the session; record what they asked, not what was assumed |

## 12. Instruction for the next implementation session

Read `CLAUDE.md`, `docs/architecture.md`, `docs/upgrading.md`, this
roadmap and `roadmap/progress.md`. Start at the first unfinished slice in
section 10. Inspect current code before interpreting anything in this
document, and preserve completed work and package IDs.

Deliver one reviewable behaviour per pull request, with acceptance evidence,
the API, client and documentation updates rule 6 names, and a progress-row
update in the same pull request. Preserve the single binary, SQLite and the
pure scheduler. No live infrastructure is changed without task authority.

Every package in section 8 ships as a self-hosted feature with a self-hosted
reason, and nothing you write on the site, in the README or in the UI
describes an instance operated on a team's behalf, a plan or a tier (rule
15). Keep implemented, done and withdrawn distinct, and report the exact
remaining dependency. Do not invent live runs, elapsed observation, benchmark
results or user feedback; do not wait for them either.

## 13. Change record

* **8 October 2026: Version 3.6:** the agent-readiness programme read in.
  ZF-229 (Kennel Club, whose record predates this entry) added to section 8
  as the roadmap's pointer to its own record, with what #714 delivered and
  what remains; ZF-230, ZF-231 and ZF-233 to ZF-236 added from
  [roadmap/agent-readiness.md](roadmap/agent-readiness.md), which reviewed
  the owner's seven-phase plan against the code and re-cut it so that each
  phase extends what exists. Rows 11 to 17 added to section 10 (size advice is ZF-236, because main took ZF-232 the same day); ZF-235 is
  listed but not authorised to start until the owner says so. Header
  reconciled against `88c41f6`.

* **8 October 2026: Version 3.5:** ZF-232 added to section 8, as proposed:
  updating Zoomies from the web UI, off by default. The owner chose a
  root-owned helper on each host over a service that replaces its own binary
  and over a documentation-only recipe.
  [Decision 0011](roadmap/decisions/0011-an-operator-may-let-zoomies-update-itself.md)
  records it, reads rule 12 as binding recovery logic and not an operator's own
  policy, and narrows decision 25's deferral of controller-driven agent
  upgrades to the one thing it allows: updating the agent's own binary, on a
  host that opted in. Rule 12, decision 25 and section 9 point to it. The header
  was still at 3.3, behind the 3.4 entry below, and now matches. Design only; no
  code.

* **4 October 2026: Version 3.4:** the record caught up with the week.
  ZF-226 (placement from job history), ZF-227 (AI Context) and ZF-228
  (connecting an assistant, and owning your tokens) added to section 8; the
  first two are delivered and in flight respectively, as `roadmap/progress.md`
  says. No earlier package changed.

* **27 September 2026: Version 3.3:** ZF-225 proposed and delivered in
  one pull request: optional two-step sign-in (TOTP) for local password
  accounts, recovery codes, an audited administrator's reset, and
  `security.require_two_step`, off by default. Added to section 8 and to
  section 10 as row 10b.

* **27 September 2026: Version 3.2:** ZF-216's disposable-VM backend in
  section 9 staged: a sandboxed OCI runtime per pool (gVisor or Kata) first,
  decision 28's spike second, and a native Firecracker `microvm` backend only
  if both fall short, with what each costs named. Still demand-gated; nothing
  in this entry implements runtime behaviour.

* **19 September 2026, Version 3.1:** ZF-005 delivered, the three pages
  corrected, the marketplace package repinned to `v1.2.0`, the release
  workflow's tag guard made loud, and moved from section 8 to section 6,
  with section 10's row 0 marked done and section 11's `V1.1.0` row
  reduced to the release itself. Section 1 corrected: the Scorecard job runs
  on GitHub's Ubuntu runners again, because its publishing service refuses
  every other runner label and every run from the fleet had failed since 18
  September. Nothing in this entry implements runtime behaviour.

* **19 September 2026: Version 3.0:** the roadmap re-read against `main`
  at `9a80b31` and re-pointed. **The primary target** is now an instance
  operated on a team's behalf where the team connects its own runner hosts
  (section 2, decision 30), which is the shape decision 27's packages were
  written for; ZF-207 to ZF-210 move to the front and each is rewritten from
  where the code is now, ZF-207 grew, because migration `0031` made the
  process's own keys writable by any admin and the role `CHECK` in
  `0001_init.sql` needs a rebuild; ZF-208 gained an outbound address guard;
  ZF-210a is half done, because the answer file landed with the marketplace
  work; ZF-210b gained a passphrase re-seal (decision 32). **Withdrawn**
  (section 7, rule 17): Gate F, ZF-303, ZF-301c, ZF-219, ZF-211's
  measurement half, ZF-302's two reference-host drills, ZF-402, and every
  live-qualification clause, because the product proves itself by carrying
  this repository's CI on a Zoomies fleet since 18 September and three full
  releases in a week. **Read back into the record**: fifty-six pull requests
  since version 2.40, backups as a subsystem with offsite destinations and
  a staged restore (ZF-203, closed as delivered and larger than written),
  runner sizing, per-pool timings and elastic CPU (ZF-103, ZF-220), the
  start-up resilience work (ZF-221, mostly done), fault categories and rerun
  (ZF-202), prompt cancellation (ZF-105), the Tailcat recovery fixes
  (ZF-401), and the settings export and import that turned out to be half of
  ZF-213. **Added**: ZF-005, three documentation corrections and the
  marketplace repin; ZF-223, the report per installation that replaces
  ZF-303's helper; delivery rules 15 to 17; decisions 30 to 32; the
  `withdrawn` status. **Found for the owner**: `V1.1.0` is a full release
  with no assets. Nothing in this entry implements runtime behaviour, and
  nothing under `docs/` changes with it.

* **15 September 2026: Version 2.40:** add ZF-222, a name-free fleet status
  projection for the audience that has no account and no way to tell a fleet at
  capacity from a pool that matches nothing, and decision 29, which is whether
  one administrative trust domain is also one readership. Sequenced behind
  ZF-207's problem split. Nothing is enabled by default and this entry
  implements no runtime behaviour.

* **13 September 2026: Version 2.38:** add the owner's bounded pre-release
  allocation request as ZF-220. Prioritise measured host headroom, existing
  reservations and automatic pressure admission; retain separate scope for
  runtime incident diagnosis and remediation.

* **13 September 2026: Version 2.37:** make repository-root `ROADMAP.md` the
  sole active source of truth for scope, ordering and authorisation of roadmap
  work. `IMPLEMENTATION_PLAN.md` and `roadmap/source/` are historical records;
  `roadmap/progress.md` records status and evidence only.

* **13 September 2026: Version 2.36:** record the `v1.0.0` release in
  progress as the stable baseline for a VPS one-click-install track once
  published. Add ZF-218 for the
  tangible, provider-neutral deployment, HTTPS/bootstrap and partner-hand-off
  work, followed by ZF-219 for testing and one friendly-provider pilot
  readiness. Testing intentionally follows completed implementation; no
  official marketplace submission is implied.

* **13 September 2026: Version 2.35:** confirm Proxmox as the first provider.
  Split ZF-214 into shared contract/recovery, complete Proxmox lifecycle and
  second-provider validation. Bring contract fixtures forward after bootstrap
  design, retain mutation/qualification gates, and time-box optional GARM reuse.

* **13 September 2026: Version 2.34:** reconcile the delivery order with
  completed packages and a current competitor review. Add ZF-211 to ZF-217,
  prioritise ZF-207/208 and split ZF-210 by its actual dependencies. Keep
  qualification alongside product work. Scope the roadmap to self-hosted
  development and retain existing package acceptance criteria.

* **12 September 2026: Version 2.33:** record six narrow correctness fixes
  as ZF-004 and add ZF-207 to ZF-210 for repeatable instance administration.

* **9 September 2026: Version 2.32:** the dead-socket drill, in the half a
  tier with no daemon can do honestly: a second agent joins with its Docker
  socket missing, and the fleet has to say so. It does, and well; the host
  names the backend it cannot use and the socket it looked for, and the pool
  with work waiting raises an error quoting that sentence with a fix. The
  reason to write it anyway was the failure mode where saying nothing is
  worst: the jobs still queue and the pool still looks configured, so a fleet
  that quietly places nothing is indistinguishable from a fleet with nothing to
  do. What is left of ZF-302 is the two halves that need a machine this tier
  deliberately has not got (starting a daemon back up, and filling a
  filesystem) and both belong on the reference host the owner actions ask for.

* **9 September 2026: Version 2.31:** the restore-and-rollback drill Gate F's
  sixth bullet asks for, in the two tiers its halves belong to, and both were
  weaker than they looked. The restore drill stopped at "the fence lifts and the
  controller answers", which proves a fleet *starts*; it now runs a job on the
  restored database, with the agent never told any of it happened. And the
  upgrade check now rolls back; the old binary started again on the copy the
  new build took before migrating. **Written the obvious way, that check passed
  without restoring anything at all.** The published release predates the ledger
  check that refuses a database written by a newer build, so it comes up on the
  migrated one and looks perfectly healthy: the silent start
  `docs/upgrading.md` warns about, and what somebody rolling back under pressure
  would read as success. The check asserts the schema went back now, and the
  documentation says plainly that rolling back *to* `0.2-beta` is the one case
  where nothing will stop you. Twice in two days a check has passed for a reason
  that was not the one it claimed; a directory that outlived its process, and
  now a binary that started on the wrong database. Both were caught by asking
  what would have to be true for the assertion to be worth making.

* **9 September 2026: Version 2.30:** two more fault drills, and the second
  one found a defect in the stand-down ZF-105 built. A rate limit is the fault
  a real fleet is most likely to meet and the least visible from inside it
  (nothing has crashed, the fleet simply stops) so the product answers it with
  a per-installation hold and a gauge saying so. **Neither engaged.** GitHub
  refuses the *installation token refresh* first, ghinstallation mints that
  inside its transport, and the refusal reached `classify` unclassified: the
  poller kept calling every two seconds while the quota was gone, and
  `zoomies_github_paused` (the signal `docs/metrics.md` tells an operator to
  alert on) stayed at zero. It is fixed, and deliberately only when the
  refusal's own headers say the quota is actually gone: a 403 for a permission
  the App has not been granted comes back with quota to spare, and standing an
  installation down for fifteen minutes over that is a wait that fixes nothing.
  The other drill is the failing JIT endpoint, where the whole claim is that
  the fleet does not make a bad situation worse: the attempt is recorded rather
  than deleted, nothing is left on the host or on GitHub, and it recovers
  without anybody restarting anything. This is what a drill tier is for. Both
  faults have been covered by in-process tests for months; neither test could
  see that the stand-down was never reached, because in process the call that
  fails is the one the test makes.

* **9 September 2026: Version 2.29:** the first two fault drills, and what
  they found. Both inject their fault while a job is actually running: killing
  the controller leaves the work running and the fleet finishes it after the
  restart, and killing the agent leaves the work running and the agent comes
  back as the same host rather than as a second one. **The finding is the
  agent's:** the exit code is written by the parent that reaped the process, so
  an agent restarted mid-job finds its adopted runner gone with nothing recorded
  and calls that a failure, racing the removal the finished job set off, so the
  same successful build is recorded `removed` on one run and `failed` on the
  next. That is a decision rather than a bug fix, and it is now a row in the
  drill record with the fleet's own accounting named as what is at stake.
  Two smaller things worth keeping: a drill that checked a directory was
  satisfied by a runner that had died, because the marker file outlives the
  process, and a drill that checked once was satisfied by a process that was
  already doomed; a death that follows its parent's arrives a beat later.

* **9 September 2026: Version 2.28:** ZF-302 starts with the piece the plan
  says has to come first: keeping the drill rows. They were written to
  `roadmap/validation/drills.md` and reached one job summary and nowhere else,
  so the comparison the record exists for (recovered cleanly forty times and
  then did not) could not be made at all. The drill job now commits the rows it
  wrote, from the default branch only: a pull request's rows describe a commit
  that may never exist. Each row gained the run that wrote it, because a row
  worth keeping a month is one that can be taken back to the logs behind it.
  Nothing here is a fault drill yet; those are next, and they are worth writing
  only now that their rows will survive the run that produced them.

* **9 September 2026: Version 2.27:** ZF-105's last open finding is closed,
  and with it the package. `TaskBatch.Backoff` had been on the wire since the
  protocol was written, published in the OpenAPI document and waited on by the
  agent, and no controller path ever set it; a load-shedding channel that
  existed everywhere except where the load is. The controller now counts the
  polls it is holding and asks the ones that found nothing to come back later,
  by an amount that rises with the excess. What the shape says: the pressure is
  the fleet's *size*, not its workload, because an idle host costs a held
  connection for the whole of a long poll; a batch with work in it is never
  delayed, because the long poll exists so a task reaches its host in the
  instant it is queued; and the wait is jittered, because a controller sheds
  every agent it is holding in the same instant, and an unjittered backoff
  would bring the fleet back together and re-form the queue it was spreading.

* **9 September 2026: Version 2.26:** ZF-201 is done. Its last pull request
  gives the suite's GitHub fake a port, so the connect and verify pages are
  tested where they live rather than at every layer beneath them, and the suite
  gains its only fail-then-recover journey outside a wrong password. **That is
  what exposed the gap**: replacing an installation's private key had a route
  and no way in, so recovering from the commonest credential mistake there is
  (pasting the wrong `.pem`, a file GitHub hands over exactly once) meant
  disconnecting the installation, which takes its pools and their runner rows
  with it. A key is replaceable; a fleet should not have to be. Two of the
  harness defects were self-inflicted and worth recording as such: a fixture
  that published the fake's address *after* the controller answered `/healthz`,
  so a spec could read a port from the previous run; and a defensive
  `os.Stdout.Sync()` in the fake, which fails on a pipe and so killed the
  program the instant it had said where it was. A guard that turns a working
  program into a dead one is worse than no guard.

* **9 September 2026: Version 2.25:** ZF-201's first pull request is done, and
  its three defects were all the same kind of thing: the product saying
  something that was not true on the journey a new operator takes. The bootstrap
  page claimed a length it cannot know, four steps, while the Overview's
  checklist draws five on exactly the install that reaches it. The installer's
  remedy for a spent join token had never fired, because a refused token on an
  anonymous route is a 422 and the transport mapped only 401. **And the third
  was larger than the package recorded**: `MissingRequirements` bundled the
  `workflow_job` subscription in with the permissions, so an App with every
  permission it needs but no subscription probed as a *broken credential* and
  was recorded unhealthy, while the fleet it describes works perfectly well on
  the fallback poller, which is a supported way to run. Every other entry in
  that list is something the fleet cannot work without; that one is something it
  works without. Writing the dialog's own test then found a fourth: the demo
  client's shortcut lived at one call site, so pressing Verify on the seeded
  installation (the one a new fleet has) answered "the stored private key is
  not a PEM-encoded RSA key". It is in the client cache now, where every path
  goes through it.

* **9 September 2026: Version 2.24:** ZF-103's third pull request is done, and
  with it Phase 1: every package in it is `done`. The reserve had a column, a
  store statement and no way in (no route, no caller outside tests) so the
  documented floors were the only reserve any host has ever had. `PATCH /hosts`
  takes it now, **refused rather than clamped** when it would leave nothing to
  place on or is held back from a figure the host has never reported, and
  written by its own statement so a heartbeat can never touch it. The finding
  worth keeping is about provenance: what the Hosts page shows a host has
  promised away is *the scheduler's own sum*, recorded from the snapshot each
  pass decided on rather than recomputed for the page; a figure that
  disagreed with the one placement used would be worse than none, because it
  would be believed, and it reads as unknown until a pass has run rather than
  as zero. `pool.resources_unenforced` says the quiet part: a `process` pool's
  limits bind nothing, the reservation still holds the room, and the room is
  bookkeeping. `host.resources_unknown` is a note rather than a warning, because
  an agent too old to measure its machine is placed by slots exactly as every
  host was before it could.

* **8 September 2026 (Version 2.23:** ZF-205 is done, and with it every
  unblocked package in Phase 2) ZF-201 waits on the owner's disposable
  organisation and ZF-206 on Gate F. **Two corrections to the package as
  written.** The poller's rate-limit pause is *not* fleet-wide and never was:
  it is keyed by installation, so the gauge carries the installation, and a
  fleet with two of them, one held, is a fleet half working that the described
  flag could not have said. And the time zone and freshness the package asked
  to be rendered from data already were; the window was the only prose, and it
  was worse than prose; a fetch of `/stats` defaulted to a day and a `stats`
  frame to an hour, so the Overview's completed counts and wait percentiles
  changed under the operator a second after every page load while the
  specification said an hour throughout. **Three findings.** A registry-wide
  label rule has to read the collectors' descriptors rather than a scrape: a
  vector with no observations reports nothing at all, so the version that
  gathered and inspected passed happily with a `repository` label added, and
  would have failed on the day something first incremented it. The load
  measurement earned its keep on its first run, finding that the audit log's
  action filter scanned the one table Zoomies deliberately never prunes. And
  the bulk prune's announcement, not its deletion, was the reconnect storm:
  past sixty-four rows it now sends the one frame that already means "fetch
  the resources again".

* **8 September 2026: Version 2.22:** ZF-204 is done. Its last pull request
  is the upgrade check nothing else could stand in for: every other tier builds
  one binary and asks what it does, and upgrade day asks whether an
  installation is still an installation once the binary under it is replaced.
  `make test-upgrade` installs the last published release the way an operator
  does, swaps this build in over its state, and checks that the version moved,
  the operator's `zoomies.yaml` did not, the host row is the same row, and the
  pre-migration copy exists. **Writing it found that `curl … install.sh | sh`
  did not work at all**: GitHub's /releases/latest only knows about full
  releases, every Zoomies release so far is a prerelease, and the redirect the
  installer reads therefore landed on the release index with no tag in it. The
  documented one-line install has been broken for as long as there have been
  only prereleases, and nothing could have caught it; the installer's only CI
  coverage was a syntax check. **This also discharges an owner action**: the
  package asked for a fresh pre-release tag to upgrade from, and the last
  published release is a better fixture than a tag cut for the test, because it
  is what people actually have. **And a drill was written and thrown away**,
  which is the delivery rule working as intended: an agent-upgrade drill on the
  runtime tier passed with adoption removed, because the reaping it claimed to
  prevent sits behind a two-minute constant a separate process cannot move. The
  wiring is pinned in-process instead, where the clock can be, and the gap it
  exposed was real, since every existing test called `adoptExisting` directly
  and none of them noticed the call disappearing from the startup path.

* **8 September 2026: Version 2.21:** the supply-chain work is done, and it
  found that three of its seven parts had already shipped: the plan's
  reconciliation predates the action pinning, the Dependabot configuration and
  the govulncheck workflow. The finding worth keeping from the rest is that
  **the pinning rule was a claim rather than a check**, CLAUDE.md has said CI
  enforces it for some time and nothing did. It is tested now, with the
  permissions rule and the release workflow's own guards. The image builds are
  written twice, once per runner vendor, rather than once with a swapped
  action: this workflow runs only on a tag, so CI cannot tell anybody it has
  been broken until the moment somebody needs a release, and the path that
  works today therefore stays byte for byte what it was.

* **8 September 2026 (Version 2.20:** backup-before-migrate is done, and the
  interaction it uncovered is worth recording: `zoomies restore` opens the
  database it restored, so restoring a backup from an *older* release migrates
  it) and now keeps a copy of it as it was first. Restoring an old backup no
  longer consumes it. Both pages say so, which they did not before, because
  until this the migration on restore was invisible.

* **8 September 2026: Version 2.19:** ZF-204's schema-safety tests are done,
  and writing them corrected the plan's picture twice. There are two releases
  now rather than one, so the upgrade fixture covers both points a real
  database sits at. And the fixture has to be a database built by applying the
  old migrations, not a current one with its ledger trimmed: trimming leaves
  the columns the later migrations add, so re-applying them fails on a
  duplicate and the thing being tested is an upgrade from a database that
  never existed. The first attempt did exactly that and the test caught it.

* **8 September 2026: Version 2.18:** ZF-204's second pull request is done.
  The distinction it settled is what "skew" means: a *release* difference, not
  a commit difference. Two builds of one tag are the same release (worth
  knowing in a bug report, and not skew) and treating a rebuild as skew was
  what made the agent's log and the Hosts page disagree. The comparison behind
  both refuses to order what it cannot parse, because a wrong order would send
  an operator to upgrade the wrong side, and a host ahead of its controller is
  called out separately for the same reason: there the fix is the other
  machine.

* **8 September 2026: Version 2.17:** ZF-204's first pull request is done, and
  it corrected two things in this plan. "An agent may lag by one minor release"
  is not a rule any code here can enforce, because nothing compares release
  numbers, what is enforced is that the protocol matches, and lag beyond that
  is a fact to show rather than a rule to apply. And the verifier's cordon
  finding has a sharper answer than "gate the poll loop": an agent refuses a
  *create* while cordoned or incompatible and serves every other task kind,
  because a cordoned host still has runners to drain and an agent that stopped
  polling would strand them. Pull request 3's first item is also already done:
  the ledger refusal landed in ZF-203, which needed it for a safe restore, and
  the emergency override that item asks for should not be built; the refusal
  names the release to run, and an override is a way to corrupt a database
  under pressure.

* **8 September 2026: Version 2.16:** the fence is done, and ZF-203's code is
  complete; only the owner-run GitHub half of its acceptance remains. Two
  distinctions the work sharpened. Readiness fails while fenced and liveness
  does not, because the container image's health check is `/healthz`: a fenced
  controller must be taken out of rotation and must **not** be restarted, which
  would achieve nothing and lose the operator's session. And the fence stops
  the fleet *acting* without stopping it *deciding*; the plan is still
  computed and published, so an operator can tell "nothing to do" from "not
  allowed to", which is the difference a fenced fleet otherwise cannot show.

* **8 September 2026: Version 2.15:** `zoomies restore` is done, and it fixed
  the fence's home. `recovery.fenced` is a row in the restored database's own
  `settings` table (the one the verifier noted shares a name with the settings
  API and none of its data) rather than a line in `zoomies.yaml`. The fence
  belongs to the data: a restored database is fenced wherever it is put, and a
  copy carried to a second machine arrives fenced too, which is the case the
  fence exists for. An unparseable value reads as fenced, because the fence is
  the safe side of its own question and a half-finished restore is exactly what
  writes one.

* **8 September 2026: Version 2.14:** `zoomies backup` is done. Two shapes
  settled while writing it. A backup is one directory rather than a database
  file beside a manifest file: retention has something whole to delete, and
  `zoomies restore` will take one argument. And the summary states the key's
  presence or absence on every run, not only on the dangerous one; an
  operator who reads "backed up" and stops is exactly the person the sentence
  is for.

* **8 September 2026: Version 2.13:** ZF-203's first pull request is done, and
  it sharpened one distinction the package's prose had left implicit: a
  *missing* encryption key over a sealed database is a startup refusal, and a
  *wrong* one cannot be, because a key is proven only by opening something.
  So the two guards live in different places (one in the command that would
  have generated a key, one as a problem code the drawer raises once the
  controller is running) and the restore documentation now says which failure
  looks like which.

* **8 September 2026: Version 2.12:** the support bundle is done and **ZF-202
  is finished**. Two decisions in it are worth recording. The action is admin
  rather than viewer, because the weakest role that covers a bundle is the
  strongest role inside it; it carries the settings section, and that has
  always been admin. And the byte cap sheds sections rather than refusing the
  request: explanations first because they are recomputable from the jobs
  beside them, then the scaling history, then jobs and runners; the fleet's own
  shape is what a bundle is for and never goes. A short bundle answers some
  questions and a 500 answers none.

* **8 September 2026: Version 2.11:** the explanation is finished: the drawer
  and the CLI render it rather than reasoning for themselves, which is the
  defect this package named and the reason the endpoint exists. What the work
  clarified is where the boundary falls; a pool's live counts are facts and
  stay in the panel; only the reason moved.

* **8 September 2026: Version 2.10:** `GET /jobs/{id}/explanation` is done.
  The shape it settled on is worth recording because the rest of the package
  will render it: one summary that is always set, a detail in the scheduler's
  own words where it has any, a fix that is absent when there is nothing to do,
  and `waiting` separated from `blocked`; a fleet that is merely busy clears
  itself and a pool nothing can place never will.

* **8 September 2026: Version 2.9:** ZF-202's small half and its test fixture
  are done. The fixture is worth a line of its own because of what it changes
  about how this package is verified: the suite's shared fleet is deliberately
  healthy, so every page that explains a fault was unreachable from a test, and
  three pull requests in a row shipped without a pin for that reason. It found
  a defect on its first run; a held job is unmatched by construction, and the
  Jobs page hid every one of them by default.

* **8 September 2026: Version 2.8:** the poller can be seen. Writing it found
  a stale instruction in this document: ZF-202 asked for `poller.paused` to be
  attributed to the whole poller "because the pause is fleet-wide until ZF-101
  changes it", and ZF-101 changed it; the hold has been per installation since
  its second pull request. The entry names the installation, and the package's
  text is corrected rather than left to instruct the next session wrongly.

* **8 September 2026: Version 2.7:** the two of ZF-202's small fixes that
  need no API shape are done, and one of them was a disclosure rather than a
  gap: `zoomies config print` has printed `capacity_demand.signing_secret` in
  full for as long as that feature has existed, because the blanking is a
  hand-written list. The replacement guard does not read the list. Nothing in
  the plan changed; the package's entry now says which half of its small fixes
  remains and why (an OpenAPI change and both generated clients).

* **8 September 2026: Version 2.6:** ZF-103b is done, so Phase 1 is complete
  as code: every placement decision now costs a reservation, and the figures
  ZF-103a taught agents to report are read by something. Two choices the
  package did not name are worth keeping in front of the third pull request,
  because they decide what its numbers mean: the fallback share (a field a pool
  leaves unset costs one slot's worth of the host) is what makes the upgrade
  admit exactly what it admitted before, and free disk is charged only for the
  runners a pass adds, because the measurement already contains what the
  runners already there have written. The floors under a host's reserve are
  documented in section 6 and in `docs/hosts-and-pools.md`, and until the third
  pull request gives the reserve a route they are the only thing holding
  anything back.

* **8 September 2026: Version 2.5:** ZF-206's first pull request is done, and
  it is the only part of that package this document authorises before decision
  26 and Gate F. Nothing about the plan changed; what changed is that three
  places no longer imply Windows support. The package entry and section 10 say
  so, and the rest of ZF-206 is untouched.

* **8 September 2026: Version 2.4:** **ZF-206, Windows runners**, added to
  Phase 2 with decision 26 choosing its shape, and sequenced after Gate F in
  section 10. The vocabulary for it already shipped (`naming.OSWindows` is a
  legal operating system and `docs/hosts-and-pools.md` used `os=windows` as a
  pool example) while nothing behind it did, which is the gap the package's
  first pull request closes on its own.

* **8 September 2026: Version 2.3:** Assignment A is finished, and this
  document is reconciled against the code rather than against itself. Section
  10 says where the assignment started and ended and which of its steps is
  still open; section 11 gains a **State** column, because a table of owner
  actions with no state is a table nobody updates; section 12 becomes one
  instruction per assignment, with Assignment A's marked spent so that a fresh
  session cannot re-issue a finished assignment, and Assignment B's written but
  explicitly not yet sent. Decision 15 and the Phase 1 preamble had ZF-103's
  two halves the wrong way round against how they shipped: 103a is the
  reporting half, 103b the admission half, and both precede Gate F. Delivery
  rule 14 writes down the discipline the whole assignment actually ran on --
  a test is kept only once it has been seen to fail, and rule 7 names the
  two tests a migration must be added to. The ZF-002, ZF-101, ZF-103, ZF-205
  and ZF-301b entries are corrected where they described work that has since
  landed differently.

* **7 September 2026: Version 2.2:** the migration prefixes this document
  reserved are stale. `0010` and `0011` shipped from other work between the
  plan being written and ZF-101 starting, so ZF-101 took `0012`, and ZF-103's
  host resource reporting took `0017` when it landed later the same day. The
  schema rule itself is
  unchanged: the next file takes the next unused prefix, whatever that is by
  then, which is why the rule and not a number is what this document now
  names.

* **6 September 2026: Version 2.1:** N02 is fixed and removed as a programme
  gate. Added the deliberately deferred **ZF-404b** host-stewardship and
  bounded-housekeeping slice; it is not authorised for implementation before
  Gate F.
