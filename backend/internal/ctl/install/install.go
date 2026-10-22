// Package install locates an OA Agent installation and exposes its .env-derived settings.
package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"oa-agent/backend/internal/ctl/envfile"
)

const (
	DefaultDir       = "/opt/oa-agent"
	MountsFile       = "mounts.json"
	OverrideFile     = "docker-compose.mounts.yml"
	BaseCompose      = "docker-compose.yml"
	HTTPSCompose     = "docker-compose.https.yml"
	DirEnv           = "OA_AGENT_DIR"
	defaultEtcMarker = "/etc/oa-agent/install_dir"
)

// EtcMarker is where the installer records the chosen directory; a variable for tests.
var EtcMarker = defaultEtcMarker

// ResolveDir picks the install dir: --dir, then OA_AGENT_DIR, then the installer marker, then the default.
func ResolveDir(flagDir string) string {
	if flagDir != "" {
		return filepath.Clean(flagDir)
	}
	if v := os.Getenv(DirEnv); v != "" {
		return filepath.Clean(v)
	}
	if b, err := os.ReadFile(EtcMarker); err == nil {
		if d := strings.TrimSpace(string(b)); d != "" && filepath.IsAbs(d) {
			return filepath.Clean(d)
		}
	}
	return DefaultDir
}

// Install is one loaded installation.
type Install struct {
	Dir string
	Env *envfile.File
}

// ErrNotInstalled means .env or docker-compose.yml is missing.
var ErrNotInstalled = errors.New("no OA Agent installation found")

// Load reads .env and checks the marker files.
func Load(dir string) (*Install, error) {
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("install dir must be absolute: %s", dir)
	}
	env, err := envfile.Load(filepath.Join(dir, ".env"))
	if err != nil {
		return nil, fmt.Errorf("%w in %s (.env: %v)", ErrNotInstalled, dir, err)
	}
	if _, err := os.Stat(filepath.Join(dir, BaseCompose)); err != nil {
		return nil, fmt.Errorf("%w in %s (%s missing)", ErrNotInstalled, dir, BaseCompose)
	}
	return &Install{Dir: dir, Env: env}, nil
}

func (i *Install) EnvPath() string      { return filepath.Join(i.Dir, ".env") }
func (i *Install) MountsPath() string   { return filepath.Join(i.Dir, MountsFile) }
func (i *Install) OverridePath() string { return filepath.Join(i.Dir, OverrideFile) }

// ReloadEnv re-reads .env after it was changed.
func (i *Install) ReloadEnv() error {
	e, err := envfile.Load(i.EnvPath())
	if err != nil {
		return err
	}
	i.Env = e
	return nil
}

// ComposeFile is the COMPOSE_FILE value (default: the base file only).
func (i *Install) ComposeFile() string { return i.Env.Value("COMPOSE_FILE", BaseCompose) }

// Profiles is COMPOSE_PROFILES.
func (i *Install) Profiles() string { return i.Env.Value("COMPOSE_PROFILES", "") }

// HTTPS reports whether caddy is part of the stack (profile or compose layer).
func (i *Install) HTTPS() bool {
	for _, p := range strings.Split(i.Profiles(), ",") {
		if strings.TrimSpace(p) == "https" {
			return true
		}
	}
	return strings.Contains(i.ComposeFile(), HTTPSCompose)
}

// Services are the containers a healthy stack must have.
func (i *Install) Services() []string {
	s := []string{"postgres", "api", "worker"}
	if i.HTTPS() {
		s = append(s, "caddy")
	}
	return s
}

// Port is the published API port.
func (i *Install) Port() string { return i.Env.Value("OA_PORT", "8080") }

// URL is the public URL for display.
func (i *Install) URL() string { return i.Env.Value("OA_PUBLIC_URL", "http://localhost:"+i.Port()) }

// DataDir resolves OA_DATA_DIR (relative paths are relative to the install dir).
func (i *Install) DataDir() string {
	d := i.Env.Value("OA_DATA_DIR", "./data")
	if filepath.IsAbs(d) {
		return filepath.Clean(d)
	}
	return filepath.Join(i.Dir, d)
}

// EnsureMountsLayer returns COMPOSE_FILE with the generated override inserted right after the base file.
func EnsureMountsLayer(composeFile string) string {
	parts := strings.Split(composeFile, ":")
	for _, p := range parts {
		if p == OverrideFile {
			return composeFile
		}
	}
	out := []string{}
	done := false
	for _, p := range parts {
		out = append(out, p)
		if p == BaseCompose && !done {
			out = append(out, OverrideFile)
			done = true
		}
	}
	if !done {
		out = append([]string{OverrideFile}, out...)
	}
	return strings.Join(out, ":")
}

// SetHTTPSLayer adds or removes docker-compose.https.yml, keeping the layer order
// base, mounts, https, build (the same order the bash tool and installer use).
func SetHTTPSLayer(composeFile string, on bool) string {
	var keep []string
	build := false
	for _, p := range strings.Split(composeFile, ":") {
		switch p {
		case HTTPSCompose:
		case "docker-compose.build.yml":
			build = true
		case "":
		default:
			keep = append(keep, p)
		}
	}
	if len(keep) == 0 {
		keep = []string{BaseCompose}
	}
	if on {
		keep = append(keep, HTTPSCompose)
	}
	if build {
		keep = append(keep, "docker-compose.build.yml")
	}
	return strings.Join(keep, ":")
}
