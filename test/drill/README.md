# The drill tier

This is the only place in the repository that runs the product as an operator
gets it: **the built binary, twice** (once as a controller, once as a remote
agent that joined with a join token) with a real workload appearing on the
machine that runs it. The only thing faked is GitHub.

Everything else here tests the code inside the test process, where a restart is
a struct being rebuilt and an agent is a goroutine. That is the right shape for
almost everything, and it cannot see the seam this tier exists for: whether the
decision the scheduler makes actually reaches a host, whether the host does
something real with it, and whether the fleet's accounting agrees with the
world afterwards.

## Running it

```sh
make test-drill
```

No credentials, no Docker daemon, no network. That is deliberate; a tier that
needed any of those could not run on every pull request, and the roadmap wants
the lifecycle drill to.

## How it can run without any of that

Three substitutions, each chosen so that what is left is still Zoomies:

* **GitHub is `internal/github.FakeGitHub`**, started in the test process. The
  controller is pointed at it with `ZOOMIES_GITHUB_API_BASE_URL` and an
  installation carrying that base URL, so it mints JIT configurations and
  deletes registrations against the fake exactly as it would against the real
  thing.
* **The backend is `process`**, with a stub runner staged under the agent's
  work directory. The process backend skips its download when
  `_tools/<version>/bin/Runner.Listener` is already there, so the pool pins a
  version we staged. The stub is a shell script that writes a marker, waits,
  and exits; a *real process*, laid out by the real backend, in the real
  place.
* **The drill drives the job**, both on the fake and through a signed
  `workflow_job` webhook. The fallback poller only finds *queued* jobs (it is
  the webhook's backstop, not a second channel) so a drill that only added
  jobs to the fake would watch a runner sit idle and never see the job it was
  made for.

What is *not* substituted: the controller, the agent, the join, the scheduler,
the task queue, the state machine, the backend, and the cleanup. Those are the
things under test.

Whether Microsoft's runner binary can talk to GitHub is not this repository's
behaviour, and putting the real one here would trade the whole tier for a
download.

## What the lifecycle drill asserts

1. A queued job makes the fleet create a runner, and the task reaches the
   remote agent.
2. **A real workload appears on this host**: not "the row says provisioning",
   but a directory laid out by the backend with a process running in it.
3. GitHub holds a registration, under the same name the fleet minted.
4. The job starts and the runner goes busy.
5. The job completes, the ephemeral runner exits of its own accord, and the
   fleet notices and marks it removed.
6. **Nothing is left**: no workload on the host, no registration on GitHub.

Steps 2 and 6 are the ones that make this a drill rather than an assertion
about Zoomies' opinion of itself.

## What the removal drill asserts, and why it exists separately

`TestRemovingARunnerDeletesItsRegistration` removes an idle runner the way an
operator does, and requires its GitHub registration to be gone **within fifteen
seconds**, well inside the reaper's first sweep.

It is separate because the lifecycle path cannot carry that assertion, and the
first version of this tier learned it the hard way. Written the obvious way
(wait for the workload, then check GitHub) the lifecycle drill **passed with
`deleteRegistration` commented out**. Two things delete a registration:
`removeRunner`, and the reaper that lists GitHub every ten minutes and tidies
up leftovers. The reaper's first sweep lands a minute after the controller
starts, which is roughly when the lifecycle drill finishes looking. It was
testing the backstop.

The deeper reason it cannot be sharpened in place: an ephemeral runner that
finishes its job exits by itself, the agent reports it gone, and the row is
marked removed without anything being deleted on GitHub, correctly, because
real GitHub removes a just-in-time runner once it has run its one job. The fake
does not model that, so on that path there is nothing of Zoomies' to hold to
account.

An operator removing an idle runner is the case the roadmap named: GitHub will
not tidy that up, so if Zoomies does not, the organisation's runner list fills
with dead entries. There, deleting the registration really is `removeRunner`'s
job, and the drill fails in seventeen seconds when it stops doing it.

Both drills name the runner they are asserting about rather than counting what
is left. While a job is still queued the scheduler is entitled to put a
replacement on the host immediately, and a drill that counted would fail on
correct behaviour, or, worse, pass or fail on which happened first.

## The fault drills

Five of the six the roadmap names are here. Both inject their fault while a job
is actually running, because that is the only moment at which any of this is
interesting: a fleet with nothing in flight recovers from anything.

`TestKillingTheControllerMidJobLeavesTheWorkRunning` kills the controller and
starts another on the same address and the same database. The runner keeps
running -- a control plane that takes the work down with it is worse than one
that simply stops -- the fleet still has the runner when it comes back, and the
job finishes and is cleaned up afterwards, which it can only do if the agent
found its way back on its own. Recovery is measured to the cleanup rather than
to the API answering: a fleet is not recovered when its API is up, it is
recovered when work is moving through it.

`TestKillingTheAgentMidJobLeavesTheWorkRunning` kills the agent and starts it
again with no join token. The runner survives -- `detachRunner` puts it in its
own process group, and the agent's death is not its own -- and the agent comes
back as the same host from the credentials in its work directory rather than as
a second one.

**Its finding, and the reason a drill tier earns its runtime:** the exit code is
written by the parent that reaped the process, so an agent restarted mid-job
finds its adopted runner gone with nothing recorded and calls that a failure.
It is racing the removal the completed job set off, so the same successful build
ends up recorded as `removed` on one run and `failed` on the next. The drill
asserts what is true either way and puts the rest in the record's last column,
because a drill that asserted one of the two would be a flaky test pretending to
be a rule.

`TestAFailingJITEndpointLeavesNothingBehindAndRecovers` makes GitHub refuse to
mint runner configurations. There is nothing wrong with the fleet, the host or
the pool, and nothing an operator can do but wait, so what matters is that the
fleet does not make it worse -- the attempt is recorded as a failed runner with
GitHub's own message, and nothing is left on the host or registered on GitHub --
and that it places a runner by itself once the endpoint answers again.

`TestARateLimitedInstallationStandsDownAndComesBack` takes the installation's
quota away. A fleet out of quota looks exactly like an idle one everywhere
except `zoomies_github_paused`, so the drill reads the controller's own scrape
rather than its API, and then watches it start again by itself when the reset
passes -- a wall-clock deadline the controller keeps for itself, which nothing
in process can prove.

**It found a defect on its first run.** The stand-down never engaged: GitHub
refuses the *installation token refresh* first, ghinstallation does that inside
the transport, and its refusal arrived unclassified -- so the poller kept
calling every two seconds while the quota was gone and the paused gauge stayed
at zero. The fix is in `classify`, and only when the refusal's own headers say
the quota is actually gone: a 403 for a missing permission comes back with
quota to spare, and standing an installation down for fifteen minutes over that
would be a wait that fixes nothing.

`TestAHostWithNoDockerSocketSaysWhichBackendIsUnusable` joins a second agent
whose Docker socket is not there -- a second machine rather than a
reconfigured first one, because that is the shape of the fault in a real fleet:
one host's daemon is down and the others are fine. The agent joins (one that
refused to start would take away the fleet's only way of telling anybody), the
host names the backend it cannot use and the socket it looked for, and the pool
with work waiting raises `pool.no_capacity` at error severity with a fix. It
deliberately stops there: starting a daemon needs a daemon, and a tier that
needed one could not run on every pull request. A socket that is not there is
what a stopped daemon looks like from the outside, and that half is honest
here; the recovery half belongs on the reference host the roadmap asks the
owner for.

`TestAHostHammeringTheAgentRoutesDoesNotDelayAnotherHostsTasks` joins a
second host from the test process (one that offers no backend, so nothing is
placed on it) and has it hammer its heartbeat and its task poll from dozens of
goroutines at once, which is what a broken retry loop or a stolen agent token
looks like. While it does, a job is queued for the drill's own agent, and the
drill **asserts** that the job becomes a workload on that host within one poll
interval, that the host stays healthy, and that the controller's scrape counts
the noisy host's refusals under both the `rate` and `poll` limits. The limits
are per host so that one host's misbehaviour is its own problem; this is the
drill that says so of the built binary rather than of a struct.

The restart drills watch the runner for a few seconds after the fault rather
than checking it once. A death that follows its parent's arrives a beat later, and the stub's
marker file outlives the process that wrote it -- so the check is the pid file
and signal 0, not the directory.

## Why they take about a minute each

The agent compares the host against what it is tracking on a fixed thirty-second
tick (`defaultReconcileInterval`), so a finished workload is noticed within one
tick. That is a real characteristic of the product, not a slow test, and it is
not configurable, nor should it be made configurable to speed a test up.

## The record

Every run appends a row to `roadmap/validation/drills.md`: when, the commit,
the run that wrote it, the drill, the outcome, what it proves, what was
observed, recovery time for a fault drill, and **whether a person has to do
anything**. That last column is
the one to read first: a drill that recovers on its own and one that leaves a
container for somebody to delete are different findings.

The file is appended rather than replaced, and the drill job commits it from
the default branch, so the history outlives the run that produced it. A drill
that has recovered cleanly forty times and then did not is a finding that only
exists if the forty are there to compare against. A pull request's rows stay in
its own job summary: they describe a commit that may never exist.

Set `ZOOMIES_DRILL_RECORD_DIR` to write it somewhere else.
