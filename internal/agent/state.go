package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrNotJoined reports that this host has no usable credentials yet. Callers
// distinguish it from a genuine failure so that "not joined" can be an
// instruction to the operator rather than a crash loop.
var ErrNotJoined = errors.New("agent: this host has not joined a controller")

// StateFile is the name of the credentials file inside the agent's work
// directory.
const StateFile = "agent.json"

// StatePath returns where an agent with this work directory keeps its
// credentials. It lives beside the runner scratch space so that an operator
// backing up or wiping one host touches exactly one directory.
func StatePath(workDir string) string { return filepath.Join(workDir, StateFile) }

// Load reads the credentials a previous Join persisted.
//
// The file holds a bearer token that speaks for the whole host, so a file any
// other local user can read is refused rather than used: on a shared build host
// that token is the interesting thing to steal. A missing or incomplete file is
// reported as ErrNotJoined.
func Load(path string) (Credentials, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, fmt.Errorf("%w: no credentials at %s; run `zoomies agent join <controller-url> --token <join-token>` on this host first: %w", ErrNotJoined, path, err)
		}
		return Credentials{}, fmt.Errorf("agent: reading credentials %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Credentials{}, fmt.Errorf("agent: %s is not a regular file; agent credentials must be a JSON file written by `zoomies agent join`", path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return Credentials{}, fmt.Errorf("agent: refusing to read %s: it is mode %04o, so other users on this host can read this agent's token; run: chmod 600 %s", path, perm, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("agent: reading credentials %s: %w", path, err)
	}
	var c Credentials
	if err := json.Unmarshal(raw, &c); err != nil {
		return Credentials{}, fmt.Errorf("agent: %s is not valid JSON; delete it and re-join this host with a fresh join token: %w", path, err)
	}
	if !c.Valid() {
		return Credentials{}, fmt.Errorf("%w: %s has no host ID or agent token; delete it and run `zoomies agent join <controller-url> --token <join-token>` again", ErrNotJoined, path)
	}
	return c, nil
}

// Save persists credentials for the next run of the agent.
//
// The write is to a temporary file followed by a rename, because a half-written
// credentials file after a crash would look like a valid identity that the
// controller has never heard of, and the operator would have no way to tell
// that from a revoked one.
func Save(path string, c Credentials) error {
	if !c.Valid() {
		return errors.New("agent: refusing to save credentials without a host ID and agent token; the controller's join response was incomplete")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("agent: creating agent state directory %s: %w", dir, err)
	}

	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("agent: encoding credentials for %s: %w", path, err)
	}
	raw = append(raw, '\n')

	tmp, err := os.CreateTemp(dir, ".agent-*.json")
	if err != nil {
		return fmt.Errorf("agent: creating a temporary file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("agent: setting permissions on %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return fmt.Errorf("agent: writing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("agent: closing %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("agent: installing credentials at %s: %w", path, err)
	}
	return nil
}
