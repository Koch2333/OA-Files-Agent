package oaadmin

import "testing"

// These are the literal outputs of cmd/oa-admin (keep in sync with its Printf calls).
func TestParsesRealOaAdminOutput(t *testing.T) {
	s := ParseSecret("password reset for alice\nTEMPORARY PASSWORD: Zx9-abc_def-123\n(shown once; all sessions of the user were ended; the password must be changed at next login)\n")
	if s.Password != "Zx9-abc_def-123" {
		t.Fatalf("password %q", s.Password)
	}
	if contains(s.Output, "Zx9-abc") {
		t.Fatalf("output leaks the password: %q", s.Output)
	}
	s = ParseSecret("created administrator bob\nTEMPORARY PASSWORD: Qq1-pw\n(shown once; the password must be changed at first login)\n")
	if s.Password != "Qq1-pw" {
		t.Fatalf("password %q", s.Password)
	}
}

func TestParsesRealListUsersJSON(t *testing.T) {
	us, err := ParseUsersJSON(`[{"username":"admin","role":"admin","status":"active","mfa":true,"locked":false,"locked_until":null},{"username":"u2","role":"viewer","status":"disabled","mfa":false,"locked":true,"locked_until":"2026-10-22T10:00:00Z"}]`)
	if err != nil || len(us) != 2 || !us[0].MFA || us[1].Status != "disabled" || !us[1].Locked {
		t.Fatalf("%v %+v", err, us)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestParsesRealListUsersTable(t *testing.T) {
	us, err := ParseUsersTable("USERNAME                 ROLE      STATUS    TOTP PASSK LOCKED UNTIL\nadmin                    admin     active    true 1     -\nbob                      viewer    disabled  false 0    2026-10-22 18:00:00\n")
	if err != nil || len(us) != 2 || us[0].Username != "admin" || us[1].Status != "disabled" {
		t.Fatalf("%v %+v", err, us)
	}
}
