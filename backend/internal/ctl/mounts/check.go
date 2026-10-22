package mounts

import (
	"fmt"
	"io/fs"
	"strings"
)

const gib = 1 << 30

// LowSpaceBytes is the threshold below which a core mount gets a warning.
const LowSpaceBytes = 20 * gib

var networkFS = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true, "9p": true, "davfs": true,
	"ceph": true, "glusterfs": true, "afs": true, "sshfs": true, "lustre": true, "ncpfs": true,
}

func isNetworkFS(t string) bool  { return networkFS[t] || strings.HasPrefix(t, "fuse") }
func isVolatileFS(t string) bool { return t == "tmpfs" || t == "ramfs" }

// ExpectedOwner is the uid:gid the container process needs on a core role.
func ExpectedOwner(role string) (uid, gid int) {
	if role == "pgdata" {
		return PGUID, PGGID
	}
	return AppUID, AppGID
}

// Fix is a remediation that can be run (as root) or printed (otherwise).
type Fix struct {
	Kind      string // mkdir | chown | chmod
	Path      string
	UID, GID  int
	Mode      fs.FileMode
	Recursive bool
}

// Command is the exact shell command equivalent, quoted for copy and paste.
func (f Fix) Command() string {
	q := "'" + strings.ReplaceAll(f.Path, "'", `'\''`) + "'"
	switch f.Kind {
	case "mkdir":
		return fmt.Sprintf("install -d -o %d -g %d -m %04o %s", f.UID, f.GID, f.Mode, q)
	case "chown":
		r := ""
		if f.Recursive {
			r = "-R "
		}
		return fmt.Sprintf("chown %s%d:%d %s", r, f.UID, f.GID, q)
	case "chmod":
		return fmt.Sprintf("chmod %04o %s", f.Mode, q)
	}
	return ""
}

// CheckResult is the report for one entry plus the fixes that would resolve it.
type CheckResult struct {
	Entry  Entry
	Probe  Probe
	Issues []Issue
	Fixes  []Fix
}

func canAccess(p Probe, uid, gid int, write bool) bool {
	perm := p.Mode.Perm()
	var r, w, x fs.FileMode = 0o004, 0o002, 0o001
	switch {
	case p.UID == uid:
		r, w, x = 0o400, 0o200, 0o100
	case p.GID == gid:
		r, w, x = 0o040, 0o020, 0o010
	}
	if perm&r == 0 || perm&x == 0 {
		return false
	}
	return !write || perm&w != 0
}

// Check inspects one entry. Probe errors become warnings: a missing right is not a reason to hide the rest.
func Check(e Entry, pr Prober) CheckResult {
	res := CheckResult{Entry: e}
	add := func(code string, sev Severity, fix string, args ...any) {
		res.Issues = append(res.Issues, Issue{Code: code, Sev: sev, Subject: e.Label(), Text: msg(code, args...), Fix: fix})
	}
	p, err := pr.Probe(e.Host)
	if err != nil {
		add("probe_failed", Warn, "", e.Host, err)
		return res
	}
	res.Probe = p
	core := e.Role != "extra"
	uid, gid := AppUID, AppGID
	if core {
		uid, gid = ExpectedOwner(e.Role)
	}
	if !p.Exists {
		fx := Fix{Kind: "mkdir", Path: e.Host, UID: uid, GID: gid, Mode: 0o750}
		if e.Role == "pgdata" {
			fx.Mode = 0o700
		}
		if core {
			add("missing", Warn, fx.Command(), e.Host)
			res.Fixes = append(res.Fixes, fx)
		} else {
			add("missing", Error, "", e.Host) // an external directory is the operator's; we never create it
		}
	} else if !p.IsDir {
		add("not_dir", Error, "", e.Host)
		return res
	} else if core {
		if p.UID != uid || p.GID != gid {
			fx := Fix{Kind: "chown", Path: e.Host, UID: uid, GID: gid, Recursive: true}
			code, sev := "owner", Warn
			args := []any{p.UID, p.GID, uid, gid}
			if e.Role == "pgdata" {
				code, sev = "pg_owner", Error
			}
			add(code, sev, fx.Command(), args...)
			res.Fixes = append(res.Fixes, fx)
		}
		if e.Role == "pgdata" {
			if m := p.Mode.Perm(); m != 0o700 && m != 0o750 {
				fx := Fix{Kind: "chmod", Path: e.Host, Mode: 0o700}
				add("pg_mode", Error, fx.Command(), m)
				res.Fixes = append(res.Fixes, fx)
			}
		}
	} else {
		if !canAccess(p, uid, gid, false) {
			add("not_readable", Warn, "", uid, e.Host)
		} else if !e.ReadOnly && !canAccess(p, uid, gid, true) {
			add("not_writable", Warn, fmt.Sprintf("chmod o+w '%s'  # or: chown %d:%d '%s'", e.Host, uid, gid, e.Host), uid, e.Host)
		}
	}
	if p.FSType != "" {
		switch {
		case e.Role == "pgdata" && (isVolatileFS(p.FSType) || isNetworkFS(p.FSType) || p.FSType == "overlay"):
			add("fs_pg", Warn, "", p.FSType)
		case core && isVolatileFS(p.FSType):
			add("fs_type", Warn, "", e.Host, p.FSType, msg("fs_volatile"))
		case core && (isNetworkFS(p.FSType)):
			add("fs_type", Info, "", e.Host, p.FSType, msg("fs_net"))
		}
	}
	if core && p.TotalBytes > 0 && p.FreeBytes < LowSpaceBytes && e.Role != "app" {
		add("low_space", Warn, "", float64(p.FreeBytes)/gib)
	}
	return res
}

// CheckAll checks every entry of the config.
func CheckAll(c Config, pr Prober) []CheckResult {
	var out []CheckResult
	for _, e := range c.Entries() {
		out = append(out, Check(e, pr))
	}
	return out
}
