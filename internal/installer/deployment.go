package installer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Deployment is how Zoomies is run on this host, which is a separate question
// from Mode.
//
// Mode says what this host is -- a controller, an agent, or both in one
// process. Deployment says what supervises it. The two are orthogonal: an
// agent can be a binary under systemd or a container, and a single-host
// controller can be either as well. Keeping them apart is what stops the
// installer growing a matrix of nine special cases.
type Deployment string

const (
	// DeploymentNative runs the binary directly, supervised by systemd or
	// launchd. It is the leanest option and the only one that needs no
	// container runtime for the controller itself.
	DeploymentNative Deployment = "native"
	// DeploymentCompose writes a docker-compose.yml and a fully populated
	// .env, then brings them up. It is the easiest to upgrade and to move to
	// another host, because the whole deployment is two files.
	DeploymentCompose Deployment = "compose"
	// DeploymentDocker runs a single container from an env file. Fewest files,
	// but the operator owns the run command from then on.
	DeploymentDocker Deployment = "docker"
)

// ParseDeployment validates a --deployment value, naming the alternatives when
// it is not one of them. An empty string means "ask", the same as ParseMode.
func ParseDeployment(s string) (Deployment, error) {
	switch Deployment(strings.ToLower(strings.TrimSpace(s))) {
	case DeploymentNative:
		return DeploymentNative, nil
	case DeploymentCompose:
		return DeploymentCompose, nil
	case DeploymentDocker:
		return DeploymentDocker, nil
	case "":
		return "", nil
	default:
		return "", fmt.Errorf("installer: %q is not a deployment; use native (the binary under systemd or launchd), "+
			"compose (a docker-compose.yml and a .env) or docker (a single container)", s)
	}
}

// Containerised reports whether this deployment runs Zoomies inside a
// container, which is the difference that decides where its database lives and
// which questions setup still has to ask.
func (d Deployment) Containerised() bool {
	return d == DeploymentCompose || d == DeploymentDocker
}

// DeploymentOption is one deployment the prompt offers: what the operator
// gets, and what it costs them.
type DeploymentOption struct {
	Deployment  Deployment
	Label       string
	Description string
	// Default marks the one chosen when the operator just presses Enter.
	Default bool
}

// DeploymentOptions returns the deployments this host can actually carry out,
// best-understood first.
//
// Only workable choices are offered: compose needs a compose command that
// answered, docker needs a socket this user can talk to, and native always
// works because a binary and a supervisor are always available -- even if the
// supervisor turns out to be the operator.
func DeploymentOptions(d Detection) []DeploymentOption {
	def := DefaultDeployment(d)
	// The containerised options change the shape of the rest of setup, not
	// only where the process lives: the database is in a volume this installer
	// cannot open, so the administrator, the GitHub App and the first pool are
	// all done in the browser afterwards. Saying so here is the difference
	// between choosing that and discovering it.
	const deferred = " The administrator, the GitHub App and the first pool are then set up in the browser, not here."
	out := []DeploymentOption{{
		Deployment: DeploymentNative,
		Label:      "Native -- " + nativeSupervisor(d),
		Description: "Leanest, starts fastest, and needs no container runtime for the controller itself. " +
			"Setup finishes everything here, including your first pool.",
	}}
	if d.Compose.Available {
		out = append(out, DeploymentOption{
			Deployment: DeploymentCompose,
			Label:      "Compose -- docker-compose.yml and .env in " + d.ConfigDir,
			Description: "Writes both, then brings them up with `" + d.Compose.String() +
				" up -d`. Easiest to upgrade and to move to another host." + deferred,
		})
	}
	if d.Docker.Available {
		out = append(out, DeploymentOption{
			Deployment:  DeploymentDocker,
			Label:       "Docker -- a single container",
			Description: "Fewest files: an env file and one container. You manage the run command yourself." + deferred,
		})
	}
	for n := range out {
		if out[n].Deployment == def {
			out[n].Default = true
			out[n].Label += " (default)"
		}
	}
	return out
}

// nativeSupervisor describes what would keep a native install running, so that
// the option does not promise a systemd unit on a host with no systemd.
func nativeSupervisor(d Detection) string {
	switch {
	case d.HasSystemd:
		return "the binary under systemd"
	case d.HasLaunchd:
		return "the binary under launchd"
	default:
		return "the binary, started by you"
	}
}

// DefaultDeployment is what the operator gets by pressing Enter, and what a
// non-interactive run takes when the answer file is silent.
//
// Compose wins whenever a compose command exists: an operator who has gone to
// the trouble of installing Compose almost certainly wants their services in
// it, and a compose deployment is the one that upgrades and moves cleanly.
func DefaultDeployment(d Detection) Deployment {
	if d.Compose.Available {
		return DeploymentCompose
	}
	return DeploymentNative
}

// DeploymentAvailable reports whether this host can carry out a deployment,
// which is what turns a --deployment flag into an error the operator can fix
// rather than a failure half-way through setup.
func DeploymentAvailable(d Detection, want Deployment) bool {
	switch want {
	case DeploymentCompose:
		return d.Compose.Available
	case DeploymentDocker:
		return d.Docker.Available
	default:
		return true
	}
}

// UnavailableDeployment explains why a deployment cannot be used here and what
// to do about it. It returns an empty string when the deployment is fine.
func UnavailableDeployment(d Detection, want Deployment) string {
	if DeploymentAvailable(d, want) {
		return ""
	}
	switch want {
	case DeploymentCompose:
		detail := "no compose command answered on this host"
		if d.Compose.Detail != "" {
			detail = d.Compose.Detail
		}
		return "installer: --deployment compose was asked for, but " + detail +
			". Install the Compose plugin (`docker compose version` must work), or use --deployment native."
	case DeploymentDocker:
		detail := "no Docker socket answered here"
		if d.Docker.Detail != "" {
			detail = firstLine(d.Docker.Detail)
		}
		return "installer: --deployment docker was asked for, but " + detail +
			". Fix that, or use --deployment native."
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// The record
// ---------------------------------------------------------------------------

// deploymentRecordFile is the name of the record inside the config directory.
const deploymentRecordFile = "deployment.json"

// DeploymentRecord is what `zoomies init` leaves behind so that
// `zoomies uninstall` can take the same deployment down again.
//
// Without it, uninstall would have to guess: it would stop a systemd unit that
// was never installed and leave a container running that the operator believes
// is gone. Everything needed to tear the deployment down is written here, in
// the words it was created with -- including which compose command was used,
// because a host can have either.
type DeploymentRecord struct {
	Deployment Deployment `json:"deployment"`
	// Directory holds docker-compose.yml and the env file.
	Directory string `json:"directory"`
	// ComposeCommand is the argv prefix, e.g. ["docker","compose"].
	ComposeCommand []string `json:"compose_command,omitempty"`
	// Container, Image, Volume and Network name what a docker deployment
	// created, so that removing it does not depend on this code's defaults
	// still being what they were on the day it was installed.
	Container string `json:"container,omitempty"`
	Image     string `json:"image,omitempty"`
	Volume    string `json:"volume,omitempty"`
	Network   string `json:"network,omitempty"`
	EnvFile   string `json:"env_file,omitempty"`
	Mode      Mode   `json:"mode,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
}

// ComposeFile is the compose file this record's deployment was brought up
// with.
func (r DeploymentRecord) ComposeFile() string {
	if r.Directory == "" {
		return ""
	}
	return filepath.Join(r.Directory, ComposeFileName)
}

// WriteDeploymentRecord saves the record beside the configuration. It is
// written atomically at 0640 so that an interrupted install never leaves
// uninstall reading half a JSON document.
func WriteDeploymentRecord(configDir string, r DeploymentRecord) (string, error) {
	if r.CreatedAt == "" {
		r.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("installer: recording the deployment: %w", err)
	}
	path := filepath.Join(configDir, deploymentRecordFile)
	if err := writeFileAtomic(path, append(b, '\n'), 0o640); err != nil {
		return "", fmt.Errorf("installer: writing %s: %w", path, err)
	}
	return path, nil
}

// ReadDeploymentRecord reads the record back. A missing or unreadable record
// is not an error: it means this host was installed natively, or by a version
// of Zoomies that predates the record, and the caller falls back to looking
// for a unit file.
func ReadDeploymentRecord(configDir string) (DeploymentRecord, bool) {
	b, err := os.ReadFile(filepath.Join(configDir, deploymentRecordFile))
	if err != nil {
		return DeploymentRecord{}, false
	}
	var r DeploymentRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return DeploymentRecord{}, false
	}
	if r.Deployment == "" {
		return DeploymentRecord{}, false
	}
	return r, true
}

// DeploymentRecordPath is where the record for a config directory lives, which
// uninstall needs so it can remove it along with everything else.
func DeploymentRecordPath(configDir string) string {
	return filepath.Join(configDir, deploymentRecordFile)
}
