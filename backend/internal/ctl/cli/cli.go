// Package cli implements the non-interactive oa-agentctl subcommands.
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"oa-agent/backend/internal/ctl/compose"
	"oa-agent/backend/internal/ctl/install"
	"oa-agent/backend/internal/ctl/mounts"
	"oa-agent/backend/internal/ctl/ops"
)

// Exit codes.
const (
	ExitOK      = 0
	ExitFail    = 1
	ExitUsage   = 2
	ExitNoInst  = 3
	ExitRefused = 4 // confirmation missing or declined
	ExitDocker  = 5
)

// App carries every dependency so command flows run against fakes in tests.
type App struct {
	Out, Err io.Writer
	In       io.Reader
	Runner   compose.Runner // nil = real docker
	Prober   mounts.Prober  // nil = real filesystem
	Exec     mounts.Exec    // nil = real cp
	IsTTY    func() bool    // stdin is a terminal
	Getuid   func() int
	Version  string
	// RunTUI starts the interactive UI (wired by main; nil = unavailable).
	RunTUI func(a *App, dir string) error
	// Poll shortens health polling in tests.
	Poll time.Duration
	// HealthTimeout shortens health waits in tests.
	HealthTimeout time.Duration
	HostIP        func() string
	Now           func() time.Time
}

// usageError marks a bad command line.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usage(f string, a ...any) error { return usageError{fmt.Sprintf(f, a...)} }

// refusedError marks a missing or declined confirmation.
type refusedError struct{ msg string }

func (e refusedError) Error() string { return e.msg }

type global struct {
	dir    string
	json   bool
	yes    bool
	dry    bool
	rest   []string
	helpOK bool
}

// parseGlobal pulls the global flags out of args. Everything after "admin" is left alone.
func parseGlobal(args []string) (global, error) {
	var g global
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "admin" && len(g.rest) == 0 {
			g.rest = append(g.rest, args[i:]...)
			break
		}
		switch {
		case a == "--dir":
			if i+1 >= len(args) {
				return g, usage("--dir 需要一个参数")
			}
			g.dir = args[i+1]
			i++
		case strings.HasPrefix(a, "--dir="):
			g.dir = strings.TrimPrefix(a, "--dir=")
		case a == "--json":
			g.json = true
		case a == "--yes" || a == "-y":
			g.yes = true
		case a == "--dry-run":
			g.dry = true
		case a == "-h" || a == "--help":
			g.helpOK = true
		default:
			g.rest = append(g.rest, a)
		}
	}
	return g, nil
}

func takeBool(args *[]string, name string) bool {
	found := false
	out := (*args)[:0:0]
	for _, a := range *args {
		if a == name {
			found = true
			continue
		}
		out = append(out, a)
	}
	*args = out
	return found
}

func takeValue(args *[]string, name string) (string, bool, error) {
	var val string
	found := false
	out := (*args)[:0:0]
	for i := 0; i < len(*args); i++ {
		a := (*args)[i]
		switch {
		case a == name:
			if i+1 >= len(*args) {
				return "", false, usage("%s 需要一个参数", name)
			}
			val, found = (*args)[i+1], true
			i++
		case strings.HasPrefix(a, name+"="):
			val, found = strings.TrimPrefix(a, name+"="), true
		default:
			out = append(out, a)
		}
	}
	*args = out
	return val, found, nil
}

// Main runs one command line and returns the process exit code.
func (a *App) Main(args []string) int {
	g, err := parseGlobal(args)
	if err != nil {
		return a.fail(err)
	}
	if len(g.rest) == 0 {
		if g.helpOK {
			a.printUsage(a.Out)
			return ExitOK
		}
		return a.runInteractive(g)
	}
	cmd, rest := g.rest[0], g.rest[1:]
	if g.helpOK || cmd == "help" {
		a.printUsage(a.Out)
		return ExitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if cmd == "version" {
		return a.fail(a.cmdVersion(ctx, g))
	}
	err = a.dispatch(ctx, g, cmd, rest)
	return a.fail(err)
}

func (a *App) runInteractive(g global) int {
	if a.RunTUI == nil || a.IsTTY == nil || !a.IsTTY() {
		fmt.Fprintln(a.Err, "标准输入/输出不是终端，无法启动交互界面。请使用子命令，例如 oa-agentctl status（oa-agentctl help 查看全部）。")
		return ExitUsage
	}
	return a.fail(a.RunTUI(a, install.ResolveDir(g.dir)))
}

func (a *App) fail(err error) int {
	if err == nil {
		return ExitOK
	}
	var ue usageError
	var re refusedError
	var ee *compose.ExitError
	switch {
	case errors.As(err, &ue):
		fmt.Fprintln(a.Err, "用法错误：", err)
		fmt.Fprintln(a.Err, "运行 oa-agentctl help 查看帮助。")
		return ExitUsage
	case errors.As(err, &re):
		fmt.Fprintln(a.Err, "已拒绝：", err)
		return ExitRefused
	case errors.Is(err, install.ErrNotInstalled):
		fmt.Fprintln(a.Err, "错误：", err, "—— 用 --dir 指定安装目录。")
		return ExitNoInst
	case errors.Is(err, compose.ErrDockerMissing):
		fmt.Fprintln(a.Err, "错误：未找到 docker。")
		return ExitDocker
	case errors.As(err, &ee):
		fmt.Fprintln(a.Err, "错误：", err)
		return ExitDocker
	}
	fmt.Fprintln(a.Err, "错误：", err)
	return ExitFail
}

// ctx bundles a loaded installation for a command.
type ctxt struct {
	g    global
	inst *install.Install
	p    *compose.Project
	env  ops.Env
}

func (a *App) runner(inst *install.Install) compose.Runner {
	if a.Runner != nil {
		return a.Runner
	}
	return compose.ExecRunner{Scrub: scrubber(inst)}
}

func scrubber(inst *install.Install) func(string) string {
	secrets := inst.Env.Secrets()
	return func(s string) string {
		for _, v := range secrets {
			s = strings.ReplaceAll(s, v, "********")
		}
		return s
	}
}

// Open loads the installation and builds the helpers. Used by the TUI too.
func (a *App) Open(dir string) (*Session, error) {
	inst, err := install.Load(install.ResolveDir(dir))
	if err != nil {
		return nil, err
	}
	p := &compose.Project{R: a.runner(inst), Inst: inst, Poll: a.Poll}
	prober := a.Prober
	if prober == nil {
		prober = mounts.OSProber{}
	}
	return &Session{Inst: inst, P: p, Prober: prober, Env: ops.Env{
		P: p, Getuid: a.getuid, Timeout: a.HealthTimeout, Prober: prober, Now: a.Now, BinDir: installedBinDir(),
	}}, nil
}

// Session is a loaded installation plus its helpers (shared with the TUI).
type Session struct {
	Inst   *install.Install
	P      *compose.Project
	Prober mounts.Prober
	Env    ops.Env
}

func (a *App) getuid() int {
	if a.Getuid != nil {
		return a.Getuid()
	}
	return os.Getuid()
}

func (a *App) tty() bool { return a.IsTTY != nil && a.IsTTY() }

func (a *App) open(g global) (*ctxt, error) {
	s, err := a.Open(g.dir)
	if err != nil {
		return nil, err
	}
	return &ctxt{g: g, inst: s.Inst, p: s.P, env: s.Env}, nil
}

func (a *App) printf(f string, args ...any) { fmt.Fprintf(a.Out, f, args...) }

func (a *App) emitJSON(v any) error {
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// confirm asks y/N. Without a terminal it refuses unless --yes was given.
func (a *App) confirm(g global, prompt string) error {
	if g.yes {
		return nil
	}
	if !a.tty() {
		return refusedError{"需要确认「" + prompt + "」，但标准输入不是终端；请加 --yes"}
	}
	fmt.Fprintf(a.Err, "%s [y/N] ", prompt)
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	return refusedError{"已取消"}
}

// typed asks the operator to type a word; never satisfied by --yes.
func (a *App) typed(prompt, word string) error {
	if !a.tty() {
		return refusedError{"该操作需要手动输入确认词，无法在非交互环境完成（见帮助中的 --accept-data-move）"}
	}
	fmt.Fprintf(a.Err, "%s\n请输入 %s 以确认：", prompt, word)
	line, _ := bufio.NewReader(a.In).ReadString('\n')
	if strings.TrimSpace(line) != word {
		return refusedError{"输入不符，已取消"}
	}
	return nil
}

func (a *App) progress() ops.Progress {
	return func(s string) { fmt.Fprintln(a.Out, s) }
}

func (a *App) hostIP() string {
	if a.HostIP != nil {
		return a.HostIP()
	}
	addrs, _ := net.InterfaceAddrs()
	for _, ad := range addrs {
		if ipn, ok := ad.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil {
			return ipn.IP.String()
		}
	}
	return "localhost"
}

// installedBinDir is the directory holding the running oa-agentctl when it is an installed copy (the binary is named
// oa-agentctl, e.g. /usr/local/bin), so "upgrade" can refresh it next to oa-agentctl.sh. "" for go run / test binaries.
func installedBinDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	if filepath.Base(exe) != "oa-agentctl" {
		return ""
	}
	return filepath.Dir(exe)
}
