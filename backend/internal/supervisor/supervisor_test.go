package supervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadEnvFileOnlyOAKeysAndDoesNotOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "oa-agent.env")
	content := "\ufeff# comment\nOA_ARCHIVE_ROOT=\"D:\\归档\"\nPATH=evil\nOA_LISTEN_ADDR = 127.0.0.1:9000\nOA_PRESET=fromfile\nOA_EMPTY=\n"
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OA_PRESET", "fromenv")
	for _, k := range []string{"OA_ARCHIVE_ROOT", "OA_LISTEN_ADDR", "OA_EMPTY"} {
		os.Unsetenv(k)
	}
	before := os.Getenv("PATH")
	if err := LoadEnvFile(p); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("OA_ARCHIVE_ROOT") != `D:\归档` || os.Getenv("OA_LISTEN_ADDR") != "127.0.0.1:9000" {
		t.Fatalf("values not loaded: %q %q", os.Getenv("OA_ARCHIVE_ROOT"), os.Getenv("OA_LISTEN_ADDR"))
	}
	if os.Getenv("OA_PRESET") != "fromenv" {
		t.Fatal("existing environment must win over the file")
	}
	if os.Getenv("PATH") != before {
		t.Fatal("non-OA_ keys must be ignored")
	}
	if err := LoadEnvFile(filepath.Join(t.TempDir(), "missing.env")); err != nil {
		t.Fatalf("missing file should be fine: %v", err)
	}
}

func TestFreePort(t *testing.T) {
	a, err := FreePort()
	if err != nil || a < 1024 {
		t.Fatalf("port %d err %v", a, err)
	}
}

func TestStartWorkerRequiresExecutable(t *testing.T) {
	l := NewLayout(t.TempDir())
	if _, err := StartWorker(t.Context(), l); err == nil {
		t.Fatal("expected error for missing worker executable")
	}
}
