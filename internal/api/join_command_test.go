package api

import (
	"os/exec"
	"strings"
	"testing"
)

func TestJoinCommandsKeepURLsAndCredentialsLiteral(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("a POSIX shell is not installed")
	}
	h := newHarness(t)
	for _, address := range []string{
		"https://zoomies.example/?first=1&second=2",
		"https://zoomies.example/$(printf injected)",
		"https://zoomies.example/it's-here",
	} {
		if msg := checkControllerURL(address); msg != "" {
			t.Fatal(msg)
		}
		const token = "join-token"
		// Neither curl nor the installer runs: these functions expose only
		// how the generated command is parsed into arguments by a real shell.
		script := "curl() { :; }; sh() { printf '%s\\n' \"$@\"; }; " + h.api.joinCommand(token, address)
		out, err := exec.CommandContext(t.Context(), shell, "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("shell: %v: %s", err, out)
		}
		want := "\n--controller\n" + address + "\n--join-token\n" + token + "\n"
		if !strings.Contains(string(out), want) {
			t.Fatalf("URL %q changed during shell parsing: %s", address, out)
		}
	}
}

func TestPowershellArgumentKeepsHostileValuesLiteral(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"plain", `'plain'`},
		{"it's", `'it''s'`},
		{"$(Get-Process)", `'$(Get-Process)'`},
		{"a`b\"c", "'a`b\"c'"},
		{"two\r\nlines", `'twolines'`},
		{"'; calc; '", `'''; calc; '''`},
	} {
		if got := powershellArgument(tc.in); got != tc.want {
			t.Errorf("powershellArgument(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

// The Windows command is built by the same path as the Linux one so that the
// channel pin, the placeholder for a local external URL and the Tailcat
// address cannot drift between the two operating systems.
func TestWindowsJoinCommandCarriesTheSameArgumentsAsTheLinuxOne(t *testing.T) {
	h := newHarness(t)
	for _, address := range []string{
		"https://zoomies.example/it's-here",
		"tailcat://abc.tailcat.example:443",
		"",
	} {
		linux := h.api.joinCommand("zoojoin_x'y", address)
		windows := h.api.joinCommandWindows("zoojoin_x'y", address)
		controller := h.api.joinControllerURL(address)
		prefix := "& ([scriptblock]::Create((irm https://zoomies.sh/install.ps1))) -Mode agent -Controller " +
			powershellArgument(controller) + " -JoinToken 'zoojoin_x''y'"
		if !strings.HasPrefix(windows, prefix) {
			t.Fatalf("windows command for %q = %s, want prefix %s", address, windows, prefix)
		}
		if strings.Contains(windows, "-Yes") {
			t.Fatalf("the generated command must leave the confirmation to the person: %s", windows)
		}
		if strings.Contains(linux, "--version") != strings.Contains(windows, "-Version") {
			t.Fatalf("channel pin differs:\n%s\n%s", linux, windows)
		}
	}
}
