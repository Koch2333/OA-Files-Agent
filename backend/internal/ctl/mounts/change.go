package mounts

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Changes lists the roles whose host path differs.
func Changes(old, new Config) []string {
	var out []string
	for _, r := range CoreRoles {
		if old.CoreHost(r) != new.CoreHost(r) {
			out = append(out, r)
		}
	}
	return out
}

// RiskInput describes the situation for the data-safety assessment.
type RiskInput struct {
	Old, New       Config
	Prober         Prober
	RunningService []string // containers currently running (pgdata changes need none)
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Risks assesses a core-path change. This is the "never silently point at an empty archive" rule:
// nothing here moves data; it only decides what must be acknowledged or refused.
func Risks(in RiskInput) []Issue {
	var out []Issue
	state := func(host string) (exists, nonEmpty bool) {
		p, err := in.Prober.Probe(host)
		if err != nil || !p.Exists || !p.IsDir {
			return false, false
		}
		return true, !p.Empty
	}
	for _, role := range Changes(in.Old, in.New) {
		o, n := in.Old.CoreHost(role), in.New.CoreHost(role)
		_, oldFull := state(o)
		_, newFull := state(n)
		is := func(code string, sev Severity, ack bool, args ...any) {
			out = append(out, Issue{Code: code, Sev: sev, Subject: role, Text: msg(code, args...), Ack: ack})
		}
		switch role {
		case "archive":
			if oldFull {
				is("archive_relocate", Warn, true, o, n, o, n)
				if !newFull {
					is("archive_new_empty", Warn, false, n, o)
				}
			}
			if newFull && oldFull {
				is("archive_new_dirty", Warn, false, n)
			}
		case "pgdata":
			if len(in.RunningService) > 0 {
				is("pgdata_running", Error, false, strings.Join(in.RunningService, ", "))
				continue
			}
			if oldFull && !newFull {
				is("pgdata_relocate", Warn, true, o, n, o, n)
			} else if oldFull && newFull {
				is("pgdata_relocate", Warn, true, o, n, o, n)
				is("pgdata_new_has", Warn, false, n)
			} else if newFull {
				is("pgdata_new_has", Warn, false, n)
			}
		case "app":
			if oldFull {
				if _, err := os.Stat(filepath.Join(n, "secret.key")); err != nil {
					is("app_relocate", Warn, true, o, n)
				}
			}
		case "spool":
			if oldFull {
				is("spool_relocate", Warn, false, o, n)
			}
		}
	}
	// ro -> rw flips on existing extras
	for _, nx := range in.New.Extra {
		if i := in.Old.FindExtra(nx.Name); i >= 0 && in.Old.Extra[i].ReadOnly && !nx.ReadOnly {
			out = append(out, Issue{Code: "ro_to_rw", Sev: Warn, Subject: nx.Name, Text: msg("ro_to_rw", nx.Name), Ack: true})
		}
	}
	return out
}

// CopyCommand is the cp invocation used for --copy and printed in instructions.
func CopyCommand(from, to string) (string, []string) {
	return "cp", []string{"-a", from + "/.", to + "/"}
}

// Exec runs an external command; injectable so tests never copy real data.
type Exec func(ctx context.Context, name string, args ...string) error

// DefaultExec runs the command with an argument vector (no shell).
func DefaultExec(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// TreeStat is a cheap fingerprint of a directory tree: file count and total bytes.
type TreeStat struct {
	Files int
	Bytes int64
	Dirs  int
}

// Measure walks a tree (symlinks are counted, not followed).
func Measure(root string) (TreeStat, error) {
	var s TreeStat
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			s.Dirs++
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		s.Files++
		if info.Mode().IsRegular() {
			s.Bytes += info.Size()
		}
		return nil
	})
	return s, err
}

// CopyAndVerify copies the old tree into the new one with cp -a and compares fingerprints.
// The source is never modified.
func CopyAndVerify(ctx context.Context, run Exec, from, to string) (TreeStat, error) {
	if err := os.MkdirAll(to, 0o750); err != nil {
		return TreeStat{}, err
	}
	before, err := Measure(from)
	if err != nil {
		return TreeStat{}, err
	}
	name, args := CopyCommand(from, to)
	if err := run(ctx, name, args...); err != nil {
		return TreeStat{}, err
	}
	after, err := Measure(to)
	if err != nil {
		return TreeStat{}, err
	}
	// the destination may already hold extra files, never fewer than the source
	if after.Files < before.Files || after.Bytes < before.Bytes {
		return after, fmt.Errorf("copy verification failed: source %d files / %d bytes, destination %d files / %d bytes",
			before.Files, before.Bytes, after.Files, after.Bytes)
	}
	return after, nil
}

// RunFix applies a remediation. Only meaningful as root; the caller decides.
func RunFix(f Fix) error {
	switch f.Kind {
	case "mkdir":
		if err := os.MkdirAll(f.Path, f.Mode); err != nil {
			return err
		}
		if err := os.Chmod(f.Path, f.Mode); err != nil {
			return err
		}
		return os.Chown(f.Path, f.UID, f.GID)
	case "chmod":
		return os.Chmod(f.Path, f.Mode)
	case "chown":
		if !f.Recursive {
			return os.Chown(f.Path, f.UID, f.GID)
		}
		return filepath.WalkDir(f.Path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			return os.Lchown(p, f.UID, f.GID)
		})
	}
	return fmt.Errorf("unknown fix %q", f.Kind)
}
