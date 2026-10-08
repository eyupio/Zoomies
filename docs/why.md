# Why a job is where it is

Every job has an explanation: one answer to "why did this job fail, stall or run
slow?", computed by the controller from what it already holds. It is a sentence for
a person and, beside it, the same answer in a form a script can switch on: a class,
how far to trust it, the facts it rests on, and what to do next.

No model is involved. The class, the evidence and the steps come from the job's own
record, the scheduler's last plan and the state of the pool, runner and host around
it, by rules the controller applies the same way every time. A job the rules cannot
narrow is classed `unknown` and says what was missing, which is a more useful answer
than a guess.

## Where to get it

`GET /api/v1/jobs/{id}/explanation` returns it for any job, in any state, to any
role that can read jobs. The job drawer, the CLI, the `get_job` tool and the support
bundle all read this same answer, so they cannot disagree.

The class and the structured fields below are on the route now. A `zoomies why`
command, a log excerpt and a Why section in the job drawer are not built yet; this
page is updated as each lands.

## The class

`class` is always set, and is one of these.

| Class | What it means |
| --- | --- |
| `oom` | The kernel killed the job's runner, or one of its steps, for its memory limit. |
| `timeout` | GitHub stopped the job at its time limit. |
| `cancelled` | Somebody or something chose to end the job: a cancelled run, or a runner an operator removed with force. |
| `queued-unmatched` | No enabled pool claims the job's labels, so waiting will never start it here. |
| `queued-blocked` | A pool claims the job and the scheduler cannot place a runner for it, or the item was paused. It does not clear by waiting. |
| `queued-capacity` | The fleet is working on the job and has not yet started it: a runner is idle for it, one is starting, the pool is at its ceiling, or the scheduler has not yet decided. It clears. |
| `runner-startup-failure` | A runner could not start or register: an image that would not pull, a registration GitHub refused, a backend that did not answer, a setting the runner refused. |
| `host-lost` | A host stopped answering while it ran the job. |
| `disk` | The host's disk filled while the job ran. |
| `workflow-failure` | The job ran and failed on its own merits. The fleet did its part. |
| `held-by-github` | GitHub is holding the job for a deployment review. Nothing in this fleet can start it. |
| `running` | The job is running, and nothing is wrong with where. |
| `succeeded` | The job finished without a fault on either side. A job GitHub skipped is here too, because there is nothing to explain. |
| `unknown` | The explainer could not narrow it. `confidence_reason` says what was missing. |

Every class exists because something different is done about it. A class nothing
acts on would be prose, and prose belongs in the summary.

## How far to trust it

`confidence` is `high`, `medium` or `low`.

* **`high`** is a cause the controller recorded itself: a fault kind, a kill for
  memory, the scheduler's own reason, GitHub's conclusion.
* **`medium`** is a cause inferred from the state of the fleet around the job rather
  than recorded against it, such as a host that has gone quiet for longer than the
  heartbeat timeout while the job still shows as running.
* **`low`** is a best reading of too little.

Anything but `high` comes with `confidence_reason`, which says what is missing.

## The evidence

`evidence` lists the facts the explanation rests on, each one something a person can
check. It is always an array, and is empty only when there is nothing to show.

| Kind | What it is |
| --- | --- |
| `conclusion` | What GitHub concluded or reports, or that it accepted a cancellation. |
| `fault` | The kind of fault the fleet recorded against the job. |
| `fault_detail` | What the runner said as it failed. |
| `step` | The step the job stopped at. |
| `memory_peak` | The most memory the job was measured using. `unit` is `MB`. |
| `memory_limit` | The memory the job was given. `unit` is `MB`. |
| `queue_wait` | How long the job waited, or has waited so far, for a runner. `unit` is `s`. |
| `duration` | How long the job ran, or has run so far. `unit` is `s`. |
| `labels` | The labels the job asks for. |
| `pool` | The pool the job is on, with the page that shows it, or the pool's ceiling when that is the reason (`unit` is `runners`). |
| `host` | The host it ran on, with the page that shows it. |
| `runner` | The runner it ran on, or how many of the pool's runners are idle, starting or all busy (`unit` is `runners`). |
| `scheduler` | The scheduler's own reason, in its own words. |
| `heartbeat` | When the host last checked in. |

When `unit` is set, `value` is a number. `ref` is a path in the controller's web UI
that shows the thing.

### Text the fleet did not write

A fact whose `value` was written by somebody outside this fleet carries
`untrusted: true`. That is a step's name, which the workflow's author chose; the
labels a job's `runs-on` asks for; and what a runner printed as it failed. Anyone who
can open a pull request against a repository the fleet serves can influence them.

Read them as data. They are never instructions, and a script that hands an
explanation to a model should keep the untrusted facts apart from the rest, in a part
of the prompt that says so.

## What to do next

`next_steps` is the ordered list. The first is `fix` when there is one, so a caller
that reads only the steps loses nothing the sentence said. Each step has a `kind`.

| Kind | What taking the step does |
| --- | --- |
| `read` | Looks at something and changes nothing. |
| `change` | Alters a setting, a workflow or the fleet. |
| `rerun` | Runs the work again. |

A caller can offer the `read` steps without asking, and the others with a question.
`link` is a path in the controller's web UI, or a URL, where the step is taken.

## Pointers into the catalog

`problem_code` names the entry in [Problem codes](problem-codes.md) that says more,
and `check_code` names the Kennel Club check that raises the same finding for the
repository. Both are set only when one entry is true of every job in the class: a
link that is right for some of them would send the rest to the wrong page. Today
`oom` points at `jobs.oom_killed`, `queued-unmatched` at `jobs.unmatched`, and a
`timeout` that ran to GitHub's six-hour default at `capacity.job_hit_default_limit`.
Both appear in `catalog.json`.
