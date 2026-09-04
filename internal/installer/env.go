package installer

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
)

// EnvFileName is the environment file a compose deployment reads. Compose
// picks it up automatically from the directory the project is brought up in,
// which is why it is not named after Zoomies.
const EnvFileName = ".env"

// DockerEnvFileName is the environment file a single-container deployment
// reads. It is named rather than hidden because nothing picks it up
// automatically: `docker run --env-file` has to be pointed at it, and an
// operator reading their own shell history should be able to tell what it is.
const DockerEnvFileName = "zoomies.env"

// EnvSpec is every value a generated environment file carries.
//
// It is a plain struct with no behaviour so that RenderEnv can be a pure
// function from it to a string: the file the operator ends up with is then
// something a test can assert on directly, character for character, rather
// than something that only exists after an install has touched a host.
type EnvSpec struct {
	// Deployment and Mode decide which variables belong in the file at all: a
	// controller needs a listener, an agent needs a controller to talk to.
	Deployment Deployment
	Mode       Mode

	// ExternalURL is how GitHub and browsers reach this controller.
	ExternalURL string
	// EncryptionKey is base64, 32 bytes. It is the reason the file is 0600.
	EncryptionKey string

	// Bind is the address inside the container. PublishAddr and PublishedPort
	// are the host side of the same listener.
	Bind          string
	PublishAddr   string
	PublishedPort int

	TLSMode        config.TLSMode
	TLSCertFile    string
	TLSKeyFile     string
	TrustedProxies []string

	// Embedded, Backend, DockerHost and Capacity describe the agent half.
	Embedded   bool
	Backend    string
	DockerHost string
	Capacity   int

	WorkDir string
	DBPath  string
	// StateDir is where the container keeps anything it generates, which is
	// the volume's mount point.
	StateDir string

	LogFormat string
	LogLevel  string
	// PollFallback is the queued-job poller. It is a pointer only so that
	// "not set" can take the safe default of true rather than of false.
	PollFallback *bool
	// GitHubAPIBaseURL is https://api.github.com, or a GHES API root.
	GitHubAPIBaseURL string

	// Image and DockerGID are read by compose and by docker run, not by
	// Zoomies itself.
	Image     string
	DockerGID int

	// ControllerURL and AgentToken are how a containerised agent reaches the
	// controller it has already joined.
	ControllerURL string
	AgentToken    string
}

// defaults fills in the values a caller may reasonably leave empty, so that
// RenderEnv is total: every field it prints has a sensible value, and the
// operator never opens the file to find a blank where a decision should be.
func (s EnvSpec) defaults() EnvSpec {
	if s.Deployment == "" {
		s.Deployment = DeploymentCompose
	}
	if s.Mode == "" {
		s.Mode = ModeSingle
	}
	if s.Image == "" {
		s.Image = DefaultImage
	}
	if s.Bind == "" {
		s.Bind = "0.0.0.0:" + strconv.Itoa(ContainerPort)
	}
	if s.PublishAddr == "" {
		s.PublishAddr = "127.0.0.1"
	}
	if s.PublishedPort == 0 {
		s.PublishedPort = ContainerPort
	}
	if s.TLSMode == "" {
		s.TLSMode = config.TLSOff
	}
	if s.Backend == "" {
		s.Backend = "docker"
	}
	if s.Capacity <= 0 {
		s.Capacity = defaultCapacity()
	}
	if s.WorkDir == "" {
		s.WorkDir = ContainerWorkDir
	}
	if s.DBPath == "" {
		s.DBPath = ContainerDBPath
	}
	if s.StateDir == "" {
		s.StateDir = ContainerStateDir
	}
	if s.LogFormat == "" {
		s.LogFormat = "json"
	}
	if s.LogLevel == "" {
		s.LogLevel = "info"
	}
	if s.GitHubAPIBaseURL == "" {
		s.GitHubAPIBaseURL = "https://api.github.com"
	}
	if s.PollFallback == nil {
		on := true
		s.PollFallback = &on
	}
	return s
}

// Config renders the configuration the container will actually run with.
//
// A container deployment writes no zoomies.yaml: the environment file is the
// configuration. This is what lets a test put the generated .env through
// config.Validate and assert that each deployment produces a controller that
// starts, and produces exactly the warnings that deployment deserves.
func (s EnvSpec) Config() *config.Config {
	s = s.defaults()
	cfg := config.Default()
	cfg.Server.Bind = s.Bind
	cfg.Server.ExternalURL = strings.TrimRight(s.ExternalURL, "/")
	cfg.Server.TLS.Mode = s.TLSMode
	cfg.Server.TLS.CertFile = s.TLSCertFile
	cfg.Server.TLS.KeyFile = s.TLSKeyFile
	cfg.Server.TrustedProxies = s.TrustedProxies

	cfg.Database.Path = s.DBPath
	// The key is in the environment, not in a file the container can read, so
	// the key file setting is cleared rather than left pointing at a path that
	// does not exist inside the image.
	cfg.Security.EncryptionKey = s.EncryptionKey
	cfg.Security.EncryptionKeyFile = ""

	cfg.GitHub.APIBaseURL = s.GitHubAPIBaseURL
	cfg.GitHub.PollFallback = *s.PollFallback

	cfg.Agent.Embedded = s.Embedded
	cfg.Agent.Capacity = s.Capacity
	cfg.Agent.Backend = s.Backend
	cfg.Agent.DockerHost = s.DockerHost
	cfg.Agent.WorkDir = s.WorkDir
	cfg.Agent.ControllerURL = s.ControllerURL
	cfg.Agent.AgentToken = s.AgentToken
	if cfg.Agent.Name == "" {
		cfg.Agent.Name = hostname()
	}

	cfg.Log.Format = s.LogFormat
	cfg.Log.Level = s.LogLevel

	secure := s.TLSMode != config.TLSOff || strings.HasPrefix(cfg.Server.ExternalURL, "https://")
	cfg.Security.CookieSecure = &secure
	return cfg
}

// required lists the values this file would otherwise be written with a blank
// for.
//
// A blank is a placeholder, and a placeholder is the thing this generator
// exists to abolish: an operator who has to go and fill something in has been
// handed a half-finished install and told it is done. Refusing here means the
// failure lands during setup, where it can be fixed, rather than at first
// start.
func (s EnvSpec) required() []string {
	var missing []string
	if s.Mode == ModeAgent {
		if s.ControllerURL == "" {
			missing = append(missing, "ZOOMIES_CONTROLLER_URL (the controller this host joined)")
		}
		if s.AgentToken == "" {
			missing = append(missing, "ZOOMIES_AGENT_TOKEN (the credential the join returned)")
		}
		return missing
	}
	if s.ExternalURL == "" {
		missing = append(missing, "ZOOMIES_EXTERNAL_URL (the URL GitHub and your browser use)")
	}
	if s.EncryptionKey == "" {
		missing = append(missing, "ZOOMIES_ENCRYPTION_KEY (32 bytes, base64; WriteEnv generates one when there is none to keep)")
	}
	if s.TLSMode == config.TLSFiles && (s.TLSCertFile == "" || s.TLSKeyFile == "") {
		missing = append(missing, "ZOOMIES_TLS_CERT_FILE and ZOOMIES_TLS_KEY_FILE (tls mode is \"files\")")
	}
	return missing
}

// RenderEnv renders an environment file from a spec.
//
// Two rules govern what comes out. Nothing is left as a placeholder: every
// value was answered during setup, so an operator never has to open this file
// to make it work. And every variable carries a line saying what it is for,
// because the person who reads this file next is usually reading it six months
// later, at speed, without the documentation open.
//
// It is a pure function so that the file can be asserted on in a test rather
// than only observed after an install.
func RenderEnv(spec EnvSpec) (string, error) {
	s := spec.defaults()
	if missing := s.required(); len(missing) > 0 {
		return "", fmt.Errorf("installer: the environment file would be written with nothing in:\n  - %s",
			strings.Join(missing, "\n  - "))
	}
	w := &envWriter{}

	w.header(s)

	if s.Mode == ModeAgent {
		w.section("Joining a controller")
		w.set("ZOOMIES_CONTROLLER_URL", s.ControllerURL,
			"The controller this host takes work from. It is the URL `zoomies agent join` was pointed at.")
		w.set("ZOOMIES_AGENT_TOKEN", s.AgentToken,
			"The long-lived credential the join exchanged your join token for. Treat it as a password: it lets this host claim jobs.")
	} else {
		w.section("The controller")
		w.set("ZOOMIES_EXTERNAL_URL", s.ExternalURL,
			"How GitHub and your browser reach this controller. Webhook deliveries go to this URL plus /webhooks/github, and the https here is what marks the session cookie Secure.")
		w.set("ZOOMIES_ENCRYPTION_KEY", s.EncryptionKey,
			"32 random bytes, base64. Everything secret in the database is sealed with it. Back it up separately from the database: without it the stored GitHub App private key and webhook secrets cannot be decrypted.")
		w.set("ZOOMIES_BIND", s.Bind,
			"Where the controller listens inside the container. The published port below is what actually controls exposure.")
		w.set("ZOOMIES_TLS_MODE", string(s.TLSMode), tlsModeComment(s.TLSMode))
		if s.TLSMode == config.TLSFiles {
			w.set("ZOOMIES_TLS_CERT_FILE", s.TLSCertFile,
				"The full chain to serve, PEM encoded. It is mounted into the container at this same path.")
			w.set("ZOOMIES_TLS_KEY_FILE", s.TLSKeyFile,
				"The certificate's private key, mounted read-only at this same path.")
		}
		w.set("ZOOMIES_TRUSTED_PROXIES", strings.Join(s.TrustedProxies, ","),
			"Comma-separated CIDRs whose X-Forwarded-For header is believed. Empty takes client addresses from the socket, which is safe but records your proxy's address in every audit entry and every login rate-limit decision.")
		w.set("ZOOMIES_GITHUB_API_BASE_URL", s.GitHubAPIBaseURL,
			"https://api.github.com, or https://ghes.example.com/api/v3 for GitHub Enterprise Server.")
		w.set("ZOOMIES_POLL_FALLBACK", strconv.FormatBool(*s.PollFallback),
			"The queued-job poller. Leave it on: it is what keeps jobs being picked up when a webhook delivery is lost or misconfigured.")
	}

	w.section("The agent")
	w.set("ZOOMIES_AGENT_EMBEDDED", strconv.FormatBool(s.Embedded),
		"Whether this process runs runners itself. false makes it a controller only, and runner hosts join it with `zoomies agent join`.")
	w.set("ZOOMIES_AGENT_BACKEND", s.Backend,
		"docker, podman or process. The process backend runs workflow steps directly on this host with no container isolation.")
	w.set("ZOOMIES_DOCKER_HOST", s.DockerHost,
		"The socket runner containers are created on. This is Zoomies' own access to the runtime -- it is NOT pool.docker_mode=host-socket, which would hand the socket to your jobs.")
	w.set("ZOOMIES_AGENT_CAPACITY", strconv.Itoa(s.Capacity),
		"The most runners this host may hold at once. A pool's max_runners can be lower than this, never higher.")
	w.set("ZOOMIES_WORK_DIR", s.WorkDir,
		"Scratch space for runner work directories. It is inside the volume, so it survives the container being replaced.")

	w.section("Storage and logs")
	w.set("ZOOMIES_STATE_DIR", s.StateDir,
		"Where anything generated at run time goes -- a self-signed certificate, above all. It points into the volume so that replacing the container does not change the certificate underneath your agents.")
	w.set("ZOOMIES_DB_PATH", s.DBPath,
		"The SQLite database: pools, runners, job history, the audit log and the sealed GitHub credentials. It lives in the named volume.")
	w.set("ZOOMIES_LOG_FORMAT", s.LogFormat,
		"json or text. json is what a log shipper wants; text is what a person reading `docker logs` wants.")
	w.set("ZOOMIES_LOG_LEVEL", s.LogLevel,
		"debug, info, warn or error. debug logs repository and workflow names in full; secrets are redacted at every level.")

	w.section("Read by " + composeOrDocker(s.Deployment) + ", not by Zoomies")
	w.set("ZOOMIES_IMAGE", s.Image,
		"The image to run. Pin a release tag instead of latest if you would rather choose when to upgrade.")
	w.set("ZOOMIES_PUBLISHED_ADDR", s.PublishAddr,
		"The address on this host the container is published on. 127.0.0.1 means only this machine and whatever proxies from it can reach Zoomies.")
	w.set("ZOOMIES_PUBLISHED_PORT", strconv.Itoa(s.PublishedPort),
		"The port on this host that maps to the container's listener.")
	w.set("DOCKER_GID", dockerGIDValue(s.DockerGID), dockerGIDComment(s.DockerGID))

	return w.result()
}

// composeOrDocker names whichever tool reads the deployment-only variables, so
// the section heading is true for the file it is actually in.
func composeOrDocker(d Deployment) string {
	if d == DeploymentDocker {
		return "docker run"
	}
	return "docker compose"
}

func tlsModeComment(mode config.TLSMode) string {
	switch mode {
	case config.TLSFiles:
		return "off, self-signed or files. This container terminates TLS itself with the certificate below."
	case config.TLSSelfSigned:
		return "off, self-signed or files. A generated certificate: browsers will warn, and GitHub will refuse to deliver webhooks to it."
	default:
		return "off, self-signed or files. \"off\" is right when something in front of this host terminates TLS; Zoomies warns about the plaintext listener at startup, and that warning is expected here."
	}
}

func dockerGIDValue(gid int) string {
	if gid <= 0 {
		return ""
	}
	return strconv.Itoa(gid)
}

func dockerGIDComment(gid int) string {
	if gid <= 0 {
		return "The gid that owns the container socket. It could not be read on this host, so the container is given no extra group; if the embedded agent cannot reach the socket, set this to the gid that owns it (ls -l on the socket says which)."
	}
	return "The gid that owns the container socket, read from the socket itself on this host -- which is not always the group called docker. The image runs as uid 65532, which must be in this group to use the socket."
}

// envWriter builds the file. It collects the first error rather than returning
// one per line, so that a refusal names the variable that caused it and
// nothing is written at all.
type envWriter struct {
	b   strings.Builder
	err error
	// keys guards against a variable being written twice, which would leave
	// the second value silently winning.
	keys map[string]bool
}

func (w *envWriter) header(s EnvSpec) {
	fmt.Fprintf(&w.b, `# Zoomies -- environment for the %s deployment.
#
# Written by `+"`zoomies init`"+`. Every value here was answered during setup:
# there is nothing left to fill in.
#
# This file is mode 0600 because ZOOMIES_ENCRYPTION_KEY below is what every
# stored secret is sealed with. Do not commit it, and back that key up
# somewhere that is not the same backup as the database.
#
# Any of these can also be set in the environment, which wins over this file.
`, s.Deployment)
}

func (w *envWriter) section(title string) {
	fmt.Fprintf(&w.b, "\n# --- %s %s\n", title, strings.Repeat("-", max(3, 68-len(title))))
}

// set writes one variable with the comment that says what it is for. A
// variable without a comment is a bug, not a style choice, so it is refused.
func (w *envWriter) set(key, value, comment string) {
	if w.err != nil {
		return
	}
	if strings.TrimSpace(comment) == "" {
		w.err = fmt.Errorf("installer: %s would be written without a comment saying what it is for", key)
		return
	}
	if w.keys == nil {
		w.keys = map[string]bool{}
	}
	if w.keys[key] {
		w.err = fmt.Errorf("installer: %s would be written twice, and the second value would silently win", key)
		return
	}
	w.keys[key] = true

	quoted, err := quoteEnvValue(key, value)
	if err != nil {
		w.err = err
		return
	}
	w.b.WriteString("\n")
	for _, line := range wrapComment(comment, 74) {
		w.b.WriteString("# " + line + "\n")
	}
	w.b.WriteString(key + "=" + quoted + "\n")
}

func (w *envWriter) result() (string, error) {
	if w.err != nil {
		return "", w.err
	}
	return w.b.String(), nil
}

// quoteEnvValue renders a value so that both `docker compose` and
// `docker run --env-file` read back exactly what was meant.
//
// Quoting is kept to the minimum on purpose. Compose strips quotes; docker
// run's --env-file does not, and would hand the container a value with the
// quote marks still on it. None of the values Zoomies writes -- URLs, paths,
// numbers, a base64 key, a comma-separated CIDR list -- needs quoting, so a
// value that does is unusual enough to be worth seeing quoted in the file.
//
// A newline is refused outright rather than written: an environment file is
// line-oriented, so a value containing one would end the assignment early and
// silently drop every variable after it.
func quoteEnvValue(key, value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("installer: the value for %s contains a line break, which would truncate the environment file "+
			"and leave every variable after it unset; remove the line break", key)
	}
	if value == "" {
		return "", nil
	}
	if !strings.ContainsAny(value, " \t\"'$#\\`") && value == strings.TrimSpace(value) {
		return value, nil
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(value) + `"`, nil
}

// wrapComment breaks a comment onto lines that fit an eighty-column terminal,
// because a comment that needs horizontal scrolling is one nobody reads.
func wrapComment(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}

// ---------------------------------------------------------------------------
// Reading and writing
// ---------------------------------------------------------------------------

// ParseEnvFile reads an environment file back into a map.
//
// It exists for one job above all: finding the encryption key an earlier run
// wrote, so that a re-run keeps it. It is deliberately forgiving about
// everything else, because the file it reads may well have been edited by
// hand since it was generated.
func ParseEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = unquoteEnvValue(strings.TrimSpace(value))
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("installer: reading %s: %w", path, err)
	}
	return out, nil
}

// unquoteEnvValue undoes quoteEnvValue, and leaves anything it does not
// recognise exactly as it found it.
func unquoteEnvValue(v string) string {
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		return strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\$`, "$", "\\`", "`").Replace(inner)
	}
	return v
}

// EnvResult reports what WriteEnv did, so the installer can tell the operator
// rather than making them work it out from the directory listing.
type EnvResult struct {
	Path string
	// Backup is where a previous file was copied to, empty when there was
	// none.
	Backup string
	// ReusedKey means an existing encryption key was kept, which is what makes
	// a re-run an upgrade instead of a reinstall that cannot read its own
	// database.
	ReusedKey bool
}

// WriteEnv writes the environment file for a spec, and is safe to run again
// over an installation that already exists.
//
// Three things make it safe. An existing ZOOMIES_ENCRYPTION_KEY is read back
// and kept: minting a new key on an upgrade would leave every stored secret
// undecryptable, which is the worst thing this installer could do to somebody.
// The previous file is copied to .env.bak.<timestamp> before anything is
// overwritten, so an operator's hand edits are recoverable. And the write
// itself goes through a temporary file and a rename, so an interrupted install
// never leaves a half-written file that compose would then read.
func WriteEnv(path string, spec EnvSpec) (EnvResult, error) {
	res := EnvResult{Path: path}

	prior, err := ParseEnvFile(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		prior = nil
	default:
		// An unreadable existing file must stop the run. Carrying on would
		// generate a fresh key and orphan the database this one belongs to.
		return res, fmt.Errorf("installer: %s exists but cannot be read, and overwriting it could orphan the database it belongs to: %w", path, err)
	}

	if existing := strings.TrimSpace(prior["ZOOMIES_ENCRYPTION_KEY"]); existing != "" {
		spec.EncryptionKey = existing
		res.ReusedKey = true
	}
	if spec.EncryptionKey == "" {
		key, err := cryptox.GenerateKey()
		if err != nil {
			return res, fmt.Errorf("installer: generating an encryption key: %w", err)
		}
		spec.EncryptionKey = key.Encode()
	}

	// Render before touching anything: a value this file cannot represent must
	// stop the run while the old file is still exactly as it was.
	body, err := RenderEnv(spec)
	if err != nil {
		return res, err
	}

	if prior != nil {
		backup := path + ".bak." + time.Now().UTC().Format("20060102-150405")
		if err := copyFile(path, backup, 0o600); err != nil {
			return res, fmt.Errorf("installer: keeping a copy of the previous %s: %w", filepath.Base(path), err)
		}
		res.Backup = backup
	}
	// 0600: this file holds the key every stored secret is sealed with.
	if err := writeFileAtomic(path, []byte(body), 0o600); err != nil {
		return res, fmt.Errorf("installer: writing %s: %w", path, err)
	}
	return res, nil
}

// copyFile duplicates a file rather than renaming it, so that at no point does
// the deployment have no environment file at all.
func copyFile(src, dst string, mode os.FileMode) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return writeFileAtomic(dst, b, mode)
}
