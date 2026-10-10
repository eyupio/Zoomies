package proxmox

// The provider itself: everything internal/provider asks of an infrastructure
// provider, spoken to one Proxmox VE cluster.
//
// Three things about the shape of this file are decided by the Proxmox API
// rather than by taste, and are what to understand before changing it.
//
//   - A clone is asynchronous and holds a lock on the new guest until it
//     finishes, so nothing can be sized, tagged or started while it runs.
//     Create therefore does the one thing that can be done in a single call --
//     the clone, carrying the guest's name and its ownership record, both of
//     which the clone endpoint accepts -- and hands back the task handle.
//     Sizing, tagging and starting belong to Start, which runs at the first
//     moment the guest is unlocked.
//   - Start is given a ref and no spec, so the shape a machine was asked for
//     has to survive the gap between the two calls without living in this
//     process's memory: a controller may restart in between, and a machine
//     that came back the template's size rather than the operator's would be
//     wrong in a way nothing would ever report. It travels in the guest's own
//     description, beside the ownership record, and is read back there.
//   - Nothing here waits for a task. Every call returns as soon as the cluster
//     has taken the work, and the UPID it returns is what follows it, because
//     one reconcile step is budgeted for about two requests and a full clone
//     takes minutes. The one exception is the guest agent, which has no task
//     to hand back at all; bootstrap.go says what it does instead.
//
// Nothing in this package decides how many machines should exist, when to give
// up or what may be deleted. That is one reconciler's job for every provider.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
)

// The budgets this provider asks for. They are its own honest estimate of how
// long each piece of work takes on a cluster that is behaving; the controller
// applies the larger of these and the operator's configuration, so these are a
// floor under supervision and never a way around it.
const (
	callBudget      = 30 * time.Second
	allocateBudget  = time.Minute
	createBudget    = 20 * time.Minute
	bootstrapBudget = 10 * time.Minute
	deleteBudget    = 15 * time.Minute
)

// allocateAttempts bounds the re-picking of an identifier somebody took between
// the listing and the assertion. It is small on purpose: a handful of races is
// a busy cluster, and a hundred of them is a range with nothing free in it,
// which is a different answer and has its own finding.
const allocateAttempts = 5

// shutdownGrace is how long a guest is given to shut itself down before the
// power is pulled, on the way to being destroyed. Its host has already been
// drained by the time anything here runs, so nothing is mid-job; the grace is
// for the filesystem, not for the work.
const shutdownGrace = 60 * time.Second

// Provider rents virtual machines from one Proxmox VE cluster.
//
// It holds the credential for the life of the client it built and writes it
// nowhere. Everything else it needs is in the settings the operator answered,
// parsed once here so that a mistake in them is an error when the provider is
// built rather than a failure at the first clone.
type Provider struct {
	client   *Client
	settings settings
	caps     provider.Capabilities
	log      *slog.Logger
}

// NewProvider builds a provider for one stored configuration row.
//
// It never dials: an unreachable cluster is Preflight's answer to give, for the
// reason backend.Probe reports an absent Docker rather than failing to
// construct -- a controller that would not start because a hypervisor was
// rebooting could not even show an operator why.
func NewProvider(cfg provider.Config) (*Provider, error) {
	set, findings := parseSettings(cfg.Settings)
	if err := refuse(findings); err != nil {
		return nil, err
	}
	tokenID, secret, err := splitCredential(set.tokenID, cfg.Credential)
	if err != nil {
		return nil, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	client, err := New(Options{
		Endpoint:    cfg.Endpoint,
		TokenID:     tokenID,
		Secret:      secret,
		CAPEM:       cfg.CAPEM,
		Insecure:    cfg.Insecure,
		DialContext: cfg.DialContext,
		HTTPClient:  cfg.HTTPClient,
		Logger:      log,
	})
	if err != nil {
		return nil, err
	}
	return &Provider{
		client:   client,
		settings: set,
		caps:     capabilities(cfg.Deadlines),
		log:      log.With("component", "provider.proxmox"),
	}, nil
}

func (p *Provider) Kind() store.ProviderKind { return store.ProviderProxmox }

func (p *Provider) Capabilities() provider.Capabilities { return p.caps }

// capabilities is what this provider can do, declared rather than discovered.
//
// The deadlines are the larger of this provider's own and the operator's, kept
// in the order the contract requires: a call may not be budgeted more than the
// allocate it is part of, and an allocate no more than a create.
func capabilities(operator provider.Deadlines) provider.Capabilities {
	d := provider.Deadlines{
		Call:      longest(callBudget, operator.Call),
		Allocate:  longest(allocateBudget, operator.Allocate),
		Create:    longest(createBudget, operator.Create),
		Bootstrap: longest(bootstrapBudget, operator.Bootstrap),
		Delete:    longest(deleteBudget, operator.Delete),
	}
	d.Allocate = longest(d.Allocate, d.Call)
	d.Create = longest(d.Create, d.Allocate)
	return provider.Capabilities{
		MinContract:      provider.ContractVersion,
		MaxContract:      provider.ContractVersion,
		Kind:             store.ProviderProxmox,
		Label:            "Proxmox VE",
		Bootstrap:        provider.BootstrapGuestAgent,
		CanStartStop:     true,
		CanDiscover:      true,
		CanMarkOwnership: true,
		AsyncOperations:  true,
		// No cost unit: a cluster somebody already owns has no per-machine
		// price this integration could honestly quote, which is not the same
		// as a machine being free.
		CostUnit:        "",
		Deadlines:       d,
		EndpointExample: "https://pve.example.com:8006",
		EndpointSource: "The URL you open the Proxmox console at, port 8006 included: https://<any node>:8006. " +
			"HTTPS only; the token would otherwise cross the network in the clear.",
		CredentialSource: "Datacenter -> Permissions -> API Tokens. Add a token to a dedicated user (zoomies@pve) with " +
			"privilege separation on; Proxmox shows the secret once. Paste the whole line, user@realm!tokenid=secret.",
		CASource: "/etc/pve/pve-root-ca.pem on any node -- the certificate, not the .key beside it. " +
			"Leave it empty when the cluster's certificate is signed by a public CA.",
		Guide: guide(),
	}
}

// guide is what has to exist on the cluster before this provider can rent a
// machine. It is served with the kind, so the wizard shows it before its first
// question and the CLI can print it, and the docs page says the same things
// at more length.
//
// Proxmox ships no templates, and the standard answer is a distribution's
// cloud image imported with qm: the commands below are that recipe for the
// one image that has been qualified, and the only step that cannot be a
// command is the one done inside the guest.
func guide() []provider.GuideStep {
	return []provider.GuideStep{
		{
			Title: "A Proxmox VE 8 cluster this controller can reach",
			Detail: "The API listens on port 8006 over HTTPS on every node; https://<node>:8006 is the address " +
				"the form asks for. A cluster on a home network with no route from the controller can be " +
				"reached through `zoomies gateway` instead: run it beside the cluster and choose a private connection.",
		},
		{
			Title: "An API token that can clone, start and delete guests",
			Detail: "Datacenter -> Permissions -> Users: add zoomies@pve. Then API Tokens: add a token to it with " +
				"privilege separation on, and copy the whole line Proxmox prints once, user@realm!tokenid=secret. " +
				"Grant the token VM.Clone on the template, and VM.Allocate, VM.Audit, VM.Config.*, VM.PowerMgmt " +
				"and VM.GuestAgent.Unrestricted on /vms, Datastore.AllocateSpace on the storage, and SDN.Use on " +
				"/sdn/zones/localnetwork so it can see the bridge. The check " +
				"after saving names any privilege that is missing. Or, on any node, as root:",
			Command: `pveum user add zoomies@pve
pveum role add Zoomies -privs "VM.Clone VM.Allocate VM.Audit VM.Config.Disk VM.Config.CPU VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt VM.GuestAgent.Unrestricted Datastore.AllocateSpace SDN.Use"
pveum acl modify /vms -user zoomies@pve -role Zoomies
pveum acl modify /storage/local-lvm -user zoomies@pve -role Zoomies
pveum acl modify /sdn/zones/localnetwork -user zoomies@pve -role Zoomies
pveum user token add zoomies@pve ci --privsep 1`,
		},
		{
			Title: "A template with Docker, the guest agent and the Zoomies agent in it",
			Detail: "Proxmox ships no templates. Start from Ubuntu 24.04's cloud image, which is the one that has " +
				"been qualified: import it as a VM, boot it once, install what a runner needs inside, and convert " +
				"it. Inside the guest: the Docker engine, qemu-guest-agent, and `zoomies agent install`, which " +
				"leaves the agent's service disabled for this controller to enable in each clone. Never leave an " +
				"enrolled agent (/var/lib/zoomies/work/agent.json) in the template. On the node, as root:",
			Command: `wget https://cloud-images.ubuntu.com/noble/current/noble-server-cloudimg-amd64.img
qm create 9000 --name zoomies-template --memory 4096 --cores 2 --cpu host --ostype l26 \
  --net0 virtio,bridge=vmbr0 --scsihw virtio-scsi-pci --agent enabled=1 --serial0 socket --vga serial0
qm set 9000 --scsi0 local-lvm:0,import-from=$PWD/noble-server-cloudimg-amd64.img --boot order=scsi0
qm set 9000 --ide2 local-lvm:cloudinit --ciuser ubuntu --sshkeys ~/.ssh/id_ed25519.pub --ipconfig0 ip=dhcp
qm resize 9000 scsi0 32G && qm start 9000
# inside the guest, over ssh:
#   curl -fsSL https://get.docker.com | sh && sudo apt-get install -y qemu-guest-agent
#   curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init && sudo zoomies agent install
#   sudo cloud-init clean && sudo poweroff
qm template 9000`,
		},
		{
			Title: "A block of VMIDs nothing else allocates from",
			Detail: "Zoomies takes the lowest free identifier inside the block, so it is both a budget and a blast " +
				"radius: a guest outside it is, by construction, not one of ours. 9000-9099 is the default; keep it " +
				"out of whatever your other tooling numbers from, and out of the template's own VMID.",
		},
	}
}

func longest(a, b time.Duration) time.Duration {
	if b > a {
		return b
	}
	return a
}

// Preflight checks configuration, credentials and prerequisites, changing
// nothing and never failing: what it reports is what an operator must fix.
func (p *Provider) Preflight(ctx context.Context) provider.Report {
	return Preflight(ctx, p.client, p.settings.prereqs())
}

// Discover lists what this credential can actually see, for the guided form.
func (p *Provider) Discover(ctx context.Context) (provider.Discovery, error) {
	return Discover(ctx, p.client)
}

// ---------------------------------------------------------------------------
// Identity
// ---------------------------------------------------------------------------

// Allocate picks the node and the VMID a machine will have, and creates
// nothing. The caller writes what this returns down before anything can exist
// to be lost, which is what makes a create whose answer never came back
// something to look for rather than something to do again.
//
// The identifier is the lowest free one inside the configured range, computed
// from the cluster's own listing and then asserted free. Proxmox's "next free
// id" endpoint is not used to pick: it knows about the whole cluster and
// nothing about the range an operator allowed this provider, so a fleet driven
// by it would take identifiers somebody else's automation is entitled to. Its
// assertion form is used as the last check before the clone, and a refusal
// there means somebody won the race for that identifier -- a re-pick, not a
// failure.
func (p *Provider) Allocate(ctx context.Context, spec provider.MachineSpec) (provider.MachineRef, error) {
	guests, err := p.client.ClusterVMs(ctx)
	if err != nil {
		return provider.MachineRef{}, err
	}
	node, err := p.placeOn(guests)
	if err != nil {
		return provider.MachineRef{}, err
	}
	taken := make([]int, 0, len(guests))
	for _, g := range guests {
		taken = append(taken, g.VMID.Int())
	}

	for range allocateAttempts {
		vmid, err := NextFreeVMID(taken, p.settings.vmidMin, p.settings.vmidMax)
		if err != nil {
			return provider.MachineRef{}, err
		}
		err = p.client.AssertVMIDFree(ctx, vmid)
		switch provider.KindOf(err) {
		case "":
			return provider.MachineRef{Zone: node, ID: strconv.Itoa(vmid), Name: spec.Name}, nil
		case provider.FailureConflict, provider.FailureConfig:
			// Somebody took it between the listing and now. Proxmox reports
			// that from two places -- its parameter check and the cluster's
			// own -- so both are read as "taken" and the next identifier is
			// tried; anything else, a missing privilege above all, is the
			// caller's to see.
			taken = append(taken, vmid)
		default:
			return provider.MachineRef{}, err
		}
	}
	return provider.MachineRef{}, &provider.Error{
		Kind: provider.FailureConflict, Op: "allocate a VMID",
		Message: fmt.Sprintf("every identifier picked from %d-%d was taken by something else while it was being claimed",
			p.settings.vmidMin, p.settings.vmidMax),
		Remedy: "another tool is allocating inside this provider's VMID range; give Zoomies a block of its own",
	}
}

// placeOn chooses which node a machine is built on: the configured one with
// the fewest guests on it, and the first one configured when they are level.
//
// Spreading rather than filling is the only default that does not quietly put
// a whole fleet on one node, and counting every guest rather than only ours
// means a node somebody else has already filled is the last one chosen. The
// count comes from the listing Allocate already has, so this costs no call.
func (p *Provider) placeOn(guests []ClusterVM) (string, error) {
	if len(p.settings.nodes) == 0 {
		return "", &provider.Error{
			Kind: provider.FailureConfig, Op: "choose a node",
			Message: "this provider has no node configured, so there is nowhere to build a machine",
			Remedy:  "set nodes to at least one node of the cluster",
		}
	}
	counts := map[string]int{}
	for _, g := range guests {
		counts[g.Node]++
	}
	best := p.settings.nodes[0]
	for _, node := range p.settings.nodes[1:] {
		if counts[node] < counts[best] {
			best = node
		}
	}
	return best, nil
}

// ---------------------------------------------------------------------------
// Building
// ---------------------------------------------------------------------------

// Create clones the template into the identity Allocate picked.
//
// The clone carries the guest's name and its ownership record, which are the
// two marks that make it findable and provably ours from the moment it exists:
// a guest created with neither, whose answer was then lost, is a machine
// nothing could tell from somebody else's. Applying the tags and the machine's
// shape is Start's, because a clone holds a lock on its target until it
// finishes and nothing may be written to a locked guest; the shape a machine
// was asked for is recorded in that same description here, which is how Start
// knows it without having been told.
//
// Calling it twice for one ref builds one machine. That is what makes a create
// re-issued after an outcome nobody heard safe, and it is checked here by
// asking what is at the identifier before anything is cloned.
func (p *Provider) Create(ctx context.Context, ref provider.MachineRef, spec provider.MachineSpec) (provider.OperationRef, error) {
	node, vmid, err := p.locate(ref, "create a machine")
	if err != nil {
		return provider.OperationRef{}, err
	}

	switch existing, err := p.client.VMStatus(ctx, node, vmid); {
	case err == nil && existing.Name != "" && existing.Name != ref.Name:
		return provider.OperationRef{}, &provider.Error{
			Kind: provider.FailureConflict, Op: "create a machine", Ref: vmRef(node, vmid),
			Message: fmt.Sprintf("VMID %d on %s is already a guest called %q, so this machine's identifier belongs to something else",
				vmid, node, existing.Name),
			Remedy: "nothing has been created; the machine is given a new identifier, and a VMID range nothing else allocates from stops this happening",
		}
	case err == nil:
		// Already built, by an earlier call whose answer may never have
		// arrived. There is nothing to follow and nothing to do again.
		return provider.OperationRef{}, nil
	case errors.Is(err, provider.ErrNotFound):
	default:
		return provider.OperationRef{}, err
	}

	req := CloneRequest{
		NewID:       vmid,
		Name:        ref.Name,
		Full:        p.settings.fullClone,
		Storage:     p.settings.storage,
		Pool:        p.settings.pool,
		Description: describeMachine(spec.Owner, ref.Name, spec.Shape),
	}
	if node != p.templateNode() {
		// Proxmox only allows a clone to land on another node when the
		// template's disk is on shared storage, and says so plainly when it
		// is not -- so this is left off entirely for the ordinary case rather
		// than sending a node that is already the right one.
		req.Target = node
	}
	upid, err := p.client.CloneVM(ctx, p.templateNode(), p.settings.templateID, req)
	if err != nil {
		return provider.OperationRef{}, err
	}
	return provider.OperationRef{Kind: provider.OpCreate, Handle: upid}, nil
}

// Start sizes a machine, marks it, and powers it on.
//
// All three are here because a clone locks its target until it finishes, and
// this is the first call that runs after it: the guest is stopped, unlocked,
// and about to be handed to an operating system that will read its processors
// and memory once at boot. The shape comes off the guest's own description,
// where Create recorded it, so that a controller which restarted between the
// two calls still builds the machine the operator asked for.
func (p *Provider) Start(ctx context.Context, ref provider.MachineRef) (provider.OperationRef, error) {
	node, vmid, err := p.locate(ref, "start a machine")
	if err != nil {
		return provider.OperationRef{}, err
	}
	status, err := p.client.VMStatus(ctx, node, vmid)
	if err != nil {
		return provider.OperationRef{}, err
	}
	if status.Status == statusRunning {
		// Already running: there is nothing to follow, and configuring a
		// running guest would change nothing it has already read.
		return provider.OperationRef{}, nil
	}
	if status.Lock != "" {
		return provider.OperationRef{}, lockedError("start a machine", node, vmid, status.Lock)
	}

	cfg, err := p.client.VMConfig(ctx, node, vmid)
	if err != nil {
		return provider.OperationRef{}, err
	}
	if err := p.configure(ctx, node, vmid, cfg); err != nil {
		return provider.OperationRef{}, err
	}
	upid, err := p.client.StartVM(ctx, node, vmid)
	if err != nil {
		return provider.OperationRef{}, err
	}
	return provider.OperationRef{Kind: provider.OpStart, Handle: upid}, nil
}

// configure gives a guest the shape it was asked for, the network the operator
// chose, and the tags that let a cluster-wide listing find it.
//
// It is idempotent: every value is written absolutely rather than adjusted, so
// a pass that failed halfway is fixed by running again. The disk is the one
// exception -- Proxmox will not shrink one, and a shape asking for less than
// the template has is honoured by leaving the disk alone rather than by
// risking a filesystem.
func (p *Provider) configure(ctx context.Context, node string, vmid int, cfg VMConfig) error {
	shape, haveShape := decodeShape(cfg.Description)
	params := url.Values{
		// The guest agent is how the enrolment reaches this machine without
		// ever entering its metadata, so it is switched on here as well as in
		// the template: a template prepared without it would otherwise produce
		// machines that boot, cost money and never join.
		"agent": {"1"},
		// Never on boot. A machine this fleet rents is started when the fleet
		// starts it; one that came back by itself after a hypervisor reboot
		// would be a host nothing is accounting for.
		"onboot": {"0"},
	}
	if p.settings.bridge != "" {
		params.Set("net0", netDevice(cfg.Net0, p.settings.bridge))
	}
	if p.settings.ipConfig != "" {
		params.Set("ipconfig0", p.settings.ipConfig)
	}
	if p.settings.nameserver != "" {
		params.Set("nameserver", p.settings.nameserver)
	}
	if haveShape {
		if shape.Cores > 0 {
			params.Set("cores", strconv.Itoa(shape.Cores))
			params.Set("sockets", "1")
		}
		if shape.MemoryMB > 0 {
			params.Set("memory", strconv.FormatInt(shape.MemoryMB, 10))
		}
	} else {
		// The record is written by the clone, so its absence means this guest
		// was made by something else or by an older build. Everything above
		// still applies; the size is the one thing that cannot be guessed, and
		// silently building the template's size is worth saying out loud.
		p.log.Warn("a machine carries no record of the shape it was asked for, so it keeps the template's processors, memory and disk",
			"node", node, "vmid", vmid,
			"fix", "delete this machine and let the fleet build another, which will carry the record")
	}
	if owner, ok := DecodeOwner(cfg.Description); ok {
		// Merged with whatever the guest already carries, so a tag an operator
		// added by hand survives being configured.
		params.Set("tags", EncodeTags(cfg.Tags, Tags(owner)))
	}

	if _, err := p.client.ConfigureVM(ctx, node, vmid, params); err != nil {
		return err
	}
	if !haveShape || shape.DiskMB <= 0 {
		return nil
	}
	return p.growDisk(ctx, node, vmid, cfg, shape.DiskMB)
}

// growDisk grows the boot disk to the shape's size, and only ever grows it.
//
// A resize that is not needed is not attempted at all: Proxmox refuses a size
// that is not larger than the current one, and a provider that asked anyway
// would turn every start of an already-correct machine into a failure.
func (p *Provider) growDisk(ctx context.Context, node string, vmid int, cfg VMConfig, wantMB int64) error {
	disk, spec := bootDisk(cfg)
	if disk == "" {
		p.log.Warn("a machine has no disk this build recognises, so its size is left as the template's",
			"node", node, "vmid", vmid)
		return nil
	}
	haveMB, ok := diskSizeMB(spec)
	if !ok {
		// A size that cannot be read is left alone rather than guessed at:
		// Proxmox refuses a resize that is not an increase, so a guess that is
		// too small turns every start of this machine into a failure, and a
		// machine that came up slightly smaller than it was asked for beats a
		// machine that never comes up at all.
		p.log.Warn("a machine's disk does not say how big it is, so its size is left as the template's",
			"node", node, "vmid", vmid, "disk", disk)
		return nil
	}
	if haveMB >= wantMB {
		return nil
	}
	return p.client.ResizeDisk(ctx, node, vmid, disk, strconv.FormatInt(wantMB, 10)+"M")
}

// Stop powers a machine down without destroying it.
//
// Graceful is a shutdown the guest performs itself, with the power pulled after
// it if it will not: a shutdown that hangs on a wedged guest would otherwise
// leave a machine nothing can destroy, because Proxmox will not remove one that
// is running.
func (p *Provider) Stop(ctx context.Context, ref provider.MachineRef, graceful bool) (provider.OperationRef, error) {
	node, vmid, err := p.locate(ref, "stop a machine")
	if err != nil {
		return provider.OperationRef{}, err
	}
	var upid string
	if graceful {
		upid, err = p.client.ShutdownVM(ctx, node, vmid, shutdownGrace, true)
	} else {
		upid, err = p.client.StopVM(ctx, node, vmid)
	}
	if err != nil {
		return provider.OperationRef{}, err
	}
	return provider.OperationRef{Kind: provider.OpStop, Handle: upid}, nil
}

// ---------------------------------------------------------------------------
// Reading
// ---------------------------------------------------------------------------

// Inspect reports one guest: what it is doing, what holds it, and the ownership
// marks as they are written on it.
//
// The marks are read back from the guest's own configuration and never echoed
// from what a caller supplied, because a value handed back proves nothing about
// what is on the machine -- and what stands between a sweep and a delete is
// exactly that difference.
func (p *Provider) Inspect(ctx context.Context, ref provider.MachineRef) (provider.Machine, error) {
	node, vmid, err := p.locate(ref, "inspect a machine")
	if err != nil {
		return provider.Machine{}, err
	}
	status, err := p.client.VMStatus(ctx, node, vmid)
	if err != nil {
		return provider.Machine{}, err
	}
	m := provider.Machine{
		Ref:    provider.MachineRef{Zone: node, ID: strconv.Itoa(vmid), Name: status.Name},
		Phase:  phaseOf(status.Status),
		Locked: status.Lock,
		Detail: describeStatus(node, vmid, status),
	}
	cfg, err := p.client.VMConfig(ctx, node, vmid)
	if err != nil {
		// The guest answered a moment ago, so a configuration that cannot be
		// read is a refusal to report rather than a machine to guess about:
		// returning it with no marks would read as an unmarked resource, which
		// is a different thing and authorises different decisions.
		return provider.Machine{}, err
	}
	if owner, ok := DecodeOwner(cfg.Description); ok {
		m.Owner = owner
	}
	if m.Ref.Name == "" {
		m.Ref.Name = cfg.Name
	}
	return m, nil
}

// List is the ownership sweep: every guest that wears this fleet's tag or its
// naming grammar, with the marks each one carries.
//
// The cluster-wide listing is one call however many nodes there are, which is
// what makes the sweep cheap. The marks themselves are not in it -- a guest's
// description is only readable from its own configuration -- so each candidate
// costs one more call, and the tag is what keeps "each candidate" to this
// fleet's own machines rather than to every guest in the cluster.
//
// A guest that cannot be read fails the whole call rather than being left out.
// A sweep that silently returned fewer machines than exist would read as rows
// whose resources have gone, and that answer drains hosts.
func (p *Provider) List(ctx context.Context, owner provider.Owner) ([]provider.Machine, error) {
	guests, err := p.client.ClusterVMs(ctx)
	if err != nil {
		return nil, err
	}
	var out []provider.Machine
	for _, g := range guests {
		// Templates are never machines, and a container cannot run an agent
		// this fleet installs.
		if g.Type != "qemu" || bool(g.Template) {
			continue
		}
		// Ours by tag, or wearing our naming grammar: the second is how an
		// orphan of our own is told from another fleet's machine, and the
		// caller needs both to tell those apart.
		if !HasFleetTag(g.Tags) && !store.IsMachineName(g.Name) {
			continue
		}
		vmid := g.VMID.Int()
		m := provider.Machine{
			Ref:    provider.MachineRef{Zone: g.Node, ID: strconv.Itoa(vmid), Name: g.Name},
			Phase:  phaseOf(g.Status),
			Detail: fmt.Sprintf("VMID %d on %s", vmid, g.Node),
		}
		cfg, err := p.client.VMConfig(ctx, g.Node, vmid)
		switch {
		case errors.Is(err, provider.ErrNotFound):
			// Destroyed between the listing and this read. It is gone, which
			// is what leaving it out of the answer says.
			continue
		case err != nil:
			return nil, err
		}
		if got, ok := DecodeOwner(cfg.Description); ok {
			m.Owner = got
		}
		out = append(out, m)
	}
	return out, nil
}

// Operation reports an asynchronous operation's progress, and is the whole of
// restart recovery: a controller holding nothing but a stored handle asks this
// rather than doing the work again.
//
// Two kinds of handle arrive here. A UPID is a cluster task and carries its own
// node, so nothing else about it needs to have been stored. A guest-exec handle
// is bootstrap.go's, and is explained there.
func (p *Provider) Operation(ctx context.Context, op provider.OperationRef) (provider.OperationStatus, error) {
	if node, vmid, pid, ok := parseExecHandle(op.Handle); ok {
		return p.execStatus(ctx, node, vmid, pid)
	}
	upid, err := ParseUPID(op.Handle)
	if err != nil {
		// A handle this build cannot read names no node, so there is nowhere
		// to ask about it. Not-found is the honest answer and the one the
		// contract requires: it authorises nothing on its own, and the caller
		// goes and looks at the machine instead.
		return provider.OperationStatus{}, &provider.Error{
			Kind: provider.FailureNotFound, Op: "follow an operation", Ref: op.Handle,
			Message: err.Error(),
		}
	}
	task, err := p.client.Task(ctx, upid.Node, upid.Raw)
	if err != nil {
		return provider.OperationStatus{}, err
	}
	switch {
	case !task.Done():
		return provider.OperationStatus{Detail: "in progress on " + upid.Node}, nil
	case task.OK():
		return provider.OperationStatus{Done: true, OK: true, Detail: "finished on " + upid.Node}, nil
	}
	// Proxmox's own words for the failure, and its task log behind them: the
	// exit status says "storage 'local-lvm' is full" and the log says which
	// disk it was copying at the time, and an operator reading a machine's
	// page is owed both.
	detail := task.ExitStatus
	if detail == "" {
		detail = "the task ended without saying why"
	}
	if log, err := p.client.TaskLog(ctx, upid.Node, upid.Raw, taskLogLines); err == nil {
		if log = strings.TrimSpace(log); log != "" {
			detail += "\n" + log
		}
	}
	return provider.OperationStatus{Done: true, Detail: detail}, nil
}

// taskLogLines is how much of a failed task's log is carried into the message.
// Enough for the lines that name the storage or the file, and not so much that
// a machine's page becomes a log viewer.
const taskLogLines = 20

// ---------------------------------------------------------------------------
// Removing
// ---------------------------------------------------------------------------

// Delete destroys a guest, having first stopped it if it is running.
//
// Proxmox refuses to destroy a running guest, so a delete that finds one issues
// a shutdown and hands that task back as the delete's handle. The caller then
// follows it, finds the resource still there, and asks again -- one more pass,
// and every state of it durable. Stopping and destroying inside one call would
// mean waiting on a task, which is the thing this provider does not do.
//
// Deleting a guest that is already gone is success: the desired end state has
// been reached, so there is nothing to follow and nothing to report.
func (p *Provider) Delete(ctx context.Context, ref provider.MachineRef) (provider.OperationRef, error) {
	node, vmid, err := p.locate(ref, "destroy a machine")
	if err != nil {
		return provider.OperationRef{}, err
	}
	status, err := p.client.VMStatus(ctx, node, vmid)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return provider.OperationRef{}, nil
	case err != nil:
		return provider.OperationRef{}, err
	}
	if status.Lock != "" {
		return provider.OperationRef{}, lockedError("destroy a machine", node, vmid, status.Lock)
	}
	if status.Status == statusRunning {
		upid, err := p.client.ShutdownVM(ctx, node, vmid, shutdownGrace, true)
		if err != nil {
			return provider.OperationRef{}, err
		}
		return provider.OperationRef{Kind: provider.OpDelete, Handle: upid}, nil
	}
	upid, err := p.client.DeleteVM(ctx, node, vmid)
	switch {
	case errors.Is(err, provider.ErrNotFound):
		return provider.OperationRef{}, nil
	case err != nil:
		return provider.OperationRef{}, err
	}
	return provider.OperationRef{Kind: provider.OpDelete, Handle: upid}, nil
}

// ---------------------------------------------------------------------------
// The shape a machine was asked for
// ---------------------------------------------------------------------------

// shapeBlock is the shape a machine was asked for, recorded on the guest at
// the moment it is cloned.
//
// It is a separate line of the description from the ownership record, and a
// separate object, so that neither decoder can be confused by the other and
// neither has learnt anything about the other's job. What it exists for is the
// call that sizes a machine: that call is handed a reference and no
// specification, and a controller may have restarted between the clone and it.
type shapeBlock struct {
	Shape recordedShape `json:"zoomies_shape"`
}

type recordedShape struct {
	Cores    int   `json:"cores,omitempty"`
	MemoryMB int64 `json:"memory_mb,omitempty"`
	DiskMB   int64 `json:"disk_mb,omitempty"`
}

func (r recordedShape) zero() bool { return r.Cores == 0 && r.MemoryMB == 0 && r.DiskMB == 0 }

// describeMachine renders the description a clone carries: the ownership
// record, and the shape the machine was asked for.
func describeMachine(owner provider.Owner, name string, shape provider.Shape) string {
	described := Describe(owner, name)
	rec := recordedShape{Cores: coresFor(shape.CPUs), MemoryMB: shape.MemoryMB, DiskMB: shape.DiskMB}
	if rec.zero() {
		return described
	}
	blob, err := json.Marshal(shapeBlock{Shape: rec})
	if err != nil {
		// A description without the shape still identifies the machine, and
		// the guest keeps the template's size with a warning rather than not
		// being created at all.
		return described
	}
	return described + "\n" + string(blob)
}

// decodeShape reads the recorded shape back, reporting whether one was there.
// It scans line by line for the same reason DecodeOwner does: an operator who
// appends a note to a guest's description has not thereby changed its shape.
func decodeShape(description string) (recordedShape, bool) {
	for _, line := range strings.Split(description, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var block shapeBlock
		if err := json.Unmarshal([]byte(line), &block); err != nil {
			continue
		}
		if block.Shape.zero() {
			continue
		}
		return block.Shape, true
	}
	return recordedShape{}, false
}

// coresFor turns the contract's fractional processors into the whole ones
// Proxmox allocates, rounding up: a runner given less than it was promised is
// a slow build nobody can explain, and half a core is not a thing a guest can
// be given.
func coresFor(cpus float64) int {
	if cpus <= 0 {
		return 0
	}
	return int(math.Ceil(cpus))
}

// ---------------------------------------------------------------------------
// Plumbing
// ---------------------------------------------------------------------------

// statusRunning is the one status string that means a guest is up. Proxmox
// writes "running" or "stopped" and nothing else for a guest it can see.
const statusRunning = "running"

// phaseOf maps Proxmox's statuses onto the contract's phases. Anything this
// build does not recognise is unknown, which authorises no delete and never
// reads as gone: what we cannot classify, we leave alone.
func phaseOf(status string) provider.Phase {
	switch status {
	case statusRunning:
		return provider.PhaseRunning
	case "stopped", "paused", "suspended":
		return provider.PhaseStopped
	}
	return provider.PhaseUnknown
}

// describeStatus is the one operator-facing sentence a machine's page shows.
func describeStatus(node string, vmid int, s VMStatus) string {
	detail := fmt.Sprintf("VMID %d on %s is %s", vmid, node, s.Status)
	if s.Lock != "" {
		detail += fmt.Sprintf(", held by a %s", s.Lock)
	}
	return detail
}

// locate turns a ref into the node and identifier every call needs, refusing a
// ref that names neither rather than guessing at one.
func (p *Provider) locate(ref provider.MachineRef, op string) (string, int, error) {
	node := strings.TrimSpace(ref.Zone)
	if node == "" {
		return "", 0, &provider.Error{
			Kind: provider.FailureInternal, Op: op, Ref: ref.Name,
			Message: "this machine has no node recorded, and a guest cannot be found without one",
			Remedy:  "the row was written before the node was chosen; release this machine and let the fleet build another",
		}
	}
	vmid, err := strconv.Atoi(strings.TrimSpace(ref.ID))
	if err != nil || vmid <= 0 {
		return "", 0, &provider.Error{
			Kind: provider.FailureInternal, Op: op, Ref: ref.Name,
			Message: fmt.Sprintf("%q is not a VMID, so there is no guest to act on", ref.ID),
			Remedy:  "release this machine and let the fleet build another",
		}
	}
	return node, vmid, nil
}

// lockedError is another operation holding a guest: something to wait for and
// observe, never something to force or to build a second machine over.
func lockedError(op, node string, vmid int, lock string) error {
	return &provider.Error{
		Kind: provider.FailureConflict, Op: op, Ref: vmRef(node, vmid),
		Message: fmt.Sprintf("VM %d on %s is held by a %s", vmid, node, lock),
		Remedy:  "nothing is forced; the operation holding it is waited for",
	}
}

// templateNode is where the template lives: the node an operator named, or the
// first node this provider may place on, which is the arrangement a
// single-node cluster has.
func (p *Provider) templateNode() string {
	if p.settings.templateNode != "" {
		return p.settings.templateNode
	}
	if len(p.settings.nodes) > 0 {
		return p.settings.nodes[0]
	}
	return ""
}

// netDevice is the network interface to write, keeping whatever hardware
// address the guest already has.
//
// Carrying the address over is what makes configuring a machine twice harmless:
// an interface written without one has Proxmox generate a new address, and a
// machine that changes its address takes a new DHCP lease and a new IP with it,
// which loses any reservation somebody made for it. The template's own model is
// kept for the same reason -- it is the operator's choice, not ours.
func netDevice(existing, bridge string) string {
	device := "virtio"
	if model, mac, ok := strings.Cut(firstField(existing), "="); ok && looksLikeMAC(mac) {
		device = model + "=" + mac
	}
	return device + ",bridge=" + bridge
}

func firstField(s string) string {
	field, _, _ := strings.Cut(s, ",")
	return strings.TrimSpace(field)
}

// looksLikeMAC is the shape Proxmox writes a hardware address in, and enough of
// a check to tell one from a property that happens to carry an "=".
func looksLikeMAC(s string) bool {
	return len(s) == len("AA:BB:CC:DD:EE:FF") && strings.Count(s, ":") == 5
}

// bootDisk is the disk a machine's operating system is on, and its
// configuration string. The order is the one a cloud image is imported with:
// scsi0 is what every current Proxmox template uses and virtio0 what the older
// ones do.
func bootDisk(cfg VMConfig) (string, string) {
	switch {
	case cfg.SCSI0 != "":
		return "scsi0", cfg.SCSI0
	case cfg.VirtIO0 != "":
		return "virtio0", cfg.VirtIO0
	}
	return "", ""
}

// diskSizeMB reads the size out of a disk's configuration string --
// "local-lvm:vm-143-disk-0,size=20G" -- reporting whether there was one. A disk
// whose size cannot be read is left alone rather than guessed at.
func diskSizeMB(spec string) (int64, bool) {
	for _, part := range strings.Split(spec, ",") {
		value, ok := strings.CutPrefix(strings.TrimSpace(part), "size=")
		if !ok {
			continue
		}
		return parseSize(value)
	}
	return 0, false
}

// parseSize reads Proxmox's own size grammar: a number and an optional unit,
// where a bare number is bytes.
func parseSize(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	unit := value[len(value)-1]
	digits := value
	var scale float64 = 1.0 / (1 << 20) // bytes, when there is no unit at all
	switch unit {
	case 'K', 'k':
		scale = 1.0 / 1024
	case 'M', 'm':
		scale = 1
	case 'G', 'g':
		scale = 1024
	case 'T', 't':
		scale = 1024 * 1024
	default:
		unit = 0
	}
	if unit != 0 {
		digits = value[:len(value)-1]
	}
	n, err := strconv.ParseFloat(digits, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return int64(n * scale), true
}

// refuse turns a settings check into the error a build fails with, naming every
// answer that is wrong rather than only the first: an operator fixing a form
// should not have to submit it four times to be told four things.
func refuse(findings []config.Finding) error {
	var wrong []string
	for _, f := range findings {
		if f.Severity == config.SeverityError {
			wrong = append(wrong, f.Title)
		}
	}
	if len(wrong) == 0 {
		return nil
	}
	return &provider.Error{
		Kind: provider.FailureConfig, Op: "build a Proxmox provider",
		Message: strings.Join(wrong, "; "),
		Remedy:  "correct this provider's settings; the provider's page lists each one and what it is for",
	}
}

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

// The settings an operator answers, beyond the endpoint and the credential.
// Preflight names several of these in its findings, which is why those keys
// live beside it rather than here.
const (
	SettingTemplateNode  = "template_node"
	SettingFullClone     = "full_clone"
	SettingIPConfig      = "ipconfig0"
	SettingNameserver    = "nameserver"
	SettingControllerURL = "controller_url"
)

// defaultIPConfig is what a cloud image is told about its network when nobody
// says otherwise. A machine with no address can never enrol, and a cloud-init
// guest given no ipconfig at all is given no network configuration.
const defaultIPConfig = "ip=dhcp"

// settings is one provider row's answers, parsed once.
type settings struct {
	nodes            []string
	storage          string
	bridge           string
	templateNode     string
	templateID       int
	pool             string
	vmidMin, vmidMax int
	fullClone        bool
	ipConfig         string
	nameserver       string
	tokenID          string
}

// prereqs is the configured half of this provider in preflight's terms.
func (s settings) prereqs() Prereqs {
	return Prereqs{
		Nodes:        s.nodes,
		Storage:      s.storage,
		Bridge:       s.bridge,
		TemplateNode: s.templateNode,
		TemplateID:   s.templateID,
		Pool:         s.pool,
		VMIDMin:      s.vmidMin,
		VMIDMax:      s.vmidMax,
		// Always: the qualified bootstrap is the guest agent, so the two
		// privileges it needs are prerequisites rather than extras.
		GuestAgent: true,
	}
}

// parseSettings reads a row's answers and says what is wrong with them.
//
// One function feeds three callers -- the form's offline validation, the
// provider that is built from the row, and the preflight that checks the
// answers against the cluster -- so a setting cannot be understood one way in
// the wizard and another way by the thing that clones a template.
func parseSettings(in map[string]string) (settings, []config.Finding) {
	get := func(key string) string { return strings.TrimSpace(in[key]) }
	s := settings{
		nodes:        splitList(get(SettingNodes)),
		storage:      get(SettingStorage),
		bridge:       get(SettingBridge),
		templateNode: get(SettingTemplateNode),
		pool:         get(SettingPool),
		nameserver:   get(SettingNameserver),
		tokenID:      get(SettingTokenID),
		fullClone:    true,
		ipConfig:     defaultIPConfig,
	}
	// Absent and empty are different answers here: clearing this is how a
	// template with no cloud-init drive at all is described, and a default
	// applied over that would put a setting on a guest that cannot use it.
	if raw, answered := in[SettingIPConfig]; answered {
		s.ipConfig = strings.TrimSpace(raw)
	}
	if raw := get(SettingFullClone); raw != "" {
		// A linked clone is quick and cheap and ties the machine's life to the
		// template's: the template can then never be deleted or altered while
		// a machine made from it lives. Full is the default for that reason,
		// and this is how an operator who understands the trade takes it.
		s.fullClone = raw != "false" && raw != "0" && raw != "no"
	}

	var findings []config.Finding
	add := func(f config.Finding) { findings = append(findings, f) }

	if len(s.nodes) == 0 {
		add(config.Finding{
			Code: "proxmox.node_missing", Severity: config.SeverityError, Setting: SettingNodes,
			Title:  "no node is configured",
			Detail: "there is nowhere for a machine to be built.",
			Fix:    "choose at least one node of the cluster; the form lists the ones this token can see.",
		})
	}
	if s.storage == "" {
		add(config.Finding{
			Code: "proxmox.storage_missing", Severity: config.SeverityError, Setting: SettingStorage,
			Title:  "no storage is configured",
			Detail: "a clone has nowhere to put the machine's disk.",
			Fix:    "choose a storage that accepts disk images.",
		})
	}
	if s.bridge == "" {
		add(config.Finding{
			Code: "proxmox.bridge_missing", Severity: config.SeverityError, Setting: SettingBridge,
			Title:  "no network bridge is configured",
			Detail: "a machine with no network cannot reach this controller, so it can never enrol.",
			Fix:    "choose the bridge the rest of this network uses, usually vmbr0.",
		})
	}

	var err error
	if raw := get(SettingTemplateID); raw != "" {
		if s.templateID, err = strconv.Atoi(raw); err != nil || s.templateID <= 0 {
			s.templateID = 0
		}
	}
	if s.templateID <= 0 {
		add(config.Finding{
			Code: "proxmox.template_missing", Severity: config.SeverityError, Setting: SettingTemplateID,
			Title:  "no template is configured",
			Detail: "every machine starts as a clone of one, and a template is named by its VMID.",
			Fix:    "prepare a template as the runbook describes and give its VMID here.",
		})
	}

	s.vmidMin, s.vmidMax = atoiOrZero(get(SettingVMIDMin)), atoiOrZero(get(SettingVMIDMax))
	switch {
	case s.vmidMin <= 0 || s.vmidMax <= 0:
		add(config.Finding{
			Code: "proxmox.vmid_range", Severity: config.SeverityError, Setting: SettingVMIDMin,
			Title:  "no VMID range is configured",
			Detail: "without one this provider would allocate identifiers anywhere in the cluster, including ones somebody else's automation is entitled to.",
			Fix:    "set vmid_min and vmid_max to a range nothing else uses, for example 9000 to 9099.",
		})
	case s.vmidMin > s.vmidMax:
		add(config.Finding{
			Code: "proxmox.vmid_range", Severity: config.SeverityError, Setting: SettingVMIDMin,
			Title:  fmt.Sprintf("the VMID range %d-%d runs backwards", s.vmidMin, s.vmidMax),
			Detail: "no identifier can be allocated from it, so no machine can be created.",
			Fix:    "set vmid_min below vmid_max.",
		})
	}
	return s, findings
}

// splitList reads a setting that holds several answers. Commas are what the
// form writes and whitespace is what somebody typing one out by hand uses, so
// both are accepted.
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func atoiOrZero(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// splitCredential separates the token's name from its secret.
//
// Proxmox prints a new token as "user@realm!tokenid=secret" and that is what
// gets pasted, so the whole thing arrives as the credential and is split here.
// An operator who keeps the two apart -- the name in a setting, the secret in
// the credential -- is right as well, and gets the same answer: the name is
// not a secret, and it is what every message asking for a privilege has to
// quote.
func splitCredential(tokenID, credential string) (string, string, error) {
	credential = strings.TrimSpace(credential)
	tokenID = strings.TrimSpace(tokenID)
	if credential == "" {
		return "", "", &provider.Error{
			Kind: provider.FailureConfig, Op: "build a Proxmox provider",
			Message: "this provider has no API token",
			Remedy:  "paste the token exactly as Proxmox printed it when it was created: user@realm!tokenid=secret",
		}
	}
	if name, secret, ok := strings.Cut(credential, "="); ok &&
		strings.Contains(name, "@") && strings.Contains(name, "!") {
		return strings.TrimSpace(name), strings.TrimSpace(secret), nil
	}
	return tokenID, credential, nil
}

// ---------------------------------------------------------------------------
// The factory
// ---------------------------------------------------------------------------

// Factory builds Proxmox providers. It is what the registry holds, and what the
// page listing the kinds a build supports is rendered from.
type Factory struct{}

// NewFactory returns the factory to register.
func NewFactory() Factory { return Factory{} }

func (Factory) Kind() store.ProviderKind { return store.ProviderProxmox }

// Describe is this kind's capabilities without an instance. It is built from
// the same function the instance's are, so the two cannot disagree.
func (Factory) Describe() provider.Capabilities { return capabilities(provider.Deadlines{}) }

// Settings is the schema the configuration form, the validator and the
// documentation share.
func (Factory) Settings() []provider.SettingSpec {
	return []provider.SettingSpec{
		{
			Key: SettingNodes, Label: "Nodes", Kind: provider.SettingList, Required: true,
			Discovers:   "nodes",
			Help:        "The cluster nodes machines may be built on.",
			Consequence: "Machines are spread across these, fewest guests first. A node not named here is never touched.",
			Source:      "The node names in the tree on the left of the Proxmox console, or `pvesh get /nodes` on any node.",
		},
		{
			Key: SettingTemplateID, Label: "Template VMID", Kind: provider.SettingChoice, Required: true,
			Discovers:   "templates",
			Help:        "The prepared template every machine is cloned from.",
			Consequence: "What is in the template is what a runner gets: Zoomies installs nothing that is not already there.",
			Source:      "The number beside the template in the console tree; `qm list` on its node shows every guest with its VMID.",
		},
		{
			Key: SettingStorage, Label: "Storage", Kind: provider.SettingChoice, Required: true,
			Discovers:   "storages",
			Help:        "Where a clone's disk lands.",
			Consequence: "A storage only one node can see means machines can only be built on that node.",
			Source:      "Datacenter -> Storage. It has to allow the Disk image content type, and a storage only one node can see confines machines to that node.",
		},
		{
			Key: SettingBridge, Label: "Network bridge", Kind: provider.SettingChoice, Required: true,
			Discovers:   "bridges",
			Default:     "vmbr0",
			Help:        "The bridge a machine's network interface is attached to.",
			Consequence: "A machine that cannot reach this controller from this bridge boots, costs money and never joins.",
			Source:      "A node -> System -> Network. vmbr0 is the bridge a default install creates; `ip link` inside a working guest shows which one it is on.",
		},
		{
			Key: SettingVMIDMin, Label: "Lowest VMID", Kind: provider.SettingNumber, Required: true,
			Default:     "9000",
			Help:        "The bottom of the block of VM identifiers Zoomies may allocate from.",
			Consequence: "The range is a blast radius as well as a budget: a guest outside it is, by construction, not one of ours.",
			Source:      "Your choice, not something to look up. Pick a block no other tooling numbers from; `qm list` on each node shows what is taken.",
		},
		{
			Key: SettingVMIDMax, Label: "Highest VMID", Kind: provider.SettingNumber, Required: true,
			Default:     "9099",
			Help:        "The top of that block.",
			Consequence: "It also caps how many machines can exist at once, whatever the fleet's other limits say.",
			Source:      "The top of the same block. The template itself must sit outside it.",
		},
		{
			Key: SettingTemplateNode, Label: "Template's node", Kind: provider.SettingChoice, Advanced: true,
			Discovers:   "nodes",
			Help:        "Where the template itself lives. Empty means the first node above.",
			Consequence: "Cloning to a different node needs the template's disk on shared storage; Proxmox refuses it otherwise.",
			Source:      "The node the template appears under in the console tree.",
		},
		{
			Key: SettingPool, Label: "Resource pool", Kind: provider.SettingText, Advanced: true,
			Help:        "A Proxmox pool new guests are put in.",
			Consequence: "A pool is also a path privileges can be granted on, which is how this token is kept away from the rest of the cluster.",
			Source:      "Datacenter -> Permissions -> Pools; the pool must already exist.",
		},
		{
			Key: SettingFullClone, Label: "Full clone", Kind: provider.SettingBool, Advanced: true,
			Default:     "true",
			Help:        "Copy the template's disk rather than referring to it.",
			Consequence: "A linked clone is quicker and smaller, and the template can then never be deleted or changed while a machine made from it lives.",
		},
		{
			Key: SettingIPConfig, Label: "Cloud-init IP configuration", Kind: provider.SettingText, Advanced: true,
			Default:     defaultIPConfig,
			Help:        "What a cloud image is told about its network, in Proxmox's ipconfig0 grammar.",
			Consequence: "Clear it for a template that has no cloud-init drive; a machine with no address can never enrol.",
		},
		{
			Key: SettingNameserver, Label: "Nameserver", Kind: provider.SettingText, Advanced: true,
			Help:        "A resolver for cloud-init to write into the guest. Empty leaves whatever DHCP provides.",
			Consequence: "A machine that cannot resolve this controller's name never joins.",
		},
		{
			Key: SettingControllerURL, Label: "Controller URL for machines", Kind: provider.SettingText, Advanced: true,
			Help:        "How a machine on this cluster reaches this controller, when that is not the fleet's external URL.",
			Consequence: "Machines on a private network usually cannot use the address a browser does.",
			Source:      "The address a machine on this bridge would use to reach the controller: usually the external URL, or its LAN address when the cluster is on the same network.",
		},
		{
			Key: SettingTokenID, Label: "API token id", Kind: provider.SettingText, Advanced: true,
			Help:        "user@realm!tokenid. Leave it empty when the token is pasted whole as user@realm!tokenid=secret.",
			Consequence: "It is not a secret: it is what every message asking for a privilege to be granted has to quote.",
			Source:      "Datacenter -> Permissions -> API Tokens, as user@realm!tokenid.",
		},
	}
}

// Validate checks the answers offline, dialling nothing: it is what the form
// runs as somebody types, and Preflight is the call that talks to the cluster.
func (Factory) Validate(settings map[string]string) []config.Finding {
	_, findings := parseSettings(settings)
	return findings
}

// New builds one provider. It takes a context because the contract does; there
// is nothing to dial here, because reachability is Preflight's answer to give.
func (Factory) New(_ context.Context, cfg provider.Config) (provider.Provider, error) {
	return NewProvider(cfg)
}

// DiscoverDraft lists what a credential can see with nothing but the address
// and the credential, which is the state a wizard is in when it wants the
// menu: the node, storage and template it is about to ask for are exactly the
// settings NewProvider would refuse to build without.
func (Factory) DiscoverDraft(ctx context.Context, cfg provider.Config) (provider.Discovery, error) {
	tokenID, secret, err := splitCredential(cfg.Settings[SettingTokenID], cfg.Credential)
	if err != nil {
		return provider.Discovery{}, err
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	client, err := New(Options{
		Endpoint:    cfg.Endpoint,
		TokenID:     tokenID,
		Secret:      secret,
		CAPEM:       cfg.CAPEM,
		Insecure:    cfg.Insecure,
		DialContext: cfg.DialContext,
		HTTPClient:  cfg.HTTPClient,
		Logger:      log,
	})
	if err != nil {
		return provider.Discovery{}, err
	}
	return Discover(ctx, client)
}
