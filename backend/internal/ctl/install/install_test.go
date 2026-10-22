package install

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolveDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX path semantics")
	}
	marker := filepath.Join(t.TempDir(), "install_dir")
	EtcMarker = marker
	t.Setenv(DirEnv, "")
	if got := ResolveDir(""); got != DefaultDir {
		t.Errorf("default: %s", got)
	}
	os.WriteFile(marker, []byte("/srv/oa-agent\n"), 0o644)
	if got := ResolveDir(""); got != "/srv/oa-agent" {
		t.Errorf("marker: %s", got)
	}
	t.Setenv(DirEnv, "/env/dir/")
	if got := ResolveDir(""); got != "/env/dir" {
		t.Errorf("env: %s", got)
	}
	if got := ResolveDir("/flag/dir"); got != "/flag/dir" {
		t.Errorf("flag: %s", got)
	}
	os.WriteFile(marker, []byte("relative\n"), 0o644)
	t.Setenv(DirEnv, "")
	if got := ResolveDir(""); got != DefaultDir {
		t.Errorf("relative marker must be ignored: %s", got)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load("relative"); err == nil {
		t.Error("relative dir")
	}
	d := t.TempDir()
	if _, err := Load(d); err == nil {
		t.Error("empty dir")
	}
}

func TestLayers(t *testing.T) {
	cases := []struct{ in, want string }{
		{"docker-compose.yml", "docker-compose.yml:docker-compose.mounts.yml"},
		{"docker-compose.yml:docker-compose.build.yml", "docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml"},
		{"docker-compose.yml:docker-compose.mounts.yml", "docker-compose.yml:docker-compose.mounts.yml"},
		{"docker-compose.yml:docker-compose.https.yml", "docker-compose.yml:docker-compose.mounts.yml:docker-compose.https.yml"},
	}
	for _, c := range cases {
		if got := EnsureMountsLayer(c.in); got != c.want {
			t.Errorf("%s -> %s", c.in, got)
		}
	}
	on := SetHTTPSLayer("docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml", true)
	if on != "docker-compose.yml:docker-compose.mounts.yml:docker-compose.https.yml:docker-compose.build.yml" {
		t.Errorf("https on: %s", on)
	}
	if off := SetHTTPSLayer(on, false); off != "docker-compose.yml:docker-compose.mounts.yml:docker-compose.build.yml" {
		t.Errorf("https off: %s", off)
	}
}
