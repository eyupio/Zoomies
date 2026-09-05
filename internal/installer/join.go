package installer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// JoinOptions configures `zoomies agent join`.
//
// A join is short: a token, a controller, and a host that can start
// containers. Everything else is detected, because the operator running this
// has usually just pasted a one-line command out of the UI.
type JoinOptions struct {
	ControllerURL string
	JoinToken     string
	// Name is how this host appears in the UI. It defaults to the hostname,
	// which is what an operator will recognise on the Hosts page.
	Name     string
	Capacity int
	Labels   map[string]string
	// Backend forces one; empty picks the best one this host has.
	Backend    store.BackendKind
	DockerHost string
	// CAFile pins the controller's certificate, which is how a private
	// deployment is verified without buying a public name.
	CAFile             string
	ClientCertFile     string
	ClientKeyFile      string
	InsecureSkipVerify bool

	ConfigDir  string
	StateDir   string
	BinaryPath string
	// ServiceUser is the account the agent service runs as.
	ServiceUser string
	// Service selects the supervisor; empty detects one.
	Service ServiceKind

	Answers        *Answers
	NonInteractive bool
	// AssumeYes accepts the one confirmation a join asks for: replacing
	// credentials this host already has.
	AssumeYes bool
	Out       io.Writer
	In        io.Reader
	Logger    *slog.Logger

	// detection lets `zoomies init` hand over what it already found instead of
	// probing the host twice.
	detection *Detection
}

func (o JoinOptions) configDir() string {
	if o.ConfigDir != "" {
		return o.ConfigDir
	}
	return config.ConfigDir()
}

func (o JoinOptions) stateDir() string {
	if o.StateDir != "" {
		return o.StateDir
	}
	return config.StateDir()
}

// Join enrols this host with a controller: it probes the local backends,
// redeems the join token, writes the credentials and a minimal agent config,
// installs the service and confirms the controller can see the host.
//
// Every failure here has a specific cause and a specific remedy, so the errors
// name both rather than reporting "join failed".
func Join(ctx context.Context, opts JoinOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	log := opts.Logger.With("component", "installer.join")
	u := newUI(opts.Out)

	var det Detection
	if opts.detection != nil {
		det = *opts.detection
	} else {
		det = Detect(ctx, Options{ConfigDir: opts.ConfigDir, StateDir: opts.StateDir, InstalledBinary: opts.BinaryPath, NonInteractive: opts.NonInteractive})
		u.step("Checking this host")
		for _, f := range det.Fields() {
			u.field(f.Key, f.Value)
		}
		u.blank()
	}

	if opts.ControllerURL == "" {
		return errors.New("installer: no controller URL; run `zoomies agent join https://zoomies.example.com --token zoojoin_...`, " +
			"copying the line the UI shows under Hosts -> Add a host")
	}
	if opts.JoinToken == "" {
		return errors.New("installer: no join token; mint one in the UI under Hosts -> Add a host, or with " +
			"`zoomies hosts join-token create --ttl 15m`, and pass it as --token")
	}

	workDir := filepath.Join(opts.stateDir(), "work")
	configFile := filepath.Join(opts.configDir(), "zoomies.yaml")
	name := opts.Name
	if name == "" {
		name = det.Hostname
	}
	capacity := opts.Capacity
	if capacity <= 0 {
		capacity = defaultCapacity()
	}
	serviceUser, serviceGroup := opts.ServiceUser, opts.ServiceUser
	if serviceUser == "" {
		serviceUser, serviceGroup = defaultServiceUser(det)
	}

	// --- Directories and the service account ---------------------------
	u.step("Service user and directories")
	if det.Root && det.OS != "darwin" {
		created, err := ensureServiceUser(ctx, serviceUser, serviceGroup, opts.stateDir())
		if err != nil {
			return err
		}
		if created {
			u.ok("created the system user " + serviceUser)
		}
	}
	for _, dir := range []string{opts.configDir(), opts.stateDir(), workDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("installer: creating %s: %w", dir, err)
		}
	}
	u.ok(opts.configDir() + " and " + opts.stateDir() + " are ready")

	// --- An identity this host already has -------------------------------
	if err := confirmRejoin(opts, u, workDir); err != nil {
		return err
	}

	// --- Backends -------------------------------------------------------
	u.step("Backends")
	registry, chosen, err := buildRegistry(ctx, det, opts, workDir)
	if err != nil {
		return err
	}
	for _, info := range registry.Probe(ctx) {
		if info.Available {
			u.ok(fmt.Sprintf("%s available%s", info.Kind, socketSuffix(info.Endpoint)))
		} else {
			u.note(fmt.Sprintf("%s unavailable: %s", info.Kind, firstLine(info.Detail)))
		}
	}
	u.ok("this host will run jobs with the " + string(chosen) + " backend")

	// --- Join ------------------------------------------------------------
	u.step("Joining " + opts.ControllerURL)
	transport, err := agent.NewHTTPTransport(agent.HTTPOptions{
		ControllerURL:      opts.ControllerURL,
		CAFile:             opts.CAFile,
		ClientCertFile:     opts.ClientCertFile,
		ClientKeyFile:      opts.ClientKeyFile,
		InsecureSkipVerify: opts.InsecureSkipVerify,
		Logger:             log,
	})
	if err != nil {
		return err
	}

	a, err := agent.New(agent.Options{
		Name:           name,
		WorkDir:        workDir,
		Capacity:       capacity,
		Labels:         opts.Labels,
		Backends:       registry,
		DefaultBackend: chosen,
		Transport:      transport,
		Logger:         log,
	})
	if err != nil {
		return err
	}
	if err := a.Join(ctx, opts.JoinToken); err != nil {
		return explainJoinError(err, opts)
	}
	u.ok("joined as host " + a.HostID())

	// --- Confirm the controller sees us ----------------------------------
	// The join response alone proves the token was good. A heartbeat proves the
	// long-lived agent token works too, which is the credential every later
	// call depends on and the one an operator cannot check by hand.
	beatCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := transport.Heartbeat(beatCtx, agent.HeartbeatRequest{
		ProtocolVersion: agent.ProtocolVersion,
		Capacity:        capacity,
		Version:         version.Version,
		Backends:        registry.Probe(beatCtx),
	}); err != nil {
		u.warn("the controller accepted the join but rejected the first heartbeat: " + err.Error())
	} else {
		u.ok("the controller sees this host as " + name + " with capacity " + fmt.Sprint(capacity))
	}

	// --- Config ----------------------------------------------------------
	u.step("Configuration")
	cfg := config.Default()
	cfg.Agent.Embedded = false
	cfg.Agent.Name = name
	cfg.Agent.Capacity = capacity
	cfg.Agent.Backend = string(chosen)
	cfg.Agent.DockerHost = opts.DockerHost
	cfg.Agent.WorkDir = workDir
	cfg.Agent.ControllerURL = opts.ControllerURL
	cfg.Agent.CAFile = opts.CAFile
	cfg.Agent.ClientCertFile = opts.ClientCertFile
	cfg.Agent.ClientKeyFile = opts.ClientKeyFile
	cfg.Agent.InsecureSkipVerify = opts.InsecureSkipVerify
	cfg.Agent.Labels = opts.Labels
	cfg.Database.Path = filepath.Join(opts.stateDir(), "agent.db")
	// The agent token lives in the credentials file the join just wrote, with
	// mode 0600, and never in the config file: zoomies.yaml is 0640 and ends up
	// in backups and configuration management.
	for _, f := range cfg.Validate().Warnings() {
		if f.Setting == "server.external_url" {
			continue // an agent has no listener, so this warning is noise here
		}
		u.warn(f.Title)
	}
	if err := cfg.Save(configFile); err != nil {
		return fmt.Errorf("installer: writing %s: %w", configFile, err)
	}
	u.ok("wrote " + configFile)
	u.note("credentials are in " + agent.StatePath(workDir) + " (mode 0600)")

	if det.Root && det.OS != "darwin" {
		if uid, gid, err := lookupUser(serviceUser, serviceGroup); err == nil {
			for _, p := range []string{opts.configDir(), opts.stateDir(), workDir, configFile, agent.StatePath(workDir)} {
				_ = os.Chown(p, uid, gid)
			}
		}
	}

	// --- Service ---------------------------------------------------------
	kind := opts.Service
	if kind == "" {
		kind = DetectServiceKind(det)
	}
	u.step("Service")
	if kind != ServiceSystemd && kind != ServiceLaunchd {
		u.note("no service manager here; start the agent yourself with:")
		u.note("  " + det.BinaryPath + " agent --config " + configFile)
		return nil
	}

	mgr, err := NewServiceManager(kind, UnitAgent)
	if err != nil {
		return err
	}
	path, err := mgr.Install(ctx, ServiceSpec{
		Unit:        UnitAgent,
		ExecPath:    det.BinaryPath,
		ConfigFile:  configFile,
		User:        serviceUser,
		Group:       serviceGroup,
		StateDir:    opts.stateDir(),
		ConfigDir:   opts.configDir(),
		WantsDocker: chosen != store.BackendProcess,
		RuntimeName: string(chosen),
	})
	if err != nil {
		return err
	}
	u.ok("installed " + path)
	if err := mgr.Enable(ctx); err != nil {
		return fmt.Errorf("installer: enabling the agent service: %w", err)
	}
	if err := mgr.Start(ctx); err != nil {
		if out, logErr := mgr.Logs(ctx, 20); logErr == nil && out != "" {
			u.blank()
			u.note(out)
		}
		return fmt.Errorf("installer: starting the agent service: %w (see %s)", err, mgr.LogCommand())
	}
	status, _ := mgr.Status(ctx)
	u.ok("started (" + status + ")")

	u.blank()
	u.step("Done")
	u.field("host", name+" ("+a.HostID()+")")
	u.field("logs", mgr.LogCommand())
	u.note("It should be on the Hosts page of " + opts.ControllerURL + " within a heartbeat.")
	return nil
}

// confirmRejoin stops a second join from silently replacing an identity this
// host already has. The old host record does not disappear when it does: it
// stays on the controller as an offline host somebody has to notice and
// remove, which is worth one question.
func confirmRejoin(opts JoinOptions, u *ui, workDir string) error {
	path := agent.StatePath(workDir)
	if !exists(path) || opts.AssumeYes {
		return nil
	}
	creds, err := agent.Load(path)
	if err != nil {
		// Unreadable or incomplete credentials are exactly what a re-join is
		// for, so this is not something to stop over.
		return nil
	}
	u.warn("this host has already joined " + creds.Controller + " as " + creds.HostID)
	u.note("joining again replaces the credentials in " + path + "; the old host record stays on the")
	u.note("controller as an offline host until you remove it there.")
	if opts.NonInteractive {
		return fmt.Errorf("installer: %s already holds credentials for %s; re-run with --yes to replace them, "+
			"or remove the old host on the controller first", path, creds.Controller)
	}
	ok, err := askYesNo(opts.In, opts.Out, "Replace them and join again? [y/N]: ")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("installer: nothing was changed; this host is still joined to " + creds.Controller)
	}
	return nil
}

// buildRegistry probes what this host can run jobs with and picks a default.
// An agent with no working backend is refused here rather than three minutes
// later when the first task arrives.
func buildRegistry(ctx context.Context, det Detection, opts JoinOptions, workDir string) (*backend.Registry, store.BackendKind, error) {
	var backends []backend.Backend

	docker, err := backend.NewDocker(backend.DockerOptions{Host: opts.DockerHost, WorkDir: workDir, Logger: opts.Logger})
	if err == nil {
		backends = append(backends, docker)
	}
	podman, err := backend.NewPodman(backend.DockerOptions{Host: opts.DockerHost, WorkDir: workDir, Logger: opts.Logger})
	if err == nil {
		backends = append(backends, podman)
	}
	proc, err := backend.NewProcess(backend.ProcessOptions{WorkDir: workDir, Logger: opts.Logger})
	if err == nil {
		backends = append(backends, proc)
	}
	registry := backend.NewRegistry(backends...)

	if opts.Backend != "" {
		b, err := registry.Get(opts.Backend)
		if err != nil {
			return nil, "", fmt.Errorf("installer: backend %q is not available on this host: %w", opts.Backend, err)
		}
		if info := b.Probe(ctx); !info.Available {
			return nil, "", fmt.Errorf("installer: the %s backend was requested but is not usable here: %s", opts.Backend, info.Detail)
		}
		return registry, opts.Backend, nil
	}

	for _, c := range backendChoices(det) {
		if c.Available {
			if _, err := registry.Get(c.Kind); err == nil {
				return registry, c.Kind, nil
			}
		}
	}
	return nil, "", errors.New("installer: this host has no usable backend: no Docker or Podman socket answered, and the process backend could not be prepared. " +
		"Start a runtime (systemctl --user start docker, or systemctl --user enable --now podman.socket) and run the join again")
}

// explainJoinError turns a transport failure into an instruction. The five
// things that go wrong here look almost identical on the wire and have
// completely different remedies, which is the whole reason this exists.
func explainJoinError(err error, opts JoinOptions) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, agent.ErrUnauthorized):
		return fmt.Errorf("installer: %s rejected this join token: it has expired, or it has already been used "+
			"(join tokens are single-use and short-lived by design). Mint another with `zoomies hosts join-token create --ttl 15m` "+
			"and run this again: %w", opts.ControllerURL, err)
	case errors.Is(err, agent.ErrHostGone):
		return fmt.Errorf("installer: %s no longer has a record of this host; mint a fresh join token and try again: %w", opts.ControllerURL, err)
	}

	var certErr *tls.CertificateVerificationError
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	switch {
	case errors.As(err, &certErr), errors.As(err, &unknownAuthority):
		return fmt.Errorf("installer: could not verify %s's TLS certificate. If the controller serves a self-signed or "+
			"private certificate, pin it with --ca-file /path/to/ca.pem (copy it from the controller). Do not disable "+
			"verification: anything on the network path could then impersonate the controller and hand this agent "+
			"containers to run: %w", opts.ControllerURL, err)
	case errors.As(err, &hostnameErr):
		return fmt.Errorf("installer: %s presented a certificate for a different name (%s). Join using the name on the "+
			"certificate, or reissue it for this one: %w", opts.ControllerURL, hostnameErr.Host, err)
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Errorf("installer: %s does not resolve from this host (%s). Check the name, or use the controller's "+
			"IP address: %w", opts.ControllerURL, dnsErr.Name, err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) || errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("installer: could not reach %s from this host. The agent only ever connects outbound, so check "+
			"that the controller is running, that its external URL is right, and that a firewall between here and there "+
			"allows the connection: %w", opts.ControllerURL, err)
	}

	var httpErr *agent.HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
		return fmt.Errorf("installer: %s answered, but has no join endpoint. That URL may be a reverse proxy pointing "+
			"somewhere else, or the controller may be older than this agent: %w", opts.ControllerURL, err)
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && errors.Is(urlErr.Err, context.DeadlineExceeded) {
		return fmt.Errorf("installer: %s did not answer in time; check that it is running and reachable from this host: %w", opts.ControllerURL, err)
	}
	return err
}

// insecureLoopbackTransport is used for one thing only: the installer's own
// health check against 127.0.0.1, where the certificate may be the self-signed
// one generated a moment ago and there is nothing on the loopback interface to
// impersonate it.
func insecureLoopbackTransport() *http.Transport {
	return &http.Transport{
		TLSClientConfig: &tls.Config{
			// #nosec G402 -- loopback only; see the comment above.
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
				return nil, fmt.Errorf("installer: refusing to skip certificate verification for %s; this transport is for loopback health checks only", addr)
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
		},
	}
}
