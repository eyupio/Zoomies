package hosttune

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

var dedicatedUnits = []string{"snapd.service", "ModemManager.service", "multipathd.service", "apport.service", "motd-news.service", "udisks2.service", "fwupd.service", "cloud-init.service"}

func dedicatedChecks() []Check {
	var out []Check
	for _, unit := range dedicatedUnits {
		out = append(out, unitCheck(unit))
	}
	for _, u := range []string{"apt-daily.timer", "apt-daily-upgrade.timer"} {
		out = append(out, unitCheck(u))
	}
	out = append(out, Check{ID: "journal.size", Title: "Journal disk usage cap", Tier: Dedicated, Rationale: "A journal size cap leaves more disk space available for builds.", Detect: detectJournal, Plan: planJournal},
		Check{ID: "memory.swap-off", Title: "Disable swap", Tier: Dedicated, Optional: true, Rationale: "On a host with at least 32 GiB RAM, disabling swap can reduce latency but increases out-of-memory risk.", Detect: detectSwap, Plan: planSwap})
	return out
}
func unitID(u string) string {
	return "service." + strings.TrimSuffix(strings.TrimSuffix(u, ".service"), ".timer")
}
func unitFamily(u string) []string {
	switch u {
	case "snapd.service":
		return []string{u, "snapd.socket", "snapd.seeded.service", "snapd.apparmor.service", "snapd.autoimport.service"}
	case "multipathd.service":
		return []string{u, "multipathd.socket"}
	case "motd-news.service":
		return []string{u, "motd-news.timer"}
	case "cloud-init.service":
		return []string{u, "cloud-init-local.service", "cloud-config.service", "cloud-final.service"}
	}
	return []string{u}
}
func familyGuard(ctx context.Context, e *Engine, u string) string {
	for _, member := range unitFamily(u) {
		enabled, active, err := unitState(ctx, e, member)
		if err != nil {
			if member == u {
				return err.Error()
			}
			continue
		}
		if enabled != "enabled" && enabled != "disabled" && enabled != "static" && enabled != "masked" {
			return "unit state cannot be restored safely: " + member + " " + enabled
		}
		if active != "active" && active != "inactive" {
			return "unit is not settled: " + member + " " + active
		}
		if reason := unitGuard(ctx, e, member); reason != "" {
			return reason
		}
	}
	return ""
}
func allowedUnit(u string) bool {
	for _, a := range dedicatedUnits {
		if u == a {
			return true
		}
	}
	switch u {
	case "apt-daily.timer", "apt-daily-upgrade.timer", "snapd.socket", "snapd.seeded.service", "snapd.apparmor.service", "snapd.autoimport.service", "multipathd.socket", "motd-news.timer", "cloud-init-local.service", "cloud-config.service", "cloud-final.service":
		return true
	}
	return false
}
func unitState(ctx context.Context, e *Engine, u string) (enabled, active string, err error) {
	v, err := command(ctx, e, "systemctl", "show", u, "--property=LoadState", "--property=UnitFileState", "--property=ActiveState")
	if err != nil {
		return "", "", err
	}
	load := ""
	for _, l := range strings.Split(v, "\n") {
		k, x, _ := strings.Cut(l, "=")
		switch k {
		case "LoadState":
			load = x
		case "UnitFileState":
			enabled = x
		case "ActiveState":
			active = x
		}
	}
	if load == "not-found" || load == "" {
		return "", "", fmt.Errorf("unit is not installed")
	}
	return
}
func unitGuard(ctx context.Context, e *Engine, u string) string {
	if strings.HasPrefix(u, "apt-daily") && !e.SecurityMaintenanceReady {
		return "security-update maintenance window is not configured; leave automatic updates enabled (maintenance scheduler TODO)"
	}
	if strings.HasPrefix(u, "snapd") {
		v, err := command(ctx, e, "snap", "list")
		if err != nil {
			return "cannot verify installed snaps"
		}
		for i, l := range strings.Split(v, "\n") {
			f := strings.Fields(l)
			if i == 0 || len(f) == 0 {
				continue
			}
			n := f[0]
			if n != "core" && n != "core18" && n != "core20" && n != "core22" && n != "core24" {
				return "a non-core snap is installed: " + n
			}
		}
	}
	if u == "fwupd.service" {
		v, err := command(ctx, e, "systemd-detect-virt", "--vm")
		if err != nil || v == "none" || v == "" {
			return "fwupd may only be disabled on a confirmed virtual machine"
		}
	}
	if u == "cloud-init.service" {
		v, err := command(ctx, e, "cloud-init", "status", "--format", "json")
		var status struct {
			Status string `json:"status"`
		}
		if err != nil || json.Unmarshal([]byte(v), &status) != nil || status.Status != "done" {
			return "cloud-init has not been confirmed complete"
		}
	}
	v, err := command(ctx, e, "systemctl", "list-dependencies", "--reverse", "--all", "--plain", "--no-pager", u)
	if err != nil {
		return "cannot verify reverse dependencies"
	}
	for _, l := range strings.Split(v, "\n") {
		f := strings.Fields(strings.TrimSpace(l))
		if len(f) == 0 {
			continue
		}
		name := f[len(f)-1]
		if name == u {
			continue
		}
		if strings.Contains(name, ".") && !allowedUnit(name) {
			return "another unit depends on it: " + name
		}
	}
	return ""
}
func unitCheck(u string) Check {
	return Check{ID: unitID(u), Title: "Dedicated host: " + u, Tier: Dedicated, Rationale: "Disable and mask an unused service only on a host dedicated to Zoomies.", Detect: func(ctx context.Context, e *Engine) Result {
		enabled, active, err := unitState(ctx, e, u)
		if err != nil {
			return Result{Status: Skip, Reason: err.Error()}
		}
		r := Result{Status: Warn, Current: enabled + ", " + active, Recommended: "disabled and masked"}
		allMasked := true
		for _, member := range unitFamily(u) {
			en, ac, er := unitState(ctx, e, member)
			if er == nil && (en != "masked" || ac != "inactive") {
				allMasked = false
			}
		}
		if allMasked && strings.HasPrefix(enabled, "masked") && active != "active" {
			r.Status = OK
			return r
		}
		if enabled != "enabled" && enabled != "disabled" && enabled != "static" && enabled != "masked" {
			r.Status = Skip
			r.Reason = "unit state cannot be restored safely: " + enabled
			return r
		}
		if reason := familyGuard(ctx, e, u); reason != "" {
			r.Status = Skip
			if strings.HasPrefix(u, "apt-daily") {
				r.Status = Warn
			}
			r.Reason = reason
		}
		return r
	}, Plan: func(ctx context.Context, e *Engine, r Result) (Change, error) {
		if reason := familyGuard(ctx, e, u); reason != "" {
			return Change{}, fmt.Errorf("%s", reason)
		}

		c := Change{ID: r.ID, Previous: r.Current, New: "disabled and masked"}
		for _, member := range unitFamily(u) {
			enabled, active, err := unitState(ctx, e, member)
			if err != nil {
				if member == u {
					return Change{}, err
				}
				continue
			}
			if enabled == "masked" && active == "inactive" {
				continue
			}
			c.Units = append(c.Units, UnitChange{Name: member, BeforeEnabled: enabled, BeforeActive: active, AfterEnabled: "masked", AfterActive: "inactive"})
			if active == "active" {
				c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "stop", member}, Undo: []string{"systemctl", "start", member}})
			}
			if enabled == "enabled" {
				c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "disable", member}, Undo: []string{"systemctl", "enable", member}})
			}
			if enabled != "masked" {
				c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "mask", member}, Undo: []string{"systemctl", "unmask", member}})
			}
		}

		return c, nil
	}}
}
func detectJournal(ctx context.Context, e *Engine) Result {
	path := "/etc/systemd/journald.conf.d/90-zoomies.conf"
	current := "distribution default"
	s := Warn
	for _, dir := range []string{"/etc/systemd/journald.conf.d", "/run/systemd/journald.conf.d", "/usr/lib/systemd/journald.conf.d"} {
		entries, _ := e.System.ReadDir(dir)
		for _, x := range entries {
			p := filepath.Join(dir, x.Name())
			b, err := read(e, p)
			if err != nil {
				continue
			}
			if strings.Contains(b, "SystemMaxUse=") {
				if p != path || managed(b) {
					return Result{Status: Warn, Current: p, Recommended: "1G", Reason: "journal size already managed elsewhere"}
				}
				current = b
				if strings.Contains(b, "SystemMaxUse=1G") {
					s = OK
				}
			}
		}
	}
	b, _ := read(e, "/etc/systemd/journald.conf")
	if assigns(b, "SystemMaxUse") {
		return Result{Status: Warn, Current: "/etc/systemd/journald.conf", Recommended: "1G", Reason: "journal size already managed elsewhere"}
	}
	return Result{Status: s, Current: current, Recommended: "1G"}
}
func planJournal(ctx context.Context, e *Engine, r Result) (Change, error) {
	f, err := fileChange(e, "/etc/systemd/journald.conf.d/90-zoomies.conf", "[Journal]\nSystemMaxUse=1G\n")
	return Change{ID: r.ID, Previous: r.Current, New: r.Recommended, Files: []FileChange{f}, Notice: "The cap takes effect when journald next starts. Zoomies does not stop or restart journald."}, err
}
func detectSwap(ctx context.Context, e *Engine) Result {
	b, err := read(e, "/proc/meminfo")
	if err != nil {
		return unavailable(err)
	}
	ram := int64(0)
	for _, l := range strings.Split(b, "\n") {
		f := strings.Fields(l)
		if len(f) > 1 && f[0] == "MemTotal:" {
			ram = number(f[1]) * 1024
		}
	}
	if ram < 32*1024*1024*1024 {
		return Result{Status: Skip, Reason: "less than 32 GiB RAM; keep swap enabled"}
	}
	v, err := command(ctx, e, "swapon", "--show", "--noheadings", "--raw", "--output", "NAME")
	if err != nil {
		return unavailable(err)
	}
	if v == "" {
		return Result{Status: OK, Current: "no active swap", Recommended: "swap off"}
	}
	// Only native systemd swap units backed by fstab can be restored and masked
	// with drop-ins; zram generators and custom swap policies remain untouched.
	for _, p := range strings.Fields(v) {
		u, err := command(ctx, e, "systemd-escape", "--path", "--suffix=swap", p)
		if err != nil {
			return unavailable(err)
		}
		fragment, err := command(ctx, e, "systemctl", "show", u, "--property=FragmentPath", "--value")
		if err != nil || !strings.HasPrefix(fragment, "/run/systemd/generator/") {
			return Result{Status: Skip, Current: v, Reason: "swap is generated or managed outside fstab; leave its policy unchanged"}
		}
	}
	return Result{Status: Warn, Current: v, Recommended: "swap off"}
}
func planSwap(ctx context.Context, e *Engine, r Result) (Change, error) {
	c := Change{ID: r.ID, Previous: r.Current, New: "swap off"}
	for _, p := range strings.Fields(r.Current) {
		u, err := command(ctx, e, "systemd-escape", "--path", "--suffix=swap", p)
		if err != nil {
			return c, err
		}
		f, err := fileChange(e, "/etc/systemd/system/"+u+".d/90-zoomies.conf", "[Unit]\nConditionPathExists=/run/zoomies-swap-enabled\n")
		if err != nil {
			return c, err
		}
		if _, err = e.System.Stat("/run/zoomies-swap-enabled"); err == nil {
			return c, fmt.Errorf("swap condition marker already exists")
		}
		c.Files = append(c.Files, f)
		c.Operations = append(c.Operations, Operation{Do: []string{"swapoff", "--", p}, Undo: []string{"swapon", "--", p}})
	}
	c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "daemon-reload"}, Undo: []string{"systemctl", "daemon-reload"}})
	return c, nil
}
