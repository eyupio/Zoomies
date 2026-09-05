package config

import (
	"os"
	"testing"
)

// The compose file hands the whole .env to the container, and an operator with
// a rootless or remote daemon keeps DOCKER_HOST in that file for docker and
// compose themselves. Read after ZOOMIES_DOCKER_HOST, it silently replaced the
// socket the compose file names explicitly.
func TestZoomiesDockerHostWinsOverDockersOwnVariable(t *testing.T) {
	t.Setenv("ZOOMIES_DOCKER_HOST", "unix:///var/run/docker.sock")
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")

	c := Default()
	if err := c.applyEnv(); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if c.Agent.DockerHost != "unix:///var/run/docker.sock" {
		t.Fatalf("docker host = %q, want the explicit ZOOMIES_DOCKER_HOST", c.Agent.DockerHost)
	}
}

// Without Zoomies' own variable, Docker's is still honoured: it is what a
// developer's shell already has set.
func TestDockerHostIsHonouredWhenZoomiesDockerHostIsUnset(t *testing.T) {
	t.Setenv("ZOOMIES_DOCKER_HOST", "")
	// t.Setenv cannot unset, so clear it the long way round.
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	unsetenv(t, "ZOOMIES_DOCKER_HOST")

	c := Default()
	if err := c.applyEnv(); err != nil {
		t.Fatalf("applyEnv: %v", err)
	}
	if c.Agent.DockerHost != "unix:///run/user/1000/docker.sock" {
		t.Fatalf("docker host = %q, want DOCKER_HOST", c.Agent.DockerHost)
	}
}

// unsetenv removes a variable for the rest of the test, restoring whatever was
// there before -- t.Setenv can only set.
func unsetenv(t *testing.T, key string) {
	t.Helper()
	if was, ok := os.LookupEnv(key); ok {
		t.Cleanup(func() { _ = os.Setenv(key, was) })
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetting %s: %v", key, err)
	}
}
