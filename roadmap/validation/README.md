# Validation evidence

One file per gate or per measured baseline, named for the commit it describes,
with one exception, the drill record, which is a running log rather than a
snapshot and is named for what writes it.
A file here is a record of what was run, on what, with what result, and what
was not run. It is not a summary of what should be true.

Rules:

* Name the exact commit, the toolchain versions, and the machine or runner
  class. A number without the setup that produced it is not evidence.
* Separate *passed*, *failed*, *not run* and *blocked*. A test that skipped
  itself for want of a credential or a daemon is *not run*, and a gate that
  counts it as passed is lying.
* Record the denominator with every rate. "99% of attempts" means nothing
  without the count and the exclusions.
* Link to the thing itself where one exists: a workflow run, a pull request, a
  log kept somewhere durable. Do not paste a log body here; a redacted excerpt
  is enough.
* When a later change invalidates a result, say so in the file that held it
  rather than deleting it, so the history of what was believed and why stays
  readable.

Files:

| File | What it records |
| --- | --- |
| [baseline-6d12a72.md](baseline-6d12a72.md) | The reconciled baseline for the follow-on roadmap: what was run against `main` at `6d12a72` on 6 September 2026 and what was not. |
| [load-7d9441b.md](load-7d9441b.md) | What the reads an operator's pages make cost against a fleet with a month of history: the fixture, the figures, what came out of them and what they do not cover. Written by `make measure`. |
| [gate-f-readiness-2cc7d9c.md](gate-f-readiness-2cc7d9c.md) | What Gate F still needs at the end of Assignment A: which of its targets can be measured at all, what has never run, and what is the owner's to supply. |
| [proxmox-qualification.md](proxmox-qualification.md) | What ZF-214b's live qualification needs, the procedure it must follow, and the fact that none of it has been run. Written before the runs so the procedure cannot become a description of whatever happened to work. |
| [drills.md](drills.md) | Historical drill log. New CI runs append locally, publish the results in the job summary, and upload `drill-evidence-<commit>` artifacts retained for 90 days. CI does not push results to `main`; download evidence before expiry for longer retention. |

Gate F was withdrawn by ROADMAP.md version 3.0 on 19 September 2026, so no
file for it will be added; its definitions of scheduling latency, of a
Zoomies-caused failure and of a denominator stay in
[support-and-measurement.md](../support-and-measurement.md) and in
`docs/metrics.md`. Evidence from a real fleet (a Windows host, a Proxmox
cluster) is still recorded here when it arrives, and is never waited for.

See [Scorecard hardening](scorecard-hardening.md) for the September 2026 security changes and remaining repository settings.
