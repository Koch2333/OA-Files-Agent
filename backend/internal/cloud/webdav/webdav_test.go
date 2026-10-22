package webdav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/net/webdav"

	"oa-agent/backend/internal/cloud"
)

type server struct {
	*httptest.Server
	ignoreINM bool // strip If-None-Match like servers that do not support it
}

func newServer(t *testing.T, prefix string, user, pass string, ignoreINM bool) *httptest.Server {
	t.Helper()
	h := &webdav.Handler{Prefix: prefix, FileSystem: webdav.NewMemFS(), LockSystem: webdav.NewMemLS()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != user || p != pass {
				w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
				http.Error(w, "no", 401)
				return
			}
		}
		if r.Header.Get("If-None-Match") == "*" {
			if ignoreINM {
				r.Header.Del("If-None-Match")
			} else if _, err := h.FileSystem.Stat(r.Context(), strings.TrimPrefix(r.URL.Path, prefix)); err == nil && r.Method == "PUT" {
				w.WriteHeader(412)
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func connect(t *testing.T, rawURL, user, pass, base string) cloud.Provider {
	t.Helper()
	p, err := New(context.Background(), cloud.Settings{
		Values:   map[string]string{"url": rawURL, "auth": "basic", "username": user},
		Secrets:  map[string]string{"password": pass},
		BasePath: base,
	})
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
	rc, _, err := p.Open(context.Background(), name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	return string(b)
}

func TestRoundTripBothINMPaths(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		srv := newServer(t, "", "u", "p", ignore)
		p := connect(t, srv.URL, "u", "p", "")
		ctx := context.Background()
		names := []string{"a.txt", "财务部/2026/合同 一.pdf", "dir with space/#hash?q%25.txt", "x/y/z.bin"}
		for _, n := range names {
			put(t, p, n, "body of "+n)
		}
		for _, n := range names {
			if got := read(t, p, n); got != "body of "+n {
				t.Fatalf("ignore=%v %s: %q", ignore, n, got)
			}
			e, err := p.Stat(ctx, n)
			if err != nil || e.IsDir || e.Size != int64(len("body of "+n)) || e.Path != n {
				t.Fatalf("stat %s: %+v %v", n, e, err)
			}
		}
		// never overwrite, content unchanged
		err := p.PutNew(ctx, "a.txt", strings.NewReader("NEW"), 3)
		if !errors.Is(err, cloud.ErrExists) || read(t, p, "a.txt") != "body of a.txt" {
			t.Fatalf("ignore=%v second put: %v", ignore, err)
		}
		ents, err := p.List(ctx, "")
		if err != nil || len(ents) != 4 {
			t.Fatalf("root list: %+v %v", ents, err)
		}
		cloud.SortEntries(ents)
		if !ents[0].IsDir || ents[len(ents)-1].Name != "a.txt" {
			t.Fatalf("entries: %+v", ents)
		}
		sub, err := p.List(ctx, "财务部/2026")
		if err != nil || len(sub) != 1 || sub[0].Name != "合同 一.pdf" || sub[0].Path != "财务部/2026/合同 一.pdf" {
			t.Fatalf("sub list: %+v %v", sub, err)
		}
		if _, err := p.List(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("missing dir: %v", err)
		}
		if _, err := p.Stat(ctx, "nope.txt"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("missing stat: %v", err)
		}
		if _, _, err := p.Open(ctx, "nope.txt"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("missing open: %v", err)
		}
		if d, err := p.Stat(ctx, "x/y"); err != nil || !d.IsDir {
			t.Fatalf("dir stat: %+v %v", d, err)
		}
		if err := p.Remove(ctx, "a.txt"); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Stat(ctx, "a.txt"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("after remove: %v", err)
		}
		if err := p.Remove(ctx, "a.txt"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("remove missing: %v", err)
		}
		// zero byte file
		put(t, p, "empty.txt", "")
		if e, err := p.Stat(ctx, "empty.txt"); err != nil || e.Size != 0 {
			t.Fatalf("empty: %+v %v", e, err)
		}
	}
}

func TestSubPathBaseURLAndBasePath(t *testing.T) {
	srv := newServer(t, "/dav", "", "", false)
	p := connect(t, srv.URL+"/dav/", "", "", "归档/2026")
	put(t, p, "文件 1.txt", "hi")
	put(t, p, "sub/b.txt", "yo")
	ents, err := p.List(context.Background(), "")
	if err != nil || len(ents) != 2 {
		t.Fatalf("%+v %v", ents, err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Path, "归档") || strings.Contains(e.Path, "/dav") {
			t.Fatalf("paths must be relative to the base path: %+v", e)
		}
	}
	// the base folder was created under the URL path
	root := connect(t, srv.URL+"/dav", "", "", "")
	if e, err := root.Stat(context.Background(), "归档/2026/sub/b.txt"); err != nil || e.Size != 2 {
		t.Fatalf("%+v %v", e, err)
	}
	// escaping the root is refused
	for _, bad := range []string{"../x", "a/../../x"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Errorf("%q must be rejected", bad)
		}
	}
}

func TestAuthFailureAndBearer(t *testing.T) {
	srv := newServer(t, "", "u", "secret-pw", false)
	p := connect(t, srv.URL, "u", "wrong", "")
	_, err := p.List(context.Background(), "")
	if !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("want ErrAuth: %v", err)
	}
	if strings.Contains(cloud.Describe(err), "wrong") {
		t.Fatal("leak")
	}
	var seen string
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(403)
	}))
	defer b.Close()
	bp, _ := New(context.Background(), cloud.Settings{Values: map[string]string{"url": b.URL, "auth": "bearer"}, Secrets: map[string]string{"token": "tok"}})
	if _, err := bp.List(context.Background(), ""); !errors.Is(err, cloud.ErrPermission) || seen != "Bearer tok" {
		t.Fatalf("%v %q", err, seen)
	}
}

func TestStatusMapping(t *testing.T) {
	codes := map[int]error{401: cloud.ErrAuth, 403: cloud.ErrPermission, 404: cloud.ErrNotFound, 412: cloud.ErrExists}
	for code, want := range codes {
		if err := statusErr(code); !errors.Is(err, want) {
			t.Errorf("%d: %v", code, err)
		}
	}
	for _, code := range []int{423, 500, 502, 507} {
		err := statusErr(code)
		if err == nil || errors.Is(err, cloud.ErrAuth) || errors.Is(err, cloud.ErrNotFound) {
			t.Errorf("%d: %v", code, err)
		}
	}
	// locked / 5xx surface as plain errors through the client
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(423) }))
	defer s.Close()
	p := connect(t, s.URL, "", "", "")
	if err := p.Remove(context.Background(), "a"); err == nil || errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("locked: %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	p := connect(t, u, "", "", "")
	if _, err := p.List(context.Background(), ""); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("%v", err)
	}
	if _, err := p.Stat(context.Background(), "a"); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("%v", err)
	}
}

func TestRedirectSafety(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization")
		w.WriteHeader(207)
	}))
	defer other.Close()
	// 127.0.0.1 vs localhost are different hosts for the redirect policy
	ou, _ := url.Parse(other.URL)
	target := "http://localhost:" + ou.Port() + "/"
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusTemporaryRedirect)
	}))
	defer redir.Close()
	p := connect(t, redir.URL, "u", "p", "")
	_, err := p.List(context.Background(), "")
	if err == nil || leaked != "" {
		t.Fatalf("cross-host redirect must be refused: err=%v leaked=%q", err, leaked)
	}
	// same-host redirect chain longer than 3 is refused
	n := 0
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		http.Redirect(w, r, "/r"+strings.Repeat("x", n), http.StatusTemporaryRedirect)
	}))
	defer loop.Close()
	p2 := connect(t, loop.URL, "", "", "")
	if _, err := p2.List(context.Background(), ""); err == nil || n > 5 {
		t.Fatalf("redirect loop: err=%v n=%d", err, n)
	}
}

func TestParseEdgeCases(t *testing.T) {
	base, _ := url.Parse("http://h/remote.php/dav/files/me")
	p := &provider{base: base}
	xmlDoc := `<?xml version="1.0"?><!DOCTYPE x [<!ENTITY e SYSTEM "file:///etc/passwd">]>
<D:multistatus xmlns:D="DAV:">
<D:response><D:href>/remote.php/dav/files/me/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>http://h/remote.php/dav/files/me/%E8%B4%A2%E5%8A%A1%20%E9%83%A8/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
<D:response><D:href>/remote.php/dav/files/me/a%23b%3F%25.txt</D:href><D:propstat><D:prop><D:getcontentlength>12</D:getcontentlength><D:getlastmodified>Mon, 02 Jan 2006 15:04:05 GMT</D:getlastmodified><D:resourcetype/></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat><D:propstat><D:prop><D:foo/></D:prop><D:status>HTTP/1.1 404 Not Found</D:status></D:propstat></D:response>
</D:multistatus>`
	items, err := p.parse([]byte(xmlDoc))
	if err != nil || len(items) != 3 {
		t.Fatalf("%v %+v", err, items)
	}
	if items[1].abs != "/remote.php/dav/files/me/财务 部" || !items[1].entry.IsDir {
		t.Fatalf("%+v", items[1])
	}
	f := items[2]
	if f.abs != "/remote.php/dav/files/me/a#b?%.txt" || f.entry.Size != 12 || f.entry.IsDir || f.entry.ModTime.Year() != 2006 {
		t.Fatalf("%+v", f)
	}
	if _, err := p.parse([]byte("<nonsense")); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestListCapAndTrailingSlashHref(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`<d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	for i := 0; i < maxEntries+10; i++ {
		sb.WriteString(`<d:response><d:href>/f` + strings.Repeat("0", 1) + itoa(i) + `</d:href><d:propstat><d:prop><d:getcontentlength>1</d:getcontentlength></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	sb.WriteString(`</d:multistatus>`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(207)
		io.WriteString(w, sb.String())
	}))
	defer srv.Close()
	p := connect(t, srv.URL, "", "", "")
	ents, err := p.List(context.Background(), "")
	if err != nil || len(ents) != maxEntries {
		t.Fatalf("%d %v", len(ents), err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	s := ""
	for ; i > 0; i /= 10 {
		s = string(rune('0'+i%10)) + s
	}
	return s
}

func TestValidateAndFactoryRules(t *testing.T) {
	cases := []struct {
		name string
		v, s map[string]string
		ok   bool
	}{
		{"basic", map[string]string{"url": "https://h/dav", "username": "u"}, map[string]string{"password": "p"}, true},
		{"bearer", map[string]string{"url": "https://h/dav", "auth": "bearer"}, map[string]string{"token": "t"}, true},
		{"missing url", map[string]string{}, nil, false},
		{"bad auth", map[string]string{"url": "https://h", "auth": "digest"}, nil, false},
		{"bad skip", map[string]string{"url": "https://h", "insecure_skip_verify": "maybe"}, nil, false},
		{"unknown", map[string]string{"url": "https://h", "x": "y"}, nil, false},
	}
	for _, c := range cases {
		_, _, err := cloud.Validate("webdav", c.v, c.s, nil)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	for _, u := range []string{"ftp://h/x", "file:///etc", "https://", "https://u:p@h/", "http://169.254.169.254/", "nonsense"} {
		if _, err := New(context.Background(), cloud.Settings{Values: map[string]string{"url": u}}); err == nil {
			t.Errorf("%q must be rejected", u)
		}
	}
	if k, ok := cloud.KindByID("webdav"); !ok || k.Group != "gateway" {
		t.Fatal("registration")
	}
}

func TestProbe(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		srv := newServer(t, "", "u", "p", ignore)
		p := connect(t, srv.URL, "u", "p", "")
		r := cloud.Probe(context.Background(), p, true, ".oa-test-marker")
		if !r.OK || len(r.Steps) != 2 {
			t.Fatalf("%+v", r)
		}
		if _, err := p.Stat(context.Background(), ".oa-test-marker"); !errors.Is(err, cloud.ErrNotFound) {
			t.Fatalf("marker must be removed: %v", err)
		}
	}
	srv := newServer(t, "", "u", "p", false)
	bad := connect(t, srv.URL, "u", "nope", "")
	if r := cloud.Probe(context.Background(), bad, true, "m"); r.OK || !strings.Contains(r.Steps[0].Message, "认证失败") {
		t.Fatalf("%+v", r)
	}
}
