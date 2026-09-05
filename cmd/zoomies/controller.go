package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eyupio/zoomies/internal/api"
	"github.com/eyupio/zoomies/internal/backend"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/controller"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/logging"
	"github.com/eyupio/zoomies/internal/store"
	"github.com/eyupio/zoomies/internal/version"
)

// stopGrace bounds how long the controller's loops get to finish once the
// listener has stopped. It is deliberately generous: nothing here kills a
// runner, so the only cost of waiting is a slightly slower restart, and the
// benefit is a final fleet sample and a tidy log.
const stopGrace = 20 * time.Second

// runController is `zoomies controller`: the control plane, the API, the UI,
// the webhook endpoint and -- unless agent.embedded is false -- an agent.
func runController(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies controller [--config path]",
		"Run the control plane: the scheduler, the API, the web UI and the webhook endpoint.")
	cfgPath := fs.String("config", "", "path to zoomies.yaml (default: "+config.DefaultConfigFile()+", and a missing file is not an error)")
	fs.example(
		"zoomies controller --config /etc/zoomies/zoomies.yaml",
		"ZOOMIES_BIND=0.0.0.0:8080 zoomies controller     # no file at all: defaults plus environment",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	findings := cfg.Validate()
	printFindings(e.err, findings)
	if err := findings.Err(); err != nil {
		return err
	}

	log, level := setupLogging(cfg)

	st, err := store.Open(ctx, store.Options{Path: cfg.Database.Path})
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := loadOrCreateKey(cfg, log)
	if err != nil {
		return err
	}

	// The registry is built only for an embedded agent. A controller that runs
	// no runners itself has no business probing this host's Docker socket, and
	// building one anyway would produce backend warnings about a host that is
	// never going to start a container.
	var backends *backend.Registry
	if cfg.Agent.Embedded {
		backends, err = buildBackends(ctx, cfg, log)
		if err != nil {
			return err
		}
	}

	ctrl, err := controller.New(controller.Options{
		Store:    st,
		Config:   cfg,
		Key:      key,
		Backends: backends,
		Logger:   log,
	})
	if err != nil {
		return err
	}

	srv, err := api.New(api.Options{Controller: ctrl, Logger: log})
	if err != nil {
		return err
	}

	// SIGHUP re-reads the log level and nothing else. Everything else in the
	// configuration is either safe to change through PATCH /settings or needs
	// the process restarted, and pretending otherwise -- rebinding a listener
	// under live connections, say -- is how a reload becomes an outage.
	stopHUP := watchSIGHUP(ctx, *cfgPath, level, log)
	defer stopHUP()

	if err := ctrl.Start(ctx); err != nil {
		return err
	}
	// Stop runs on every path out, including a failed listen. It shuts the
	// loops down without touching a single runner: restarting a controller
	// must never kill somebody's build.
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stopGrace)
		defer cancel()
		if err := ctrl.Stop(shutdownCtx); err != nil {
			log.Warn("the controller did not shut down cleanly", "error", err)
		}
	}()

	if cfg.Agent.Embedded {
		if err := ctrl.StartEmbeddedAgent(ctx, cfg); err != nil {
			return fmt.Errorf("starting the embedded agent: %w (set agent.embedded to false to run a controller that hosts no runners)", err)
		}
	}

	printBanner(e.out, cfg, backends)
	return srv.ListenAndServe(ctx)
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

// setupLogging installs the process logger and returns the level it is filtered
// at, so that SIGHUP can move it without rebuilding every logger the controller
// and the API have already captured.
func setupLogging(cfg *config.Config) (*slog.Logger, *slog.LevelVar) {
	level := new(slog.LevelVar)
	level.Set(parseLevel(cfg.Log.Level))

	// The inner handler is built at debug so that it never filters anything
	// out itself; the wrapper below is the only gate, and it is the one whose
	// mind can be changed at runtime.
	inner := logging.Setup(logging.Options{Level: "debug", Format: cfg.Log.Format}).Handler()
	log := slog.New(&levelHandler{Handler: inner, level: level})
	slog.SetDefault(log)
	return log, level
}

// levelHandler gates a handler on a level that can change while the process
// runs. slog.HandlerOptions.Level accepts a Leveler for exactly this, but the
// handler underneath is built by internal/logging, which owns the redaction
// rules; wrapping is how both stay true.
type levelHandler struct {
	slog.Handler
	level *slog.LevelVar
}

func (h *levelHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level.Level() }

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithAttrs(attrs), level: h.level}
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return &levelHandler{Handler: h.Handler.WithGroup(name), level: h.level}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// watchSIGHUP re-reads the configuration on SIGHUP and applies the log level
// from it. It returns a function that stops watching.
func watchSIGHUP(ctx context.Context, cfgPath string, level *slog.LevelVar, log *slog.Logger) func() {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)

	var once sync.Once
	stop := func() { once.Do(func() { signal.Stop(hup); close(hup) }) }

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-hup:
				if !ok {
					return
				}
				fresh, err := config.Load(cfgPath)
				if err != nil {
					log.Error("SIGHUP: the configuration did not re-read, so nothing changed",
						"error", err, "fix", "correct the file and send SIGHUP again")
					continue
				}
				was := level.Level()
				now := parseLevel(fresh.Log.Level)
				level.Set(now)
				if was == now {
					log.Info("SIGHUP: log level re-read and unchanged", "log_level", now.String())
				} else {
					// Logged at the new level's own severity would risk the
					// message being filtered out; Info is always visible at
					// debug and info, and Warn covers the rest.
					log.Warn("SIGHUP: log level changed", "from", was.String(), "to", now.String())
				}
			}
		}
	}()
	return stop
}

// ---------------------------------------------------------------------------
// Keys and backends
// ---------------------------------------------------------------------------

// loadOrCreateKey resolves the instance encryption key, generating one the
// first time.
//
// Generating rather than refusing is deliberate: a first run should work. What
// must not happen is a key appearing silently, so the path it went to and the
// fact that it is the only copy are logged at warning level, once.
func loadOrCreateKey(cfg *config.Config, log *slog.Logger) (*cryptox.Key, error) {
	if raw := strings.TrimSpace(cfg.Security.EncryptionKey); raw != "" {
		key, err := cryptox.ParseKey(raw)
		if err != nil {
			return nil, fmt.Errorf("security.encryption_key (or ZOOMIES_ENCRYPTION_KEY) is not usable: %w", err)
		}
		return key, nil
	}

	path := strings.TrimSpace(cfg.Security.EncryptionKeyFile)
	if path == "" {
		return nil, errors.New("no encryption key: GitHub App private keys and webhook secrets are sealed with one, " +
			"so set security.encryption_key_file to a path this process may write, or pass the key itself in ZOOMIES_ENCRYPTION_KEY")
	}

	key, err := cryptox.LoadKeyFile(path)
	switch {
	case err == nil:
		return key, nil
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}

	key, err = cryptox.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generating an encryption key: %w", err)
	}
	if err := cryptox.WriteKeyFile(path, key); err != nil {
		return nil, fmt.Errorf("writing the new encryption key to %s: %w (set security.encryption_key_file to a path this process may write)", path, err)
	}
	log.Warn("generated a new encryption key",
		"path", path,
		"detail", "it is the only copy, and without it the stored GitHub App private keys and webhook secrets cannot be decrypted",
		"fix", "back up "+path+" now, alongside your database")
	return key, nil
}

// socketExists reports whether a unix socket path is present. A TCP endpoint
// has no path and is taken on trust.
func socketExists(path string) bool {
	if path == "" {
		return true
	}
	_, err := os.Stat(path)
	return err == nil
}

// buildBackends prepares the runner backends this host can use.
//
// Construction does not contact a daemon -- that is Probe's job -- so a host
// whose Docker is stopped still starts, reports the backend as unavailable and
// says why, instead of refusing to boot.
func buildBackends(ctx context.Context, cfg *config.Config, log *slog.Logger) (*backend.Registry, error) {
	var backends []backend.Backend
	opts := backend.DockerOptions{
		Network: cfg.Agent.Network,
		WorkDir: cfg.Agent.WorkDir,
		Logger:  log,
	}
	// An explicit agent.docker_host belongs to the backend it was configured
	// for; the other container backend keeps autodetecting its own socket.
	// Handing one socket to both made the Hosts page report the same denial
	// twice -- once as docker, once as "podman" at /var/run/docker.sock -- and,
	// once the socket was reachable, made a Docker host claim it offered Podman
	// too, since the two speak the same API.
	hostFor := func(kind store.BackendKind) string {
		if cfg.Agent.Backend == "" || cfg.Agent.Backend == string(kind) {
			return cfg.Agent.DockerHost
		}
		return ""
	}
	// A backend nobody configured is registered only when the host visibly
	// has it: a socket that exists, a shell to run the runner with. The
	// configured one is registered regardless, so that a daemon which is not
	// up yet is still re-probed and reported on. Registering the rest anyway
	// put a red row with install advice on every Docker host's Hosts page --
	// "install Podman", "apt-get install libicu" into a distroless image --
	// when the honest answer is that the host does not offer them.
	configured := func(kind store.BackendKind) bool {
		return cfg.Agent.Backend == "" || cfg.Agent.Backend == string(kind)
	}
	docker := opts
	docker.Host = hostFor(store.BackendDocker)
	if b, err := backend.NewDocker(docker); err != nil {
		log.Debug("the Docker backend is not available on this host", "error", err)
	} else if configured(b.Kind()) || socketExists(b.SocketPath()) {
		backends = append(backends, b)
	}
	podman := opts
	podman.Host = hostFor(store.BackendPodman)
	if b, err := backend.NewPodman(podman); err != nil {
		log.Debug("the Podman backend is not available on this host", "error", err)
	} else if configured(b.Kind()) || socketExists(b.SocketPath()) {
		backends = append(backends, b)
	}
	if b, err := backend.NewProcess(backend.ProcessOptions{
		WorkDir:                 cfg.Agent.WorkDir,
		RunnerVersion:           cfg.GitHub.RunnerVersion,
		RunnerSHA256:            cfg.Agent.RunnerSHA256,
		AllowUnverifiedDownload: cfg.Agent.AllowUnverifiedRunnerDownload,
		DownloadBaseURL:         cfg.Agent.RunnerDownloadURL,
		Logger:                  log,
	}); err != nil {
		log.Debug("the process backend could not be prepared", "error", err)
	} else if configured(b.Kind()) || backend.HasShell() {
		backends = append(backends, b)
	}

	reg := backend.NewRegistry(backends...)
	if len(reg.Kinds()) == 0 {
		return nil, errors.New("no runner backend could be prepared on this host, so the embedded agent would have no way to start a runner: " +
			"install Docker or Podman, make sure agent.work_dir is writable, or set agent.embedded to false")
	}
	if kind := strings.TrimSpace(cfg.Agent.Backend); kind != "" {
		if _, err := reg.Get(store.BackendKind(kind)); err != nil {
			return nil, fmt.Errorf("agent.backend is %q, which this host cannot provide: %w", kind, err)
		}
	}
	// Probing here rather than at first use means the startup log says what is
	// actually usable, which is the difference between "why is nothing
	// scheduling" and one line naming the stopped daemon.
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for _, info := range reg.Probe(probeCtx) {
		if info.Available {
			log.Info("backend available", "kind", info.Kind, "version", info.Version, "rootless", info.Rootless, "endpoint", info.Endpoint)
		} else {
			log.Warn("backend not available", "kind", info.Kind, "detail", info.Detail)
		}
	}
	return reg, nil
}

// ---------------------------------------------------------------------------
// Startup banner
// ---------------------------------------------------------------------------

// printBanner prints the six facts an operator checks when a controller comes
// up, in one block, once. Everything else is a log line.
func printBanner(w io.Writer, cfg *config.Config, backends *backend.Registry) {
	external := cfg.Server.ExternalURL
	if external == "" {
		external = "(not set -- webhook URLs and the UI's links need server.external_url)"
	}
	webhook := cfg.WebhookURL()
	if webhook == "" {
		webhook = "(needs server.external_url) " + cfg.GitHub.WebhookPath
	}

	runners := "none -- this controller hosts no runners (agent.embedded is false)"
	if cfg.Agent.Embedded {
		kind := cfg.Agent.Backend
		if kind == "" && backends != nil {
			if kinds := backends.Kinds(); len(kinds) == 1 {
				kind = string(kinds[0])
			}
		}
		if kind == "" {
			kind = "autodetected"
		}
		runners = fmt.Sprintf("%s, embedded agent %q, capacity %d", kind, cfg.Agent.Name, cfg.Agent.Capacity)
	}

	scheme := "http"
	if cfg.Server.TLS.Mode != config.TLSOff {
		scheme = "https"
	}

	fmt.Fprintf(w, "\nzoomies %s\n", version.Short())
	for _, row := range [][2]string{
		{"listening on", fmt.Sprintf("%s://%s", scheme, cfg.Server.Bind)},
		{"external URL", external},
		{"webhook URL", webhook},
		{"backend", runners},
		{"database", cfg.Database.Path},
		{"config", configSource(cfg)},
	} {
		fmt.Fprintf(w, "  %-14s %s\n", row[0], row[1])
	}
	fmt.Fprintln(w)
}

// configSource names the file the configuration came from, or says there was
// not one -- which is a supported way to run and should not look like a fault.
func configSource(cfg *config.Config) string {
	if p := cfg.Path(); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}
	return "(no file: defaults plus ZOOMIES_* environment)"
}
