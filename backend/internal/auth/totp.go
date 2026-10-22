package auth

import (
	"crypto/subtle"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/hotp"
	"github.com/pquerna/otp/totp"
)

const (
	TOTPPeriod = 30
	TOTPDigits = 6
)

var totpOpts = totp.ValidateOpts{Period: TOTPPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// NewTOTPSecret creates a 160-bit secret and its otpauth:// URI (issuer is the relying-party display name).
func NewTOTPSecret(issuer, account string) (secret, uri string, err error) {
	if strings.TrimSpace(issuer) == "" {
		issuer = "OA Agent"
	}
	k, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account, Period: TOTPPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1, SecretSize: 20})
	if err != nil {
		return "", "", err
	}
	return k.Secret(), k.URL(), nil
}

// TOTPStep is the RFC 6238 time step (T0 = 0, 30 s).
func TOTPStep(t time.Time) int64 { return t.Unix() / TOTPPeriod }

// TOTPCode computes the code of a base32 secret at time t (used by tests and the e2e driver).
func TOTPCode(secret string, t time.Time) (string, error) {
	return totp.GenerateCodeCustom(secret, t, totp.ValidateOpts{Period: TOTPPeriod, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
}

// VerifyTOTP accepts a code of the previous, current or next step (±1 window) whose step is strictly newer than
// lastStep (replay protection). It returns the matched step. All candidate steps are compared in constant time.
func VerifyTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.NewReplacer(" ", "", "-", "").Replace(code)
	if len(code) != TOTPDigits {
		return 0, false
	}
	cur := TOTPStep(now)
	var matched int64
	ok := false
	for _, step := range []int64{cur - 1, cur, cur + 1} {
		want, err := hotp.GenerateCodeCustom(secret, uint64(step), hotp.ValidateOpts{Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
		if err != nil {
			return 0, false
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 && step > lastStep {
			if !ok || step > matched {
				matched = step
			}
			ok = true
		}
	}
	return matched, ok
}
