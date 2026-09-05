package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eyupio/zoomies/internal/agent"
	"github.com/eyupio/zoomies/internal/config"
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
	return runAgentDaemon(ctx, e, args)
}

// runAgentDaemon runs the agent that materialises runners on this host.
func runAgentDaemon(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies agent [--config path]",
		"Run this host's agent: long-poll a controller for work, start and stop runners, report what happens.")
	cfgPath := fs.String("config", "", "path to zoomies.yaml (default: "+config.DefaultConfigFile()+")")
	controllerURL := fs.String("controller", "", "the controller to talk to; overrides agent.controller_url")
	joinToken := fs.String("join-token", "", "a join token, used only when this host has no credentials yet")
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

	transport, err := agent.NewHTTPTransport(agent.HTTPOptions{
		ControllerURL:      cfg.Agent.ControllerURL,
		CAFile:             cfg.Agent.CAFile,
		ClientCertFile:     cfg.Agent.ClientCertFile,
		ClientKeyFile:      cfg.Agent.ClientKeyFile,
		InsecureSkipVerify: cfg.Agent.InsecureSkipVerify,
		Logger:             log,
	})
	if err != nil {
		return err
	}

	a, err := agent.New(agent.Options{
		Name:              cfg.Agent.Name,
		WorkDir:           cfg.Agent.WorkDir,
		Capacity:          cfg.Agent.Capacity,
		Labels:            cfg.Agent.Labels,
		Backends:          backends,
		DefaultBackend:    store.BackendKind(cfg.Agent.Backend),
		Transport:         transport,
		HeartbeatInterval: cfg.Agent.HeartbeatInterval,
		FinishedRetention: cfg.Agent.FinishedRetention,
		Logger:            log,
	})
	if err != nil {
		return err
	}

	// Credentials come from the state file a previous join wrote. Joining here
	// is the fallback for a host handed a token by configuration management,
	// which is why it only happens when there is nothing to restore.
	statePath := agent.StatePath(cfg.Agent.WorkDir)
	if _, err := agent.Load(statePath); err != nil {
		if !errors.Is(err, agent.ErrNotJoined) {
			return err
		}
		token := strings.TrimSpace(cfg.Agent.JoinToken)
		if token == "" {
			return fmt.Errorf("this host has no agent credentials at %s and no join token to get some: "+
				"run `zoomies agent join %s --token <join-token>`, minting the token in the UI under Hosts -> Add a host, "+
				"or set ZOOMIES_JOIN_TOKEN and start again", statePath, cfg.Agent.ControllerURL)
		}
		if err := a.Join(ctx, token); err != nil {
			return err
		}
	}

	fmt.Fprintf(e.out, "zoomies agent %q -> %s\n", cfg.Agent.Name, cfg.Agent.ControllerURL)
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

// serviceChoice turns --no-service into the installer's explicit "none", and
// leaves the empty string otherwise so that the supervisor is detected.
func serviceChoice(none bool) installer.ServiceKind {
	if none {
		return installer.ServiceNone
	}
	return ""
}
