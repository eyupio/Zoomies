package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const HostHealthUnit = "zoomies-host-health.service"
const HostHealthUnitPath = "/etc/systemd/system/" + HostHealthUnit
const HostHealthReport = SharedHostDir + "/host-health/report.json"

// RenderHostHealthService uses the native installed binary, so a container
// controller receives a real OS report without privileged container mounts.
func RenderHostHealthService(binary, work, dockerHost string) string {
	quote := func(s string) string { return strconv.Quote(strings.ReplaceAll(s, "%", "%%")) }
	args := quote(binary) + " doctor --watch --tier dedicated --report-file " + quote(HostHealthReport) + " --work-dir " + quote(work)
	if dockerHost != "" {
		args += " --docker-host " + quote(dockerHost)
	}
	return "[Unit]\nDescription=Zoomies read-only host health reports\nAfter=docker.service\n\n[Service]\nType=simple\nExecStart=" + args + "\nRestart=on-failure\nRestartSec=10s\nUser=root\nNoNewPrivileges=yes\nProtectSystem=strict\nProtectHome=read-only\nProtectKernelTunables=yes\nProtectKernelModules=yes\nProtectControlGroups=yes\nReadWritePaths=" + SharedHostDir + "/host-health\nUMask=0022\n\n[Install]\nWantedBy=multi-user.target\n"
}
func installHostHealth(ctx context.Context, binary, work, dockerHost string, run commandRunner) error {
	base, err := os.OpenRoot(SharedHostDir)
	if err != nil {
		return err
	}
	defer base.Close()
	if err = base.Mkdir("host-health", 0755); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := base.Lstat("host-health")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("host-health must be a real directory")
	}
	expected := info
	dir, err := base.OpenRoot("host-health")
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err = dir.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(expected, info) {
		return fmt.Errorf("host-health directory changed while opening it")
	}
	uid, _, ok := fileOwner(info)
	if !ok || uid != 0 {
		return fmt.Errorf("host-health must be root-owned")
	}
	if err = dir.Chmod(".", 0755); err != nil {
		return err
	}
	body := RenderHostHealthService(binary, work, dockerHost)
	if old, err := os.ReadFile(HostHealthUnitPath); err == nil && string(old) != body {
		return fmt.Errorf("%s already exists with different contents; inspect it before replacing", HostHealthUnitPath)
	}
	if err := writeFileAtomic(HostHealthUnitPath, []byte(body), 0644); err != nil {
		return err
	}
	if _, err := run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	_, err = run(ctx, "systemctl", "enable", "--now", HostHealthUnit)
	return err
}
func (p *upgradePlan) hostHealthChanges(ctx context.Context, s deploymentSettings) []layoutChange {
	if p.opts.Doctor == nil || !p.record.Deployment.Containerised() || !s.runsRunners(p.record.Mode) {
		return nil
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return nil
	}
	if _, err := os.Stat(HostHealthUnitPath); err == nil {
		return nil
	}
	work := s.cfg.Agent.WorkDir
	if mount, err := p.opts.run(ctx, "docker", "volume", "inspect", "--format", "{{.Mountpoint}}", p.record.Volume); err == nil && filepath.IsAbs(strings.TrimSpace(mount)) {
		work = filepath.Join(strings.TrimSpace(mount), "work")
	}
	return []layoutChange{{what: "install " + HostHealthUnitPath + " using the native binary for read-only OS checks; publish to " + HostHealthReport + " through the existing shared mount (no tuning)", apply: func(ctx context.Context) error {
		return installHostHealth(ctx, p.opts.BinaryPath, work, s.cfg.Agent.DockerHost, p.opts.run)
	}}}

}
