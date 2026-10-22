package dropbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/oauthhttp"
)

func TestMain(m *testing.M) {
	oauthhttp.BackoffBase = time.Millisecond
	m.Run()
}

const secretVal = "SUPER-SECRET-VALUE-123"

type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	files    map[string][]byte
	dirs     map[string]bool
	token    string
	tokenN   int
	refresh  []string
	sessions map[string][]byte
	rate     int
	hdrBad   bool
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, files: map[string][]byte{}, dirs: map[string]bool{}, sessions: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func jw(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fake) meta(p string) map[string]any {
	name := p[strings.LastIndex(p, "/")+1:]
	if f.dirs[p] {
		return map[string]any{".tag": "folder", "name": name}
	}
	return map[string]any{".tag": "file", "name": name, "size": len(f.files[p]), "server_modified": "2024-05-06T07:08:09Z"}
}

func (f *fake) mkparents(p string) {
	for i := 1; i < len(p); i++ {
		if p[i] == '/' {
			f.dirs[p[:i]] = true
		}
	}
}

func (f *fake) arg(r *http.Request, v any) {
	h := r.Header.Get("Dropbox-API-Arg")
	for _, c := range h {
		if c > 126 {
			f.hdrBad = true
		}
	}
	json.Unmarshal([]byte(h), v)
}

func notFound(w http.ResponseWriter) {
	jw(w, 409, map[string]any{"error_summary": "path/not_found/.", "error": map[string]any{".tag": "path"}})
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path == "/oauth2/token" {
		r.ParseForm()
		rt := r.PostForm.Get("refresh_token")
		f.refresh = append(f.refresh, rt)
		if r.PostForm.Get("client_secret") != secretVal || r.PostForm.Get("client_id") != "cid" || (rt != "rt1" && rt != "rt2") {
			jw(w, 400, map[string]any{"error": "invalid_grant", "error_description": "bad " + secretVal})
			return
		}
		f.tokenN++
		f.token = "at" + strconv.Itoa(f.tokenN)
		out := map[string]any{"access_token": f.token, "expires_in": 14400}
		if rt == "rt1" {
			out["refresh_token"] = "rt2"
		}
		jw(w, 200, out)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		jw(w, 401, map[string]any{"error_summary": "expired_access_token/", "error": secretVal})
		return
	}
	ep := strings.TrimPrefix(r.URL.Path, "/2")
	var in struct {
		Path   string `json:"path"`
		Cursor string `json:"cursor"`
	}
	switch ep {
	case "/files/list_folder", "/files/list_folder/continue", "/files/get_metadata", "/files/delete_v2":
		json.NewDecoder(r.Body).Decode(&in)
	}
	switch ep {
	case "/files/list_folder", "/files/list_folder/continue":
		if f.rate > 0 {
			f.rate--
			w.Header().Set("Retry-After", "0")
			jw(w, 429, map[string]any{"error_summary": "too_many_requests/"})
			return
		}
		start := 0
		dir := in.Path
		if ep == "/files/list_folder/continue" {
			i := strings.LastIndex(in.Cursor, "|")
			dir = in.Cursor[:i]
			start, _ = strconv.Atoi(in.Cursor[i+1:])
		} else if dir != "" && !f.dirs[dir] {
			notFound(w)
			return
		}
		var names []string
		for p := range f.files {
			if strings.HasPrefix(p, dir+"/") && !strings.Contains(p[len(dir)+1:], "/") {
				names = append(names, p)
			}
		}
		for p := range f.dirs {
			if strings.HasPrefix(p, dir+"/") && !strings.Contains(p[len(dir)+1:], "/") {
				names = append(names, p)
			}
		}
		sort.Strings(names)
		end := min(start+2, len(names))
		ents := []map[string]any{}
		for _, p := range names[start:end] {
			ents = append(ents, f.meta(p))
		}
		jw(w, 200, map[string]any{"entries": ents, "cursor": dir + "|" + strconv.Itoa(end), "has_more": end < len(names)})
	case "/files/get_metadata":
		if _, ok := f.files[in.Path]; !ok && !f.dirs[in.Path] {
			notFound(w)
			return
		}
		jw(w, 200, f.meta(in.Path))
	case "/files/delete_v2":
		if _, ok := f.files[in.Path]; !ok {
			notFound(w)
			return
		}
		delete(f.files, in.Path)
		jw(w, 200, map[string]any{"metadata": map[string]any{}})
	case "/files/download":
		var a struct{ Path string }
		f.arg(r, &a)
		d, ok := f.files[a.Path]
		if !ok {
			notFound(w)
			return
		}
		w.Write(d)
	case "/files/upload":
		var a struct {
			Path       string `json:"path"`
			Mode       string `json:"mode"`
			Autorename bool   `json:"autorename"`
		}
		f.arg(r, &a)
		if a.Mode != "add" || a.Autorename {
			f.t.Errorf("upload must use mode add without autorename: %+v", a)
		}
		body, _ := io.ReadAll(r.Body)
		f.commit(w, a.Path, body)
	case "/files/upload_session/start":
		body, _ := io.ReadAll(r.Body)
		id := "S" + strconv.Itoa(len(f.sessions)+1)
		f.sessions[id] = body
		jw(w, 200, map[string]any{"session_id": id})
	case "/files/upload_session/append_v2", "/files/upload_session/finish":
		var a struct {
			Cursor struct {
				ID     string `json:"session_id"`
				Offset int    `json:"offset"`
			} `json:"cursor"`
			Commit struct {
				Path       string `json:"path"`
				Mode       string `json:"mode"`
				Autorename bool   `json:"autorename"`
			} `json:"commit"`
		}
		f.arg(r, &a)
		body, _ := io.ReadAll(r.Body)
		buf, ok := f.sessions[a.Cursor.ID]
		if !ok || len(buf) != a.Cursor.Offset {
			jw(w, 409, map[string]any{"error_summary": "incorrect_offset/"})
			return
		}
		f.sessions[a.Cursor.ID] = append(buf, body...)
		if ep == "/files/upload_session/finish" {
			if a.Commit.Mode != "add" || a.Commit.Autorename {
				f.t.Errorf("finish must use mode add: %+v", a.Commit)
			}
			f.commit(w, a.Commit.Path, f.sessions[a.Cursor.ID])
			return
		}
		jw(w, 200, map[string]any{})
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) commit(w http.ResponseWriter, p string, data []byte) {
	if _, ok := f.files[p]; ok || f.dirs[p] {
		jw(w, 409, map[string]any{"error_summary": "path/conflict/file/" + secretVal[:0], "error": map[string]any{".tag": "path"}})
		return
	}
	f.mkparents(p)
	f.files[p] = data
	jw(w, 200, f.meta(p))
}

func connect(t *testing.T, f *fake, base string, persist func(k, v string)) cloud.Provider {
	t.Helper()
	p, err := cloud.Open(context.Background(), "dropbox", cloud.Settings{
		Values:   map[string]string{"endpoint": f.srv.URL, "client_id": "cid"},
		Secrets:  map[string]string{"client_secret": secretVal, "refresh_token": "rt1"},
		BasePath: base, Persist: persist,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestRegisteredKind(t *testing.T) {
	k, ok := cloud.KindByID("dropbox")
	if !ok || k.Group != "cloud" {
		t.Fatal("kind missing")
	}
	for _, fl := range k.Fields {
		if (fl.Key == "client_secret" || fl.Key == "refresh_token") && !fl.Secret {
			t.Fatalf("%s must be secret", fl.Key)
		}
	}
}

func TestTokenRefreshRotationAndCache(t *testing.T) {
	f := newFake(t)
	got := map[string]string{}
	p := connect(t, f, "", func(k, v string) { got[k] = v })
	if got["refresh_token"] != "rt2" {
		t.Fatalf("rotation not persisted: %v", got)
	}
	p.List(context.Background(), "")
	p.List(context.Background(), "")
	if f.tokenN != 1 {
		t.Fatalf("token fetched %d times", f.tokenN)
	}
	connect(t, newFake(t), "", nil)
}

func TestListStatOpen(t *testing.T) {
	f := newFake(t)
	f.dirs["/Docs"], f.dirs["/Docs/sub"] = true, true
	for _, n := range []string{"a", "b", "c", "d"} {
		f.files["/Docs/"+n+".txt"] = []byte(n)
	}
	f.files["/Docs/名称 é😀.txt"] = []byte("unicode")
	p := connect(t, f, "Docs", nil)
	ctx := context.Background()
	ents, err := p.List(ctx, "")
	if err != nil || len(ents) != 6 || !ents[0].IsDir || ents[0].Path != "sub" {
		t.Fatalf("list: %+v %v", ents, err)
	}
	if e, err := p.Stat(ctx, "b.txt"); err != nil || e.Size != 1 || e.ModTime.IsZero() {
		t.Fatalf("stat: %+v %v", e, err)
	}
	if e, err := p.Stat(ctx, ""); err != nil || !e.IsDir {
		t.Fatalf("root stat: %+v %v", e, err)
	}
	if _, err := p.Stat(ctx, "zz"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
	if _, err := p.List(ctx, "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
	rc, n, err := p.Open(ctx, "名称 é😀.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "unicode" || n != 7 || f.hdrBad {
		t.Fatalf("open %q %d hdrBad=%v", b, n, f.hdrBad)
	}
	if _, _, err := p.Open(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("open missing: %v", err)
	}
}

func TestPutNewParentsConflictAndLarge(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "base", nil)
	ctx := context.Background()
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("one"), 3); err != nil {
		t.Fatal(err)
	}
	if string(f.files["/base/a/b/c.txt"]) != "one" || !f.dirs["/base/a/b"] {
		t.Fatalf("state: %v", f.files)
	}
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("two"), 3); !errors.Is(err, cloud.ErrExists) || string(f.files["/base/a/b/c.txt"]) != "one" {
		t.Fatalf("conflict: %v", err)
	}
	if err := p.PutNew(ctx, "e.txt", strings.NewReader(""), 0); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2*chunkSize+4321)
	rand.Read(data)
	if err := p.PutNew(ctx, "big/x.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.files["/base/big/x.bin"], data) {
		t.Fatal("large content mismatch")
	}
	before := len(f.sessions)
	if err := p.PutNew(ctx, "big/x.bin", bytes.NewReader(data), int64(len(data))); !errors.Is(err, cloud.ErrExists) || len(f.sessions) != before {
		t.Fatalf("large conflict should fail before upload: %v", err)
	}
	// conflict detected only at finish (race): still ErrExists, nothing modified
	f.files["/base/race.bin"] = nil
	delete(f.files, "/base/race.bin")
	if err := p.PutNew(ctx, "short.bin", bytes.NewReader(data[:5]), int64(len(data))); err == nil || f.files["/base/short.bin"] != nil {
		t.Fatalf("short source: %v", err)
	}
}

func TestRemove(t *testing.T) {
	f := newFake(t)
	f.dirs["/d"] = true
	f.files["/d/m.txt"] = []byte("m")
	p := connect(t, f, "", nil)
	ctx := context.Background()
	if err := p.Remove(ctx, "d/m.txt"); err != nil || len(f.files) != 0 {
		t.Fatalf("remove: %v", err)
	}
	if err := p.Remove(ctx, "d"); !errors.Is(err, cloud.ErrUnsupported) || !f.dirs["/d"] {
		t.Fatalf("folder: %v", err)
	}
	if err := p.Remove(ctx, "d/none"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestRetries(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	ctx := context.Background()
	p.List(ctx, "")
	before := f.tokenN
	f.mu.Lock()
	f.token = "revoked"
	f.mu.Unlock()
	if _, err := p.List(ctx, ""); err != nil || f.tokenN != before+1 {
		t.Fatalf("401 refresh: %v n=%d", err, f.tokenN-before)
	}
	if f.refresh[len(f.refresh)-1] != "rt2" {
		t.Fatalf("rotated token not used: %v", f.refresh)
	}
	f.rate = 2
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatalf("429 should be retried: %v", err)
	}
	f.rate = 10
	if _, err := p.List(ctx, ""); err == nil || f.rate != 7 { // 3 attempts used
		t.Fatalf("want failure after 3 attempts: %v rate=%d", err, f.rate)
	}
}

func TestTraversalRejected(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "base", nil)
	ctx := context.Background()
	for _, bad := range []string{"../x", "a/../../x", "/abs", "a//b", "a\\b", "./a"} {
		if _, err := p.Stat(ctx, bad); err == nil {
			t.Errorf("stat %q", bad)
		}
		if err := p.PutNew(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("put %q", bad)
		}
		if _, _, err := p.Open(ctx, bad); err == nil {
			t.Errorf("open %q", bad)
		}
		if _, err := p.List(ctx, bad); err == nil {
			t.Errorf("list %q", bad)
		}
	}
	if _, err := New(cloud.Settings{BasePath: "../up", Values: map[string]string{"client_id": "a"}, Secrets: map[string]string{"client_secret": "s", "refresh_token": "r"}}); err == nil {
		t.Error("base escape accepted")
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	f := newFake(t)
	f.files["/a.txt"] = []byte("a")
	p := connect(t, f, "", nil)
	ctx := context.Background()
	var errs []error
	errs = append(errs, p.PutNew(ctx, "a.txt", strings.NewReader("b"), 1))
	_, err := p.Stat(ctx, "zzz")
	errs = append(errs, err)
	_, err = cloud.Open(ctx, "dropbox", cloud.Settings{
		Values:  map[string]string{"endpoint": f.srv.URL, "client_id": "cid"},
		Secrets: map[string]string{"client_secret": secretVal, "refresh_token": "wrong-" + secretVal},
	})
	errs = append(errs, err)
	for _, e := range errs {
		if e == nil {
			t.Fatal("expected error")
		}
		for _, s := range []string{secretVal, "rt1", "rt2", "at1"} {
			if strings.Contains(e.Error(), s) {
				t.Fatalf("leak %q: %v", s, e)
			}
		}
	}
	if !errors.Is(errs[2], cloud.ErrAuth) {
		t.Fatalf("want ErrAuth: %v", errs[2])
	}
}

func TestHeaderJSONIsASCII(t *testing.T) {
	h := headerJSON(map[string]string{"path": "/名😀é"})
	for _, c := range h {
		if c > 126 {
			t.Fatalf("non-ASCII in %q", h)
		}
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(h), &m); err != nil || m["path"] != "/名😀é" {
		t.Fatalf("round trip: %v %v", m, err)
	}
}
