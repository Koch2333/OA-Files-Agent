// Package mounts is the pure logic behind "storage mounts": the mounts.json model, path safety
// rules, compose override generation and filesystem checks. It never talks to docker.
package mounts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"

	"oa-agent/backend/internal/ctl/envfile"
	"oa-agent/backend/internal/ctl/fsutil"
)

// Container uids from the images (see deploy/docker): postgres alpine and the app images.
const (
	PGUID, PGGID   = 70, 70
	AppUID, AppGID = 10001, 10001
	ExtraRoot      = "/mnt/storage"
)

// Core roles, in the stable order used everywhere.
var CoreRoles = []string{"archive", "spool", "app", "pgdata"}

type Path struct {
	Host string `json:"host"`
}

type Extra struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Container string `json:"container"`
	ReadOnly  bool   `json:"readonly"`
}

// Config is mounts.json, the source of truth for host paths.
type Config struct {
	Version int     `json:"version"`
	Archive Path    `json:"archive"`
	Spool   Path    `json:"spool"`
	App     Path    `json:"app"`
	PGData  Path    `json:"pgdata"`
	Extra   []Extra `json:"extra"`
}

// Entry is one mount as a flat row (what both list views and validators iterate over).
type Entry struct {
	Role      string // archive | spool | app | pgdata | extra
	Name      string // extras only
	Host      string
	Container string
	ReadOnly  bool
}

// Label is the human handle: the role or the extra's name.
func (e Entry) Label() string {
	if e.Role == "extra" {
		return e.Name
	}
	return e.Role
}

// ContainerFor returns the in-container target of a core role.
func ContainerFor(role string) string {
	switch role {
	case "archive":
		return "/data/archive"
	case "spool":
		return "/data/spool"
	case "app":
		return "/data"
	case "pgdata":
		return "/var/lib/postgresql/data"
	}
	return ""
}

// Entries flattens the config: core roles first, then extras sorted by name.
func (c Config) Entries() []Entry {
	out := []Entry{
		{Role: "archive", Host: c.Archive.Host, Container: ContainerFor("archive")},
		{Role: "spool", Host: c.Spool.Host, Container: ContainerFor("spool")},
		{Role: "app", Host: c.App.Host, Container: ContainerFor("app")},
		{Role: "pgdata", Host: c.PGData.Host, Container: ContainerFor("pgdata")},
	}
	for _, x := range c.sortedExtras() {
		out = append(out, Entry{Role: "extra", Name: x.Name, Host: x.Host, Container: x.Container, ReadOnly: x.ReadOnly})
	}
	return out
}

func (c Config) sortedExtras() []Extra {
	x := append([]Extra(nil), c.Extra...)
	sort.Slice(x, func(i, j int) bool { return x[i].Name < x[j].Name })
	return x
}

// CoreHost returns the host path of a core role.
func (c Config) CoreHost(role string) string {
	switch role {
	case "archive":
		return c.Archive.Host
	case "spool":
		return c.Spool.Host
	case "app":
		return c.App.Host
	case "pgdata":
		return c.PGData.Host
	}
	return ""
}

// WithCore returns a copy with one core role pointed at host.
func (c Config) WithCore(role, host string) Config {
	n := c.Clone()
	switch role {
	case "archive":
		n.Archive.Host = host
	case "spool":
		n.Spool.Host = host
	case "app":
		n.App.Host = host
	case "pgdata":
		n.PGData.Host = host
	}
	return n
}

func (c Config) Clone() Config {
	n := c
	n.Extra = append([]Extra(nil), c.Extra...)
	return n
}

// FindExtra returns the index of an extra by name, or -1.
func (c Config) FindExtra(name string) int {
	for i, x := range c.Extra {
		if x.Name == name {
			return i
		}
	}
	return -1
}

// Normalize cleans host paths, fills container paths and sorts extras. Paths use forward
// slashes on purpose: the host is Linux and the tests must not depend on the build OS.
func (c *Config) Normalize() {
	if c.Version == 0 {
		c.Version = 1
	}
	c.Archive.Host = cleanHost(c.Archive.Host)
	c.Spool.Host = cleanHost(c.Spool.Host)
	c.App.Host = cleanHost(c.App.Host)
	c.PGData.Host = cleanHost(c.PGData.Host)
	for i := range c.Extra {
		c.Extra[i].Host = cleanHost(c.Extra[i].Host)
		if c.Extra[i].Container == "" {
			c.Extra[i].Container = ExtraRoot + "/" + c.Extra[i].Name
		}
	}
	c.Extra = c.sortedExtras()
	if c.Extra == nil {
		c.Extra = []Extra{}
	}
}

// cleanHost cleans only absolute paths; anything else is left for validation to reject.
func cleanHost(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return path.Clean(p)
	}
	return p
}

// Marshal is the canonical mounts.json encoding (indented, trailing newline).
func (c Config) Marshal() ([]byte, error) {
	n := c.Clone()
	n.Normalize()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(n); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Parse decodes mounts.json strictly (unknown fields are an error: a typo must not be silently ignored).
func Parse(data []byte) (Config, error) {
	var c Config
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("mounts.json: %w", err)
	}
	if c.Version != 1 {
		return Config{}, fmt.Errorf("mounts.json: unsupported version %d", c.Version)
	}
	c.Normalize()
	return c, nil
}

// Load reads mounts.json. When it does not exist the legacy layout from .env is returned and
// fromFile is false, so the first mutation can create the file.
func Load(installDir string, env *envfile.File) (cfg Config, fromFile bool, err error) {
	b, err := os.ReadFile(filepath.Join(installDir, "mounts.json"))
	if os.IsNotExist(err) {
		return DefaultFromEnv(installDir, env), false, nil
	}
	if err != nil {
		return Config{}, false, err
	}
	cfg, err = Parse(b)
	return cfg, true, err
}

// DefaultFromEnv derives the layout the base compose file uses when no mounts.json exists.
func DefaultFromEnv(installDir string, env *envfile.File) Config {
	data := env.Value("OA_DATA_DIR", "./data")
	abs := func(p string) string {
		if len(p) > 0 && p[0] == '/' {
			return p
		}
		return path.Join(filepath.ToSlash(installDir), p)
	}
	data = abs(data)
	c := Config{Version: 1,
		Archive: Path{abs(env.Value("OA_ARCHIVE_DIR", data+"/archive"))},
		Spool:   Path{abs(env.Value("OA_SPOOL_DIR", data+"/spool"))},
		App:     Path{abs(env.Value("OA_APP_DIR", data+"/app"))},
		PGData:  Path{abs(env.Value("OA_PG_DIR", data+"/pg"))},
	}
	c.Normalize()
	return c
}

// Save writes mounts.json atomically after backing up the previous file; it returns the backup path.
func Save(installDir string, c Config) (string, error) {
	b, err := c.Marshal()
	if err != nil {
		return "", err
	}
	p := filepath.Join(installDir, "mounts.json")
	bak, err := fsutil.Backup(p, 10)
	if err != nil {
		return "", err
	}
	return bak, fsutil.WriteAtomic(p, b, 0o640)
}

// SyncEnv mirrors the core host paths into the OA_*_DIR variables the base compose file falls back to.
func SyncEnv(env *envfile.File, c Config) {
	env.Set("OA_PG_DIR", c.PGData.Host)
	env.Set("OA_APP_DIR", c.App.Host)
	env.Set("OA_SPOOL_DIR", c.Spool.Host)
	env.Set("OA_ARCHIVE_DIR", c.Archive.Host)
}
