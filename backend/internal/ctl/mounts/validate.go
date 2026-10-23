package mounts

import (
	"fmt"
	"path"
	"regexp"
	"strings"

	"oa-agent/backend/internal/ctl/fsutil"
)

type Severity int

const (
	Info Severity = iota
	Warn
	Error
)

func (s Severity) String() string { return [...]string{"info", "warn", "error"}[s] }

// Issue is one finding. Ack means the change is only allowed after an explicit acknowledgement
// (an ordinary confirmation: Enter on the TUI review page, y/N or --yes on the command line).
type Issue struct {
	Code    string
	Sev     Severity
	Subject string // role or extra name
	Text    string
	Fix     string // exact shell command the operator can run, when there is one
	Ack     bool
}

func (i Issue) String() string {
	if i.Subject != "" {
		return fmt.Sprintf("[%s] %s: %s", i.Sev, i.Subject, i.Text)
	}
	return fmt.Sprintf("[%s] %s", i.Sev, i.Text)
}

// HasErrors reports whether any issue blocks the operation.
func HasErrors(is []Issue) bool {
	for _, i := range is {
		if i.Sev == Error {
			return true
		}
	}
	return false
}

// NeedsAck reports whether any issue needs explicit acknowledgement.
func NeedsAck(is []Issue) bool {
	for _, i := range is {
		if i.Ack {
			return true
		}
	}
	return false
}

// forbidden host prefixes: system directories, kernel filesystems and docker's own storage.
var forbidden = []string{"/etc", "/usr", "/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32", "/boot", "/proc", "/sys", "/dev", "/run", "/var/lib/docker"}

// Reserved names cannot be used for extras: they collide with core roles or compose services.
var reserved = map[string]bool{
	"archive": true, "spool": true, "app": true, "pgdata": true, "data": true, "pg": true,
	"postgres": true, "api": true, "worker": true, "caddy": true, "storage": true,
	"tmp": true, "default": true, "none": true, "root": true,
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,30}$`)

// ValidateName checks an extra's name.
func ValidateName(name string) *Issue {
	if !nameRe.MatchString(name) {
		return &Issue{Code: "bad_name", Sev: Error, Subject: name, Text: msg("bad_name", name)}
	}
	if reserved[name] {
		return &Issue{Code: "reserved_name", Sev: Error, Subject: name, Text: msg("reserved_name", name)}
	}
	return nil
}

// ValidatePath applies the rules every host path must satisfy, independent of the other mounts.
func ValidatePath(subject, host, installDir string) *Issue {
	bad := func(code string, args ...any) *Issue {
		return &Issue{Code: code, Sev: Error, Subject: subject, Text: msg(code, args...)}
	}
	if strings.TrimSpace(host) == "" {
		return bad("empty")
	}
	if !strings.HasPrefix(host, "/") {
		return bad("not_abs", host)
	}
	for _, seg := range strings.Split(host, "/") {
		if seg == ".." {
			return bad("dotdot", host)
		}
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./-", r)) {
			return bad("bad_char", host, string(r))
		}
	}
	clean := path.Clean(host)
	if clean == "/" {
		return bad("root")
	}
	for _, f := range forbidden {
		if fsutil.Within(f, clean) {
			return bad("forbidden", clean, f)
		}
	}
	// a parent of /var/lib/docker (e.g. /var/lib) would expose it
	for _, f := range forbidden {
		if fsutil.Within(clean, f) {
			return bad("forbidden_parent", clean, f)
		}
	}
	if installDir != "" {
		id := path.Clean(installDir)
		if fsutil.Within(clean, id) {
			return bad("install_dir", clean, id)
		}
	}
	return nil
}

// Options feeds Validate with environment facts; both are optional.
type Options struct {
	InstallDir string
	// Resolve maps a path to its symlink-free form (os.Stat based in production, nil to skip).
	Resolve func(string) string
}

// Validate checks a whole config: per-path rules, names, uniqueness and nesting.
func Validate(c Config, o Options) []Issue {
	var out []Issue
	add := func(i *Issue) {
		if i != nil {
			out = append(out, *i)
		}
	}
	entries := c.Entries()
	seen := map[string]bool{}
	cont := map[string]string{}
	for _, e := range entries {
		add(ValidatePath(e.Label(), e.Host, o.InstallDir))
		if e.Role != "extra" {
			continue
		}
		if n := ValidateName(e.Name); n != nil {
			add(n)
			continue
		}
		if seen[e.Name] {
			add(&Issue{Code: "dup_name", Sev: Error, Subject: e.Name, Text: msg("dup_name", e.Name)})
		}
		seen[e.Name] = true
		if want := ExtraRoot + "/" + e.Name; e.Container != want {
			add(&Issue{Code: "bad_container", Sev: Error, Subject: e.Name, Text: msg("bad_container", e.Container, want)})
		}
		if prev, ok := cont[e.Container]; ok {
			add(&Issue{Code: "dup_container", Sev: Error, Subject: e.Name, Text: msg("dup_container", e.Container, prev)})
		}
		cont[e.Container] = e.Name
	}
	// nesting: only entries with a sane path take part (the rest already reported)
	type ent struct {
		label, host string
	}
	var ok []ent
	for _, e := range entries {
		if ValidatePath(e.Label(), e.Host, o.InstallDir) == nil {
			h := path.Clean(e.Host)
			ok = append(ok, ent{e.Label(), h})
			if o.Resolve != nil {
				if r := o.Resolve(h); r != "" && r != h {
					ok = append(ok, ent{e.Label(), r})
				}
			}
		}
	}
	reported := map[string]bool{}
	for i := 0; i < len(ok); i++ {
		for j := i + 1; j < len(ok); j++ {
			a, b := ok[i], ok[j]
			if a.label == b.label {
				continue
			}
			if fsutil.Within(a.host, b.host) || fsutil.Within(b.host, a.host) {
				k := a.label + "|" + b.label
				if a.label > b.label {
					k = b.label + "|" + a.label
				}
				if reported[k] {
					continue
				}
				reported[k] = true
				out = append(out, Issue{Code: "overlap", Sev: Error, Subject: a.label,
					Text: msg("overlap", a.label, a.host, b.label, b.host)})
			}
		}
	}
	return out
}
