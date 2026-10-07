# Decision records

One file per decision, numbered in the order they were taken, never renumbered
and never deleted. A decision that is reversed gets a new record that says so
and links back; the old one is marked superseded.

Each record has five parts and no more:

* **Status**: proposed, accepted, superseded by NNNN.
* **Date**, and who took it. A decision the owner has to ratify says so in the
  status line until they have.
* **Context**: the facts that made the decision necessary, with links to the
  code or the evidence. Short.
* **Decision**: one paragraph, in the imperative.
* **Consequences**: what it costs, what it rules out, and what has to change
  because of it.

The decisions the roadmap asks the owner to ratify are listed in
[ROADMAP.md](../../ROADMAP.md) section 3. The first two have records here;
the rest get a record here when ratified or changed, so that the roadmap's
numbered list stays the index and this directory stays the history.

Decisions 13 to 17 were acted on before their records were written. Assignment
A built them, and most of what they settled is now in shipped migrations,
which decision 7 forbids editing or renaming: `0012` for the installation a
job is attributed to (13); `0013` and `0014` for the controller lease, the
task-issue stamp and the duplicated-agent fence (14); `0017` for the host's
reported resources and the operator's reserve (15); `0015` for the cleanup
record and `0016` for the drain timeout (17). Decision 16 shipped in code
alone and left no schema behind. Of the five, only 15 is still cheap to decide
differently — ZF-103's reporting half landed and its admission half did not,
so how a reservation is fitted, and what a blocked job is told about it, are
still open. Changing any of the other four now means a new record here and a
new migration, never an edit to an old one. All five still lack records, and
until those are written the roadmap's numbered list is the only account of
what was decided.

| Record | Decision | Status |
| --- | --- | --- |
| [0001](0001-planning-documents-live-beside-the-code.md) | Planning documents live in `ROADMAP.md` and `roadmap/`, outside the published site | proposed |
| [0003](0003-windows-runners-are-processes-on-a-host.md) | Windows runners are actions/runner processes on a Windows host, not Windows containers | accepted, at the owner's instruction; shape not separately confirmed |
| [0002](0002-choose-the-model-by-what-the-stage-risks.md) | Choose the Claude model and effort by what a stage risks, and record both per package | proposed |
| [0005](0005-a-stable-automation-contract.md) | Name the routes unattended automation may rely on, and promise their stability | proposed |
| [0006](0006-kennel-club-tracks-repositories-one-at-a-time.md) | Kennel Club tracks repositories one at a time: each is tracked by default, and an administrator can stop tracking one | accepted, at the owner's instruction; the role split not yet ratified |
| [0007](0007-the-repository-page-hosts-ai-context-and-adds-no-switch.md) | The repository page hosts the AI Context card and adds no switch or pause to AI Context | accepted, at the owner's instruction |
| [0008](0008-the-repository-page-leads-with-what-the-fleet-knows.md) | The repository page leads with an Overview of what the fleet knows, built from existing routes | accepted, at the owner's instruction |
| [0009](0009-the-problems-list-says-one-thing-for-kennel-club.md) | The problems list carries one entry for Kennel Club, not one for each exposed repository | accepted, at the owner's instruction |
| [0010](0010-kennel-club-has-a-side-menu-and-the-switch-is-in-it.md) | Kennel Club has a side menu like Settings', and its on/off switch is in it | accepted, at the owner's instruction |
