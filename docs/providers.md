---
icon: material/cloud-sync-outline
title: The infrastructure provider contract
description: >-
  What Zoomies asks an infrastructure provider to do, what it promises in
  return, and the rules a second provider must obey: failures, handles,
  ownership.
---

# The provider contract

An **infrastructure provider** rents machines. Zoomies asks one for a machine
when a pool has queued work and no host can run it, installs an agent inside
what comes back, and asks for the machine to be destroyed when it is no longer
needed.

This page is for whoever writes one. It is the other side of
[Proxmox VE](proxmox.md), which is for whoever operates the one that exists.

The contract is deliberately small. A provider performs **infrastructure
operations** and nothing else: it does not decide how many machines should
exist, when to give up, how long to wait, or what to delete. One reconciler in
the controller owns all of that, for every provider, so that a second provider
is a new package rather than a new branch in the controller.

## What a provider implements

```go
type Provider interface {
	Kind() store.ProviderKind
	Capabilities() Capabilities

	Preflight(ctx context.Context) Report
	Allocate(ctx context.Context, spec MachineSpec) (MachineRef, error)
	Create(ctx context.Context, ref MachineRef, spec MachineSpec) (OperationRef, error)
	Inspect(ctx context.Context, ref MachineRef) (Machine, error)
	List(ctx context.Context, owner Owner) ([]Machine, error)
	Delete(ctx context.Context, ref MachineRef) (OperationRef, error)
	Operation(ctx context.Context, op OperationRef) (OperationStatus, error)
}
```

Three capabilities are optional, and each is a separate one-method interface
found by type assertion rather than a flag: `PowerController` (start and stop
without destroying), `Bootstrapper` (push the enrolment payload into the guest),
and `Discoverer` (list the nodes, storages and networks the credential can
actually see, so the configuration form offers them instead of asking for a
string). A provider that cannot do one of these does not implement it. It never
implements it and returns "unsupported": a method that is always going to refuse
is a method the reconciler will keep calling.

### Allocate is separate from Create on purpose

`Allocate` picks the identity a machine will have — which zone, which native
identifier, which name — and **creates nothing**. Zoomies writes that identity
into its own database before it calls `Create`.

That ordering is the single most important thing in this design, and the whole
reason the two are separate calls. When `Create` times out, or the connection
drops after the request went out, the machine may or may not exist. Zoomies then
has a name and an identifier it chose in advance, so the answer is to go and
look — `Inspect`, then `List` — rather than to ask for a second machine.

**A timeout is not evidence that creation failed.** A provider that makes this
untrue — by allocating identity only after creation, with no way to find the
result again — cannot be made safe by the reconciler. If your identity is only
knowable after the fact, return a ref carrying `Name` alone from `Allocate` and
make that name findable through `List`.

`Create` must also be idempotent for one ref: calling it twice for the same
`MachineRef` must not build two machines.

## Failure categories

Every error a provider returns carries a category, because the reconciler's next
move is different for each one. Returning a bare error means `internal`, which
retries nothing.

| Kind | What it means | What the reconciler does |
| --- | --- | --- |
| `config` | A setting is wrong. | Fails the machine. Nothing but an edit fixes it. |
| `auth` | The credential was rejected. | Fails, and stands the provider down for creates. |
| `permission` | The credential is valid and not allowed to do this. | Fails, naming the privilege and the path. |
| `unreachable` | We could not talk to the provider. | Backs off. **Never** evidence about a resource. |
| `quota` | Refused for want of resources or a limit. | Retries slowly; never by asking for more. |
| `conflict` | Something else holds the resource — a lock, an identifier taken. | Waits and observes. |
| `not_found` | The resource is not there. | For a delete, that is success. |
| `refused` | A definite refusal, in the provider's own words. | Retries with backoff, then fails. |
| `ambiguous` | **We do not know whether it happened.** | Resolves it by looking. Never retries the operation. |
| `internal` | Anything unclassified, including a category from a newer contract. | Retries nothing, and asks for a person. |

`permission` is kept apart from `auth` because the advice differs: one is "this
token is wrong", the other is "this token is right and lacks
`VM.Clone` on `/vms/9000`", and an operator sent to the wrong one of those
loses an afternoon.

### The classification that decides everything

Only the transport knows whether a deadline or a reset happened **before or
after the request body went out**. Before it, nothing can have happened:
`unreachable`. After it, the provider may have done the work and lost the
answer: `ambiguous`.

Get this wrong in the safe direction and a machine waits. Get it wrong in the
unsafe direction and you rent two machines and pay for the one nobody is
tracking. Classify at the point where the answer is known — inside the HTTP
client — and never above it.

An `ambiguous` failure is **never retryable**. Its only legal next move is
observation.

## Operations are durable handles

`Create`, `Delete` and the optional power operations return an `OperationRef`:
an opaque handle the provider can be asked about later, through `Operation`.

It has to survive a controller restart, because that is what it is for. Zoomies
stores the handle on the machine row before it considers the call done, and a
controller that comes back holding nothing but that handle asks about it rather
than acting again. Proxmox's UPID is a good shape: it encodes its own node, so
the handle alone is enough to follow the task.

A provider whose operations are synchronous returns a zero `OperationRef` and
sets `AsyncOperations: false` in its capabilities.

## Ownership

Zoomies marks the resources it creates, and verifies those marks before it
deletes anything. `Owner` carries the controller's identity, the provider's, the
machine's, a per-machine fingerprint minted with the row, and a creation time.

None of it is authenticated. Anyone with write access to your infrastructure can
forge the lot, and the contract says so out loud: **the marks are
tamper-evidence, not authorisation.** The database is the authority. What the
marks catch is the realistic accident — a recycled identifier, another
controller's resource, one somebody made by hand — rather than an attacker.

A delete therefore needs four facts to agree: the row, a **fresh** inspection
(a cached sweep result is not evidence), this controller holding the fleet's
lease, and a state that permits it. Any disagreement quarantines the machine for
a person to look at. Nothing is deleted on a guess, and an untracked resource
wearing our marks is reported and never removed — it might be another live
fleet's.

`List` must return both the machines carrying this owner's controller mark and
any carrying our naming grammar with somebody else's mark. The caller needs both
to tell an orphan from a foreign resource, and it must be cheap: one API call
where the API allows one.

## Deadlines

Every method takes a context and must honour its deadline. A provider declares
its own honest budgets in `Capabilities().Deadlines`, and the reconciler applies
the larger of those and the operator's configuration — so a provider may ask for
longer, and never for less supervision than the operator asked for.

A provider does not retry, back off, or read a clock for its own decisions.
Those belong to the caller, as they do for the GitHub client.

## Versioning

`ContractVersion` is one number. Adding a field, a failure category or a method
to an optional interface never raises it; a change that would make a provider
written for the previous version behave *wrongly* does.

A provider declares the range it speaks. The controller refuses to build one
whose range does not contain this build's version, names both numbers, and says
which side to upgrade — and the machines that provider already owns stay
visible, drainable and deletable, because a version mismatch must never strand a
running VM.

Two forward-compatibility rules follow from the same instinct, and both fail
safe: an unknown failure category reads as `internal`, which retries nothing;
and an unknown machine phase reads as `unknown`, which never authorises a
delete.

Exactly one version is supported, and every provider is in the Zoomies tree.
Accepting a range of versions is a decision for the day an out-of-tree provider
exists; it was considered and deliberately deferred, because a plugin ABI is a
compatibility promise that is much harder to withdraw than to make.

## Writing one

`RunContractTests(t, name, open)` is exported from the provider package and
exercises every rule on this page against any implementation. A new provider's
test file starts with one line calling it, and the suite is the definition of
done:

```go
func TestTheProviderObeysTheContract(t *testing.T) {
	provider.RunContractTests(t, "example", func(t *testing.T) provider.Provider {
		return newExampleProvider(t)
	})
}
```

`provider.NewFake()` is an in-memory provider that obeys the same rules, with
knobs for the cases that are hard to reach against real infrastructure: an
operation that does the work and then reports an unknown outcome, one that
reports an unknown outcome having done nothing, a quota that runs out, a
resource that vanishes underneath you, and a foreign resource wearing your
naming grammar. The whole reconciler is tested against it, with no network.

Two tests keep the contract honest in the other direction: one asserts the
package imports nothing beyond the domain types, and one fails on any identifier
or comment in it that names a specific provider. A contract that has learned the
word `vmid` has stopped being one.

A provider that opens its own connections has one more rule to keep. `Config`
carries a `DialContext`, set when the operator reached the provider through a
[private connection](private-hosts.md#private-providers), and every socket the
provider opens has to go through it, with no proxy in between: the endpoint's
host is then only the name TLS verifies the certificate against, and a provider
that dialled it directly would report a hypervisor down that is merely at home.
The Proxmox client does this in one place, in its `http.Transport`, which is
where a new provider should do it too.
