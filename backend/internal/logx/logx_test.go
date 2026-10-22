package logx

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"oa-agent/backend/internal/buildinfo"
)

func TestDebugfOnlyInDebugBuilds(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(nil)
	Debugf("hello %d", 7)
	got := strings.Contains(buf.String(), "[DEBUG]") && strings.Contains(buf.String(), "hello 7")
	if got != (buildinfo.Debug && !quiet.Load()) {
		t.Fatalf("debug=%v quiet=%v output=%q", buildinfo.Debug, quiet.Load(), buf.String())
	}
}
