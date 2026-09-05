package config

import (
	"net"
	"testing"
)

// The web UI does not show bind.public_no_tls, because a fleet behind
// Cloudflare or any other TLS-terminating proxy is configured correctly and
// would carry the warning forever. The CLI still says it.
func TestForUIHidesTheNoTLSWarningButValidateKeepsIt(t *testing.T) {
	c := Default()
	c.Server.Bind = "0.0.0.0:8080"
	c.Server.TLS.Mode = TLSOff

	all := c.Validate()
	if !hasCode(all, "bind.public_no_tls") {
		t.Fatal("Validate dropped bind.public_no_tls; the CLI has to keep saying it")
	}
	if hasCode(all.ForUI(), "bind.public_no_tls") {
		t.Error("ForUI kept bind.public_no_tls, which the UI must not show")
	}

	// Everything else survives: this is a suppression list of one, not a
	// filter that quietly swallows whatever it does not recognise.
	for _, f := range all {
		if f.Code == "bind.public_no_tls" {
			continue
		}
		if !hasCode(all.ForUI(), f.Code) {
			t.Errorf("ForUI dropped %q, which the UI still needs", f.Code)
		}
	}
}

// An empty result must still be a usable slice: the panel renders "nothing
// needs your attention" from a zero-length list, not from nil.
func TestForUIReturnsEmptyNotNil(t *testing.T) {
	if got := (Findings{}).ForUI(); got == nil || len(got) != 0 {
		t.Fatalf("ForUI() = %#v, want an empty non-nil Findings", got)
	}
}

// The token stands for Cloudflare's published ranges, so it must validate as
// cleanly as the CIDRs it replaces.
func TestTrustedProxiesAcceptsTheCloudflareToken(t *testing.T) {
	c := Default()
	c.Server.TrustedProxies = []string{"cloudflare", "10.0.0.0/8"}
	for _, f := range c.Validate() {
		if f.Code == "proxy.bad_cidr" {
			t.Fatalf("the cloudflare token was treated as a bad CIDR: %v", f)
		}
	}
}

// The embedded ranges are a trust boundary written by hand, so the test
// re-checks every one of them parses.
func TestCloudflareCIDRsAllParse(t *testing.T) {
	if len(CloudflareCIDRs) == 0 {
		t.Fatal("no embedded Cloudflare ranges")
	}
	for _, cidr := range CloudflareCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			t.Errorf("embedded range %q does not parse: %v", cidr, err)
		}
	}
}

func TestExpandTrustedProxiesReplacesTheTokenAndKeepsTheRest(t *testing.T) {
	got := ExpandTrustedProxies([]string{"10.0.0.0/8", "cloudflare"})
	if len(got) != 1+len(CloudflareCIDRs) {
		t.Fatalf("expanded to %d entries, want the CIDR plus %d ranges", len(got), len(CloudflareCIDRs))
	}
	if got[0] != "10.0.0.0/8" {
		t.Errorf("the operator's own CIDR must stay in place, got %q", got[0])
	}
	if got[1] != CloudflareCIDRs[0] {
		t.Errorf("the token did not expand in order, got %q", got[1])
	}
}

func hasCode(fs Findings, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}

// A finished runner's container is disk the host never gets back until the
// agent deletes it, so the window has to be a real duration, and a window long
// enough to fill a busy host has to be said out loud rather than found out.
func TestFinishedRetentionIsBoundedInBothDirections(t *testing.T) {
	c := Default()
	c.Agent.Embedded = true
	if f := c.Validate(); hasCode(f, "agent.finished_retention") || hasCode(f, "agent.finished_retention_long") {
		t.Fatalf("the default finished retention drew a finding: %+v", f)
	}

	c.Agent.FinishedRetention = -1
	if f := c.Validate(); !hasCode(f, "agent.finished_retention") {
		t.Fatal("a negative agent.finished_retention passed validation")
	}

	c.Agent.FinishedRetention = maxQuietFinishedRetention + 1
	f := c.Validate()
	if hasCode(f, "agent.finished_retention") {
		t.Fatal("a long agent.finished_retention was reported as an error; it is a warning, not a reason to refuse to start")
	}
	if !hasCode(f, "agent.finished_retention_long") {
		t.Fatal("a day-long agent.finished_retention drew no warning")
	}
}
