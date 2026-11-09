// Package buildinfo identifies the source used for a CLI build.
package buildinfo

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Release builds set these values with -ldflags -X.
var Version = "dev"
var Commit = "unknown"

func String() string {
	commit := Commit
	dirty := false
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && commit == "unknown" {
				commit = setting.Value
			}
			if setting.Key == "vcs.modified" {
				dirty = setting.Value == "true"
			}
		}
	}
	if len(commit) > 12 {
		commit = commit[:12]
	}
	if dirty {
		commit += "+modified"
	}
	return fmt.Sprintf("%s (%s; %s/%s; %s)", Version, commit, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
