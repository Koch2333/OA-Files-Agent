package diag

import (
	"errors"
	"testing"
	"time"
)

func TestBackoffGrowsCapsAndResets(t *testing.T) {
	var b Backoff
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30}
	for i, w := range want {
		d, first := b.Fail("test", errors.New("down"))
		if d != w*time.Second {
			t.Fatalf("failure %d: %v, want %v", i+1, d, w*time.Second)
		}
		if first != (i == 0) {
			t.Fatalf("failure %d: first=%v", i+1, first)
		}
	}
	b.OK("test")
	if d, first := b.Fail("test", errors.New("down")); d != time.Second || !first {
		t.Fatalf("after recovery: %v %v", d, first)
	}
}
