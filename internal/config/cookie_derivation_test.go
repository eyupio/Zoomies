package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// Load derives security.cookie_secure from the file and the environment alone,
// because the database is not open yet. A controller whose external URL is kept
// in the database (the Settings page, or a config import) then rebuilt over the
// stored rows with the flag already decided, so its session cookies went out
// without Secure even though it was served over https -- and on a loopback bind
// behind a TLS proxy nothing warned about it.
func TestTheSecureCookieFlagIsDerivedFromTheStoredExternalURL(t *testing.T) {
	isolateEnvironment(t)
	key := testKey(t)
	https := []store.InstanceSetting{{Key: "server.external_url", Value: "https://zoomies.example.com"}}

	dir := t.TempDir()
	pinnedOff := filepath.Join(dir, "off.yaml")
	if err := os.WriteFile(pinnedOff, []byte("security:\n  cookie_secure: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		file string
		env  string
		want bool
	}{
		{"nothing says, so the stored https URL decides", "", "", true},
		{"the file says false, and an operator's choice stands", pinnedOff, "", false},
		{"the environment says false, and an operator's choice stands", "", "false", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.env != "" {
				t.Setenv("ZOOMIES_COOKIE_SECURE", tc.env)
			}
			cfg, err := Load(tc.file)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := cfg.Rebuild(https, key); err != nil {
				t.Fatalf("Rebuild: %v", err)
			}
			if got := cfg.CookieSecureValue(); got != tc.want {
				t.Errorf("CookieSecureValue() = %v, want %v", got, tc.want)
			}
		})
	}
}
