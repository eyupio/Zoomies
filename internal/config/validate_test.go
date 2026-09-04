package config

import "testing"

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

func hasCode(fs Findings, code string) bool {
	for _, f := range fs {
		if f.Code == code {
			return true
		}
	}
	return false
}
