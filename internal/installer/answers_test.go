package installer

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
	return path
}

func TestLoadAnswers(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "answers.yaml", `
mode: controller
backend: podman
capacity: 6
bind: 0.0.0.0:8443
tls:
  mode: files
  cert_file: /etc/zoomies/tls/cert.pem
  key_file: /etc/zoomies/tls/key.pem
trusted_proxies: [10.0.0.0/8]
external_url: https://zoomies.example.com/
github:
  target: acme
  app_id: 42
  installation_id: 99
  private_key_file: /etc/zoomies/app.pem
  webhook_secret: shhh
admin:
  username: ada
  password: correct-horse-battery
service:
  manager: systemd
  start: false
`)

	a, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a.Mode != "controller" || a.Backend != "podman" || a.Capacity != 6 {
		t.Fatalf("basic fields not decoded: %+v", a)
	}
	if a.ExternalURL != "https://zoomies.example.com" {
		t.Fatalf("external URL not normalised: %q", a.ExternalURL)
	}
	if a.TLS.Mode != "files" || a.TLS.CertFile == "" {
		t.Fatalf("tls block not decoded: %+v", a.TLS)
	}
	if a.Service.Start == nil || *a.Service.Start {
		t.Fatalf("service.start should decode as an explicit false, got %v", a.Service.Start)
	}
	if a.Service.Enable != nil {
		t.Fatalf("service.enable was not in the file, so it must stay nil rather than default to false")
	}
	if got := a.Path(); got != path {
		t.Fatalf("Path() = %q, want %q", got, path)
	}
}

func TestLoadAnswersRejectsUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "answers.yaml", "extenal_url: https://example.com\n")
	if _, err := Load(path); err == nil {
		t.Fatal("a typo'd key must be an error, or an unattended install silently keeps the default")
	}
}

func TestLoadAnswersMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("want an error for a missing answer file")
	}
	if !strings.Contains(err.Error(), "--print-answers") {
		t.Fatalf("the error should say how to make one, got: %v", err)
	}
}

func TestAnswersMissingNamesEveryRequiredKey(t *testing.T) {
	var a *Answers // no answer file at all
	missing := keysOf(a.Missing(ModeSingle))

	for _, want := range []string{"external_url", "admin.username", "admin.password",
		"github.target", "github.app_id", "github.installation_id", "github.private_key_file", "github.webhook_secret"} {
		if !contains(missing, want) {
			t.Errorf("Missing() should name %q; got %v", want, missing)
		}
	}

	err := a.Validate(ModeSingle)
	if err == nil {
		t.Fatal("Validate must fail when everything is missing")
	}
	if !strings.Contains(err.Error(), "external_url") {
		t.Fatalf("the error must name the keys, got: %v", err)
	}
}

func TestAnswersMissingSkipsGitHubWhenSkipped(t *testing.T) {
	a := &Answers{ExternalURL: "https://x.example.com"}
	a.GitHub.Skip = true
	a.Admin.Username = "ada"
	a.Admin.Password = "correct-horse-battery"

	if got := a.Missing(ModeSingle); len(got) != 0 {
		t.Fatalf("nothing should be missing, got %v", keysOf(got))
	}
	if err := a.Validate(ModeSingle); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestAnswersMissingForAgentMode(t *testing.T) {
	a := &Answers{}
	missing := keysOf(a.Missing(ModeAgent))
	if !contains(missing, "agent.controller_url") || !contains(missing, "agent.join_token") {
		t.Fatalf("agent mode needs a controller and a token, got %v", missing)
	}
	if contains(missing, "external_url") {
		t.Fatalf("an agent has no external URL to be missing: %v", missing)
	}
}

func TestAnswersTLSModeOffSurvivesYAMLBooleans(t *testing.T) {
	dir := t.TempDir()
	// An unquoted "off" is a boolean in YAML 1.1, and operators write it.
	path := writeFile(t, dir, "answers.yaml", "tls:\n  mode: false\n")
	a, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if a.TLS.Mode != "off" {
		t.Fatalf("tls.mode = %q, want off", a.TLS.Mode)
	}
}

func TestAnswersSecretsComeFromFiles(t *testing.T) {
	dir := t.TempDir()
	pwPath := writeFile(t, dir, "pw", "correct-horse-battery\n")
	secretPath := writeFile(t, dir, "hook", "  hunter2hunter2  \n")
	keyPath := writeFile(t, dir, "app.pem", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----\n")
	tokenPath := writeFile(t, dir, "join", "zoojoin_abc\n")

	a := &Answers{}
	a.Admin.PasswordFile = pwPath
	a.GitHub.WebhookSecretFile = secretPath
	a.GitHub.PrivateKeyFile = keyPath
	a.Agent.JoinTokenFile = tokenPath

	if pw, err := a.AdminPassword(); err != nil || pw != "correct-horse-battery" {
		t.Fatalf("AdminPassword = %q, %v", pw, err)
	}
	if s, err := a.WebhookSecret(); err != nil || s != "hunter2hunter2" {
		t.Fatalf("WebhookSecret = %q, %v", s, err)
	}
	if pem, err := a.PrivateKey(); err != nil || !strings.Contains(pem, "PRIVATE KEY") {
		t.Fatalf("PrivateKey = %q, %v", pem, err)
	}
	if tok, err := a.JoinToken(); err != nil || tok != "zoojoin_abc" {
		t.Fatalf("JoinToken = %q, %v", tok, err)
	}
}

func TestPrivateKeyRejectsSomethingThatIsNotAKey(t *testing.T) {
	dir := t.TempDir()
	a := &Answers{}
	a.GitHub.PrivateKeyFile = writeFile(t, dir, "app.pem", "this is not a key\n")
	if _, err := a.PrivateKey(); err == nil {
		t.Fatal("want an error for a file that is not a PEM key")
	}
}

func TestWriteExampleRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteExample(&buf); err != nil {
		t.Fatalf("WriteExample: %v", err)
	}
	dir := t.TempDir()
	path := writeFile(t, dir, "answers.yaml", buf.String())

	a, err := Load(path)
	if err != nil {
		t.Fatalf("the printed template must load back: %v", err)
	}
	if a.Mode != "single" {
		t.Fatalf("mode = %q, want single", a.Mode)
	}
	if a.TLS.Mode != "off" {
		t.Fatalf("tls.mode = %q, want off (it must be quoted in the template)", a.TLS.Mode)
	}
	if !strings.Contains(buf.String(), "REQUIRED") {
		t.Fatal("the template should mark the keys an unattended run cannot invent")
	}
}

func keysOf(ms []MissingAnswer) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Key)
	}
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
