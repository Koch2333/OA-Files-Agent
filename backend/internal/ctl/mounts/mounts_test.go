package mounts

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baseCfg() Config {
	c := Config{Version: 1,
		Archive: Path{"/srv/oa/archive"}, Spool: Path{"/srv/oa/spool"},
		App: Path{"/srv/oa/app"}, PGData: Path{"/srv/oa/pgdata"},
		Extra: []Extra{{Name: "nas1", Host: "/mnt/nas1", Container: "/mnt/storage/nas1", ReadOnly: true}},
	}
	c.Normalize()
	return c
}

func codes(is []Issue) string {
	var s []string
	for _, i := range is {
		s = append(s, i.Code)
	}
	return strings.Join(s, ",")
}

func TestValidatePath(t *testing.T) {
	cases := []struct {
		path, installDir, want string
	}{
		{"/srv/oa/archive", "/opt/oa-agent", ""},
		{"/data", "/opt/oa-agent", ""},
		{"/opt/oa-agent/data/pg", "/opt/oa-agent", ""},
		{"", "", "empty"},
		{"relative/dir", "", "not_abs"},
		{"./x", "", "not_abs"},
		{"/srv/../etc", "", "dotdot"},
		{"/srv/oa/..", "", "dotdot"},
		{"/", "", "root"},
		{"//", "", "root"},
		{"/etc", "", "forbidden"},
		{"/etc/oa", "", "forbidden"},
		{"/usr/local/x", "", "forbidden"},
		{"/bin", "", "forbidden"},
		{"/sbin/x", "", "forbidden"},
		{"/lib64", "", "forbidden"},
		{"/boot", "", "forbidden"},
		{"/proc/1", "", "forbidden"},
		{"/sys/fs", "", "forbidden"},
		{"/dev/shm", "", "forbidden"},
		{"/run/user", "", "forbidden"},
		{"/var/lib/docker/volumes", "", "forbidden"},
		{"/var/lib", "", "forbidden_parent"},
		{"/var", "", "forbidden_parent"},
		{"/srv/my dir", "", "bad_char"},
		{"/srv/a:b", "", "bad_char"},
		{"/srv/a$b", "", "bad_char"},
		{"/srv/a\nb", "", "bad_char"},
		{"/srv/a#b", "", "bad_char"},
		{"/srv/中文", "", "bad_char"},
		{"/opt", "/opt/oa-agent", "install_dir"},
		{"/opt/oa-agent", "/opt/oa-agent", "install_dir"},
		{"/etcetera", "", ""}, // prefix match must be on path segments
	}
	for _, c := range cases {
		got := ValidatePath("x", c.path, c.installDir)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%q: unexpected %v", c.path, got.Code)
		case c.want != "" && (got == nil || got.Code != c.want):
			t.Errorf("%q: want %s got %v", c.path, c.want, got)
		}
	}
}

func TestValidateName(t *testing.T) {
	good := []string{"nas1", "a", "0x", "a_b-c", strings.Repeat("a", 31)}
	bad := []string{"", "Nas", "-a", "_a", "a b", "a/b", strings.Repeat("a", 32), "中", "a.b"}
	res := []string{"archive", "spool", "app", "pgdata", "postgres", "api", "worker", "caddy", "data", "storage"}
	for _, n := range good {
		if i := ValidateName(n); i != nil {
			t.Errorf("%q rejected: %v", n, i)
		}
	}
	for _, n := range bad {
		if i := ValidateName(n); i == nil || i.Code != "bad_name" {
			t.Errorf("%q should be bad_name, got %v", n, i)
		}
	}
	for _, n := range res {
		if i := ValidateName(n); i == nil || i.Code != "reserved_name" {
			t.Errorf("%q should be reserved, got %v", n, i)
		}
	}
}

func TestValidateOverlap(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string // substring of codes; "" = clean
	}{
		{"clean", func(c *Config) {}, ""},
		{"archive inside spool", func(c *Config) { c.Archive.Host = "/srv/oa/spool/archive" }, "overlap"},
		{"spool inside archive", func(c *Config) { c.Spool.Host = "/srv/oa/archive/spool" }, "overlap"},
		{"same path", func(c *Config) { c.App.Host = c.Spool.Host }, "overlap"},
		{"trailing slash same", func(c *Config) { c.App.Host = "/srv/oa/spool/" }, "overlap"},
		{"pgdata parent of app", func(c *Config) { c.PGData.Host = "/srv/oa" }, "overlap"},
		{"extra inside archive", func(c *Config) { c.Extra[0].Host = "/srv/oa/archive/nas" }, "overlap"},
		{"extra parent of archive", func(c *Config) { c.Extra[0].Host = "/srv" }, "overlap"},
		{"two extras nested", func(c *Config) {
			c.Extra = append(c.Extra, Extra{Name: "n2", Host: "/mnt/nas1/sub", Container: "/mnt/storage/n2"})
		}, "overlap"},
		{"sibling prefix is fine", func(c *Config) { c.Extra[0].Host = "/srv/oa/archive2" }, ""},
		{"dup extra name", func(c *Config) {
			c.Extra = append(c.Extra, Extra{Name: "nas1", Host: "/mnt/other", Container: "/mnt/storage/nas1"})
		}, "dup_name"},
		{"wrong container", func(c *Config) { c.Extra[0].Container = "/mnt/storage/zzz" }, "bad_container"},
		{"reserved extra", func(c *Config) { c.Extra[0].Name = "api"; c.Extra[0].Container = "/mnt/storage/api" }, "reserved_name"},
	}
	for _, tc := range cases {
		c := baseCfg()
		tc.mutate(&c)
		got := codes(Validate(c, Options{InstallDir: "/opt/oa-agent"}))
		if tc.want == "" && got != "" {
			t.Errorf("%s: unexpected %s", tc.name, got)
		}
		if tc.want != "" && !strings.Contains(got, tc.want) {
			t.Errorf("%s: want %s got %q", tc.name, tc.want, got)
		}
	}
}

func TestValidateSymlinkOverlap(t *testing.T) {
	c := baseCfg()
	c.Extra[0].Host = "/mnt/link" // resolves into the archive
	res := func(p string) string {
		if p == "/mnt/link" {
			return "/srv/oa/archive/inner"
		}
		return p
	}
	if got := codes(Validate(c, Options{Resolve: res})); !strings.Contains(got, "overlap") {
		t.Fatalf("symlinked overlap not detected: %q", got)
	}
}

func TestRoundTrip(t *testing.T) {
	c := baseCfg()
	c.Extra = append(c.Extra, Extra{Name: "backup", Host: "/mnt/b", Container: "/mnt/storage/backup"})
	b, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := got.Marshal()
	if string(b) != string(b2) {
		t.Fatalf("round trip differs:\n%s\n%s", b, b2)
	}
	if got.Extra[0].Name != "backup" { // sorted
		t.Fatalf("extras not sorted: %+v", got.Extra)
	}
}

func TestParseStrict(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"version":1,"archive":{"host":"/a"},"bogus":1}`,
		"bad version":   `{"version":2}`,
		"not json":      `nope`,
	}
	for n, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", n)
		}
	}
	// the installer's documented shape parses
	ok := `{"version":1,"archive":{"host":"/srv/oa/archive"},"spool":{"host":"/srv/oa/spool"},"app":{"host":"/srv/oa/app"},"pgdata":{"host":"/srv/oa/pgdata"},"extra":[{"name":"nas1","host":"/mnt/nas1","container":"/mnt/storage/nas1","readonly":true}]}`
	c, err := Parse([]byte(ok))
	if err != nil || len(c.Extra) != 1 || !c.Extra[0].ReadOnly {
		t.Fatalf("installer shape: %v %+v", err, c)
	}
}

func TestRenderGolden(t *testing.T) {
	cases := map[string]Config{"full": baseCfg()}
	none := baseCfg()
	none.Extra = nil
	cases["noextra"] = none
	multi := baseCfg()
	multi.Extra = []Extra{
		{Name: "zeta", Host: "/mnt/z", Container: "/mnt/storage/zeta"},
		{Name: "alpha", Host: "/mnt/a", Container: "/mnt/storage/alpha", ReadOnly: true},
	}
	cases["multi"] = multi
	for name, c := range cases {
		got, err := Render(c, Options{})
		if err != nil {
			t.Fatal(err)
		}
		again, _ := Render(c, Options{})
		if string(got) != string(again) {
			t.Fatalf("%s: not deterministic", name)
		}
		if !strings.Contains(string(got), "generated — do not edit") {
			t.Errorf("%s: missing header", name)
		}
		file := filepath.Join("testdata", "override_"+name+".golden")
		if os.Getenv("UPDATE_GOLDEN") != "" {
			os.WriteFile(file, got, 0o644)
		}
		want, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v (run with UPDATE_GOLDEN=1)", name, err)
		}
		if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got) {
			t.Errorf("%s golden mismatch:\n%s", name, got)
		}
	}
}

// The installer ships an example of the exact shape; the body (without comments) must match.
func TestRenderMatchesInstallerExample(t *testing.T) {
	ex, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "linux", "docker-compose.mounts.example.yml"))
	if err != nil {
		t.Skip("installer example not available")
	}
	body := func(s string) string {
		var out []string
		for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
			if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
				continue
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}
	got, _ := Render(baseCfg(), Options{})
	if body(string(got)) != body(string(ex)) {
		t.Fatalf("differs from deploy/linux example:\n%s\n---\n%s", body(string(got)), body(string(ex)))
	}
}

func TestRenderRefusesInvalid(t *testing.T) {
	c := baseCfg()
	c.Archive.Host = "/etc/x"
	if _, err := Render(c, Options{}); err == nil {
		t.Fatal("expected error")
	}
}

type fakeProber map[string]Probe

func (f fakeProber) Probe(p string) (Probe, error) { return f[p], nil }

func TestCheck(t *testing.T) {
	good := Probe{Exists: true, IsDir: true, Mode: 0o750, UID: 10001, GID: 10001, FreeBytes: 100 * gib, TotalBytes: 200 * gib, FSType: "ext4", Empty: true}
	pg := Probe{Exists: true, IsDir: true, Mode: 0o700, UID: 70, GID: 70, FreeBytes: 100 * gib, TotalBytes: 200 * gib, FSType: "ext4"}
	cases := []struct {
		name  string
		e     Entry
		p     Probe
		want  string
		fixes int
	}{
		{"archive ok", Entry{Role: "archive", Host: "/a"}, good, "", 0},
		{"archive missing", Entry{Role: "archive", Host: "/a"}, Probe{}, "missing", 1},
		{"archive wrong owner", Entry{Role: "archive", Host: "/a"}, func() Probe { p := good; p.UID = 0; p.GID = 0; return p }(), "owner", 1},
		{"archive not dir", Entry{Role: "archive", Host: "/a"}, Probe{Exists: true}, "not_dir", 0},
		{"archive low space", Entry{Role: "archive", Host: "/a"}, func() Probe { p := good; p.FreeBytes = 5 * gib; return p }(), "low_space", 0},
		{"archive tmpfs", Entry{Role: "archive", Host: "/a"}, func() Probe { p := good; p.FSType = "tmpfs"; return p }(), "fs_type", 0},
		{"pg ok", Entry{Role: "pgdata", Host: "/p"}, pg, "", 0},
		{"pg owner", Entry{Role: "pgdata", Host: "/p"}, func() Probe { p := pg; p.UID = 10001; return p }(), "pg_owner", 1},
		{"pg mode", Entry{Role: "pgdata", Host: "/p"}, func() Probe { p := pg; p.Mode = 0o777; return p }(), "pg_mode", 1},
		{"pg nfs", Entry{Role: "pgdata", Host: "/p"}, func() Probe { p := pg; p.FSType = "nfs4"; return p }(), "fs_pg", 0},
		{"pg overlay", Entry{Role: "pgdata", Host: "/p"}, func() Probe { p := pg; p.FSType = "overlay"; return p }(), "fs_pg", 0},
		{"pg 9p", Entry{Role: "pgdata", Host: "/p"}, func() Probe { p := pg; p.FSType = "9p"; return p }(), "fs_pg", 0},
		{"extra missing is error", Entry{Role: "extra", Name: "n", Host: "/n", ReadOnly: true}, Probe{}, "missing", 0},
		{"extra ro readable", Entry{Role: "extra", Name: "n", Host: "/n", ReadOnly: true}, Probe{Exists: true, IsDir: true, Mode: 0o755, UID: 0, GID: 0}, "", 0},
		{"extra ro unreadable", Entry{Role: "extra", Name: "n", Host: "/n", ReadOnly: true}, Probe{Exists: true, IsDir: true, Mode: 0o700, UID: 0, GID: 0}, "not_readable", 0},
		{"extra rw not writable", Entry{Role: "extra", Name: "n", Host: "/n"}, Probe{Exists: true, IsDir: true, Mode: 0o755, UID: 0, GID: 0}, "not_writable", 0},
		{"extra rw owner writable", Entry{Role: "extra", Name: "n", Host: "/n"}, Probe{Exists: true, IsDir: true, Mode: 0o755, UID: 10001, GID: 0}, "", 0},
	}
	for _, c := range cases {
		r := Check(c.e, fakeProber{c.e.Host: c.p})
		got := codes(r.Issues)
		if c.want == "" && got != "" || c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q got %q", c.name, c.want, got)
		}
		if len(r.Fixes) != c.fixes {
			t.Errorf("%s: fixes=%d want %d", c.name, len(r.Fixes), c.fixes)
		}
	}
	if HasErrors(Check(Entry{Role: "extra", Name: "n", Host: "/n"}, fakeProber{}).Issues) == false {
		t.Error("missing extra must block")
	}
}

func TestFixCommand(t *testing.T) {
	cases := map[string]Fix{
		"chown -R 70:70 '/p'":                       {Kind: "chown", Path: "/p", UID: 70, GID: 70, Recursive: true},
		"chmod 0700 '/p'":                           {Kind: "chmod", Path: "/p", Mode: 0o700},
		"install -d -o 10001 -g 10001 -m 0750 '/a'": {Kind: "mkdir", Path: "/a", UID: 10001, GID: 10001, Mode: 0o750},
		`chown 1:1 '/a'\''b'`:                       {Kind: "chown", Path: "/a'b", UID: 1, GID: 1},
	}
	for want, f := range cases {
		if got := f.Command(); got != want {
			t.Errorf("got %q want %q", got, want)
		}
	}
}

func TestProcMounts(t *testing.T) {
	data := "overlay / overlay rw 0 0\n/dev/sda1 /srv ext4 rw 0 0\nnas:/x /srv/oa/archive nfs4 rw 0 0\ntmpfs /srv/oa/tmp tmpfs rw 0 0\n/dev/sdb /mnt/my\\040disk ext4 rw 0 0\n"
	ms := ParseProcMounts(data)
	cases := map[string]string{
		"/srv/oa/archive/2024": "nfs4", "/srv/oa/archive": "nfs4", "/srv/oa/archive2": "ext4",
		"/srv/x": "ext4", "/other": "overlay", "/srv/oa/tmp/a": "tmpfs", "/mnt/my disk/a": "ext4",
	}
	for p, want := range cases {
		if got := FSTypeFor(ms, p); got != want {
			t.Errorf("%s: got %s want %s", p, got, want)
		}
	}
}

func TestAddRemoveSet(t *testing.T) {
	c := baseCfg()
	opt := Options{InstallDir: "/opt/oa-agent"}
	n, is := AddExtra(c, "nas2", "/mnt/nas2", true, false, opt)
	if HasErrors(is) || len(n.Extra) != 2 || !n.Extra[1].ReadOnly {
		t.Fatalf("add: %v %+v", is, n.Extra)
	}
	if _, is := AddExtra(c, "nas1", "/mnt/x", true, false, opt); !HasErrors(is) {
		t.Error("duplicate add must fail")
	}
	r, is := AddExtra(c, "nas1", "/mnt/nas1", false, true, opt)
	if HasErrors(is) || r.Extra[0].ReadOnly {
		t.Error("replace to rw failed")
	}
	if _, is := AddExtra(c, "bad", "/etc/x", true, false, opt); !HasErrors(is) {
		t.Error("forbidden path must fail")
	}
	rm, err := RemoveExtra(n, "nas2")
	if err != nil || len(rm.Extra) != 1 {
		t.Fatal(err)
	}
	if _, err := RemoveExtra(rm, "zzz"); err == nil {
		t.Error("remove unknown must fail")
	}
	s, is, err := SetCore(c, "archive", "/srv/new/archive", opt)
	if err != nil || HasErrors(is) || s.Archive.Host != "/srv/new/archive" {
		t.Fatalf("set: %v %v", err, is)
	}
	if _, _, err := SetCore(c, "nope", "/x", opt); err == nil {
		t.Error("unknown role must fail")
	}
	if _, is, _ := SetCore(c, "archive", "/srv/oa/spool/in", opt); !HasErrors(is) {
		t.Error("nested set must fail")
	}
}

func TestRisks(t *testing.T) {
	full := Probe{Exists: true, IsDir: true}
	empty := Probe{Exists: true, IsDir: true, Empty: true}
	old := baseCfg()
	cases := []struct {
		name    string
		role    string
		to      string
		probes  fakeProber
		running []string
		want    string
		ack     bool
		block   bool
	}{
		{"archive old full new empty", "archive", "/srv/new", fakeProber{"/srv/oa/archive": full, "/srv/new": empty}, nil, "archive_relocate,archive_new_empty", true, false},
		{"archive old full new missing", "archive", "/srv/new", fakeProber{"/srv/oa/archive": full}, nil, "archive_new_empty", true, false},
		{"archive both full", "archive", "/srv/new", fakeProber{"/srv/oa/archive": full, "/srv/new": full}, nil, "archive_new_dirty", true, false},
		{"archive old empty", "archive", "/srv/new", fakeProber{"/srv/oa/archive": empty}, nil, "", false, false},
		{"archive old missing", "archive", "/srv/new", fakeProber{}, nil, "", false, false},
		{"pgdata running", "pgdata", "/srv/newpg", fakeProber{"/srv/oa/pgdata": full}, []string{"postgres"}, "pgdata_running", false, true},
		{"pgdata stopped old full", "pgdata", "/srv/newpg", fakeProber{"/srv/oa/pgdata": full, "/srv/newpg": empty}, nil, "pgdata_relocate", true, false},
		{"pgdata stopped fresh", "pgdata", "/srv/newpg", fakeProber{}, nil, "", false, false},
		{"spool", "spool", "/srv/ns", fakeProber{"/srv/oa/spool": full}, nil, "spool_relocate", false, false},
	}
	for _, c := range cases {
		next := old.WithCore(c.role, c.to)
		is := Risks(RiskInput{Old: old, New: next, Prober: c.probes, RunningService: c.running})
		got := codes(is)
		for _, w := range strings.Split(c.want, ",") {
			if w != "" && !strings.Contains(got, w) {
				t.Errorf("%s: want %s in %q", c.name, w, got)
			}
		}
		if c.want == "" && got != "" {
			t.Errorf("%s: unexpected %q", c.name, got)
		}
		if NeedsAck(is) != c.ack {
			t.Errorf("%s: ack=%v want %v", c.name, NeedsAck(is), c.ack)
		}
		if HasErrors(is) != c.block {
			t.Errorf("%s: block=%v want %v", c.name, HasErrors(is), c.block)
		}
	}
	// ro -> rw flip needs acknowledgement
	next := old.Clone()
	next.Extra[0].ReadOnly = false
	if is := Risks(RiskInput{Old: old, New: next, Prober: fakeProber{}}); !NeedsAck(is) {
		t.Error("ro->rw must need ack")
	}
	if is := Risks(RiskInput{Old: old, New: old, Prober: fakeProber{}}); len(is) != 0 {
		t.Error("no change, no risk")
	}
}

func TestCopyAndVerify(t *testing.T) {
	from, to := t.TempDir(), filepath.Join(t.TempDir(), "dst")
	os.MkdirAll(filepath.Join(from, "a", "b"), 0o755)
	os.WriteFile(filepath.Join(from, "a", "b", "f.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(from, "g.txt"), []byte("12345"), 0o644)
	// a "cp" that really copies (portable, test-only)
	good := func(ctx context.Context, name string, args ...string) error {
		if name != "cp" || args[0] != "-a" {
			t.Errorf("unexpected command %s %v", name, args)
		}
		src := strings.TrimSuffix(args[1], "/.")
		dst := strings.TrimSuffix(args[2], "/")
		return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			if info.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			b, _ := os.ReadFile(p)
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		})
	}
	st, err := CopyAndVerify(context.Background(), good, from, to)
	if err != nil || st.Files != 2 || st.Bytes != 10 {
		t.Fatalf("copy: %v %+v", err, st)
	}
	// source untouched
	if b, _ := os.ReadFile(filepath.Join(from, "g.txt")); string(b) != "12345" {
		t.Fatal("source modified")
	}
	// a copy that silently drops a file must fail verification
	lossy := func(ctx context.Context, name string, args ...string) error { return nil }
	if _, err := CopyAndVerify(context.Background(), lossy, from, filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("verification should fail")
	}
	boom := func(ctx context.Context, name string, args ...string) error { return errors.New("boom") }
	if _, err := CopyAndVerify(context.Background(), boom, from, filepath.Join(t.TempDir(), "y")); err == nil {
		t.Fatal("exec error must propagate")
	}
}

func TestCompleteHelpersAndEnvSync(t *testing.T) {
	// SyncEnv is covered with the env package; here just check Entries order and CoreHost
	c := baseCfg()
	es := c.Entries()
	if es[0].Role != "archive" || es[3].Role != "pgdata" || es[4].Name != "nas1" {
		t.Fatalf("entry order: %+v", es)
	}
}
