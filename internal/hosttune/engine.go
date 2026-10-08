// Package hosttune inspects host settings and plans explicitly approved changes.
// Detection never writes files or invokes a mutating command.
package hosttune

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Tier string

const (
	Safe       Tier = "safe"
	Aggressive Tier = "aggressive"
	Dedicated  Tier = "dedicated"
)

type Status string

const (
	OK    Status = "ok"
	Warn  Status = "warn"
	Skip  Status = "skip"
	Error Status = "error"
)
const StateDir = "/var/lib/zoomies-host-tune"
const StatePath = StateDir + "/state.json"

// System is the complete host boundary. Tests supply an in-memory filesystem
// and command runner; none of the checks reach the real machine themselves.
type System interface {
	ReadFile(string) ([]byte, error)
	ReadDir(string) ([]fs.DirEntry, error)
	Stat(string) (fs.FileInfo, error)
	WriteFile(string, []byte, fs.FileMode) error
	WriteValue(string, string) error
	Remove(string) error
	Run(context.Context, string, ...string) (string, error)
	Lock(string) (func(), error)
}
type LocalSystem struct{}

func (LocalSystem) ReadFile(p string) ([]byte, error)       { return os.ReadFile(p) }
func (LocalSystem) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }
func (LocalSystem) Stat(p string) (fs.FileInfo, error)      { return os.Lstat(p) }
func (LocalSystem) Remove(p string) error                   { return os.Remove(p) }
func (LocalSystem) Run(ctx context.Context, n string, a ...string) (string, error) {
	budget := 30 * time.Second
	switch {
	case n == "apt-get":
		budget = 20 * time.Minute
	case n == "systemctl" && len(a) > 1 && (a[0] == "stop" || a[0] == "start") && slices.Contains(maintainedServices, a[len(a)-1]):
		// A Zoomies unit stops by waiting for the tasks it admitted -- a cold image pull
		// included -- and its unit allows 20 minutes for it, so a client that gave up at
		// three left systemd finishing a stop nothing would then start again.
		budget = 21 * time.Minute
	case n == "systemctl" && len(a) > 0 && (a[0] == "stop" || a[0] == "start" || a[0] == "restart"),
		n == "docker" && len(a) > 0 && a[0] == "stop":
		// A service or a daemon stopping waits on what it is running: Docker
		// gives each container ten seconds before it kills it.
		budget = 3 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	c := exec.CommandContext(cctx, n, a...)
	c.Env = append(os.Environ(), "LC_ALL=C", "DEBIAN_FRONTEND=noninteractive")
	b, e := c.CombinedOutput()
	return strings.TrimSpace(string(b)), e
}

// WriteValue writes a kernel virtual file directly; atomic renames cannot be
// used on procfs or sysfs. It is never used for persistent configuration.
func (LocalSystem) WriteValue(p, v string) error { return os.WriteFile(p, []byte(v+"\n"), 0644) }
func fileOwner(info fs.FileInfo) (int, int) {
	if info == nil {
		return -1, -1
	}
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return -1, -1
	}
	u, g := v.FieldByName("Uid"), v.FieldByName("Gid")
	if u.IsValid() && g.IsValid() {
		return int(u.Uint()), int(g.Uint())
	}
	return -1, -1
}
func preserveOwner(f *os.File, info fs.FileInfo) error {
	if info == nil {
		return nil
	}
	v := reflect.ValueOf(info.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	u, g := v.FieldByName("Uid"), v.FieldByName("Gid")
	if u.IsValid() && g.IsValid() {
		return f.Chown(int(u.Uint()), int(g.Uint()))
	}
	return nil
}
func (LocalSystem) Lock(p string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(filepath.Dir(p))
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, fmt.Errorf("tuning state directory must be a real directory with mode 0700")
	}
	if uid, _ := fileOwner(info); uid != 0 {
		return nil, fmt.Errorf("tuning state directory must be owned by root")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("tuning is locked at %s; check for another tune process before removing a stale lock: %w", p, err)
	}
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}
func (LocalSystem) WriteFile(p string, b []byte, m fs.FileMode) error {
	if p == "/var/lib/zoomies/shared/host-health/report.json" {
		return writeHostReport(b)
	}
	if i, e := os.Lstat(p); e == nil && !i.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular file %s", p)
	}
	if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(p), ".zoomies-tune-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	owner, _ := os.Lstat(p)
	if e = preserveOwner(f, owner); e != nil {
		_ = f.Close()
		return e
	}
	if e = f.Chmod(m); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	if e = os.Rename(f.Name(), p); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(p))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

type Options struct {
	System         System
	OS             string
	UID            int
	Now            func() time.Time
	WorkDir        string
	DockerRoot     string
	DockerHost     string
	HostReportPath string
	// SecurityMaintenanceReady is deliberately false until the maintenance
	// scheduler can prove security updates are run only on a drained host.
	// TODO: wire to the effective maintenance window configuration when built.
	SecurityMaintenanceReady bool
}

func LocalOptions(workDir string) Options {
	return Options{System: LocalSystem{}, OS: runtime.GOOS, UID: os.Geteuid(), Now: time.Now, WorkDir: workDir}
}

type Result struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Tier        Tier   `json:"tier"`
	Status      Status `json:"status"`
	Current     string `json:"current"`
	Recommended string `json:"recommended"`
	Rationale   string `json:"rationale"`
	Reason      string `json:"reason,omitempty"`
	// Command is what an operator can paste into a shell on the host to put the
	// finding right by hand, for a package or setting Zoomies would never change
	// on its own (a kernel, say) or cannot from where it is running. Empty when
	// there is nothing sensible to type. It is a suggestion, never run by us.
	Command    string `json:"command,omitempty"`
	Actionable bool   `json:"actionable"`
	Optional   bool   `json:"optional,omitempty"`
	// Accepted, Ended and Acceptable are the controller's reading of the
	// operator's decisions (see Report.Judged). An agent never sets them: they are
	// scrubbed on the way in and stamped on a copy on the way out.
	Accepted   *Acceptance `json:"accepted,omitempty"`
	Ended      *Ended      `json:"ended,omitempty"`
	Acceptable bool        `json:"acceptable,omitempty"`
}
type Report struct {
	CheckedAt     time.Time `json:"checked_at"`
	OS            string    `json:"os"`
	Distro        string    `json:"distro"`
	WorkDir       string    `json:"work_dir,omitempty"`
	Container     bool      `json:"container"`
	Results       []Result  `json:"results"`
	RebootPending bool      `json:"reboot_pending"`
}

func (r Report) Counts() (warnings, errors, skipped int) {
	for _, x := range r.Results {
		// An accepted warning is the operator's decision, so doctor on the page
		// and doctor in a terminal agree on what is left. A local run never has
		// one: only the controller stamps it.
		if x.Accepting() {
			continue
		}
		switch x.Status {
		case Warn:
			warnings++
		case Error:
			errors++
		case Skip:
			skipped++
		}
	}
	return
}
func (r Report) ExitCode() int {
	w, e, _ := r.Counts()
	if e > 0 {
		return 2
	}
	if w > 0 {
		return 1
	}
	return 0
}

// Check keeps detection separate from the plan that Apply and Revert execute.
// A nil Plan is a report-only check, which cannot be applied accidentally.
type Check struct {
	ID, Title string
	Tier      Tier
	Rationale string
	Optional  bool
	Detect    func(context.Context, *Engine) Result
	Plan      func(context.Context, *Engine, Result) (Change, error)
}
type Engine struct {
	Options
	Distro, Version string
	Container       bool
	Checks          []Check
}

func New(o Options) *Engine {
	if o.System == nil {
		o.System = LocalSystem{}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.WorkDir == "" {
		o.WorkDir = "/var/lib/zoomies/work"
	}
	e := &Engine{Options: o}
	b, _ := o.System.ReadFile("/etc/os-release")
	for _, l := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if ok {
			v = strings.Trim(v, "\"'")
			if k == "ID" {
				e.Distro = v
			}
			if k == "VERSION_ID" {
				e.Version = v
			}
		}
	}
	for _, p := range []string{"/.dockerenv", "/run/.containerenv", "/run/systemd/container"} {
		if _, err := o.System.Stat(p); err == nil {
			e.Container = true
		}
	}
	if b, err := o.System.ReadFile("/proc/1/cgroup"); err == nil && (strings.Contains(string(b), "docker") || strings.Contains(string(b), "lxc") || strings.Contains(string(b), "kubepods")) {
		e.Container = true
	}
	e.Checks = baseChecks()
	e.Checks = append(e.Checks, kernelChecks()...)
	e.Checks = append(e.Checks, dedicatedChecks()...)
	return e
}

// supportedPlatforms is where tuning is expected to do what it says: the
// distributions whose sysctl layout, Docker daemon file and journal settings the
// changes were written against. Anywhere else the checks still report, and
// nothing is changed, because a fix that is right for one distribution is a guess
// on another. Adding a release is one row here, plus a line in
// docs/host-health.md; the kernel's HWE check has its own, narrower rule.
var supportedPlatforms = []struct {
	distro string
	// version is matched against VERSION_ID exactly, or against its major part
	// when major is set (Debian numbers its releases, and 13.1 is still 13).
	version string
	major   bool
}{
	{distro: "ubuntu", version: "24.04"},
	{distro: "ubuntu", version: "26.04"},
	{distro: "debian", version: "13", major: true},
}

// SupportedPlatforms names them in the order an operator would read them.
func SupportedPlatforms() string {
	names := make([]string, 0, len(supportedPlatforms))
	for _, p := range supportedPlatforms {
		names = append(names, strings.ToUpper(p.distro[:1])+p.distro[1:]+" "+p.version)
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// Platform is this host as it names itself, which is what a refusal should say:
// "linux ubuntu" does not tell an operator whether it is the release that is
// unsupported or the whole distribution.
func (e *Engine) Platform() string { return strings.TrimSpace(e.Distro + " " + e.Version) }

func (e *Engine) Supported() bool {
	if e.OS != "linux" {
		return false
	}
	for _, p := range supportedPlatforms {
		if e.Distro != p.distro {
			continue
		}
		if p.major && strings.Split(e.Version, ".")[0] == p.version || !p.major && e.Version == p.version {
			return true
		}
	}
	return false
}
func (e *Engine) Run(ctx context.Context, t Tier) Report {
	r := Report{CheckedAt: e.Now().UTC(), OS: e.OS, Distro: e.Distro + " " + e.Version, WorkDir: e.WorkDir, Container: e.Container, Results: []Result{}}
	if e.OS != "linux" {
		r.Results = append(r.Results, Result{ID: "environment", Title: "Operating system", Tier: Safe, Status: Skip, Current: e.OS, Recommended: "Linux", Rationale: "Host tuning requires Linux.", Reason: "unsupported operating system"})
		return r
	}
	if !e.Supported() {
		r.Results = append(r.Results, Result{ID: "environment", Title: "Distribution", Tier: Safe, Status: Warn, Current: r.Distro, Recommended: SupportedPlatforms(), Rationale: "Other distributions are report-only in this release."})
	}
	for _, c := range e.Checks {
		if c.Tier == Dedicated && t != Dedicated {
			continue
		}
		if c.Tier == Aggressive && t == Safe {
			continue
		}
		x := c.Detect(ctx, e)
		x.ID = c.ID
		x.Title = c.Title
		x.Tier = c.Tier
		x.Rationale = c.Rationale
		x.Optional = c.Optional
		if c.ID == "kernel.hwe-install" && strings.HasPrefix(x.Reason, "optional install") {
			x.Reason = ""
		}
		x.Actionable = c.Plan != nil && x.Status == Warn && x.Reason == "" && e.Supported() && !e.Container && e.UID == 0
		if c.ID == KernelPending && x.Status == Warn {
			r.RebootPending = true
		}
		r.Results = append(r.Results, x)
	}
	return r
}
func (e *Engine) Check(id string) (Check, bool) {
	for _, c := range e.Checks {
		if c.ID == id {
			return c, true
		}
	}
	return Check{}, false
}
func read(e *Engine, p string) (string, error) {
	b, err := e.System.ReadFile(p)
	return strings.TrimSpace(string(b)), err
}
func unavailable(err error) Result {
	if errors.Is(err, fs.ErrPermission) {
		return Result{Status: Skip, Reason: "root access is needed to read this setting"}
	}
	if errors.Is(err, fs.ErrNotExist) {
		return Result{Status: Skip, Reason: "not available on this host"}
	}
	return Result{Status: Error, Reason: err.Error()}
}
func number(s string) int64 { n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64); return n }
func command(ctx context.Context, e *Engine, n string, a ...string) (string, error) {
	return e.System.Run(ctx, n, a...)
}

// The shared parent belongs to the container account. Hold a directory handle
// and refuse an untrusted subdirectory so it cannot redirect a root reporter
// into an OS configuration file by replacing a path with a symlink.
func writeHostReport(b []byte) error {
	base, err := os.OpenRoot("/var/lib/zoomies/shared")
	if err != nil {
		return err
	}
	defer base.Close()
	expected, err := base.Lstat("host-health")
	if err != nil {
		return err
	}
	if !expected.IsDir() {
		return fmt.Errorf("host-health must be a real directory")
	}
	root, err := base.OpenRoot("host-health")
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(expected, info) {
		return fmt.Errorf("host-health directory changed while opening it")
	}
	uid, _ := fileOwner(info)
	if uid != 0 || info.Mode().Perm()&0022 != 0 {
		return fmt.Errorf("host-health directory must be root-owned and not group/world-writable")
	}
	name := ".report-" + rand.Text()
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer root.Remove(name)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return root.Rename(name, "report.json")
}
