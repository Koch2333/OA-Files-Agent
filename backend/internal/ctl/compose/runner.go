// Package compose runs docker / docker compose through an interface so every command and TUI
// screen can be tested with a fake. Commands are always argument vectors, never shell strings.
package compose

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Spec describes one docker invocation.
type Spec struct {
	Args  []string // arguments after the docker binary, e.g. ["compose","ps"]
	Dir   string
	Env   []string // extra KEY=VALUE entries appended to the process environment
	Stdin []byte
}

// String renders the call for logs. Env is deliberately omitted (it can hold secrets).
func (s Spec) String() string { return "docker " + strings.Join(s.Args, " ") }

type Result struct {
	Stdout, Stderr string
	ExitCode       int
}

// ExitError is returned for a non-zero exit; Result is still filled.
type ExitError struct {
	Spec   Spec
	Result Result
}

func (e *ExitError) Error() string {
	msg := strings.TrimSpace(e.Result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(e.Result.Stdout)
	}
	if len(msg) > 600 {
		msg = msg[:600] + "…"
	}
	return fmt.Sprintf("%s: exit %d: %s", e.Spec, e.Result.ExitCode, msg)
}

// Runner executes docker commands.
type Runner interface {
	// Run captures output.
	Run(ctx context.Context, s Spec) (Result, error)
	// Stream calls line for every output line (stdout and stderr interleaved) until the command ends.
	Stream(ctx context.Context, s Spec, line func(string)) error
	// Attach wires the command to the caller's terminal streams (interactive oa-admin).
	Attach(ctx context.Context, s Spec, in io.Reader, out, errw io.Writer) error
}

// ErrDockerMissing is returned when the docker binary cannot be found.
var ErrDockerMissing = errors.New("docker not found in PATH")

// ExecRunner is the production Runner.
type ExecRunner struct {
	Bin string // default "docker"
	// Scrub removes secrets from captured output before it is returned.
	Scrub func(string) string
}

func (r ExecRunner) cmd(ctx context.Context, s Spec) *exec.Cmd {
	bin := r.Bin
	if bin == "" {
		bin = "docker"
	}
	c := exec.CommandContext(ctx, bin, s.Args...)
	c.Dir = s.Dir
	c.Env = append(os.Environ(), s.Env...)
	return c
}

func (r ExecRunner) scrub(s string) string {
	if r.Scrub != nil {
		return r.Scrub(s)
	}
	return s
}

func wrapStart(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return ErrDockerMissing
	}
	return err
}

func (r ExecRunner) Run(ctx context.Context, s Spec) (Result, error) {
	c := r.cmd(ctx, s)
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	if s.Stdin != nil {
		c.Stdin = bytes.NewReader(s.Stdin)
	}
	err := c.Run()
	res := Result{Stdout: r.scrub(so.String()), Stderr: r.scrub(se.String())}
	if err == nil {
		return res, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		res.ExitCode = ee.ExitCode()
		return res, &ExitError{Spec: s, Result: res}
	}
	return res, wrapStart(err)
}

func (r ExecRunner) Stream(ctx context.Context, s Spec, line func(string)) error {
	c := r.cmd(ctx, s)
	pr, pw := io.Pipe()
	c.Stdout, c.Stderr = pw, pw
	if err := c.Start(); err != nil {
		return wrapStart(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line(r.scrub(sc.Text()))
		}
	}()
	err := c.Wait()
	pw.Close()
	<-done
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Spec: s, Result: Result{ExitCode: ee.ExitCode()}}
	}
	return err
}

func (r ExecRunner) Attach(ctx context.Context, s Spec, in io.Reader, out, errw io.Writer) error {
	c := r.cmd(ctx, s)
	c.Stdin, c.Stdout, c.Stderr = in, out, errw
	err := c.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return &ExitError{Spec: s, Result: Result{ExitCode: ee.ExitCode()}}
	}
	return wrapStart(err)
}

// Rule makes Fake answer calls whose joined arguments contain Match.
type Rule struct {
	Match  string
	Result Result
	Err    error
	Times  int // 0 = unlimited; otherwise the rule is skipped after that many hits
	hits   int
}

// Fake records calls and returns canned results; the first matching rule wins.
type Fake struct {
	mu    sync.Mutex
	Calls []Spec
	Rules []*Rule
}

// On appends a rule and returns it for chaining.
func (f *Fake) On(match string, res Result, err error) *Rule {
	r := &Rule{Match: match, Result: res, Err: err}
	f.mu.Lock()
	f.Rules = append(f.Rules, r)
	f.mu.Unlock()
	return r
}

func (f *Fake) answer(s Spec) (Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, s)
	joined := strings.Join(s.Args, " ")
	for _, r := range f.Rules {
		if strings.Contains(joined, r.Match) && (r.Times == 0 || r.hits < r.Times) {
			r.hits++
			if r.Err != nil && r.Result.ExitCode != 0 {
				return r.Result, r.Err
			}
			return r.Result, r.Err
		}
	}
	return Result{}, nil
}

// Joined returns the recorded calls as strings (without env).
func (f *Fake) Joined() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.Calls))
	for i, c := range f.Calls {
		out[i] = strings.Join(c.Args, " ")
	}
	return out
}

// Called reports whether any recorded call contains the fragment.
func (f *Fake) Called(fragment string) bool {
	for _, c := range f.Joined() {
		if strings.Contains(c, fragment) {
			return true
		}
	}
	return false
}

func (f *Fake) Run(_ context.Context, s Spec) (Result, error) { return f.answer(s) }

func (f *Fake) Stream(_ context.Context, s Spec, line func(string)) error {
	res, err := f.answer(s)
	for _, l := range strings.Split(strings.TrimRight(res.Stdout+res.Stderr, "\n"), "\n") {
		if l != "" {
			line(l)
		}
	}
	return err
}

func (f *Fake) Attach(_ context.Context, s Spec, _ io.Reader, out, errw io.Writer) error {
	res, err := f.answer(s)
	_, _ = io.WriteString(out, res.Stdout)
	_, _ = io.WriteString(errw, res.Stderr)
	return err
}
