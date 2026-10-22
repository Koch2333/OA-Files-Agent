package diag

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	reNormUUID  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reNormTime  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+\-]\d{2}:?\d{2})?`)
	reNormHex   = regexp.MustCompile(`(?i)\b(?:0x)?[0-9a-f]{8,}\b`)
	reNormPath  = regexp.MustCompile(`(?:[A-Za-z]:[\\/]|/)[^\s"'()<>|,;]*[\\/][^\s"'()<>|,;]*`)
	reNormURL   = regexp.MustCompile(`(?i)https?://[^\s"'()<>]+`)
	reNormNum   = regexp.MustCompile(`\d+`)
	reNormSpace = regexp.MustCompile(`\s+`)
	reNormQuote = regexp.MustCompile(`'[^']{0,80}'`)
	reArgs      = regexp.MustCompile(`\((?:0x[0-9a-fA-F]+|\.\.\.|\{[^)]*\}|[^()]*)\)$`)
	reLineCol   = regexp.MustCompile(`:\d+(?::\d+)?`)
	reOffset    = regexp.MustCompile(`\s*\+0x[0-9a-fA-F]+`)
	reAsset     = regexp.MustCompile(`([A-Za-z0-9_.\-]+?)[-.][A-Za-z0-9_\-]{6,12}\.(js|mjs|css)\b`)
	reJSHost    = regexp.MustCompile(`(?i)https?://[^/\s)]+`)
)

// NormalizeMessage maps variable parts of an error message (ids, paths, numbers, timestamps, urls) to placeholders so
// the same defect groups together however often and wherever it occurs.
func NormalizeMessage(m string) string {
	m = RedactSecrets(m)
	m = reNormURL.ReplaceAllString(m, "<url>")
	m = reNormTime.ReplaceAllString(m, "<time>")
	m = reNormUUID.ReplaceAllString(m, "<id>")
	m = reNormPath.ReplaceAllString(m, "<path>")
	m = reNormHex.ReplaceAllString(m, "<hex>")
	m = reNormNum.ReplaceAllString(m, "<n>")
	m = reNormQuote.ReplaceAllString(m, "'<s>'")
	m = reNormSpace.ReplaceAllString(m, " ")
	return strings.ToLower(strings.TrimSpace(m))
}

// skipFrame hides plumbing frames that are identical for unrelated errors.
func skipFrame(fn string) bool {
	for _, p := range []string{"runtime.", "runtime/", "oa-agent/backend/internal/diag.", "net/http.", "github.com/gin-gonic/gin.", "github.com/gin-contrib/", "panic(", "testing."} {
		if strings.HasPrefix(fn, p) {
			return true
		}
	}
	return false
}

// TopFrames returns the first n meaningful frames of a Go or JavaScript stack as stable names (no addresses, line
// numbers, hosts or bundle hashes).
func TopFrames(stack string, n int) []string {
	var out []string
	for _, line := range strings.Split(stack, "\n") {
		if len(out) >= n {
			break
		}
		if strings.HasPrefix(line, "\t") { // Go file:line rows
			continue
		}
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "goroutine ") || strings.HasPrefix(l, "created by ") || strings.HasPrefix(l, "...") {
			continue
		}
		js := strings.HasPrefix(l, "at ") || strings.Contains(l, "@http")
		if js {
			l = reJSHost.ReplaceAllString(l, "")
			l = reLineCol.ReplaceAllString(l, "")
			l = reAsset.ReplaceAllString(l, "$1.$2")
			l = reNormSpace.ReplaceAllString(l, " ")
			out = append(out, strings.TrimSpace(l))
			continue
		}
		l = reOffset.ReplaceAllString(l, "")
		l = reArgs.ReplaceAllString(l, "")
		if skipFrame(l) || !strings.Contains(l, ".") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// Fingerprint hashes source + code + normalised message + the top three stack frames. The first return value is the
// hex digest; the second a deterministic UUID derived from it, used as the report id.
func Fingerprint(source, code, message, stack string) (fp, id string) {
	parts := []string{source, code, NormalizeMessage(message)}
	parts = append(parts, TopFrames(stack, 3)...)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	fp = hex.EncodeToString(sum[:])
	h := fp[:32]
	id = h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
	return fp, id
}

// ShortID is the human-friendly report number quoted to users ("错误编号 ab12cd34").
func ShortID(id string) string {
	id = strings.ReplaceAll(id, "-", "")
	if len(id) > 8 {
		id = id[:8]
	}
	return id
}

// UserMessage is the sentence shown to users when an internal failure was recorded.
func UserMessage(id string) string { return "内部错误，错误编号 " + ShortID(id) }
