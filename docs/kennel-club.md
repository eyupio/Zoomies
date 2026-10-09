---
icon: material/dog-side
title: Kennel Club
description: >-
  Kennel Club checks the repositories your fleet serves for what could hurt it
  or stop its CI, what it reads from GitHub, and how to waive or read a finding.
---

# Kennel Club

Kennel Club looks at the repositories your fleet serves and says which of them
could hurt the fleet or stop its CI. It answers one question about each
repository, and a check that cannot answer it does not belong here:

> Does this affect running CI, or the fleet that runs it?

So it is not a linter, and it has no opinion about your code. It says that a
public repository's jobs run on a pool a stranger's pull request could damage,
and that jobs have been waiting ten minutes for a label no pool serves.

Optional repository guidance checks bend that rule, and say so: the **setup** checks report what
a repository lacks that makes it harder to maintain, such as a README or a
security policy. They are off until you turn them on, and informational when they
are on. The **guidance** checks inspect agent instruction files when separately
enabled. See [the optional checks](#the-optional-checks).

It is **off by default**, and off means off: nothing is read from GitHub,
nothing is stored, and [AI Context](ai-context.md) carries on exactly as it was.
Turn it on with the **Check repository standards** setting
(`kennel.enabled`, see [Configuration](configuration.md)), and it appears in the
UI under **Kennel Club**. This is its Overview for the demo fleet:

![The Kennel Club Overview of the demo fleet: three repositories, two with no open findings and one needing attention, two errors open, acme/site listed first under Needs attention, and the by-check table beneath](screenshots/kennel-dark.webp#only-dark){ .zoomies-shot }
![The Kennel Club Overview of the demo fleet: three repositories, two with no open findings and one needing attention, two errors open, acme/site listed first under Needs attention, and the by-check table beneath](screenshots/kennel-light.webp#only-light){ .zoomies-shot }

## What it checks

There are thirty-four checks, in seven areas. Seven of them, in **exposure** and
**capacity**, run whenever Kennel Club is on; the other twenty-seven are opt-in,
and [the switches that turn them on](#the-optional-checks) are described after
them. Each has a stable code, so a waiver, a metric or a `disabled_checks`
entry keeps meaning the same thing from one release to the next, and the same
codes are published with every problem code in
[`catalog.json`](https://zoomies.sh/catalog.json) and served by a running
controller at `GET /api/v1/catalog`, each with what to change and how to see
that it worked.

The list below is generated from the registry the evaluator runs by
`make generate`, so it cannot disagree with what a repository's page shows,
and a test fails the build when a check is added without it being run.

<!-- zoomies:catalogue-begin -->

### `guidance.missing` { #guidance-missing }

Area
:   guidance

Severity
:   info

Detects
:   No recognised agent instruction file was found on the default branch.

Fix
:   Preview agent guidance to propose an AGENTS.md and a Claude import, using commands declared in repository files.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when the guidance is read without this problem.

### `guidance.broken_reference` { #guidance-broken_reference }

Area
:   guidance

Severity
:   warning

Detects
:   An instruction file imports or links to a missing repository path, or its Claude imports form a cycle.

Fix
:   Preview agent guidance for unambiguous import repairs; review other broken links or import cycles and correct them manually. Use AI Context repair for damaged Zoomies-managed sections.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when the guidance is read without this problem.

### `guidance.duplicated` { #guidance-duplicated }

Area
:   guidance

Severity
:   info

Detects
:   A root or nested Claude instruction file repeats the root AGENTS.md in full.

Fix
:   Preview agent guidance to replace the duplicate Claude file with an import of the shared AGENTS.md.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when the guidance is read without this problem.

### `guidance.unreadable` { #guidance-unreadable }

Area
:   guidance

Severity
:   warning

Detects
:   An instruction file is empty, is not regular UTF-8 text or exceeds the file or byte limits.

Fix
:   Keep instruction files as non-empty regular UTF-8 text under 64 KiB each, with at most 32 files inspected; split or shorten them and recheck.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when the guidance is read without this problem.

### `exposure.public_repo_on_fleet` { #exposure-public_repo_on_fleet }

Area
:   exposure

Severity
:   warning

Detects
:   A public repository ran jobs on this fleet, or has jobs waiting for it.

Fix
:   Decide whether this fleet should run a public repository's jobs at all; if it should, keep them on an ephemeral pool with no host socket, no privileged daemon and no root, so a stranger's pull request cannot reach the host.

Verify
:   Press Recheck once the pool is ephemeral and unprivileged, or once the repository no longer sends jobs here; the finding closes when the next read sees neither.

### `exposure.public_repo_weak_pool` { #exposure-public_repo_weak_pool }

Area
:   exposure

Severity
:   error

Detects
:   The pool that ran a public repository's jobs is persistent, mounts the host Docker socket, gives its jobs a privileged Docker daemon, runs as root or uses no container.

Fix
:   Move the public repository's jobs to a pool that is ephemeral, does not mount the host Docker socket, runs no privileged daemon and does not run as root, or make this pool so.

Verify
:   Press Recheck after the pool's settings change; the finding closes when every run of the repository in the window landed on a pool without those settings.

### `exposure.fork_code_ran` { #exposure-fork_code_ran }

Area
:   exposure

Severity
:   error

Detects
:   A run from a fork's pull request executed on this fleet.

Fix
:   Require approval for workflows from fork pull requests in the repository's Actions settings, or stop routing its pull-request jobs to this fleet.

Verify
:   Press Recheck after the setting changes; the finding closes once no run from a fork's pull request has executed here in the window.

### `exposure.target_event_ran` { #exposure-target_event_ran }

Area
:   exposure

Severity
:   warning

Detects
:   A workflow that strangers can trigger (pull_request_target, workflow_run, issue_comment, issues) ran on this fleet in a public repository.

Fix
:   Read the workflows these events trigger and make sure none of them checks out or executes the pull request's own code; if one must, run it on an isolated, ephemeral pool.

Verify
:   Press Recheck once the workflow has been reviewed or moved; the finding closes when no run for those events has executed here in the window.

### `exposure.target_checkout_pr_head` { #exposure-target_checkout_pr_head }

Area
:   exposure

Severity
:   error

Detects
:   A workflow that strangers can trigger (pull_request_target) checks out the pull request's own code, which then runs with the repository's token and secrets.

Fix
:   Do not check out the pull request's head under pull_request_target; read what the event carries, or run the code under pull_request, where it gets the fork's lesser token and no secrets.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when no pull_request_target workflow checks out the pull request's head.

### `exposure.fork_approval_weak` { #exposure-fork_approval_weak }

Area
:   exposure

Severity
:   warning

Detects
:   A public repository this fleet serves asks for approval of fork pull requests only from contributors new to GitHub, so most outside contributors can start a job here.

Fix
:   Require approval for workflows from all outside contributors in the repository's Actions settings, so a maintainer reads a fork's pull request before it runs here.

Verify
:   Press Recheck after the setting changes; the finding closes when the approval policy covers every outside contributor.

### `exposure.private_fork_secrets` { #exposure-private_fork_secrets }

Area
:   exposure

Severity
:   warning

Detects
:   A private repository lets fork pull requests run workflows and sends them secrets or a token that can write.

Fix
:   In the repository's Actions settings, stop sending secrets and write tokens to fork pull request workflows, or stop fork pull requests running workflows.

Verify
:   Press Recheck after the setting changes; the finding closes when fork pull requests are sent neither secrets nor a token that can write.

### `capacity.unserved_label` { #capacity-unserved_label }

Area
:   capacity

Severity
:   warning

Detects
:   Jobs waited more than ten minutes for a label no pool serves.

Fix
:   Add the label to a pool that can run the job, or change the workflow's runs-on to a label a pool serves.

Verify
:   Press Recheck after the pool or the workflow changes; the finding closes once no job has waited ten minutes for an unserved label in the last seven days.

### `capacity.job_hit_default_limit` { #capacity-job_hit_default_limit }

Area
:   capacity

Severity
:   warning

Detects
:   A job ran until GitHub stopped it at its six-hour default limit.

Fix
:   Set timeout-minutes on the job from its own usual duration, so a hung run is stopped in minutes rather than hours.

Verify
:   Press Recheck after the next run of the job; the finding closes once no run in the window was cancelled at the six-hour limit.

### `capacity.matrix_exceeds_pool` { #capacity-matrix_exceeds_pool }

Area
:   capacity

Severity
:   info

Detects
:   A matrix's jobs waited together on a pool with fewer runners than the matrix has jobs, so the matrix ran in waves.

Fix
:   Raise the pool's max_runners to the matrix's width, spread the matrix over more than one pool with runs-on, or cap it with max-parallel so the wait is chosen and not suffered.

Verify
:   Press Recheck after the pool or the matrix changes; the finding closes when no matrix in the window is wider than the pool it ran on.

### `setup.readme` { #setup-readme }

Area
:   setup

Severity
:   info

Detects
:   No repository-local README was found on the default branch.

Fix
:   Add a README with the project purpose, prerequisites and the commands to build, test and run it.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.licence` { #setup-licence }

Area
:   setup

Severity
:   info

Detects
:   No repository-local licence was found on the default branch.

Fix
:   Choose an appropriate licence with the project owner and record it in a root licence file.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.security` { #setup-security }

Area
:   setup

Severity
:   info

Detects
:   No repository-local security policy was found on the default branch.

Fix
:   Add SECURITY.md with a private reporting route and supported versions, or confirm that the account default provides them.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.contributing` { #setup-contributing }

Area
:   setup

Severity
:   info

Detects
:   No repository-local contribution guide was found on the default branch.

Fix
:   Add CONTRIBUTING.md with setup, test and pull request guidance, or confirm that the account default provides it.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.code_of_conduct` { #setup-code_of_conduct }

Area
:   setup

Severity
:   info

Detects
:   No repository-local code of conduct was found on the default branch.

Fix
:   Add CODE_OF_CONDUCT.md with an enforcement contact, or confirm that the account default provides it.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.issue_template` { #setup-issue_template }

Area
:   setup

Severity
:   info

Detects
:   No repository-local issue template was found on the default branch.

Fix
:   Add an issue form or template under .github/ISSUE_TEMPLATE, or confirm that account defaults provide one.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.pull_request_template` { #setup-pull_request_template }

Area
:   setup

Severity
:   info

Detects
:   No repository-local pull request template was found on the default branch.

Fix
:   Add a pull request template with a change summary and validation prompts, or confirm that the account default provides one.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.codeowners` { #setup-codeowners }

Area
:   setup

Severity
:   info

Detects
:   No repository-local CODEOWNERS file was found on the default branch.

Fix
:   Add CODEOWNERS in .github, the repository root or docs and assign owners for the CI workflows; review enforcement separately.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.dependency_updates` { #setup-dependency_updates }

Area
:   setup

Severity
:   info

Detects
:   No repository-local dependency update configuration was found on the default branch.

Fix
:   Configure Dependabot or Renovate for the package ecosystems and GitHub Actions, or confirm that an external service manages updates.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `setup.workflows` { #setup-workflows }

Area
:   setup

Severity
:   info

Detects
:   No repository-local CI workflow was found on the default branch.

Fix
:   Add a workflow under .github/workflows that runs the relevant build and tests, or confirm that CI is provided elsewhere.

Verify
:   Press Recheck once the file is on the default branch; the finding closes when the next tree read finds it.

### `ci.no_timeout` { #ci-no_timeout }

Area
:   ci

Severity
:   warning

Detects
:   Executable jobs have no timeout-minutes.

Fix
:   Set timeout-minutes on each executable job in the default-branch workflows, from its usual duration with a margin for a slow run.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when every executable job declares a timeout.

### `ci.no_concurrency` { #ci-no_concurrency }

Area
:   ci

Severity
:   info

Detects
:   Pull-request-only workflows do not cancel superseded runs at workflow or job level.

Fix
:   Where a superseded pull-request run may be cancelled, add a concurrency group scoped to the workflow and the pull request with cancel-in-progress on.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when every pull-request-only workflow cancels superseded runs.

### `ci.action_not_pinned` { #ci-action_not_pinned }

Area
:   ci

Severity
:   warning

Detects
:   External actions or reusable workflows use mutable refs, or Docker actions use no image digest.

Fix
:   Pin every external action and reusable workflow to a reviewed full commit, and every Docker action to an image digest, and let an updater move the pins.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when no external reference is a tag or a branch.

### `token.permissions_unset` { #token-permissions_unset }

Area
:   token

Severity
:   warning

Detects
:   Jobs inherit token permissions without a declaration at workflow or job level.

Fix
:   Declare the least permissions each workflow or job needs, after reading what its actions and publishing steps use.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when every job has a permissions block of its own or its workflow's.

### `token.default_write` { #token-default_write }

Area
:   token

Severity
:   warning

Detects
:   The repository's default workflow token can write, so every workflow that sets no permissions runs with write access.

Fix
:   Set the default workflow permissions to read-only in the repository's Actions settings, then declare write permissions only on the workflows or jobs that need them.

Verify
:   Press Recheck after the setting changes; the finding closes when the default workflow token is read-only.

### `ci.workflow_unreadable` { #ci-workflow_unreadable }

Area
:   ci

Severity
:   warning

Detects
:   A workflow file could not be read within Kennel Club's limits, so nothing in it was judged.

Fix
:   Bring the file within the limits: under 256 KiB, no YAML anchors, aliases or merge keys, no duplicate keys, one document, valid UTF-8 and a jobs mapping; or split it into smaller workflows.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when the file is read and judged.

### `ci.pins_without_updater` { #ci-pins_without_updater }

Area
:   ci

Severity
:   info

Detects
:   Actions are pinned to commits but no updater configuration moves the pins, so they age until somebody remembers.

Fix
:   Configure Dependabot or Renovate for GitHub Actions so the pinned commits are moved by pull request, or confirm that an external service moves them.

Verify
:   Press Recheck once the configuration is on the default branch; the finding closes when the next tree read finds it.

### `ci.label_unserved` { #ci-label_unserved }

Area
:   ci

Severity
:   info

Detects
:   A job's runs-on names labels no pool here serves, so the job will wait until a pool matches it.

Fix
:   Change the job's runs-on to labels a pool serves, or add the label to a pool that can run the job.

Verify
:   Press Recheck after the pool or the workflow changes; the finding closes when every job's runs-on is served.

### `ci.secret_on_command_line` { #ci-secret_on_command_line }

Area
:   ci

Severity
:   warning

Detects
:   A secret is interpolated into a run line or a command-line argument, where it reaches the process list and the log.

Fix
:   Pass the secret through the step's env block and read it from the environment in the command; never interpolate it into run or args.

Verify
:   Press Recheck after the change reaches the default branch; the finding closes when no run line or args interpolates a secret.

### `protection.required_check_never_reports` { #protection-required_check_never_reports }

Area
:   protection

Severity
:   warning

Detects
:   A status check that GitHub Actions should post is required to merge, but no job this fleet saw in the window produced it.

Fix
:   Rename the required check to the name of the job that posts it, or remove it from the branch protection or ruleset if no job posts it any more.

Verify
:   Press Recheck after the required checks change; the finding closes when every required check pinned to GitHub Actions has a job of that name in the window.

<!-- zoomies:catalogue-end -->

**Exposure** is about strangers. Code from outside your project should never
run on a machine that keeps state between jobs, can reach the host's Docker
daemon, or sits somewhere you would mind losing. A public repository on a
self-hosted runner is the textbook way to hand a stranger a shell on your
network, which is why the weak-pool and fork checks are errors and the
softer two are warnings.

**Capacity** is about the fleet failing the people who use it. A job waiting for
a label no pool serves will wait for ever, and a job that runs for six hours is
usually stuck and not slow. Neither is a security problem, and both stop CI.

Each finding is written for the repository it is about: what is wrong, what to
change, the pools and runs involved, and where to look. The sentences come from
Kennel Club and never from the repository, so a pull request title or a branch
name cannot put words into an operator's page.

The whole registry, all thirty-four, is served by `GET /api/v1/kennel/checks`, so a
script can read what is checked without scraping this page.

**One finding can be worse than its usual severity.** On its own,
`exposure.public_repo_on_fleet` is a warning: a public repository using your
fleet is a fact to know, and often a deliberate one. When
`exposure.public_repo_weak_pool` or `exposure.fork_code_ran` is open beside it,
it is raised to an **error**, because a stranger can already reach something
that matters. The raising happens before waivers are applied, so a waiver of the
milder finding does not quietly cover the worse one. On acme/site the first
finding below is an error because the second is open beside it, and each says
what to change:

![The CI tab of acme/site: two open errors, a public repository running jobs on this fleet and a public repository's jobs run on a pool with weak isolation, each with what to change and a Waive button](screenshots/kennel-repository-dark.webp#only-dark){ .zoomies-shot }
![The CI tab of acme/site: two open errors, a public repository running jobs on this fleet and a public repository's jobs run on a pool with weak isolation, each with what to change and a Waive button](screenshots/kennel-repository-light.webp#only-light){ .zoomies-shot }

### The optional checks

Twenty-three checks read what is in a repository. Each group has a switch of its own
and is **off by default**. Until a switch is on, its checks are shown as **Turned
off** on the Overview, and nothing is read for them.

| Area | Checks | Turned on by | What it reads | What a finding is |
| --- | --- | --- | --- | --- |
| `setup` | 10: a README, a licence, a security policy, a contribution guide, a code of conduct, issue and pull request templates, `CODEOWNERS`, dependency updates and a CI workflow | `kennel.repository_setup` (**Check repository setup**) | The default branch's file names, as one Git tree request for each repository. No file contents. | Informational. It never lowers a repository's standing. |
| `guidance` | 4: missing instructions, broken references, exact duplication and unreadable files | `kennel.agent_guidance` (**Check agent guidance**) | Bounded instruction files and their local references. | Informational for absence and duplication; warnings for broken or unreadable guidance. |
| `ci` | 7: `ci.no_timeout`, `ci.no_concurrency`, `ci.action_not_pinned` | `kennel.workflow_checks` (**Check workflow best practices**) | The default branch's workflow files. | A warning, except `ci.no_concurrency`, which is a note, as is `ci.action_not_pinned` when only GitHub's own actions are affected. |
| `token` | 1: `token.permissions_unset` | the same switch | The same files. | A warning for a public repository, a note for a private one. |
| `token`, `exposure` | 3: `token.default_write`, `exposure.fork_approval_weak`, `exposure.private_fork_secrets` | `kennel.settings_checks` (**Check repository settings**) | The repository's Actions settings, with the App's Administration read permission. | Warnings. A weak approval policy is an error once a fork's code has run here. |

A setup finding says **repository-local**, because an account's default guidance
may stand in for a file the repository does not have, and a missing file is
advice, not proof. Empty files and symlinks do not count as present. A tree
GitHub truncates leaves the repository **partly checked** and produces no
"missing" finding, and so does a workflow file that is too large or too odd to
read: findings from the files that were read stay, but the repository cannot earn
an all clear.
[Configuration](configuration.md#repository-setup-advice) lists each setup check
and what its absence makes harder, and
[the workflow checks](configuration.md#workflow-best-practice-checks) say what
each detects and what is excluded.

Four more checks read a repository's settings and the status checks its default
branch requires. Three of them, `token.default_write`,
`exposure.fork_approval_weak` and `exposure.private_fork_secrets`, are turned on
by `kennel.settings_checks` (**Check repository settings**) and read the
repository's Actions settings: the default workflow token and the policy for
fork pull requests. They need the App's **Administration** read permission,
which GitHub offers no narrower form of, so it is asked for separately from the
rest and only if you turn the setting on; the read changes no setting. The
fourth, `protection.required_check_never_reports`, reads the status checks a
branch requires and cannot be turned on yet. See
[Repository settings checks](configuration.md#repository-settings-checks).

## Agent guidance

The **Agent guidance** tab shows structural findings about a repository's
instruction files. A healthy single-file setup is sufficient. These checks do
not judge whether prose is correct or whether a declared command works.

Enable **Check agent guidance** (`kennel.agent_guidance`) in Settings. Missing
instructions and exact Claude duplication are informational; broken references,
import cycles and files that cannot be inspected are warnings. Simple inline Markdown
links and standalone Claude `@path` imports are checked against the same pinned
tree. Links in fenced or inline examples, HTML comments, external links and
fragment-only links are ignored. Reference-style Markdown links and heading anchors are not checked.

An administrator presses **Preview guidance changes** to read the current default
branch and see each file's current and proposed contents. The proposal can:

* Create an initial `AGENTS.md` and a Claude import when guidance is absent.
  Commands come only from explicit root Makefile targets or package.json scripts;
  Zoomies never executes them or copies their script bodies into guidance.
* Replace exact duplicates of the root `AGENTS.md` in `CLAUDE.md` or
  `.claude/CLAUDE.md` with a relative import, preserving line endings and mode.
* Correct a standalone Claude import when exactly one recognised guidance file
  matches its name. Other prose stays as written.

Ambiguous links, import cycles and differing instructions need manual review.
For damaged Zoomies-owned context sections, use **Reinstall / repair** in
[AI Context](ai-context.md). Guidance proposals do not install AI Context.

**Open draft pull request** approves the exact preview. A changed default branch
requires a fresh preview. The proposal branch is derived from the file changes;
retries reconcile it, and an existing different proposal is preserved rather
than overwritten or duplicated. Merge the draft in GitHub, then press
**Recheck**. Turning checks off or stopping repository tracking prevents both
reads and new proposals. No new assistant connection or source access is granted.

The explicit preview and proposal use the source-setup GitHub API: repository
metadata, default-branch refs, commits, trees and bounded blobs; publishing adds
Git trees, a commit, a new branch ref and a draft pull request. These are separate
from the background read-only endpoint list below.

## How to read a repository's standing

Every repository is in one of four standings, and the badge never says more than
it knows.

```mermaid
flowchart TD
    A[Evaluated?] -->|not yet| P[Pending]
    A -->|yes| B{An error or a<br/>warning open?}
    B -->|yes| N[Needs attention]
    B -->|no| C{Every enabled check<br/>ran against<br/>everything it needs?}
    C -->|no| Q[Partly checked]
    C -->|yes| G[Best in show]
```

| Standing | Plain words (Status style Off) | Means |
| --- | --- | --- |
| **Best in show** | No open findings | Every check that is turned on ran against everything it needs, and nothing worse than a note is open. |
| **Needs attention** | Needs attention | An error or a warning is open. It outranks *Partly checked*, so an open error is never hidden behind "we could not read everything". |
| **Partly checked** | Partly checked | Nothing is open, but something could not be read, so this is **not an all clear**. |
| **Pending** | Pending | Kennel Club has not looked at the repository yet. |

*Best in show* is a playful name for a serious claim, and it is earned the
hard way. A repository that could not be fully read is not the best of
anything, a repository with every check turned off is never given the badge,
and a finding that is only a note does not take it away. A waived finding does
not take it away either, but it is never hidden: the Overview counts the waived
findings beside the badge.

The dog-park name is the **Status style** in **Settings → Appearance**: with it
on *Cute* or *Standard* the badge says *Best in show*, and with it *Off*, which
is where a browser that has never chosen starts, it says *No open findings*.
What it means is the same either way.

One more thing looks like a standing and is not one: **Not tracked** means
somebody told Kennel Club not to look at the repository. See
[Stopping Kennel Club looking at a repository](#stopping-kennel-club-looking-at-a-repository).

### Where a workflow finding points

A finding from a workflow file says which file, which job and which line, as
`.github/workflows/ci.yml:41 (job 2)`, so the person who opens it knows where
to look before they open the editor. The file is identified by the SHA of the
blob that was read, not by its path: a path is text somebody chose and a blob
SHA is not, so the page shows the path only where it has the shape a workflow
path always has, and otherwise says the workflow has an unusual name. A waiver
of such a finding is about that one file, named by the first twelve characters
of its SHA, and ends by itself when the file changes, because the finding it
excused is no longer the finding there is. A finding about the repository as a
whole, such as pins that no updater moves, has no file and a waiver of it covers
the repository.

## What it reads

Every check names the facts it needs, and a check whose facts cannot be read is
**skipped and says why**. It is never judged against half the picture.

| Source | What it is | Permission it needs |
| --- | --- | --- |
| **What this fleet observed** | Which pool ran a job, which label nothing served, how long a job ran. Already in Zoomies' database. | None. It costs no GitHub request and is always readable. |
| **Repository details** | The repository's own record, above all whether it is public. | *Repository permissions: Metadata: Read-only* |
| **Workflow runs** | What triggered the runs this fleet ran, and which repository they came from. Read for **public repositories only**. | *Repository permissions: Actions: Read-only* |
| **Repository setup files** | The default branch's file names. Read only when `kennel.repository_setup` is on. | *Repository permissions: Contents: Read-only*, needed for private repositories |
| **Repository settings** | The repository's default workflow token, and its policy for fork pull requests: the approval policy for a public repository, the rules for a private one. Read only when `kennel.settings_checks` is on. | *Repository permissions: Administration: Read-only*, needed for every repository. GitHub offers no narrower permission for these settings. |
| **Workflow best practices** | The default branch's workflow files, read for timeouts, concurrency, action pins, token permissions, a `pull_request_target` workflow that checks out the pull request's head, a secret interpolated into a command line, a `runs-on` label no pool of this fleet serves, pins that no updater moves, and a file that could not be read at all. A finding from here names the file, the job and the line. Read only when `kennel.workflow_checks` is on. | *Repository permissions: Contents: Read-only*, needed for private repositories |

`zoomies kennel check [path]` reads none of this. It reads `.github/workflows`
from the disk of the machine it runs on, with the same parser and the same
evaluator, and sends nothing anywhere: the checks that need what the fleet
observed, the run history or the tree as GitHub lists it are listed as *not
checked here*, and `--controller owner/name` is the one way a controller joins
in. A file is known by Git's blob SHA on both sides, so a waiver made on one is
about the file on the other.

A source can be in one of these states, and the Kennel Club page shows it beside
the repository it affects:

* **Read in full.** The usual case.
* **Partly read.** A page cap was reached, or the read was a sample. A finding
  found in a partial read stands, but finding nothing is not an all clear.
* **Not permitted.** GitHub refused for want of a permission the installation
  has not been given. The page names the permission. If you chose not to grant
  it, that is a choice and not a fault, so it is shown on the repositories it
  affects and does not become a problem.
* **Not available.** This GitHub does not offer it, or the repository does not
  have it. Never a finding.
* **Held back.** Reads are paused for this installation, either because GitHub
  rate-limited it or because Kennel Club has spent what its budget allows this
  hour.
* **Failed.** A transient failure, tried again at the next refresh.

A private repository costs no read beyond the repository listing Zoomies already
makes, because the checks that need run history are about public repositories.
The opt-in sources are the exception: with any on, a private repository's
tree is read too, which is why they ask for *Contents*.

### Every GitHub request it may make

The reader is held to a list, and a test runs it against a fake GitHub and fails
on any request that is not on the list, so a new call is a visible change in
review and not something found in a log. All background reads are `GET`s, and the last five
are made only when one of the opt-in switches is on.

| Request | What for |
| --- | --- |
| `GET /installation/repositories` | The repositories the App can see, which Zoomies already lists. A refresh GitHub answers "not modified" costs nothing against the rate limit. |
| `GET /repos/{owner}/{repo}` | One repository's own record: whether it is public. |
| `GET /repos/{owner}/{repo}/actions/runs/{run}` | One workflow run the fleet ran in a public repository. **Four fields are read**: the event that triggered it (held to a short allow-list before anything sees it), the repository it ran in, and the repository its head commit is in, which differ for a pull request from a fork, and the run's ID. Who triggered it, its branch, its title and its workflow's path are written by whoever opened the pull request, and are never read. |
| `GET /orgs/{org}/actions/runner-groups` | Whether the organisation runner group a pool joins allows public repositories, so a finding can say so. |
| `GET /rate_limit` | The limit the installation reports, which the budget is a share of. GitHub does not count this request against the limit. |
| `GET /repos/{owner}/{repo}/git/trees/{tree}` | The default branch's file names, one conditional request for each repository on each refresh, for the setup checks and to find workflow and instruction files. Inspection stops at 10,000 entries, and a truncated tree produces no "missing" findings. |
| `GET /repos/{owner}/{repo}/actions/permissions/workflow` | Whether the repository's default workflow token can write. One request for each repository on each refresh, only when `kennel.settings_checks` is on. |
| `GET /repos/{owner}/{repo}/actions/permissions/fork-pr-contributor-approval` | The approval policy for fork pull requests, for a public repository only. The policy's name is held to a short list before anything sees it. |
| `GET /repos/{owner}/{repo}/actions/permissions/fork-pr-workflows-private-repos` | Whether a private repository's fork pull requests run workflows, and are sent its secrets or a token that can write. Three yes-or-no answers; nothing a repository wrote. |
| `GET /repos/{owner}/{repo}/git/blobs/{blob}` | One workflow or instruction file, pinned to the immutable blob the tree named. At most 50 files a refresh, each at most 256 KiB. Agent guidance reads at most 32 files, each at most 64 KiB. The file is parsed and never executed, and what is kept is where each finding is, as the blob's SHA, a job's index and a line number, together with the file's path where it is one of the usual shape: no job name, expression or text. |

## What it never does

* **Background checks only read.** Agent guidance changes are proposed only
  when an administrator reviews a preview and asks for a draft pull request.
  Kennel Club never merges it or changes a pool, runner or job.
* **It reads no source code.** It looks at who ran what, where, and how long a
  job took. The only files it can read are the default branch's file names and
  its workflow and instruction files, and only if you turned on their respective
  opt-in settings. A requested guidance preview also reads the root Makefile
  and package.json for declared commands, capped at 512 KiB each. AI Context is the part of Zoomies that reads source
  for assistants, and Kennel Club does not touch it, which is also why turning
  one off leaves the other alone.
* **It does not repeat a stranger's words.** The text of a finding is written by
  the check, from a fixed set of sentences. Anything in a repository that a
  stranger could write, such as a branch name, a pull request title or a
  workflow name, is kept out of the sentences, and a test fails if one gets in.
* **It does not starve scaling of GitHub requests.** Registering runners and
  scaling use the same rate limit. Kennel Club never starts a read for an
  installation GitHub has rate-limited (the tree and file reads included), it
  spends at most
  `kennel.api_budget_percent` (5 to 50 per cent, 20 by default) of the limit
  that installation last reported, and it stops altogether when less than half
  of the limit is left. A read GitHub answers with "not modified" costs
  nothing against the limit, so a refresh that finds nothing changed is free.

## Choosing what it looks at

`kennel.scope` says which repositories it checks:

* **`served`** (the default) checks the repositories this fleet has run a job
  for. A repository nobody has used you for has nothing to say about your fleet.
* **`installation`** checks the repositories the GitHub App can see, which also
  finds the ones that *could* send you a job but have not yet. It reads **at most
  500** repositories for each installation, so on a larger one the rest are never
  discovered, and Kennel Club says nothing about them. It also multiplies the
  requests Kennel Club makes, which is why it is a choice and not the default.

`kennel.refresh_interval` is how stale what Kennel Club read from GitHub may get
before it is read again. What your own fleet observed is not subject to it: that
is re-checked as the fleet changes, and costs GitHub nothing.

### Turning a check or an area off

`kennel.disabled_checks` takes check codes (`exposure.fork_code_ran`) or whole
areas (`exposure`, `capacity`, `setup`, `ci`, `token`). Turning off every setup
check stops its file-name reads, and turning off both `ci` and `token` stops the
workflow reads. A misspelt name is refused when you save it,
because a name that matches nothing would leave running the check you meant to
turn off.

A check that is turned off is **shown as turned off**, not hidden, so nobody
reads its silence as a clean bill of health. And a repository with every check
turned off is never given the *Best in show* badge.

## Waiving a finding

Some findings are decisions you have made. A repository that deliberately runs
public jobs on a disposable pool has a finding for it, and you may be content
with that. A **waiver** records the decision, instead of hiding the finding.
**Waive** on a finding asks for the reason and for how long:

![The Waive this finding dialog for a public repository running jobs on this fleet: a reason typed in, a waiver of 30 days, and the Cancel and Waive buttons](screenshots/kennel-waive-dark.webp#only-dark){ .zoomies-shot }
![The Waive this finding dialog for a public repository running jobs on this fleet: a reason typed in, a waiver of 30 days, and the Cancel and Waive buttons](screenshots/kennel-waive-light.webp#only-light){ .zoomies-shot }

* **It needs a reason**, of at least ten and at most five hundred characters,
  because whoever reads the audit log in a year will want to know why.
* **It needs an end**, at most a year away. A decision nobody is asked to make
  again is how an exception becomes the rule.
* **It names who decided**, and it is in the audit log.
* **It is never hidden.** A waived finding is listed under **Waived** with who
  decided, why and until when, and the Overview counts it.
* **It comes back by itself if it gets worse.** A waiver covers the finding at
  the severity it had when it was made, so a warning waived last month does not
  excuse the error it has since become.

Waiving an **error** is an administrator's decision. An operator can waive a
warning or a note. Any operator can end a waiver, since ending one only makes
Kennel Club stricter.

## Stopping Kennel Club looking at a repository

Every repository is **tracked** until somebody says otherwise. A scratch
repository that runs public jobs on purpose, or one that is archived in all but
name, can be told to be left alone. On the repository's page, **Track this
repository** is a switch, and turning it off says what it will do before it does
it:

![The Stop tracking this repository dialog for acme/api: four consequences listed, a reason for leaving it alone, and the Cancel and Stop tracking buttons](screenshots/kennel-stop-tracking-dark.webp#only-dark){ .zoomies-shot }
![The Stop tracking this repository dialog for acme/api: four consequences listed, a reason for leaving it alone, and the Cancel and Stop tracking buttons](screenshots/kennel-stop-tracking-light.webp#only-light){ .zoomies-shot }

* **An administrator stops it**, and is asked for a reason first, in the same
  length a waiver's is held to: stopping silences the repository's errors, so
  whoever finds it quiet in a year will want to know why.
* **An operator starts it again** with one press and no reason, because that can
  only make Kennel Club stricter. Anybody else sees the state and who can
  change it, instead of a switch that would answer 403.
* **A repository that is not tracked is not read and nothing is evaluated for
  it.** It has no findings, raises no problem, is counted on the Overview as
  **Not tracked** and in no other number, and has no *Recheck*. Having no
  findings is not an all clear, and its page says so.
* **Its waivers are kept** and do nothing until it is tracked again.
* **It is kept however long it has been quiet.** A repository nobody has run a
  job for in ninety days is normally forgotten, but not one somebody chose to
  stop, so a sandbox does not come back tracked, and read, the day somebody
  pushes to it.

Afterwards the repository's page says who stopped it, when and why, and that it
is not looking:

![The page of acme/api once it is not tracked: the Track this repository switch off, and a notice saying Kennel Club is not looking at it, who stopped it, when and why](screenshots/kennel-not-tracked-dark.webp#only-dark){ .zoomies-shot }
![The page of acme/api once it is not tracked: the Track this repository switch off, and a notice saying Kennel Club is not looking at it, who stopped it, when and why](screenshots/kennel-not-tracked-light.webp#only-light){ .zoomies-shot }

The repository list leaves untracked repositories out unless you ask for them,
so that a card's number and the rows behind it agree. The **Tracking** filter
brings them back.

### Stopping several at once

A fleet that has a dozen sandboxes to leave alone does not have to open a dozen
pages. On the **Repositories** list an administrator ticks the rows and presses
**Stop tracking** in the bar that appears. The dialog names what is about to be
stopped (eight names, then a count of the rest), asks for one reason in the same
length as above, and says the same four things it says for one repository.

* **Each repository is its own decision.** The reason is written once and sent
  for each, so every repository has its own request and its own audit entry,
  naming the person who stopped it.
* **The requests go one after another**, in the order the rows were ticked, and
  not all together. A failure is then attributable: when one is refused the page
  names it, instead of saying that some were, and the audit log reads in the
  order the person chose.
* **A repository that is already not tracked is left as it is**, keeping the
  reason it was stopped with and the person who stopped it, so this is not a way
  to rewrite what a colleague wrote. The dialog says how many it is leaving, and
  if everything ticked was already stopped it says there is nothing to stop
  instead of asking for a reason to do nothing.
* **What was refused stays ticked.** If some could not be stopped, the page says
  how many, names them and says why, and the ones that did stop leave the list;
  the rest are still selected, so pressing the button again tries only those.
* **A reason the controller refuses is said beside the field**, and nothing has
  been stopped, because the same reason would be refused for every one of them.

Only an administrator is offered the tick boxes, since stopping is theirs. There
is no bulk start: starting again is one press on a repository's own page, and
nobody has asked for a hundred repositories to be made stricter at once.

## Where it shows up

The Repositories list opens on the tracked repositories that have run a job in
the last thirty days. **Active on Zoomies** widens it to every repository Kennel
Club knows, a choice each person's browser remembers, and the Overview's cards
open it widened, so that a number and the rows behind it agree:

![The Kennel Club Repositories list: acme/site needs attention with two errors, acme/widgets and acme/api have no open findings, and the filters above it include Tracked and Active on Zoomies](screenshots/kennel-repositories-dark.webp#only-dark){ .zoomies-shot }
![The Kennel Club Repositories list: acme/site needs attention with two errors, acme/widgets and acme/api have no open findings, and the filters above it include Tracked and Active on Zoomies](screenshots/kennel-repositories-light.webp#only-light){ .zoomies-shot }

* **A prompt for a coding agent.** Every open finding has a **Copy prompt for
  your coding agent** button, and `zoomies kennel repository <id> --prompts`
  prints the same text: the code, the sentences the page shows, the evidence
  quoted in a fenced block headed as repository data, and how to go about the
  change, which is to read the file's history first and make the smallest
  change that resolves the finding. The page never composes a prompt of its
  own: the controller renders it, so the button, the API and the terminal hand
  an agent one text. Over MCP the prompt is left out with the evidence it
  quotes, and the tool says where to get one. A waived finding has none,
  because nobody is asked to fix it.
* **The terminal.** `zoomies kennel` has the Overview, the list, one
  repository, the catalogue and a recheck, and `zoomies kennel check [path]`
  runs the workflow checks over a checkout with no controller at all. See
  [the CLI page](cli.md#zoomies-kennel).
* **The UI.** Under **Kennel Club** in the side menu: the **Overview** (how many
  repositories are in each standing, what is open by severity, and which to open
  first), **Repositories** (the list, narrowed by standing, severity, check and
  tracking, every filter in the address so that a view is a link to send), and
  each repository's own page, with an **Overview** of what the fleet knows, a
  **CI** tab with Kennel Club's findings and a tab for its **AI Context**. See
  [the UI, page by page](ui.md#kennel-club) for each page.
* **Problems.** Two kinds reach the problems list, and no others:
  `kennel.exposure`, one problem for the fleet with the count of repositories
  that have an open, unwaived *error* in its title and none of them named, and
  `kennel.unavailable`, which says Kennel Club has been unable to read an
  installation for six hours. A warning on its own raises nothing: it appears in
  Kennel Club and nowhere else. See [problem codes](problem-codes.md).
* **The API.** `GET /api/v1/kennel` and the routes beneath it, and three event
  kinds on the stream: `kennel.updated`, `kennel.deleted` and `kennel.summary`. See [the API](api-surface.md#kennel-club).
* **Assistants.** The `kennel_overview`, `kennel_repository` and `kennel_findings`
  tools, which read the same API and can do nothing the caller's token could
  not. See [Connect Claude](connect-claude.md).
* **Metrics.** `zoomies_kennel_*` counters: findings opened, closed and waived
  by check, GitHub requests by outcome, and rate-limit holds. See
  [metrics](metrics.md).

## Roles

| Can | Role |
| --- | --- |
| Read everything above | viewer |
| Recheck a repository, waive a warning or a note, end a waiver, start tracking again | operator |
| Waive an error, stop tracking a repository | administrator |

Tokens carry the same split in scopes, so a token can be allowed to waive
warnings without being allowed to waive errors.

## Turning it off again

Set `kennel.enabled` back to false. It stops reading at once, and the stored
evaluations are kept, so turning it on again does not start from nothing. While
it is off the UI says so and shows nothing as if it were a verdict, and nothing
Kennel Club raised stays in the problems list.
