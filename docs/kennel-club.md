---
icon: material/shield-check-outline
description: >-
  What Kennel Club checks about the repositories a Zoomies fleet serves, check
  by check: what each detects, what to change, and how to see that it worked.
---

# Kennel Club

Kennel Club is how the repositories this fleet serves measure up against what
affects CI and the fleet: a public repository whose pull requests run on a
machine that keeps state between jobs, a label no pool serves, a job with no
timeout. It is off until an administrator turns on `kennel.enabled`, and
[the UI page](ui.md#kennel-club) says what it would read and what it never does
until then. [Configuration](configuration.md#kennel-club) has its settings;
the [problem codes](problem-codes.md) page has the one problem it raises,
`kennel.exposure`.

Every check has a stable code, `area.name`, and the list below is the one the
evaluator runs: it is generated from the registry, so it cannot disagree with
what a repository's page shows. The same list, with every problem code beside
it, is published as [`catalog.json`](https://zoomies.sh/catalog.json) for an
agent to fetch once, and a running controller serves it at
`GET /api/v1/catalog`.

## Checks

<!-- zoomies:catalogue-begin -->

#### `exposure.public_repo_on_fleet` { #exposure-public_repo_on_fleet }

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

#### `exposure.public_repo_weak_pool` { #exposure-public_repo_weak_pool }

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

#### `exposure.fork_code_ran` { #exposure-fork_code_ran }

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

#### `exposure.target_event_ran` { #exposure-target_event_ran }

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

#### `capacity.unserved_label` { #capacity-unserved_label }

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

#### `capacity.job_hit_default_limit` { #capacity-job_hit_default_limit }

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

#### `setup.readme` { #setup-readme }

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

#### `setup.licence` { #setup-licence }

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

#### `setup.security` { #setup-security }

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

#### `setup.contributing` { #setup-contributing }

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

#### `setup.code_of_conduct` { #setup-code_of_conduct }

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

#### `setup.issue_template` { #setup-issue_template }

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

#### `setup.pull_request_template` { #setup-pull_request_template }

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

#### `setup.codeowners` { #setup-codeowners }

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

#### `setup.dependency_updates` { #setup-dependency_updates }

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

#### `setup.workflows` { #setup-workflows }

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

#### `ci.no_timeout` { #ci-no_timeout }

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

#### `ci.no_concurrency` { #ci-no_concurrency }

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

#### `ci.action_not_pinned` { #ci-action_not_pinned }

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

#### `token.permissions_unset` { #token-permissions_unset }

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

<!-- zoomies:catalogue-end -->

## Keeping this list honest

The block above is written by `make generate` from `internal/kennel`'s
registry, and `TestTheKennelClubPageListsEveryCheck` fails the build when a
check is added without it being run. The sentences are the registry's own, so
correcting one here is correcting it in the wrong place.
