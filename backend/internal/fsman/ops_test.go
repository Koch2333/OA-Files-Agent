package fsman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupComparesNamesByNFCAndOptionalCase(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	os.MkdirAll(filepath.Join(m.Root, "d"), 0o755)
	os.WriteFile(filepath.Join(m.Root, "d", "Caf\u00e9.PDF"), []byte("x"), 0o644)
	old := CaseInsensitive
	defer func() { CaseInsensitive = old }()
	CaseInsensitive = true
	if e, ok, err := m.Lookup("d", "Cafe\u0301.pdf"); err != nil || !ok || e.Name != "Caf\u00e9.PDF" || e.Path != "d/Caf\u00e9.PDF" {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
	CaseInsensitive = false
	if _, ok, _ := m.Lookup("d", "Cafe\u0301.pdf"); ok {
		t.Fatal("case differs and the filesystem is case-sensitive")
	}
	if _, ok, _ := m.Lookup("d", "Cafe\u0301.PDF"); !ok {
		t.Fatal("NFD name must find its NFC twin")
	}
	if _, _, err := m.Lookup("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRestoreAsNeverOverwritesAndHashTrashedStreams(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	os.MkdirAll(filepath.Join(m.Root, "d"), 0o755)
	os.WriteFile(filepath.Join(m.Root, "d", "a.txt"), []byte("abc"), 0o644)
	it, err := m.Trash("d/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	sum, err := m.HashTrashed(context.Background(), it.ID)
	if err != nil || sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("%s %v", sum, err)
	}
	os.WriteFile(filepath.Join(m.Root, "d", "a.txt"), []byte("occupied"), 0o644)
	if err := m.RestoreAs(it.ID, "d/a.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("%v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.Root, "d", "a.txt")); string(b) != "occupied" {
		t.Fatal("clobbered")
	}
	if err := m.RestoreAs(it.ID, "new/dir/a.txt"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(m.Root, "new", "dir", "a.txt")); string(b) != "abc" {
		t.Fatal("content")
	}
	if err := m.RestoreAs(it.ID, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("already restored: %v", err)
	}
}

func TestCaseOnlyRenameAndWalkOrder(t *testing.T) {
	m := &Manager{Root: t.TempDir()}
	os.MkdirAll(filepath.Join(m.Root, "d", "sub"), 0o755)
	os.MkdirAll(filepath.Join(m.Root, "d", ".hid"), 0o755)
	for _, f := range []string{"d/b.txt", "d/a.txt", "d/sub/z.txt", "d/.hid/x"} {
		os.WriteFile(filepath.Join(m.Root, filepath.FromSlash(f)), []byte("1"), 0o644)
	}
	var seen []string
	if err := m.Walk(context.Background(), "d", func(e Entry) error { seen = append(seen, e.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	want := []string{"d/sub", "d/sub/z.txt", "d/a.txt", "d/b.txt"}
	if len(seen) != len(want) {
		t.Fatalf("%v", seen)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("%v", seen)
		}
	}
	old := CaseInsensitive
	defer func() { CaseInsensitive = old }()
	CaseInsensitive = true
	if _, err := m.Move("d/a.txt", "d/A.txt"); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	if ents, _ := os.ReadDir(filepath.Join(m.Root, "d")); len(ents) != 4 {
		t.Fatalf("%v", ents)
	}
	if _, err := m.Move("d/A.txt", "d/b.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("a different existing file still refuses: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := m.HashFile(ctx, "d/b.txt"); err == nil {
		t.Fatal("cancelled hashing must fail")
	}
}
