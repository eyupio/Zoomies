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
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/eyupio/zoomies/internal/config"
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

// ConnectionURL is where this host's own CLI should reach a controller with
// these settings.
//
// The external URL is preferred wherever it is set: it is what the operator
// actually reaches, it carries the right scheme and name for a certificate, and
// a container's published port is not something the controller's own settings
// know. Without one, a controller serving plain HTTP is on its listener,
// reached on loopback when that listener is open to every address. A controller
// serving TLS with no external URL has no name its certificate would match, so
// nothing is guessed.
func ConnectionURL(external, bind string, tls config.TLSMode) string {
	if u := strings.TrimRight(strings.TrimSpace(external), "/"); u != "" {
		return u
	}
	if tls != config.TLSOff {
		return ""
	}
	host, port, err := net.SplitHostPort(strings.TrimSpace(bind))
	if err != nil || port == "" {
		return ""
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// InstalledURL is the last resort for a CLI with no URL from anywhere else: the
// controller installed on this host, as its zoomies.yaml names it. It is empty
// when there is no such file, which is what stops a machine with no controller
// being sent to a loopback address nobody is listening on.
//
// It reads the file and nothing else -- not the database, not this process's
// environment -- because the CLI's commands are pure API clients that open no
// database. The cost is that an address changed afterwards on the Settings page
// is not in the file, so the caller says where the URL came from, and the file
// the installer writes (from the settings as they stand) is the accurate one.
func InstalledURL(file string) string {
	if _, err := os.Stat(file); err != nil {
		return ""
	}
	cfg, _, err := config.Effective(file, nil, nil, nil)
	if err != nil {
		return ""
	}
	if !cfg.Agent.Embedded && cfg.Agent.ControllerURL != "" {
		// An agent's file names the controller it joined and runs none of its own.
		// Its server section is only the defaults, and read as a listener it is a
		// controller on loopback that is not there: `zoomies doctor --host` on a
		// host that had joined was sent to an address nothing on it answers.
		return dialable(cfg.Agent.ControllerURL)
	}
	return ConnectionURL(cfg.Server.ExternalURL, cfg.Server.Bind, cfg.Server.TLS.Mode)
}

// InstalledUnreadable reports whether the installed controller's configuration is there
// but this user may not read it. The installer keeps that directory to the service user
// (and its group), so on a host where the controller runs an ordinary login gets a
// permission error from stat, and InstalledURL's empty answer reads the same as "no
// controller is installed here" -- which sends the operator looking for an install that
// is in front of them.
func InstalledUnreadable(file string) bool {
	f, err := os.Open(file)
	if err != nil {
		return errors.Is(err, fs.ErrPermission)
	}
	_ = f.Close()
	return false
}

// dialable is the address if a command typed in a shell can dial it, and nothing
// if it cannot. An agent that joined over a private connection reaches its
// controller through a tunnel of its own, and no URL stands for that.
func dialable(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}
