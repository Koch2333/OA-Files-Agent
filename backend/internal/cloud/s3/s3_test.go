package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/minio/minio-go/v7"

	"oa-agent/backend/internal/cloud"
)

const (
	ak = "AKIATESTKEY"
	sk = "secret-test-key"
)

type fake struct {
	srv       *httptest.Server
	mu        sync.Mutex
	putHeader []string // If-None-Match values seen on PUT
	reject    string   // key suffix that always answers 412
}

func newFake(t *testing.T) *fake {
	t.Helper()
	be := s3mem.New()
	if err := be.CreateBucket("bkt"); err != nil {
		t.Fatal(err)
	}
	s3h := gofakes3.New(be).Server()
	f := &fake{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential="+ak+"/") {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			io.WriteString(w, `<?xml version="1.0"?><Error><Code>InvalidAccessKeyId</Code><Message>bad</Message></Error>`)
			return
		}
		if r.Method == "PUT" && r.Header.Get("X-Amz-Decoded-Content-Length") == "0" {
			// gofakes3 cannot read an empty aws-chunked body (real S3 and MinIO can)
			r.ContentLength = 0
			r.Header.Set("Content-Length", "0")
			r.TransferEncoding = nil
		}
		if r.Method == "PUT" {
			f.mu.Lock()
			f.putHeader = append(f.putHeader, r.Header.Get("If-None-Match"))
			rej := f.reject != "" && strings.HasSuffix(r.URL.Path, f.reject)
			f.mu.Unlock()
			if rej {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(412)
				io.WriteString(w, `<?xml version="1.0"?><Error><Code>PreconditionFailed</Code><Message>x</Message></Error>`)
				return
			}
		}
		s3h.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) connect(t *testing.T, base string, over map[string]string) cloud.Provider {
	t.Helper()
	v := map[string]string{"endpoint": strings.TrimPrefix(f.srv.URL, "http://"), "bucket": "bkt", "use_ssl": "false"}
	for k, x := range over {
		v[k] = x
	}
	p, err := New(context.Background(), cloud.Settings{Values: v, Secrets: map[string]string{"access_key": ak, "secret_key": sk}, BasePath: base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func put(t *testing.T, p cloud.Provider, name, body string) {
	t.Helper()
	if err := p.PutNew(context.Background(), name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

func read(t *testing.T, p cloud.Provider, name string) string {
	t.Helper()
	rc, size, err := p.Open(context.Background(), name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || int64(len(b)) != size {
		t.Fatalf("read %s: %v size=%d len=%d", name, err, size, len(b))
	}
	return string(b)
}

func TestRoundTripListStatRemove(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil)
	ctx := context.Background()
	names := []string{"a.txt", "财务部/2026/合同 一.pdf", "财务部/readme.txt", "dir with space/x y#z.txt", "x/y/z/deep.bin"}
	for _, n := range names {
		put(t, p, n, "body of "+n)
	}
	for _, n := range names {
		if got := read(t, p, n); got != "body of "+n {
			t.Fatalf("%s: %q", n, got)
		}
		e, err := p.Stat(ctx, n)
		if err != nil || e.IsDir || e.Size != int64(len("body of "+n)) || e.Path != n {
			t.Fatalf("stat %s: %+v %v", n, e, err)
		}
	}
	if len(f.putHeader) == 0 || f.putHeader[0] != "*" {
		t.Fatalf("conditional header must be sent, saw %q", f.putHeader)
	}
	ents, err := p.List(ctx, "")
	if err != nil || len(ents) != 4 {
		t.Fatalf("root: %+v %v", ents, err)
	}
	cloud.SortEntries(ents)
	if !ents[0].IsDir || ents[0].Name != "dir with space" || ents[3].Name != "a.txt" || ents[0].Path != "dir with space" {
		t.Fatalf("%+v", ents)
	}
	sub, err := p.List(ctx, "财务部")
	if err != nil || len(sub) != 2 {
		t.Fatalf("%+v %v", sub, err)
	}
	cloud.SortEntries(sub)
	if sub[0].Name != "2026" || !sub[0].IsDir || sub[0].Path != "财务部/2026" || sub[1].Path != "财务部/readme.txt" {
		t.Fatalf("%+v", sub)
	}
	if d, err := p.Stat(ctx, "x/y"); err != nil || !d.IsDir || d.Name != "y" {
		t.Fatalf("prefix stat: %+v %v", d, err)
	}
	if d, err := p.Stat(ctx, ""); err != nil || !d.IsDir {
		t.Fatalf("root stat: %+v %v", d, err)
	}
	for _, missing := range []string{"nope.txt", "x/nothing"} {
		if _, err := p.Stat(ctx, missing); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("stat %s: %v", missing, err)
		}
	}
	if _, _, err := p.Open(ctx, "nope.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("open missing: %v", err)
	}
	if _, err := p.List(ctx, "nodir"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("list missing: %v", err)
	}
	if err := p.Remove(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "a.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("after remove: %v", err)
	}
	put(t, p, "empty.txt", "")
	if e, err := p.Stat(ctx, "empty.txt"); err != nil || e.Size != 0 {
		t.Fatalf("empty: %+v %v", e, err)
	}
}

func TestZeroByteFolderMarkerSkipped(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil)
	put(t, p, "marked/inner.txt", "i")
	cli := p.(*provider).cli
	if _, err := cli.PutObject(context.Background(), "bkt", "marked/", strings.NewReader(""), 0, putOpts()); err != nil {
		t.Fatal(err)
	}
	ents, err := p.List(context.Background(), "marked")
	if err != nil || len(ents) != 1 || ents[0].Name != "inner.txt" {
		t.Fatalf("%+v %v", ents, err)
	}
	root, _ := p.List(context.Background(), "")
	if len(root) != 1 || root[0].Name != "marked" || !root[0].IsDir {
		t.Fatalf("%+v", root)
	}
}

func TestPutNewNeverOverwrites(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil)
	put(t, p, "dup/file.txt", "ORIGINAL")
	err := p.PutNew(context.Background(), "dup/file.txt", strings.NewReader("REPLACED!"), 9)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	if got := read(t, p, "dup/file.txt"); got != "ORIGINAL" {
		t.Fatalf("content changed: %q", got)
	}
	// server-side 412 (race: Stat saw nothing) maps to ErrExists
	f.reject = "race.txt"
	if err := p.PutNew(context.Background(), "race.txt", strings.NewReader("x"), 1); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("412: %v", err)
	}
}

func TestPutSizeMismatch(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil)
	if err := p.PutNew(context.Background(), "short.txt", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short source must fail")
	}
	if _, err := p.Stat(context.Background(), "short.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("no truncated object may remain: %v", err)
	}
	if err := p.PutNew(context.Background(), "long.txt", strings.NewReader("abcdef"), 3); err == nil {
		t.Fatal("long source must fail")
	}
	if _, err := p.Stat(context.Background(), "long.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("no object may remain: %v", err)
	}
}

func TestBasePathMapping(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "归档/2026 年", nil)
	put(t, p, "文件 1.txt", "hi")
	put(t, p, "子目录/b.txt", "yo")
	ents, err := p.List(context.Background(), "")
	if err != nil || len(ents) != 2 {
		t.Fatalf("%+v %v", ents, err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Path, "归档") {
			t.Fatalf("paths must be relative to the base: %+v", e)
		}
	}
	root := f.connect(t, "", nil)
	if e, err := root.Stat(context.Background(), "归档/2026 年/子目录/b.txt"); err != nil || e.Size != 2 {
		t.Fatalf("object key must be base+rel: %+v %v", e, err)
	}
	if got := read(t, p, "文件 1.txt"); got != "hi" {
		t.Fatal(got)
	}
	if _, err := p.Stat(context.Background(), "../x"); err == nil {
		t.Fatal("escape must be refused")
	}
	// a sibling folder with the same textual prefix is not listed
	put(t, root, "归档/2026 年x/other.txt", "o")
	if ents, _ := p.List(context.Background(), ""); len(ents) != 2 {
		t.Fatalf("%+v", ents)
	}
}

func TestPagination(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil)
	cli := p.(*provider).cli
	const n = 1150
	for i := 0; i < n; i++ {
		if _, err := cli.PutObject(context.Background(), "bkt", fmt.Sprintf("many/f%04d.txt", i), strings.NewReader("x"), 1, putOpts()); err != nil {
			t.Fatal(err)
		}
	}
	ents, err := p.List(context.Background(), "many")
	if err != nil || len(ents) != n {
		t.Fatalf("%d %v", len(ents), err)
	}
}

func TestAuthErrors(t *testing.T) {
	f := newFake(t)
	p, _ := New(context.Background(), cloud.Settings{
		Values:  map[string]string{"endpoint": strings.TrimPrefix(f.srv.URL, "http://"), "bucket": "bkt", "use_ssl": "false"},
		Secrets: map[string]string{"access_key": "WRONG", "secret_key": "leaky-secret"}})
	for name, fn := range map[string]func() error{
		"list": func() error { _, err := p.List(context.Background(), ""); return err },
		"stat": func() error { _, err := p.Stat(context.Background(), "a"); return err },
		"put":  func() error { return p.PutNew(context.Background(), "a", strings.NewReader("x"), 1) },
	} {
		err := fn()
		if !errors.Is(err, cloud.ErrAuth) {
			t.Errorf("%s: want ErrAuth, got %v", name, err)
		} else if strings.Contains(err.Error(), "leaky") || strings.Contains(cloud.Describe(err), "WRONG") {
			t.Errorf("%s leaks", name)
		}
	}
	if r := cloud.Probe(context.Background(), p, false, "m"); r.OK || !strings.Contains(r.Steps[0].Message, "认证失败") {
		t.Fatalf("%+v", r)
	}
}

func TestMapErrAccessDeniedAndBucket(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, status := "AccessDenied", 403
		if r.URL.Path == "/nobucket" || strings.HasPrefix(r.URL.Path, "/nobucket/") {
			code, status = "NoSuchBucket", 404
		}
		if strings.Contains(r.URL.Path, "sig") {
			code, status = "SignatureDoesNotMatch", 403
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		io.WriteString(w, `<?xml version="1.0"?><Error><Code>`+code+`</Code><Message>m</Message></Error>`)
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	ep := strings.TrimPrefix(srv.URL, "http://")
	mk := func(bucket string) cloud.Provider {
		p, err := New(context.Background(), cloud.Settings{Values: map[string]string{"endpoint": ep, "bucket": bucket, "use_ssl": "false"}, Secrets: map[string]string{"access_key": "a", "secret_key": "b"}})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := mk("denied").List(context.Background(), ""); !errors.Is(err, cloud.ErrPermission) {
		t.Fatalf("%v", err)
	}
	if _, err := mk("nobucket").List(context.Background(), ""); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := mk("sig").List(context.Background(), ""); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("%v", err)
	}
}

func TestUnreachable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	ep := strings.TrimPrefix(s.URL, "http://")
	s.Close()
	p, err := New(context.Background(), cloud.Settings{Values: map[string]string{"endpoint": ep, "bucket": "bkt", "use_ssl": "false"}, Secrets: map[string]string{"access_key": "a", "secret_key": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.List(context.Background(), ""); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("list: %v", err)
	}
	if _, err := p.Stat(context.Background(), "a"); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("stat: %v", err)
	}
	if err := p.PutNew(context.Background(), "a", strings.NewReader("x"), 1); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("put: %v", err)
	}
}

func TestPathStyleDetection(t *testing.T) {
	cases := []struct {
		ep, mode string
		want     bool
	}{
		{"192.168.1.10:9000", "auto", true},
		{"10.0.0.5", "auto", true},
		{"[::1]", "auto", true},
		{"localhost", "auto", true},
		{"LOCALHOST", "", true},
		{"minio.corp:9000", "auto", true},
		{"oss-cn-hangzhou.aliyuncs.com", "auto", false},
		{"cos.ap-guangzhou.myqcloud.com", "auto", false},
		{"obs.cn-north-4.myhuaweicloud.com", "auto", false},
		{"s3.amazonaws.com", "auto", false},
		{"acct.r2.cloudflarestorage.com", "auto", false},
		{"oss-cn-hangzhou.aliyuncs.com", "true", true},
		{"192.168.1.10:9000", "false", false},
	}
	for _, c := range cases {
		if got := usePathStyle(c.ep, c.mode); got != c.want {
			t.Errorf("%s/%s: %v", c.ep, c.mode, got)
		}
	}
}

func TestEndpointNormalisation(t *testing.T) {
	ep, ssl, err := splitEndpoint("", true, false)
	if err != nil || ep != "s3.amazonaws.com" || !ssl {
		t.Fatalf("%q %v %v", ep, ssl, err)
	}
	if ep, ssl, _ := splitEndpoint("http://10.0.0.1:9000/", true, false); ep != "10.0.0.1:9000" || ssl {
		t.Fatalf("%q %v", ep, ssl)
	}
	if _, ssl, _ := splitEndpoint("http://10.0.0.1:9000", true, true); !ssl {
		t.Fatal("explicit use_ssl wins")
	}
	for _, bad := range []string{"169.254.169.254", "169.254.169.254:80", "a b", "h/x"} {
		if _, _, err := splitEndpoint(bad, true, false); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestValidate(t *testing.T) {
	good := map[string]string{"bucket": "b"}
	sec := map[string]string{"access_key": "a", "secret_key": "s"}
	cases := []struct {
		name string
		v, s map[string]string
		ok   bool
	}{
		{"minimal", good, sec, true},
		{"full", map[string]string{"bucket": "b", "endpoint": "oss-cn-hangzhou.aliyuncs.com", "region": "cn-hangzhou", "use_ssl": "false", "path_style": "true", "insecure_skip_verify": "true"}, map[string]string{"access_key": "a", "secret_key": "s", "session_token": "t"}, true},
		{"no bucket", map[string]string{}, sec, false},
		{"no access key", good, map[string]string{"secret_key": "s"}, false},
		{"no secret key", good, map[string]string{"access_key": "a"}, false},
		{"bad path_style", map[string]string{"bucket": "b", "path_style": "yes"}, sec, false},
		{"bad use_ssl", map[string]string{"bucket": "b", "use_ssl": "1"}, sec, false},
		{"unknown", map[string]string{"bucket": "b", "x": "y"}, sec, false},
	}
	for _, c := range cases {
		if _, _, err := cloud.Validate("s3", c.v, c.s, nil); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, _, err := cloud.Validate("s3", good, nil, map[string]bool{"access_key": true, "secret_key": true}); err != nil {
		t.Errorf("stored secrets: %v", err)
	}
	if k, ok := cloud.KindByID("s3"); !ok || k.Group != "cloud" || k.Factory == nil {
		t.Fatal("registration")
	}
}

func TestProbe(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "probe-base", nil)
	r := cloud.Probe(context.Background(), p, true, ".oa-test-marker")
	if !r.OK || len(r.Steps) != 2 {
		t.Fatalf("%+v", r)
	}
	if _, err := p.Stat(context.Background(), ".oa-test-marker"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("marker must be removed: %v", err)
	}
	bad := f.connect(t, "", map[string]string{"bucket": "missing"})
	if r := cloud.Probe(context.Background(), bad, true, "m"); r.OK {
		t.Fatalf("%+v", r)
	}
}

func putOpts() minio.PutObjectOptions { return minio.PutObjectOptions{} }
