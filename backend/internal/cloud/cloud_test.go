package cloud

import (
	"context"
	"errors"
	"io"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

func init() {
	Register(Kind{ID: "_test", Name: "test", Fields: []Field{
		{Key: "host", Label: "主机", Type: TText, Required: true},
		{Key: "port", Label: "端口", Type: TNumber},
		{Key: "mode", Label: "模式", Type: TSelect, Default: "a", Options: []Option{{"a", "A"}, {"b", "B"}}},
		{Key: "user", Label: "用户", Type: TText, ShowIf: &ShowIf{Key: "mode", Equals: "b"}, Required: true},
		{Key: "password", Label: "密码", Type: TPassword, Secret: true, Required: true},
		{Key: "key", Label: "密钥", Type: TTextarea, Secret: true},
	}})
}

func TestValidate(t *testing.T) {
	ok, sec, err := Validate("_test", map[string]string{"host": " h ", "port": "22"}, map[string]string{"password": " p "}, nil)
	if err != nil || ok["host"] != "h" || sec["password"] != "p" {
		t.Fatalf("valid input: %v %v %v", ok, sec, err)
	}
	cases := map[string]struct {
		v, s map[string]string
		have map[string]bool
	}{
		"missing required":      {map[string]string{}, map[string]string{"password": "p"}, nil},
		"missing secret":        {map[string]string{"host": "h"}, nil, nil},
		"unknown field":         {map[string]string{"host": "h", "nope": "x"}, map[string]string{"password": "p"}, nil},
		"secret as plain field": {map[string]string{"host": "h", "password": "p"}, nil, nil},
		"plain as secret":       {map[string]string{"host": "h"}, map[string]string{"password": "p", "host": "x"}, nil},
		"bad port":              {map[string]string{"host": "h", "port": "99999"}, map[string]string{"password": "p"}, nil},
		"bad option":            {map[string]string{"host": "h", "mode": "z"}, map[string]string{"password": "p"}, nil},
		"control char":          {map[string]string{"host": "h\x00"}, map[string]string{"password": "p"}, nil},
		"conditional required":  {map[string]string{"host": "h", "mode": "b"}, map[string]string{"password": "p"}, nil},
	}
	for name, c := range cases {
		if _, _, err := Validate("_test", c.v, c.s, c.have); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if _, isV := err.(*ValidationError); !isV {
			t.Errorf("%s: %T", name, err)
		}
	}
	// an already stored secret satisfies "required"; conditional fields are dropped when hidden
	v, s, err := Validate("_test", map[string]string{"host": "h", "user": "ignored"}, nil, map[string]bool{"password": true})
	if err != nil || v["user"] != "" || len(s) != 0 {
		t.Fatalf("stored secret / hidden field: %v %v %v", v, s, err)
	}
	if _, _, err := Validate("nope", nil, nil, nil); err == nil {
		t.Error("unknown kind")
	}
	if _, s, _ := Validate("_test", map[string]string{"host": "h"}, map[string]string{"password": "p", "key": "-----BEGIN\n line \n"}, nil); !strings.Contains(s["key"], "\n") {
		t.Error("textarea secrets keep their newlines")
	}
}

func TestCleanRel(t *testing.T) {
	for _, ok := range []string{"", "a", "a/b/c.pdf", "财务部/2026/合同 一.pdf", "x:y/z"} {
		if got, err := CleanRel(ok); err != nil || got != ok {
			t.Errorf("%q: %q %v", ok, got, err)
		}
	}
	for _, bad := range []string{"/a", "a//b", "a/../b", "..", ".", "a/.", `a\b`, "a\x00b", "a\nb", strings.Repeat("x", 1025), "a/"} {
		if _, err := CleanRel(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	if Join("", "a") != "a" || Join("b", "") != "b" || Join("b", "a") != "b/a" {
		t.Error("Join")
	}
}

func TestBlockedIPAndCheckHost(t *testing.T) {
	for _, s := range []string{"169.254.169.254", "169.254.1.1", "fe80::1", "0.0.0.0", "::", "224.0.0.1", "::ffff:169.254.169.254"} {
		if !BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s must be blocked", s)
		}
	}
	for _, s := range []string{"10.0.0.5", "192.168.1.20", "172.16.0.9", "127.0.0.1", "8.8.8.8", "::1", "fd00::1"} {
		if BlockedIP(netip.MustParseAddr(s)) {
			t.Errorf("%s is a legitimate storage address", s)
		}
	}
	for _, h := range []string{"", "http://x", "a/b", "u@h", "h?x", "a b", "169.254.169.254", "[fe80::1]"} {
		if CheckHost(h) == nil {
			t.Errorf("host %q must be rejected", h)
		}
	}
	for _, h := range []string{"files.corp.example", "10.1.2.3", "[::1]", "nas"} {
		if err := CheckHost(h); err != nil {
			t.Errorf("host %q: %v", h, err)
		}
	}
}

func TestSafeDialerRefusesMetadataAddresses(t *testing.T) {
	d := SafeDialer(2 * time.Second)
	_, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:80")
	if err == nil || !errors.Is(err, ErrUnreachable) && !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("dial to the metadata address must be refused, got %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			c.Close()
		}
	}()
	c, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("loopback/LAN dials are allowed: %v", err)
	}
	c.Close()
}

type memProv struct {
	files    map[string]string
	failPut  error
	failList error
	removed  []string
}

func (m *memProv) List(_ context.Context, dir string) ([]Entry, error) {
	if m.failList != nil {
		return nil, m.failList
	}
	return []Entry{{Name: "a"}}, nil
}
func (m *memProv) Stat(context.Context, string) (Entry, error) { return Entry{}, ErrNotFound }
func (m *memProv) Open(context.Context, string) (io.ReadCloser, int64, error) {
	return nil, 0, ErrNotFound
}
func (m *memProv) PutNew(_ context.Context, p string, r io.Reader, _ int64) error {
	if m.failPut != nil {
		return m.failPut
	}
	b, _ := io.ReadAll(r)
	m.files[p] = string(b)
	return nil
}
func (m *memProv) Remove(_ context.Context, p string) error {
	m.removed = append(m.removed, p)
	delete(m.files, p)
	return nil
}
func (m *memProv) Close() error { return nil }

func TestProbeReportsEachStepWithoutLeakingErrors(t *testing.T) {
	m := &memProv{files: map[string]string{}}
	r := Probe(context.Background(), m, true, ".oa-test-1")
	if !r.OK || len(r.Steps) != 2 || len(m.removed) != 1 || m.removed[0] != ".oa-test-1" || len(m.files) != 0 {
		t.Fatalf("write probe must write then remove only its marker: %+v removed=%v", r, m.removed)
	}
	r = Probe(context.Background(), m, false, "x")
	if !r.OK || len(r.Steps) != 1 {
		t.Fatalf("read-only probe: %+v", r)
	}
	m.failPut = ErrPermission
	r = Probe(context.Background(), m, true, "x")
	if r.OK || !strings.Contains(r.Steps[1].Message, "权限不足") {
		t.Fatalf("write failure: %+v", r)
	}
	m.failList = errors.New("dial tcp 10.0.0.9:22: password=hunter2 refused")
	r = Probe(context.Background(), m, true, "x")
	if r.OK || len(r.Steps) != 1 || strings.Contains(r.Steps[0].Message, "hunter2") || strings.Contains(r.Steps[0].Message, "10.0.0.9") {
		t.Fatalf("raw errors must never reach the report: %+v", r)
	}
	if Describe(&HostKeyError{Fingerprint: "SHA256:x"}) == "操作失败" || Describe(&HostKeyError{Changed: true}) == Describe(&HostKeyError{}) {
		t.Error("host-key errors need their own messages")
	}
}

func TestRegistryAndSort(t *testing.T) {
	if _, ok := KindByID("_test"); !ok || len(Kinds()) == 0 {
		t.Fatal("registry")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	Register(Kind{ID: "_test"})
}

func TestSortEntries(t *testing.T) {
	es := []Entry{{Name: "b.txt"}, {Name: "Z", IsDir: true}, {Name: "a.txt"}, {Name: "a", IsDir: true}}
	SortEntries(es)
	got := es[0].Name + es[1].Name + es[2].Name + es[3].Name
	if got != "aZa.txtb.txt" {
		t.Fatalf("folders first then case-insensitive names: %s", got)
	}
}
