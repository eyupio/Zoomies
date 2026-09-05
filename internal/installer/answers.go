package installer

import (
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Answers is the answer file: one YAML key for every question `zoomies init`
// would otherwise ask.
//
// It exists so that a fleet can be rebuilt by configuration management without
// anyone sitting in front of it. Every field maps to exactly one prompt, and
// the non-interactive path uses the same decision code as the interactive one,
// so an answer file cannot drift away from what the prompts do.
type Answers struct {
	// Mode is single, controller or agent.
	Mode string `yaml:"mode"`
	// Deployment is native, compose or docker: how this host runs Zoomies,
	// which is a separate question from what it runs.
	Deployment string `yaml:"deployment"`
	// DeploymentDir is where a compose or docker deployment writes its
	// docker-compose.yml and its environment file.
	DeploymentDir string `yaml:"deployment_dir"`
	// Image is the container image those deployments run.
	Image string `yaml:"image"`
	// ServiceUser is the unprivileged account the service runs as.
	ServiceUser string `yaml:"service_user"`
	ConfigDir   string `yaml:"config_dir"`
	StateDir    string `yaml:"state_dir"`

	// Backend is docker, podman or process.
	Backend string `yaml:"backend"`
	// DockerHost overrides socket autodetection.
	DockerHost string `yaml:"docker_host"`
	// Capacity is the most runners this host may hold at once.
	Capacity int `yaml:"capacity"`

	Bind           string      `yaml:"bind"`
	TLS            AnswersTLS  `yaml:"tls"`
	TrustedProxies []string    `yaml:"trusted_proxies"`
	ExternalURL    string      `yaml:"external_url"`
	GitHub         AnswersApp  `yaml:"github"`
	Admin          AnswersUser `yaml:"admin"`
	Pool           AnswersPool `yaml:"pool"`
	Service        AnswersSvc  `yaml:"service"`
	Agent          AnswersJoin `yaml:"agent"`

	// path records where this file came from, so errors can name it.
	path string `yaml:"-"`
}

// AnswersTLS mirrors the bind-and-TLS question.
type AnswersTLS struct {
	// Mode is "off", "files" or "self-signed". Quote it: an unquoted off is a
	// boolean in YAML, not a word.
	Mode     string   `yaml:"mode"`
	CertFile string   `yaml:"cert_file"`
	KeyFile  string   `yaml:"key_file"`
	Hosts    []string `yaml:"hosts"`
}

// AnswersApp mirrors the GitHub App questions. A non-interactive run cannot
// drive a browser, so it takes credentials that already exist instead.
type AnswersApp struct {
	APIBaseURL string `yaml:"api_base_url"`
	// Target is an organisation login, or "owner/repo" for a single repository.
	Target string `yaml:"target"`
	// TargetType is org or repo. It is derived from Target when empty.
	TargetType     string `yaml:"target_type"`
	AppID          int64  `yaml:"app_id"`
	InstallationID int64  `yaml:"installation_id"`
	// PrivateKeyFile is the .pem GitHub gave you when the App was created.
	PrivateKeyFile string `yaml:"private_key_file"`
	// WebhookSecret is the secret configured on the App's webhook. Without it
	// Zoomies cannot verify deliveries and will reject every one.
	WebhookSecret string `yaml:"webhook_secret"`
	// WebhookSecretFile keeps the secret out of the answer file, which is the
	// better habit when that file lives in a repository.
	WebhookSecretFile string `yaml:"webhook_secret_file"`
	// Skip records a deliberate decision to connect GitHub later in the UI.
	Skip bool `yaml:"skip"`
}

// AnswersUser is the first administrator.
type AnswersUser struct {
	Username string `yaml:"username"`
	// Password is here for completeness; PasswordFile is the one to use when
	// the answer file is stored anywhere shared.
	Password     string `yaml:"password"`
	PasswordFile string `yaml:"password_file"`
}

// AnswersPool mirrors the first-pool question a single-host install asks.
//
// Every field is optional: an answer file that says nothing about pools gets
// the same pool the prompt would have offered, which is the one this host can
// actually run.
type AnswersPool struct {
	// Skip finishes setup with no pool. The Pools page is then empty and
	// nothing runs until one is created.
	Skip bool `yaml:"skip"`
	// Name overrides the suggested pool name, which is derived from this
	// host's OS, architecture and backend. It is branded on the way in, so a
	// name written here without the "zoomies-" prefix is given one.
	Name string `yaml:"name"`
	// Labels are what a workflow's runs-on has to ask for. Empty means the
	// pool answers to its own name.
	Labels []string `yaml:"labels"`
	// MaxRunners caps how many runners the pool may start at once. It cannot
	// usefully exceed the host's capacity, which is where the default comes
	// from.
	MaxRunners int `yaml:"max_runners"`
}

// AnswersSvc mirrors the service question.
type AnswersSvc struct {
	// Manager is systemd, launchd, compose or none.
	Manager string `yaml:"manager"`
	// Enable and Start are pointers so that "false" can be told apart from
	// "not mentioned", which is what lets a partial answer file work.
	Enable *bool `yaml:"enable"`
	Start  *bool `yaml:"start"`
}

// AnswersJoin mirrors `zoomies agent join`.
type AnswersJoin struct {
	ControllerURL string            `yaml:"controller_url"`
	JoinToken     string            `yaml:"join_token"`
	JoinTokenFile string            `yaml:"join_token_file"`
	Name          string            `yaml:"name"`
	Labels        map[string]string `yaml:"labels"`
	CAFile        string            `yaml:"ca_file"`
}

// Load reads an answer file. Unknown keys are an error, not a shrug: a typo in
// an unattended install would otherwise leave a setting silently at its
// default and be found weeks later.
func Load(path string) (*Answers, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("installer: reading the answer file %s: %w (write one with `zoomies init --print-answers > %s`)", path, err, path)
	}
	var a Answers
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil && err != io.EOF {
		return nil, fmt.Errorf("installer: %s is not a valid answer file: %w", path, err)
	}
	a.path = path
	a.normalize()
	return &a, nil
}

// Path returns the file these answers were read from, for error messages.
func (a *Answers) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

func (a *Answers) normalize() {
	a.Mode = strings.ToLower(strings.TrimSpace(a.Mode))
	a.Deployment = strings.ToLower(strings.TrimSpace(a.Deployment))
	a.Backend = strings.ToLower(strings.TrimSpace(a.Backend))
	a.TLS.Mode = strings.ToLower(strings.TrimSpace(a.TLS.Mode))
	a.Service.Manager = strings.ToLower(strings.TrimSpace(a.Service.Manager))
	a.GitHub.TargetType = strings.ToLower(strings.TrimSpace(a.GitHub.TargetType))
	a.GitHub.Target = strings.TrimSpace(a.GitHub.Target)
	a.ExternalURL = strings.TrimRight(strings.TrimSpace(a.ExternalURL), "/")
	a.Agent.ControllerURL = strings.TrimRight(strings.TrimSpace(a.Agent.ControllerURL), "/")
	a.Admin.Username = strings.TrimSpace(a.Admin.Username)
	// "false" is what a YAML 1.1 parser makes of an unquoted "off", and an
	// operator who wrote that meant the TLS mode.
	if a.TLS.Mode == "false" {
		a.TLS.Mode = "off"
	}
}

// MissingAnswer names one answer a non-interactive run cannot invent, and says
// what it is for. Silently defaulting any of these would produce a controller
// that starts and then does not work.
type MissingAnswer struct {
	Key string
	Why string
}

func (m MissingAnswer) String() string { return m.Key + " -- " + m.Why }

// Missing lists the answers this file still needs for a given mode. A nil
// Answers is treated as an empty one, which is what a run with no answer file
// at all should report.
func (a *Answers) Missing(mode Mode) []MissingAnswer {
	if a == nil {
		a = &Answers{}
	}
	var out []MissingAnswer
	need := func(ok bool, key, why string) {
		if !ok {
			out = append(out, MissingAnswer{Key: key, Why: why})
		}
	}

	if mode == ModeAgent {
		need(a.Agent.ControllerURL != "", "agent.controller_url",
			"the controller this host joins, e.g. https://zoomies.example.com")
		need(a.Agent.JoinToken != "" || a.Agent.JoinTokenFile != "", "agent.join_token",
			"a join token from the UI under Hosts, or `zoomies hosts join-token create`")
		return out
	}

	need(a.ExternalURL != "", "external_url",
		"the URL GitHub and your browser reach this controller on; it forms the webhook URL")
	// A container's database lives in a volume the installer never opens, so
	// its first administrator is created in the browser rather than here.
	if !Deployment(a.Deployment).Containerised() {
		need(a.Admin.Username != "", "admin.username",
			"the first administrator's login name")
		need(a.Admin.Password != "" || a.Admin.PasswordFile != "", "admin.password",
			"the first administrator's password, or admin.password_file to keep it out of this file")
	}

	if !a.GitHub.Skip {
		need(a.GitHub.Target != "", "github.target",
			"the organisation, or owner/repo, whose runners this fleet manages")
		need(a.GitHub.AppID != 0, "github.app_id",
			"the GitHub App's ID; unattended setup cannot drive the browser handshake, so create the App first and paste its numbers here")
		need(a.GitHub.InstallationID != 0, "github.installation_id",
			"the installation ID from the App's installation page URL")
		need(a.GitHub.PrivateKeyFile != "", "github.private_key_file",
			"the path to the App's .pem private key, which Zoomies seals with the instance encryption key")
		need(a.GitHub.WebhookSecret != "" || a.GitHub.WebhookSecretFile != "", "github.webhook_secret",
			"the App's webhook secret; without it every delivery fails signature validation")
	}
	if a.TLS.Mode == "files" {
		need(a.TLS.CertFile != "", "tls.cert_file", "the certificate to serve")
		need(a.TLS.KeyFile != "", "tls.key_file", "the certificate's private key")
	}
	return out
}

// Validate returns an error naming every missing key, or nil.
func (a *Answers) Validate(mode Mode) error {
	missing := a.Missing(mode)
	if len(missing) == 0 {
		return nil
	}
	var b strings.Builder
	where := a.Path()
	if where == "" {
		where = "this run"
	}
	fmt.Fprintf(&b, "installer: %s cannot continue without these answers (%s):\n", where, mode)
	for _, m := range missing {
		fmt.Fprintf(&b, "  - %s: %s\n", m.Key, m.Why)
	}
	b.WriteString("write them with `zoomies init --print-answers > answers.yaml`, fill it in, and pass --answers answers.yaml")
	return fmt.Errorf("%s", b.String())
}

// AdminPassword resolves the administrator's password, preferring the file so
// that the secret need never be written into the answer file itself.
func (a *Answers) AdminPassword() (string, error) {
	if a == nil {
		return "", nil
	}
	if a.Admin.PasswordFile != "" {
		b, err := os.ReadFile(a.Admin.PasswordFile)
		if err != nil {
			return "", fmt.Errorf("installer: reading admin.password_file %s: %w", a.Admin.PasswordFile, err)
		}
		return strings.TrimRight(string(b), "\r\n"), nil
	}
	return a.Admin.Password, nil
}

// WebhookSecret resolves the App's webhook secret from the file or the inline
// value.
func (a *Answers) WebhookSecret() (string, error) {
	if a == nil {
		return "", nil
	}
	if a.GitHub.WebhookSecretFile != "" {
		b, err := os.ReadFile(a.GitHub.WebhookSecretFile)
		if err != nil {
			return "", fmt.Errorf("installer: reading github.webhook_secret_file %s: %w", a.GitHub.WebhookSecretFile, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return a.GitHub.WebhookSecret, nil
}

// PrivateKey reads the GitHub App's private key. The PEM is returned rather
// than kept, so the caller can seal it and let it go out of scope.
func (a *Answers) PrivateKey() (string, error) {
	if a == nil || a.GitHub.PrivateKeyFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(a.GitHub.PrivateKeyFile)
	if err != nil {
		return "", fmt.Errorf("installer: reading github.private_key_file %s: %w (it is the .pem GitHub offered once, when the App was created)", a.GitHub.PrivateKeyFile, err)
	}
	if !strings.Contains(string(b), "PRIVATE KEY") {
		return "", fmt.Errorf("installer: %s does not look like a PEM private key; download a new one from the App's settings page", a.GitHub.PrivateKeyFile)
	}
	return string(b), nil
}

// JoinToken resolves the join token for `zoomies agent join`.
func (a *Answers) JoinToken() (string, error) {
	if a == nil {
		return "", nil
	}
	if a.Agent.JoinTokenFile != "" {
		b, err := os.ReadFile(a.Agent.JoinTokenFile)
		if err != nil {
			return "", fmt.Errorf("installer: reading agent.join_token_file %s: %w", a.Agent.JoinTokenFile, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(a.Agent.JoinToken), nil
}

// exampleAnswers is what `zoomies init --print-answers` emits. It is a
// commented template rather than a dump of defaults, because the person
// filling it in is deciding, not reviewing.
const exampleAnswers = `# zoomies answer file
#
#   zoomies init --non-interactive --answers answers.yaml
#
# Every key here is one question the interactive installer would ask. Anything
# left out keeps the detected or default value, except the keys marked
# REQUIRED: an unattended run refuses to guess those rather than build a
# controller that starts and then does not work.

# single     -- controller with an embedded agent, one process on one VM
# controller -- controller only; runner hosts join it separately
# agent      -- this host only runs runners
mode: single

# How this host runs Zoomies. Orthogonal to mode: any of them can be
# containerised.
#
# native   -- the binary under systemd or launchd. Leanest, starts fastest,
#             and needs no container runtime for the controller itself.
# compose  -- a docker-compose.yml and a fully populated .env, brought up with
#             "docker compose up -d". Easiest to upgrade and to move.
# docker   -- a single container from an env file. Fewest files, but you own
#             the run command.
#
# Omitted, it takes compose when this host has a compose command and native
# otherwise. A compose or docker deployment creates its first administrator in
# the browser, so admin.username and admin.password below are then unused.
deployment: compose
# Where docker-compose.yml and the .env are written. Defaults to config_dir.
# deployment_dir: /etc/zoomies
# The image those deployments run. Pin a release tag to choose when to upgrade.
# image: ghcr.io/eyupio/zoomies:latest

# The unprivileged account the service runs as. Created when init runs as root.
service_user: zoomies
# config_dir: /etc/zoomies
# state_dir: /var/lib/zoomies

# docker | podman | process
# Rootless docker or podman is strongly preferred: a container escape from a
# job then lands on an unprivileged account rather than on root. The process
# backend gives jobs no isolation at all -- they run directly on this host.
backend: docker
# docker_host: unix:///run/user/1000/docker.sock
# The most runners this host may hold at once.
capacity: 4

# Where the controller listens. Loopback is the safe default; reach it through
# a reverse proxy or an SSH tunnel. Use 0.0.0.0:8080 only with a proxy in front
# or with TLS configured below.
bind: 127.0.0.1:8080

tls:
  # "off" (quote it -- an unquoted off is a boolean in YAML), "files" or
  # "self-signed". GitHub will not deliver webhooks to a self-signed
  # certificate, so use "files" or terminate TLS in a proxy.
  mode: "off"
  # cert_file: /etc/zoomies/tls/fullchain.pem
  # key_file: /etc/zoomies/tls/privkey.pem
  # hosts: [zoomies.example.com]

# CIDRs whose X-Forwarded-For header is believed. Set this when a proxy
# terminates TLS, or every audit entry records the proxy's address. The word
# cloudflare stands for Cloudflare's published ranges.
# trusted_proxies: [10.0.0.0/8]

# REQUIRED. How GitHub and your browser reach this controller. Webhook
# deliveries go to $external_url/webhooks/github.
external_url: https://zoomies.example.com

github:
  # https://api.github.com, or https://ghes.example.com/api/v3 for Enterprise.
  api_base_url: https://api.github.com
  # REQUIRED unless skip is true. An organisation login, or "owner/repo".
  target: acme
  # org | repo. Derived from target when omitted.
  target_type: org
  # REQUIRED unless skip is true. An unattended run cannot drive the browser
  # handshake that creates the App, so create it first -- interactively, once --
  # and paste its numbers here.
  app_id: 0
  installation_id: 0
  private_key_file: /etc/zoomies/github-app.private-key.pem
  # Prefer the file: an answer file often ends up in a repository.
  # webhook_secret_file: /etc/zoomies/webhook.secret
  webhook_secret: ""
  # Set skip to true to finish setup now and connect GitHub later in the UI.
  skip: false

admin:
  # REQUIRED. The first administrator.
  username: admin
  # REQUIRED (or password_file). At least 12 characters.
  password: ""
  # password_file: /run/secrets/zoomies-admin-password

# The first pool, created on a single-host install once GitHub is connected.
# Without one the fleet has a host and nothing to place on it, so every key
# here is optional and the defaults come from what this host actually is.
pool:
  # Set skip to true to finish setup with no pool and create one yourself.
  skip: false
  # Defaults to this host's OS and architecture, e.g. zoomies-linux-x64 (and
  # zoomies-linux-x64-host for the process backend, which gives jobs no
  # container). A name given without the zoomies- prefix is stored with one.
  # name: zoomies-linux-x64
  # What a workflow's runs-on has to ask for. Defaults to the pool's name.
  # labels: [linux-x64]
  # Defaults to capacity above. Always set a maximum somewhere: it is the only
  # backstop against a runaway workflow.
  # max_runners: 4

# Only for deployment: native.
service:
  # systemd | launchd | compose | none. Detected when omitted.
  manager: systemd
  enable: true
  start: true

# Only for mode: agent.
agent:
  # REQUIRED for mode: agent.
  controller_url: https://zoomies.example.com
  # REQUIRED for mode: agent. Mint one with:
  #   zoomies hosts join-token create --ttl 15m
  join_token: ""
  # join_token_file: /run/secrets/zoomies-join-token
  # name: build-box-3
  # labels: {zone: eu-west-1a}
  # ca_file: /etc/zoomies/controller-ca.pem
`

// WriteExample emits a commented answer-file template, which is what
// `zoomies init --print-answers` prints.
func WriteExample(w io.Writer) error {
	if _, err := io.WriteString(w, exampleAnswers); err != nil {
		return fmt.Errorf("installer: writing the answer template: %w", err)
	}
	return nil
}
