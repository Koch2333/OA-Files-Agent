// Package buildinfo reports how the binary was built. Debug is a compile-time constant (build tag "debug"), so debug-only
// code is removed from release binaries. Version and Commit are set with -ldflags "-X".
package buildinfo

import "runtime"

var (
	Version = "dev"
	Commit  = "unknown"
)

// Mode is "debug" or "release".
func Mode() string {
	if Debug {
		return "debug"
	}
	return "release"
}

func String() string {
	return Version + " (" + Commit + ", " + Mode() + ", " + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
