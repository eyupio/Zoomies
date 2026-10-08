package installer

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProxmoxGatewayUpgradePreservesIdentityAndPreviews(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "0123456789ab")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "zoomies")
	for name, value := range map[string]string{"zoomies": "old", "credential": "saved-secret", "gateway.json": "saved-identity", "completed-receipt": "connected"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	source := filepath.Join(t.TempDir(), "zoomies")
	if err := os.WriteFile(source, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls []string
	fail := false
	p := &upgradePlan{opts: UpgradeOptions{BinaryPath: source, proxmoxRoot: root, Out: io.Discard, run: func(_ context.Context, name string, args ...string) (string, error) {
		line := name + " " + strings.Join(args, " ")
		calls = append(calls, line)
		if len(args) > 0 && args[0] == "show" {
			return "path=" + target + " ;", nil
		}
		if fail {
			return "", errors.New("restart failed")
		}
		return "", nil
	}}}
	p.opts.Check = true
	if err := p.upgradeProxmoxGateways(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(target)
	if string(b) != "old" || strings.Contains(strings.Join(calls, "\n"), "restart") {
		t.Fatal("preview changed gateway")
	}
	p.opts.Check = false
	if err := p.upgradeProxmoxGateways(context.Background()); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(target)
	if string(b) != "new" {
		t.Fatal("gateway binary not replaced")
	}
	for name, value := range map[string]string{"credential": "saved-secret", "gateway.json": "saved-identity", "completed-receipt": "connected"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(b) != value {
			t.Fatalf("changed %s", name)
		}
	}
	if !strings.Contains(strings.Join(calls, "\n"), "is-active --quiet zoomies-proxmox-0123456789ab.service") {
		t.Fatal("restart not verified")
	}
	fail = true
	if err := p.upgradeProxmoxGateways(context.Background()); err == nil {
		t.Fatal("restart failure hidden")
	}
}
