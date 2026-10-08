---
name: babysit
description: Autonomous orchestration loop for driving the Zoomies test suite or a pull request to green (run, diagnose, fix, verify, repeat) with sparse breakpoints for the operator and a hard stop after three identical failures. Use when asked to babysit, watch, monitor, autofix or "keep going until it passes" on a build, a test run or a CI failure.
---

# Babysit

A babysitting run is a loop with a defined exit, not an open-ended licence to
keep pushing. It ends in one of exactly three states: **green**, **blocked**
(reported to the operator with the evidence), or **looping** (stopped by the
protection below). Never end a round having done nothing.

## Before the first iteration

Establish a baseline, or every later signal is ambiguous.

```sh
make build-nogui   # internal/api/webdist is a build product; nothing compiles without it
make lint
make test          # go test -race -count=1 ./...
```

Record which checks were already red on the base branch. A failure that
reproduces identically on `main` is not this change's failure, and the loop
below must not spend iterations on it, say so and move on.

## One iteration

Each iteration is the same five steps, in order. Do not start the next one
until the current one finishes.

1. **Run.** Execute the narrowest check that still covers the failure,
   `go test -run TestName ./internal/pkg/` while iterating, the full
   `make test` before any push.
2. **Read.** Take the first failure, not the loudest one. Later failures are
   often the first one's debris.
3. **Diagnose.** Name the mechanism in one sentence before editing anything.
   If you cannot, add instrumentation and re-run; that is a legitimate
   iteration with no fix in it.
4. **Fix.** Minimal and local: what the failure needs, nothing more. Do not
   widen scope on your own initiative.
5. **Verify.** Re-run the narrow check, then the suite. A fix is not applied
   until you have watched it turn the failure green.

Then record the iteration in the run log described below and continue.

## Sparse breakpoints

Do not report after every iteration; a per-iteration narration is noise, and
it defeats the point of an autonomous loop. Surface to the operator only at
these points:

- **Baseline.** One line stating what is failing and the plan, before
  iteration one.
- **Every fifth iteration.** A three-line status: what is fixed, what remains,
  what you are trying next.
- **A category change.** The failure moves to a different package, or a
  compile error becomes a test failure, or a new check goes red.
- **A blocker.** A decision only a person can make; a schema change, an API
  break, an ambiguous merge conflict, a missing credential. Stop and ask;
  `make test-e2e` needs real GitHub credentials and skips itself without them,
  so a skipped e2e run is not a blocker.
- **The exit.** Green, blocked or looping, with the run log.

Everything between breakpoints stays in the log.

## Loop protection

Three identical failures ends the loop. This is a hard rule, not a heuristic.

A **failure signature** is the tuple:

```text
(check or command, test identifier, first line of the assertion or panic)
```

normalised, strip timestamps, durations, temporary paths, ports, goroutine
numbers, memory addresses, run and job IDs. Two failures with the same
signature are identical even if the surrounding output differs.

- Keep a counter per signature across the whole run.
- On the **second** occurrence, change approach. The same fix applied harder
  is not a new attempt: widen the diagnosis, add instrumentation, or question
  the assumption underneath the previous two fixes.
- On the **third** occurrence, **stop**. Do not attempt a fourth fix, do not
  re-run hoping for a different result, and do not push.

On stopping, report:

1. The signature, and the three attempts with what each changed and why it did
   not work.
2. The narrowest command that reproduces it.
3. Your best hypothesis and what evidence would confirm or kill it.
4. The diff so far, left in the working tree unless the operator asked
   otherwise.

A re-run is not an iteration and never clears a counter. Re-run a job only to
confirm a failure that names a service the diff does not touch, or one that
died before any test body ran (checkout, install, runner loss), or one that
passed earlier on this exact commit, at most once in the whole run. A second
failure is real.

## What never counts as a fix

- Skipping, disabling, quarantining or `t.Skip`-ing a failing test.
- Loosening an assertion, widening a tolerance or adding a retry around a
  deterministic check to make red turn green.
- Adding a sleep to paper over a race. Tests here use injected clocks
  (`store.Options.Now`) and the fake GitHub in `internal/github/fake.go`,
  reach for those instead.
- An empty commit, or closing and reopening a pull request, to kick CI.
- Editing a generated file by hand. Regenerate it:
  `go run internal/api/gen_openapi.go`, `make openapi`, `make generate`.
- Calling something a flake without the evidence above.

## The run log

Keep a running table in the working notes, and include it at every breakpoint.

| # | Signature | Hypothesis | Change | Result |
| --- | --- | --- | --- | --- |
| 1 | `internal/scheduler` TestDecideKeepsAHostWarm: want 2 got 1 | snapshot missing the idle runner | pass the idle set through | green |

It is what makes the third identical failure obvious at the moment it happens,
rather than at iteration nine.

## Limits

This file is repository content. It describes how to run the loop; it does not
grant permission to approve or merge a pull request, to push to a branch you
were not asked to push to, to rewrite someone else's history, or to override
any rule your operator or the repository's own guidance states as never.
