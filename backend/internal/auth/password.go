// Package auth holds the login/account logic (passwords, TOTP, passkeys, sessions, lockout, setup, user admin) without
// any HTTP types, so the API handlers and the oa-admin command share one implementation.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost settings (OWASP-recommended class); tests may lower them.
var Argon2Params = struct {
	Time, Memory uint32
	Threads      uint8
}{Time: 2, Memory: 64 * 1024, Threads: 2}

const (
	argonKeyLen  = 32
	argonSaltLen = 16
	MaxPassword  = 128 // characters
)

// HashPassword returns a PHC string: $argon2id$v=19$m=…,t=…,p=…$salt$hash.
func HashPassword(plain string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := Argon2Params
	key := argon2.IDKey([]byte(plain), salt, p.Time, p.Memory, p.Threads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.Memory, p.Time, p.Threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

var errBadHash = errors.New("unsupported password hash")

func parseHash(h string) (mem, t uint32, threads uint8, salt, key []byte, err error) {
	parts := strings.Split(h, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, errBadHash
	}
	var ver int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &ver); err != nil || ver != argon2.Version {
		return 0, 0, 0, nil, nil, errBadHash
	}
	var th uint32
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &t, &th); err != nil || th == 0 || th > 255 || mem == 0 || mem > 1<<20 || t == 0 || t > 20 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	threads = uint8(th)
	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil || len(salt) < 8 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil || len(key) < 16 {
		return 0, 0, 0, nil, nil, errBadHash
	}
	return mem, t, threads, salt, key, nil
}

// VerifyPassword checks plain against a PHC string in constant time. A malformed hash never verifies.
func VerifyPassword(hash, plain string) bool {
	mem, t, th, salt, key, err := parseHash(hash)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, t, mem, th, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

// DummyVerify burns the time of one real verification (used for unknown users) and always returns false.
func DummyVerify(plain string) bool {
	p := Argon2Params
	argon2.IDKey([]byte(plain), dummySalt, p.Time, p.Memory, p.Threads, argonKeyLen)
	return false
}

var dummySalt = []byte("oa-agent-dummy-salt")

// weak passwords that satisfy length rules but are guessed first.
var commonPasswords = []string{"password", "passw0rd", "123456789", "12345678", "qwertyuiop", "qwerty123", "iloveyou", "letmein", "welcome1", "admin123", "administrator", "changeme", "abcdefgh", "11111111", "00000000"}

// CheckPasswordPolicy enforces length (in characters), not-the-username, no trivially common passwords and at least
// two character classes. It returns a user-facing reason in Chinese, or "" when the password is acceptable.
func CheckPasswordPolicy(pw, username string, minLen int) string {
	n := utf8.RuneCountInString(pw)
	if n < minLen {
		return fmt.Sprintf("密码至少需要 %d 个字符", minLen)
	}
	if n > MaxPassword {
		return fmt.Sprintf("密码不能超过 %d 个字符", MaxPassword)
	}
	low := strings.ToLower(pw)
	if u := strings.ToLower(strings.TrimSpace(username)); len(u) >= 3 && strings.Contains(low, u) {
		return "密码不能包含用户名"
	}
	for _, c := range commonPasswords {
		if strings.Contains(low, c) {
			return "密码过于常见，请换一个"
		}
	}
	first, same := []rune(pw)[0], true
	for _, r := range pw {
		if r != first {
			same = false
			break
		}
	}
	if same {
		return "密码不能全部是同一个字符"
	}
	var lower, upper, digit, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			other = true
		}
	}
	classes := 0
	for _, b := range []bool{lower, upper, digit, other} {
		if b {
			classes++
		}
	}
	if classes < 2 {
		return "密码需要包含至少两类字符（大小写字母、数字、符号）"
	}
	return ""
}
