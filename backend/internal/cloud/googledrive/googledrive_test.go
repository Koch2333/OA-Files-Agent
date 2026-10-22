package googledrive

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
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

type gnode struct {
	id, name, parent, mime string
	data                   []byte
	seq                    int
}

type sess struct {
	n     *gnode
	buf   []byte
	total int
}

type fake struct {
	t   *testing.T
	srv *httptest.Server

	mu       sync.Mutex
	nodes    []*gnode
	seq      int
	token    string
	tokenN   int
	refresh  []string
	sessions map[string]*sess
	rate403  int
	creates  int
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, sessions: map[string]*sess{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) add(name, parent, mime string, data []byte) *gnode {
	f.seq++
	n := &gnode{id: "id" + strconv.Itoa(f.seq), name: name, parent: parent, mime: mime, data: data, seq: f.seq}
	f.nodes = append(f.nodes, n)
	return n
}

func (f *fake) byID(id string) *gnode {
	for _, n := range f.nodes {
		if n.id == id {
			return n
		}
	}
	return nil
}

func (f *fake) find(parent, name string) *gnode {
	for _, n := range f.nodes {
		if n.parent == parent && n.name == name {
			return n
		}
	}
	return nil
}

func (f *fake) remove(id string) {
	for i, n := range f.nodes {
		if n.id == id {
			f.nodes = append(f.nodes[:i], f.nodes[i+1:]...)
			return
		}
	}
}

func jw(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fake) js(n *gnode) map[string]any {
	return map[string]any{"id": n.id, "name": n.name, "mimeType": n.mime, "size": strconv.Itoa(len(n.data)),
		"modifiedTime": "2024-05-06T07:08:09Z", "createdTime": time.Unix(1700000000+int64(n.seq), 0).UTC().Format(time.RFC3339)}
}

var (
	parentRe = regexp.MustCompile(`'((?:[^'\\]|\\.)*)' in parents`)
	nameRe   = regexp.MustCompile(`name='((?:[^'\\]|\\.)*)'`)
	unesc    = strings.NewReplacer(`\\`, `\`, `\'`, `'`)
)

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	if path == "/token" {
		r.ParseForm()
		rt := r.PostForm.Get("refresh_token")
		f.refresh = append(f.refresh, rt)
		if r.PostForm.Get("client_secret") != secretVal || (rt != "rt1" && rt != "rt2") {
			jw(w, 400, map[string]any{"error": "invalid_grant", "error_description": "bad " + secretVal})
			return
		}
		f.tokenN++
		f.token = "at" + strconv.Itoa(f.tokenN)
		out := map[string]any{"access_token": f.token, "expires_in": 3600}
		if rt == "rt1" {
			out["refresh_token"] = "rt2"
		}
		jw(w, 200, out)
		return
	}
	if strings.HasPrefix(path, "/session/") {
		f.session(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		jw(w, 401, map[string]any{"error": map[string]any{"message": "token " + secretVal}})
		return
	}
	switch {
	case r.Method == "GET" && path == "/drive/v3/files":
		if f.rate403 > 0 {
			f.rate403--
			jw(w, 403, map[string]any{"error": map[string]any{"errors": []map[string]any{{"reason": "rateLimitExceeded"}}}})
			return
		}
		q := r.URL.Query().Get("q")
		pm := parentRe.FindStringSubmatch(q)
		if pm == nil || !strings.Contains(q, "trashed=false") {
			jw(w, 400, map[string]any{})
			return
		}
		parent := unesc.Replace(pm[1])
		var match []*gnode
		nm := nameRe.FindStringSubmatch(q)
		for _, n := range f.nodes {
			if n.parent == parent && (nm == nil || n.name == unesc.Replace(nm[1])) {
				match = append(match, n)
			}
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("pageToken"))
		end := min(start+2, len(match))
		files := []map[string]any{}
		for _, n := range match[start:end] {
			files = append(files, f.js(n))
		}
		res := map[string]any{"files": files}
		if end < len(match) {
			res["nextPageToken"] = strconv.Itoa(end)
		}
		jw(w, 200, res)
	case r.Method == "POST" && path == "/drive/v3/files":
		var b struct {
			Name     string   `json:"name"`
			MimeType string   `json:"mimeType"`
			Parents  []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		f.creates++
		jw(w, 200, f.js(f.add(b.Name, b.Parents[0], b.MimeType, nil)))
	case r.Method == "GET" && strings.HasPrefix(path, "/drive/v3/files/"):
		n := f.byID(strings.TrimPrefix(path, "/drive/v3/files/"))
		if n == nil || r.URL.Query().Get("alt") != "media" {
			jw(w, 404, map[string]any{})
			return
		}
		w.Write(n.data)
	case r.Method == "DELETE" && strings.HasPrefix(path, "/drive/v3/files/"):
		id := strings.TrimPrefix(path, "/drive/v3/files/")
		if f.byID(id) == nil {
			jw(w, 404, map[string]any{})
			return
		}
		f.remove(id)
		w.WriteHeader(204)
	case r.Method == "POST" && path == "/upload/drive/v3/files":
		var b struct {
			Name    string   `json:"name"`
			Parents []string `json:"parents"`
		}
		json.NewDecoder(r.Body).Decode(&b)
		total, _ := strconv.Atoi(r.Header.Get("X-Upload-Content-Length"))
		id := "s" + strconv.Itoa(len(f.sessions)+1)
		f.sessions[id] = &sess{n: &gnode{name: b.Name, parent: b.Parents[0], mime: "application/octet-stream"}, total: total}
		w.Header().Set("Location", f.srv.URL+"/session/"+id)
		w.WriteHeader(200)
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) session(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("Authorization sent to session URL")
	}
	id := strings.TrimPrefix(r.URL.Path, "/session/")
	s := f.sessions[id]
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == "DELETE" {
		delete(f.sessions, id)
		w.WriteHeader(499)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if s.total == 0 {
		f.seq++
		s.n.id, s.n.seq = "id"+strconv.Itoa(f.seq), f.seq
		f.nodes = append(f.nodes, s.n)
		jw(w, 200, map[string]any{"id": s.n.id})
		return
	}
	var a, b, total int
	fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &a, &b, &total)
	if a != len(s.buf) || len(body) != b-a+1 {
		w.WriteHeader(416)
		return
	}
	s.buf = append(s.buf, body...)
	if len(s.buf) < total {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(s.buf)-1))
		w.WriteHeader(308)
		return
	}
	f.seq++
	s.n.id, s.n.seq, s.n.data = "id"+strconv.Itoa(f.seq), f.seq, s.buf
	f.nodes = append(f.nodes, s.n)
	jw(w, 200, map[string]any{"id": s.n.id})
}

func connect(t *testing.T, f *fake, base string, persist func(k, v string)) cloud.Provider {
	t.Helper()
	p, err := cloud.Open(context.Background(), "googledrive", cloud.Settings{
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
	k, ok := cloud.KindByID("googledrive")
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
	connect(t, newFake(t), "", nil) // nil Persist tolerated
}

func TestListPaginationDuplicatesAndNative(t *testing.T) {
	f := newFake(t)
	docs := f.add("Docs", "root", folderMime, nil)
	f.add("sub", docs.id, folderMime, nil)
	for i := 0; i < 4; i++ {
		f.add(fmt.Sprintf("f%d.txt", i), docs.id, "text/plain", []byte(strings.Repeat("x", i)))
	}
	f.add("dup.txt", docs.id, "text/plain", []byte("old"))
	f.add("dup.txt", docs.id, "text/plain", []byte("newer"))
	f.add("sheet", docs.id, "application/vnd.google-apps.spreadsheet", nil)
	f.add("it's.txt", docs.id, "text/plain", []byte("quote"))
	p := connect(t, f, "Docs", nil)
	ents, err := p.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]int{}
	for _, e := range ents {
		names[e.Name]++
	}
	if len(ents) != 7 || names["dup.txt"] != 1 || names["sheet"] != 0 || !ents[0].IsDir {
		t.Fatalf("entries: %+v", ents)
	}
	rc, n, err := p.Open(context.Background(), "dup.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "old" || n != 3 {
		t.Fatalf("duplicate must resolve to the oldest, got %q", b)
	}
	e, err := p.Stat(context.Background(), "it's.txt")
	if err != nil || e.Size != 5 {
		t.Fatalf("stat quote: %+v %v", e, err)
	}
	if _, err := p.Stat(context.Background(), "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
	if _, err := p.List(context.Background(), "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
	if _, _, err := p.Open(context.Background(), "sheet"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatalf("native docs: %v", err)
	}
	if _, err := New(cloud.Settings{BasePath: "x", Values: map[string]string{"client_id": "a"}, Secrets: map[string]string{"client_secret": "s", "refresh_token": "r"}}); err != nil {
		t.Fatal(err)
	}
	// missing base path
	pm := connect(t, f, "Nowhere", nil)
	if _, err := pm.List(context.Background(), ""); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("missing base: %v", err)
	}
}

func TestPutNewParentsConflictAndLarge(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	ctx := context.Background()
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("one"), 3); err != nil {
		t.Fatal(err)
	}
	a := f.find("root", "a")
	b := f.find(a.id, "b")
	c := f.find(b.id, "c.txt")
	if a == nil || b == nil || c == nil || string(c.data) != "one" {
		t.Fatal("parents or file missing")
	}
	if err := p.PutNew(ctx, "a/b/d.txt", strings.NewReader("two"), 3); err != nil || f.creates != 2 {
		t.Fatalf("folders must be reused: %v creates=%d", err, f.creates)
	}
	err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("zzz"), 3)
	if !errors.Is(err, cloud.ErrExists) || string(c.data) != "one" || len(f.sessions) != 2 {
		t.Fatalf("conflict: %v", err)
	}
	if err := p.PutNew(ctx, "a/b", strings.NewReader("x"), 1); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("folder name: %v", err)
	}
	if err := p.PutNew(ctx, "e.txt", strings.NewReader(""), 0); err != nil || f.find("root", "e.txt") == nil {
		t.Fatalf("empty: %v", err)
	}
	data := make([]byte, 8<<20+777)
	rand.Read(data)
	if err := p.PutNew(ctx, "big.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if n := f.find("root", "big.bin"); n == nil || !bytes.Equal(n.data, data) {
		t.Fatal("large content mismatch")
	}
	if err := p.PutNew(ctx, "short.bin", bytes.NewReader(data[:10]), int64(len(data))); err == nil || f.find("root", "short.bin") != nil {
		t.Fatalf("short source: %v", err)
	}
}

func TestRemove(t *testing.T) {
	f := newFake(t)
	d := f.add("d", "root", folderMime, nil)
	f.add("m.txt", d.id, "text/plain", []byte("m"))
	p := connect(t, f, "", nil)
	if err := p.Remove(context.Background(), "d/m.txt"); err != nil || f.find(d.id, "m.txt") != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := p.Remove(context.Background(), "d"); !errors.Is(err, cloud.ErrUnsupported) || f.byID(d.id) == nil {
		t.Fatalf("folder: %v", err)
	}
	if err := p.Remove(context.Background(), "d/none"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestRetries(t *testing.T) {
	f := newFake(t)
	f.add("a.txt", "root", "text/plain", []byte("a"))
	p := connect(t, f, "", nil)
	ctx := context.Background()
	p.List(ctx, "")
	before := f.tokenN
	f.mu.Lock()
	f.token = "revoked"
	f.mu.Unlock()
	if _, err := p.List(ctx, ""); err != nil || f.tokenN != before+1 {
		t.Fatalf("401 refresh: %v refreshes=%d", err, f.tokenN-before)
	}
	if f.refresh[len(f.refresh)-1] != "rt2" {
		t.Fatalf("rotated token not used: %v", f.refresh)
	}
	f.rate403 = 2
	if _, err := p.List(ctx, ""); err != nil {
		t.Fatalf("rateLimitExceeded 403 should be retried: %v", err)
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
	f.add("a.txt", "root", "text/plain", []byte("a"))
	p := connect(t, f, "", nil)
	ctx := context.Background()
	var errs []error
	errs = append(errs, p.PutNew(ctx, "a.txt", strings.NewReader("b"), 1))
	_, err := p.Stat(ctx, "zzz")
	errs = append(errs, err)
	_, err = cloud.Open(ctx, "googledrive", cloud.Settings{
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

func TestQuoteEscaping(t *testing.T) {
	if got := qstr(`a'b\c`); got != `'a\'b\\c'` {
		t.Fatal(got)
	}
}
