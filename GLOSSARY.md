# Zoomies

A self-hosted GitHub Actions runner fleet controller. This glossary fixes the words an operator sees in the web UI, so that one word means one thing wherever it appears.

## Language

### People

**Operator**:
The person who runs a Zoomies fleet and watches it, often all day. Setup flows and the public status page serve other audiences with their own rules.
_Avoid_: Admin, user (when meaning the person watching the fleet)

### Capacity

**Machine**:
A virtual machine that a provider rented or created. It exists before any agent has joined it.
_Avoid_: VM, node, instance

**Host**:
Anything running a Zoomies agent that the controller can place runners on, rented or your own. A machine becomes a host when its agent joins.
_Avoid_: Agent (as a thing an operator manages), node, server

**Agent**:
The software on a host that runs runners. It is never the thing an operator manages; the host is.
_Avoid_: "the agent on this host" as a stand-in for the host

**GitHub-hosted**:
Work that ran on GitHub's own runners rather than on this fleet.
_Avoid_: Hosted elsewhere, elsewhere, other runners

### Stopping and holding

**Drain**:
Finish what you have, take nothing new, then go. Applies to a runner or a machine.
_Avoid_: Cordon, disable (for a runner or machine)

**Pause**:
Hold, reversibly, with nothing lost. Applies to a pool, a provider, a host or a queued job's demand.
_Avoid_: Cordon, disable, hold (for a pool, provider, host or queued job)

**Disable**:
Switch an account off so it can no longer sign in. Used for accounts only.
_Avoid_: Turn off (for an account); disable (for a pool, provider or host)

**Remove**:
Take something out of the fleet or the queue for good. The action and the resulting state use the same word.
_Avoid_: Delete (for the action, where the state is Removed), deleted

### Work

**Run**:
One execution of a workflow, made up of jobs.
_Avoid_: Workflow (when meaning a run), build

**Job**:
One unit of work inside a run, placed on a single runner.
_Avoid_: Task, build step

**Queue**:
The jobs waiting for a runner, and what the fleet will do to start one for each. Where the demand itself is meant, say "queued demand".
_Avoid_: Provisioning (for the queue), backlog

**Provisioning**:
The state of a runner that is being set up. It describes runners only, never the queue.
_Avoid_: Provisioning demand, provisioning (for queued jobs)

### Things that are wrong

**Problem**:
Something that is currently wrong with the fleet, shown with a severity of Info, Warning or Error.
_Avoid_: Issue, risk, "to fix", warning (as a noun for a live fault)

**Finding**:
A result in the Kennel Club report about one repository. A report about a repository, not a live fault.
_Avoid_: Problem (for a Kennel Club result), warning

**Fixable, Advice, Optional**:
The three words a flagged row in a host's check tables carries beside its Warning or Suggestion badge: Fixable when the report offers the row to `zoomies tune`, Advice when it does not, Optional for a trade-off. Words about one row, never a state of the host. Warning stays reserved for the badge on a counted check.
_Avoid_: Warning (for what a person can do about a row), Safe (for Fixable, which is also a tier name)

### Kennel Club

**Kennel Club**:
The part of Zoomies that checks how a repository measures up against what affects CI and the fleet. It is the only meaning of "Kennel" in the interface's structure.
_Avoid_: Kennel (alone), AI Context (which is separate)

**AI Context**:
Repository context that Zoomies generates for AI tools to read. A top-level area of its own, not part of the Kennel Club.
_Avoid_: Kennel Club (as its parent), operational context

### Theme words

**Dog-park vocabulary**:
The optional playful state names, such as "Walkies!" and "Back in the kennel", that stand in for the plain state names when an operator switches them on. A theme, not a set of domain terms: every one has a plain equivalent that carries the meaning.
_Avoid_: Kennel vocabulary, quirky status
