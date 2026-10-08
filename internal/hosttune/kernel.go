package hosttune

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

const hwePackage = "linux-generic-hwe-24.04"

// hweCommand is the by-hand route to the same install planHWE performs, for a
// host Zoomies cannot tune: a container install, a non-root run, or an operator
// who would rather type it. It installs no recommends, as the plan does.
const hweCommand = "sudo apt-get install --no-install-recommends " + hwePackage

func kernelChecks() []Check {
	return []Check{
		{ID: "kernel.running", Title: "Running kernel", Tier: Safe, Rationale: "Linux 6.8 or later is the baseline for these runner hosts.", Detect: func(ctx context.Context, e *Engine) Result {
			v, err := command(ctx, e, "uname", "-r")
			if err != nil {
				return unavailable(err)
			}
			s := OK
			if versionCompare(v, "6.8") < 0 {
				s = Warn
			}
			return Result{Status: s, Current: v, Recommended: "6.8 or later (distribution supplied)"}
		}},
		{ID: "kernel.pending", Title: "Installed kernel awaiting reboot", Tier: Safe, Rationale: "A newer installed kernel is only used after a planned reboot.", Detect: func(ctx context.Context, e *Engine) Result {
			running, err := command(ctx, e, "uname", "-r")
			if err != nil {
				return unavailable(err)
			}
			entries, err := e.System.ReadDir("/lib/modules")
			if err != nil {
				return Result{Status: Skip, Current: running, Reason: "installed host kernels are not visible"}
			}
			latest := running
			for _, x := range entries {
				if versionCompare(x.Name(), latest) > 0 {
					latest = x.Name()
				}
			}
			s := OK
			pending := latest != running
			if _, err := e.System.Stat("/var/run/reboot-required"); err == nil {
				pending = true
			}
			if pending {
				s = Warn
			}
			r := Result{Status: s, Current: running, Recommended: latest}
			if pending {
				// Cordon, not drain: a drain stops a runner that is still busy after
				// five minutes, and this text reaches the host page in the web UI,
				// which gives the same advice the controller does.
				r.Reason = "reboot pending; cordon the host and wait until none of its runners is running a job, then reboot it manually"
			}
			return r
		}},
		{ID: "kernel.hwe", Title: "Ubuntu hardware enablement kernel", Tier: Safe, Rationale: "Ubuntu's HWE package supplies an official newer kernel.", Detect: detectHWE},
		{ID: "kernel.hwe-install", Title: "Install Ubuntu HWE kernel", Tier: Dedicated, Optional: true, Rationale: "An official Ubuntu HWE kernel can improve newer hardware support; a reboot is planned separately.", Detect: detectHWE, Plan: planHWE},
	}
}
func versionCompare(a, b string) int {
	numbers := func(s string) []int {
		f := strings.FieldsFunc(s, func(r rune) bool { return r < '0' || r > '9' })
		out := []int{}
		for _, x := range f {
			n, _ := strconv.Atoi(x)
			out = append(out, n)
		}
		return out
	}
	aa, bb := numbers(a), numbers(b)
	for i := 0; i < len(aa) || i < len(bb); i++ {
		x, y := 0, 0
		if i < len(aa) {
			x = aa[i]
		}
		if i < len(bb) {
			y = bb[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}
func detectHWE(ctx context.Context, e *Engine) Result {
	if e.Distro != "ubuntu" || e.Version != "24.04" {
		return Result{Status: Skip, Reason: "HWE check applies to Ubuntu 24.04 only"}
	}
	pkg := hwePackage
	v, err := command(ctx, e, "dpkg-query", "-W", "-f=${Status}", pkg)
	if err == nil && strings.Contains(v, "install ok installed") {
		return Result{Status: OK, Current: pkg + " installed", Recommended: pkg}
	}
	v, err = command(ctx, e, "apt-cache", "policy", pkg)
	if err != nil {
		return Result{Status: Skip, Reason: "Ubuntu package metadata is unavailable; update it during maintenance"}
	}
	candidate := ""
	for _, l := range strings.Split(v, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "Candidate:") {
			candidate = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Candidate:"))
		}
	}
	if candidate == "" || candidate == "(none)" {
		return Result{Status: Skip, Current: "not installed", Reason: "no HWE candidate in configured package metadata"}
	}
	return Result{Status: Warn, Current: "available, not installed", Recommended: pkg, Command: hweCommand, Reason: "optional install with --dedicated --only kernel.hwe-install"}
}
func planHWE(ctx context.Context, e *Engine, r Result) (Change, error) {
	pkg := hwePackage
	out, err := command(ctx, e, "apt-get", "--simulate", "--no-install-recommends", "install", pkg)
	if err != nil {
		return Change{}, fmt.Errorf("cannot plan HWE installation: %s", out)
	}
	var packages, versions []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "Remv ") {
			return Change{}, fmt.Errorf("HWE would remove existing packages; install manually")
		}
		if !strings.HasPrefix(l, "Inst ") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < 3 || strings.HasPrefix(f[2], "[") {
			return Change{}, fmt.Errorf("HWE would upgrade existing packages; install manually")
		}
		name := f[1]
		version := strings.TrimPrefix(f[2], "(")
		if !strings.Contains(l, "Ubuntu:") {
			return Change{}, fmt.Errorf("%s is not an official Ubuntu package candidate", name)
		}
		packages = append(packages, name)
		versions = append(versions, name+"="+version)
	}
	if len(packages) == 0 {
		return Change{}, fmt.Errorf("HWE package plan is empty")
	}
	do := append([]string{"apt-get", "--yes", "--no-install-recommends", "install"}, versions...)
	undo := append([]string{"apt-get", "--yes", "remove", "--"}, packages...)
	return Change{ID: r.ID, Previous: "not installed", New: strings.Join(versions, ", "), Operations: []Operation{{Do: do, Undo: undo}}, Notice: "No reboot is performed. After installation, drain this host and reboot manually. Revert removes only the newly installed packages; no pre-existing package is upgraded or purged."}, nil
}
