package openlist

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

type fake struct {
	mu      sync.Mutex
	files   map[string]string // abs path -> content
	dirs    map[string]bool
	token   string
	logins  int
	expire  bool // next authenticated call answers code 401 once
	srv     *httptest.Server
	seenOTP string
}

func newFake(t *testing.T) *fake {
	f := &fake{files: map[string]string{}, dirs: map[string]bool{"/": true}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func reply(w http.ResponseWriter, code int, msg string, data any) {
	json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "data": data})
}

func (f *fake) children(dir string) []map[string]any {
	var out []map[string]any
	for p, c := range f.files {
		if path.Dir(p) == dir {
			out = append(out, map[string]any{"name": path.Base(p), "size": len(c), "is_dir": false, "modified": "2024-01-02T03:04:05Z"})
		}
	}
	for p := range f.dirs {
		if p != "/" && path.Dir(p) == dir {
			out = append(out, map[string]any{"name": path.Base(p), "size": 0, "is_dir": true, "modified": "2024-01-02T03:04:05Z"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/api/auth/login" {
		var b map[string]string
		json.NewDecoder(r.Body).Decode(&b)
		f.seenOTP = b["otp_code"]
		if b["username"] != "u" || b["password"] != "p4ssw0rd-secret" {
			reply(w, 400, "username or password is incorrect", nil)
			return
		}
		f.logins++
		f.token = "tok" + string(rune('0'+f.logins))
		reply(w, 200, "success", map[string]string{"token": f.token})
		return
	}
	if strings.HasPrefix(r.URL.Path, "/d/") {
		if c, ok := f.files[strings.TrimPrefix(r.URL.Path, "/d")]; ok {
			io.WriteString(w, c)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != f.token || f.expire {
		f.expire = false
		reply(w, 401, "token is invalidated", nil)
		return
	}
	if r.URL.Path == "/api/fs/put" {
		p, _ := url.PathUnescape(r.Header.Get("File-Path"))
		if _, ok := f.files[p]; ok && r.Header.Get("Overwrite") == "false" {
			reply(w, 403, "file exists", nil)
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.files[p] = string(b)
		reply(w, 200, "success", nil)
		return
	}
	var b struct {
		Path    string   `json:"path"`
		Dir     string   `json:"dir"`
		Names   []string `json:"names"`
		Page    int      `json:"page"`
		PerPage int      `json:"per_page"`
	}
	json.NewDecoder(r.Body).Decode(&b)
	switch r.URL.Path {
	case "/api/fs/list":
		if !f.dirs[b.Path] {
			reply(w, 500, "failed get dir: object not found", nil)
			return
		}
		all := f.children(b.Path)
		per := 2 // the fake serves small pages regardless of the request
		start := (b.Page - 1) * per
		end := start + per
		if start > len(all) {
			start = len(all)
		}
		if end > len(all) {
			end = len(all)
		}
		reply(w, 200, "success", map[string]any{"content": all[start:end], "total": len(all)})
	case "/api/fs/get":
		if c, ok := f.files[b.Path]; ok {
			reply(w, 200, "success", map[string]any{"name": path.Base(b.Path), "size": len(c), "is_dir": false, "modified": "2024-01-02T03:04:05Z", "raw_url": f.srv.URL + "/d" + b.Path})
		} else if f.dirs[b.Path] {
			reply(w, 200, "success", map[string]any{"name": path.Base(b.Path), "is_dir": true})
		} else {
			reply(w, 500, "object not found", nil)
		}
	case "/api/fs/mkdir":
		for p := b.Path; p != "/"; p = path.Dir(p) {
			f.dirs[p] = true
		}
		reply(w, 200, "success", nil)
	case "/api/fs/remove":
		for _, n := range b.Names {
			delete(f.files, path.Join(b.Dir, n))
		}
		reply(w, 200, "success", nil)
	default:
		http.NotFound(w, r)
	}
}

func connect(t *testing.T, f *fake, base string) cloud.Provider {
	t.Helper()
	cloudhttp.Backoff = time.Millisecond
	p, err := New(context.Background(), cloud.Settings{
		Values:   map[string]string{"url": f.srv.URL, "auth": "password", "username": "u"},
		Secrets:  map[string]string{"password": "p4ssw0rd-secret"},
		BasePath: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestListPaginationAndLoginReuse(t *testing.T) {
	f := newFake(t)
	f.dirs["/a"] = true
	for _, n := range []string{"1.txt", "2.txt", "3.txt", "4.txt"} {
		f.files["/a/"+n] = "x"
	}
	f.dirs["/a/sub"] = true
	p := connect(t, f, "a")
	for i := 0; i < 2; i++ {
		es, err := p.List(context.Background(), "")
		if err != nil || len(es) != 5 {
			t.Fatalf("list: %v %v", es, err)
		}
	}
	if f.logins != 1 {
		t.Fatalf("token should be reused, logins=%d", f.logins)
	}
	if _, err := p.List(context.Background(), "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestReloginOn401(t *testing.T) {
	f := newFake(t)
	f.files["/x.txt"] = "hello"
	p := connect(t, f, "")
	if _, err := p.Stat(context.Background(), "x.txt"); err != nil {
		t.Fatal(err)
	}
	f.expire = true
	e, err := p.Stat(context.Background(), "x.txt")
	if err != nil || e.Size != 5 || f.logins != 2 {
		t.Fatalf("relogin: %v %v logins=%d", e, err, f.logins)
	}
}

func TestBadPasswordNoSecretLeak(t *testing.T) {
	f := newFake(t)
	p, _ := New(context.Background(), cloud.Settings{
		Values: map[string]string{"url": f.srv.URL, "username": "u"}, Secrets: map[string]string{"password": "wrong-secret-pw"}})
	_, err := p.List(context.Background(), "")
	if !errors.Is(err, cloud.ErrAuth) || strings.Contains(err.Error(), "wrong-secret-pw") {
		t.Fatalf("got %v", err)
	}
}

func TestOpenPutRemove(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "")
	ctx := context.Background()
	if err := p.PutNew(ctx, "d1/d2/f.txt", strings.NewReader("data!"), 5); err != nil {
		t.Fatal(err)
	}
	if !f.dirs["/d1/d2"] || f.files["/d1/d2/f.txt"] != "data!" {
		t.Fatal("parents or file missing")
	}
	if err := p.PutNew(ctx, "d1/d2/f.txt", strings.NewReader("zz"), 2); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want exists, got %v", err)
	}
	if f.files["/d1/d2/f.txt"] != "data!" {
		t.Fatal("overwritten")
	}
	rc, n, err := p.Open(ctx, "d1/d2/f.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "data!" || n != 5 {
		t.Fatalf("open: %q %d", b, n)
	}
	if err := p.PutNew(ctx, "empty.txt", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "d1"); err == nil {
		t.Fatal("folder removal must be refused")
	}
	if err := p.Remove(ctx, "d1/d2/f.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "d1/d2/f.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestTraversalRejected(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "a")
	for _, bad := range []string{"../x", "a/../../x", "/abs", "a\\b", "a//b"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
		if err := p.PutNew(context.Background(), bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("put %q accepted", bad)
		}
	}
}

func TestTokenAuthAndTOTP(t *testing.T) {
	f := newFake(t)
	f.token = "static-token"
	p, _ := New(context.Background(), cloud.Settings{Values: map[string]string{"url": f.srv.URL, "auth": "token"}, Secrets: map[string]string{"token": "static-token"}})
	if _, err := p.List(context.Background(), ""); err != nil || f.logins != 0 {
		t.Fatalf("token auth: %v", err)
	}
	// RFC 6238 test vector (SHA-1, secret "12345678901234567890"), 6 digits of 94287082 -> 287082
	c, err := totp("GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", time.Unix(59, 0))
	if err != nil || c != "287082" {
		t.Fatalf("totp %q %v", c, err)
	}
}

func TestBadURL(t *testing.T) {
	for _, u := range []string{"ftp://x", "http://u:p@host", "http://169.254.169.254", ""} {
		if _, err := New(context.Background(), cloud.Settings{Values: map[string]string{"url": u}}); err == nil {
			t.Fatalf("%q accepted", u)
		}
	}
}
