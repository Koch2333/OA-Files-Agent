//go:build windows

package fileops

import (
	"os/exec"
	"syscall"
)

func revealSupported() bool { return true }

// launchExplorer runs `explorer.exe /select,"<file>"` or `explorer.exe "<folder>"`. The command line is built
// by hand because explorer parses /select,"..." itself; abs was validated to contain no quotes or control characters.
// Explorer exits with status 1 even on success, so the process is only reaped, never judged.
func launchExplorer(abs string, selectFile bool) error {
	cmd := exec.Command("explorer.exe")
	line := `explorer.exe "` + abs + `"`
	if selectFile {
		line = `explorer.exe /select,"` + abs + `"`
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
