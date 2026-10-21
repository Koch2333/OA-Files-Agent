//go:build windows

package supervisor

import (
	"os/exec"
	"syscall"
)

func isWindows() bool { return true }

// hide stops helper processes from flashing console windows.
func hide(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}
