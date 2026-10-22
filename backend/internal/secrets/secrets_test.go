package secrets

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndTamperDetection(t *testing.T) {
	b, err := Open("", filepath.Join(t.TempDir(), "sub", "k"))
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := b.Seal([]byte("sk-secret"))
	s2, _ := b.Seal([]byte("sk-secret"))
	if bytes.Equal(s1, s2) || bytes.Contains(s1, []byte("sk-secret")) {
		t.Fatal("ciphertext must be randomised and not contain the plaintext")
	}
	if p, err := b.Unseal(s1); err != nil || string(p) != "sk-secret" {
		t.Fatalf("unseal: %v %q", err, p)
	}
	s1[len(s1)-1] ^= 1
	if _, err := b.Unseal(s1); err != ErrCorrupt {
		t.Fatalf("tampering must be detected: %v", err)
	}
	if _, err := b.Unseal([]byte("short")); err != ErrCorrupt {
		t.Fatal("short input")
	}
}

func TestKeyFileIsCreatedOnceAndReused(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k")
	a, err := Open("", p)
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := a.Seal([]byte("x"))
	b, err := Open("", p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Unseal(sealed); err != nil || string(got) != "x" {
		t.Fatalf("second open must use the same key: %v", err)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("key file must be private: %v", fi.Mode())
	}
	c, _ := Open("", filepath.Join(t.TempDir(), "other"))
	if _, err := c.Unseal(sealed); err != ErrCorrupt {
		t.Fatal("a different key must not decrypt")
	}
}

func TestEnvKeyAndValidation(t *testing.T) {
	if _, err := Open(strings.Repeat("ab", 32), ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"zz", strings.Repeat("ab", 31), "not hex" + strings.Repeat("0", 60)} {
		if _, err := Open(bad, ""); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	p := filepath.Join(t.TempDir(), "k")
	os.WriteFile(p, []byte("garbage"), 0o600)
	if _, err := Open("", p); err == nil {
		t.Fatal("a damaged key file must be an error, never silently replaced")
	}
}
