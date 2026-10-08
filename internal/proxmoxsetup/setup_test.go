package proxmoxsetup

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"github.com/tailscale/tailcat"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func hostCertificate(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "pve.example"}, DNSNames: []string{"pve.example"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestPrepareDetectsConnectionAndReusesCredential(t *testing.T) {
	cert := hostCertificate(t)
	files := map[string][]byte{"/etc/pve/pve-root-ca.pem": cert, "/etc/pve/local/pve-ssl.pem": cert, "/etc/hostname": []byte("pve-1\n"), "/var/lib/zoomies/agent.json": []byte("existing runner credentials")}
	var commands []string
	h := Host{
		ReadFile: func(path string) ([]byte, error) {
			if b, ok := files[path]; ok {
				return b, nil
			}
			return nil, os.ErrNotExist
		},
		WriteFile: func(path string, b []byte, mode os.FileMode) error {
			if mode != 0600 {
				t.Fatalf("credential mode: %v", mode)
			}
			files[path] = b
			return nil
		},
		Run: func(_ context.Context, name string, argv ...string) ([]byte, error) {
			line := name + " " + strings.Join(argv, " ")
			commands = append(commands, line)
			if strings.Contains(line, "token add") {
				return []byte(`{"value":"token-secret"}`), nil
			}
			return nil, nil
		},
	}
	dir := "/var/lib/zoomies-proxmox/abc"
	c, err := Prepare(context.Background(), h, dir, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "proxmox-pve-1" || c.Endpoint != "https://pve.example:8006" || c.Credential != "zoomies-abc@pve!provider-"+InstanceKey("pve-1")+"=token-secret" || c.CAPEM != string(cert) {
		t.Fatalf("unexpected connection: %+v", c)
	}
	if string(files["/var/lib/zoomies/agent.json"]) != "existing runner credentials" {
		t.Fatal("runner state changed")
	}
	if !strings.Contains(strings.Join(commands, "\n"), "--privsep 1") || !strings.Contains(strings.Join(commands, "\n"), "--tokens zoomies-abc@pve!provider-"+InstanceKey("pve-1")) {
		t.Fatalf("missing separated token ACLs: %v", commands)
	}
	commands = nil
	again, err := Prepare(context.Background(), h, dir, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if c.Credential != again.Credential || len(commands) != 0 {
		t.Fatal("retry rotated token or modified Proxmox")
	}
	files["/etc/hostname"] = []byte("pve-2\n")
	second, err := Prepare(context.Background(), h, "/another-node/abc", "abc")
	if err != nil {
		t.Fatal(err)
	}
	if second.Credential == c.Credential || string(files[dir+"/credential"]) != c.Credential {
		t.Fatal("adding another cluster node reused or rotated the first node's token")
	}

	// A first run can stop after pveum creates a token but before saving it.
	previousRun := h.Run
	attempts := 0
	h.Run = func(ctx context.Context, command string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "token add") {
			attempts++
			if attempts == 1 {
				return nil, errors.New("token already exists")
			}
		}
		return previousRun(ctx, command, args...)
	}
	recovered, err := Prepare(context.Background(), h, "/interrupted-node/abc", "abc")
	if err != nil || attempts != 2 || recovered.Credential == second.Credential {
		t.Fatalf("partial setup not recovered: %v", err)
	}
	if strings.Contains(strings.Join(commands, "\n"), "token remove") {
		t.Fatal("recovery revoked another connection")
	}
}

func TestPrepareRefusesUnreadableCredentialInsteadOfRotatingIt(t *testing.T) {
	cert := hostCertificate(t)
	h := Host{ReadFile: func(path string) ([]byte, error) {
		switch path {
		case "/etc/pve/pve-root-ca.pem", "/etc/pve/local/pveproxy-ssl.pem":
			return cert, nil
		case "/etc/hostname":
			return []byte("pve"), nil
		default:
			return nil, os.ErrPermission
		}
	}, Run: func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("attempted to rotate token")
		return nil, nil
	}}
	if _, err := Prepare(context.Background(), h, "/provider", "abc"); !os.IsPermission(err) {
		t.Fatalf("unreadable token: %v", err)
	}
}

func TestSetupCommandIsStandaloneAndShellQuoted(t *testing.T) {
	controller := "https://zoomies.example/a'$(touch should-not-exist)"
	command := Command(controller, "pvs_abc", "secret", "v1.2.3")
	cmd := exec.Command("bash", "-n")
	cmd.Stdin = strings.NewReader(command)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("invalid shell: %v %s", err, out)
	}
	for _, forbidden := range []string{"install.sh", "agent join", "zoomies-agent.service", "/usr/local/bin/zoomies"} {
		if strings.Contains(command, forbidden) {
			t.Fatalf("command touches runner installation: %s", forbidden)
		}
	}
	if strings.Index(command, "completed-") > strings.Index(command, "curl ") {
		t.Fatal("receipt checked after download")
	}
	if !strings.Contains(command, "sha256sum -c") || !strings.Contains(command, "/var/lib/zoomies-proxmox/") {
		t.Fatal("missing isolated, verified installation")
	}
	if InstanceKey("https://one.example/") != InstanceKey("https://one.example") || InstanceKey("https://one.example") == InstanceKey("https://two.example") {
		t.Fatal("incorrect controller identity")
	}
}

func TestControllerKeySurvivesTailcatRelayChanges(t *testing.T) {
	identity := tailcat.NewPrivateKey()
	identity.Public.RegionID = 1
	first := InstanceKey("tailcat://" + string(identity.Public.Addr()))
	identity.Public.RegionID = 2
	if first != InstanceKey("tailcat://"+string(identity.Public.Addr())) {
		t.Fatal("relay change created a second provider service")
	}
}

func TestCompletedCommandReusesUpgradedBinaryWithoutDownloading(t *testing.T) {
	dir := t.TempDir()
	controller, id, token := "https://controller.example", "pvs_done", "old-capability"
	if err := os.WriteFile(dir+"/completed-"+InstanceKey(id+token), []byte("connected\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/zoomies", []byte("#!/bin/sh\nprintf 'upgraded gateway reused'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	command := Command(controller, id, token, "v0.1.0")
	command = strings.ReplaceAll(command, "/var/lib/zoomies-proxmox/"+InstanceKey(controller), dir)
	// Strip only the privilege wrapper so this test also works without root.
	_, command, ok := strings.Cut(command, "bash <<'ZOOMIES_PROXMOX_SETUP'\n")
	if !ok {
		t.Fatal("missing script")
	}
	command = strings.TrimSuffix(command, "ZOOMIES_PROXMOX_SETUP")
	out, err := exec.Command("bash", "-c", command).CombinedOutput()
	if err != nil || string(out) != "upgraded gateway reused" {
		t.Fatalf("completed replay downloaded or failed: %v %s", err, out)
	}
}

func TestDiscoverTemplatesFiltersGuestsAndSortsIDs(t *testing.T) {
	h := Host{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "pvesh" || strings.Join(args, " ") != "get /cluster/resources --type vm --output-format json" {
			t.Fatalf("unexpected inventory command: %s %v", name, args)
		}
		return []byte(`[{"vmid":9001,"name":"second","node":"pve-2","type":"qemu","template":1},{"vmid":101,"name":"running","node":"pve-1","type":"qemu"},{"vmid":9000,"name":"runner","node":"pve-1","type":"qemu","template":1},{"vmid":200,"node":"pve-1","type":"lxc","template":1}]`), nil
	}}
	templates, err := DiscoverTemplates(context.Background(), h)
	if err != nil || len(templates) != 2 || templates[0].VMID != 9000 || templates[0].Node != "pve-1" || templates[1].VMID != 9001 {
		t.Fatalf("templates: %+v, %v", templates, err)
	}
	h.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte(`[]`), nil }
	templates, err = DiscoverTemplates(context.Background(), h)
	if err != nil || len(templates) != 0 {
		t.Fatalf("empty inventory: %v %v", templates, err)
	}
	h.Run = func(context.Context, string, ...string) ([]byte, error) { return []byte(`not json`), nil }
	if _, err := DiscoverTemplates(context.Background(), h); err == nil {
		t.Fatal("malformed inventory accepted")
	}
}
