//go:build !debug

package buildinfo

// Debug is false in release binaries: every logx.Debugf call is dead code and compiled out.
const Debug = false
