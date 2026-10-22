package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestWriteAtomic(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "f")
	if err := WriteAtomic(p, []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtomic(p, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "two" {
		t.Errorf("content %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
			t.Errorf("mode %v", st.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(d)
	if len(ents) != 1 {
		t.Errorf("temp files left behind: %v", ents)
	}
	// failure (directory missing) must not leave junk or panic
	if err := WriteAtomic(filepath.Join(d, "nodir", "f"), []byte("x"), 0o600); err == nil {
		t.Error("expected error")
	}
}

func TestBackupPruneRestore(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "mounts.json")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	i := 0
	Now = func() time.Time { i++; return base.Add(time.Duration(i) * time.Second) }
	defer func() { Now = time.Now }()
	if b, err := Backup(p, 3); err != nil || b != "" {
		t.Fatalf("missing source: %q %v", b, err)
	}
	var last string
	for n := 0; n < 5; n++ {
		os.WriteFile(p, []byte{byte('a' + n)}, 0o644)
		var err error
		if last, err = Backup(p, 3); err != nil {
			t.Fatal(err)
		}
	}
	m, _ := filepath.Glob(p + ".bak-*")
	if len(m) != 3 {
		t.Fatalf("prune: %v", m)
	}
	if !strings.Contains(last, ".bak-20260101-0000") {
		t.Errorf("name %s", last)
	}
	os.WriteFile(p, []byte("new"), 0o644)
	if err := Restore(last, p, 0o644); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "e" {
		t.Errorf("restore content %q", b)
	}
	// empty backup path = file did not exist before: remove
	if err := Restore("", p, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("file should be removed")
	}
	if err := Restore("", p, 0o644); err != nil {
		t.Error("removing a missing file must be fine")
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		p, c string
		want bool
	}{{"/a", "/a", true}, {"/a", "/a/b", true}, {"/a", "/ab", false}, {"/", "/x", true}, {"/a/b", "/a", false}}
	for _, c := range cases {
		if Within(c.p, c.c) != c.want {
			t.Errorf("%v", c)
		}
	}
}
