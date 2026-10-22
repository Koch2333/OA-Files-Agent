package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
)

// Clock is injectable so expiry logic can be tested without sleeping.
type Clock func() time.Time

// NewToken returns 32 random bytes as an unpadded base64url string (the cookie / invitation / challenge value).
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken is what the database stores: hex(sha256(token)).
func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

// CSRFToken derives the per-session CSRF token from the session cookie value, so it can be re-issued by /me without
// being stored; knowing it does not reveal the cookie.
func CSRFToken(sessionToken string) string {
	m := hmac.New(sha256.New, []byte(sessionToken))
	m.Write([]byte("oa-agent csrf v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// CheckCSRF compares in constant time.
func CheckCSRF(sessionToken, given string) bool {
	if given == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(CSRFToken(sessionToken)), []byte(given)) == 1
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSetupToken is 24 random bytes in base32 (39 chars), shown once in the log / by oa-admin.
func NewSetupToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return b32.EncodeToString(b), nil
}

// NormalizeSetupToken tolerates the spaces/dashes and lower case a human may introduce when copying the token.
func NormalizeSetupToken(t string) string {
	return strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\n", "", "\r", "", "\t", "").Replace(t))
}

// EqualHash compares two hex hashes in constant time.
func EqualHash(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// TempPassword generates a one-time password that satisfies the policy (mixed classes, 20 chars).
func TempPassword() (string, error) {
	const lower, upper, digits, symbols = "abcdefghijkmnopqrstuvwxyz", "ABCDEFGHJKLMNPQRSTUVWXYZ", "23456789", "-_.!"
	all := lower + upper + digits
	pick := func(set string) (byte, error) {
		var b [1]byte
		for {
			if _, err := rand.Read(b[:]); err != nil {
				return 0, err
			}
			if int(b[0]) < 256-256%len(set) {
				return set[int(b[0])%len(set)], nil
			}
		}
	}
	out := make([]byte, 0, 20)
	for _, set := range []string{lower, upper, digits, symbols} {
		c, err := pick(set)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	for len(out) < 20 {
		c, err := pick(all)
		if err != nil {
			return "", err
		}
		out = append(out, c)
	}
	// shuffle (Fisher-Yates with crypto/rand)
	for i := len(out) - 1; i > 0; i-- {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		j := int(b[0])<<8 | int(b[1])
		j %= i + 1
		out[i], out[j] = out[j], out[i]
	}
	return string(out), nil
}
