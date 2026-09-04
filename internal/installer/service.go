package installer

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"
)

// templateFS holds the unit files, the launchd plist and the compose file.
//
// They are embedded rather than read from disk because the installer must work
// from a single downloaded binary on a host that has never seen this
// repository. deploy/zoomies.service is the same unit; this copy is the one
// that ships.
//
//go:embed templates/*.tmpl
var templateFS embed.FS

var unitTemplates = template.Must(template.New("").Funcs(template.FuncMap{
	"join": strings.Join,
}).ParseFS(templateFS, "templates/*.tmpl"))

// ServiceKind names the supervisor an installation will be managed by.
type ServiceKind string

const (
	// ServiceSystemd installs a unit under /etc/systemd/system.
	ServiceSystemd ServiceKind = "systemd"
	// ServiceLaunchd installs a plist, which is how macOS keeps a development
	// controller running.
	ServiceLaunchd ServiceKind = "launchd"
	// ServiceCompose prints a docker-compose.yml for hosts with neither, so
	// the operator still ends up with something that restarts on boot.
	ServiceCompose ServiceKind = "compose"
	// ServiceNone leaves the operator to run the binary themselves.
	ServiceNone ServiceKind = "none"
)

// Unit names the two services Zoomies installs.
const (
	// UnitController is the controller, with or without an embedded agent.
	UnitController = "zoomies"
	// UnitAgent is a standalone agent on a runner host.
	UnitAgent = "zoomies-agent"
)

// ServiceSpec is everything the unit templates need. It is a plain struct so
// that rendering can be tested without a service manager, an init system or
// root.
type ServiceSpec struct {
	// Unit is UnitController or UnitAgent.
	Unit string
	// Description is the one-line summary systemd and launchd show.
	Description string
	// ExecPath is the zoomies binary the service runs.
	ExecPath string
	// Command is the subcommand: "controller" or "agent".
	Command    string
	ConfigFile string
	User       string
	Group      string
	StateDir   string
	ConfigDir  string
	// SupplementaryGroups are extra groups the service joins, which is how a
	// root Docker socket becomes usable without running as root.
	SupplementaryGroups []string
	// RuntimeName names the runtime those groups exist for, so the unit's
	// comment says why the line is there.
	RuntimeName string
	// Bind is the listen address, used only to decide whether the service
	// needs CAP_NET_BIND_SERVICE.
	Bind string
	// StopTimeout bounds a graceful stop. The agent's is long because it waits
	// for jobs; the controller's is short because it does not kill runners.
	StopTimeout time.Duration
	// WantsDocker orders the unit after the Docker daemon.
	WantsDocker bool
	// Home and Path are launchd's environment, which is otherwise empty.
	Home string
	Path string
	// LogFile is where launchd sends stdout and stderr, since macOS has no
	// journal.
	LogFile string
}

// Label is the launchd job label, which doubles as the plist's file name.
func (s ServiceSpec) Label() string {
	if s.Unit == UnitAgent {
		return "sh.zoomies.agent"
	}
	return "sh.zoomies.controller"
}

// StandardDirs reports whether the state and config directories are the ones
// systemd's StateDirectory= and ConfigurationDirectory= would create. When an
// operator has chosen their own, those directives would point elsewhere, so
// they are left out and ReadWritePaths does the work instead.
func (s ServiceSpec) StandardDirs() bool {
	return s.StateDir == "/var/lib/zoomies" && s.ConfigDir == "/etc/zoomies"
}

// NeedsNetBind reports whether the listener wants a privileged port, which is
// the only reason this service is ever given a capability.
func (s ServiceSpec) NeedsNetBind() bool {
	_, port, err := splitBind(s.Bind)
	return err == nil && port > 0 && port < 1024
}

// defaults fills the fields a caller may reasonably leave empty, and returns an
// error for the ones it may not.
func (s *ServiceSpec) defaults() error {
	if s.Unit == "" {
		s.Unit = UnitController
	}
	if s.Command == "" {
		if s.Unit == UnitAgent {
			s.Command = "agent"
		} else {
			s.Command = "controller"
		}
	}
	if s.Description == "" {
		if s.Unit == UnitAgent {
			s.Description = "Zoomies runner agent"
		} else {
			s.Description = "Zoomies GitHub Actions runner fleet controller"
		}
	}
	if s.StopTimeout == 0 {
		if s.Unit == UnitAgent {
			s.StopTimeout = 600 * time.Second
		} else {
			s.StopTimeout = 60 * time.Second
		}
	}
	if s.ExecPath == "" {
		return errors.New("installer: the service needs the path of the zoomies binary; pass --installed-binary or install it to /usr/local/bin/zoomies")
	}
	if s.ConfigFile == "" {
		return errors.New("installer: the service needs a config file path; it is what `zoomies " + s.Command + " --config` will read")
	}
	if s.User == "" {
		return errors.New("installer: the service needs a user to run as; on Linux that is the dedicated \"zoomies\" account, on macOS your own")
	}
	if s.Group == "" {
		s.Group = s.User
	}
	if s.StateDir == "" {
		return errors.New("installer: the service needs a state directory, for example /var/lib/zoomies")
	}
	if s.LogFile == "" {
		s.LogFile = filepath.Join(s.StateDir, s.Unit+".log")
	}
	if s.Home == "" {
		s.Home = s.StateDir
	}
	return nil
}

// RenderSystemdUnit renders the unit file for a spec.
func RenderSystemdUnit(spec ServiceSpec) (string, error) {
	if err := spec.defaults(); err != nil {
		return "", err
	}
	name := "zoomies.service.tmpl"
	if spec.Unit == UnitAgent {
		name = "zoomies-agent.service.tmpl"
	}
	return render(name, systemdView{ServiceSpec: spec,
		StandardDirs: spec.StandardDirs(),
		NeedsNetBind: spec.NeedsNetBind(),
		StopTimeout:  formatSeconds(spec.StopTimeout),
	})
}

// systemdView adapts a spec for the template, so that the template contains no
// logic beyond choosing between two lines.
type systemdView struct {
	ServiceSpec
	StandardDirs bool
	NeedsNetBind bool
	StopTimeout  string
}

// RenderLaunchdPlist renders the macOS job description for a spec.
func RenderLaunchdPlist(spec ServiceSpec) (string, error) {
	if err := spec.defaults(); err != nil {
		return "", err
	}
	return render("launchd.plist.tmpl", launchdView{ServiceSpec: spec, Label: spec.Label()})
}

type launchdView struct {
	ServiceSpec
	Label string
}

// ComposeSpec is what RenderCompose needs. The encryption key is deliberately
// absent: the file references an environment variable instead, because a
// compose file usually ends up in a repository.
type ComposeSpec struct {
	Image       string
	ExternalURL string
	Port        int
	Backend     string
	DockerHost  string
	SocketPath  string
	Capacity    int
	Embedded    bool
	DockerGID   int
}

// RenderCompose writes a docker-compose.yml for hosts with neither systemd nor
// launchd, so that "there is no service manager here" still ends with something
// that comes back after a reboot.
func RenderCompose(w io.Writer, spec ComposeSpec) error {
	if spec.Image == "" {
		spec.Image = "ghcr.io/eyupio/zoomies:main"
	}
	if spec.Port == 0 {
		spec.Port = 8080
	}
	if spec.Backend == "" {
		spec.Backend = "docker"
	}
	if spec.DockerHost == "" {
		spec.DockerHost = "unix:///var/run/docker.sock"
	}
	if spec.SocketPath == "" {
		spec.SocketPath = strings.TrimPrefix(spec.DockerHost, "unix://")
	}
	if spec.Capacity <= 0 {
		spec.Capacity = 4
	}
	if spec.DockerGID == 0 {
		spec.DockerGID = 999
	}
	if spec.ExternalURL == "" {
		spec.ExternalURL = "https://zoomies.example.com"
	}
	out, err := render("compose.yml.tmpl", spec)
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, out)
	return err
}

func render(name string, data any) (string, error) {
	var b bytes.Buffer
	if err := unitTemplates.ExecuteTemplate(&b, name, data); err != nil {
		return "", fmt.Errorf("installer: rendering %s: %w", name, err)
	}
	return b.String(), nil
}

// formatSeconds renders a duration the way systemd writes them, which keeps
// the generated unit readable next to the hand-written one in deploy/.
func formatSeconds(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + "s"
}

// SystemdUnitPath is where a unit of this name lives.
func SystemdUnitPath(unit string) string {
	return filepath.Join("/etc/systemd/system", unit+".service")
}

// LaunchdPlistPath is where a launchd job of this name lives. A root install
// is a daemon that starts at boot; a user install is an agent that starts at
// login, which is what a development controller on a laptop wants.
func LaunchdPlistPath(label string, root bool) string {
	if root {
		return filepath.Join("/Library/LaunchDaemons", label+".plist")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist")
}

// ReadUnitIdentity recovers the account and groups an existing systemd unit
// runs as.
//
// An upgrade rewrites the unit, and rewriting it with this installer's
// defaults would quietly move a controller onto a different account -- and
// away from the files that account owns. Whatever is already there wins.
func ReadUnitIdentity(path string) (userName, group string, groups []string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", nil
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "User="):
			userName = strings.TrimSpace(strings.TrimPrefix(line, "User="))
		case strings.HasPrefix(line, "Group="):
			group = strings.TrimSpace(strings.TrimPrefix(line, "Group="))
		case strings.HasPrefix(line, "SupplementaryGroups="):
			groups = strings.Fields(strings.TrimPrefix(line, "SupplementaryGroups="))
		}
	}
	return userName, group, groups
}

// ServiceManager installs and controls a service through whichever supervisor
// this host actually runs.
//
// Every method is allowed to fail with an explanation rather than a status
// code: setup has to be able to tell an operator what to do next, and "exit
// status 1" is not that.
type ServiceManager interface {
	// Kind names the supervisor.
	Kind() ServiceKind
	// Install writes the unit and returns the path it wrote.
	Install(ctx context.Context, spec ServiceSpec) (string, error)
	// Enable makes the service start at boot.
	Enable(ctx context.Context) error
	// Start starts it now.
	Start(ctx context.Context) error
	// Stop stops it, and is not an error when it was not running.
	Stop(ctx context.Context) error
	// Disable undoes Enable, and is not an error when it was not enabled.
	Disable(ctx context.Context) error
	// Remove deletes the unit file.
	Remove(ctx context.Context) error
	// Status returns one line describing the service's current state.
	Status(ctx context.Context) (string, error)
	// Logs returns the last n lines, for the failure path of a health check.
	Logs(ctx context.Context, n int) (string, error)
	// LogCommand is the command the operator should run to see more, printed
	// alongside those lines.
	LogCommand() string
}

// DetectServiceKind chooses the supervisor for this host. It only ever returns
// one that is actually present: shelling out to systemctl on Alpine is how
// installers produce their most confusing errors.
func DetectServiceKind(d Detection) ServiceKind {
	switch {
	case d.HasSystemd:
		return ServiceSystemd
	case d.HasLaunchd:
		return ServiceLaunchd
	case d.Docker.Available || d.HasCompose:
		return ServiceCompose
	default:
		return ServiceNone
	}
}

// commandRunner runs an external command. It is a field on the managers so
// that tests can watch what would have been run without running it.
type commandRunner func(ctx context.Context, name string, args ...string) (string, error)

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("installer: %s is not on PATH, so this step cannot run here: %w", name, err)
	}
	cmd := exec.CommandContext(ctx, path, args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	text := strings.TrimSpace(out.String())
	if err != nil {
		if text != "" {
			return text, fmt.Errorf("installer: %s %s failed: %w: %s", name, strings.Join(args, " "), err, text)
		}
		return text, fmt.Errorf("installer: %s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return text, nil
}

// NewServiceManager returns the manager for a kind, refusing one whose tooling
// is not installed rather than failing later in the middle of setup.
func NewServiceManager(kind ServiceKind, unit string) (ServiceManager, error) {
	if unit == "" {
		unit = UnitController
	}
	switch kind {
	case ServiceSystemd:
		if lookPath("systemctl") == "" {
			return nil, errors.New("installer: systemd was selected but systemctl is not on PATH; re-run with --service none and start `zoomies controller` yourself, or install systemd")
		}
		return &systemdManager{unit: unit, run: runCommand}, nil
	case ServiceLaunchd:
		if lookPath("launchctl") == "" {
			return nil, errors.New("installer: launchd was selected but launchctl is not on PATH; this is not macOS")
		}
		return &launchdManager{unit: unit, root: os.Geteuid() == 0, run: runCommand}, nil
	case ServiceCompose, ServiceNone:
		return nil, fmt.Errorf("installer: %q installs no service; the installer prints what to run instead", kind)
	default:
		return nil, fmt.Errorf("installer: %q is not a service manager; use systemd, launchd, compose or none", kind)
	}
}

// systemdManager drives systemctl for one unit.
type systemdManager struct {
	unit string
	run  commandRunner
	// path is remembered by Install so Remove needs no second guess.
	path string
}

func (m *systemdManager) Kind() ServiceKind { return ServiceSystemd }

func (m *systemdManager) unitName() string { return m.unit + ".service" }

// Install writes the unit and reloads systemd so the new file is seen.
func (m *systemdManager) Install(ctx context.Context, spec ServiceSpec) (string, error) {
	spec.Unit = m.unit
	body, err := RenderSystemdUnit(spec)
	if err != nil {
		return "", err
	}
	path := SystemdUnitPath(m.unit)
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("installer: writing %s: %w (this step needs root; re-run with sudo)", path, err)
	}
	m.path = path
	if _, err := m.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return path, err
	}
	return path, nil
}

func (m *systemdManager) Enable(ctx context.Context) error {
	_, err := m.run(ctx, "systemctl", "enable", m.unitName())
	return err
}

func (m *systemdManager) Start(ctx context.Context) error {
	_, err := m.run(ctx, "systemctl", "start", m.unitName())
	return err
}

// Stop is quiet about a unit that is not loaded, because uninstall has to be
// safe to re-run.
func (m *systemdManager) Stop(ctx context.Context) error {
	if !m.loaded(ctx) {
		return nil
	}
	_, err := m.run(ctx, "systemctl", "stop", m.unitName())
	return err
}

func (m *systemdManager) Disable(ctx context.Context) error {
	if !m.loaded(ctx) {
		return nil
	}
	_, err := m.run(ctx, "systemctl", "disable", m.unitName())
	return err
}

func (m *systemdManager) Remove(ctx context.Context) error {
	path := m.path
	if path == "" {
		path = SystemdUnitPath(m.unit)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("installer: removing %s: %w", path, err)
	}
	_, _ = m.run(ctx, "systemctl", "daemon-reload")
	_, _ = m.run(ctx, "systemctl", "reset-failed", m.unitName())
	return nil
}

func (m *systemdManager) Status(ctx context.Context) (string, error) {
	out, err := m.run(ctx, "systemctl", "is-active", m.unitName())
	if out == "" && err != nil {
		return "", err
	}
	// is-active exits non-zero for anything but "active", and the word it
	// prints is more useful than the exit code.
	return out, nil
}

func (m *systemdManager) Logs(ctx context.Context, n int) (string, error) {
	if lookPath("journalctl") == "" {
		return "", errors.New("installer: journalctl is not on PATH, so the service's logs cannot be shown here; try `systemctl status " + m.unitName() + "`")
	}
	out, err := m.run(ctx, "journalctl", "-u", m.unitName(), "-n", strconv.Itoa(n), "--no-pager")
	if err != nil && out == "" {
		return "", err
	}
	return out, nil
}

func (m *systemdManager) LogCommand() string {
	return "journalctl -u " + m.unitName() + " -f"
}

func (m *systemdManager) loaded(ctx context.Context) bool {
	out, _ := m.run(ctx, "systemctl", "list-unit-files", m.unitName())
	return strings.Contains(out, m.unitName())
}

// launchdManager drives launchctl for one job.
type launchdManager struct {
	unit string
	root bool
	run  commandRunner
	path string
	log  string
}

func (m *launchdManager) Kind() ServiceKind { return ServiceLaunchd }

func (m *launchdManager) label() string {
	if m.unit == UnitAgent {
		return "sh.zoomies.agent"
	}
	return "sh.zoomies.controller"
}

func (m *launchdManager) domain() string {
	if m.root {
		return "system"
	}
	return "gui/" + strconv.Itoa(os.Getuid())
}

func (m *launchdManager) Install(ctx context.Context, spec ServiceSpec) (string, error) {
	spec.Unit = m.unit
	body, err := RenderLaunchdPlist(spec)
	if err != nil {
		return "", err
	}
	path := LaunchdPlistPath(m.label(), m.root)
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("installer: writing %s: %w", path, err)
	}
	m.path = path
	m.log = spec.LogFile
	// A job already loaded from an older plist would keep running the old
	// command, so the load is always preceded by an unload.
	_, _ = m.run(ctx, "launchctl", "bootout", m.domain()+"/"+m.label())
	return path, nil
}

// Enable is a no-op beyond bootstrapping: RunAtLoad in the plist is what makes
// a launchd job come back, and bootstrapping is what Start does.
func (m *launchdManager) Enable(ctx context.Context) error {
	_, err := m.run(ctx, "launchctl", "enable", m.domain()+"/"+m.label())
	return err
}

func (m *launchdManager) Start(ctx context.Context) error {
	path := m.path
	if path == "" {
		path = LaunchdPlistPath(m.label(), m.root)
	}
	if _, err := m.run(ctx, "launchctl", "bootstrap", m.domain(), path); err != nil {
		return err
	}
	_, err := m.run(ctx, "launchctl", "kickstart", m.domain()+"/"+m.label())
	return err
}

func (m *launchdManager) Stop(ctx context.Context) error {
	_, _ = m.run(ctx, "launchctl", "bootout", m.domain()+"/"+m.label())
	return nil
}

func (m *launchdManager) Disable(ctx context.Context) error {
	_, _ = m.run(ctx, "launchctl", "disable", m.domain()+"/"+m.label())
	return nil
}

func (m *launchdManager) Remove(ctx context.Context) error {
	path := m.path
	if path == "" {
		path = LaunchdPlistPath(m.label(), m.root)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("installer: removing %s: %w", path, err)
	}
	return nil
}

func (m *launchdManager) Status(ctx context.Context) (string, error) {
	out, err := m.run(ctx, "launchctl", "print", m.domain()+"/"+m.label())
	if err != nil {
		return "not loaded", nil
	}
	for _, line := range strings.Split(out, "\n") {
		if s := strings.TrimSpace(line); strings.HasPrefix(s, "state = ") {
			return strings.TrimPrefix(s, "state = "), nil
		}
	}
	return "loaded", nil
}

// Logs tails the file the plist points at, because macOS has no journal and
// `log show` cannot be filtered usefully by job label.
func (m *launchdManager) Logs(_ context.Context, n int) (string, error) {
	path := m.log
	if path == "" {
		path = filepath.Join("/var/log", m.unit+".log")
	}
	return tailFile(path, n)
}

func (m *launchdManager) LogCommand() string {
	path := m.log
	if path == "" {
		path = filepath.Join("/var/log", m.unit+".log")
	}
	return "tail -f " + path
}

// tailFile returns the last n lines of a file, which is all the failure path
// of a health check needs.
func tailFile(path string, n int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("installer: reading %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n"), nil
}

// writeFileAtomic writes through a temporary file and a rename, so that a
// crash halfway through never leaves a half-written unit that systemd would
// then refuse to parse.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
