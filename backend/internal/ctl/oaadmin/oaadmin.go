// Package oaadmin wraps the oa-admin tool that ships inside the api container.
// Its output format is not frozen yet, so parsing is deliberately defensive.
package oaadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"oa-agent/backend/internal/ctl/compose"
)

// User is one row of list-users.
type User struct {
	Username string
	Role     string
	Status   string // active | disabled | ...
	MFA      bool
	Locked   bool
}

// Client runs oa-admin via the compose project.
type Client struct {
	P *compose.Project
}

// Run executes oa-admin; when the api container is not running it uses a one-off container.
func (c *Client) Run(ctx context.Context, args ...string) (compose.Result, error) {
	running, err := c.P.Running(ctx)
	if err != nil {
		return compose.Result{}, err
	}
	up := false
	for _, n := range running {
		up = up || n == "api"
	}
	var cargs []string
	if up {
		cargs = append([]string{"exec", "-T", "api", "oa-admin"}, args...)
	} else {
		cargs = append([]string{"run", "--rm", "-T", "api", "oa-admin"}, args...)
	}
	return c.P.Run(ctx, cargs...)
}

// ListUsers prefers `list-users --json` and falls back to the plain table.
func (c *Client) ListUsers(ctx context.Context) ([]User, error) {
	if res, err := c.Run(ctx, "list-users", "--json"); err == nil {
		if us, perr := ParseUsersJSON(res.Stdout); perr == nil {
			return us, nil
		}
	}
	res, err := c.Run(ctx, "list-users")
	if err != nil {
		return nil, err
	}
	return ParseUsersTable(res.Stdout)
}

// Secret result of an operation that prints a one-time password.
type Secret struct {
	Password string // empty when none was found
	Output   string // oa-admin output with the password replaced by ********
	Raw      string // unredacted output, shown once when the password could not be recognised
}

// ResetPassword returns the temporary password; it must be shown once and never logged.
func (c *Client) ResetPassword(ctx context.Context, user string) (Secret, error) {
	return c.secretCmd(ctx, "reset-password", user)
}

// CreateAdmin creates an administrator and returns its temporary password.
func (c *Client) CreateAdmin(ctx context.Context, user string) (Secret, error) {
	return c.secretCmd(ctx, "create-admin", user)
}

func (c *Client) secretCmd(ctx context.Context, sub, user string) (Secret, error) {
	if err := ValidUsername(user); err != nil {
		return Secret{}, err
	}
	res, err := c.Run(ctx, sub, user)
	if err != nil {
		// stderr of a failed oa-admin never contains the password, but redact defensively
		var ee *compose.ExitError
		if errors.As(err, &ee) {
			return Secret{}, fmt.Errorf("oa-admin %s failed: %s", sub, strings.TrimSpace(redactAll(ee.Result.Stderr+ee.Result.Stdout)))
		}
		return Secret{}, err
	}
	return ParseSecret(res.Stdout + res.Stderr), nil
}

// Simple runs a subcommand that prints no secret (reset-mfa, unlock, disable-user) and returns its output.
func (c *Client) Simple(ctx context.Context, sub, user string) (string, error) {
	if err := ValidUsername(user); err != nil {
		return "", err
	}
	res, err := c.Run(ctx, sub, user)
	if err != nil {
		var ee *compose.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("oa-admin %s failed: %s", sub, strings.TrimSpace(ee.Result.Stderr+ee.Result.Stdout))
		}
		return "", err
	}
	return strings.TrimSpace(res.Stdout + res.Stderr), nil
}

var userRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// ValidUsername keeps odd characters out of the argument vector (and out of log lines).
func ValidUsername(u string) error {
	if !userRe.MatchString(u) {
		return fmt.Errorf("invalid username %q", u)
	}
	return nil
}

var pwRe = regexp.MustCompile(`(?im)(temporary password|temp password|new password|initial password|password|临时密码|新密码|初始密码|密码)\s*[:：=]\s*(\S+)`)

// ParseSecret finds the one-time password in oa-admin's output.
func ParseSecret(out string) Secret {
	s := Secret{Raw: out}
	if m := pwRe.FindStringSubmatchIndex(out); m != nil {
		s.Password = out[m[4]:m[5]]
		s.Output = out[:m[4]] + "********" + out[m[5]:]
		return s
	}
	s.Output = redactAll(out)
	return s
}

// redactAll hides anything that looks like a password assignment.
func redactAll(out string) string {
	return pwRe.ReplaceAllString(out, "$1: ********")
}

// ParseUsersJSON accepts an array or an object with a "users" array; field names vary, so several aliases are tried.
func ParseUsersJSON(out string) ([]User, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, errors.New("empty output")
	}
	var raw []map[string]any
	if strings.HasPrefix(out, "[") {
		if err := json.Unmarshal([]byte(out), &raw); err != nil {
			return nil, err
		}
	} else {
		var wrap struct {
			Users []map[string]any `json:"users"`
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal([]byte(out), &wrap); err != nil {
			return nil, err
		}
		raw = wrap.Users
		if raw == nil {
			raw = wrap.Items
		}
		if raw == nil {
			return nil, errors.New("no users array")
		}
	}
	us := make([]User, 0, len(raw))
	for _, m := range raw {
		u := User{
			Username: str(m, "username", "user", "name", "login"),
			Role:     str(m, "role"),
			Status:   str(m, "status", "state"),
		}
		if u.Username == "" {
			continue
		}
		if b, ok := boolean(m, "disabled"); ok && b {
			u.Status = "disabled"
		} else if u.Status == "" {
			if a, ok := boolean(m, "active", "enabled"); ok && !a {
				u.Status = "disabled"
			} else {
				u.Status = "active"
			}
		}
		u.MFA = anyBool(m, "mfa", "mfa_enabled", "totp", "totp_enabled", "has_mfa")
		u.Locked = anyBool(m, "locked", "is_locked")
		if !u.Locked {
			if v := str(m, "locked_until", "lockedUntil"); v != "" && v != "null" && v != "-" {
				u.Locked = true
			}
		}
		us = append(us, u)
	}
	return us, nil
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			if s, ok := v.(string); ok {
				return s
			}
		}
	}
	return ""
}

func boolean(m map[string]any, keys ...string) (bool, bool) {
	for _, k := range keys {
		switch v := m[k].(type) {
		case bool:
			return v, true
		case string:
			return truthy(v), true
		}
	}
	return false, false
}

func anyBool(m map[string]any, keys ...string) bool {
	b, _ := boolean(m, keys...)
	return b
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "y", "1", "on", "enabled", "locked", "是", "已启用", "是 ", "✓":
		return true
	}
	return false
}

var colSplit = regexp.MustCompile(`\s{2,}|\t+|\s*\|\s*`)

// ParseUsersTable parses a whitespace/pipe separated table with a header row.
// Unknown columns are ignored; a table with no username column is an error.
func ParseUsersTable(out string) ([]User, error) {
	var header []string
	var us []User
	idx := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(strings.Trim(l, "|"))
		if l == "" || strings.Trim(l, "-+=| ") == "" {
			continue
		}
		cols := colSplit.Split(l, -1)
		for i := range cols {
			cols[i] = strings.TrimSpace(cols[i])
		}
		if header == nil {
			for i, h := range cols {
				switch k := strings.ToLower(h); k {
				case "username", "user", "name", "login", "用户名", "用户":
					idx["username"] = i
				case "role", "角色":
					idx["role"] = i
				case "status", "state", "状态":
					idx["status"] = i
				case "mfa", "2fa", "totp":
					idx["mfa"] = i
				case "locked", "lock", "锁定":
					idx["locked"] = i
				}
			}
			if _, ok := idx["username"]; !ok {
				return nil, errors.New("unrecognised list-users output (no username column)")
			}
			header = cols
			continue
		}
		get := func(k string) string {
			if i, ok := idx[k]; ok && i < len(cols) {
				return cols[i]
			}
			return ""
		}
		u := User{Username: get("username"), Role: get("role"), Status: get("status"), MFA: truthy(get("mfa")), Locked: truthy(get("locked"))}
		if u.Username == "" {
			continue
		}
		if u.Status == "" {
			u.Status = "active"
		}
		us = append(us, u)
	}
	if header == nil {
		return nil, errors.New("unrecognised list-users output")
	}
	return us, nil
}
