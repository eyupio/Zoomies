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

func HelperOptionsFromPointer(string) (HelperOptions, error) {
	return HelperOptions{}, errNoUpdateHelper
}

func RunUpdateHelper(context.Context, HelperOptions) error { return errNoUpdateHelper }
