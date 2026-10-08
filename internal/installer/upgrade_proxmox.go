package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

var proxmoxKey = regexp.MustCompile(`^[0-9a-f]{12}$`)

// Gateways have private executables so replacing the runner binary alone would
// leave them behind. Upgrade only installations whose systemd executable agrees
// with their dedicated state directory. Never touch credentials or identities.
func (p *upgradePlan) upgradeProxmoxGateways(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	root := p.opts.proxmoxRoot
	if root == "" {
		root = "/var/lib/zoomies-proxmox"
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !proxmoxKey.MatchString(entry.Name()) {
			continue
		}
		target := filepath.Join(root, entry.Name(), "zoomies")
		unit := "zoomies-proxmox-" + entry.Name() + ".service"
		start, err := p.opts.run(ctx, "systemctl", "show", unit, "--property=ExecStart", "--value")
		if err != nil {
			return fmt.Errorf("installer: inspect %s: %w", unit, err)
		}
		if !strings.Contains(start, "path="+target+" ;") && !strings.Contains(start, "path="+target+";") {
			continue
		}
		if p.opts.Check {
			PaletteFor(p.opts.Out).Hint(p.opts.Out, "Will update %s", unit)
			continue
		}
		lock, err := os.OpenFile(filepath.Join(root, entry.Name(), "setup.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := TryLockExclusive(lock); err != nil {
			return fmt.Errorf("installer: %s is being configured; retry the upgrade when setup finishes", unit)
		}
		defer UnlockFile(lock)
		binary, err := os.ReadFile(p.opts.BinaryPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(target+".new", binary, 0700); err != nil {
			return err
		}
		if err := os.Rename(target+".new", target); err != nil {
			return err
		}
		PaletteFor(p.opts.Out).Doing(p.opts.Out, "Updating %s", unit)
		for _, args := range [][]string{{"restart", unit}, {"is-active", "--quiet", unit}} {
			if _, err := p.opts.run(ctx, "systemctl", args...); err != nil {
				return fmt.Errorf("installer: %s could not restart; rerun the upgrade: %w", unit, err)
			}
		}
	}
	return nil
}
