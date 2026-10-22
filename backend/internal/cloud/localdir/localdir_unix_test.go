//go:build unix

package localdir

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFifoAndNonRegular(t *testing.T) {
	e, _ := setup(t)
	fifo := filepath.Join(e.dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip(err)
	}
	os.WriteFile(filepath.Join(e.dir, "ok.txt"), []byte("x"), 0o644)
	p := open(t, e.dir, nil, "")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, _, err := p.Open(ctx, "pipe"); err == nil {
			t.Error("Open(fifo) must fail")
		}
		if _, err := p.Stat(ctx, "pipe"); err == nil {
			t.Error("Stat(fifo) must fail")
		}
		if err := p.Remove(ctx, "pipe"); err == nil {
			t.Error("Remove(fifo) must fail")
		}
		if err := put(p, "pipe", "x"); err == nil {
			t.Error("PutNew onto fifo must fail")
		}
		ents, _ := p.List(ctx, "")
		if len(ents) != 1 || ents[0].Name != "ok.txt" {
			t.Errorf("fifo listed: %+v", ents)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("blocked on fifo")
	}
	if _, err := os.Lstat(fifo); err != nil {
		t.Fatal("fifo removed")
	}
	// /dev/null style device through a path is unreachable by design (outside the root); nothing else to do.
}
