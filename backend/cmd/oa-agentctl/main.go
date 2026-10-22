// Command oa-agentctl manages an OA Agent Docker Compose installation: a TUI plus
// non-interactive subcommands. It talks to docker only through argument vectors.
package main

import (
	"os"

	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/ctl/cli"
	"oa-agent/backend/internal/ctl/tui"
)

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func main() {
	// Version/Commit come from the same -ldflags -X variables deploy/linux/build-release.sh sets.
	ver := buildinfo.Version
	if buildinfo.Commit != "unknown" {
		ver += " (" + buildinfo.Commit + ")"
	}
	app := &cli.App{
		Out: os.Stdout, Err: os.Stderr, In: os.Stdin,
		Version: ver,
		IsTTY:   func() bool { return isTTY(os.Stdin) && isTTY(os.Stdout) },
		RunTUI:  tui.Run,
	}
	os.Exit(app.Main(os.Args[1:]))
}
