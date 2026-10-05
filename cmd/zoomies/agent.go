package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/hosttune"
	"github.com/eyupio/zoomies/internal/installer"
	"github.com/eyupio/zoomies/internal/store"
)

// runAgent is `zoomies agent`: either the daemon, or its `join` subcommand.
//
// The two are one command because they are one idea to the operator -- "this
// host runs jobs" -- and because `join` is almost always followed immediately
// by the daemon starting under a service manager.
func runAgent(ctx context.Context, e *env, args []string) error {
	if len(args) > 0 && args[0] == "join" {
		return runAgentJoin(ctx, e, args[1:])
	}
	if len(args) > 0 && args[0] == "install" {
		return runAgentInstall(ctx, e, args[1:])
	}
	return runAgentDaemon(ctx, e, args)
}

// runAgentDaemon runs the agent that materialises runners on this host.
func runAgentDaemon(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies agent [--config path]",
		"Run this host's agent: long-poll a controller for work, start and stop runners, report what happens.")
	cfgPath := fs.String("config", "", "path to zoomies.yaml (default: "+config.DefaultConfigFile()+")")
	controllerURL := fs.String("controller", "", "the controller to talk to; overrides agent.controller_url")
	joinToken := fs.String("join-token", "", "a join token, used only when this host has no credentials yet")
	logFile := fs.String("log-file", "", "append the log to this file instead of stderr (the Windows service is installed with one, because a service has no stderr anyone can read)")
	fs.example(
		"zoomies agent --config /etc/zoomies/zoomies.yaml",
		"zoomies agent --controller https://zoomies.example.com --join-token zoojoin_...",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	if *logFile != "" {
		if err := redirectOutput(e, *logFile); err != nil {
			return err
		}
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	// Flags win over the file, and are applied before validation so that
	// `--controller` satisfies the agent checks the file alone would fail.
	if *controllerURL != "" {
		cfg.Agent.ControllerURL = strings.TrimRight(*controllerURL, "/")
	}
	if *joinToken != "" {
		cfg.Agent.JoinToken = *joinToken
	}
	if cfg.Agent.ControllerURL == "" {
		return errors.New("no controller to talk to: set agent.controller_url in zoomies.yaml, " +
			"or pass --controller https://zoomies.example.com (ZOOMIES_CONTROLLER_URL works too)")
	}

	findings := cfg.Validate()
	printFindings(e.err, findings)
	if err := findings.Err(); err != nil {
		return err
	}

	log, _ := setupLogging(cfg)

	backends, err := buildBackends(ctx, cfg, log)
	if err != nil {
		return err
	}

	// Credentials come from the state file a previous join wrote. Checking
	// here, before anything is built from them, is what keeps a host with no
	// usable credentials from failing with whatever error an HTTP transport
	// half-built from an empty token happens to produce -- for a private
	// connection specifically, that used to be a low-level "no tunnel
	// address" error with no mention of the state file or what to do about
	// it. One reason, stated once, up front.
	statePath := agent.StatePath(cfg.Agent.WorkDir)
	saved, loadErr := agent.Load(statePath)
	joined := loadErr == nil
	if loadErr != nil && !errors.Is(loadErr, agent.ErrNotJoined) {
		return loadErr
	}
	token := strings.TrimSpace(cfg.Agent.JoinToken)
	if !joined && token == "" {
		reason := "no credentials file yet"
		if loadErr != nil {
			reason = loadErr.Error()
		}
		join := fmt.Sprintf("run `zoomies agent join %s --token <join-token>`, minting the token in the UI under Hosts -> Add a host, "+
			"or set ZOOMIES_JOIN_TOKEN and start again", agent.DisplayController(cfg.Agent.ControllerURL))
		if cfg.Agent.ControllerURL == agent.TailcatController {
			// tailcat://controller is a marker, not a dialable address: no
			// flag or environment variable on this host can supply the
			// private connection's tunnel address, only a fresh enrolment
			// command from the UI can.
			join = "mint a fresh enrolment command in the UI under Hosts -> Add a host -> Private connection and run it on this host"
		}
		return fmt.Errorf("this host cannot start: %s, and no join token to get new ones: %s", reason, join)
	}

	transport, err := agent.NewHTTPTransport(agent.HTTPOptions{
		TailcatAddress:     saved.TailcatAddress,
		ControllerURL:      cfg.Agent.ControllerURL,
		CAFile:             cfg.Agent.CAFile,
		ClientCertFile:     cfg.Agent.ClientCertFile,
		ClientKeyFile:      cfg.Agent.ClientKeyFile,
		InsecureSkipVerify: cfg.Agent.InsecureSkipVerify,
		AllowInsecureHTTP:  cfg.Agent.AllowInsecureHTTP,
		Logger:             log,
	})
	if err != nil {
		return err
	}

	defer transport.Close()

	health := hosttune.LocalOptions(cfg.Agent.WorkDir)
	health.DockerHost = cfg.Agent.DockerHost
	health.HostReportPath = filepath.Join(config.SharedDir(), "host-health", "report.json")
	a, err := agent.New(agent.Options{
		Doctor:             hosttune.NewMonitor(hosttune.New(health)),
		Name:               cfg.Agent.Name,
		WorkDir:            cfg.Agent.WorkDir,
		Capacity:           cfg.Agent.Capacity,
		Labels:             cfg.Agent.Labels,
		Backends:           backends,
		DefaultBackend:     store.BackendKind(cfg.Agent.Backend),
		Transport:          transport,
		HeartbeatInterval:  cfg.Agent.HeartbeatInterval,
		FinishedRetention:  cfg.Agent.FinishedRetention,
		BootstrapCPUGrace:  cfg.Agent.BootstrapCPUGrace,
		PrewarmJitter:      cfg.Agent.PrewarmJitter,
		PrewarmTimeout:     cfg.Agent.PrewarmTimeout,
		DockerBuildCacheMB: cfg.Agent.DockerBuildCacheMB,
		Logger:             log,
	})
	if err != nil {
		return err
	}

	// Joining here is the fallback for a host handed a token by configuration
	// management rather than `zoomies agent join`; it only runs when the state
	// file above had nothing to restore.
	if !joined {
		if err := a.Join(ctx, token); err != nil {
			return err
		}
	}

	fmt.Fprintf(e.out, "zoomies agent %q -> %s\n", cfg.Agent.Name, transport.Describe())
	return a.Run(ctx)
}

// runAgentJoin is `zoomies agent join`: enrol this host with a controller and,
// unless told not to, install the service that keeps it enrolled.
func runAgentJoin(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies agent join <controller-url> --token <join-token>",
		"Enrol this host with a controller: redeem the join token, write the credentials, install the service.")
	token := fs.String("token", "", "the join token from the UI, under Hosts -> Add a host (required)")
	name := fs.String("name", "", "how this host appears in the UI (default: this host's name)")
	capacity := fs.Int("capacity", 0, "the most runners this host will hold at once (default: half its CPUs)")
	labels := kvValue{}
	fs.Var(labels, "labels", "host labels a pool's selector can match, e.g. arch=arm64,tier=fast")
	backendKind := fs.String("backend", "", "force a backend: docker, podman or process (default: the best this host has)")
	dockerHost := fs.String("docker-host", "", "the Docker or Podman socket to use (default: autodetect, rootless first)")
	caFile := fs.String("ca-file", "", "PEM file holding the controller's certificate, for a private deployment")
	clientCert := fs.String("client-cert", "", "client certificate for mutual TLS to the controller")
	clientKey := fs.String("client-key", "", "client key for mutual TLS to the controller")
	insecure := fs.Bool("insecure", false, "do not verify the controller's certificate (prefer --ca-file: this trusts anything on the path)")
	allowHTTP := fs.Bool("allow-insecure-http", false, "allow a plain http:// controller URL that is not on loopback (this agent's token and every runner's registration credentials then cross the network in the clear)")
	noService := fs.Bool("no-service", false, "join only; do not install or start a service")
	serviceUser := fs.String("service-user", "", "the account the agent service runs as")
	configDir := fs.String("config-dir", "", "where to write the agent's configuration (default: "+config.ConfigDir()+")")
	stateDir := fs.String("state-dir", "", "where the agent keeps its credentials and scratch space (default: "+config.StateDir()+")")
	nonInteractive := fs.Bool("non-interactive", false, "never prompt; a missing answer is an error naming it")
	assumeYes := fs.Bool("yes", false, "answer yes to the one question a re-join asks")
	fs.example(
		"zoomies agent join https://zoomies.example.com --token zoojoin_...",
		"zoomies agent join https://zoomies.example.com --token zoojoin_... --capacity 8 --labels arch=arm64",
		"zoomies agent join https://zoomies.internal --token zoojoin_... --ca-file /etc/zoomies/controller.crt",
	)
	if err := fs.parse(args); err != nil {
		return err
	}

	url, err := fs.oneArg("the controller URL, e.g. https://zoomies.example.com")
	if err != nil {
		return err
	}
	if strings.TrimSpace(*token) == "" {
		return usagef("agent join", "needs --token; mint one in the UI under Hosts -> Add a host, which prints the whole command to paste")
	}
	kind := store.BackendKind(strings.ToLower(strings.TrimSpace(*backendKind)))
	if kind != "" && !kind.Valid() {
		return usagef("agent join", "--backend %q is not a backend; use docker, podman or process", *backendKind)
	}

	return installer.Join(ctx, installer.JoinOptions{
		ControllerURL:      strings.TrimRight(url, "/"),
		JoinToken:          *token,
		Name:               *name,
		Capacity:           *capacity,
		Labels:             labels,
		Backend:            kind,
		DockerHost:         *dockerHost,
		CAFile:             *caFile,
		ClientCertFile:     *clientCert,
		ClientKeyFile:      *clientKey,
		InsecureSkipVerify: *insecure,
		AllowInsecureHTTP:  *allowHTTP,
		ConfigDir:          *configDir,
		StateDir:           *stateDir,
		ServiceUser:        *serviceUser,
		Service:            serviceChoice(*noService),
		NonInteractive:     *nonInteractive,
		AssumeYes:          *assumeYes,
		Out:                e.out,
		In:                 e.in,
	})
}

// runAgentInstall is `zoomies agent install`: the agent's service on a machine
// that is about to become a template, joined to nothing. A provider clones the
// template and enrols each clone by writing its environment file and enabling
// the unit this leaves disabled.
func runAgentInstall(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies agent install",
		"Install the agent service without joining anything, on a machine about to become a provider's template. The unit is left disabled; the controller enables it inside each clone.")
	noAnimation := fs.Bool("no-animation", false, "skip the short branded terminal splash")
	serviceUser := fs.String("service-user", "", "the account the agent service runs as")
	configDir := fs.String("config-dir", "", "where to write the agent's configuration (default: "+config.ConfigDir()+")")
	stateDir := fs.String("state-dir", "", "where the agent keeps its credentials and scratch space (default: "+config.StateDir()+")")
	binary := fs.String("installed-binary", "", "the zoomies binary the unit runs (default: this one)")
	fs.example(
		"sudo zoomies agent install",
		"sudo zoomies agent install --service-user runner",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if !*noAnimation {
		if err := installer.Splash(ctx, e.out, "Install"); err != nil {
			return err
		}
	}
	return installer.PrepareAgent(ctx, installer.PrepareOptions{
		ConfigDir:   *configDir,
		StateDir:    *stateDir,
		BinaryPath:  *binary,
		ServiceUser: *serviceUser,
		Out:         e.out,
	})
}

// serviceChoice turns --no-service into the installer's explicit "none", and
// leaves the empty string otherwise so that the supervisor is detected.
func serviceChoice(none bool) installer.ServiceKind {
	if none {
		return installer.ServiceNone
	}
	return ""
}

// redirectOutput sends everything the agent would print or log to a file.
//
// The logger is built from os.Stderr when the agent starts, so the variable
// is replaced rather than the handler wrapped: every later write, including
// the findings printed before the logger exists, lands in the same file. It
// is opened for append so that a restart continues the file rather than
// truncating what an operator was about to read.
func redirectOutput(e *env, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("opening the log file %s: %w", path, err)
	}
	os.Stdout, os.Stderr = f, f
	e.out, e.err = f, f
	return nil
}
