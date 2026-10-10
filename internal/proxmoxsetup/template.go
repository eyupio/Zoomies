package proxmoxsetup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/eyupio/zoomies/internal/installer"
)

const TemplateName = "zoomies-template"

// DefaultBridge is the bridge a stock Proxmox install creates, and the one a
// guest is attached to when the node has it and nothing else is asked for.
const DefaultBridge = "vmbr0"

// ActiveBridge names the bridge a guest on this node should attach to: vmbr0
// when it is up, otherwise the first active bridge. It is read from the node
// itself, as root, so it does not depend on what an API token may list. The
// template is built on it and the provider is saved with it, so a node whose
// bridge is not vmbr0 needs nothing typed by hand.
func ActiveBridge(ctx context.Context, h Host, node string) (string, error) {
	raw, err := h.Run(ctx, "pvesh", "get", "/nodes/"+node+"/network", "--output-format", "json")
	if err != nil {
		return "", errors.New("proxmox setup: pvesh failed; correct the host configuration and rerun setup")
	}
	var networks []struct {
		Interface string `json:"iface"`
		Type      string `json:"type"`
		Active    int    `json:"active"`
	}
	if json.Unmarshal(raw, &networks) != nil {
		return "", errors.New("proxmox setup: invalid network inventory")
	}
	bridge := ""
	for _, n := range networks {
		if n.Type == "bridge" && n.Active == 1 && (bridge == "" || n.Interface == DefaultBridge) {
			bridge = n.Interface
		}
	}
	if bridge == "" {
		return "", errors.New("proxmox setup: create an active network bridge on this node, then retry")
	}
	return bridge, nil
}

// EnsureTemplate prepares the image locally, before reserving a VMID. An
// interrupted import is resumed only when the VM carries this setup's marker;
// a guest with the same name is never enough evidence to change it.
func EnsureTemplate(ctx context.Context, h Host, dir, binary, node, key string) ([]Template, error) {
	run := func(name string, args ...string) ([]byte, error) {
		out, err := h.Run(ctx, name, args...)
		if err != nil {
			return nil, fmt.Errorf("proxmox setup: %s %s failed; correct the host configuration and rerun setup", name, args[0])
		}
		return out, nil
	}
	raw, err := run("pvesh", "get", "/cluster/resources", "--type", "vm", "--output-format", "json")
	if err != nil {
		return nil, err
	}
	var guests []struct {
		VMID     int    `json:"vmid"`
		Name     string `json:"name"`
		Node     string `json:"node"`
		Type     string `json:"type"`
		Template int    `json:"template"`
	}
	if json.Unmarshal(raw, &guests) != nil {
		return nil, errors.New("proxmox setup: invalid VM inventory")
	}
	var templates []Template
	vmid := 0
	preferred := 0
	for _, vm := range guests {
		if vm.Type != "qemu" {
			continue
		}
		if vm.Template == 1 {
			templates = append(templates, Template{VMID: vm.VMID, Name: vm.Name, Node: vm.Node})
		}
		if vm.Name != TemplateName {
			continue
		}
		if vm.Template == 1 {
			preferred = vm.VMID
			continue
		}
		if vmid != 0 || vm.Node != node {
			return nil, errors.New("proxmox setup: a VM named zoomies-template already exists; finish or rename it before retrying")
		}
		vmid = vm.VMID
	}
	if preferred != 0 {
		return templatesWithPreferred(templates, preferred), nil
	}
	marker := "Zoomies template setup " + key
	config := map[string]any{}
	if vmid != 0 {
		raw, err := run("pvesh", "get", fmt.Sprintf("/nodes/%s/qemu/%d/config", node, vmid), "--output-format", "json")
		if err != nil {
			return nil, err
		}
		if json.Unmarshal(raw, &config) != nil || config["description"] != marker {
			return nil, errors.New("proxmox setup: the existing zoomies-template VM was not created by this setup; finish or rename it before retrying")
		}
	}
	if config["scsi0"] == nil {
		raw, err = run("pvesh", "get", "/nodes/"+node+"/storage", "--output-format", "json")
		if err != nil {
			return nil, err
		}
		var storages []struct {
			Storage string `json:"storage"`
			Content string `json:"content"`
			Active  int    `json:"active"`
			Avail   int64  `json:"avail"`
		}
		if json.Unmarshal(raw, &storages) != nil {
			return nil, errors.New("proxmox setup: invalid storage inventory")
		}
		storage := ""
		var available int64
		for _, s := range storages {
			if s.Active != 1 || !strings.Contains(","+s.Content+",", ",images,") || s.Avail < 33<<30 {
				continue
			}
			if storage == "" || s.Avail > available {
				storage, available = s.Storage, s.Avail
			}
		}
		if storage == "" {
			return nil, errors.New("proxmox setup: enable storage for disk images with at least 33 GiB free on this node, then retry")
		}
		bridge, err := ActiveBridge(ctx, h, node)
		if err != nil {
			return nil, err
		}
		unit, err := installer.RenderSystemdUnit(installer.ServiceSpec{
			Unit: installer.UnitAgent, ExecPath: "/usr/local/bin/zoomies", ConfigFile: "/etc/zoomies/zoomies.yaml",
			User: "zoomies", Group: "zoomies", StateDir: "/var/lib/zoomies", ConfigDir: "/etc/zoomies",
			EnvFile: installer.MachineEnvPath, WantsDocker: true, RuntimeName: "docker", SupplementaryGroups: []string{"docker"},
		})
		if err != nil {
			return nil, err
		}
		unitPath := filepath.Join(dir, "zoomies-agent.service")
		if err := h.WriteFile(unitPath, []byte(unit), 0600); err != nil {
			return nil, err
		}
		if _, err := run("bash", "-c", prepareTemplateImage, "zoomies-template", dir, binary, unitPath); err != nil {
			return nil, errors.New("proxmox setup: runner image preparation failed; check outbound access to cloud-images.ubuntu.com and Ubuntu package mirrors, and that libguestfs-tools can be installed, then retry")
		}
		if vmid == 0 {
			// The default runner block is 9000-9099. Keep the template outside it.
			raw, err = run("pvesh", "get", "/cluster/nextid", "--vmid", "9100")
			if err != nil {
				return nil, err
			}
			vmid, err = strconv.Atoi(strings.TrimSpace(string(raw)))
			if err != nil || vmid < 9100 || vmid > 999999999 {
				return nil, errors.New("proxmox setup: Proxmox did not return an available template VMID")
			}
			if _, err := run("qm", "create", strconv.Itoa(vmid), "--name", TemplateName, "--description", marker,
				"--memory", "4096", "--cores", "2", "--cpu", "host", "--ostype", "l26",
				"--net0", "virtio,bridge="+bridge, "--scsihw", "virtio-scsi-pci", "--agent", "enabled=1", "--serial0", "socket", "--vga", "serial0"); err != nil {
				return nil, err
			}
		}
		id := strconv.Itoa(vmid)
		if _, err := run("qm", "set", id, "--scsi0", storage+":0,import-from="+filepath.Join(dir, "template.img"), "--boot", "order=scsi0"); err != nil {
			return nil, err
		}
		config["scsi0"] = storage + ":disk"
	}
	id := strconv.Itoa(vmid)
	if config["ide2"] == nil {
		disk, _ := config["scsi0"].(string)
		storage, _, ok := strings.Cut(disk, ":")
		if !ok || storage == "" {
			return nil, errors.New("proxmox setup: the interrupted template has an invalid disk; inspect it before retrying")
		}
		if _, err := run("qm", "set", id, "--ide2", storage+":cloudinit", "--ciuser", "ubuntu", "--ipconfig0", "ip=dhcp"); err != nil {
			return nil, err
		}
	}
	// Proxmox refuses an equal-size resize. A retry after a successful resize
	// must still be able to finish the conversion.
	disk, _ := config["scsi0"].(string)
	if !strings.Contains(","+disk+",", ",size=32G,") {
		if _, err := run("qm", "resize", id, "scsi0", "32G"); err != nil {
			return nil, err
		}
	}
	if _, err := run("qm", "template", id); err != nil {
		return nil, err
	}
	// The imported disk is now owned by Proxmox; keep no second image copy on
	// the host once conversion has succeeded.
	_, _ = h.Run(ctx, "rm", "-f", "--", filepath.Join(dir, "template.img"))
	return templatesWithPreferred(append(templates, Template{VMID: vmid, Name: TemplateName, Node: node}), vmid), nil
}

func templatesWithPreferred(templates []Template, id int) []Template {
	// The callback acknowledges byte-identical retries after a lost response.
	// Cluster inventory order must not change its payload between runs.
	sort.Slice(templates, func(i, j int) bool {
		if templates[i].VMID == id {
			return templates[j].VMID != id
		}
		if templates[j].VMID == id {
			return false
		}
		return templates[i].VMID < templates[j].VMID
	})
	return templates
}

// Only the guest image receives Docker and the runner service. The host's
// runner installation and credentials never enter the image.
const prepareTemplateImage = `set -eu
dir=$1
binary=$2
unit=$3
[ "$(uname -m)" = x86_64 ] || { echo 'Automatic templates need an x86_64 Proxmox host'; exit 1; }
if ! command -v virt-customize >/dev/null; then
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y libguestfs-tools
fi
tmp=$(mktemp -d "$dir/image.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
base=https://cloud-images.ubuntu.com/noble/current
asset=noble-server-cloudimg-amd64.img
curl --proto '=https' --proto-redir '=https' -fsSL "$base/$asset" -o "$tmp/$asset"
curl --proto '=https' --proto-redir '=https' -fsSL "$base/SHA256SUMS" -o "$tmp/SHA256SUMS"
(cd "$tmp"; awk -v asset="$asset" '$2 == asset || $2 == "*" asset { print; found=1 } END { if (!found) exit 1 }' SHA256SUMS > selected; sha256sum -c selected)
export LIBGUESTFS_BACKEND=direct
qemu-img create -f qcow2 "$tmp/prepared.img" 8G
virt-resize --expand /dev/sda1 "$tmp/$asset" "$tmp/prepared.img"
virt-customize -a "$tmp/prepared.img" --install docker.io,qemu-guest-agent \
  --upload "$binary:/usr/local/bin/zoomies" --chmod 0755:/usr/local/bin/zoomies \
  --upload "$unit:/etc/systemd/system/zoomies-agent.service" --chmod 0644:/etc/systemd/system/zoomies-agent.service \
  --run-command 'set -e; useradd --system --user-group --home-dir /var/lib/zoomies --shell /usr/sbin/nologin zoomies; usermod -aG docker zoomies' \
  --run-command 'set -e; mkdir -p /etc/zoomies /var/lib/zoomies/work; printf "agent:\n  embedded: false\n  work_dir: /var/lib/zoomies/work\n" > /etc/zoomies/zoomies.yaml; chown -R zoomies:zoomies /etc/zoomies /var/lib/zoomies' \
  --run-command 'set -e; systemctl enable docker.service qemu-guest-agent.service; systemctl disable zoomies-agent.service' \
  --run-command 'set -e; cloud-init clean --logs --machine-id; rm -f /etc/ssh/ssh_host_*; rm -f /var/lib/dbus/machine-id'
mv "$tmp/prepared.img" "$dir/template.img"
`
