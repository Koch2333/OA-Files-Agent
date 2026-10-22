// Package envfile reads and edits the installer's .env without disturbing comments or ordering.
package envfile

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"oa-agent/backend/internal/ctl/fsutil"
)

// File keeps the original lines so a Set only touches the one assignment.
type File struct {
	lines []string
}

var assign = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)

// Parse splits raw .env text into lines.
func Parse(data []byte) *File {
	s := strings.ReplaceAll(string(data), "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return &File{}
	}
	return &File{lines: strings.Split(s, "\n")}
}

// Load parses path; a missing file is an error (callers decide whether that matters).
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b), nil
}

// cleanValue mirrors the bash tool: strip a trailing " # comment" and one pair of quotes.
func cleanValue(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		v = v[1 : len(v)-1]
	}
	return v
}

// Get returns the last assignment of key.
func (f *File) Get(key string) (string, bool) {
	val, ok := "", false
	for _, l := range f.lines {
		if m := assign.FindStringSubmatch(l); m != nil && m[1] == key {
			val, ok = cleanValue(m[2]), true
		}
	}
	return val, ok
}

// Value is Get with a default for missing or empty values.
func (f *File) Value(key, def string) string {
	if v, ok := f.Get(key); ok && v != "" {
		return v
	}
	return def
}

// Set replaces the last assignment in place (earlier duplicates are dropped) or appends.
func (f *File) Set(key, value string) {
	last := -1
	for i, l := range f.lines {
		if m := assign.FindStringSubmatch(l); m != nil && m[1] == key {
			last = i
		}
	}
	line := key + "=" + value
	if last < 0 {
		f.lines = append(f.lines, line)
		return
	}
	out := make([]string, 0, len(f.lines))
	for i, l := range f.lines {
		if m := assign.FindStringSubmatch(l); m != nil && m[1] == key {
			if i != last {
				continue
			}
			l = line
		}
		out = append(out, l)
	}
	f.lines = out
}

// Bytes renders the file with a trailing newline.
func (f *File) Bytes() []byte {
	if len(f.lines) == 0 {
		return nil
	}
	return []byte(strings.Join(f.lines, "\n") + "\n")
}

// Save writes atomically with mode 0600 (the file holds secrets).
func (f *File) Save(path string) error {
	return fsutil.WriteAtomic(path, f.Bytes(), 0o600)
}

// Environ returns KEY=VALUE for every assignment (last wins), for passing to docker compose.
func (f *File) Environ() []string {
	seen := map[string]int{}
	var out []string
	for _, l := range f.lines {
		m := assign.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		kv := m[1] + "=" + cleanValue(m[2])
		if i, ok := seen[m[1]]; ok {
			out[i] = kv
			continue
		}
		seen[m[1]] = len(out)
		out = append(out, kv)
	}
	return out
}

var secretKey = regexp.MustCompile(`(PASSWORD|TOKEN|SECRET|API_KEY)`)

// IsSecret reports whether the value of key must never be displayed or logged.
func IsSecret(key string) bool { return secretKey.MatchString(strings.ToUpper(key)) }

// MaskedText renders the file with secret values hidden; comments are kept.
func (f *File) MaskedText() string {
	var b strings.Builder
	for _, l := range f.lines {
		m := assign.FindStringSubmatch(l)
		if m != nil && IsSecret(m[1]) && cleanValue(m[2]) != "" {
			l = m[1] + "=******** (hidden)"
		}
		b.WriteString(l + "\n")
	}
	return b.String()
}

// Secrets lists non-empty secret values so loggers can scrub them from captured output.
func (f *File) Secrets() []string {
	var out []string
	for _, l := range f.lines {
		if m := assign.FindStringSubmatch(l); m != nil && IsSecret(m[1]) {
			if v := cleanValue(m[2]); len(v) >= 6 {
				out = append(out, v)
			}
		}
	}
	return out
}

var (
	reMem  = regexp.MustCompile(`^[0-9]+[kKmMgG]$`)
	reNum  = regexp.MustCompile(`^[0-9]+$`)
	reTZ   = regexp.MustCompile(`^[A-Za-z0-9_+/-]{1,64}$`)
	reURL  = regexp.MustCompile(`^https?://[A-Za-z0-9.:_-]+$`)
	reLLM  = regexp.MustCompile(`^(|https?://[^\s]+)$`)
	reMod  = regexp.MustCompile(`^[A-Za-z0-9._:/-]*$`)
	reMail = regexp.MustCompile(`^(|[^@\s]+@[^@\s]+\.[^@\s]+)$`)
	reVer  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	reReg  = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,200}$`)
	reKey  = regexp.MustCompile(`^[A-Za-z0-9._:/+=-]*$`)
)

// ErrNotWhitelisted marks keys that have dedicated commands (secrets, domain, mounts).
var ErrNotWhitelisted = errors.New("key is not editable with config set")

// ValidateConfig is the config-set whitelist; it matches the bash fallback so both tools agree.
func ValidateConfig(key, val string) error {
	if strings.ContainsAny(val, "\n\r\t \"'$\\`#") {
		return fmt.Errorf("invalid value for %s (no whitespace, quotes, $, #, backslash)", key)
	}
	ok := false
	switch key {
	case "OA_PORT", "OA_HTTP_PORT", "OA_HTTPS_PORT":
		ok = validPort(val)
	case "OA_STORAGE":
		ok = val == "on" || val == "off"
	case "OA_PG_MEM", "OA_API_MEM", "OA_WORKER_MEM", "OA_CADDY_MEM", "OA_LOG_MAX_SIZE":
		ok = reMem.MatchString(val)
	case "OA_LOG_MAX_FILE", "OA_LLM_TIMEOUT_SECONDS":
		ok = reNum.MatchString(val)
	case "OA_TZ":
		ok = reTZ.MatchString(val)
	case "OA_LOG_LEVEL":
		ok = val == "" || val == "info" || val == "debug"
	case "OA_PUBLIC_URL":
		ok = reURL.MatchString(val)
	case "OA_LLM_BASE_URL":
		ok = reLLM.MatchString(val)
	case "OA_LLM_MODEL":
		ok = reMod.MatchString(val)
	case "OA_LLM_API_KEY":
		ok = reKey.MatchString(val)
	case "OA_ACME_EMAIL":
		ok = reMail.MatchString(val)
	case "OA_VERSION":
		ok = reVer.MatchString(val)
	case "OA_IMAGE_REGISTRY":
		ok = reReg.MatchString(val)
	default:
		return fmt.Errorf("%s: %w", key, ErrNotWhitelisted)
	}
	if !ok {
		return fmt.Errorf("invalid value for %s", key)
	}
	return nil
}

func validPort(v string) bool {
	if !reNum.MatchString(v) || len(v) > 5 {
		return false
	}
	n := 0
	for _, c := range v {
		n = n*10 + int(c-'0')
	}
	return n >= 1 && n <= 65535
}

// ValidDomain accepts an FQDN (no scheme, no port).
var ValidDomain = regexp.MustCompile(`^([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`)
