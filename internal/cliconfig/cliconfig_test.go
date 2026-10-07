package cliconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/config"
)

func TestEnsureURLCreatesThePrivateFileAndItsDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".config", "zoomies", "cli.yaml")
	got, err := EnsureURL(path, "https://zoomies.example.com")
	if err != nil || got != Created {
		t.Fatalf("EnsureURL = %v, %v; want Created", got, err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.URL != "https://zoomies.example.com" || cfg.Token != "" {
		t.Errorf("the file must name the controller and hold no credential: %+v (%v)", cfg, err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s mode = %v (%v), want %v: a token goes in this file next", p, fi.Mode().Perm(), err, want)
		}
	}
}

// What an operator put in the file is theirs: a url they chose is never replaced,
// and one added beside their token leaves the token and their comments alone.
func TestEnsureURLNeverReplacesWhatTheOperatorWrote(t *testing.T) {
	dir := t.TempDir()
	chosen := filepath.Join(dir, "chosen.yaml")
	if err := os.WriteFile(chosen, []byte("url: https://mine.example\ntoken: zoo_x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := EnsureURL(chosen, "https://other.example"); err != nil || got != Kept {
		t.Fatalf("EnsureURL over a chosen url = %v, %v; want Kept", got, err)
	}
	if raw, _ := os.ReadFile(chosen); !strings.Contains(string(raw), "mine.example") || strings.Contains(string(raw), "other.example") {
		t.Errorf("the operator's url must stay: %s", raw)
	}

	tokenOnly := filepath.Join(dir, "token.yaml")
	if err := os.WriteFile(tokenOnly, []byte("# mine\ntoken: zoo_y\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := EnsureURL(tokenOnly, "https://zoomies.example.com"); err != nil || got != Added {
		t.Fatalf("EnsureURL beside a token = %v, %v; want Added", got, err)
	}
	cfg, err := Load(tokenOnly)
	if err != nil || cfg.URL != "https://zoomies.example.com" || cfg.Token != "zoo_y" {
		t.Errorf("the url is added and the token kept: %+v (%v)", cfg, err)
	}
	if raw, _ := os.ReadFile(tokenOnly); !strings.Contains(string(raw), "# mine") {
		t.Errorf("the operator's comment must survive: %s", raw)
	}
}

func TestEnsureURLLeavesAFileItCannotReadAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cli.yaml")
	if err := os.WriteFile(path, []byte("url: [not, a, string\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureURL(path, "https://zoomies.example.com"); err == nil {
		t.Error("a file that does not parse must be reported, not rewritten")
	}
	if raw, _ := os.ReadFile(path); string(raw) != "url: [not, a, string\n" {
		t.Errorf("an unreadable file must be left as it was: %q", raw)
	}
}

func TestPathHonoursTheOverridesInOrder(t *testing.T) {
	t.Setenv("ZOOMIES_CLI_CONFIG", "/x/cli.yaml")
	if Path() != "/x/cli.yaml" {
		t.Errorf("Path = %s", Path())
	}
	t.Setenv("ZOOMIES_CLI_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if Path() != filepath.Join("/xdg", "zoomies", "cli.yaml") {
		t.Errorf("Path = %s", Path())
	}
}

func TestConnectionURLPrefersTheExternalURLAndGuessesNothingForTLS(t *testing.T) {
	for _, tc := range []struct {
		external, bind string
		tls            config.TLSMode
		want           string
	}{
		{"https://zoomies.example.com/", "0.0.0.0:8080", config.TLSFiles, "https://zoomies.example.com"},
		{"", "0.0.0.0:8080", config.TLSOff, "http://127.0.0.1:8080"},
		{"", ":8080", config.TLSOff, "http://127.0.0.1:8080"},
		{"", "127.0.0.1:9000", config.TLSOff, "http://127.0.0.1:9000"},
		{"", "10.0.0.5:8080", config.TLSOff, "http://10.0.0.5:8080"},
		// A certificate would not match an address, and there is no name to use.
		{"", "0.0.0.0:8443", config.TLSSelfSigned, ""},
		{"", "", config.TLSOff, ""},
	} {
		if got := ConnectionURL(tc.external, tc.bind, tc.tls); got != tc.want {
			t.Errorf("ConnectionURL(%q, %q, %s) = %q, want %q", tc.external, tc.bind, tc.tls, got, tc.want)
		}
	}
}

// The fallback reads the file it is given and nothing else: with no file there is
// no controller on this host, and a default listener address must not be invented
// for a machine that has none. Nor does it read this process's environment, which
// says nothing about the service.
func TestInstalledURLReadsOnlyTheFileItIsGiven(t *testing.T) {
	dir := t.TempDir()
	if got := InstalledURL(filepath.Join(dir, "absent.yaml")); got != "" {
		t.Errorf("no file is no controller, got %q", got)
	}

	file := filepath.Join(dir, "zoomies.yaml")
	if err := os.WriteFile(file, []byte("server:\n  bind: 0.0.0.0:8080\n  tls:\n    mode: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZOOMIES_EXTERNAL_URL", "https://from-the-environment.example")
	if got := InstalledURL(file); got != "http://127.0.0.1:8080" {
		t.Errorf("InstalledURL = %q, want the listener on loopback and not the environment's URL", got)
	}

	if err := os.WriteFile(file, []byte("server: [not, a, map\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := InstalledURL(file); got != "" {
		t.Errorf("a file that does not parse names no controller, got %q", got)
	}
}

// A configuration the user may not read is not the same as no configuration: the first
// is fixed with sudo or a group, and the second by installing something.
func TestAnInstalledConfigurationThisUserCannotReadIsToldApartFromNoneAtAll(t *testing.T) {
	dir := t.TempDir()
	if InstalledUnreadable(filepath.Join(dir, "absent.yaml")) {
		t.Error("a file that is not there was reported as unreadable")
	}
	file := filepath.Join(dir, "zoomies.yaml")
	if err := os.WriteFile(file, []byte("server: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if InstalledUnreadable(file) {
		t.Error("a file this user wrote and can read was reported as unreadable")
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads everything, so a permission error cannot be provoked")
	}
	if err := os.Chmod(file, 0); err != nil {
		t.Fatal(err)
	}
	if !InstalledUnreadable(file) {
		t.Error("a file this user may not read was not reported as unreadable")
	}
}

// A host that joined a controller as an agent has a zoomies.yaml as well --
// `zoomies agent join` writes it with embedded off and the controller's address --
// and what it names is the controller it joined, not a listener of its own. Read for
// a listener address instead, it answers with the default one on loopback -- a
// controller that is not there, which is where `zoomies doctor --host` on such a
// host was sent.
func TestInstalledURLOnAnAgentHostIsTheControllerItJoined(t *testing.T) {
	for _, tc := range []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "a controller reached over https",
			yaml: "agent:\n  embedded: false\n  controller_url: https://zoomies.example.com\n",
			want: "https://zoomies.example.com",
		},
		{
			name: "plain http on a private network",
			yaml: "agent:\n  embedded: false\n  controller_url: http://10.0.0.5:8080\n  allow_insecure_http: true\n",
			want: "http://10.0.0.5:8080",
		},
		{
			// The agent dials this over a tunnel of its own; a command typed in a
			// shell has nothing to dial, and a made-up address would be worse than
			// saying so.
			name: "a private connection is not an address a command can dial",
			yaml: "agent:\n  embedded: false\n  controller_url: tailcat://controller\n",
			want: "",
		},
		{
			// An embedded agent ignores controller_url: this host is the controller.
			name: "an embedded agent is the controller's own",
			yaml: "server:\n  external_url: https://zoomies.example.com\nagent:\n  embedded: true\n  controller_url: https://elsewhere.example.com\n",
			want: "https://zoomies.example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "zoomies.yaml")
			if err := os.WriteFile(file, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := InstalledURL(file); got != tc.want {
				t.Errorf("InstalledURL = %q, want %q", got, tc.want)
			}
		})
	}
}
