# Proxmox VE qualification

**Status: not run. Nothing on this page is evidence yet.**

This is the record ZF-214b's acceptance asks for, written before the runs
rather than after, so that the procedure is fixed in advance and a later result
cannot be a description of whatever happened to work. Every row below is
`not run` until somebody with a disposable Proxmox cluster fills it in, and the
roadmap's own sentence applies until then: **fixture success is not live
qualification.**

What *has* been run is the fixture suite; the whole machine lifecycle against
`provider.Fake` with no network, and the Proxmox client against an
`httptest` server speaking `/api2/json`. That proves the logic and the wire
format. It proves nothing whatever about a hypervisor.

## What this needs before it can be run

| | |
| --- | --- |
| A cluster | Proxmox VE 8.0 or later, **disposable**, whose VMs nobody minds losing |
| A VMID range | a block reserved for this and nothing else |
| An API token | scoped as [docs/proxmox.md](../../docs/proxmox.md#the-api-token) lists |
| A template | prepared as [docs/proxmox.md](../../docs/proxmox.md#preparing-the-template) describes |
| A GitHub installation | able to queue real jobs against a test repository |

The harness lives in `test/e2e/proxmox/` behind the `e2e` build tag and skips
itself when the credentials are absent, which is everywhere they have not been
deliberately supplied. Run it with `make test-e2e-proxmox`; its README lists
every setting, and set `ZOOMIES_PROXMOX_REQUIRED=1` when running it in earnest,
because on the machine somebody is qualifying a cluster from a skip looks
exactly like a pass.

It writes its evidence into a copy of this page named for the commit and the
run, so the figures below are filled in from a file the harness produced rather
than typed from memory, and it writes every resource it is about to create to
a ledger first, so a run that dies part-way still names what it left behind.

## Setup, to be recorded

Fill these in from the run itself, not from the plan. A number without the
setup that produced it is not evidence.

| | |
| --- | --- |
| Zoomies commit | - |
| Proxmox VE version | (`pveversion -v`, verbatim) |
| Node(s) | - |
| Template VMID, OS and image | - |
| Storage and its type | - |
| Network bridge | - |
| VMID range | - |
| Machine shape | cpus / memory / disk |
| Fleet and provider limits | - |
| Runner backend in the guest | - |

## The runs

Twenty create → enrol → run a real job → drain → delete cycles, plus the six
cases the roadmap names. Record every timing with its denominator: "p95 of 20"
means nothing written as "usually about".

| # | Case | Outcome | Timings | What was observed | Human action needed |
| --- | --- | --- | --- | --- | --- |
| 1 | 20 full cycles | `not run` | - | - | - |
| 2 | Scale from zero; no hosts at all, then queued work | `not run` | - | - | - |
| 3 | Multi-pool burst, two pools demanding at once | `not run` | - | - | - |
| 4 | Controller restart mid-create | `not run` | - | - | - |
| 5 | Bootstrap failure; a deliberately broken template | `not run` | - | - | - |
| 6 | Deletion retry; a delete that fails once | `not run` | - | - | - |
| 7 | Closing inventory reconciliation | `not run` | - | - | - |

Timings to record across the 20 cycles, each with p50 and p95 and the count
they came from: queue to create issued; create to resource running; running to
guest agent answering; bootstrap to host joined; host joined to first job
started; drain requested to last runner finished; delete issued to resource
confirmed gone.

## The closing reconciliation

This is the one that decides qualification, and it is deliberately last.

After the runs, list every VM and every disk in the configured VMID range and
on the configured storage, and account for each one against the `machines`
table. **No owned resource may be left unexplained.** Record the query used,
the counts on both sides, and each discrepancy with its explanation; an
orphan found and deliberately left is a finding, not a failure, but an orphan
nobody noticed is a failure of this procedure rather than of the code.

| | Count | Notes |
| --- | --- | --- |
| VMs in the range at the start | - | - |
| Machines created during the runs | - | - |
| Machines confirmed deleted | - | - |
| VMs in the range at the end | - | - |
| Disks left on the storage | - | - |
| Unexplained owned resources | - | **must be zero** |

## What is deliberately not covered

* **Any other template, operating system or runner backend.** One combination is
  qualified by this procedure, and [docs/proxmox.md](../../docs/proxmox.md) says
  so rather than implying the rest are fine.
* **A second provider.** ZF-214c is separate work and is not started.
* **Sustained or long-duration operation.** These are cycles, not a soak test.
  A fleet left renting machines for a week is a different question.
* **Cost.** The cost figures Zoomies shows are an operator-supplied rate
  multiplied by time; nothing here validates them against an invoice.
