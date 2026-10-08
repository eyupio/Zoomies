# Proxmox qualification

This is the harness that turns "the Proxmox provider works" from a claim into
evidence: twenty create, enrol, run, drain and delete cycles against a real
Proxmox VE cluster, the five fault cases the roadmap names, and a closing
inventory reconciliation that fails on any owned resource nobody can account
for.

It creates virtual machines on somebody's real hypervisor, so most of what
follows is about making sure it never leaves them there.

**Fixture success is not live qualification.** `go test ./...` exercises the
whole machine lifecycle against `provider.Fake` and the Proxmox client against
an `httptest` server. That proves the logic and the wire format, and proves
nothing whatever about a hypervisor. Only a green run of this harness, with its
evidence filled into
[roadmap/validation/proxmox-qualification.md](../../../roadmap/validation/proxmox-qualification.md),
is qualification.

## What you need

A Proxmox VE cluster you are willing to lose, and a GitHub installation that can
queue real jobs; a machine that never ran a job has not been qualified for
anything.

On the cluster, before you start:

* **A VMID range reserved for this and nothing else.** This is the one setting
  that makes the harness safe to run at all. Without it the provider would
  allocate identifiers anywhere in the cluster, including ones somebody else's
  automation is entitled to. The range must hold at least twenty identifiers.
* **A prepared template** to clone, with the QEMU guest agent installed and
  enabled, outside the reserved range. Everything the provider builds starts as
  a clone of it; [docs/proxmox.md](../../../docs/proxmox.md) says how to build
  one.
* **An API token** with the privileges the provider's preflight asks for,
  [docs/proxmox.md](../../../docs/proxmox.md) lists them. The harness runs that
  same preflight before it creates anything and names each one that is missing,
  rather than failing at cycle three.

## Running it

```sh
export ZOOMIES_PROXMOX_URL=https://pve.example.com:8006
export ZOOMIES_PROXMOX_TOKEN='zoomies@pve!qualify=00000000-0000-0000-0000-000000000000'
export ZOOMIES_PROXMOX_NODE=pve1
export ZOOMIES_PROXMOX_TEMPLATE=9000          # the template's VMID
export ZOOMIES_PROXMOX_STORAGE=local-lvm
export ZOOMIES_PROXMOX_VMID_RANGE=9100-9199   # reserved for this, and nothing else

# The GitHub side, the same variables the Docker end-to-end test uses.
export ZOOMIES_E2E_APP_ID=123456
export ZOOMIES_E2E_INSTALLATION_ID=987654
export ZOOMIES_E2E_PRIVATE_KEY_FILE=/path/to/app.private-key.pem
export ZOOMIES_E2E_TARGET=my-org
export ZOOMIES_E2E_REPO=my-org/zoomies-e2e

make test-e2e-proxmox
```

It takes hours, not minutes, which is why it is not in CI and why the Makefile
gives it a ten-hour timeout.

The setting people get wrong is `ZOOMIES_PROXMOX_CONTROLLER_URL`. The guests
enrol by calling back, so a controller bound to loopback can be talked to from
the machine running the harness and from nowhere else, and every machine it
builds enrols never. The harness works out a reachable address by looking at
which interface would route to the cluster; set the variable when that guess is
wrong, to a URL with a port that a guest can reach.

The rest are optional:

| Variable | Default | What it is |
| --- | --- | --- |
| `ZOOMIES_PROXMOX_TOKEN_SECRET` | - | The token's secret, if you would rather not put it in `ZOOMIES_PROXMOX_TOKEN` |
| `ZOOMIES_PROXMOX_CONTROLLER_URL` | worked out from the route to the cluster | Where a guest reaches this controller |
| `ZOOMIES_PROXMOX_CA_PEM_FILE` | - | The cluster's own CA, for the usual self-signed certificate |
| `ZOOMIES_PROXMOX_INSECURE` | unset | Do not verify the cluster's certificate at all |
| `ZOOMIES_PROXMOX_BRIDGE` | `vmbr0` | The bridge each machine's network card joins |
| `ZOOMIES_PROXMOX_POOL` | - | A Proxmox resource pool to put the machines in |
| `ZOOMIES_PROXMOX_CPUS` | `2` | Cores per machine |
| `ZOOMIES_PROXMOX_MEMORY_MB` | `2048` | Memory per machine |
| `ZOOMIES_PROXMOX_DISK_MB` | `0` (the template's) | Grow each clone's disk by this much |
| `ZOOMIES_PROXMOX_BACKEND` | `docker` | How the guest runs runners: `docker`, `podman` or `process` |
| `ZOOMIES_PROXMOX_BROKEN_TEMPLATE` | - | A template with no guest agent, to induce the bootstrap failure honestly |
| `ZOOMIES_PROXMOX_WORKFLOW` | `zoomies-e2e.yml` | The workflow the cycles trigger |
| `ZOOMIES_PROXMOX_LEDGER_DIR` | `~/.zoomies/proxmox-ledgers` | Where the ledgers go |
| `ZOOMIES_PROXMOX_RECORD` | `roadmap/validation/proxmox-qualification-<commit>-<run>.md` | Where the evidence goes |
| `ZOOMIES_PROXMOX_REQUIRED` | unset | Make a missing prerequisite a failure rather than a skip |

## Skipped, blocked, or run

Without the six cluster variables the harness skips itself and says which are
missing. That is the right answer on a laptop and the wrong one on the machine
someone is qualifying a cluster from, where a skip looks exactly like a pass:
set `ZOOMIES_PROXMOX_REQUIRED=1` there and a missing prerequisite fails instead.

Nothing is created until every prerequisite holds, including the read-only ones
against the cluster itself; the node exists, the storage takes disk images, the
template is a template, the range is free. A run that is going to fail for a
reason a person can fix should fail before it has built anything.

## Nothing is created before it is written down

Every resource is written to a ledger *before* the call that could create it,
which is the same discipline the `machines` row has and exists for the same
reason: a timeout is not evidence that creation failed. A harness killed
between the clone and the delete (by `go test`'s deadline, by a lost
connection, by somebody's Ctrl-C) still leaves a file naming the cluster, the
node, the storage, the range and every VMID it had claimed.

The ledgers live outside the test's temporary directory, in
`~/.zoomies/proxmox-ledgers` by default, precisely so that they outlive the run.

## Cleaning up

The harness reconciles at the end and fails on any owned resource it cannot
account for. When it dies before it gets there, the ledgers are what to read,
and `verify` is what reads them:

```sh
go run ./test/e2e/proxmox/verify
```

It lists every VM in the range and every disk on the storage, accounts for each
against the ledgers, and exits non-zero if anything owned is left unexplained.
It takes the same settings from the environment, or from flags
(`-url`, `-token`, `-node`, `-storage`, `-range`, `-ledgers`, `-json`); it is a
separate program so that the question "did it leave anything on the cluster" is
answerable by something that is still alive.

It deletes nothing. A harness that cleaned up automatically would be a harness
that could delete a machine it merely believed it owned, which is the one
mistake this whole design is built to make impossible.

## Timeouts

The waits live in `budget.go`, with no build tag, and `budget_test.go` checks
them against the Makefile on every ordinary `go test ./...`. A harness whose
waits total more than its own timeout could never reach its last assertion, and
here the last assertion is the reconciliation; the step that decides
qualification, and the one that finds what a shorter run would have left behind.

The harness also reads `go test`'s real deadline before each cycle and stops
early, reconciling, when what is left will not fit another one plus the cleanup.
