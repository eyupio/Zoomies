package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
)

func TestAgentNeedsAControllerToTalkTo(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	isolateHost(t)
	path := writeConfig(t, "agent:\n  embedded: false\n")

	if code := dispatch(context.Background(), e, []string{"agent", "--config", path}); code != exitError {
		t.Fatalf("exit code = %d, want %d", code, exitError)
	}
	for _, want := range []string{"agent.controller_url", "--controller", "ZOOMIES_CONTROLLER_URL"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the error does not mention %q:\n%s", want, errOut)
		}
	}
}

func TestAgentJoinRejectsAnUnknownBackendBeforeTouchingTheHost(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	isolateHost(t)

	code := dispatch(context.Background(), e, []string{
		"agent", "join", "https://zoomies.example.com", "--token", "zoojoin_x", "--backend", "wibble",
	})
	if code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "docker, podman or process") {
		t.Errorf("the error does not list the backends:\n%s", errOut)
	}
}

func TestAgentJoinNeedsAControllerURL(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	isolateHost(t)

	if code := dispatch(context.Background(), e, []string{"agent", "join", "--token", "zoojoin_x"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errOut.String(), "controller URL") {
		t.Errorf("the error does not say what is missing:\n%s", errOut)
	}
}

// A host enrolled through a private connection has no local way to invent a
// tunnel address, so a missing or incomplete credentials file has to fail
// with one message naming the state file and pointing at the UI -- not the
// low-level "no private connection address" error a half-built transport
// used to produce before anything checked the credentials existed at all.
func TestAgentWithNoCredentialsExplainsHowToRejoinAPrivateConnection(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	dir := isolateHost(t)
	path := writeConfig(t, "agent:\n  embedded: false\n  controller_url: tailcat://controller\n  work_dir: "+filepath.Join(dir, "work")+"\n")

	if code := dispatch(context.Background(), e, []string{"agent", "--config", path}); code != exitError {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitError, errOut)
	}
	for _, want := range []string{"this host cannot start", "Add a host", "Private connection"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the error does not mention %q:\n%s", want, errOut)
		}
	}
	if strings.Contains(errOut.String(), "no private connection address; re-join") {
		t.Errorf("the low-level transport error leaked through instead of the daemon's own check:\n%s", errOut)
	}
}

// The same failure, but with a credentials file that exists and carries a
// host ID and agent token -- just not the tunnel address a private
// connection needs. This is what a truncated or partially restored agent.json
// looks like, and the daemon should name that specifically rather than
// reporting the host as simply unjoined.
func TestAgentWithATailcatHostMissingItsAddressNamesTheStateFile(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	dir := isolateHost(t)
	workDir := filepath.Join(dir, "work")
	if err := os.MkdirAll(workDir, 0o700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(workDir, "agent.json")
	if err := os.WriteFile(statePath, []byte(`{"host_id":"host_x","agent_token":"tok","controller_url":"tailcat://controller"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeConfig(t, "agent:\n  embedded: false\n  controller_url: tailcat://controller\n  work_dir: "+workDir+"\n")

	if code := dispatch(context.Background(), e, []string{"agent", "--config", path}); code != exitError {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitError, errOut)
	}
	for _, want := range []string{statePath, "no private connection address", "Add a host"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the error does not mention %q:\n%s", want, errOut)
		}
	}
}

func TestServiceChoice(t *testing.T) {
	if got := serviceChoice(true); got != installer.ServiceNone {
		t.Errorf("--no-service gave %q, want %q", got, installer.ServiceNone)
	}
	// An empty kind means "detect one", which is what a plain join should do.
	if got := serviceChoice(false); got != "" {
		t.Errorf("without --no-service the supervisor should be detected, got %q", got)
	}
}

// `agent install` takes no positional argument: the machine it prepares is
// the one it runs on, and a stray word is more likely a join command typed on
// the wrong line than anything this should guess at.
func TestAgentInstallTakesNoArguments(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"agent", "install", "https://zoomies.example.com"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut.String(), "unexpected argument") {
		t.Errorf("the error does not name the stray argument:\n%s", errOut)
	}
}

// A standalone agent finds the helper's folder the way the installer recorded
// it, beside the configuration file. Where the helper's systemd units cannot
// run there is no folder to find, whatever a stray pointer says, so the agent
// never offers to update itself there.
func TestAnAgentFindsItsUpdateFolderOnlyWhereTheHelperCanRun(t *testing.T) {
	cfgDir := t.TempDir()
	folder := filepath.Join(t.TempDir(), "update")
	pointer, err := json.Marshal(channel.Pointer{V: updates.WireVersion, Dir: folder, ConfigDir: cfgDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, channel.PointerFile), pointer, 0o600); err != nil {
		t.Fatal(err)
	}

	dir, ok := agentUpdateDir(filepath.Join(cfgDir, "zoomies.yaml"))()
	if runtime.GOOS != "linux" {
		if ok {
			t.Fatalf("on %s the agent found the update folder %s, where no helper can run", runtime.GOOS, dir)
		}
		return
	}
	if !ok || dir != folder {
		t.Fatalf("the agent found %q (ok %v), want the folder the pointer names, %s", dir, ok, folder)
	}
}
