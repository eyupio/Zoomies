// Package version carries build metadata stamped in at link time.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// These are overridden with -ldflags "-X github.com/eyupio/zoomies/internal/version.Version=..."
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

func init() {
	if Commit != "" {
		return
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			Commit = s.Value
		case "vcs.time":
			Date = s.Value
		}
	}
}

// Short returns a compact "v1.2.3 (abc1234)" style identifier.
func Short() string {
	if len(Commit) >= 7 {
		return fmt.Sprintf("%s (%s)", Version, Commit[:7])
	}
	return Version
}

// String returns the full human readable version banner line.
func String() string {
	return fmt.Sprintf("zoomies %s %s/%s %s", Short(), runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// UserAgent is sent on every outbound GitHub API call.
func UserAgent() string { return "zoomies/" + Version }
