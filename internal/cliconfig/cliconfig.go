// Package cliconfig is ~/.config/zoomies/cli.yaml: where the `zoomies` command on
// a machine finds the controller it talks to, and the credentials of last resort
// for an operator who does not want ZOOMIES_TOKEN in their shell history.
//
// It is a package of its own, not part of the CLI, because the installer writes
// the file as well as the CLI reading it, and two copies of "where is it" are how
// a host ends up with a file its own commands never look at.
package cliconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the file's content.
type Config struct {
	URL      string `yaml:"url"`
	Token    string `yaml:"token"`
	CAFile   string `yaml:"ca_file"`
	Insecure bool   `yaml:"insecure"`
}

// Path is where the file lives.
//
// It is resolved explicitly rather than with os.UserConfigDir so that the path
// is the documented ~/.config/zoomies/cli.yaml on every platform, including
// macOS, where UserConfigDir would answer with an Application Support
// directory no documentation mentions.
func Path() string {
	if p := strings.TrimSpace(os.Getenv("ZOOMIES_CLI_CONFIG")); p != "" {
		return p
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); dir != "" {
		return filepath.Join(dir, "zoomies", "cli.yaml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "zoomies", "cli.yaml")
	}
	return filepath.Join(home, ".config", "zoomies", "cli.yaml")
}

// Load reads the credentials file. A missing file is not an error: most people
// use the environment, and complaining about a file they never created would be
// noise.
func Load(path string) (Config, error) {
	var c Config
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return c, fmt.Errorf("%s: %w (it should hold url, token and optionally ca_file and insecure)", path, err)
	}
	return c, nil
}

// Outcome says what EnsureURL did, for a line in an installer's summary.
type Outcome int

const (
	// Kept: the file already names a controller, which is the operator's choice
	// and is never overwritten.
	Kept Outcome = iota
	// Created: there was no file.
	Created
	// Added: the file existed without a url, and has one now.
	Added
)

const header = `# Where the zoomies command on this machine finds its controller.
# Written by zoomies init and zoomies upgrade; once it names a url it is yours
# and is never overwritten.
#
# Commands that read are fine without more, where the controller allows it; to
# change anything add a token (create one in the UI, under your account's API
# tokens, or with: zoomies tokens create --name this-host --role operator):
#
# token: zoo_...
`

// EnsureURL makes sure the file at path names a controller. It never replaces a
// url that is there, and never writes a credential: the URL is an address, and
// minting a token on an operator's behalf would be authority nobody asked for.
//
// The file is written private (0600 in a 0700 directory) because it is where a
// token is put next, and an operator should not have to remember to tighten it.
func EnsureURL(path, url string) (Outcome, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return Kept, errors.New("no controller URL to write")
	}
	existing, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return Kept, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
		}
		return Created, writeFile(path, []byte(header+"url: "+url+"\n"))
	case err != nil:
		return Kept, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg, err := Load(path)
	if err != nil {
		// A file that does not parse is not ours to rewrite.
		return Kept, err
	}
	if strings.TrimSpace(cfg.URL) != "" {
		return Kept, nil
	}
	// A url line added at the top leaves the operator's comments and token where
	// they were, which re-encoding the document would not.
	return Added, writeFile(path, append([]byte("url: "+url+"\n"), existing...))
}

// writeFile replaces the file in one step, so an interrupted write leaves the
// old one rather than half of the new.
func writeFile(path string, body []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".cli.yaml-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
