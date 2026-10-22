package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"strings"
)

const (
	RecoveryCodeCount = 10
	recoveryChars     = 12 // 60 bits of entropy, shown as XXXX-XXXX-XXXX
)

// NewRecoveryCodes returns plaintext codes (shown once) and their sha256 hashes (stored). High entropy makes a
// slow hash unnecessary; online guessing is bounded by rate limits and lockout.
func NewRecoveryCodes() (plain []string, hashes [][]byte, err error) {
	for i := 0; i < RecoveryCodeCount; i++ {
		raw := make([]byte, 8)
		if _, err = rand.Read(raw); err != nil {
			return nil, nil, err
		}
		s := b32.EncodeToString(raw)[:recoveryChars]
		code := s[0:4] + "-" + s[4:8] + "-" + s[8:12]
		plain = append(plain, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return plain, hashes, nil
}

// NormalizeRecoveryCode ignores case, spaces and dashes.
func NormalizeRecoveryCode(c string) string {
	return strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

func HashRecoveryCode(c string) []byte {
	h := sha256.Sum256([]byte(NormalizeRecoveryCode(c)))
	return h[:]
}
