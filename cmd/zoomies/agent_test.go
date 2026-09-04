package main

import (
	"context"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/installer"
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

func TestServiceChoice(t *testing.T) {
	if got := serviceChoice(true); got != installer.ServiceNone {
		t.Errorf("--no-service gave %q, want %q", got, installer.ServiceNone)
	}
	// An empty kind means "detect one", which is what a plain join should do.
	if got := serviceChoice(false); got != "" {
		t.Errorf("without --no-service the supervisor should be detected, got %q", got)
	}
}
