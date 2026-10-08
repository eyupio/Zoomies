//go:build unix

package installer

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// openSetupLock opens the lock file as the upgrade does. Every open is an open
// file description of its own, which is what flock arbitrates between, so a
// second open in this process meets the refusal that a second process would.
func openSetupLock(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// Setup and upgrade are separate commands that both replace a gateway's binary,
// and this lock is all that stops one interleaving with the other.
func TestASecondSetupLockIsRefusedWhileTheFirstIsHeld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.lock")
	release, err := tryLockSetup(openSetupLock(t, path))
	if err != nil {
		t.Fatalf("tryLockSetup: %v", err)
	}

	second := openSetupLock(t, path)
	if _, err := tryLockSetup(second); err == nil {
		t.Fatal("a second lock on the same file was granted while the first was held")
	}

	release()
	// The same descriptor is let in once the first lock is released, which is
	// what shows the refusal above came from the first holder and not from a
	// descriptor that could never have been locked.
	again, err := tryLockSetup(second)
	if err != nil {
		t.Fatalf("the lock was not released: %v", err)
	}
	again()
}

// A setup that is killed never calls its release. The kernel closes its files
// for it, which closing the holder's file stands in for here, and that has to
// be enough or an interrupted setup would block every retry.
func TestTheSetupLockIsReleasedWhenItsFileCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "setup.lock")
	holder := openSetupLock(t, path)
	if _, err := tryLockSetup(holder); err != nil {
		t.Fatalf("tryLockSetup: %v", err)
	}
	holder.Close()

	again, err := tryLockSetup(openSetupLock(t, path))
	if err != nil {
		t.Fatalf("closing the file left the lock held: %v", err)
	}
	again()
}

// The upgrade is what the lock exists to hold off: a setup that is mid-way
// through writing a gateway's binary and unit must not have either replaced
// underneath it. The upgrade stops and says why, and the operator reruns it once
// setup has finished. Setup is separate code that takes the same file, so this
// also holds the name the two have to agree on in place.
func TestProxmoxGatewayUpgradeStopsWhileASetupHoldsTheGateway(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Proxmox gateways are systemd services, so the upgrade leaves everything alone off Linux")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "0123456789ab")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "zoomies")
	if err := os.WriteFile(target, []byte("old"), 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "zoomies")
	if err := os.WriteFile(source, []byte("new"), 0700); err != nil {
		t.Fatal(err)
	}
	var calls []string
	p := &upgradePlan{opts: UpgradeOptions{BinaryPath: source, proxmoxRoot: root, Out: io.Discard, run: func(_ context.Context, name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		if len(args) > 0 && args[0] == "show" {
			return "path=" + target + " ;", nil
		}
		return "", nil
	}}}

	setup, err := tryLockSetup(openSetupLock(t, filepath.Join(dir, "setup.lock")))
	if err != nil {
		t.Fatal(err)
	}
	defer setup()

	err = p.upgradeProxmoxGateways(context.Background())
	if err == nil || !strings.Contains(err.Error(), "is being configured; retry the upgrade when setup finishes") {
		t.Fatalf("upgrade while a setup holds the gateway = %v, want advice to retry when setup finishes", err)
	}
	if b, _ := os.ReadFile(target); string(b) != "old" {
		t.Fatal("the gateway binary was replaced while a setup held it")
	}
	for _, c := range calls {
		if strings.Contains(c, "restart") {
			t.Fatalf("the gateway was restarted while a setup held it: %v", calls)
		}
	}
}
