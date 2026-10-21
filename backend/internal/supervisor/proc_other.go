//go:build !windows

package supervisor

import "os/exec"

func isWindows() bool { return false }

func hide(cmd *exec.Cmd) {}
