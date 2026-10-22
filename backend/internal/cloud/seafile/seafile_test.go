package seafile

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

const repoID = "11111111-2222-3333-4444-555555555555"

type fake struct {
	mu           sync.Mutex
	files        map[string]string // "/dir/name" -> content
	dirs         map[string]bool
	token        string
	logins       int
	expire       bool
	beforeUpload func(f *fake)
	srv          *httptest.Server
}

func newFake(t *testing.T) *fake {
	f := &fake{files: map[string]string{}, dirs: map[string]bool{"/": true}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) { json.NewEncoder(w).Encode(v) }

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api2/auth-token/":
		r.ParseForm()
		if r.PostForm.Get("username") != "me@x.org" || r.PostForm.Get("password") != "s3cret-pw" {
			w.WriteHeader(400)
			io.WriteString(w, `{"non_field_errors":["Unable to login with provided credentials. s3cret-pw"]}`)
			return
		}
		f.logins++
		f.token = "T" + string(rune('0'+f.logins))
		writeJSON(w, map[string]string{"token": f.token})
		return
	case strings.HasPrefix(r.URL.Path, "/seafhttp/files/"):
		if c, ok := f.files[strings.TrimPrefix(r.URL.Path, "/seafhttp/files")]; ok {
			io.WriteString(w, c)
			return
		}
		http.NotFound(w, r)
		return
	case strings.HasPrefix(r.URL.Path, "/seafhttp/upload-api/"):
		f.upload(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Token "+f.token || f.expire {
		f.expire = false
		w.WriteHeader(401)
		return
	}
	p := r.URL.Query().Get("p")
	switch {
	case r.URL.Path == "/api2/repos/":
		writeJSON(w, []map[string]any{{"id": "other", "name": "Other"}, {"id": repoID, "name": "Docs", "encrypted": false}})
	case r.URL.Path == "/api2/repos/"+repoID+"/dir/" && r.Method == "GET":
		if !f.dirs[p] {
			w.WriteHeader(404)
			return
		}
		var out []map[string]any
		for fp, c := range f.files {
			if path.Dir(fp) == p {
				out = append(out, map[string]any{"type": "file", "name": path.Base(fp), "size": len(c), "mtime": 1700000000})
			}
		}
		for dp := range f.dirs {
			if dp != "/" && path.Dir(dp) == p {
				out = append(out, map[string]any{"type": "dir", "name": path.Base(dp), "size": 0, "mtime": 1700000000})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
		writeJSON(w, out)
	case r.URL.Path == "/api2/repos/"+repoID+"/dir/" && r.Method == "POST":
		r.ParseForm()
		if r.PostForm.Get("operation") != "mkdir" || !f.dirs[path.Dir(p)] {
			w.WriteHeader(400)
			return
		}
		f.dirs[p] = true
		w.WriteHeader(201)
		io.WriteString(w, `"success"`)
	case r.URL.Path == "/api2/repos/"+repoID+"/file/" && r.Method == "GET":
		if _, ok := f.files[p]; !ok {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, f.srv.URL+"/seafhttp/files"+p)
	case r.URL.Path == "/api2/repos/"+repoID+"/file/" && r.Method == "DELETE":
		if _, ok := f.files[p]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(f.files, p)
		io.WriteString(w, `"success"`)
	case r.URL.Path == "/api2/repos/"+repoID+"/upload-link/":
		if !f.dirs[p] {
			w.WriteHeader(404)
			return
		}
		writeJSON(w, f.srv.URL+"/seafhttp/upload-api/tok123")
	default:
		w.WriteHeader(404)
	}
}

func (f *fake) upload(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ret-json") != "1" || r.Header.Get("Authorization") != "" {
		w.WriteHeader(400)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		w.WriteHeader(400)
		return
	}
	if r.MultipartForm.Value["replace"][0] != "0" {
		w.WriteHeader(400)
		return
	}
	parent := r.MultipartForm.Value["parent_dir"][0]
	fh := r.MultipartForm.File["file"][0]
	rd, _ := fh.Open()
	b, _ := io.ReadAll(rd)
	if f.beforeUpload != nil {
		f.beforeUpload(f)
	}
	name := fh.Filename
	if _, ok := f.files[path.Join(parent, name)]; ok { // real Seafile renames on replace=0
		ext := path.Ext(name)
		name = strings.TrimSuffix(name, ext) + " (1)" + ext
	}
	f.files[path.Join(parent, name)] = string(b)
	writeJSON(w, []map[string]any{{"name": name, "id": "x", "size": len(b)}})
}

func connect(t *testing.T, f *fake, base string) cloud.Provider {
	t.Helper()
	cloudhttp.Backoff = time.Millisecond
	p, err := New(context.Background(), cloud.Settings{
		Values:   map[string]string{"url": f.srv.URL, "auth": "password", "username": "me@x.org", "library": "Docs"},
		Secrets:  map[string]string{"password": "s3cret-pw"},
		BasePath: base,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestListStatOpenAndLogin(t *testing.T) {
	f := newFake(t)
	f.dirs["/in"] = true
	f.files["/in/a.txt"] = "hello"
	f.dirs["/in/sub"] = true
	p := connect(t, f, "in")
	ctx := context.Background()
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 2 {
		t.Fatalf("%v %v", es, err)
	}
	e, err := p.Stat(ctx, "a.txt")
	if err != nil || e.Size != 5 || e.IsDir {
		t.Fatalf("%v %v", e, err)
	}
	if e, err := p.Stat(ctx, "sub"); err != nil || !e.IsDir {
		t.Fatalf("%v %v", e, err)
	}
	if _, err := p.Stat(ctx, "zzz"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	rc, n, err := p.Open(ctx, "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" || n != 5 {
		t.Fatalf("%q %d", b, n)
	}
	if f.logins != 1 {
		t.Fatalf("token should be reused: %d", f.logins)
	}
	f.expire = true
	if _, err := p.List(ctx, ""); err != nil || f.logins != 2 {
		t.Fatalf("relogin: %v %d", err, f.logins)
	}
}

func TestTokenModeAndBadLogin(t *testing.T) {
	f := newFake(t)
	f.token = "static"
	p, err := New(context.Background(), cloud.Settings{Values: map[string]string{"url": f.srv.URL, "auth": "token", "library": repoID}, Secrets: map[string]string{"token": "static"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.List(context.Background(), ""); err != nil || f.logins != 0 {
		t.Fatalf("%v", err)
	}
	bad, _ := New(context.Background(), cloud.Settings{Values: map[string]string{"url": f.srv.URL, "username": "me@x.org", "library": "Docs"}, Secrets: map[string]string{"password": "nope-nope"}})
	_, err = bad.List(context.Background(), "")
	if !errors.Is(err, cloud.ErrAuth) || strings.Contains(err.Error(), "nope-nope") || strings.Contains(err.Error(), "Unable") {
		t.Fatalf("%v", err)
	}
	missing, _ := New(context.Background(), cloud.Settings{Values: map[string]string{"url": f.srv.URL, "auth": "token", "library": "Nope"}, Secrets: map[string]string{"token": "static"}})
	if _, err := missing.List(context.Background(), ""); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestPutNewParentsConflictRemove(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "")
	ctx := context.Background()
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}
	if !f.dirs["/a/b"] || f.files["/a/b/c.txt"] != "12345" {
		t.Fatal("parents/file missing")
	}
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("zz"), 2); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	if len(f.files) != 1 || f.files["/a/b/c.txt"] != "12345" {
		t.Fatalf("files changed: %v", f.files)
	}
	if err := p.PutNew(ctx, "a/b/empty", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	// race: the target appears after the existence check; the renamed stray copy must be removed
	f.beforeUpload = func(f *fake) { f.files["/a/b/race.txt"] = "theirs" }
	if err := p.PutNew(ctx, "a/b/race.txt", strings.NewReader("mine"), 4); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	if f.files["/a/b/race.txt"] != "theirs" || len(f.files) != 3 {
		t.Fatalf("stray not cleaned: %v", f.files)
	}
	f.beforeUpload = nil
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

func TestTraversalRejected(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "")
	for _, bad := range []string{"../x", "a/../b", "/abs", "a\\b"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Fatalf("%q", bad)
		}
		if err := p.PutNew(context.Background(), bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("put %q", bad)
		}
	}
	if _, err := New(context.Background(), cloud.Settings{Values: map[string]string{"url": "http://169.254.169.254", "library": "x"}}); err == nil {
		t.Fatal("metadata address accepted")
	}
}
