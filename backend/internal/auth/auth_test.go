package auth

import (
	"strings"
	"testing"
	"time"
)

func init() { Argon2Params.Memory, Argon2Params.Time = 1024, 1 } // fast tests; production defaults are untouched

func TestPasswordHashVerify(t *testing.T) {
	h, err := HashPassword("Correct-Horse-9-Battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=") || strings.Contains(h, "Correct") {
		t.Fatalf("not a PHC argon2id string: %s", h)
	}
	if !VerifyPassword(h, "Correct-Horse-9-Battery") || VerifyPassword(h, "Correct-Horse-9-Batterx") || VerifyPassword(h, "") {
		t.Fatal("verify")
	}
	h2, _ := HashPassword("Correct-Horse-9-Battery")
	if h == h2 {
		t.Fatal("salt must be random")
	}
	for _, bad := range []string{"", "plain", "$argon2id$v=19$m=1,t=1,p=1$AAAA$AAAA", "$argon2i$v=19$m=1024,t=1,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 22), "$argon2id$v=19$m=99999999,t=1,p=1$c2FsdHNhbHQ$" + strings.Repeat("A", 22)} {
		if VerifyPassword(bad, "x") {
			t.Fatalf("malformed hash verified: %q", bad)
		}
	}
	if DummyVerify("anything") {
		t.Fatal("dummy verify must never succeed")
	}
}

func TestPasswordPolicy(t *testing.T) {
	for _, tc := range []struct {
		pw, user string
		min      int
		ok       bool
	}{
		{"Correct-Horse-9-Battery", "alice", 12, true},
		{"short1A", "alice", 12, false},
		{"alicealice-Secret1", "alice", 12, false},    // contains username
		{"Password12345678", "bob", 12, false},        // common
		{"aaaaaaaaaaaaaaaa", "bob", 12, false},        // single character
		{"abcdefghijklmnopq", "bob", 12, false},       // one class only
		{"密码密码密码密码密码密码密码密码", "bob", 12, false},        // one class (other)
		{"密码密码密码密码密码密码密码密码x", "bob", 12, true},        // length counts characters, two classes
		{strings.Repeat("aB1", 50), "bob", 12, false}, // > 128 chars
	} {
		if got := CheckPasswordPolicy(tc.pw, tc.user, tc.min) == ""; got != tc.ok {
			t.Errorf("%q: ok=%v want %v (%s)", tc.pw, got, tc.ok, CheckPasswordPolicy(tc.pw, tc.user, tc.min))
		}
	}
}

func TestTempPasswordMeetsPolicy(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		p, err := TempPassword()
		if err != nil || CheckPasswordPolicy(p, "someone", 12) != "" || seen[p] {
			t.Fatalf("%q %v", p, err)
		}
		seen[p] = true
	}
}

// RFC 6238 appendix B (SHA-1, secret "12345678901234567890"); our codes are the 6-digit truncation of the 8-digit vectors.
func TestTOTPRFC6238Vectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for unix, want := range map[int64]string{59: "287082", 1111111109: "081804", 1111111111: "050471", 1234567890: "005924", 2000000000: "279037", 20000000000: "353130"} {
		got, err := TOTPCode(secret, time.Unix(unix, 0))
		if err != nil || got != want {
			t.Errorf("T=%d: got %s want %s (%v)", unix, got, want, err)
		}
	}
}

func TestTOTPWindowAndReplay(t *testing.T) {
	secret, uri, err := NewTOTPSecret("OA Agent", "alice")
	if err != nil || !strings.HasPrefix(uri, "otpauth://totp/") || !strings.Contains(uri, "issuer=OA+Agent") && !strings.Contains(uri, "issuer=OA%20Agent") || !strings.Contains(uri, "secret="+secret) || !strings.Contains(uri, "digits=6") || !strings.Contains(uri, "period=30") {
		t.Fatalf("uri %s %v", uri, err)
	}
	now := time.Unix(1_700_000_000, 0)
	cur := TOTPStep(now)
	code := func(step int64) string { c, _ := TOTPCode(secret, time.Unix(step*30, 0)); return c }
	for _, step := range []int64{cur - 1, cur, cur + 1} {
		if got, ok := VerifyTOTP(secret, code(step), now, 0); !ok || got != step {
			t.Errorf("step %d inside the ±1 window rejected", step)
		}
	}
	for _, step := range []int64{cur - 2, cur + 2} {
		if _, ok := VerifyTOTP(secret, code(step), now, 0); ok {
			t.Errorf("step %d outside the window accepted", step)
		}
	}
	// replay: a step that was used (or any older one) is refused
	if _, ok := VerifyTOTP(secret, code(cur), now, cur); ok {
		t.Error("replay of the last used step accepted")
	}
	if _, ok := VerifyTOTP(secret, code(cur-1), now, cur); ok {
		t.Error("older step after a newer one accepted")
	}
	if _, ok := VerifyTOTP(secret, code(cur+1), now, cur); !ok {
		t.Error("newer step must still work")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "000000"} {
		if _, ok := VerifyTOTP(secret, bad, now, 0); ok && bad != "000000" {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, ok := VerifyTOTP(secret, " "+code(cur)[:3]+" "+code(cur)[3:], now, 0); !ok {
		t.Error("spaces in a typed code should be tolerated")
	}
}

func TestRecoveryCodes(t *testing.T) {
	plain, hashes, err := NewRecoveryCodes()
	if err != nil || len(plain) != 10 || len(hashes) != 10 {
		t.Fatal(err, len(plain))
	}
	seen := map[string]bool{}
	for i, c := range plain {
		if len(c) != 14 || c[4] != '-' || seen[c] {
			t.Fatalf("code format %q", c)
		}
		seen[c] = true
		if string(HashRecoveryCode(strings.ToLower(strings.ReplaceAll(c, "-", " ")))) != string(hashes[i]) {
			t.Fatal("normalisation must ignore case, spaces and dashes")
		}
		if strings.Contains(string(hashes[i]), c) {
			t.Fatal("hash must not contain the code")
		}
	}
}

func TestCSRFAndTokens(t *testing.T) {
	tok, err := NewToken()
	if err != nil || len(tok) != 43 {
		t.Fatal(tok, err)
	}
	if HashToken(tok) == tok || len(HashToken(tok)) != 64 {
		t.Fatal("hash")
	}
	c := CSRFToken(tok)
	if !CheckCSRF(tok, c) || CheckCSRF(tok, "") || CheckCSRF(tok, c+"x") || CheckCSRF("other", c) || c == tok {
		t.Fatal("csrf")
	}
	a, _ := NewSetupToken()
	b, _ := NewSetupToken()
	if a == b || len(a) != 39 || NormalizeSetupToken(" "+strings.ToLower(a[:5])+"-"+a[5:]+"\n") != a {
		t.Fatalf("setup token %q", a)
	}
}

func TestLimiterBucketsRefillAndSweep(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(3, 10*time.Second, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k"); !ok {
			t.Fatal("burst")
		}
	}
	ok, wait := l.Allow("k")
	if ok || wait < time.Second || wait > 10*time.Second {
		t.Fatalf("expected throttling with Retry-After, got %v %v", ok, wait)
	}
	if ok, _ := l.Allow("other"); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(10 * time.Second)
	if ok, _ := l.Allow("k"); !ok {
		t.Fatal("one token after one interval")
	}
	if ok, _ := l.Allow("k"); ok {
		t.Fatal("only one token earned")
	}
	now = now.Add(5 * time.Minute)
	l.Allow("z") // triggers the sweep: idle (full) buckets disappear
	if l.Size() != 1 {
		t.Fatalf("idle buckets not cleaned: %d", l.Size())
	}
	l.MaxKeys = 1
	if ok, _ := l.Allow("brand-new"); ok {
		t.Fatal("key flooding must not grow memory")
	}
}

func TestGhostLockMimicsLockout(t *testing.T) {
	var g ghostLocks
	now := time.Unix(5000, 0)
	for i := 0; i < 4; i++ {
		if _, locked := g.fail("nobody", 5, time.Minute, now); locked {
			t.Fatal("locked too early")
		}
	}
	until, locked := g.fail("nobody", 5, time.Minute, now)
	if !locked || !until.Equal(now.Add(time.Minute)) {
		t.Fatal("5th failure must lock")
	}
	if _, l := g.locked("nobody", now.Add(30*time.Second)); !l {
		t.Fatal("still locked")
	}
	if _, l := g.locked("nobody", now.Add(2*time.Minute)); l {
		t.Fatal("lock must expire")
	}
}

func TestValidateRP(t *testing.T) {
	good := [][2]any{
		{"example.com", []string{"https://example.com"}}, {"example.com", []string{"https://oa.example.com", "https://example.com:8443"}},
		{"localhost", []string{"http://localhost:8080"}}, {"", []string(nil)},
	}
	for _, g := range good {
		if err := ValidateRP(g[0].(string), g[1].([]string)); err != nil {
			t.Errorf("%v: %v", g, err)
		}
	}
	bad := [][2]any{
		{"example.com", []string{"http://example.com"}},          // not a secure context
		{"example.com", []string{"https://evil.com"}},            // not within the RP ID
		{"example.com", []string{"https://notexample.com"}},      // suffix must be on a label boundary
		{"example.com", []string{"https://example.com/"}},        // exact origin only
		{"example.com", []string{"https://*.example.com"}},       // no wildcards
		{"127.0.0.1", []string{"https://127.0.0.1"}},             // IP is not a valid RP ID
		{"https://example.com", []string{"https://example.com"}}, // RP ID is a bare domain
		{"example.com", []string(nil)}, {"", []string{"https://example.com"}},
	}
	for _, b := range bad {
		if err := ValidateRP(b[0].(string), b[1].([]string)); err == nil {
			t.Errorf("accepted %v", b)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	for _, n := range []string{"alice", "a.b-c_d@corp", "李"} {
		if (ValidateUsername(n) == nil) != (n != "李") {
			t.Errorf("%q", n)
		}
	}
	for _, n := range []string{"ab", "has space", "x/y", "local", "LOCAL", strings.Repeat("a", 65)} {
		if ValidateUsername(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}
