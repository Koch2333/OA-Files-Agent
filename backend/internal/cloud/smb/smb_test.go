package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"oa-agent/backend/internal/cloud"
)

func TestValidate(t *testing.T) {
	v := map[string]string{"host": "h", "share": "s", "username": "u"}
	if _, _, err := cloud.Validate("smb", v, map[string]string{"password": "p"}, nil); err != nil {
		t.Fatalf("valid: %v", err)
	}
	if _, _, err := cloud.Validate("smb", v, nil, nil); err != nil {
		t.Fatalf("password is optional (guest/blank): %v", err)
	}
	for name, vals := range map[string]map[string]string{
		"no host":     {"share": "s", "username": "u"},
		"no share":    {"host": "h", "username": "u"},
		"no username": {"host": "h", "share": "s"},
		"bad port":    {"host": "h", "share": "s", "username": "u", "port": "0"},
		"unknown":     {"host": "h", "share": "s", "username": "u", "x": "1"},
	} {
		if _, _, err := cloud.Validate("smb", vals, nil, nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	k, ok := cloud.KindByID("smb")
	if !ok || k.Group != "enterprise" || k.Factory == nil {
		t.Fatal("kind registration")
	}
	keys := map[string]bool{}
	for _, f := range k.Fields {
		keys[f.Key] = f.Secret
	}
	for _, key := range []string{"host", "port", "share", "domain", "username"} {
		if secret, ok := keys[key]; !ok || secret {
			t.Errorf("field %s", key)
		}
	}
	if secret, ok := keys["password"]; !ok || !secret {
		t.Error("password must be a secret")
	}
}

func TestPathConversion(t *testing.T) {
	p := &provider{}
	ok := map[string]string{"": "", "a": "a", "a/b/c.pdf": `a\b\c.pdf`, "财务部/2026/合同 一.pdf": `财务部\2026\合同 一.pdf`, ".hidden": ".hidden"}
	for in, want := range ok {
		if got, err := p.native(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	pb := &provider{base: `x\y`}
	for in, want := range map[string]string{"": `x\y`, "a/b": `x\y\a\b`} {
		if got, err := pb.native(in); err != nil || got != want {
			t.Errorf("base %q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"..", "a/../b", "/a", "a//b", `a\b`, "a:b", "ads:stream", "trailing.", "trailing ", "a/dir./b", "a/x ", "q?", "a*b", "a<b", "a|b", `a"b`, "a\x00b", "a\nb", "."} {
		if _, err := p.native(bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
	for _, bad := range []string{"..", "a/../b", `a\b`, "a:b", "x./y"} {
		if _, err := baseToNative(bad); err == nil {
			t.Errorf("base %q must be rejected", bad)
		}
	}
	if got, err := baseToNative("/docs/in/"); err != nil || got != `docs\in` {
		t.Errorf("base: %q %v", got, err)
	}
	for _, bad := range []string{"", `a\b`, "a/b", "a:b", "x*"} {
		if checkShare(bad) == nil {
			t.Errorf("share %q must be rejected", bad)
		}
	}
	if checkShare("Documents$") != nil {
		t.Error("admin-style share names are valid")
	}
}

// respErr builds a *smb2.ResponseError carrying an NTSTATUS code.
func respErr(code uint32) error { return &smb2.ResponseError{Code: code} }

func TestMapErr(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		in   error
		want error
	}{
		{respErr(stLogonFailure), cloud.ErrAuth},
		{respErr(stWrongPassword), cloud.ErrAuth},
		{respErr(stAccountDisabled), cloud.ErrAuth},
		{respErr(stAccessDenied), cloud.ErrPermission},
		{respErr(stNameNotFound), cloud.ErrNotFound},
		{respErr(stPathNotFound), cloud.ErrNotFound},
		{respErr(stBadNetworkName), cloud.ErrNotFound},
		{respErr(stNameCollision), cloud.ErrExists},
		{respErr(stBadNetworkPath), cloud.ErrUnreachable},
		{&os.PathError{Op: "open", Path: "x", Err: respErr(stAccessDenied)}, cloud.ErrPermission},
		{fmt.Errorf("wrap: %w", respErr(stNameNotFound)), cloud.ErrNotFound},
		{&smb2.TransportError{Err: io.EOF}, cloud.ErrUnreachable},
		{io.EOF, cloud.ErrUnreachable},
		{os.ErrNotExist, cloud.ErrNotFound},
	}
	for _, c := range cases {
		if got := mapErr(ctx, c.in); !errors.Is(got, c.want) {
			t.Errorf("%v -> %v, want %v", c.in, got, c.want)
		}
	}
	// the unknown share carries its own message but is still ErrNotFound
	got := mapErr(ctx, respErr(stBadNetworkName))
	if !errors.Is(got, ErrShareNotFound) || !errors.Is(got, cloud.ErrNotFound) {
		t.Errorf("bad share: %v", got)
	}
	// unmapped statuses never leak anything beyond the code
	if got := mapErr(ctx, respErr(0xC0000001)); got == nil || strings.Contains(got.Error(), "response error") {
		t.Errorf("unmapped: %v", got)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if got := mapErr(cctx, io.EOF); !errors.Is(got, context.Canceled) {
		t.Errorf("cancelled: %v", got)
	}
	if mapErr(ctx, nil) != nil {
		t.Error("nil")
	}
	if cloud.Describe(mapErr(ctx, respErr(stLogonFailure))) == "" {
		t.Error("describe")
	}
}

func TestSafeDialerEnforced(t *testing.T) {
	set := cloud.Settings{Values: map[string]string{"host": "169.254.169.254", "share": "s", "username": "u"}}
	var ve *cloud.ValidationError
	if _, err := cloud.Open(context.Background(), "smb", set); !errors.As(err, &ve) {
		t.Fatalf("CheckHost should refuse metadata address: %v", err)
	}
	if _, err := cloud.SafeDialer(time.Second).DialContext(context.Background(), "tcp", "169.254.169.254:445"); err == nil {
		t.Fatal("dialer must refuse")
	}
	// a server that accepts and immediately closes is "unreachable", not a hang or a panic
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	set = cloud.Settings{
		Values:  map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(ln.Addr().(*net.TCPAddr).Port), "share": "s", "username": "u"},
		Secrets: map[string]string{"password": "p"},
	}
	if _, err := cloud.Open(context.Background(), "smb", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("closing server: %v", err)
	}
	ln.Close()
	if _, err := cloud.Open(context.Background(), "smb", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("closed port: %v", err)
	}
}

func TestInvalidSettings(t *testing.T) {
	var ve *cloud.ValidationError
	for _, set := range []cloud.Settings{
		{Values: map[string]string{"host": "h", "share": `a\b`, "username": "u"}},
		{Values: map[string]string{"host": "h", "share": "s", "username": "u"}, BasePath: "../x"},
		{Values: map[string]string{"host": "h", "share": "s"}},
	} {
		if _, err := cloud.Open(context.Background(), "smb", set); !errors.As(err, &ve) {
			t.Errorf("%+v: %v", set.Values, err)
		}
	}
}

// TestIntegration runs the whole data path against a real server. It is skipped unless OA_TEST_SMB_URL is set to
// smb://user:pass@host[:port]/share[?domain=X&base=sub/dir]. The share must be writable; the test only creates
// (and removes) files under a unique folder it makes itself.
func TestIntegration(t *testing.T) {
	raw := os.Getenv("OA_TEST_SMB_URL")
	if raw == "" {
		t.Skip("OA_TEST_SMB_URL not set")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "smb" {
		t.Fatalf("bad OA_TEST_SMB_URL: %v", err)
	}
	pass, _ := u.User.Password()
	set := cloud.Settings{
		Values:   map[string]string{"host": u.Hostname(), "share": strings.Trim(u.Path, "/"), "username": u.User.Username(), "domain": u.Query().Get("domain")},
		Secrets:  map[string]string{"password": pass},
		BasePath: u.Query().Get("base"),
	}
	if u.Port() != "" {
		set.Values["port"] = u.Port()
	}
	ctx := context.Background()
	p, err := cloud.Open(ctx, "smb", set)
	if err != nil {
		t.Fatalf("connect: %v (%s)", err, cloud.Describe(err))
	}
	defer p.Close()
	dir := fmt.Sprintf("oa-agent-test-%d", time.Now().UnixNano())
	file := dir + "/sub/合同 一.txt"
	body := "hello smb 世界"
	if err := p.PutNew(ctx, file, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	defer func() {
		// best-effort cleanup: remove the file, then the folders with a direct share handle
		_ = p.Remove(ctx, file)
		if sp, ok := p.(*provider); ok {
			n, _ := sp.native(dir + "/sub")
			_ = sp.share.Remove(n)
			n, _ = sp.native(dir)
			_ = sp.share.Remove(n)
		}
	}()
	if err := p.PutNew(ctx, file, strings.NewReader("OVERWRITE"), 9); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("second PutNew: %v", err)
	}
	rc, n, err := p.Open(ctx, file)
	if err != nil || n != int64(len(body)) {
		t.Fatalf("Open: %v %d", err, n)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != body {
		t.Fatalf("content %q", b)
	}
	l, err := p.List(ctx, dir+"/sub")
	if err != nil || len(l) != 1 || l[0].Name != "合同 一.txt" {
		t.Fatalf("List: %+v %v", l, err)
	}
	if _, err := p.Stat(ctx, dir+"/missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Stat missing: %v", err)
	}
	if r := cloud.Probe(ctx, p, true, ".oa-agent-test-marker"); !r.OK {
		t.Fatalf("probe: %+v", r)
	}
	bad := set
	bad.Secrets = map[string]string{"password": pass + "x"}
	if _, err := cloud.Open(ctx, "smb", bad); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
	bad = set
	bad.Values = map[string]string{"host": set.Values["host"], "share": "no-such-share-oa", "username": set.Values["username"]}
	if _, err := cloud.Open(ctx, "smb", bad); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("unknown share: %v", err)
	}
}
