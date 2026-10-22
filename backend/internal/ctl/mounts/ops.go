package mounts

import "fmt"

// AddExtra returns a config with an extra added (or replaced when replace is true).
// New extras are read-only unless rw is requested; the issues are validation findings of the result.
func AddExtra(c Config, name, host string, readOnly, replace bool, o Options) (Config, []Issue) {
	n := c.Clone()
	x := Extra{Name: name, Host: cleanHost(host), Container: ExtraRoot + "/" + name, ReadOnly: readOnly}
	if i := n.FindExtra(name); i >= 0 {
		if !replace {
			return c, []Issue{{Code: "dup_name", Sev: Error, Subject: name, Text: msg("dup_name", name)}}
		}
		n.Extra[i] = x
	} else {
		n.Extra = append(n.Extra, x)
	}
	n.Normalize()
	return n, Validate(n, o)
}

// RemoveExtra drops an extra; the data on the host is of course untouched.
func RemoveExtra(c Config, name string) (Config, error) {
	i := c.FindExtra(name)
	if i < 0 {
		return c, fmt.Errorf("没有名为 %q 的外部挂载", name)
	}
	n := c.Clone()
	n.Extra = append(n.Extra[:i], n.Extra[i+1:]...)
	n.Normalize()
	return n, nil
}

// SetCore points a core role at a new host path.
func SetCore(c Config, role, host string, o Options) (Config, []Issue, error) {
	ok := false
	for _, r := range CoreRoles {
		ok = ok || r == role
	}
	if !ok {
		return c, nil, fmt.Errorf("未知的挂载角色 %q（可选：archive spool app pgdata）", role)
	}
	n := c.WithCore(role, cleanHost(host))
	n.Normalize()
	return n, Validate(n, o), nil
}
