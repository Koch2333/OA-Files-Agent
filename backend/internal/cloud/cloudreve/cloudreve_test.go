package cloudreve

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

type sess struct {
	uri  string
	size int64
	buf  []byte
}

type fake struct {
	mu       sync.Mutex
	files    map[string]string // decoded path under cloudreve://my, e.g. "/a/b.txt"
	dirs     map[string]bool
	access   string
	refresh  string
	n        int
	logins   int
	refreshs int
	sessions map[string]*sess
	killAcc  bool // reject the current access token once
	srv      *httptest.Server
}

func newFake(t *testing.T) *fake {
	f := &fake{files: map[string]string{}, dirs: map[string]bool{"/": true}, sessions: map[string]*sess{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func ok(w http.ResponseWriter, data any) {
	json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "", "data": data})
}
func fail(w http.ResponseWriter, code int) {
	json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": "raw server text with secret-token", "data": nil})
}

func (f *fake) issue() map[string]any {
	f.n++
	f.access = "acc" + strconv.Itoa(f.n)
	f.refresh = "ref" + strconv.Itoa(f.n)
	return map[string]any{"access_token": f.access, "refresh_token": f.refresh,
		"access_expires": time.Now().Add(time.Hour).Format(time.RFC3339Nano), "refresh_expires": time.Now().Add(24 * time.Hour).Format(time.RFC3339Nano)}
}

func (f *fake) pathOf(uri string) string {
	u, _ := url.Parse(uri)
	p := u.Path
	if p == "" {
		p = "/"
	}
	return p
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	api := strings.TrimPrefix(r.URL.Path, "/api/v4")
	var in map[string]any
	if r.Body != nil && strings.Contains(r.Header.Get("Content-Type"), "json") {
		json.NewDecoder(r.Body).Decode(&in)
	}
	switch {
	case api == "/site/config/login":
		ok(w, map[string]any{"login_captcha": false})
		return
	case api == "/session/token":
		if in["email"] != "me@x.org" || in["password"] != "pw-secret" {
			fail(w, codeBadCreds)
			return
		}
		f.logins++
		ok(w, map[string]any{"token": f.issue()})
		return
	case api == "/session/token/refresh":
		if in["refresh_token"] != f.refresh || f.refresh == "" {
			fail(w, codeBadCreds)
			return
		}
		f.refreshs++
		ok(w, f.issue())
		return
	case strings.HasPrefix(r.URL.Path, "/dl/"):
		if c, found := f.files[strings.TrimPrefix(r.URL.Path, "/dl")]; found {
			io.WriteString(w, c)
			return
		}
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.access || f.killAcc {
		f.killAcc = false
		fail(w, codeExpired)
		return
	}
	q := r.URL.Query()
	switch {
	case api == "/file" && r.Method == "GET":
		d := f.pathOf(q.Get("uri"))
		if !f.dirs[d] {
			fail(w, codeNotExist)
			return
		}
		var all []map[string]any
		for fp, c := range f.files {
			if path.Dir(fp) == d {
				all = append(all, map[string]any{"type": 0, "name": path.Base(fp), "size": len(c), "updated_at": "2024-01-02T03:04:05Z"})
			}
		}
		for dp := range f.dirs {
			if dp != "/" && path.Dir(dp) == d {
				all = append(all, map[string]any{"type": 1, "name": path.Base(dp), "updated_at": "2024-01-02T03:04:05Z"})
			}
		}
		sort.Slice(all, func(i, j int) bool { return all[i]["name"].(string) < all[j]["name"].(string) })
		size, _ := strconv.Atoi(q.Get("page_size"))
		page, _ := strconv.Atoi(q.Get("page"))
		if size > 2 {
			size = 2 // small pages exercise pagination
		}
		start := page * size
		if start > len(all) {
			start = len(all)
		}
		end := start + size
		if end > len(all) {
			end = len(all)
		}
		ok(w, map[string]any{"files": all[start:end], "pagination": map[string]any{"is_cursor": false, "page": page}, "storage_policy": map[string]any{"id": "pol1"}})
	case api == "/file/info":
		p := f.pathOf(q.Get("uri"))
		if c, found := f.files[p]; found {
			ok(w, map[string]any{"type": 0, "name": path.Base(p), "size": len(c), "updated_at": "2024-01-02T03:04:05Z"})
		} else if f.dirs[p] {
			ok(w, map[string]any{"type": 1, "name": path.Base(p)})
		} else {
			fail(w, codeNotExist)
		}
	case api == "/file/url":
		uris := in["uris"].([]any)
		p := f.pathOf(uris[0].(string))
		if _, found := f.files[p]; !found {
			fail(w, codeNotExist)
			return
		}
		ok(w, map[string]any{"urls": []map[string]any{{"url": f.srv.URL + "/dl" + p}}})
	case api == "/file/create":
		p := f.pathOf(in["uri"].(string))
		_, isFile := f.files[p]
		if isFile || f.dirs[p] {
			fail(w, codeExists)
			return
		}
		if !f.dirs[path.Dir(p)] {
			fail(w, codeNotExist)
			return
		}
		if in["type"] == "folder" {
			f.dirs[p] = true
		} else {
			f.files[p] = ""
		}
		ok(w, nil)
	case api == "/file/upload" && r.Method == "PUT":
		p := f.pathOf(in["uri"].(string))
		if _, found := f.files[p]; found || f.dirs[p] {
			fail(w, codeExists)
			return
		}
		id := "s" + strconv.Itoa(len(f.sessions)+1)
		f.sessions[id] = &sess{uri: p, size: int64(in["size"].(float64))}
		ok(w, map[string]any{"session_id": id, "chunk_size": 4, "uri": in["uri"], "storage_policy": map[string]any{"type": "local"}})
	case api == "/file/upload" && r.Method == "DELETE":
		delete(f.sessions, in["id"].(string))
		ok(w, nil)
	case strings.HasPrefix(api, "/file/upload/") && r.Method == "POST":
		id := strings.Split(strings.TrimPrefix(api, "/file/upload/"), "/")[0]
		s := f.sessions[id]
		b, _ := io.ReadAll(r.Body)
		s.buf = append(s.buf, b...)
		if int64(len(s.buf)) >= s.size {
			f.files[s.uri] = string(s.buf)
		}
		ok(w, nil)
	case api == "/file" && r.Method == "DELETE":
		for _, u := range in["uris"].([]any) {
			delete(f.files, f.pathOf(u.(string)))
		}
		ok(w, nil)
	default:
		w.WriteHeader(404)
	}
}

type persisted struct {
	mu sync.Mutex
	m  map[string]string
}

func (ps *persisted) set(k, v string) { ps.mu.Lock(); ps.m[k] = v; ps.mu.Unlock() }

func connect(t *testing.T, f *fake, base string, ps *persisted, refresh string) cloud.Provider {
	t.Helper()
	cloudhttp.Backoff = time.Millisecond
	set := cloud.Settings{
		Values:   map[string]string{"url": f.srv.URL, "username": "me@x.org"},
		Secrets:  map[string]string{"password": "pw-secret"},
		BasePath: base,
	}
	if refresh != "" {
		set.Secrets["refresh_token"] = refresh
	}
	if ps != nil {
		set.Persist = ps.set
	}
	p, err := New(context.Background(), set)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestLoginReuseListStatOpen(t *testing.T) {
	f := newFake(t)
	f.dirs["/in"] = true
	for _, n := range []string{"a", "b", "c"} {
		f.files["/in/"+n+".txt"] = n
	}
	f.dirs["/in/sub dir"] = true
	ps := &persisted{m: map[string]string{}}
	p := connect(t, f, "in", ps, "")
	ctx := context.Background()
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 4 {
		t.Fatalf("%v %v", es, err)
	}
	if _, err := p.List(ctx, ""); err != nil || f.logins != 1 {
		t.Fatalf("token reuse: %v logins=%d", err, f.logins)
	}
	if ps.m["refresh_token"] != "ref1" {
		t.Fatalf("refresh token not persisted: %v", ps.m)
	}
	e, err := p.Stat(ctx, "a.txt")
	if err != nil || e.Size != 1 || e.IsDir {
		t.Fatalf("%v %v", e, err)
	}
	if e, err := p.Stat(ctx, "sub dir"); err != nil || !e.IsDir {
		t.Fatalf("%v %v", e, err)
	}
	if _, err := p.Stat(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := p.List(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	rc, n, err := p.Open(ctx, "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "b" || n != 1 {
		t.Fatalf("%q %d", b, n)
	}
}

func TestRefreshRotationAndFallbackToLogin(t *testing.T) {
	f := newFake(t)
	f.dirs["/x"] = true
	// first session logs in and yields ref1; a second provider starts from that refresh token
	p1 := connect(t, f, "", nil, "")
	if _, err := p1.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	ps := &persisted{m: map[string]string{}}
	p2 := connect(t, f, "", ps, f.refresh)
	if _, err := p2.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if f.refreshs != 1 || f.logins != 1 || ps.m["refresh_token"] != f.refresh {
		t.Fatalf("refreshs=%d logins=%d persisted=%v", f.refreshs, f.logins, ps.m)
	}
	// server rejects the access token: the provider refreshes and retries
	f.killAcc = true
	if _, err := p2.List(context.Background(), ""); err != nil || f.refreshs != 2 {
		t.Fatalf("%v refreshs=%d", err, f.refreshs)
	}
	// a stale/invalid refresh token falls back to the password; nil Persist is tolerated
	p3 := connect(t, f, "", nil, "bogus")
	if _, err := p3.List(context.Background(), ""); err != nil || f.logins != 2 {
		t.Fatalf("%v logins=%d", err, f.logins)
	}
}

func TestBadPasswordNoLeak(t *testing.T) {
	f := newFake(t)
	p, _ := New(context.Background(), cloud.Settings{Values: map[string]string{"url": f.srv.URL, "username": "me@x.org"}, Secrets: map[string]string{"password": "wrong-pw-xyz"}})
	_, err := p.List(context.Background(), "")
	if !errors.Is(err, cloud.ErrAuth) || strings.Contains(err.Error(), "wrong-pw-xyz") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("%v", err)
	}
}

func TestPutNewParentsConflictRemove(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil, "")
	ctx := context.Background()
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("0123456789"), 10); err != nil {
		t.Fatal(err)
	}
	if !f.dirs["/a/b"] || f.files["/a/b/c.txt"] != "0123456789" {
		t.Fatalf("%v %v", f.dirs, f.files)
	}
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("zz"), 2); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	if f.files["/a/b/c.txt"] != "0123456789" {
		t.Fatal("overwritten")
	}
	if err := p.PutNew(ctx, "a/b/e.txt", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	if err := p.PutNew(ctx, "a/b/e.txt", strings.NewReader(""), 0); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	// a source shorter than declared must fail and cancel the session
	if err := p.PutNew(ctx, "a/short.txt", strings.NewReader("ab"), 9); err == nil {
		t.Fatal("short source accepted")
	}
	if _, ok := f.files["/a/short.txt"]; ok {
		t.Fatal("partial file created")
	}
	if err := p.Remove(ctx, "a/b"); err == nil {
		t.Fatal("folder removal must be refused")
	}
	if err := p.Remove(ctx, "a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.files["/a/b/c.txt"]; ok {
		t.Fatal("not removed")
	}
}

func TestTraversalAndBadSettings(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil, "")
	for _, bad := range []string{"../x", "a/../b", "/abs", "a\\b", "a//b"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Fatalf("%q", bad)
		}
		if err := p.PutNew(context.Background(), bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("put %q", bad)
		}
	}
	for _, s := range []cloud.Settings{
		{Values: map[string]string{"url": "http://169.254.169.254"}},
		{Values: map[string]string{"url": f.srv.URL, "root_uri": "file:///etc"}},
	} {
		if _, err := New(context.Background(), s); err == nil {
			t.Fatalf("%v accepted", s.Values)
		}
	}
}
