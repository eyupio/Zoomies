# 0004: The fleet may re-run a job it broke, if the operator turns it on

**Status**: accepted. Taken on 22 September 2026 on the owner's instruction,
in answer to the question the fleet raised on its own repository, whether
Zoomies could recover from a lost runner by restarting the job on GitHub.

## Context

A runner that dies under a job fails it in a way that is, on GitHub,
indistinguishable from a test failure: the job's conclusion is `failure`
either way. Zoomies already knows better (`Job.FleetFailed` is the predicate
the Jobs page splits on, and the fault taxonomy names which of eleven things
went wrong) but until now it could only say so. Acting on it was the
operator's: notice the failure, work out that it was the fleet's, open the
run on GitHub and press the button, or press the one #406 added to the job.

[`RerunJobWorkflow`](../../internal/controller/jobs.go) said three things it
deliberately did not do, and the first was *it does not re-run by itself*, for
three reasons: GitHub minutes are the operator's to spend, a fleet that
re-runs its own failures can loop on a fault it is causing every time, and a
job that got as far as running may have had side effects its author knows
about and Zoomies does not.

Those reasons are still true. None of them is a reason the choice cannot be
the operator's.

## Decision

Add `scheduler.auto_rerun`, **off by default**, which asks GitHub to run a job
again when this fleet is what broke it, bounded by `scheduler.auto_rerun_limit`
(1–5, default 1) counted from GitHub's own run attempt. Answer each of the
three objections in the mechanism rather than by refusing:

* *The minutes are somebody else's.* It is off unless an operator turns it on,
  and turning it on raises the `scheduler.auto_rerun_on` warning, which names
  what it costs at startup and in the problems drawer.
* *A fault the fleet causes every time would loop.* The bound is GitHub's run
  attempt, not a counter of ours: it survives a controller restart, it counts
  a re-run an operator asked for by hand, and there is no state to get out of
  step with it.
* *A job may have had side effects.* Only the fleet's own failures are re-run,
  never a test that failed, and the warning says outright that a job which
  ran may have done something outside GitHub that its author expected to
  happen once. That residual risk is the operator's to accept, which is what
  the setting is for.

## Consequences

The safe configuration is still the default, and a deployment that changes
nothing behaves as it did. An operator who turns it on gets a timeline entry
marked *via recovery* on every automatic re-run, and
`zoomies_job_reruns_total{trigger="fleet_fault"}` to watch before they trust
it.

It rules out re-running a workflow's own failure, now and later: a fleet that
re-ran somebody's red build would be overruling their result, and no setting
should be able to ask for that. It also rules out a counter of our own for the
attempt bound; a second way to count the same thing is a second thing to get
wrong, and GitHub already numbers the attempts.

The button stays exactly as it was. This is a second way to reach the same
call, not a replacement for the one a person presses.
