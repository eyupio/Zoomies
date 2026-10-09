package proxmox

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/provider"
)

// healthyPrereqs is a configuration the fake cluster can actually satisfy: one
// node, a storage that node can see and the token may allocate on, a bridge
// that exists, and the prepared template.
func healthyPrereqs() Prereqs {
	return Prereqs{
		Nodes: []string{"pve-1"}, Storage: "local-lvm", Bridge: "vmbr0",
		TemplateNode: "pve-1", TemplateID: 9000,
		VMIDMin: 9100, VMIDMax: 9199, GuestAgent: true,
	}
}

func codes(r provider.Report) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}

func finding(t *testing.T, r provider.Report, code string) config.Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Code == code {
			return f
		}
	}
	t.Fatalf("no %s finding; got %v", code, codes(r))
	return config.Finding{}
}

// Preflight is what an operator presses before anything is created, so a
// cluster that is ready has to come back with nothing to say.
func TestAClusterThatIsReadyPassesPreflight(t *testing.T) {
	f := newFakePVE(t, nil)

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	if !report.Reachable {
		t.Fatalf("the cluster answered but was reported unreachable: %v", codes(report))
	}
	if !report.OK() {
		t.Errorf("a ready cluster raised %v", codes(report))
	}
	if report.Version != "8.2.4" {
		t.Errorf("Version = %q; the qualification record is worth little without it", report.Version)
	}
}

// A missing privilege has to name the privilege and the path, because "403" or
// "check your permissions" sends an operator reading eleven role definitions to
// find out which of them they need.
func TestAMissingPrivilegeBecomesAFindingNamingIt(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetPermissions(Permissions{
		"/vms": {
			"VM.Allocate": true, "VM.Audit": true, "VM.PowerMgmt": true,
			"VM.Config.Disk": true, "VM.Config.CPU": true, "VM.Config.Memory": true,
			"VM.Config.Network": true, "VM.Config.Options": true,
			"VM.GuestAgent.FileWrite": true, "VM.GuestAgent.Unrestricted": true,
		},
		"/storage/local-lvm": {"Datastore.AllocateSpace": true},
	})

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	if report.OK() {
		t.Fatal("a token that cannot clone the template was reported as ready")
	}
	got := finding(t, report, "proxmox.privilege_missing")
	if got.Severity != config.SeverityError {
		t.Errorf("severity = %q, want error: nothing can be built without it", got.Severity)
	}
	for _, want := range []string{"VM.Clone", "/vms/9000"} {
		if !strings.Contains(got.Title, want) {
			t.Errorf("the title %q does not name %q", got.Title, want)
		}
	}
	for _, want := range []string{"pveum acl modify", "/vms/9000", tokenID, "VM.Clone"} {
		if !strings.Contains(got.Fix, want) {
			t.Errorf("the fix %q does not name %q", got.Fix, want)
		}
	}
	if !strings.Contains(got.Detail, "clon") {
		t.Errorf("the detail %q does not say what the privilege buys", got.Detail)
	}
}

// The bootstrap privileges are only asked for when the bootstrap needs them: a
// fleet told to leave the guest agent alone must not be warned about privileges
// it will never use.
func TestTheGuestAgentPrivilegesAreOnlyRequiredWhenTheAgentIsUsed(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetPermissions(Permissions{
		"/vms": {
			"VM.Clone": true, "VM.Allocate": true, "VM.Audit": true, "VM.PowerMgmt": true,
			"VM.Config.Disk": true, "VM.Config.CPU": true, "VM.Config.Memory": true,
			"VM.Config.Network": true, "VM.Config.Options": true,
		},
		"/storage/local-lvm": {"Datastore.AllocateSpace": true},
	})

	without := healthyPrereqs()
	without.GuestAgent = false
	if report := Preflight(context.Background(), f.client(t), without); !report.OK() {
		t.Errorf("a fleet that does not use the guest agent was refused: %v", codes(report))
	}

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	if report.OK() {
		t.Fatal("a bootstrap through the guest agent was accepted without the privileges it needs")
	}
	if !strings.Contains(finding(t, report, "proxmox.privilege_missing").Title, "VM.GuestAgent") {
		t.Error("the finding does not name the guest agent privilege")
	}
}

// A cluster-wide role is how most operators grant this, and a check that only
// looked at the exact path would tell somebody holding PVEVMAdmin on / that
// they are missing every privilege they have.
func TestAPrivilegeGrantedHigherUpCountsWhenItPropagates(t *testing.T) {
	tests := []struct {
		name  string
		perms Permissions
		path  string
		want  bool
	}{
		{"granted on the path itself", Permissions{"/vms": {"VM.Clone": true}}, "/vms", true},
		{"granted on the path itself without propagation", Permissions{"/vms": {"VM.Clone": false}}, "/vms", true},
		{"granted at the root and propagating", Permissions{"/": {"VM.Clone": true}}, "/vms/9000", true},
		{"granted at the root and not propagating", Permissions{"/": {"VM.Clone": false}}, "/vms/9000", false},
		{"granted one level up and propagating", Permissions{"/vms": {"VM.Clone": true}}, "/vms/9000", true},
		{"granted further down, which covers nothing above it", Permissions{"/vms/9000": {"VM.Clone": true}}, "/vms", false},
		{"a different privilege entirely", Permissions{"/vms": {"VM.Audit": true}}, "/vms", false},
		{"a token with nothing at all", Permissions{}, "/vms", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPrivilege(tc.perms, tc.path, "VM.Clone"); got != tc.want {
				t.Errorf("hasPrivilege(%v, %q) = %v, want %v", tc.perms, tc.path, got, tc.want)
			}
		})
	}
}

// A pool is the tighter way to grant this, and an operator who chose it is
// right: privileges on the pool cover the guests inside it.
func TestAPrivilegeGrantedOnThePoolIsEnough(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetPermissions(Permissions{
		"/pool/zoomies": {
			"VM.Clone": true, "VM.Allocate": true, "VM.Audit": true, "VM.PowerMgmt": true,
			"VM.Config.Disk": true, "VM.Config.CPU": true, "VM.Config.Memory": true,
			"VM.Config.Network": true, "VM.Config.Options": true,
			"VM.GuestAgent.FileWrite": true, "VM.GuestAgent.Unrestricted": true,
		},
		"/storage/local-lvm": {"Datastore.AllocateSpace": true},
	})
	prereqs := healthyPrereqs()
	prereqs.Pool = "zoomies"

	if report := Preflight(context.Background(), f.client(t), prereqs); !report.OK() {
		t.Errorf("a token scoped to the pool it creates in was refused: %v", codes(report))
	}
}

// Preflight never returns an error: a cluster that cannot be reached is an
// answer an operator can act on, and one problem rather than eleven.
func TestPreflightReportsASilentClusterRatherThanFailing(t *testing.T) {
	c, err := New(Options{Endpoint: "https://" + deadAddress(t), TokenID: tokenID, Secret: tokenSecret, Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	report := Preflight(context.Background(), c, healthyPrereqs())
	if report.Reachable {
		t.Error("a cluster that never answered was reported reachable")
	}
	if report.OK() {
		t.Error("a cluster that never answered was reported as ready")
	}
	got := finding(t, report, "proxmox.unreachable")
	if got.Severity != config.SeverityError {
		t.Errorf("severity = %q, want error", got.Severity)
	}
	if got.Fix == "" {
		t.Error("the finding says nothing to do about it")
	}
	// Listing the ten checks that could not run would bury the one that matters.
	if len(report.Findings) > 2 {
		t.Errorf("an unreachable cluster produced %d findings: %v", len(report.Findings), codes(report))
	}
}

// A refused credential and a missing privilege lead to different afternoons, so
// preflight keeps them apart even when both arrive as a failed call.
func TestARefusedTokenIsAFindingOfItsOwn(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetError(http.MethodGet, "/version", http.StatusUnauthorized, "authentication failure")

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	got := finding(t, report, "proxmox.credentials_refused")
	if !strings.Contains(got.Fix, "secret") {
		t.Errorf("the fix %q does not say what to check", got.Fix)
	}
}

// The range needs no hypervisor, so it is answered even when nothing else can
// be: a provider with no range configured is misconfigured either way.
func TestAVMIDRangeIsCheckedEvenWhenTheClusterIsSilent(t *testing.T) {
	c, err := New(Options{Endpoint: "https://" + deadAddress(t), TokenID: tokenID, Secret: tokenSecret, Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tests := []struct {
		name     string
		lo, hi   int
		wantCode string
	}{
		{"no range at all", 0, 0, "proxmox.vmid_range"},
		{"a range that runs backwards", 9199, 9100, "proxmox.vmid_range"},
		{"a range inside what Proxmox reserves", 50, 99, "proxmox.vmid_range_reserved"},
		{"a range of one", 9100, 9100, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prereqs := healthyPrereqs()
			prereqs.VMIDMin, prereqs.VMIDMax = tc.lo, tc.hi
			report := Preflight(context.Background(), c, prereqs)
			has := false
			for _, code := range codes(report) {
				if code == tc.wantCode {
					has = true
				}
				if strings.HasPrefix(code, "proxmox.vmid_range") && tc.wantCode == "" {
					t.Errorf("a usable range raised %s", code)
				}
			}
			if tc.wantCode != "" && !has {
				t.Errorf("got %v, want %s", codes(report), tc.wantCode)
			}
		})
	}
}

// Every resource an operator named has to exist and be usable, and every
// finding has to say what the cluster does offer -- one that says a name is
// wrong without saying what is right sends somebody to the console to look.
func TestEveryConfiguredResourceIsChecked(t *testing.T) {
	tests := []struct {
		name     string
		adjust   func(*Prereqs)
		wantCode string
		wantIn   string
	}{
		{"a node this cluster does not have", func(p *Prereqs) { p.Nodes = []string{"pve-9"} }, "proxmox.node_missing", "pve-1"},
		{"a node that is down", func(p *Prereqs) { p.Nodes = []string{"pve-1", "pve-3"} }, "proxmox.node_offline", "pve-3"},
		{"no node at all", func(p *Prereqs) { p.Nodes = nil }, "proxmox.node_missing", "pve-2"},
		{"a storage that node cannot see", func(p *Prereqs) { p.Storage = "nvme-fast" }, "proxmox.storage_missing", "local-lvm"},
		{"a storage that holds no disk images", func(p *Prereqs) { p.Storage = "local" }, "proxmox.storage_no_images", "iso,vztmpl"},
		{"a bridge that does not exist", func(p *Prereqs) { p.Bridge = "vmbr9" }, "proxmox.bridge_missing", "vmbr0"},
		{"no bridge at all", func(p *Prereqs) { p.Bridge = "" }, "proxmox.bridge_missing", "vmbr0"},
		{"a template that is not there", func(p *Prereqs) { p.TemplateID = 9999 }, "proxmox.template_missing", "9999"},
		{"a running machine offered as a template", func(p *Prereqs) { p.TemplateID = 100 }, "proxmox.template_not_a_template", "100"},
		{"no template at all", func(p *Prereqs) { p.TemplateID = 0 }, "proxmox.template_missing", "clone"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePVE(t, nil)
			prereqs := healthyPrereqs()
			tc.adjust(&prereqs)

			report := Preflight(context.Background(), f.client(t), prereqs)
			got := finding(t, report, tc.wantCode)
			if !strings.Contains(got.Title+" "+got.Detail+" "+got.Fix, tc.wantIn) {
				t.Errorf("the finding does not mention %q: %+v", tc.wantIn, got)
			}
			if got.Setting == "" {
				t.Error("the finding names no setting, so the wizard cannot point at the field")
			}
		})
	}
}

// A template with the guest agent switched off produces machines that boot and
// never enrol, which is a slow and baffling failure to debug from the outside.
func TestATemplateWithoutTheGuestAgentIsWarnedAbout(t *testing.T) {
	f := newFakePVE(t, nil)
	f.mu.Lock()
	f.vms[9000].agent = ""
	f.mu.Unlock()

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	got := finding(t, report, "proxmox.template_no_agent")
	if got.Severity != config.SeverityWarning {
		t.Errorf("severity = %q, want a warning: the machine may still be built", got.Severity)
	}
	if !strings.Contains(got.Fix, "qemu-guest-agent") {
		t.Errorf("the fix %q does not say what the template needs", got.Fix)
	}
}

// A template with no disk is the quiet failure: the clone starts, finds nothing
// to boot, falls through to a network boot, and sits there costing money while
// nothing looks wrong from the controller's side.
func TestATemplateWithNoDiskIsAnError(t *testing.T) {
	f := newFakePVE(t, nil)
	f.mu.Lock()
	f.vms[9000].disk = ""
	f.mu.Unlock()

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	got := finding(t, report, "proxmox.template_no_disk")
	if got.Severity != config.SeverityError {
		t.Errorf("severity = %q, want an error: no machine made from it can ever enrol", got.Severity)
	}
	if got.Setting == "" || !strings.Contains(got.Fix, "scsi0") {
		t.Errorf("the finding does not say what to attach or where to fix it: %+v", got)
	}
}

// A cloud-init drive or a CD-ROM sits in the same slots a disk would, and
// neither is something a machine boots into.
func TestADriveThatCannotBeBootedDoesNotCountAsADisk(t *testing.T) {
	for name, cfg := range map[string]VMConfig{
		"nothing":        {},
		"a cd-rom":       {IDE0: "local:iso/ubuntu.iso,media=cdrom"},
		"a cloud-init":   {SCSI0: "local-lvm:vm-9000-cloudinit,media=cdrom"},
		"a blank string": {SCSI0: "  "},
	} {
		if cfg.HasDisk() {
			t.Errorf("%s was counted as a disk", name)
		}
	}
	for name, cfg := range map[string]VMConfig{
		"scsi":   {SCSI0: "local-lvm:base-9000-disk-0,size=32G"},
		"virtio": {VirtIO0: "local-lvm:base-9000-disk-0,size=32G"},
		"sata":   {SATA0: "local-lvm:base-9000-disk-0"},
	} {
		if !cfg.HasDisk() {
			t.Errorf("%s was not counted as a disk", name)
		}
	}
}

// An unqualified release is not a broken one. Refusing would strand an operator
// whose cluster may work perfectly well; the warning is what tells them nobody
// has checked.
func TestAnUnqualifiedProxmoxWarnsRatherThanStops(t *testing.T) {
	f := newFakePVE(t, map[string]http.HandlerFunc{
		"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) {
			writeData(w, map[string]any{"version": "7.4-17", "release": "7.4"})
		},
	})

	report := Preflight(context.Background(), f.client(t), healthyPrereqs())
	got := finding(t, report, "proxmox.version_unqualified")
	if got.Severity != config.SeverityWarning {
		t.Errorf("severity = %q, want a warning", got.Severity)
	}
	if !report.OK() {
		t.Errorf("an older release stopped a provider that is otherwise ready: %v", codes(report))
	}
	if !strings.Contains(got.Title, MinPVEVersion) {
		t.Errorf("the title %q does not say what was qualified", got.Title)
	}
}

// Verification turned off is the setting that weakens the posture most, so it
// is said every time preflight runs rather than once in a log nobody kept.
func TestVerificationTurnedOffIsAWarningEveryTime(t *testing.T) {
	f := newFakePVE(t, nil)
	c, err := New(Options{Endpoint: f.URL, TokenID: tokenID, Secret: tokenSecret, Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for i := range 2 {
		report := Preflight(context.Background(), c, healthyPrereqs())
		got := finding(t, report, "proxmox.insecure_tls")
		if got.Severity != config.SeverityWarning {
			t.Errorf("pass %d: severity = %q, want a warning", i, got.Severity)
		}
		if !strings.Contains(got.Fix, "pve-root-ca.pem") {
			t.Errorf("pass %d: the fix %q does not say what to paste", i, got.Fix)
		}
	}
}
