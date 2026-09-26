package config

import (
	"strings"
	"testing"
)

// The status projection is a new way to read a fleet fact without an
// account, so the default serves none of it: an existing installation that
// upgrades gains no route anybody can reach.
func TestTheStatusProjectionIsOffByDefault(t *testing.T) {
	if got := Default().Status.Mode; got != StatusOff {
		t.Fatalf("status.mode defaults to %q, want off", got)
	}
	for _, f := range Default().Validate() {
		if strings.HasPrefix(f.Code, "status.") {
			t.Errorf("the default configuration raised %s: %s", f.Code, f.Title)
		}
	}
}

// Each mode says what it costs, and only public costs anything worth a
// warning. Public on an address strangers can reach, with nothing
// terminating TLS, is refused outright and the refusal names the settings
// that fix it -- a warning that leaves the listener open to exactly the
// people it invites would be the silent dangerous toggle the validator
// exists to prevent.
func TestTheStatusModeRaisesWhatItCosts(t *testing.T) {
	cases := []struct {
		name     string
		mode     StatusMode
		bind     string
		tls      TLSMode
		code     string
		severity Severity
		mentions []string
	}{
		{"authenticated raises nothing", StatusAuthenticated, "0.0.0.0:8080", TLSOff, "", "", nil},
		{"public on loopback is a warning", StatusPublic, "127.0.0.1:8080", TLSOff, "status.public", SeverityWarning, []string{"/api/v1/status", "No pool, host, repository"}},
		{"public with TLS on a public bind is a warning", StatusPublic, "0.0.0.0:8443", TLSSelfSigned, "status.public", SeverityWarning, nil},
		{"public on a public bind without TLS is refused", StatusPublic, "0.0.0.0:8080", TLSOff, "status.public_no_tls", SeverityError, []string{"server.tls.mode", "server.bind", "status.mode"}},
		{"a mode that is not one is refused", StatusMode("everyone"), "127.0.0.1:8080", TLSOff, "status.mode", SeverityError, []string{"off, authenticated or public"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Status.Mode = tc.mode
			c.Server.Bind = tc.bind
			c.Server.TLS.Mode = tc.tls
			var got []Finding
			for _, f := range c.Validate() {
				if strings.HasPrefix(f.Code, "status.") {
					got = append(got, f)
				}
			}
			if tc.code == "" {
				if len(got) != 0 {
					t.Fatalf("raised %v, want nothing", got)
				}
				return
			}
			if len(got) != 1 || got[0].Code != tc.code || got[0].Severity != tc.severity {
				t.Fatalf("raised %v, want exactly %s at %s", got, tc.code, tc.severity)
			}
			text := got[0].Title + " " + got[0].Detail + " " + got[0].Fix
			for _, want := range tc.mentions {
				if !strings.Contains(text, want) {
					t.Errorf("the finding does not mention %q: %s", want, text)
				}
			}
		})
	}
}

// The environment is how a container deployment turns it on.
func TestTheStatusModeIsReadFromTheEnvironment(t *testing.T) {
	c := Default()
	env := map[string]string{"ZOOMIES_STATUS_MODE": "Public"}
	if err := c.applyEnvFrom(func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if c.Status.Mode != StatusPublic {
		t.Fatalf("ZOOMIES_STATUS_MODE=Public gave %q", c.Status.Mode)
	}
}
