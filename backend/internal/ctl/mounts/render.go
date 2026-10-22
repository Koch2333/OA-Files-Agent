package mounts

import (
	"errors"
	"fmt"
	"strings"
)

const header = "# generated — do not edit. Source of truth: mounts.json; regenerate with \"oa-agentctl mounts apply\".\n" +
	"# Compose merges \"volumes\" lists by container path, so each entry replaces the base file's fallback.\n"

// quote wraps a compose short-syntax volume only when YAML would otherwise misread it.
func yamlStr(s string) string {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-:", r)) {
			return `"` + s + `"` // validated paths contain no quote or backslash
		}
	}
	return s
}

// Render produces docker-compose.mounts.yml deterministically. It refuses an invalid config.
func Render(c Config, o Options) ([]byte, error) {
	c = c.Clone()
	c.Normalize()
	if is := Validate(c, o); HasErrors(is) {
		for _, i := range is {
			if i.Sev == Error {
				return nil, errors.New(i.String())
			}
		}
	}
	vol := func(host, target, mode string) string {
		v := host + ":" + target
		if mode != "" {
			v += ":" + mode
		}
		return "      - " + yamlStr(v) + "\n"
	}
	var b strings.Builder
	b.WriteString(header)
	b.WriteString("services:\n")
	b.WriteString("  postgres:\n    volumes:\n")
	b.WriteString(vol(c.PGData.Host, ContainerFor("pgdata"), ""))
	b.WriteString("  api:\n")
	if len(c.Extra) > 0 {
		fmt.Fprintf(&b, "    environment:\n      OA_LOCAL_ROOTS: %s\n", ExtraRoot)
	}
	b.WriteString("    volumes:\n")
	b.WriteString(vol(c.App.Host, ContainerFor("app"), ""))
	b.WriteString(vol(c.Spool.Host, ContainerFor("spool"), ""))
	b.WriteString(vol(c.Archive.Host, ContainerFor("archive"), ""))
	for _, x := range c.Extra { // Normalize sorted them
		mode := ""
		if x.ReadOnly {
			mode = "ro"
		}
		b.WriteString(vol(x.Host, x.Container, mode))
	}
	b.WriteString("  worker:\n    volumes:\n")
	b.WriteString(vol(c.Spool.Host, ContainerFor("spool"), ""))
	b.WriteString(vol(c.Archive.Host, ContainerFor("archive"), "ro"))
	return []byte(b.String()), nil
}
