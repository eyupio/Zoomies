package proxmox

// Preflight: everything that can be checked before a single virtual machine
// exists, reported as findings an operator can act on.
//
// It never returns an error. A provider that cannot be reached, a token that
// was refused, a storage that cannot hold a disk and a template that is not a
// template are all answers rather than failures, and they are reported in
// config.Finding so the problems drawer, the startup print and the setup wizard
// render them with no translation at all. That is what makes "validate the
// prerequisites before provisioning" a button rather than a hope.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/version"
)

// The setting keys findings name. They are the provider row's own settings
// rather than zoomies.yaml keys, because that is where these answers live: a
// credential in a configuration file is a credential in a backup, a
// diagnostics bundle and a screenshot.
const (
	SettingNodes      = "nodes"
	SettingStorage    = "storage"
	SettingBridge     = "bridge"
	SettingTemplateID = "template_id"
	SettingPool       = "pool"
	SettingVMIDMin    = "vmid_min"
	SettingVMIDMax    = "vmid_max"
	SettingTokenID    = "api_token_id"
	SettingInsecure   = "insecure_skip_verify"
)

// Prereqs is the configured half of a provider: what an operator said this
// fleet may use, and therefore what preflight has to confirm exists.
type Prereqs struct {
	// Nodes are the nodes machines may be placed on.
	Nodes []string
	// Storage is where a clone's disk lands.
	Storage string
	// Bridge is the network a machine's interface is attached to.
	Bridge string
	// TemplateNode is where the template lives; empty means the first node.
	TemplateNode string
	// TemplateID is the template's VMID.
	TemplateID int
	// Pool is an optional Proxmox pool new guests are put in, which is also a
	// path privileges can be granted on.
	Pool string
	// VMIDMin and VMIDMax bound the identifiers this provider may take. The
	// range is the point: an unbounded provider would allocate identifiers
	// somebody else's automation is entitled to.
	VMIDMin, VMIDMax int
	// GuestAgent says the bootstrap goes through the QEMU guest agent, which
	// needs two privileges most roles do not include.
	GuestAgent bool
}

// Preflight checks configuration, credentials and prerequisites, changing
// nothing.
func Preflight(ctx context.Context, c *Client, p Prereqs) provider.Report {
	pf := &preflight{client: c, prereqs: p}

	// The range needs no hypervisor, so it is checked first and answered even
	// when nothing else can be: a provider with no range configured is
	// misconfigured whether or not the cluster is up.
	pf.checkVMIDRange()
	pf.checkVerification()

	v, err := c.Version(ctx)
	if err != nil {
		pf.explainFailure(err, "reach the cluster")
		return pf.report
	}
	pf.report.Reachable = true
	pf.report.Version = v.Version
	pf.checkVersion(v)

	pf.checkPrivileges(ctx)
	pf.checkResources(ctx)
	return pf.report
}

type preflight struct {
	client  *Client
	prereqs Prereqs
	report  provider.Report
}

func (pf *preflight) add(f config.Finding) { pf.report.Findings = append(pf.report.Findings, f) }

// explainFailure turns a call that failed into the one finding it deserves.
// A cluster that cannot be reached has one problem, not eleven, and listing the
// checks that could not run would bury it.
func (pf *preflight) explainFailure(err error, attempt string) {
	var pe *provider.Error
	message := err.Error()
	remedy := ""
	if errors.As(err, &pe) {
		message, remedy = pe.Message, pe.Remedy
	}
	switch provider.KindOf(err) {
	case provider.FailureAuth:
		pf.add(config.Finding{
			Code: "proxmox.credentials_refused", Severity: config.SeverityError, Setting: SettingTokenID,
			Title:  "the API token was refused",
			Detail: message,
			Fix:    fallback(remedy, "check the token id and its secret; the secret is shown once, when the token is created"),
		})
	case provider.FailurePermission:
		pf.add(config.Finding{
			Code: "proxmox.privilege_missing", Severity: config.SeverityError, Setting: SettingTokenID,
			Title:  "the API token may not " + attempt,
			Detail: message,
			Fix:    fallback(remedy, "grant the token the privilege this call needs"),
		})
	default:
		pf.add(config.Finding{
			Code: "proxmox.unreachable", Severity: config.SeverityError, Setting: "endpoint",
			Title:  "could not " + attempt,
			Detail: message,
			Fix:    fallback(remedy, "check the endpoint, that the node is up, and that this controller can reach port "+DefaultPort),
		})
	}
}

func (pf *preflight) checkVMIDRange() {
	lo, hi := pf.prereqs.VMIDMin, pf.prereqs.VMIDMax
	switch {
	case lo <= 0 || hi <= 0:
		pf.add(config.Finding{
			Code: "proxmox.vmid_range", Severity: config.SeverityError, Setting: SettingVMIDMin,
			Title:  "no VMID range is configured",
			Detail: "without one this provider would allocate identifiers anywhere in the cluster, including ones somebody else's automation is entitled to.",
			Fix:    "set vmid_min and vmid_max to a range nothing else uses, for example 9000 to 9099.",
		})
	case lo > hi:
		pf.add(config.Finding{
			Code: "proxmox.vmid_range", Severity: config.SeverityError, Setting: SettingVMIDMin,
			Title:  fmt.Sprintf("the VMID range %d-%d runs backwards", lo, hi),
			Detail: "no identifier can be allocated from it, so no machine can be created.",
			Fix:    "set vmid_min below vmid_max.",
		})
	case lo < 100:
		pf.add(config.Finding{
			Code: "proxmox.vmid_range_reserved", Severity: config.SeverityWarning, Setting: SettingVMIDMin,
			Title:  fmt.Sprintf("the VMID range starts at %d", lo),
			Detail: "Proxmox reserves identifiers below 100 for itself, so the first creations in this range will be refused.",
			Fix:    "start the range at 100 or above.",
		})
	}
}

func (pf *preflight) checkVerification() {
	if !pf.client.insecure {
		return
	}
	pf.add(config.Finding{
		Code: "proxmox.insecure_tls", Severity: config.SeverityWarning, Setting: SettingInsecure,
		Title: "this cluster's certificate is not verified",
		Detail: "anything able to intercept the connection can read this token and use it to create and destroy " +
			"virtual machines, and nothing else will ever mention it.",
		Fix: "paste the cluster's CA certificate from /etc/pve/pve-root-ca.pem into ca_pem and turn verification back on.",
	})
}

func (pf *preflight) checkVersion(v VersionInfo) {
	if version.CompareBuilds(v.Version, MinPVEVersion) != version.SkewBehind {
		return
	}
	pf.add(config.Finding{
		Code: "proxmox.version_unqualified", Severity: config.SeverityWarning, Setting: "endpoint",
		Title: fmt.Sprintf("Proxmox VE %s is older than the %s this integration is qualified against", v.Version, MinPVEVersion),
		Detail: "the endpoints it uses were checked against " + MinPVEVersion + " and later; on an older release a call may " +
			"behave differently, and nobody has run the qualification to find out.",
		Fix: "upgrade the cluster, or treat this provider as unqualified and watch the first machines it builds.",
	})
}

// privilege is one thing the token must be allowed to do, the paths that would
// grant it, and why it is needed. The why is in the finding, because an
// operator granting a privilege deserves to know what it buys.
type privilege struct {
	name  string
	paths []string
	why   string
}

func (pf *preflight) requiredPrivileges() []privilege {
	vms := []string{"/vms"}
	if pool := strings.TrimSpace(pf.prereqs.Pool); pool != "" {
		// A grant on the pool covers the guests inside it, so either path is a
		// real answer and an operator who chose the tighter one is right.
		vms = append(vms, "/pool/"+pool)
	}
	template := append([]string{fmt.Sprintf("/vms/%d", pf.prereqs.TemplateID)}, vms...)
	storage := []string{"/storage"}
	if s := strings.TrimSpace(pf.prereqs.Storage); s != "" {
		storage = append([]string{"/storage/" + s}, storage...)
	}

	required := []privilege{
		{"VM.Clone", template, "cloning the template is how every machine starts"},
		{"VM.Allocate", vms, "creating a machine and destroying it again"},
		{"VM.Config.Disk", vms, "giving a machine its disk"},
		{"VM.Config.CPU", vms, "giving a machine its processors"},
		{"VM.Config.Memory", vms, "giving a machine its memory"},
		{"VM.Config.Network", vms, "attaching a machine to the bridge"},
		{"VM.Config.Options", vms, "writing this fleet's ownership marks onto a machine"},
		{"VM.PowerMgmt", vms, "starting a machine and shutting it down"},
		{"VM.Audit", vms, "inspecting machines, and the sweep that tells this fleet's guests from everybody else's"},
		{"Datastore.AllocateSpace", storage, "the disk a clone allocates"},
	}
	if pf.prereqs.GuestAgent {
		// One privilege, not two. Proxmox accepts either VM.GuestAgent.FileWrite
		// or VM.GuestAgent.Unrestricted for writing a file into a guest, and
		// requires Unrestricted for running a command in one -- so Unrestricted
		// alone is enough for both halves of the bootstrap, and asking for
		// FileWrite as well refuses a token that was granted exactly what the
		// runbook asks for.
		required = append(required,
			privilege{"VM.GuestAgent.Unrestricted", vms, "writing the enrolment file inside a machine and running the one command that starts its agent, then reading back what it said when it failed"},
		)
	}
	return required
}

func (pf *preflight) checkPrivileges(ctx context.Context) {
	perms, err := pf.client.Permissions(ctx)
	if err != nil {
		pf.explainFailure(err, "read the token's own permissions")
		return
	}
	for _, want := range pf.requiredPrivileges() {
		if hasAnyPrivilege(perms, want.paths, want.name) {
			continue
		}
		path := want.paths[0]
		pf.add(config.Finding{
			Code: "proxmox.privilege_missing", Severity: config.SeverityError, Setting: SettingTokenID,
			Title:  fmt.Sprintf("the API token is missing %s on %s", want.name, path),
			Detail: want.why + " will be refused without it.",
			Fix: fmt.Sprintf("grant it: pveum acl modify %s --tokens '%s' --roles <a role holding %s>, or Datacenter -> Permissions -> API Tokens in the console.",
				path, pf.client.TokenID(), want.name),
		})
	}
}

// hasAnyPrivilege reports whether a privilege is held on any of the paths that
// would grant it.
func hasAnyPrivilege(perms Permissions, paths []string, name string) bool {
	for _, path := range paths {
		if hasPrivilege(perms, path, name) {
			return true
		}
	}
	return false
}

// hasPrivilege reports whether a token holds one privilege on one path.
//
// Proxmox answers with the paths a token has anything on, each privilege
// carrying a propagate flag, so a grant on "/" with propagation covers
// "/vms/9000" and one without it covers "/" alone. Walking the path upwards is
// what makes a cluster-wide role mean what an operator thinks it means -- a
// check that only looked at the exact path would tell somebody holding
// PVEVMAdmin on / that they are missing every privilege they have.
func hasPrivilege(perms Permissions, path, name string) bool {
	for at := path; ; {
		if propagate, ok := perms[at][name]; ok && (at == path || bool(propagate)) {
			return true
		}
		if at == "/" {
			return false
		}
		if i := strings.LastIndex(at, "/"); i > 0 {
			at = at[:i]
		} else {
			at = "/"
		}
	}
}

func (pf *preflight) checkResources(ctx context.Context) {
	nodes, err := pf.client.Nodes(ctx)
	if err != nil {
		pf.explainFailure(err, "list the cluster's nodes")
		return
	}
	known := map[string]Node{}
	for _, n := range nodes {
		known[n.Node] = n
	}

	wanted := pf.prereqs.Nodes
	if len(wanted) == 0 {
		pf.add(config.Finding{
			Code: "proxmox.node_missing", Severity: config.SeverityError, Setting: SettingNodes,
			Title:  "no node is configured",
			Detail: "there is nowhere for a machine to be built.",
			Fix:    "choose at least one node; this cluster offers " + join(nodeNames(nodes)) + ".",
		})
		return
	}

	var usable []string
	for _, name := range wanted {
		node, ok := known[name]
		switch {
		case !ok:
			pf.add(config.Finding{
				Code: "proxmox.node_missing", Severity: config.SeverityError, Setting: SettingNodes,
				Title:  fmt.Sprintf("this cluster has no node called %q", name),
				Detail: "either the name is wrong or the API token cannot see the node. The cluster reports " + join(nodeNames(nodes)) + ".",
				Fix:    "correct the node name, or grant the token Sys.Audit on /nodes so it can see the whole cluster.",
			})
		case !node.Online():
			pf.add(config.Finding{
				Code: "proxmox.node_offline", Severity: config.SeverityWarning, Setting: SettingNodes,
				Title:  fmt.Sprintf("node %s is %s", name, node.Status),
				Detail: "machines placed there will not be built until it comes back.",
				Fix:    "bring the node back, or take it out of this provider's nodes while it is down.",
			})
		default:
			usable = append(usable, name)
		}
	}

	for _, node := range usable {
		pf.checkStorage(ctx, node)
		pf.checkBridge(ctx, node)
	}
	pf.checkTemplate(ctx, usable)
}

func (pf *preflight) checkStorage(ctx context.Context, node string) {
	want := strings.TrimSpace(pf.prereqs.Storage)
	if want == "" {
		pf.add(config.Finding{
			Code: "proxmox.storage_missing", Severity: config.SeverityError, Setting: SettingStorage,
			Title:  "no storage is configured",
			Detail: "a clone has nowhere to put the machine's disk.",
			Fix:    "choose a storage that accepts disk images.",
		})
		return
	}
	storages, err := pf.client.Storages(ctx, node)
	if err != nil {
		pf.explainFailure(err, "list the storages on "+node)
		return
	}
	for _, s := range storages {
		if s.Storage != want {
			continue
		}
		switch {
		case !s.Accepts("images"):
			pf.add(config.Finding{
				Code: "proxmox.storage_no_images", Severity: config.SeverityError, Setting: SettingStorage,
				Title:  fmt.Sprintf("storage %s on %s does not hold disk images", want, node),
				Detail: fmt.Sprintf("it is configured for %q, so a clone into it will be refused.", s.Content),
				Fix:    "choose a storage whose content types include images, or add images to this one.",
			})
		case !bool(s.Enabled) || !bool(s.Active):
			pf.add(config.Finding{
				Code: "proxmox.storage_inactive", Severity: config.SeverityError, Setting: SettingStorage,
				Title:  fmt.Sprintf("storage %s is not active on %s", want, node),
				Detail: "a clone into a storage that is disabled or offline fails at the first machine.",
				Fix:    "enable the storage on this node, or place machines on a node where it is active.",
			})
		}
		return
	}
	pf.add(config.Finding{
		Code: "proxmox.storage_missing", Severity: config.SeverityError, Setting: SettingStorage,
		Title:  fmt.Sprintf("node %s has no storage called %q", node, want),
		Detail: "machines placed on this node cannot be given a disk. It offers " + join(storageNames(storages)) + ".",
		Fix:    "choose a storage this node can see -- a shared one if machines are placed on several nodes.",
	})
}

func (pf *preflight) checkBridge(ctx context.Context, node string) {
	want := strings.TrimSpace(pf.prereqs.Bridge)
	if want == "" {
		pf.add(config.Finding{
			Code: "proxmox.bridge_missing", Severity: config.SeverityError, Setting: SettingBridge,
			Title:  "no network bridge is configured",
			Detail: "a machine with no network cannot reach this controller, so it can never enrol.",
			Fix:    "choose the bridge the rest of this network uses, usually vmbr0.",
		})
		return
	}
	bridges, err := pf.client.Bridges(ctx, node)
	if err != nil {
		pf.explainFailure(err, "list the network bridges on "+node)
		return
	}
	for _, b := range bridges {
		if b.Iface == want {
			return
		}
	}
	pf.add(config.Finding{
		Code: "proxmox.bridge_missing", Severity: config.SeverityError, Setting: SettingBridge,
		Title:  fmt.Sprintf("node %s has no bridge called %q", node, want),
		Detail: "a machine built there would have no network and could never enrol. It offers " + join(bridgeNames(bridges)) + ".",
		Fix:    "use a bridge that exists on every node this provider places on.",
	})
}

func (pf *preflight) checkTemplate(ctx context.Context, usable []string) {
	if pf.prereqs.TemplateID <= 0 {
		pf.add(config.Finding{
			Code: "proxmox.template_missing", Severity: config.SeverityError, Setting: SettingTemplateID,
			Title:  "no template is configured",
			Detail: "every machine starts as a clone of one.",
			Fix:    "prepare a template as the runbook describes and give its VMID here.",
		})
		return
	}
	node := strings.TrimSpace(pf.prereqs.TemplateNode)
	if node == "" && len(usable) > 0 {
		node = usable[0]
	}
	if node == "" {
		return // The node findings above already say why nothing could be checked.
	}

	cfg, err := pf.client.VMConfig(ctx, node, pf.prereqs.TemplateID)
	if err != nil {
		if errors.Is(err, provider.ErrNotFound) {
			pf.add(config.Finding{
				Code: "proxmox.template_missing", Severity: config.SeverityError, Setting: SettingTemplateID,
				Title:  fmt.Sprintf("node %s has no guest with VMID %d", node, pf.prereqs.TemplateID),
				Detail: "there is nothing to clone.",
				Fix:    "give the VMID of the prepared template, and the node it lives on.",
			})
			return
		}
		pf.explainFailure(err, fmt.Sprintf("read the configuration of template %d on %s", pf.prereqs.TemplateID, node))
		return
	}
	if !bool(cfg.Template) {
		pf.add(config.Finding{
			Code: "proxmox.template_not_a_template", Severity: config.SeverityError, Setting: SettingTemplateID,
			Title:  fmt.Sprintf("VMID %d on %s is a virtual machine, not a template", pf.prereqs.TemplateID, node),
			Detail: "cloning a running guest copies whatever state it is in, and this fleet would be cloning something somebody else is using.",
			Fix:    "convert it to a template in the console, or point at one that already is.",
		})
		return
	}
	if !cfg.HasDisk() {
		pf.add(config.Finding{
			Code: "proxmox.template_no_disk", Severity: config.SeverityError, Setting: SettingTemplateID,
			Title: fmt.Sprintf("template %d on %s has no operating-system disk", pf.prereqs.TemplateID, node),
			Detail: "nothing is attached as scsi0, virtio0, sata0 or ide0, so a clone has nothing to boot from. " +
				"It will start, try to boot from the network, and never enrol, while being billed as a machine.",
			Fix: "attach the cloud image as the first disk (qm set <vmid> --scsi0 <storage>:0,import-from=<image> --boot order=scsi0), " +
				"using a storage that holds disk images, then convert it to a template again.",
		})
	}
	if pf.prereqs.GuestAgent && !cfg.AgentEnabled() {
		pf.add(config.Finding{
			Code: "proxmox.template_no_agent", Severity: config.SeverityWarning, Setting: SettingTemplateID,
			Title:  fmt.Sprintf("the QEMU guest agent is not enabled on template %d", pf.prereqs.TemplateID),
			Detail: "the agent is how the enrolment reaches a machine without ever putting it in the guest's metadata; machines cloned from this template will boot and never enrol.",
			Fix:    "set agent: 1 (Options -> QEMU Guest Agent) on the template, and make sure the guest has qemu-guest-agent installed.",
		})
	}
}

func fallback(s, or string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return or
}

func nodeNames(nodes []Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Node)
	}
	return out
}

func storageNames(storages []Storage) []string {
	out := make([]string, 0, len(storages))
	for _, s := range storages {
		if s.Accepts("images") {
			out = append(out, s.Storage)
		}
	}
	return out
}

func bridgeNames(bridges []NetworkInterface) []string {
	out := make([]string, 0, len(bridges))
	for _, b := range bridges {
		out = append(out, b.Iface)
	}
	return out
}

// join lists what the cluster actually offers, because a finding that says a
// name is wrong and does not say what the right ones are sends somebody to the
// console to look.
func join(names []string) string {
	switch len(names) {
	case 0:
		return "nothing this token can see"
	case 1:
		return names[0]
	default:
		return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
	}
}
