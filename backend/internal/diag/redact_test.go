package diag

import (
	"strings"
	"testing"
)

// Each sample plants a canary secret; after Redact the canary must be gone.
func TestRedactSecretsCorpus(t *testing.T) {
	cases := []struct{ name, in, canary string }{
		{"openai key", "call failed: Incorrect API key provided: sk-proj-AbCdEf0123456789abcdef", "AbCdEf0123456789abcdef"},
		{"anthropic key", "x sk-ant-api03-ZZZZ1111yyyy2222XXXX3333 y", "ZZZZ1111yyyy2222XXXX3333"},
		{"bearer", "Authorization: Bearer abc123.def456-ghi789", "abc123.def456-ghi789"},
		{"bearer inline", "retry with bearer 9f8e7d6c5b4a39281706", "9f8e7d6c5b4a39281706"},
		{"authorization basic", "authorization: Basic dXNlcjpwYXNzd29yZA==", "dXNlcjpwYXNzd29yZA"},
		{"cookie header", "Cookie: oa_session=SESSIONCANARY123; theme=dark", "SESSIONCANARY123"},
		{"set-cookie", "Set-Cookie: oa_session=SETCOOKIECANARY99; Path=/; HttpOnly", "SETCOOKIECANARY99"},
		{"oa_session pair", `got oa_session=CANARYSESS77 in url`, "CANARYSESS77"},
		{"oa_session json", `{"oa_session":"CANARYSESS78"}`, "CANARYSESS78"},
		{"url password", "dial postgres://oa:hunter2canary@127.0.0.1:5432/oa failed", "hunter2canary"},
		{"url token only user", "GET https://tok_CANARY99@example.com/x", "tok_CANARY99"},
		{"json password", `{"username":"bob","password":"p@ss w0rd canary!"}`, "w0rd canary"},
		{"json password escaped quote", `{"password":"a\"b-CANARYQ"}`, "CANARYQ"},
		{"form password", "username=bob&password=Canary%21Pass&next=/", "Canary%21Pass"},
		{"api_key kv", "config api_key=CANARY_API_KEY_1 loaded", "CANARY_API_KEY_1"},
		{"api-key colon", "api-key: CANARYAPIKEY2", "CANARYAPIKEY2"},
		{"secret", "secret = 'CANARY secret value'", "CANARY secret value"},
		{"client_secret", "client_secret=CANARYCLIENT3", "CANARYCLIENT3"},
		{"refresh token", "refresh_token: CANARYREFRESH4", "CANARYREFRESH4"},
		{"go map", "map[password:CANARYMAP5 user:bob]", "CANARYMAP5"},
		{"setup token", "SETUP TOKEN: ABCDEFGH23456723ABCDEFGH234567ABCDEFGH", "ABCDEFGH23456723ABCDEFGH234567ABCDEFGH"},
		{"totp secret", "totp secret JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP enrolled", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"},
		{"otpauth", "otpauth://totp/OA:bob?secret=CANARYOTP6&issuer=OA", "CANARYOTP6"},
		{"recovery code", "using recovery code ABCD-2345-WXYZ for bob", "ABCD-2345-WXYZ"},
		{"long hex", "token=deadbeef" + strings.Repeat("0123456789abcdef", 3), "0123456789abcdef0123456789abcdef"},
		{"long hex bare", "key " + strings.Repeat("a1b2c3d4", 8), "a1b2c3d4a1b2c3d4"},
		{"base64 blob", "blob QWxhZGRpbjpvcGVuIHNlc2FtZVRoaXNJc0FDYW5hcnlTZWNyZXQxMjM0NQ", "QWxhZGRpbjpvcGVuIHNlc2FtZVRoaXNJc0FDYW5hcnlTZWNyZXQxMjM0NQ"},
		{"jwt", "id eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJDQU5BUlkifQ.sigCANARYvalue", "eyJzdWIiOiJDQU5BUlkifQ"},
		{"aws", "key AKIAABCDEFGHIJKLMNOP end", "AKIAABCDEFGHIJKLMNOP"},
		{"github", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnopqrstuvwxyz0123456789"},
		{"pem", "-----BEGIN PRIVATE KEY-----\nMIIEvgIBADANCANARYKEY\n-----END PRIVATE KEY-----", "MIIEvgIBADANCANARYKEY"},
		{"x-api-key header", "X-Api-Key: CANARYXAPI8", "CANARYXAPI8"},
		{"csrf header", "X-CSRF-Token: CANARYCSRF9", "CANARYCSRF9"},
		{"password in error", `login failed for password="CANARYPW10"`, "CANARYPW10"},
		{"quoted token single", "token='CANARYTOK11'", "CANARYTOK11"},
		{"upper key", "PASSWORD=CANARYUP12", "CANARYUP12"},
		{"nested key name", "OA_LLM_API_KEY=CANARYENV13", "CANARYENV13"},
		{"session_id", "session_id=CANARYSID14", "CANARYSID14"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := Redact(c.in)
			if strings.Contains(out, c.canary) {
				t.Fatalf("canary %q survived:\n in: %s\nout: %s", c.canary, c.in, out)
			}
			if !strings.Contains(out, Mask) {
				t.Fatalf("no mask in %q", out)
			}
		})
	}
}

func TestRedactKeepsUsefulText(t *testing.T) {
	keep := []string{
		"database connection failed",
		"rid=0123456789abcdef0123456789abcdef task 5",
		"oa-agent/backend/internal/pipeline.(*Service).process(0xc000123456)",
		"/mnt/d/OA Files Agent/oa-agent/backend/internal/pipeline/service.go:123 +0x1d",
		"文件 通知_2024.docx 解析失败：加密文件",
		"GET /v1/files/1b9d6bcd-bbfd-4b2d-9b5d-ab8dfbbd4bed 500",
		"context deadline exceeded after 30s",
		"footprint=3 sasha otpion hotpath",
	}
	for _, s := range keep {
		if got := RedactSecrets(s); got != s {
			t.Errorf("over-redacted:\n in: %s\nout: %s", s, got)
		}
	}
}

func TestRedactPersonalData(t *testing.T) {
	cases := []struct{ in, gone string }{
		{`C:\Users\Alice\AppData\Local\oa\log.txt`, "Alice"},
		{`c:/users/bob/x`, "bob"},
		{"/home/carol/work/x.go:12", "carol"},
		{"/Users/dave/work", "dave"},
		{"/mnt/c/Users/Erin/Desktop", "Erin"},
		{"contact zhang.san+oa@example.co.uk now", "zhang.san"},
	}
	for _, c := range cases {
		if out := Redact(c.in); strings.Contains(out, c.gone) {
			t.Errorf("%q survived in %q", c.gone, out)
		}
	}
	if out := RedactSecrets("/home/carol/x a@b.cc"); !strings.Contains(out, "carol") || !strings.Contains(out, "a@b.cc") {
		t.Errorf("RedactSecrets must leave personal data alone: %s", out)
	}
}

func TestRegisterSecret(t *testing.T) {
	defer ResetSecrets()
	RegisterSecret("short", "plain-opaque-value-1", "  ")
	out := Redact("the value plain-opaque-value-1 and short appear")
	if strings.Contains(out, "plain-opaque-value-1") || !strings.Contains(out, "short") {
		t.Fatalf("got %q", out)
	}
}

func TestRedactValue(t *testing.T) {
	in := map[string]any{
		"password": "x", "Api_Key": "y", "path": `C:\Users\Zed\f`, "n": 3, "ok": true,
		"nested": map[string]any{"authorization": "Bearer abcdefgh12345", "msg": "token=CANARYV1"},
		"list":   []any{"sk-abcdefghijklmnop1234", map[string]any{"secret": "s"}},
	}
	out := RedactValue(in).(map[string]any)
	b := strings.ToLower(Redact(strings.Join([]string{out["path"].(string)}, "")))
	if strings.Contains(b, "zed") || out["password"] != Mask || out["Api_Key"] != Mask || out["n"] != 3 {
		t.Fatalf("%v", out)
	}
	s := sprint(out)
	for _, bad := range []string{"abcdefgh12345", "CANARYV1", "abcdefghijklmnop1234"} {
		if strings.Contains(s, bad) {
			t.Errorf("%s survived: %s", bad, s)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := Truncate("你好世界", 2); got != "你好…" {
		t.Fatal(got)
	}
	if Truncate("abc", 5) != "abc" {
		t.Fatal()
	}
}
