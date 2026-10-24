package diag

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// Mask replaces every redacted secret.
const Mask = "[REDACTED]"

// sensitiveKey matches key names whose value must never be stored or shown.
const sensitiveKey = `(?:api[_\-]?key|apikey|x[_\-]api[_\-]key|secret|token|passw(?:or)?d|passwd|pwd|passphrase|credential|authorization|private[_\-]?key|access[_\-]?key|client[_\-]?secret|refresh[_\-]?token|totp|recovery[_\-]?codes?|setup[_\-]?token|session[_\-]?(?:id|token|key)|oa_session|cookie|csrf)`

var (
	rePEM      = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
	reOTPAuth  = regexp.MustCompile(`(?i)otpauth://\S+`)
	reHeader   = regexp.MustCompile(`(?im)\b((?:proxy-)?authorization|set-cookie|cookie|x-csrf-token|x-api-key|x-auth-token|x-amz-security-token)(\s*[:=]\s*)[^\r\n]*`)
	reBearer   = regexp.MustCompile(`(?i)\b(bearer)(\s+)[A-Za-z0-9._~+/=\-]{8,}`)
	reSession  = regexp.MustCompile(`(?i)\b(oa_session)(\s*["']?\s*[:=]\s*["']?)[^\s;,"'&}]*`)
	reURLCred  = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.\-]*://)[^\s/@:]*:[^\s/@]*@`)
	reMyDSN    = regexp.MustCompile(`([A-Za-z0-9_.\-]+):[^@\s/]*@(tcp|unix)\(`)
	reURLUser  = regexp.MustCompile(`(?i)\b((?:postgres(?:ql)?|mysql|redis|amqp|mongodb(?:\+srv)?|ftp|ftps|sftp|smb|webdav|https?)://)[^\s/@:]+@`)
	reKVQuoted = regexp.MustCompile(`(?i)(["']?[\w.\-]*` + sensitiveKey + `[\w.\-]*["']?)(\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`)
	reKVBare   = regexp.MustCompile(`(?i)(["']?[\w.\-]*` + sensitiveKey + `[\w.\-]*["']?)(\s*[:=]\s*)([^\s&,;"'}\]\)]+)`)
	reSK       = regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_\-]{12,}`)
	reVendor   = regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{20,}\b|\bxox[abpr]-[A-Za-z0-9\-]{10,}\b|\bAIza[0-9A-Za-z_\-]{30,}\b|\bya29\.[A-Za-z0-9_\-]{20,}`)
	reJWT      = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{6,}\.[A-Za-z0-9_\-]{6,}\.[A-Za-z0-9_\-]*`)
	reSetup    = regexp.MustCompile(`(?i)(setup token\s*[:=]?\s*)[A-Z2-7\- ]{16,}`)
	reRecovery = regexp.MustCompile(`\b[A-Z2-7]{4}-[A-Z2-7]{4}-[A-Z2-7]{4}\b`)
	reBase32   = regexp.MustCompile(`\b[A-Z2-7]{16,}={0,6}\b`)
	reLongHex  = regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`)
	reLongB64  = regexp.MustCompile(`[A-Za-z0-9+/_\-]{32,}={0,2}`)
	reEmail    = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+`)
	reHomeWin  = regexp.MustCompile(`(?i)\b([a-z]:[\\/]+users[\\/]+)[^\\/\s"':*?<>|]+`)
	reHomeNix  = regexp.MustCompile(`(/(?:home|Users)/)[^/\s"':]+`)
	reHomeWSL  = regexp.MustCompile(`(?i)(/mnt/[a-z]/users/)[^/\s"':]+`)
)

var (
	secretsMu sync.RWMutex
	secrets   []string
)

// RegisterSecret makes an exact value (an API key from the environment, a generated token, a password) be masked
// wherever it appears, regardless of surrounding text. Values shorter than 6 characters are ignored.
func RegisterSecret(v ...string) {
	secretsMu.Lock()
	defer secretsMu.Unlock()
	for _, s := range v {
		s = strings.TrimSpace(s)
		if len(s) < 6 {
			continue
		}
		dup := false
		for _, e := range secrets {
			if e == s {
				dup = true
				break
			}
		}
		if !dup {
			secrets = append(secrets, s)
		}
	}
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
}

// ResetSecrets forgets every registered value (tests).
func ResetSecrets() {
	secretsMu.Lock()
	secrets = nil
	secretsMu.Unlock()
}

func maskKnown(s string) string {
	secretsMu.RLock()
	defer secretsMu.RUnlock()
	for _, k := range secrets {
		if strings.Contains(s, k) {
			s = strings.ReplaceAll(s, k, Mask)
		}
	}
	return s
}

// RedactSecrets masks credentials only (keys, tokens, passwords, cookies, secrets in URLs and key/value pairs).
func RedactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = maskKnown(s)
	s = rePEM.ReplaceAllString(s, Mask)
	s = reOTPAuth.ReplaceAllString(s, "otpauth://"+Mask)
	s = reHeader.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reSession.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reURLCred.ReplaceAllString(s, "${1}"+Mask+"@")
	s = reMyDSN.ReplaceAllString(s, "${1}:"+Mask+"@${2}(") // MySQL DSN: user:password@tcp(host:3306)/db
	s = reURLUser.ReplaceAllString(s, "${1}"+Mask+"@")
	s = reKVQuoted.ReplaceAllStringFunc(s, func(m string) string {
		sm := reKVQuoted.FindStringSubmatch(m)
		return sm[1] + sm[2] + `"` + Mask + `"`
	})
	s = reKVBare.ReplaceAllStringFunc(s, func(m string) string {
		sm := reKVBare.FindStringSubmatch(m)
		if strings.HasPrefix(sm[3], "[REDACTED") || strings.Contains(sm[3], Mask) {
			return m
		}
		return sm[1] + sm[2] + Mask
	})
	s = reBearer.ReplaceAllString(s, "${1}${2}"+Mask)
	s = reSK.ReplaceAllString(s, Mask)
	s = reVendor.ReplaceAllString(s, Mask)
	s = reJWT.ReplaceAllString(s, Mask)
	s = reSetup.ReplaceAllString(s, "${1}"+Mask)
	s = reRecovery.ReplaceAllString(s, Mask)
	s = reBase32.ReplaceAllString(s, Mask)
	s = reLongHex.ReplaceAllString(s, Mask)
	s = reLongB64.ReplaceAllStringFunc(s, func(m string) string {
		if looksLikeSecret(m) {
			return Mask
		}
		return m
	})
	return s
}

// looksLikeSecret filters the long-run heuristic: a secret mixes digits, upper and lower case and is not a path.
func looksLikeSecret(m string) bool {
	var d, u, l int
	for _, r := range m {
		switch {
		case unicode.IsDigit(r):
			d++
		case unicode.IsUpper(r):
			u++
		case unicode.IsLower(r):
			l++
		}
	}
	return d > 0 && u > 0 && l > 0 && strings.Count(m, "/") <= 3
}

// Redact is RedactSecrets plus personal data: e-mail addresses and the user name inside absolute home paths.
func Redact(s string) string {
	s = RedactSecrets(s)
	s = reHomeWin.ReplaceAllString(s, "${1}[USER]")
	s = reHomeWSL.ReplaceAllString(s, "${1}[USER]")
	s = reHomeNix.ReplaceAllString(s, "${1}[USER]")
	s = reEmail.ReplaceAllString(s, "[EMAIL]")
	return s
}

var sensitiveKeyRE = regexp.MustCompile(`(?i)` + sensitiveKey)

// SensitiveKey reports whether a map key names a secret.
func SensitiveKey(k string) bool { return sensitiveKeyRE.MatchString(k) }

// RedactValue redacts every string inside a JSON-like value; values under sensitive keys are masked outright.
// Nesting is limited to depth 6 and collections to 1000 entries.
func RedactValue(v any) any { return redactValue(v, 0) }

func redactValue(v any, depth int) any {
	if depth > 6 {
		return "[truncated]"
	}
	switch x := v.(type) {
	case nil, bool, int, int32, int64, uint, uint32, uint64, float32, float64:
		return x
	case string:
		return Redact(x)
	case []string:
		out := make([]any, 0, len(x))
		for i, s := range x {
			if i >= 1000 {
				break
			}
			out = append(out, Redact(s))
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for i, e := range x {
			if i >= 1000 {
				break
			}
			out = append(out, redactValue(e, depth+1))
		}
		return out
	case map[string]string:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = e
		}
		return redactValue(m, depth)
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(x))
		for i, k := range keys {
			if i >= 1000 {
				break
			}
			if SensitiveKey(k) {
				out[Redact(k)] = Mask
				continue
			}
			out[Redact(k)] = redactValue(x[k], depth+1)
		}
		return out
	default:
		return Redact(strings.TrimSpace(sprint(x)))
	}
}

// Truncate cuts s to at most n runes.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func sprint(v any) string { return fmt.Sprint(v) }
