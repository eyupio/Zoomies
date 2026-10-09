---
icon: material/server-outline
title: Renting runner hosts from Proxmox VE
description: >-
  Let Zoomies clone a VM template on your Proxmox VE cluster when a pool has
  queued work, and destroy the machine afterwards, with scoped API credentials.
---

# Proxmox VE

Zoomies can rent its own hosts. When a pool has queued jobs and no host in the
fleet can run them, a **provider** clones a prepared VM template on your Proxmox
VE cluster, installs the Zoomies agent inside it, runs the work, and destroys
the machine again once it has been idle long enough.

The machine is an ordinary Zoomies host. It appears on the Hosts page, runs the
same runner backend, and is drained the same way. What is different is that
Zoomies created it, knows it did, and is the only thing allowed to delete it.

!!! warning "What is qualified, and what is not"

    One combination is supported: a **Linux cloud image with `qemu-guest-agent`
    and the Zoomies agent preinstalled**, running the **Docker** runner backend,
    cloned from a template you prepared. That is the combination the
    qualification harness at the end of this page is written for, and the
    harness has not yet been run against a cluster, so nothing on this page
    has been tested end to end: the lifecycle logic and the wire format are
    proved against fixtures, and a hypervisor is not. Other operating systems,
    other backends and other bootstrap routes are untested too, not "probably
    fine", and this page will say so until they are not.

    Renting machines is off until you turn it on: `provider.enabled` defaults to
    `false`, and `provider.max_machines` defaults to **zero, which rents
    nothing**; a maximum of none is none, exactly as it is for a pool's
    `max_runners`. Set both in the same edit, or the fleet will look enabled and
    buy nothing. That is deliberate: the alternative reading, where an unset
    number means "as many as it takes", puts the one setting that decides the
    size of an invoice behind a value somebody can forget.

## What you need

| | |
| --- | --- |
| Proxmox VE | 8.0 or later; automatic setup uses Tailcat to reach the local HTTPS API on port 8006 |
| A prepared template | a Linux VM template, described [below](#preparing-the-template) |
| An API token | created automatically by the setup command, or [managed manually](#the-api-token) |
| A VMID range | a block of VM identifiers Zoomies may use and nothing else may |
| Storage and a bridge | the storage the clone's disk lands on, and the network bridge it attaches to |

Run the setup command once as root on the Proxmox node. After setup, Zoomies
uses the Proxmox API over the private gateway; it needs no SSH connection or
root password. See [the provider wizard](#the-provider) for automatic setup.

## The API token

Automatic setup creates this for you. The following steps are for
**Configure connection manually**.

Create a dedicated user and an API token for it. Do not reuse `root@pam`: the
whole point of the privilege list below is that a mistake in Zoomies, or a
compromise of the controller, cannot reach beyond the VMs it rents.

In the Proxmox UI, **Datacenter → Permissions → Users** to add
`zoomies@pve`, then **API Tokens** to add a token to it. Leave *Privilege
Separation* on; the token then has only the privileges you grant it
explicitly, which is what the next step does.

Grant the token these privileges, on these paths:

| Privilege | Path | What it is for |
| --- | --- | --- |
| `VM.Clone` | the template's `/vms/<template vmid>` | cloning the template |
| `VM.Allocate` | `/vms` | creating and destroying the clones |
| `VM.Audit` | `/vms` | inspecting a machine, and the ownership sweep |
| `VM.Config.Disk`, `VM.Config.CPU`, `VM.Config.Memory`, `VM.Config.Network`, `VM.Config.Options` | `/vms` | sizing the clone and stamping ownership on it |
| `VM.PowerMgmt` | `/vms` | starting and shutting down |
| `VM.GuestAgent.Unrestricted` | `/vms` | installing the agent inside the guest, and reading back what it said if that failed |
| `Datastore.AllocateSpace` | `/storage/<your storage>` | the clone's disk |

`VM.GuestAgent.Unrestricted` is the one worth pausing over: it lets the token
write files into, and run commands inside, any VM on those paths. That is how
the enrolment credential reaches the guest without ever being written into VM
metadata, and it is the reason to scope the token to a resource pool containing
only Zoomies' own VMs if your cluster runs anything else you care about.

Zoomies checks all of this before it creates anything. **Providers → your
provider → Check** asks the token what privileges it holds (Proxmox lets a
token read its own permissions) and names each missing one rather than
failing at the first clone. Run it after any change to the token.

Paste the token into the provider form as
`user@realm!tokenid=secret`, exactly as Proxmox printed it. It is sealed with
the instance encryption key before it reaches the database, never appears in
the API, an audit row, a diagnostics bundle or a log line, and never reaches a
guest. See [Security](security.md).

### TLS

A fresh Proxmox cluster serves its own certificate, signed by the cluster's own
CA. Paste that CA, `/etc/pve/pve-root-ca.pem` on any node, into the
provider's **CA certificate** field and the connection is verified against it.

Take the certificate, not the key: `pve-root-ca.key` next to it is the thing
that signs certificates, and Zoomies will tell you if you paste it by mistake.

The alternative, `insecure_skip_verify`, turns off verification entirely and is
a warning on the provider and in the problems drawer for as long as it is on.
Anybody who can get between the controller and the hypervisor can then read the
API token in flight. It exists for a first ten minutes, not for a deployment.

## Preparing the template

The template is the part you own. Zoomies clones it and installs nothing that
is not already there, so what is in the image is what a runner gets.

Proxmox ships no templates, and the standard answer is a distribution's cloud
image: a disk image with cloud-init in it, published for exactly this purpose.
Start from Ubuntu 24.04 LTS (it is what the harness is written for) and, in one VM
you then convert to a template:

1. **Install the Docker engine**, or whichever runner backend the machines will
   offer, and make sure the service starts at boot.
2. **Install `qemu-guest-agent`** and enable it. This is how Zoomies reaches
   inside the guest to enrol it, and a template without it is a machine that
   boots, costs money and never joins. Set `agent: enabled=1` on the VM.
3. **Install the Zoomies agent** and its systemd unit, joined to nothing:
   `zoomies agent install` writes the unit so that it reads the enrolment file
   the controller drops into each clone, and leaves it **disabled**. Zoomies
   enables it inside a clone once the clone has a credential of its own.
4. **Remove any agent state.** `zoomies agent install` refuses to run on a
   machine that has already joined, because `/var/lib/zoomies/work/agent.json`
   holds one host's identity; if you are converting a host that once joined,
   delete that file first.
5. Shut the VM down and **convert it to a template**.

The same recipe as commands, on a node as root; the wizard's *Before you
start* panel carries them too, with a copy button:

```sh
wget https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
qm create 9000 --name zoomies-template --memory 4096 --cores 2 --cpu host --ostype l26 \
  --net0 virtio,bridge=vmbr0 --scsihw virtio-scsi-pci --agent enabled=1 --serial0 socket --vga serial0
qm set 9000 --scsi0 local-lvm:0,import-from=$PWD/noble-server-cloudimg-amd64.img --boot order=scsi0
qm set 9000 --ide2 local-lvm:cloudinit --ciuser ubuntu --sshkeys ~/.ssh/id_ed25519.pub --ipconfig0 ip=dhcp
qm resize 9000 scsi0 32G && qm start 9000
```

`local-lvm` is the storage name on a default install. If yours has another (the
Datacenter tree lists them), use that name in both `qm set` lines, and make sure
it holds **Disk image** content. If the `scsi0` step fails the VM is still
created, with no disk, and it will boot to a network prompt and nothing else;
the provider's **Check** reports that as `proxmox.template_no_disk`.

Then, inside the guest over SSH:

```sh
curl -fsSL https://get.docker.com | sh && sudo apt-get install -y qemu-guest-agent
curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init && sudo zoomies agent install
sudo cloud-init clean && sudo poweroff
```

And back on the node, `qm template 9000`. Give the template a VMID outside the
block you hand to Zoomies: it must never be one of the identifiers a clone can
be allocated.

!!! danger "Never leave an enrolled agent in the template"

    `agent.json` holds one host's identity. A template containing one clones
    that identity into every machine made from it, and two agents then share a
    host row and split its tasks between them, which shows up as work
    vanishing, not as an error.

    Nothing can check this for you, and it is worth being plain about why: the
    template is powered off, so its guest agent is not running and no API call
    can read a file inside it. Zoomies sees the *consequence* (it raises a
    problem when a host's agent session alternates between two identities) but
    by then you are debugging vanishing work rather than preparing an image.
    Check it before you convert the VM.

Zoomies never installs an operating system, applies updates, or reboots a
machine it rents. If the image needs patching, rebuild the template: machines
are disposable, which is what makes that cheap.

## The provider

**Providers → Add provider → Proxmox VE → Generate setup command**.
Copy the command and run it as root on your Proxmox host. Return to the wizard;
the connection appears automatically. You do not need to enter a name, API token,
certificate, endpoint or Tailcat address.

The command downloads and verifies a separate Zoomies binary, detects the local
API certificate, creates a dedicated Proxmox user and privilege-separated token,
and installs a persistent Tailcat gateway to the local API on port 8006. It sends
the connection back over the controller's private Tailcat listener using a
connection-bound capability valid for one hour. Credentials and gateway addresses are
sealed in the controller database and never returned to the browser.

An existing Zoomies runner agent keeps its binary, service, state and registration.
The provider has its own service, `zoomies-proxmox-<controller-key>.service`, and
state under `/var/lib/zoomies-proxmox/<controller-key>`. Running a new setup command
for the same controller reuses the saved token and gateway identity. It does not
join the machine as another runner host. Interrupted setup can be retried; a lost
controller response is acknowledged when the identical connection is sent again.
After success, a root-only local receipt lets the original command be run again
even after its enrolment capability expires, without downloading an older binary.

`sudo zoomies upgrade` on a runner host also updates and restarts its dedicated
Proxmox gateways, preserving API credentials and Tailcat identities. A provider-only
host can run `sudo /var/lib/zoomies-proxmox/<controller-key>/zoomies upgrade`.
`--check` previews these updates without changing gateway files or services.
Controller upgrades do not remotely upgrade a Proxmox host.

For an existing gateway, choose **Use an existing Tailcat gateway** on the Connect
step and enter its private address. **Configure a direct connection** remains
available for an API address Zoomies can reach.

The controller needs private connections enabled. The Proxmox host needs outbound
internet access and systemd. The setup does not prepare a runner VM template:
choose the prepared template, storage and network on the Placement step. Choices
come from the cluster; anything with one answer is already selected. Review the
machine shape and limits, then save. The initial maximum is zero, so connecting a
provider does not create any VMs. The setup callback also reports existing QEMU
templates, with their VMIDs and nodes. A sole template is preselected; with several,
choose the runner template on Placement. The script prints these identifiers at
completion. If none exists, prepare a runner template first; setup does not create one.

For a direct network connection or credentials you manage yourself, choose
**Configure connection manually**. The tables and terminal command below describe
that alternative.

| Setting | What it means |
| --- | --- |
| Endpoint | `https://pve.example.com:8006`. Plain `http://` is refused: the API token would cross in the clear. |
| Connection | Direct, or private through a `zoomies gateway` running beside the cluster when it lives on a network the controller cannot reach; a home lab behind a router, typically. See [Private providers](private-hosts.md#private-providers). |
| Nodes | Which nodes clones may be made on. Give more than one and machines are spread across them, each new clone going to the least loaded. |
| Template VMID | The template prepared above. The preflight checks it exists and is a template. |
| Storage | Where the clone's disk lands. Must accept disk images. |
| Bridge | The network bridge the machine attaches to. It must reach the controller. |
| VMID range | The block of identifiers Zoomies may allocate from, and nothing else may. |
| Machine shape | CPUs, memory and disk for each machine, and the labels, capacity and backend the host will report. |
| Maximum machines | How many this provider may run at once. |

The same form is one line in a terminal, for a machine with no browser or a
setup somebody wants to keep in a script:

```sh
zoomies providers add proxmox --name proxmox-lab \
  --endpoint https://pve.example.com:8006 --endpoint-ca-file pve-root-ca.pem \
  --nodes pve1 --template 9000 --storage local-lvm --bridge vmbr0 \
  --vmid-range 9000-9099 --max-machines 4
```

It asks for the API token without echoing it (or reads it from standard
input, or from `--credential-file`) checks the answers before saving anything,
and then runs the same check the **Check** button does, so the first thing it
prints after "created" is what the cluster would refuse. The nodes, storage and
bridge have to be typed rather than picked from a list, because the list comes
from the credential and the credential is not saved until the provider is;
`zoomies providers check` names anything the cluster does not recognise.
`zoomies providers edit proxmox-lab --max-machines 8` raises the ceiling later,
and `--setting key=value` reaches the advanced settings the form keeps behind a
disclosure; `zoomies providers kinds` lists them. See [the CLI](cli.md#zoomies-providers).

A cluster on a home network usually has no address a controller in the cloud
can reach. Run `zoomies gateway --target <node-ip>:8006` on the node, or on any
machine beside it, choose **Private connection** in the form and paste the
address it prints. The endpoint stays `https://…:8006` and the certificate is
still verified end to end: the gateway forwards bytes and reads none of them.

The **VMID range** is worth setting deliberately. Zoomies allocates the lowest
free identifier inside it, so the range is both a budget and a blast radius: a
machine outside it is, by construction, not one of ours. Give Zoomies a block
nothing else uses (`9000–9099`, say) and keep it out of whatever your other
tooling allocates from.

A machine's shape is one shape per provider. If you want two sizes, make two
providers: a pool asks for the machine it fits, and having one row mean two
different machines makes the accounting ambiguous in exactly the place it has
to be exact.

## What happens when a pool runs out of hosts

```mermaid
sequenceDiagram
    autonumber
    participant P as pool with queued jobs
    participant Z as Zoomies controller
    participant PVE as Proxmox VE
    participant M as the machine

    P->>Z: 3 jobs queued, no host can run them
    Z->>Z: write the machine row -- identity first, always
    Z->>PVE: clone the template into the allocated VMID
    PVE-->>Z: UPID -- the task to follow
    Z->>PVE: size it, stamp ownership, start it
    Z->>PVE: wait for the guest agent to answer
    Z->>M: write the enrolment file, enable the agent
    M->>Z: join, using a token good for this machine alone
    Z->>Z: the machine is a host; the scheduler places runners
    Note over M: the jobs run
    Z->>Z: no runners for 15 minutes, sustained
    Z->>M: drain the host
    Z->>PVE: verify ownership, then delete
    Z->>PVE: inspect until the VM is really gone
```

Two of those steps are the whole design and are worth stating plainly:

* **The row exists before the VM does.** Zoomies writes the machine's identity
  (which node, which VMID, which name) before it asks Proxmox for anything.
  When a clone request times out, that is not evidence the clone did not happen,
  and the answer is to go and look for the identity we already chose rather than
  to ask for a second machine. A controller killed at any point in the sequence
  resumes by looking, never by creating.
* **A delete needs four agreeing facts.** The row, a fresh inspection of the VM,
  this controller holding the fleet's lease, and a state that permits it. If the
  VM at that VMID does not carry the ownership record this machine stamped on
  it, nothing is deleted and the machine is **quarantined** for a person to look
  at. A recycled VMID is a realistic accident; destroying somebody else's VM
  because of one is not a realistic mistake to recover from.

## Day to day

**Pausing.** The Hosts page has a switch that stops new machines being created.
Drains, deletions, recovery and ownership checks all continue; a pause that
stopped those too would leave VMs running with nothing tending them. It is a
row, so it survives a restart, and it is audited.

**A machine that will not come up.** Its page carries the failure in the words
whatever refused it used: Proxmox's own task error for a clone, and the guest's
own standard error for a bootstrap. Three failures in a row stand the provider
down for a widening interval, so a bad template costs a few machines rather
than fifty.

**Orphan review.** **Providers → your provider → Orphans** has three lists:

* *Untracked resources*: VMs wearing Zoomies' name or marks with no row behind
  them. Zoomies never deletes one. They are what a database restored from
  backup, or a row pruned too early, leaves behind, and they are real machines
  costing real money, so they are shown until somebody deals with them.
* *Machines with no resource*: rows whose VM has gone. Usually somebody deleted
  it in the Proxmox console.
* *Ownership unverified*: the quarantine. A row and a VM that disagree about
  who owns what.

For a quarantined machine, **Release** forgets the row without touching the VM.
That is deliberately the only escape hatch: if the two disagree, Zoomies is the
one that should stand down, and deleting the VM is a decision for whoever can
see both sides.

## Recovery

**A controller restart** needs nothing. The machine loop reads the rows, asks
Proxmox about every operation still in flight, and carries on. There is no
separate startup path, because a restart is simply the pass where nothing was
in memory.

**A restored database** is treated as possibly describing somebody else's
machines, because it might: two controllers restored from one backup would
both believe they own the same VMs. Restoring sets the fence, and while it is
set Zoomies will not create or delete anything at all, though it keeps
inspecting and reporting. Lifting the fence marks every machine's ownership
unverified, and each one has to be re-proved against the hypervisor before it
can be acted on. See [Backup and restore](backup-and-restore.md).

**A hypervisor restart** looks like a provider that cannot be reached, which is
never evidence about a resource: nothing is created, failed or deleted on the
strength of it. When the cluster comes back the sweep reconciles what is
actually there against the rows.

## Qualifying your own cluster

Fixture tests prove the logic; they prove nothing about your cluster, and so
far nothing about anyone's. The procedure below is implemented as a harness,
`make test-e2e-proxmox`, that has not yet been run against a cluster, and
[the record it fills in](https://github.com/eyupio/zoomies/blob/main/roadmap/validation/proxmox-qualification.md)
says `not run` on every row. Before this is load-bearing for you, run it against
**disposable** resources and record what happened, and if you do, send the
rows it wrote, because a run on a real cluster is the evidence this page is
waiting for.

1. Twenty full cycles: create, enrol, run a real workflow job, drain, delete.
2. Scale from zero: no hosts at all, then queued work.
3. A burst across two pools at once.
4. A controller restart in the middle of a create.
5. A deliberately broken template, to see the bootstrap failure reported.
6. A deletion that fails once, to see it retried and confirmed.
7. Finally, reconcile the VM and storage inventory against the machines table.
   **No owned resource may be left unexplained.**

Record the Proxmox version, the template, the limits, the timings and the
cleanup evidence. A cycle that worked without evidence is a cycle nobody else
can check.
