package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
)

// writeConfig puts a zoomies.yaml in a temporary directory and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "zoomies.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// isolateHost points the config and state directories at temporary ones, so a
// test cannot read or write /etc/zoomies on the machine running it.
func isolateHost(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("ZOOMIES_CONFIG_DIR", dir)
	t.Setenv("ZOOMIES_STATE_DIR", dir)
	return dir
}

const goodConfig = `
server:
  bind: 127.0.0.1:8080
  external_url: https://zoomies.example.com
database:
  path: /tmp/zoomies-test.db
agent:
  embedded: false
`

const badConfig = `
server:
  bind: "not-a-host-port"
log:
  level: chatty
`

func TestConfigCheckAcceptsAGoodFile(t *testing.T) {
	e, out, _ := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, goodConfig)

	if code := dispatch(context.Background(), e, []string{"config", "check", "--config", path}); code != exitOK {
		t.Fatalf("exit code = %d, want 0\n%s", code, out)
	}
	if !strings.Contains(out.String(), "is valid") {
		t.Errorf("check did not say the file is valid:\n%s", out)
	}
}

func TestConfigCheckRejectsABadFileAndNamesEveryFault(t *testing.T) {
	e, out, errOut := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, badConfig)

	if code := dispatch(context.Background(), e, []string{"config", "check", "--config", path}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	combined := out.String() + errOut.String()
	for _, want := range []string{
		"server.bind",
		"not a host:port address",
		"log.level",
		"is not a log level",
		"use debug, info, warn or error",
	} {
		if !strings.Contains(combined, want) {
			t.Errorf("the report does not mention %q:\n%s", want, combined)
		}
	}
}

func TestConfigCheckOnAMissingFileIsAnError(t *testing.T) {
	e, _, _ := newTestEnv(t)
	isolateHost(t)

	// An explicit --config that does not exist is a mistake worth reporting;
	// a missing default file is not, and that difference lives in config.Load.
	code := dispatch(context.Background(), e, []string{"config", "check", "--config", filepath.Join(t.TempDir(), "nope.yaml")})
	if code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
}

func TestConfigPrintBlanksSecrets(t *testing.T) {
	e, out, _ := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, goodConfig+`
security:
  encryption_key: aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa=
oidc:
  client_secret: hunter2-and-then-some
`)

	if code := dispatch(context.Background(), e, []string{"config", "print", "--config", path}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	for _, secret := range []string{"aaaaaaaaaaaa", "hunter2"} {
		if strings.Contains(out.String(), secret) {
			t.Errorf("a secret was printed:\n%s", out)
		}
	}
	if strings.Count(out.String(), secretPlaceholder) != 2 {
		t.Errorf("both secrets should be shown as %q so an operator can see one is set:\n%s", secretPlaceholder, out)
	}
	if !strings.Contains(out.String(), "external_url: https://zoomies.example.com") {
		t.Errorf("the effective configuration is missing:\n%s", out)
	}
}

func TestConfigPrintJSON(t *testing.T) {
	e, out, _ := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, goodConfig)

	if code := dispatch(context.Background(), e, []string{"config", "print", "--config", path, "--output", "json"}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "{") {
		t.Errorf("--output json did not produce JSON:\n%s", out)
	}
}

func TestConfigPrintRejectsAnUnknownFormat(t *testing.T) {
	e, _, _ := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, goodConfig)

	if code := dispatch(context.Background(), e, []string{"config", "print", "--config", path, "--output", "table"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
}

func TestHealthcheckAgainstAServer(t *testing.T) {
	t.Run("healthy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("path = %q, want /healthz", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()

		e, out, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"healthcheck", "--url", srv.URL}); code != exitOK {
			t.Fatalf("exit code = %d, want 0", code)
		}
		if !strings.Contains(out.String(), "is healthy") {
			t.Errorf("output = %q", out)
		}
	})

	t.Run("a URL already ending in /healthz is not doubled", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/healthz" {
				t.Errorf("path = %q", r.URL.Path)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()

		e, _, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"healthcheck", "--url", srv.URL + "/healthz"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
	})

	t.Run("not ready", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"ok":false,"message":"the database is not answering"}`))
		}))
		defer srv.Close()

		e, _, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"healthcheck", "--url", srv.URL}); code != exitError {
			t.Fatalf("exit code = %d, want %d", code, exitError)
		}
		if !strings.Contains(errOut.String(), "503") {
			t.Errorf("the failure does not carry the status:\n%s", errOut)
		}
	})

	t.Run("nothing listening", func(t *testing.T) {
		e, _, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"healthcheck", "--url", "http://127.0.0.1:1"}); code != exitError {
			t.Fatalf("exit code = %d, want %d", code, exitError)
		}
		if !strings.Contains(errOut.String(), "did not answer") {
			t.Errorf("output = %s", errOut)
		}
	})
}

func TestEncryptionKeyIsGeneratedOnceAndReused(t *testing.T) {
	dir := isolateHost(t)
	cfg := config.Default()
	cfg.Security.EncryptionKeyFile = filepath.Join(dir, "encryption.key")

	log := discardLogger()
	first, err := loadOrCreateKey(cfg, log)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	info, err := os.Stat(cfg.Security.EncryptionKeyFile)
	if err != nil {
		t.Fatalf("the key file was not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %04o, want 0600: anything else lets another local user read it", perm)
	}

	second, err := loadOrCreateKey(cfg, log)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if first.Encode() != second.Encode() {
		t.Error("a second start generated a different key; every sealed secret in the database would be unreadable")
	}
}

func TestEncryptionKeyFromTheEnvironmentIsUsedAsIs(t *testing.T) {
	dir := isolateHost(t)
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Security.EncryptionKey = key.Encode()
	cfg.Security.EncryptionKeyFile = filepath.Join(dir, "encryption.key")

	got, err := loadOrCreateKey(cfg, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	if got.Encode() != key.Encode() {
		t.Error("the configured key was not the one used")
	}
	if _, err := os.Stat(cfg.Security.EncryptionKeyFile); !os.IsNotExist(err) {
		t.Error("a key file was written even though the key came from the configuration")
	}
}

func TestUnusableEncryptionKeyNamesTheSetting(t *testing.T) {
	isolateHost(t)
	cfg := config.Default()
	cfg.Security.EncryptionKey = "not-a-key"

	_, err := loadOrCreateKey(cfg, discardLogger())
	if err == nil {
		t.Fatal("a nonsense key was accepted")
	}
	if !strings.Contains(err.Error(), "security.encryption_key") {
		t.Errorf("the error does not name the setting: %v", err)
	}
}

// discardLogger keeps the key-generation warning out of the test output while
// still exercising the code path that emits it.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
