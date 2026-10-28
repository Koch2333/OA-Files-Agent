package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsASCII(t *testing.T) {
	for s, want := range map[string]bool{`C:\Program Files\pg`: true, "/opt/pg": true, `F:\oa文件智能归档\pg`: false, "": true} {
		if got := isASCII(s); got != want {
			t.Errorf("isASCII(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestInitdbShareDir(t *testing.T) {
	root := t.TempDir()
	// plain ASCII install path: initdb finds its own files, no -L
	asciiPG := filepath.Join(root, "pg")
	if err := os.MkdirAll(filepath.Join(asciiPG, "share"), 0o700); err != nil {
		t.Fatal(err)
	}
	if d, cleanup, err := initdbShareDir(Layout{PGDir: asciiPG}); err != nil || d != "" {
		t.Fatalf("ascii path: %q %v", d, err)
	} else {
		cleanup()
	}
	// non-ASCII install path: share is mirrored into an ASCII directory and removed again by cleanup
	cn := filepath.Join(root, "oa文件智能归档", "pg")
	sh := filepath.Join(cn, "share", "timezonesets")
	if err := os.MkdirAll(sh, 0o700); err != nil {
		t.Fatal(err)
	}
	for p, c := range map[string]string{filepath.Join(cn, "share", "postgres.bki"): "bki", filepath.Join(sh, "Default"): "tz"} {
		if err := os.WriteFile(p, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PUBLIC", root) // t.TempDir is ASCII on the test hosts; the helper only needs an ASCII base
	if !isASCII(root) {
		t.Skip("temp directory is not ASCII")
	}
	d, cleanup, err := initdbShareDir(Layout{PGDir: cn})
	if err != nil || d == "" || !isASCII(d) {
		t.Fatalf("got %q %v", d, err)
	}
	for rel, want := range map[string]string{"postgres.bki": "bki", filepath.Join("timezonesets", "Default"): "tz"} {
		b, err := os.ReadFile(filepath.Join(d, rel))
		if err != nil || string(b) != want {
			t.Errorf("%s: %q %v", rel, b, err)
		}
	}
	cleanup()
	if _, err := os.Stat(d); !os.IsNotExist(err) {
		t.Error("scratch directory not removed")
	}
}
