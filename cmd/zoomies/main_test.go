package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestEnv gives a command somewhere to write and, crucially, a temporary
// HOME.
//
// Every fleet command falls back to ~/.config/zoomies/cli.yaml, so a test run
// on a developer's laptop would otherwise pick up their real controller and
// their real token. Pointing HOME and XDG_CONFIG_HOME at a temporary directory,
// and blanking the two environment variables, is what makes these tests read
// only what they were given.
func newTestEnv(t *testing.T) (*env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("ZOOMIES_CLI_CONFIG", "")
	t.Setenv("ZOOMIES_URL", "")
	t.Setenv("ZOOMIES_TOKEN", "")
	t.Setenv("ZOOMIES_CA_FILE", "")

	var out, errOut bytes.Buffer
	return &env{out: &out, err: &errOut, in: strings.NewReader("")}, &out, &errOut
}

func TestNoArgumentsPrintsHelpAndExitsTwo(t *testing.T) {
	e, out, errOut := newTestEnv(t)

	code := dispatch(context.Background(), e, nil)

	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if out.Len() != 0 {
		t.Errorf("help went to stdout; it should go to stderr when nobody asked for it:\n%s", out)
	}
	for _, want := range []string{"zoomies <command>", "controller", "status", "ZOOMIES_TOKEN"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("help does not mention %q:\n%s", want, errOut)
		}
	}
}

func TestHelpWordExitsZeroOnStdout(t *testing.T) {
	for _, word := range []string{"help", "-h", "--help"} {
		t.Run(word, func(t *testing.T) {
			e, out, _ := newTestEnv(t)
			if code := dispatch(context.Background(), e, []string{word}); code != exitOK {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if !strings.Contains(out.String(), "off the lead") {
				t.Errorf("help not printed to stdout:\n%s", out)
			}
		})
	}
}

func TestUnknownCommandSuggestsTheNearestOne(t *testing.T) {
	e, _, errOut := newTestEnv(t)

	if code := dispatch(context.Background(), e, []string{"runner"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), `Did you mean "runners"?`) {
		t.Errorf("no suggestion for a near miss:\n%s", errOut)
	}
}

func TestGroupWithoutSubcommandListsThem(t *testing.T) {
	e, _, errOut := newTestEnv(t)

	if code := dispatch(context.Background(), e, []string{"pools"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	for _, want := range []string{"list", "create", "disable", "needs a subcommand"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("subcommand list does not mention %q:\n%s", want, errOut)
		}
	}
}

func TestUnknownSubcommandNamesTheAlternatives(t *testing.T) {
	e, _, errOut := newTestEnv(t)

	if code := dispatch(context.Background(), e, []string{"pools", "frobnicate"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "create, delete, disable, edit, enable, get, list") {
		t.Errorf("alternatives not listed:\n%s", errOut)
	}
}

func TestVersion(t *testing.T) {
	t.Run("default", func(t *testing.T) {
		e, out, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"version"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
		if !strings.HasPrefix(out.String(), "zoomies ") {
			t.Errorf("version = %q", out)
		}
	})

	t.Run("short", func(t *testing.T) {
		e, out, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"version", "--short"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
		if strings.Contains(out.String(), "\n\n") || strings.Count(out.String(), "\n") != 1 {
			t.Errorf("--short must be exactly one line, install.sh parses it: %q", out)
		}
	})

	t.Run("json", func(t *testing.T) {
		e, out, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"version", "--json"}); code != exitOK {
			t.Fatalf("exit code = %d", code)
		}
		var fields map[string]string
		if err := json.Unmarshal(out.Bytes(), &fields); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, out)
		}
		for _, key := range []string{"version", "go", "os", "arch"} {
			if _, ok := fields[key]; !ok {
				t.Errorf("missing %q in %v", key, fields)
			}
		}
	})

	t.Run("both flags is a usage error", func(t *testing.T) {
		e, _, _ := newTestEnv(t)
		if code := dispatch(context.Background(), e, []string{"version", "--short", "--json"}); code != exitUsage {
			t.Fatalf("exit code = %d, want %d", code, exitUsage)
		}
	})
}

func TestSubcommandHelpExitsZero(t *testing.T) {
	// Help is not a failure: `zoomies pools list --help | head` must not make a
	// shell script think the command broke.
	for _, args := range [][]string{
		{"pools", "list", "--help"},
		{"runners", "drain", "--help"},
		{"hosts", "join-token", "create", "--help"},
		{"controller", "--help"},
		{"agent", "join", "--help"},
		{"config", "check", "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, args); code != exitOK {
				t.Fatalf("exit code = %d, want 0\n%s", code, errOut)
			}
			if !strings.Contains(errOut.String(), "Usage:") {
				t.Errorf("no usage printed:\n%s", errOut)
			}
		})
	}
}

func TestCommandNameComesFromTheUsageLine(t *testing.T) {
	cases := map[string]string{
		"zoomies pools list [--output table|json|yaml]": "pools list",
		"zoomies runners drain <runner-id>...":          "runners drain",
		"zoomies hosts join-token create [--ttl 15m]":   "hosts join-token create",
		"zoomies controller [--config path]":            "controller",
		"zoomies version [--short|--json]":              "version",
	}
	for usage, want := range cases {
		if got := commandName(usage); got != want {
			t.Errorf("commandName(%q) = %q, want %q", usage, got, want)
		}
	}
}

func TestMissingPositionalArgumentsAreUsageErrors(t *testing.T) {
	// Every one of these would otherwise act on nothing, or on the wrong
	// thing, so each must be refused before a request is made.
	cases := [][]string{
		{"pools", "get"},
		{"pools", "delete"},
		{"runners", "drain"},
		{"runners", "logs"},
		{"jobs", "get"},
		{"hosts", "cordon"},
		{"users", "delete"},
		{"tokens", "revoke"},
		{"installations", "verify"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, args); code != exitUsage {
				t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
			}
			if !strings.Contains(errOut.String(), "needs") {
				t.Errorf("the error does not say what is missing:\n%s", errOut)
			}
		})
	}
}

func TestRequiredFlagsAreUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"pool without labels":  {"pools", "create", "--name", "x", "--installation", "i", "--url", "http://127.0.0.1:1"},
		"pool without name":    {"pools", "create", "--labels", "x", "--installation", "i", "--url", "http://127.0.0.1:1"},
		"token without a name": {"tokens", "create", "--role", "viewer", "--url", "http://127.0.0.1:1"},
		"token with a bad role": {"tokens", "create", "--name", "x", "--role", "wizard",
			"--url", "http://127.0.0.1:1"},
		"user with a bad role": {"users", "create", "--username", "x", "--role", "wizard", "--url", "http://127.0.0.1:1"},
		"healthcheck no url":   {"healthcheck"},
		"agent join no token":  {"agent", "join", "https://example.com"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, args); code != exitUsage {
				t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
			}
		})
	}
}

func TestUnexpectedTrailingArgumentIsRefused(t *testing.T) {
	// A dropped argument is how somebody drains the wrong runner, so a command
	// that takes none must say so rather than ignore it.
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "list", "extra"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), `unexpected argument "extra"`) {
		t.Errorf("the extra argument was not named:\n%s", errOut)
	}
}

func TestInitPrintAnswersWritesAnExampleAndTouchesNothing(t *testing.T) {
	e, out, _ := newTestEnv(t)
	dir := t.TempDir()
	t.Setenv("ZOOMIES_CONFIG_DIR", dir)
	t.Setenv("ZOOMIES_STATE_DIR", dir)

	if code := dispatch(context.Background(), e, []string{"init", "--print-answers"}); code != exitOK {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out.String(), "zoomies answer file") {
		t.Errorf("no answer file printed:\n%s", out)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("--print-answers wrote to the host: %v", entries)
	}
}
