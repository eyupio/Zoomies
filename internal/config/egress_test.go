package config

import (
	"strings"
	"testing"
)

// A wrong list here is a hole: every address below is somewhere a request
// from the controller reaches this machine or a network behind it, and each
// is written the way someone probing for one would write it.
func TestAnOutboundURLNamingThisMachineOrAPrivateNetworkIsRefused(t *testing.T) {
	for _, raw := range []string{
		// Loopback, both families, and the names that can only be local.
		"http://127.0.0.1:8080", "https://127.8.9.10", "http://[::1]:9000",
		"http://localhost", "https://LOCALHOST:443", "http://localhost.", "http://idp.localhost",
		"http://localhost.localdomain", "http://ip6-localhost",
		// Unspecified, which Linux dials as loopback.
		"http://0.0.0.0:8080", "http://0.1.2.3", "http://[::]:80",
		// Link-local, and the metadata service by address and by name.
		"http://169.254.169.254/latest/meta-data/", "http://169.254.1.1", "http://[fe80::1]",
		"http://[fe80::1%25eth0]:8080", "http://metadata.google.internal",
		// RFC 1918, CGNAT and ULA (fd00:ec2::254 is a cloud metadata address).
		"https://10.0.0.1", "https://172.16.0.1", "https://172.31.255.255", "https://192.168.1.20:8006",
		"https://100.64.0.1", "https://100.127.255.254", "https://[fc00::1]", "https://[fd00:ec2::254]",
		// IPv4 inside IPv6: mapped, compatible, NAT64 and 6to4.
		"http://[::ffff:10.0.0.1]", "http://[::ffff:127.0.0.1]", "http://[::ffff:169.254.169.254]",
		"http://[::ffff:100.64.0.1]", "http://[::ffff:0.1.2.3]",
		"http://[::127.0.0.1]", "http://[64:ff9b::a00:1]", "http://[2002:a00:1::]",
		// Deprecated site-local, reserved, broadcast and multicast.
		"http://[fec0::1]", "http://240.0.0.1", "http://255.255.255.255", "http://224.0.0.1", "http://[ff02::1]",
		"http://198.18.0.1",
		// Addresses in spellings netip does not read but other parsers do.
		"http://2130706433", "http://0x7f000001", "http://127.1", "http://0177.0.0.1",
		// A bare host and port, which the Proxmox driver accepts, and a
		// user-info prefix that does not change where the request goes.
		"10.0.0.5:8006", "https://public.example.com@10.0.0.1/",
	} {
		f := CheckOutboundURL("oidc.issuer", raw, false)
		if f == nil {
			t.Errorf("%s was admitted", raw)
			continue
		}
		if f.Code != "egress.private_target" || f.Severity != SeverityError || f.Setting != "oidc.issuer" {
			t.Errorf("%s: got %s/%s on %q, want an egress.private_target error on oidc.issuer", raw, f.Code, f.Severity, f.Setting)
		}
		if !strings.Contains(f.Fix, "security.allow_private_egress") {
			t.Errorf("%s: the fix does not name the setting that would allow it: %q", raw, f.Fix)
		}
	}
}

// The guard is worth nothing if it refuses the ordinary case, so the public
// internet -- including addresses that sit just outside each private range --
// and host names it cannot see through are admitted.
func TestAnOutboundURLOnThePublicInternetIsAdmitted(t *testing.T) {
	for _, raw := range []string{
		"https://api.github.com", "https://ghes.example.com/api/v3", "https://login.example.org/realms/x",
		"https://8.8.8.8", "https://172.15.255.255", "https://172.32.0.1", "https://100.63.255.255",
		"https://100.128.0.1", "https://169.253.255.255", "https://11.0.0.1", "https://[2001:db8::1]",
		"https://[2606:4700::1111]", "https://[::ffff:8.8.8.8]", "https://[64:ff9b::808:808]",
		"https://[2002:808:808::]",
		// A name is not resolved, so one that merely contains a number is a
		// name: the check reads, it does not guess.
		"https://10.example.com", "https://s3.eu-west-2.amazonaws.com", "https://host-127-0-0-1.example",
		// Nothing to check: each caller has its own finding for these.
		"", "   ",
	} {
		if f := CheckOutboundURL("oidc.issuer", raw, false); f != nil {
			t.Errorf("%q was refused: %s", raw, f.Title)
		}
	}
}

// On a single-team instance whose identity provider is on the LAN, the one
// key is the whole answer.
func TestAllowingPrivateEgressAdmitsEveryPrivateAddress(t *testing.T) {
	for _, raw := range []string{"https://10.0.0.1", "http://127.0.0.1:5556", "http://169.254.169.254"} {
		if f := CheckOutboundURL("oidc.issuer", raw, true); f != nil {
			t.Errorf("%s was refused with the setting on: %s", raw, f.Title)
		}
	}
}

// An install that has talked to a LAN identity provider since before the
// guard existed must still start after an upgrade: the file is the process
// owner's, so the validator warns, naming the setting and the key, and never
// errs. The refusal belongs to writes through the API.
func TestAPrivateRangeIssuerInTheFileWarnsAndNeverStopsStartup(t *testing.T) {
	c := Default()
	c.OIDC.Enabled = true
	c.OIDC.Issuer = "https://192.168.10.4/realms/fleet"
	c.OIDC.ClientID = "zoomies"
	c.Server.ExternalURL = "https://zoomies.example.com"

	fs := c.Validate()
	if errs := fs.Errors(); len(errs) != 0 {
		t.Fatalf("a LAN issuer stopped startup: %v", errs)
	}
	f := findingFor(fs, "egress.private_target")
	if f == nil {
		t.Fatal("a private-range issuer was not warned about")
	}
	if f.Severity != SeverityWarning || f.Setting != "oidc.issuer" || !strings.Contains(f.Title, "192.168.10.4") {
		t.Errorf("the warning does not name the issuer: %+v", *f)
	}
	if !strings.Contains(f.Fix, "security.allow_private_egress") {
		t.Errorf("the warning does not name the setting that quiets it: %q", f.Fix)
	}

	c.Security.AllowPrivateEgress = true
	if f := findingFor(c.Validate(), "egress.private_target"); f != nil {
		t.Errorf("the setting did not admit the issuer: %s", f.Title)
	}
}

// The switch removes the only guard between an administrator's typed URL and
// this machine's neighbourhood, so a controller running with it on must say
// so where an operator looks -- and stay quiet with it off, or the warning
// means nothing. It never stops startup: a LAN install that needs it is the
// expected case, and the finding is there to make the choice visible.
func TestAllowingPrivateEgressIsNamedByAWarningAndNeverStopsStartup(t *testing.T) {
	c := Default()
	if f := findingFor(c.Validate(), "egress.private_allowed"); f != nil {
		t.Fatalf("the default configuration raised %s", f.Code)
	}

	c.Security.AllowPrivateEgress = true
	fs := c.Validate()
	if errs := fs.Errors(); len(errs) != 0 {
		t.Fatalf("the switch stopped startup: %v", errs)
	}
	f := findingFor(fs, "egress.private_allowed")
	if f == nil {
		t.Fatal("turning the switch on raised no finding")
	}
	if f.Severity != SeverityWarning || f.Setting != AllowPrivateEgressSetting {
		t.Errorf("the finding is not a warning on %s: %+v", AllowPrivateEgressSetting, *f)
	}
	if !strings.Contains(f.Detail, "169.254.169.254") || f.Fix == "" {
		t.Errorf("the finding does not say what is accepted or what to do: %+v", *f)
	}
}

// Each of the settings the controller dials is checked, as a warning, and an
// issuer that is switched off is not: nothing dials it.
func TestEveryOutboundSettingIsCheckedByTheValidator(t *testing.T) {
	for _, tc := range []struct {
		setting string
		set     func(c *Config)
	}{
		{"github.api_base_url", func(c *Config) { c.GitHub.APIBaseURL = "http://10.1.2.3/api/v3" }},
		{"capacity_demand.destination_url", func(c *Config) { c.CapacityDemand.DestinationURL = "http://169.254.169.254/" }},
		{"agent.runner_download_url", func(c *Config) { c.Agent.RunnerDownloadURL = "http://[::1]:8000/runner" }},
		{"oidc.issuer", func(c *Config) { c.OIDC.Enabled = true; c.OIDC.Issuer = "http://localhost:5556" }},
		{"backup.remotes", func(c *Config) {
			c.Backup.Remotes = []BackupRemote{{Name: "minio", Endpoint: "http://127.0.0.1:9000", Bucket: "b"}}
		}},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			c := Default()
			tc.set(c)
			f := findingFor(c.Validate(), "egress.private_target")
			if f == nil || f.Setting != tc.setting || f.Severity != SeverityWarning {
				t.Fatalf("got %+v, want an egress.private_target warning on %s", f, tc.setting)
			}
		})
	}

	c := Default()
	c.OIDC.Issuer = "http://localhost:5556"
	if f := findingFor(c.Validate(), "egress.private_target"); f != nil {
		t.Errorf("an issuer nothing dials was warned about: %s", f.Title)
	}
}

// A backup remote is one entry in a list, so its refusal has to say which.
func TestAPrivateBackupRemoteIsNamedInItsRefusal(t *testing.T) {
	c := Default()
	c.Backup.Remotes = []BackupRemote{{Name: "minio", Endpoint: "http://192.168.1.9:9000", Bucket: "b"}}
	f := findingFor(c.Validate(), "egress.private_target")
	if f == nil || !strings.Contains(f.Title, "minio") || !strings.Contains(f.Title, "192.168.1.9") {
		t.Fatalf("the refusal does not name the remote and its address: %+v", f)
	}
}

// Platform-scoped, like everything that changes what the process dials from
// its own machine, and reachable from the environment like every key.
func TestAllowPrivateEgressIsAPlatformSettingWithAnEnvironmentOverride(t *testing.T) {
	s, ok := LookupSetting(AllowPrivateEgressSetting)
	if !ok {
		t.Fatalf("%s is not in the registry", AllowPrivateEgressSetting)
	}
	if s.Scope != ScopePlatform || s.Env != "ZOOMIES_ALLOW_PRIVATE_EGRESS" {
		t.Errorf("got scope %s and env %s", s.Scope, s.Env)
	}
	t.Setenv("ZOOMIES_ALLOW_PRIVATE_EGRESS", "true")
	c := Default()
	if err := c.applyEnv(); err != nil {
		t.Fatal(err)
	}
	if !c.Security.AllowPrivateEgress {
		t.Error("the environment override did not set it")
	}
}

func findingFor(fs Findings, code string) *Finding {
	for i := range fs {
		if fs[i].Code == code {
			return &fs[i]
		}
	}
	return nil
}

// A local model is the normal case for the assistant, so its provider URL
// has its own switch rather than borrowing the blanket one: the refusal
// names it, and the blanket switch is not consulted.
func TestCheckProviderURLNamesTheAssistantSwitch(t *testing.T) {
	f := CheckProviderURL("http://127.0.0.1:11434", false)
	if f == nil || f.Code != "egress.private_target" {
		t.Fatalf("finding %+v", f)
	}
	if !strings.Contains(f.Fix, AllowPrivateProviderSetting) {
		t.Errorf("fix %q does not name %s", f.Fix, AllowPrivateProviderSetting)
	}
	if f := CheckProviderURL("http://127.0.0.1:11434", true); f != nil {
		t.Errorf("allowed, got %+v", f)
	}
	for _, allow := range []bool{false, true} {
		if f := CheckProviderURL("https://api.openai.com/v1", allow); f != nil {
			t.Errorf("public URL with allow=%v: %+v", allow, f)
		}
	}
}

func TestTheAssistantSwitchesArePlatformSettingsWithEnvironmentOverrides(t *testing.T) {
	for key, env := range map[string]string{
		AllowPrivateProviderSetting: "ZOOMIES_ASSISTANT_ALLOW_PRIVATE_PROVIDER",
		LocalOnlySetting:            "ZOOMIES_ASSISTANT_LOCAL_ONLY",
	} {
		s, ok := LookupSetting(key)
		if !ok {
			t.Fatalf("%s is not in the registry", key)
		}
		if s.Scope != ScopePlatform || s.Env != env || !s.Live {
			t.Errorf("%s: scope %s env %s live %v", key, s.Scope, s.Env, s.Live)
		}
	}
	t.Setenv("ZOOMIES_ASSISTANT_ALLOW_PRIVATE_PROVIDER", "true")
	t.Setenv("ZOOMIES_ASSISTANT_LOCAL_ONLY", "true")
	c := Default()
	if err := c.applyEnv(); err != nil {
		t.Fatal(err)
	}
	if !c.Assistant.AllowPrivateProvider || !c.Assistant.LocalOnly {
		t.Errorf("environment overrides did not set the fields: %+v", c.Assistant)
	}
}
