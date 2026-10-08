//go:build !unix

package installer

import (
	"context"
	"errors"
	"io"
	"time"
)

// UpdateHelperStateDir is where the helper keeps its state on a host that has
// one; see updatehelper.go.
const UpdateHelperStateDir = "/var/lib/zoomies-update"

// HelperOptions is the unix helper's options, so that the command line builds
// everywhere; nothing here runs.
type HelperOptions struct {
	Dir, StateDir, BinaryPath, LockPath string
	ServiceUID                          int
	Now                                 func() time.Time
	Out                                 io.Writer
}

// errNoUpdateHelper is the answer on a platform the helper does not run on.
var errNoUpdateHelper = errors.New(`the update helper runs where systemd can start it, and not on this platform; update this host by hand with "zoomies upgrade"`)

// CheckUpdateHelperPlatform says the helper cannot run here, before anything
// else is said about the account that asked.
func CheckUpdateHelperPlatform() error { return errNoUpdateHelper }

func HelperOptionsFromPointer(string) (HelperOptions, error) {
	return HelperOptions{}, errNoUpdateHelper
}

func RunUpdateHelper(context.Context, HelperOptions) error { return errNoUpdateHelper }

// The helper's units, named everywhere so that uninstall builds; nothing here
// writes them.
const (
	UpdatePathUnit    = "zoomies-update.path"
	UpdateServiceUnit = "zoomies-update.service"
	systemdUnitDir    = "/etc/systemd/system"
)

// InstallHelperOptions is the unix installer's options, so that the command
// line and uninstall build everywhere; see updatehelper_units.go.
type InstallHelperOptions struct {
	Deployment                      Deployment
	StateDir, ConfigDir, BinaryPath string
	ServiceUID                      int
	Account                         string
	Out                             io.Writer
	unitDir, helperStateDir         string
	run                             commandRunner
}

func ResolveHelperInstall(string) (InstallHelperOptions, error) {
	return InstallHelperOptions{}, errNoUpdateHelper
}

func InstallUpdateHelper(context.Context, InstallHelperOptions) error { return errNoUpdateHelper }

func RemoveUpdateHelper(context.Context, InstallHelperOptions) error { return errNoUpdateHelper }

// removeUpdateHelper has nothing to remove where the helper cannot be
// installed.
func removeUpdateHelper(context.Context, InstallHelperOptions) (removed, left []string, err error) {
	return nil, nil, nil
}
