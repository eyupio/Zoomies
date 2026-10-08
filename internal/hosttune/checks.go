package hosttune

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const sysctlFile = "/etc/sysctl.d/90-zoomies.conf"

func baseChecks() []Check {
	checks := []Check{
		sysctl("inotify.watches", "File watches", "fs.inotify.max_user_watches", 524288, Safe, "Large checkouts and build tools need enough file watches."),
		sysctl("inotify.instances", "File watch instances", "fs.inotify.max_user_instances", 1024, Safe, "Concurrent runners each need their own watch instances."),
		sysctl("files.maximum", "System file descriptors", "fs.file-max", 2097152, Safe, "Concurrent builds need enough system-wide file descriptors."),
		{ID: "files.service", Title: "Runner service file descriptors", Tier: Safe, Rationale: "The runner service needs enough open files for concurrent builds.", Detect: detectServiceLimit, Plan: planServiceLimit},
		{ID: "docker.logs", Title: "Docker log rotation", Tier: Safe, Rationale: "Rotating JSON logs prevents them from filling the host disk.", Detect: detectLogs, Plan: planLogs},
		{ID: "docker.storage", Title: "Docker storage driver", Tier: Safe, Rationale: "overlay2 is the supported baseline for this host configuration.", Detect: func(ctx context.Context, e *Engine) Result {
			v, err := dockerInfo(ctx, e, "{{.Driver}}")
			if err != nil {
				return Result{Status: Skip, Reason: "Docker information is unavailable: " + v}
			}
			s := OK
			if v != "overlay2" {
				s = Warn
			}
			return Result{Status: s, Current: v, Recommended: "overlay2"}
		}},
		{ID: "cgroup.version", Title: "Control groups", Tier: Safe, Rationale: "Control groups version 2 gives consistent resource accounting.", Detect: func(ctx context.Context, e *Engine) Result {
			_, err := e.System.Stat("/sys/fs/cgroup/cgroup.controllers")
			v, s := "v2", OK
			if err != nil {
				v, s = "v1 or unavailable", Warn
			}
			return Result{Status: s, Current: v, Recommended: "v2"}
		}},
		{ID: "work.filesystem", Title: "Work directory filesystem", Tier: Safe, Rationale: "Local ext4 or XFS with noatime avoids extra metadata writes.", Detect: mountCheck(false)},
		{ID: "docker.filesystem", Title: "Docker root filesystem", Tier: Safe, Rationale: "Docker layers benefit from local ext4 or XFS with noatime.", Detect: mountCheck(true)},
		{ID: "disk.space", Title: "Work directory free space", Tier: Safe, Rationale: "Keep at least 10% and 10 GiB free for new builds.", Detect: diskCheck(false)},
		{ID: "disk.inodes", Title: "Work directory free inodes", Tier: Safe, Rationale: "Many small build files can exhaust inodes before disk space.", Detect: diskCheck(true)},
		{ID: "cpu.governor", Title: "CPU frequency governor", Tier: Aggressive, Rationale: "Performance mode reduces frequency scaling delays at the cost of power.", Detect: detectGovernor, Plan: planGovernor},
		sysctl("memory.swappiness", "Swappiness", "vm.swappiness", 10, Aggressive, "Less swapping can improve build latency when RAM is sufficient."),
		{ID: "tmp.tmpfs", Title: "Temporary directory in memory", Tier: Aggressive, Optional: true, Rationale: "A memory-backed /tmp can speed temporary work but consumes RAM.", Detect: func(ctx context.Context, e *Engine) Result {
			v, err := command(ctx, e, "findmnt", "-n", "-o", "FSTYPE", "--target", "/tmp")
			if err != nil {
				return unavailable(err)
			}
			s := Warn
			if v == "tmpfs" {
				s = OK
			}
			r := Result{Status: s, Current: v, Recommended: "tmpfs with a 25% RAM cap, at the next planned boot"}
			if s == Warn {
				unit, err := command(ctx, e, "systemctl", "show", "tmp.mount", "--property=LoadState", "--value")
				if err != nil || unit != "loaded" {
					r.Reason = "advice only: no existing tmp.mount unit; configure a memory-backed /tmp manually after reviewing RAM"
				} else if managedUnitPolicy(ctx, e, "tmp.mount", "What", "Type", "Options") {
					r.Reason = "temporary directory mount policy is managed elsewhere"
				}
				b, _ := read(e, "/etc/fstab")
				for _, line := range strings.Split(b, "\n") {
					f := strings.Fields(line)
					if len(f) > 1 && !strings.HasPrefix(f[0], "#") && f[1] == "/tmp" {
						r.Reason = "temporary directory mount policy is already managed in /etc/fstab"
					}
				}

			}
			return r
		}, Plan: planTmpfs},
	}
	return checks
}
func sysctl(id, title, key string, want int64, t Tier, why string) Check {
	return Check{ID: id, Title: title, Tier: t, Rationale: why, Detect: func(ctx context.Context, e *Engine) Result {
		p := "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
		v, err := read(e, p)
		if err != nil {
			return unavailable(err)
		}
		r := Result{Status: Warn, Current: v, Recommended: strconv.FormatInt(want, 10)}
		if (key != "vm.swappiness" && number(v) >= want) || (key == "vm.swappiness" && number(v) <= want) {
			r.Status = OK
			return r
		}
		if e.Container {
			r.Status = Skip
			r.Reason = "container or LXC; the outer host owns this setting"
			return r
		}
		if i, err := e.System.Stat(p); err == nil && i.Mode().Perm()&0200 == 0 {
			r.Status = Skip
			r.Reason = "read-only kernel setting"
			return r
		}
		r.Reason = managedSysctl(e, key)
		return r
	}, Plan: func(ctx context.Context, e *Engine, r Result) (Change, error) {
		before, err := snapshot(e, sysctlFile)
		if err != nil {
			return Change{}, err
		}
		after := setAssignment(string(before.Data), key, r.Recommended)
		return Change{ID: id, Files: []FileChange{{Path: sysctlFile, Before: before, After: []byte(after), Mode: snapshotMode(before)}}, Operations: []Operation{{Do: []string{"sysctl", "-w", key + "=" + r.Recommended}, Undo: []string{"sysctl", "-w", key + "=" + r.Current}}}, Previous: r.Current, New: r.Recommended}, nil
	}}
}
func managedSysctl(e *Engine, key string) string {
	for _, dir := range []string{"/etc/sysctl.d", "/run/sysctl.d", "/usr/local/lib/sysctl.d", "/usr/lib/sysctl.d", "/lib/sysctl.d"} {
		entries, err := e.System.ReadDir(dir)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				return "cannot inspect " + dir + " for managed settings"
			}
			continue
		}
		for _, entry := range entries {
			p := filepath.Join(dir, entry.Name())
			if p == sysctlFile || !strings.HasSuffix(p, ".conf") {
				continue
			}
			b, err := read(e, p)
			if err != nil {
				return "cannot inspect " + p
			}
			if assigns(b, key) {
				return "already managed in " + p
			}
		}
	}
	b, err := read(e, "/etc/sysctl.conf")
	if err == nil && assigns(b, key) {
		return "already managed in /etc/sysctl.conf"
	}
	b, _ = read(e, sysctlFile)
	if managed(b) {
		return "configuration management marker in " + sysctlFile
	}
	return ""
}
func managed(s string) bool {
	s = strings.ToLower(s)
	for _, v := range []string{"ansible", "puppet", "chef", "saltstack", "do not edit", "managed by"} {
		if strings.Contains(s, v) {
			return true
		}
	}
	return false
}
func assigns(s, key string) bool {
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(strings.SplitN(l, "#", 2)[0])
		k, _, ok := strings.Cut(l, "=")
		if ok && strings.ReplaceAll(strings.TrimSpace(k), "/", ".") == key {
			return true
		}
	}
	return false
}
func setAssignment(s, key, value string) string {
	var out []string
	found := false
	for _, l := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		k, _, ok := strings.Cut(l, "=")
		if ok && strings.TrimSpace(k) == key {
			l = key + " = " + value
			found = true
		}
		if l != "" {
			out = append(out, l)
		}
	}
	if !found {
		out = append(out, key+" = "+value)
	}
	return strings.Join(out, "\n") + "\n"
}
func service(ctx context.Context, e *Engine) (string, string, error) {
	for _, u := range []string{"zoomies-agent.service", "zoomies.service"} {
		v, err := command(ctx, e, "systemctl", "show", u, "--property=LoadState", "--value")
		if err == nil && v == "loaded" {
			limit, err := command(ctx, e, "systemctl", "show", u, "--property=LimitNOFILE", "--value")
			return u, limit, err
		}
	}
	return "", "", fs.ErrNotExist
}
func detectServiceLimit(ctx context.Context, e *Engine) Result {
	u, v, err := service(ctx, e)
	if err != nil {
		return Result{Status: Skip, Reason: "no native Zoomies systemd service; container limits belong to the deployment"}
	}
	r := Result{Status: Warn, Current: v, Recommended: "65536"}
	if v == "infinity" || number(v) >= 65536 {
		r.Status = OK
	}
	if managedDropins(ctx, e, u) {
		r.Reason = "service limits are managed in another drop-in"
	}
	return r
}
func managedDropins(ctx context.Context, e *Engine, u string) bool {
	v, err := command(ctx, e, "systemctl", "show", u, "--property=DropInPaths", "--value")
	if err != nil {
		return true
	}
	for _, p := range strings.Fields(v) {
		b, err := read(e, p)
		if err != nil || managed(b) || (strings.Contains(b, "LimitNOFILE") && !strings.HasSuffix(p, "/90-zoomies.conf")) {
			return true
		}
	}
	return false
}

// Existing drop-ins and management markers belong to their administrator.
func managedUnitPolicy(ctx context.Context, e *Engine, u string, keys ...string) bool {
	paths, err := command(ctx, e, "systemctl", "show", u, "--property=DropInPaths", "--value")
	if err != nil {
		return true
	}
	fragment, err := command(ctx, e, "systemctl", "show", u, "--property=FragmentPath", "--value")
	if err != nil {
		return true
	}
	if b, err := read(e, fragment); err == nil && managed(b) {
		return true
	}
	for _, p := range strings.Fields(paths) {
		b, err := read(e, p)
		if err != nil || managed(b) {
			return true
		}
		if strings.HasSuffix(p, "/90-zoomies.conf") {
			continue
		}
		for _, k := range keys {
			if assigns(b, k) {
				return true
			}
		}
	}
	return false
}
func planServiceLimit(ctx context.Context, e *Engine, r Result) (Change, error) {
	u, _, err := service(ctx, e)
	if err != nil {
		return Change{}, err
	}
	p := "/etc/systemd/system/" + u + ".d/90-zoomies.conf"
	f, err := fileChange(e, p, "[Service]\nLimitNOFILE=65536\n")
	return Change{ID: r.ID, Previous: r.Current, New: r.Recommended, Files: []FileChange{f}, Operations: []Operation{{Do: []string{"systemctl", "daemon-reload"}, Undo: []string{"systemctl", "daemon-reload"}}}, Notice: "The service limit takes effect next time the Zoomies service starts. Drain jobs before restarting it."}, err
}
func dockerInfo(ctx context.Context, e *Engine, format string) (string, error) {
	a := []string{}
	if e.DockerHost != "" {
		a = append(a, "--host", e.DockerHost)
	}
	a = append(a, "info", "--format", format)
	return command(ctx, e, "docker", a...)
}
func detectLogs(ctx context.Context, e *Engine) Result {
	r := Result{Status: Warn, Recommended: "json-file: max-size 10m, max-file 3"}
	v, err := dockerInfo(ctx, e, "{{.LoggingDriver}}")
	if err != nil {
		return Result{Status: Skip, Reason: "Docker information is unavailable: " + v}
	}
	if security, err := dockerInfo(ctx, e, "{{json .SecurityOptions}}"); err != nil {
		return Result{Status: Skip, Reason: "cannot establish whether the Docker daemon is rootless"}
	} else if strings.Contains(security, "rootless") {
		return Result{Status: Skip, Reason: "rootless Docker uses the owning user's daemon configuration"}
	}
	if e.DockerHost != "" && e.DockerHost != "unix:///var/run/docker.sock" {
		return Result{Status: Skip, Reason: "custom Docker socket; local /etc/docker/daemon.json may not control it"}
	}
	if v != "json-file" {
		return Result{Status: Skip, Current: v, Recommended: r.Recommended, Reason: "another logging driver is configured; leave its policy unchanged"}
	}
	b, err := e.System.ReadFile("/etc/docker/daemon.json")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return unavailable(err)
	}
	var doc map[string]any
	if len(b) > 0 {
		if err = json.Unmarshal(b, &doc); err != nil {
			return Result{Status: Error, Reason: "daemon.json is invalid JSON"}
		}
	}
	r.Current = "json-file without explicit rotation"
	opts, _ := doc["log-opts"].(map[string]any)
	if opts != nil {
		r.Current = fmt.Sprint(opts)
		size, _ := opts["max-size"].(string)
		files, _ := opts["max-file"].(string)
		if size != "" && size != "-1" && number(files) > 0 {
			r.Status = OK
		}
	}
	if managed(string(b)) {
		r.Reason = "daemon.json is managed elsewhere"
	}
	if e.DockerHost != "" && !strings.HasPrefix(e.DockerHost, "unix:///") {
		r.Status = Skip
		r.Reason = "Docker endpoint is remote; local daemon.json does not control it"
	}
	return r
}

// MergeDockerLogs preserves unrelated settings and logging options.
func MergeDockerLogs(b []byte) ([]byte, error) {
	doc := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		if doc == nil {
			return nil, fmt.Errorf("daemon.json must be an object")
		}
	}
	if d, ok := doc["log-driver"]; ok && d != "json-file" {
		return nil, fmt.Errorf("refusing to replace logging driver %v", d)
	}
	doc["log-driver"] = "json-file"
	opts := map[string]any{}
	if v, ok := doc["log-opts"]; ok {
		var valid bool
		opts, valid = v.(map[string]any)
		if !valid {
			return nil, fmt.Errorf("log-opts must be an object")
		}
	}
	opts["max-size"] = "10m"
	opts["max-file"] = "3"
	doc["log-opts"] = opts
	b, err := json.MarshalIndent(doc, "", "  ")
	return append(b, '\n'), err
}
func planLogs(ctx context.Context, e *Engine, r Result) (Change, error) {
	p := "/etc/docker/daemon.json"
	s, err := snapshot(e, p)
	if err != nil {
		return Change{}, err
	}
	b, err := MergeDockerLogs(s.Data)
	if err != nil {
		return Change{}, err
	}
	return Change{ID: r.ID, Previous: r.Current, New: r.Recommended, Files: []FileChange{{Path: p, Before: s, After: b, Mode: snapshotMode(s)}}, DockerRestart: true, Notice: "Docker must restart to load this policy; it applies to newly created containers."}, nil
}
func mountCheck(docker bool) func(context.Context, *Engine) Result {
	return func(ctx context.Context, e *Engine) Result {
		p := e.WorkDir
		if docker {
			p = e.DockerRoot
			if p == "" {
				v, err := dockerInfo(ctx, e, "{{.DockerRootDir}}")
				if err != nil {
					return Result{Status: Skip, Reason: "Docker root is unavailable"}
				}
				p = v
			}
		}
		if e.Container {
			return Result{Status: Skip, Reason: "container mount view does not describe the outer host"}
		}
		v, err := command(ctx, e, "findmnt", "-n", "-o", "FSTYPE,OPTIONS", "--target", p)
		if err != nil {
			return Result{Status: Skip, Reason: "filesystem is unavailable: " + p}
		}
		s := Warn
		if (strings.HasPrefix(v, "ext4 ") || strings.HasPrefix(v, "xfs ")) && strings.Contains(","+strings.ReplaceAll(v, " ", ",")+",", ",noatime,") {
			s = OK
		}
		return Result{Status: s, Current: p + ": " + v, Recommended: "ext4 or XFS with noatime", Reason: "advice only; review mount settings, with the host cordoned, before changing them"}
	}
}
func diskCheck(inodes bool) func(context.Context, *Engine) Result {
	return func(ctx context.Context, e *Engine) Result {
		a := "-Pk"
		rec := "at least 10% and 10 GiB free"
		if inodes {
			a = "-Pi"
			rec = "at least 10% free inodes"
		}
		v, err := command(ctx, e, "df", a, "--", e.WorkDir)
		if err != nil {
			return Result{Status: Skip, Reason: "work directory is unavailable"}
		}
		ls := strings.Split(v, "\n")
		if len(ls) < 2 {
			return Result{Status: Error, Reason: "cannot read df output"}
		}
		f := strings.Fields(ls[len(ls)-1])
		if len(f) < 6 {
			return Result{Status: Error, Reason: "cannot read df columns"}
		}
		total, free := number(f[1]), number(f[3])
		if total <= 0 {
			return Result{Status: Skip, Reason: "filesystem does not report a usable capacity"}
		}
		s := OK
		if free*100/total < 10 || (!inodes && free < 10*1024*1024) {
			s = Warn
		}
		// df -P reports 1 KiB blocks, which nobody reads at a glance; the status
		// rule above is untouched, only the words an operator sees changed.
		amount := fmt.Sprintf("%d inodes", free)
		if !inodes {
			amount = formatKiB(free)
		}
		return Result{Status: s, Current: fmt.Sprintf("%d%% free (%s)", free*100/total, amount), Recommended: rec}
	}
}

// formatKiB renders a df block count in the largest unit that keeps it short:
// GiB to one decimal, whole MiB under a GiB, KiB under a MiB.
func formatKiB(k int64) string {
	switch {
	case k >= 1024*1024:
		return fmt.Sprintf("%.1f GiB", float64(k)/(1024*1024))
	case k >= 1024:
		return fmt.Sprintf("%d MiB", k/1024)
	}
	return fmt.Sprintf("%d KiB", k)
}
func governorPaths(e *Engine) []string {
	es, _ := e.System.ReadDir("/sys/devices/system/cpu/cpufreq")
	var out []string
	for _, x := range es {
		if strings.HasPrefix(x.Name(), "policy") {
			out = append(out, "/sys/devices/system/cpu/cpufreq/"+x.Name()+"/scaling_governor")
		}
	}
	sort.Strings(out)
	return out
}
func detectGovernor(ctx context.Context, e *Engine) Result {
	ps := governorPaths(e)
	if len(ps) == 0 {
		return Result{Status: Skip, Reason: "CPU governor is not exposed (common in VMs)"}
	}
	r := Result{Status: OK, Recommended: "performance"}
	var vs []string
	for _, p := range ps {
		v, err := read(e, p)
		if err != nil {
			return unavailable(err)
		}
		vs = append(vs, filepath.Base(filepath.Dir(p))+"="+v)
		if v != "performance" {
			r.Status = Warn
		}
		a, err := read(e, filepath.Join(filepath.Dir(p), "scaling_available_governors"))
		if err != nil || !strings.Contains(" "+a+" ", " performance ") {
			r.Status = Skip
			r.Reason = "performance governor is not available"
		}
	}
	r.Current = strings.Join(vs, ", ")
	if r.Status == Warn {
		v, err := command(ctx, e, "systemctl", "show", "cpupower.service", "--property=LoadState", "--value")
		if err != nil || v != "loaded" {
			r.Reason = "persistent tuning needs the distribution's cpupower.service; install its CPU frequency tools first"
		} else if managedUnitPolicy(ctx, e, "cpupower.service", "ExecStart", "Environment") {
			r.Reason = "CPU governor service policy is managed elsewhere"
		}

	}
	return r
}
func planGovernor(ctx context.Context, e *Engine, r Result) (Change, error) {
	// A systemd service drop-in persists the choice; runtime writes and their
	// individual previous values are journalled separately.
	c := Change{ID: r.ID, Previous: r.Current, New: "performance"}
	for _, p := range governorPaths(e) {
		v, err := read(e, p)
		if err != nil {
			return c, err
		}
		c.Operations = append(c.Operations, Operation{Path: p, Value: "performance", Previous: v})
	}
	// cpupower is distribution supplied; do not invent a shell script or assume
	// it is installed. Its existing service provides the persistence point.
	v, err := command(ctx, e, "systemctl", "show", "cpupower.service", "--property=LoadState", "--value")
	if err != nil || v != "loaded" {
		return c, fmt.Errorf("persistent CPU governor tuning needs an installed cpupower.service; install the distribution's CPU frequency tools first")
	}
	f, err := fileChange(e, "/etc/systemd/system/cpupower.service.d/90-zoomies.conf", "[Service]\nExecStart=\nExecStart=/usr/bin/cpupower frequency-set -g performance\n")
	if err != nil {
		return c, err
	}
	c.Files = []FileChange{f}
	c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "daemon-reload"}, Undo: []string{"systemctl", "daemon-reload"}})
	enabled, err := command(ctx, e, "systemctl", "show", "cpupower.service", "--property=UnitFileState", "--value")
	if err != nil {
		return c, err
	}
	if enabled == "disabled" {
		c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "enable", "cpupower.service"}, Undo: []string{"systemctl", "disable", "cpupower.service"}})
	} else if enabled != "enabled" {
		return c, fmt.Errorf("cpupower service state cannot be restored safely: %s", enabled)
	}
	c.Notice = "The existing cpupower service restores the governor at boot; previous service enablement and each CPU policy are recorded."
	return c, nil
}

func planTmpfs(ctx context.Context, e *Engine, r Result) (Change, error) {
	unit, err := command(ctx, e, "systemctl", "show", "tmp.mount", "--property=UnitFileState", "--value")
	if err != nil {
		return Change{}, err
	}
	if unit != "enabled" && unit != "disabled" {
		return Change{}, fmt.Errorf("tmp.mount enablement cannot be restored safely: %s", unit)
	}
	f, err := fileChange(e, "/etc/systemd/system/tmp.mount.d/90-zoomies.conf", "[Mount]\nWhat=tmpfs\nType=tmpfs\nOptions=mode=1777,size=25%\n")
	if err != nil {
		return Change{}, err
	}
	c := Change{ID: r.ID, Previous: r.Current, New: r.Recommended, Files: []FileChange{f}, Operations: []Operation{{Do: []string{"systemctl", "daemon-reload"}, Undo: []string{"systemctl", "daemon-reload"}}}, Notice: "No active /tmp files are hidden or moved. The mount is enabled only for the next planned boot; drain before rebooting. Revert restores the drop-in and enablement; if it has since booted, drain and reboot again to change the live mount."}
	if unit == "disabled" {
		c.Operations = append(c.Operations, Operation{Do: []string{"systemctl", "enable", "tmp.mount"}, Undo: []string{"systemctl", "disable", "tmp.mount"}})
	}
	return c, nil
}
