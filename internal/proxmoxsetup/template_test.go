package proxmoxsetup

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func templateHost(t *testing.T, inventory, config string) (Host, *[]string) {
	t.Helper()
	var commands []string
	h := Host{
		WriteFile: func(path string, data []byte, mode os.FileMode) error {
			if !strings.HasSuffix(path, "/zoomies-agent.service") || mode != 0600 {
				t.Fatalf("unexpected host write: %s %v", path, mode)
			}
			for _, want := range []string{"EnvironmentFile=-/etc/zoomies/zoomies.env", "SupplementaryGroups=docker", "zoomies agent"} {
				if !strings.Contains(string(data), want) {
					t.Errorf("runner unit missing %q", want)
				}
			}
			return nil
		},
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			line := name + " " + strings.Join(args, " ")
			commands = append(commands, line)
			switch {
			case strings.Contains(line, "/cluster/resources"):
				return []byte(inventory), nil
			case strings.Contains(line, "/config"):
				return []byte(config), nil
			case strings.Contains(line, "/storage"):
				return []byte(`[{"storage":"backup","active":1,"content":"backup","avail":999999999999},{"storage":"local","active":1,"content":"images,iso","avail":50000000000}]`), nil
			case strings.Contains(line, "/network"):
				return []byte(`[{"iface":"vmbr1","type":"bridge","active":1},{"iface":"vmbr0","type":"bridge","active":1}]`), nil
			case strings.Contains(line, "/cluster/nextid"):
				return []byte("9101\n"), nil
			}
			return nil, nil
		},
	}
	return h, &commands
}

func TestSetupCreatesTheRunnerTemplateAlongsideUnrelatedTemplates(t *testing.T) {
	h, commands := templateHost(t, `[{"vmid":200,"name":"other-template","node":"pve","type":"qemu","template":1}]`, "")
	templates, err := EnsureTemplate(context.Background(), h, "/provider", "/provider/zoomies", "pve", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 2 || templates[0].VMID != 9101 || templates[0].Name != TemplateName || templates[0].Node != "pve" {
		t.Fatalf("wrong template inventory: %+v", templates)
	}
	joined := strings.Join(*commands, "\n")
	for _, want := range []string{"/cluster/nextid --vmid 9100", "qm create 9101", "--net0 virtio,bridge=vmbr0", "--scsi0 local:0,import-from=/provider/template.img", "--ide2 local:cloudinit", "qm resize 9101 scsi0 32G", "qm template 9101"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(joined, "backup:0") || strings.Contains(joined, "qm create 200") {
		t.Fatal("used backup storage or changed an unrelated template")
	}
}

func TestSetupReusesAnExistingZoomiesTemplateWithoutChangingGuests(t *testing.T) {
	h, commands := templateHost(t, `[{"vmid":200,"name":"other-template","node":"pve","type":"qemu","template":1},{"vmid":9105,"name":"zoomies-template","node":"pve-2","type":"qemu","template":1}]`, "")
	templates, err := EnsureTemplate(context.Background(), h, "/provider", "/provider/zoomies", "pve", "abc")
	if err != nil || len(templates) != 2 || templates[0].VMID != 9105 || templates[0].Node != "pve-2" || len(*commands) != 1 {
		t.Fatalf("did not reuse the template: %+v %v %v", templates, err, *commands)
	}
}

func TestSetupResumesItsImportedDiskAndFinishesCloudInit(t *testing.T) {
	h, commands := templateHost(t, `[{"vmid":9101,"name":"zoomies-template","node":"pve","type":"qemu"}]`, `{"description":"Zoomies template setup abc","scsi0":"local:vm-9101-disk-0,size=32G"}`)
	if _, err := EnsureTemplate(context.Background(), h, "/provider", "/provider/zoomies", "pve", "abc"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*commands, "\n")
	if strings.Contains(joined, "bash ") || strings.Contains(joined, "qm create") || !strings.Contains(joined, "--ide2 local:cloudinit") || !strings.Contains(joined, "qm template 9101") {
		t.Fatalf("unsafe or incomplete retry: %v", *commands)
	}
	if strings.Contains(joined, "qm resize") {
		t.Fatal("retried an equal-size disk resize")
	}
}

func TestTemplateCallbackOrderSurvivesAnInventoryReorder(t *testing.T) {
	first := templatesWithPreferred([]Template{{VMID: 200}, {VMID: 300}, {VMID: 9100, Name: TemplateName}}, 9100)
	second := templatesWithPreferred([]Template{{VMID: 300}, {VMID: 9100, Name: TemplateName}, {VMID: 200}}, 9100)
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("retry changed callback order: %v %v", first, second)
		}
	}
}

func TestSetupRefusesAnUnrelatedGuestWithTheTemplateName(t *testing.T) {
	h, commands := templateHost(t, `[{"vmid":101,"name":"zoomies-template","node":"pve","type":"qemu"}]`, `{"description":"somebody else's VM","scsi0":"local:disk"}`)
	if _, err := EnsureTemplate(context.Background(), h, "/provider", "/provider/zoomies", "pve", "abc"); err == nil {
		t.Fatal("accepted an unrelated guest")
	}
	if len(*commands) != 2 {
		t.Fatalf("modified an unrelated guest: %v", *commands)
	}
}

func TestTemplatePreparationFailureNeverCreatesAVMOrReportsReady(t *testing.T) {
	for _, stage := range []string{"/cluster/resources", "bash -c", "qm template"} {
		t.Run(stage, func(t *testing.T) {
			h, commands := templateHost(t, `[]`, "")
			run := h.Run
			h.Run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if strings.Contains(name+" "+strings.Join(args, " "), stage) {
					return nil, errors.New("failed")
				}
				return run(ctx, name, args...)
			}
			templates, err := EnsureTemplate(context.Background(), h, "/provider", "/provider/zoomies", "pve", "abc")
			if err == nil || templates != nil {
				t.Fatalf("failed template reported ready: %v %v", templates, err)
			}
			if stage != "qm template" && strings.Contains(strings.Join(*commands, "\n"), "qm create") {
				t.Fatal("created a VM before the image was ready")
			}
		})
	}
}

func TestImagePreparationScriptIsValidAndLeavesAnUnenrolledAgent(t *testing.T) {
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(prepareTemplateImage)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid image script: %v %s", err, out)
	}
	for _, want := range []string{"sha256sum -c", "--install docker.io,qemu-guest-agent", "systemctl disable zoomies-agent.service", "cloud-init clean --logs --machine-id"} {
		if !strings.Contains(prepareTemplateImage, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func bridgeHost(inventory string) Host {
	return Host{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pvesh" || !strings.Contains(strings.Join(args, " "), "/nodes/pve/network") {
			return nil, errors.New("unexpected command")
		}
		return []byte(inventory), nil
	}}
}

// A node whose bridge is not vmbr0 is the case that used to be saved with a
// bridge that does not exist, so the choice has to follow the node.
func TestActiveBridgePrefersVmbr0ButFollowsTheNode(t *testing.T) {
	for name, tc := range map[string]struct{ inventory, want string }{
		"vmbr0 among others": {`[{"iface":"vmbr1","type":"bridge","active":1},{"iface":"vmbr0","type":"bridge","active":1}]`, "vmbr0"},
		"no vmbr0":           {`[{"iface":"eno1","type":"eth","active":1},{"iface":"vmbr7","type":"bridge","active":1}]`, "vmbr7"},
		"vmbr0 is down":      {`[{"iface":"vmbr0","type":"bridge","active":0},{"iface":"vmbr1","type":"bridge","active":1}]`, "vmbr1"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ActiveBridge(context.Background(), bridgeHost(tc.inventory), "pve")
			if err != nil || got != tc.want {
				t.Fatalf("ActiveBridge = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := ActiveBridge(context.Background(), bridgeHost(`[{"iface":"eno1","type":"eth","active":1}]`), "pve"); err == nil {
		t.Fatal("a node with no bridge was given one")
	}
}

func TestAConnectionRefusesAnUnsafeBridgeName(t *testing.T) {
	c := Connection{Name: "proxmox-pve", Endpoint: "https://pve:8006", Credential: "u@pve!t=secret", Bridge: "vmbr0; reboot"}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("Validate = %v, want a refusal that names the bridge", err)
	}
}

func TestVerifyAccessSavesNothingForATokenThatCannotDoTheWork(t *testing.T) {
	var granted []string
	h := Host{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		granted = append(granted, name+" "+strings.Join(args, " "))
		return nil, nil
	}}

	// Fine as it stands: no grant is made, and nothing is asked of Proxmox.
	if err := VerifyAccess(context.Background(), h, "zoomies-abc@pve!provider-1", "Zoomies-abc", "pve", func(context.Context) []string { return nil }); err != nil || len(granted) != 0 {
		t.Fatalf("a healthy token was touched: %v %v", err, granted)
	}

	// Cured by the one grant it is allowed to make: the second check passes.
	calls := 0
	err := VerifyAccess(context.Background(), h, "zoomies-abc@pve!provider-1", "Zoomies-abc", "pve", func(context.Context) []string {
		calls++
		if calls == 1 {
			return []string{"cannot list bridges"}
		}
		return nil
	})
	if err != nil || len(granted) != 4 || !strings.Contains(strings.Join(granted, "\n"), "acl modify /nodes/pve --tokens zoomies-abc@pve!provider-1 --roles Zoomies-abc") || !strings.Contains(strings.Join(granted, "\n"), "acl modify /sdn/zones/localnetwork --tokens zoomies-abc@pve!provider-1 --roles Zoomies-abc") {
		t.Fatalf("a repairable token: err=%v grants=%v", err, granted)
	}

	// Not cured: setup stops and says why, naming the command to look further.
	err = VerifyAccess(context.Background(), h, "zoomies-abc@pve!provider-1", "Zoomies-abc", "pve", func(context.Context) []string { return []string{"cannot list bridges"} })
	if err == nil || !strings.Contains(err.Error(), "cannot list bridges") || !strings.Contains(err.Error(), "pveum user permissions") || !strings.Contains(err.Error(), "nothing was saved") {
		t.Fatalf("an unrepaired token: %v", err)
	}
}
