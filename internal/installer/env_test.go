package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

// controllerEnvSpec is a fully answered controller, in the shape `zoomies init`
// hands to the generator after containerise has run.
func controllerEnvSpec() EnvSpec {
	poll := true
	return EnvSpec{
		Deployment:       DeploymentCompose,
		Mode:             ModeSingle,
		ExternalURL:      "https://zoomies.example.com",
		EncryptionKey:    "c2VjcmV0LWtleS10aGF0LWlzLXRoaXJ0eS10d28h",
		Bind:             "0.0.0.0:8080",
		PublishAddr:      "127.0.0.1",
		PublishedPort:    8080,
		TLSMode:          config.TLSOff,
		TrustedProxies:   []string{"10.0.0.0/8", "172.16.0.0/12"},
		Embedded:         true,
		Backend:          "docker",
		DockerHost:       "unix:///var/run/docker.sock",
		Capacity:         4,
		WorkDir:          ContainerWorkDir,
		DBPath:           ContainerDBPath,
		LogFormat:        "json",
		LogLevel:         "info",
		PollFallback:     &poll,
		GitHubAPIBaseURL: "https://ghes.example.com/api/v3",
		Image:            "ghcr.io/eyupio/zoomies:v1.2.3",
		DockerGID:        998,
	}
}

// parseEnv is a deliberately small reader for the file the generator writes: a
// second implementation, so that the test is not simply agreeing with
// ParseEnvFile about a shared misunderstanding. It returns each variable's
// value and the comment block immediately above it.
func parseEnv(t *testing.T, body string) (values, comments map[string]string) {
	t.Helper()
	values, comments = map[string]string{}, map[string]string{}
	var block []string
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			block = nil
		case strings.HasPrefix(line, "#"):
			block = append(block, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		default:
			key, value, ok := strings.Cut(line, "=")
			if !ok {
				t.Fatalf("line is neither blank, a comment nor an assignment: %q", line)
			}
			if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
				value = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\$`, "$").Replace(value[1 : len(value)-1])
			}
			values[key] = value
			comments[key] = strings.Join(block, " ")
			block = nil
		}
	}
	return values, comments
}

func TestParseDeployment(t *testing.T) {
	for _, in := range []string{"native", "compose", "docker", "COMPOSE", " docker "} {
		if _, err := ParseDeployment(in); err != nil {
			t.Errorf("ParseDeployment(%q): %v", in, err)
		}
	}
	_, err := ParseDeployment("kubernetes")
	if err == nil {
		t.Fatal("want an error for a deployment that does not exist")
	}
	for _, want := range []string{"native", "compose", "docker"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the alternatives; %q is missing from %q", want, err)
		}
	}
	if d, err := ParseDeployment(""); err != nil || d != "" {
		t.Errorf(`an empty deployment means "ask", got %q %v`, d, err)
	}
}

func TestRenderEnvCarriesEveryVariableWithAComment(t *testing.T) {
	spec := controllerEnvSpec()
	// Files mode so that the certificate paths are in the file too; they are
	// the only two variables that are conditional on an answer.
	dir := t.TempDir()
	spec.TLSMode = config.TLSFiles
	spec.TLSCertFile = writeFile(t, dir, "fullchain.pem", "not really a certificate")
	spec.TLSKeyFile = writeFile(t, dir, "privkey.pem", "nor is this")

	body, err := RenderEnv(spec)
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	values, comments := parseEnv(t, body)

	want := map[string]string{
		"ZOOMIES_EXTERNAL_URL":        spec.ExternalURL,
		"ZOOMIES_ENCRYPTION_KEY":      spec.EncryptionKey,
		"ZOOMIES_BIND":                spec.Bind,
		"ZOOMIES_TLS_MODE":            string(config.TLSFiles),
		"ZOOMIES_TLS_CERT_FILE":       spec.TLSCertFile,
		"ZOOMIES_TLS_KEY_FILE":        spec.TLSKeyFile,
		"ZOOMIES_TRUSTED_PROXIES":     "10.0.0.0/8,172.16.0.0/12",
		"ZOOMIES_AGENT_EMBEDDED":      "true",
		"ZOOMIES_AGENT_BACKEND":       "docker",
		"ZOOMIES_DOCKER_HOST":         spec.DockerHost,
		"ZOOMIES_AGENT_CAPACITY":      "4",
		"ZOOMIES_WORK_DIR":            ContainerWorkDir,
		"ZOOMIES_STATE_DIR":           ContainerStateDir,
		"ZOOMIES_DB_PATH":             ContainerDBPath,
		"ZOOMIES_LOG_FORMAT":          "json",
		"ZOOMIES_LOG_LEVEL":           "info",
		"ZOOMIES_POLL_FALLBACK":       "true",
		"ZOOMIES_GITHUB_API_BASE_URL": spec.GitHubAPIBaseURL,
		"ZOOMIES_IMAGE":               spec.Image,
		"ZOOMIES_PUBLISHED_ADDR":      "127.0.0.1",
		"ZOOMIES_PUBLISHED_PORT":      "8080",
		"DOCKER_GID":                  "998",
	}
	for key, wantValue := range want {
		got, ok := values[key]
		if !ok {
			t.Errorf("%s is not in the generated file", key)
			continue
		}
		if got != wantValue {
			t.Errorf("%s = %q, want %q", key, got, wantValue)
		}
		// An operator reading this file six months from now should not have to
		// go and find the documentation.
		if comments[key] == "" {
			t.Errorf("%s was written with no comment saying what it is for", key)
		}
	}
	for key, value := range values {
		if value == "" && key != "DOCKER_GID" {
			t.Errorf("%s was left blank, which is the placeholder this generator exists to abolish", key)
		}
	}
}

func TestRenderEnvIsPure(t *testing.T) {
	first, err := RenderEnv(controllerEnvSpec())
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	second, err := RenderEnv(controllerEnvSpec())
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	if first != second {
		t.Fatal("the same spec must render the same file, or nothing can be asserted about it")
	}
}

func TestRenderEnvForAnAgent(t *testing.T) {
	spec := controllerEnvSpec()
	spec.Mode = ModeAgent
	spec.ControllerURL = "https://zoomies.example.com"
	spec.AgentToken = "zooagent_9f3c"

	body, err := RenderEnv(spec)
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	values, comments := parseEnv(t, body)
	if values["ZOOMIES_CONTROLLER_URL"] != spec.ControllerURL {
		t.Errorf("ZOOMIES_CONTROLLER_URL = %q", values["ZOOMIES_CONTROLLER_URL"])
	}
	if values["ZOOMIES_AGENT_TOKEN"] != spec.AgentToken {
		t.Errorf("ZOOMIES_AGENT_TOKEN = %q", values["ZOOMIES_AGENT_TOKEN"])
	}
	for _, key := range []string{"ZOOMIES_CONTROLLER_URL", "ZOOMIES_AGENT_TOKEN"} {
		if comments[key] == "" {
			t.Errorf("%s was written with no comment", key)
		}
	}
	// An agent has no listener, so it has no encryption key and no external
	// URL to carry either.
	if _, ok := values["ZOOMIES_ENCRYPTION_KEY"]; ok {
		t.Error("an agent stores nothing, so it must not be handed the instance key")
	}
}

func TestRenderEnvRefusesAPlaceholder(t *testing.T) {
	spec := controllerEnvSpec()
	spec.EncryptionKey = ""
	if _, err := RenderEnv(spec); err == nil {
		t.Fatal("a file with a blank encryption key is a half-finished install, not a file")
	}
}

func TestRenderEnvRefusesANewline(t *testing.T) {
	spec := controllerEnvSpec()
	spec.ExternalURL = "https://zoomies.example.com\nZOOMIES_DISABLE_AUTH=true"

	_, err := RenderEnv(spec)
	if err == nil {
		t.Fatal("a value containing a newline must be refused, not written and silently truncated")
	}
	if !strings.Contains(err.Error(), "ZOOMIES_EXTERNAL_URL") {
		t.Errorf("the error must name the variable, got %q", err)
	}
}

func TestRenderEnvQuotesWhatNeedsIt(t *testing.T) {
	spec := controllerEnvSpec()
	spec.TLSMode = config.TLSFiles
	spec.TLSCertFile = `/etc/ssl/Application Support/full "chain".pem`
	spec.TLSKeyFile = "/etc/ssl/plain.pem"

	body, err := RenderEnv(spec)
	if err != nil {
		t.Fatalf("RenderEnv: %v", err)
	}
	if !strings.Contains(body, `ZOOMIES_TLS_CERT_FILE="`) {
		t.Errorf("a value with a space in it must be quoted:\n%s", body)
	}
	if !strings.Contains(body, "ZOOMIES_TLS_KEY_FILE=/etc/ssl/plain.pem") {
		t.Error("a value that needs no quoting must not get any: docker run --env-file would keep the quote marks")
	}
	values, _ := parseEnv(t, body)
	if values["ZOOMIES_TLS_CERT_FILE"] != spec.TLSCertFile {
		t.Errorf("a quoted value must read back as itself: got %q, want %q", values["ZOOMIES_TLS_CERT_FILE"], spec.TLSCertFile)
	}
}

func TestWriteEnvIsPrivateAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EnvFileName)

	res, err := WriteEnv(path, controllerEnvSpec())
	if err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}
	if res.Backup != "" || res.ReusedKey {
		t.Fatalf("nothing was there before, so nothing was reused or backed up: %+v", res)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("%s is mode %v; it holds the encryption key and must be 0600", path, got)
	}

	// The write goes through a temporary file and a rename, so an interrupted
	// install cannot leave compose reading half a file.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != EnvFileName {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("the directory should hold only the env file, got %v", names)
	}

	on, err := ParseEnvFile(path)
	if err != nil {
		t.Fatalf("ParseEnvFile: %v", err)
	}
	if on["ZOOMIES_EXTERNAL_URL"] != "https://zoomies.example.com" {
		t.Errorf("external URL did not survive the round trip: %q", on["ZOOMIES_EXTERNAL_URL"])
	}
}

// TestWriteEnvKeepsTheEncryptionKeyOnARerun guards the single most damaging
// thing this installer could do. A new key on an upgrade would leave every
// stored GitHub App private key and webhook secret undecryptable, with the
// database otherwise intact -- a fleet that looks fine and cannot talk to
// GitHub.
func TestWriteEnvKeepsTheEncryptionKeyOnARerun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EnvFileName)

	spec := controllerEnvSpec()
	spec.EncryptionKey = ""
	first, err := WriteEnv(path, spec)
	if err != nil {
		t.Fatalf("first WriteEnv: %v", err)
	}
	if first.ReusedKey {
		t.Fatal("there was no key to reuse on a fresh install")
	}
	before, err := ParseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	key := before["ZOOMIES_ENCRYPTION_KEY"]
	if key == "" {
		t.Fatal("a fresh install must generate a key")
	}

	// A second run, with different answers and no key of its own: exactly what
	// `zoomies init` does on an upgrade.
	spec.Capacity = 9
	spec.ExternalURL = "https://zoomies.example.com:8443"
	second, err := WriteEnv(path, spec)
	if err != nil {
		t.Fatalf("second WriteEnv: %v", err)
	}
	if !second.ReusedKey {
		t.Fatal("the second run must report that it kept the existing key")
	}
	after, err := ParseEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if after["ZOOMIES_ENCRYPTION_KEY"] != key {
		t.Fatalf("the encryption key was replaced on a re-run:\n before %q\n after  %q", key, after["ZOOMIES_ENCRYPTION_KEY"])
	}
	if after["ZOOMIES_AGENT_CAPACITY"] != "9" {
		t.Errorf("the rest of the file must still be rewritten; capacity = %q", after["ZOOMIES_AGENT_CAPACITY"])
	}

	// The previous file is kept, because an operator may have edited it.
	if second.Backup == "" {
		t.Fatal("the previous file must be backed up, not clobbered")
	}
	if !strings.HasPrefix(filepath.Base(second.Backup), EnvFileName+".bak.") {
		t.Errorf("backup is named %q, want .env.bak.<timestamp>", filepath.Base(second.Backup))
	}
	backed, err := ParseEnvFile(second.Backup)
	if err != nil {
		t.Fatal(err)
	}
	if backed["ZOOMIES_AGENT_CAPACITY"] != "4" {
		t.Errorf("the backup should hold the previous answers, got capacity %q", backed["ZOOMIES_AGENT_CAPACITY"])
	}
}

// TestWriteEnvLeavesTheOldFileAloneWhenItCannotRender is the other half of
// writing atomically: a refusal must not cost the operator the file they had.
func TestWriteEnvLeavesTheOldFileAloneWhenItCannotRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, EnvFileName)
	if _, err := WriteEnv(path, controllerEnvSpec()); err != nil {
		t.Fatalf("WriteEnv: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	bad := controllerEnvSpec()
	bad.ExternalURL = "https://zoomies.example.com\nrubbish"
	if _, err := WriteEnv(path, bad); err == nil {
		t.Fatal("a value with a newline must be refused")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("a refused write must leave the existing file exactly as it was")
	}
}

func TestParseEnvFileHandlesWhatTheGeneratorWrites(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, ".env", strings.Join([]string{
		"# a comment",
		"",
		"PLAIN=value",
		`QUOTED="two words"`,
		`ESCAPED="say \"hello\""`,
		"EMPTY=",
		"export EXPORTED=yes",
		"NOT AN ASSIGNMENT",
	}, "\n"))

	got, err := ParseEnvFile(path)
	if err != nil {
		t.Fatalf("ParseEnvFile: %v", err)
	}
	want := map[string]string{
		"PLAIN":    "value",
		"QUOTED":   "two words",
		"ESCAPED":  `say "hello"`,
		"EMPTY":    "",
		"EXPORTED": "yes",
	}
	for key, wantValue := range want {
		if got[key] != wantValue {
			t.Errorf("%s = %q, want %q", key, got[key], wantValue)
		}
	}
}

func TestEnvSpecConfigIsValidForEachDeployment(t *testing.T) {
	certDir := t.TempDir()
	cert := writeFile(t, certDir, "cert.pem", "not really a certificate, but it exists")
	key := writeFile(t, certDir, "key.pem", "nor is this")

	cases := []struct {
		name        string
		mutate      func(*EnvSpec)
		wantCodes   []string
		unwantCodes []string
	}{
		{
			name:   "behind a proxy, which is the reference deployment",
			mutate: func(s *EnvSpec) {},
			// The container really does serve plain HTTP; something in front
			// terminates TLS, and the operator is told so rather than left to
			// wonder why the warning is there.
			wantCodes:   []string{"bind.public_no_tls"},
			unwantCodes: []string{"crypto.no_key", "external_url.missing", "external_url.insecure", "proxy.untrusted"},
		},
		{
			name: "terminating TLS in the container",
			mutate: func(s *EnvSpec) {
				s.TLSMode = config.TLSFiles
				s.TLSCertFile, s.TLSKeyFile = cert, key
			},
			unwantCodes: []string{"bind.public_no_tls", "tls.files_missing", "tls.file_unreadable", "crypto.no_key"},
		},
		{
			name: "a controller with no embedded agent",
			mutate: func(s *EnvSpec) {
				s.Embedded = false
			},
			wantCodes:   []string{"agent.none"},
			unwantCodes: []string{"agent.backend_missing", "agent.workdir"},
		},
		{
			name: "the process backend says what it costs",
			mutate: func(s *EnvSpec) {
				s.Backend = "process"
				s.DockerHost = ""
			},
			wantCodes: []string{"agent.process_backend"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := controllerEnvSpec()
			tc.mutate(&spec)

			cfg := spec.Config()
			findings := cfg.Validate()
			if err := findings.Err(); err != nil {
				t.Fatalf("a deployment the installer produces must never refuse to start:\n%v", err)
			}
			codes := findingCodes(findings)
			for _, want := range tc.wantCodes {
				if !contains(codes, want) {
					t.Errorf("expected finding %q, got %v", want, codes)
				}
			}
			for _, unwanted := range tc.unwantCodes {
				if contains(codes, unwanted) {
					t.Errorf("did not expect finding %q, got %v", unwanted, codes)
				}
			}
		})
	}
}

func TestEnvSpecConfigDoesNotPointAtAKeyFileTheImageLacks(t *testing.T) {
	cfg := controllerEnvSpec().Config()
	if cfg.Security.EncryptionKeyFile != "" {
		t.Errorf("a containerised deployment carries its key in the environment, not at %q", cfg.Security.EncryptionKeyFile)
	}
	if !cfg.CookieSecureValue() {
		t.Error("an https external URL must still set secure cookies behind a proxy")
	}
}
