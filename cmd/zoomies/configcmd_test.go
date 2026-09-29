package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
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

	// The container's health check names http://127.0.0.1:8080 whatever the
	// controller serves, so with TLS on it could never pass: the container
	// read as unhealthy for as long as it ran, and an upgrade waiting on the
	// check would wait until it gave up.
	t.Run("a loopback controller serving TLS is asked over https", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer srv.Close()
		plain := "http://" + strings.TrimPrefix(srv.URL, "https://")

		e, out, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"healthcheck", "--url", plain}); code != exitOK {
			t.Fatalf("exit code = %d, want 0: %s", code, errOut)
		}
		if !strings.Contains(out.String(), srv.URL+"/healthz is healthy") {
			t.Errorf("output = %q, want the https URL named", out)
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

// keyStore is an empty database, which is what a first run has: nothing is
// sealed, so a key may be generated.
func keyStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Options{Path: ":memory:"})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestEncryptionKeyIsGeneratedOnceAndReused(t *testing.T) {
	dir := isolateHost(t)
	cfg := config.Default()
	cfg.Security.EncryptionKeyFile = filepath.Join(dir, "encryption.key")

	ctx := context.Background()
	st := keyStore(t)
	log := discardLogger()
	first, err := loadOrCreateKey(ctx, st, cfg, log)
	if err != nil {
		t.Fatalf("generating: %v", err)
	}

	info, err := os.Stat(cfg.Security.EncryptionKeyFile)
	if err != nil {
		t.Fatalf("the key file was not written: %v", err)
	}
	// Windows has no mode bits to check: Go reports 0666 for every writable
	// file there, and what keeps another local user out is the directory's
	// ACL, which the installer sets and this test cannot see.
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Errorf("key file mode = %04o, want 0600: anything else lets another local user read it", perm)
	}

	second, err := loadOrCreateKey(ctx, st, cfg, log)
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

	got, err := loadOrCreateKey(context.Background(), keyStore(t), cfg, discardLogger())
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

	_, err := loadOrCreateKey(context.Background(), keyStore(t), cfg, discardLogger())
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

// secretShaped reports whether a `yaml` name reads like something that must
// never be printed. A path to a secret is not a secret: an operator debugging
// a deployment needs to see which file the key is read from, and printing that
// discloses nothing, so anything ending in _file is deliberately out.
//
// The word list is the weak point, and agent.registry_auth is what proved it:
// a base64 registry credential whose name holds none of secret, token, key or
// password, so this test called it harmless and `config print` disclosed it in
// full, as did every backup manifest. auth and credential are here for that.
// The lesson generalises past the two words -- a credential is not obliged to
// be named like one -- so a new secret-bearing field is worth a thought here
// rather than a hope that its name happens to match.
func secretShaped(name string) bool {
	if strings.HasSuffix(name, "_file") {
		return false
	}
	for _, word := range []string{"secret", "token", "key", "password", "passphrase", "auth", "credential"} {
		if strings.Contains(name, word) {
			return true
		}
	}
	return false
}

// blankSecrets is a hand-written list of fields, and the capacity-demand
// signing secret shows what that costs: it was added to the configuration and
// not to the list, so `zoomies config print` disclosed it for as long as the
// feature has existed. Nobody notices a field that is missing from a list.
//
// So this test does not check the list. It walks the whole configuration by
// reflection, puts a distinctive value in every string field whose name reads
// like a secret, and asserts that none of those values survives the blanking
// anywhere in the output. A secret added tomorrow fails here on the day it is
// added, and a secret copied into some other field fails too, which checking
// the fields one by one would not catch.
func TestEverySecretShapedFieldIsBlanked(t *testing.T) {
	var cfg config.Config
	planted := map[string]string{}
	plant(t, reflect.ValueOf(&cfg).Elem(), "", planted)
	if len(planted) < 5 {
		t.Fatalf("the walk found %d secret-shaped fields, which is fewer than the configuration has: %v", len(planted), planted)
	}

	printed, err := yaml.Marshal(blankSecrets(&cfg))
	if err != nil {
		t.Fatalf("marshalling the blanked configuration: %v", err)
	}
	for path, value := range planted {
		if strings.Contains(string(printed), value) {
			t.Errorf("%s was printed in full; add it to blankSecrets", path)
		}
	}
	// And the operator can still tell that one is set, which is the whole
	// reason the placeholder is not an empty string.
	if !strings.Contains(string(printed), secretPlaceholder) {
		t.Errorf("nothing says a secret is configured:\n%s", printed)
	}
}

// plant fills every secret-shaped string field with a value naming its own
// path, so a failure says which field leaked rather than only that one did.
func plant(t *testing.T, v reflect.Value, prefix string, into map[string]string) {
	t.Helper()
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range v.NumField() {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if name == "" || name == "-" {
			continue
		}
		path := name
		if prefix != "" {
			path = prefix + "." + name
		}
		field := v.Field(i)
		switch field.Kind() {
		case reflect.Struct:
			plant(t, field, path, into)
		case reflect.Slice:
			// A list of structs is a place secrets hide from a walk that only
			// follows fields: backup.remotes carries an S3 secret and the
			// archive passphrase and was never entered. One element is enough
			// to stand for the list.
			if field.Type().Elem().Kind() == reflect.Struct {
				field.Set(reflect.MakeSlice(field.Type(), 1, 1))
				plant(t, field.Index(0), path+"[0]", into)
			}
		case reflect.String:
			if secretShaped(name) {
				value := "planted-secret-" + strings.ReplaceAll(path, ".", "-")
				field.SetString(value)
				into[path] = value
			}
		}
	}
}

// A restore that brought the database back and left the key behind is
// indistinguishable from a first run at the key file: no file, so generate
// one. The instance that produced would start, report itself healthy, and fail
// inside its first GitHub call with a decryption error nobody could connect to
// the restore that caused it.
func TestAMissingKeyOverASealedDatabaseIsRefusedAtStartup(t *testing.T) {
	dir := isolateHost(t)
	ctx := context.Background()
	st := keyStore(t)
	if err := st.CreateInstallation(ctx, &store.Installation{
		AppID: 1, InstallationID: 2, Target: "acme", TargetType: store.TargetOrg,
		PrivateKeyEnc: []byte("sealed with the key that was not restored"),
	}); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}

	cfg := config.Default()
	cfg.Security.EncryptionKeyFile = filepath.Join(dir, "encryption.key")

	_, err := loadOrCreateKey(ctx, st, cfg, discardLogger())
	if err == nil {
		t.Fatal("a new key was generated over a database whose secrets only the old one opens")
	}
	// The message has to name the file to put back, because the operator
	// reading it is mid-restore and has the backup open.
	for _, want := range []string{cfg.Security.EncryptionKeyFile, "ZOOMIES_ENCRYPTION_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(cfg.Security.EncryptionKeyFile); !os.IsNotExist(serr) {
		t.Error("a key file was written anyway, so the next start would find one and read nothing")
	}
}

func TestOnlyALoopbackHealthCheckIsMovedToHTTPS(t *testing.T) {
	for in, want := range map[string]string{
		"http://127.0.0.1:8080/healthz": "https://127.0.0.1:8080/healthz",
		"http://[::1]:8080/healthz":     "https://[::1]:8080/healthz",
		"http://localhost:8080/healthz": "https://localhost:8080/healthz",
		// Off the host, plain HTTP is what the operator asked for, and a
		// certificate left unchecked there would be a real risk.
		"http://10.0.0.5:8080/healthz":       "",
		"http://zoomies.example.com/healthz": "",
		"https://127.0.0.1:8080/healthz":     "",
	} {
		got, ok := loopbackHTTPS(in)
		if ok != (want != "") || got != want {
			t.Errorf("loopbackHTTPS(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// TestConfigPrintBlanksBackupRemoteSecretsWithoutMutatingTheConfig covers the
// one list of structs in the configuration.
//
// A backup remote carries the bucket's secret key and the passphrase that seals
// every offsite copy, so together they open the whole fleet's database, and
// `config print` is the command whose output gets pasted into an issue. The
// copy blankSecrets returns shares its slice with the configuration it was
// given, so blanking in place would also have blanked the live one.
func TestConfigPrintBlanksBackupRemoteSecretsWithoutMutatingTheConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Backup.Remotes = []config.BackupRemote{{
		Name: "offsite", Endpoint: "https://s3.example.com", Bucket: "zoomies-backups",
		AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "bucket-secret-value", Passphrase: "archive-passphrase-value",
	}}

	for _, format := range []string{"yaml", "json"} {
		var printed []byte
		var err error
		if format == "yaml" {
			printed, err = yaml.Marshal(blankSecrets(cfg))
		} else {
			printed, err = json.Marshal(blankSecrets(cfg))
		}
		if err != nil {
			t.Fatalf("marshalling as %s: %v", format, err)
		}
		for _, leaked := range []string{"bucket-secret-value", "archive-passphrase-value", "AKIAEXAMPLE"} {
			if strings.Contains(string(printed), leaked) {
				t.Errorf("%s output printed %q:\n%s", format, leaked, printed)
			}
		}
		// What an operator debugging a destination needs is still there.
		for _, kept := range []string{"offsite", "https://s3.example.com", "zoomies-backups", secretPlaceholder} {
			if !strings.Contains(string(printed), kept) {
				t.Errorf("%s output lost %q:\n%s", format, kept, printed)
			}
		}
	}

	got := cfg.Backup.Remotes[0]
	if got.SecretAccessKey != "bucket-secret-value" || got.Passphrase != "archive-passphrase-value" || got.AccessKeyID != "AKIAEXAMPLE" {
		t.Errorf("blankSecrets blanked the caller's configuration as well: %+v", got)
	}
}
