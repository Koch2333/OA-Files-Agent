package onedrive

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
	"net/url"
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

	mu          sync.Mutex
	files       map[string][]byte
	dirs        map[string]bool
	token       string // currently valid access token
	tokenN      int
	refreshSeen []string
	sessions    map[string]*session
	rateLimit   int // number of 429s to emit for the next list calls
	listCalls   int
}

type session struct {
	path string
	buf  []byte
	size int
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, files: map[string][]byte{}, dirs: map[string]bool{}, sessions: map[string]*session{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) invalidate() { f.mu.Lock(); f.token = "revoked"; f.mu.Unlock() }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func (f *fake) children(dir string) []map[string]any {
	var out []map[string]any
	var names []string
	prefix := dir
	if prefix != "" {
		prefix += "/"
	}
	seen := map[string]bool{}
	for p := range f.files {
		if strings.HasPrefix(p, prefix) && !strings.Contains(p[len(prefix):], "/") {
			names = append(names, p)
		}
	}
	for d := range f.dirs {
		if strings.HasPrefix(d, prefix) && !strings.Contains(d[len(prefix):], "/") {
			names = append(names, d)
		}
	}
	sort.Strings(names)
	for _, p := range names {
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, f.item(p))
	}
	return out
}

func (f *fake) item(p string) map[string]any {
	name := p[strings.LastIndex(p, "/")+1:]
	m := map[string]any{"id": "id-" + p, "name": name, "lastModifiedDateTime": "2024-05-06T07:08:09Z"}
	if f.dirs[p] {
		m["folder"] = map[string]any{"childCount": 0}
		m["size"] = 0
	} else {
		m["size"] = len(f.files[p])
	}
	return m
}

func (f *fake) exists(p string) bool { _, ok := f.files[p]; return ok || f.dirs[p] }

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := r.URL.Path
	switch {
	case path == "/tenant-x/oauth2/v2.0/token":
		r.ParseForm()
		f.refreshSeen = append(f.refreshSeen, r.PostForm.Get("refresh_token"))
		if r.PostForm.Get("client_id") != "cid" || r.PostForm.Get("client_secret") != secretVal || r.PostForm.Get("grant_type") != "refresh_token" ||
			(r.PostForm.Get("refresh_token") != "rt1" && r.PostForm.Get("refresh_token") != "rt2") {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "bad " + r.PostForm.Get("client_secret")})
			return
		}
		f.tokenN++
		f.token = "at" + strconv.Itoa(f.tokenN)
		out := map[string]any{"access_token": f.token, "expires_in": 3600}
		if r.PostForm.Get("refresh_token") == "rt1" {
			out["refresh_token"] = "rt2"
		}
		writeJSON(w, 200, out)
		return
	case strings.HasPrefix(path, "/upload/"):
		f.upload(w, r)
		return
	case strings.HasPrefix(path, "/dl/"):
		w.Write(f.files[strings.TrimPrefix(path, "/dl/")])
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		writeJSON(w, 401, map[string]any{"error": map[string]any{"code": "InvalidAuthenticationToken", "message": "token " + secretVal}})
		return
	}
	rest, ok := strings.CutPrefix(path, "/me/drive/root")
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, suffix := "", rest
	if strings.HasPrefix(rest, ":") {
		body := rest[1:]
		i := strings.LastIndex(body, ":")
		if i < 0 {
			i = len(body)
			body += ":"
		}
		p, suffix = strings.Trim(body[:i], "/"), body[i+1:]
	}
	switch {
	case r.Method == "GET" && suffix == "/children":
		f.listCalls++
		if f.rateLimit > 0 {
			f.rateLimit--
			w.Header().Set("Retry-After", "0")
			writeJSON(w, 429, map[string]any{"error": map[string]any{"code": "activityLimitReached"}})
			return
		}
		if p != "" && !f.dirs[p] {
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "itemNotFound"}})
			return
		}
		all := f.children(p)
		start, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		end := min(start+2, len(all)) // two per page
		res := map[string]any{"value": all[start:end]}
		if end < len(all) {
			q := url.Values{"skip": {strconv.Itoa(end)}}
			res["@odata.nextLink"] = f.srv.URL + path + "?" + q.Encode()
		}
		writeJSON(w, 200, res)
	case r.Method == "GET" && suffix == "":
		if p == "" {
			writeJSON(w, 200, map[string]any{"id": "root", "name": "root", "folder": map[string]any{}})
			return
		}
		if !f.exists(p) {
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "itemNotFound"}})
			return
		}
		writeJSON(w, 200, f.item(p))
	case r.Method == "GET" && suffix == "/content":
		if _, ok := f.files[p]; !ok {
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "itemNotFound"}})
			return
		}
		http.Redirect(w, r, f.srv.URL+"/dl/"+url.PathEscape(p), http.StatusFound)
	case r.Method == "PUT" && suffix == "/content":
		if r.URL.Query().Get("@microsoft.graph.conflictBehavior") != "fail" {
			f.t.Errorf("upload without conflictBehavior=fail")
		}
		if f.exists(p) {
			writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "nameAlreadyExists", "message": secretVal}})
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.files[p] = b
		writeJSON(w, 201, f.item(p))
	case r.Method == "POST" && suffix == "/children":
		var body struct {
			Name     string `json:"name"`
			Conflict string `json:"@microsoft.graph.conflictBehavior"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		np := body.Name
		if p != "" {
			np = p + "/" + body.Name
		}
		if body.Conflict != "fail" {
			f.t.Errorf("mkdir without conflictBehavior=fail")
		}
		if f.exists(np) {
			writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "nameAlreadyExists"}})
			return
		}
		f.dirs[np] = true
		writeJSON(w, 201, f.item(np))
	case r.Method == "POST" && suffix == "/createUploadSession":
		if f.exists(p) {
			writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "nameAlreadyExists"}})
			return
		}
		id := "s" + strconv.Itoa(len(f.sessions)+1)
		f.sessions[id] = &session{path: p}
		writeJSON(w, 200, map[string]any{"uploadUrl": f.srv.URL + "/upload/" + id})
	case r.Method == "DELETE" && suffix == "":
		if !f.exists(p) {
			writeJSON(w, 404, map[string]any{})
			return
		}
		delete(f.files, p)
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) upload(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("Authorization sent to the upload URL")
	}
	s := f.sessions[strings.TrimPrefix(r.URL.Path, "/upload/")]
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == "DELETE" {
		delete(f.sessions, strings.TrimPrefix(r.URL.Path, "/upload/"))
		w.WriteHeader(204)
		return
	}
	var a, b, total int
	fmt.Sscanf(r.Header.Get("Content-Range"), "bytes %d-%d/%d", &a, &b, &total)
	body, _ := io.ReadAll(r.Body)
	if a != len(s.buf) || len(body) != b-a+1 {
		w.WriteHeader(416)
		return
	}
	s.buf = append(s.buf, body...)
	if len(s.buf) == total {
		f.files[s.path] = s.buf
		writeJSON(w, 201, f.item(s.path))
		return
	}
	writeJSON(w, 202, map[string]any{"nextExpectedRanges": []string{fmt.Sprintf("%d-", len(s.buf))}})
}

func connect(t *testing.T, f *fake, base string, persist func(k, v string)) cloud.Provider {
	t.Helper()
	p, err := cloud.Open(context.Background(), "onedrive", cloud.Settings{
		Values:   map[string]string{"endpoint": f.srv.URL, "tenant": "tenant-x", "client_id": "cid"},
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
	k, ok := cloud.KindByID("onedrive")
	if !ok || k.Group != "cloud" {
		t.Fatal("kind missing")
	}
	for _, fl := range k.Fields {
		if (fl.Key == "client_secret" || fl.Key == "refresh_token") && !fl.Secret {
			t.Fatalf("%s must be secret", fl.Key)
		}
	}
	if _, _, err := cloud.Validate("onedrive", map[string]string{"client_id": "x"}, map[string]string{"client_secret": "s", "refresh_token": "r"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestTokenRefreshAndRotationPersisted(t *testing.T) {
	f := newFake(t)
	got := map[string]string{}
	p := connect(t, f, "", func(k, v string) { got[k] = v })
	if got["refresh_token"] != "rt2" {
		t.Fatalf("rotated token not persisted: %v", got)
	}
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if f.tokenN != 1 {
		t.Fatalf("token should be cached, fetched %d times", f.tokenN)
	}
	// nil Persist is tolerated
	f2 := newFake(t)
	connect(t, f2, "", nil)
}

func TestListPaginationStatOpen(t *testing.T) {
	f := newFake(t)
	f.dirs["Docs"] = true
	f.dirs["Docs/sub"] = true
	for i := 0; i < 5; i++ {
		f.files[fmt.Sprintf("Docs/f%d.txt", i)] = []byte(strings.Repeat("x", i))
	}
	f.files["Docs/名 称 #1.txt"] = []byte("hello")
	p := connect(t, f, "Docs", nil)
	ents, err := p.List(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 7 || !ents[0].IsDir || ents[0].Name != "sub" || ents[0].Path != "sub" {
		t.Fatalf("entries: %+v", ents)
	}
	if ents[1].ModTime.IsZero() {
		t.Fatal("mod time not parsed")
	}
	e, err := p.Stat(context.Background(), "f3.txt")
	if err != nil || e.Size != 3 || e.IsDir {
		t.Fatalf("stat: %+v %v", e, err)
	}
	if _, err := p.Stat(context.Background(), "nope.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	if _, err := p.List(context.Background(), "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
	rc, n, err := p.Open(context.Background(), "名 称 #1.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" || n != 5 {
		t.Fatalf("open: %q %d", b, n)
	}
}

func TestPutNewCreatesParentsAndNeverOverwrites(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	ctx := context.Background()
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("one"), 3); err != nil {
		t.Fatal(err)
	}
	if !f.dirs["a"] || !f.dirs["a/b"] || string(f.files["a/b/c.txt"]) != "one" {
		t.Fatalf("state: %v %v", f.dirs, f.files)
	}
	err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("two"), 3)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	if string(f.files["a/b/c.txt"]) != "one" {
		t.Fatal("existing file was modified")
	}
	if err := p.PutNew(ctx, "a/b", strings.NewReader("x"), 1); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("folder name conflict: %v", err)
	}
	if err := p.PutNew(ctx, "empty.txt", strings.NewReader(""), 0); err != nil || len(f.files["empty.txt"]) != 0 || !f.exists("empty.txt") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestPutNewLargeUsesSessionAndConflicts(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	data := make([]byte, 10<<20+12345)
	rand.Read(data)
	if err := p.PutNew(context.Background(), "big/x.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.files["big/x.bin"], data) {
		t.Fatal("content mismatch")
	}
	err := p.PutNew(context.Background(), "big/x.bin", bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	// source shorter than declared size: error and the session is cancelled
	err = p.PutNew(context.Background(), "big/short.bin", bytes.NewReader(data[:100]), int64(len(data)))
	if err == nil {
		t.Fatal("short source must fail")
	}
	if f.exists("big/short.bin") {
		t.Fatal("partial file created")
	}
}

func TestRemoveOnlyFiles(t *testing.T) {
	f := newFake(t)
	f.dirs["d"] = true
	f.files["d/m.txt"] = []byte("m")
	p := connect(t, f, "", nil)
	if err := p.Remove(context.Background(), "d/m.txt"); err != nil || f.exists("d/m.txt") {
		t.Fatalf("remove: %v", err)
	}
	if err := p.Remove(context.Background(), "d"); !errors.Is(err, cloud.ErrUnsupported) || !f.dirs["d"] {
		t.Fatalf("folder must not be removed: %v", err)
	}
	if err := p.Remove(context.Background(), "d/none"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("want not found: %v", err)
	}
}

func TestRetryAfter401RefreshesToken(t *testing.T) {
	f := newFake(t)
	f.files["a.txt"] = []byte("a")
	p := connect(t, f, "", nil)
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	before := f.tokenN
	f.invalidate()
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatalf("401 should trigger a refresh and retry: %v", err)
	}
	if f.tokenN != before+1 {
		t.Fatalf("expected one refresh, got %d", f.tokenN-before)
	}
	// the rotated refresh token (rt2) is used for later refreshes
	if f.refreshSeen[len(f.refreshSeen)-1] != "rt2" {
		t.Fatalf("refresh tokens used: %v", f.refreshSeen)
	}
}

func TestRateLimitRetried(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	f.rateLimit = 2
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatalf("429 should be retried: %v", err)
	}
	f.listCalls, f.rateLimit = 0, 10
	_, err := p.List(context.Background(), "")
	if err == nil || f.listCalls != 3 {
		t.Fatalf("want failure after 3 attempts, got err=%v calls=%d", err, f.listCalls)
	}
}

func TestTraversalRejected(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "base", nil)
	ctx := context.Background()
	for _, bad := range []string{"../x", "a/../../x", "/abs", "a//b", "a\\b", "./a"} {
		if _, err := p.Stat(ctx, bad); err == nil {
			t.Errorf("stat %q accepted", bad)
		}
		if err := p.PutNew(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("put %q accepted", bad)
		}
		if _, _, err := p.Open(ctx, bad); err == nil {
			t.Errorf("open %q accepted", bad)
		}
		if _, err := p.List(ctx, bad); err == nil {
			t.Errorf("list %q accepted", bad)
		}
	}
	if _, err := New(cloud.Settings{BasePath: "../up", Values: map[string]string{"client_id": "a"}, Secrets: map[string]string{"client_secret": "s", "refresh_token": "r"}}); err == nil {
		t.Error("base path escaping accepted")
	}
}

func TestErrorsNeverContainSecrets(t *testing.T) {
	f := newFake(t)
	f.files["a.txt"] = []byte("a")
	p := connect(t, f, "", nil)
	ctx := context.Background()
	var errs []error
	errs = append(errs, p.PutNew(ctx, "a.txt", strings.NewReader("b"), 1))
	_, err := p.Stat(ctx, "zzz")
	errs = append(errs, err)
	// wrong credentials
	_, err = cloud.Open(ctx, "onedrive", cloud.Settings{
		Values:  map[string]string{"endpoint": f.srv.URL, "tenant": "tenant-x", "client_id": "cid"},
		Secrets: map[string]string{"client_secret": secretVal, "refresh_token": "wrong-" + secretVal},
	})
	errs = append(errs, err)
	f.invalidate()
	f.mu.Lock()
	f.token = "never"
	f.mu.Unlock()
	for _, e := range errs {
		if e == nil {
			t.Fatal("expected error")
		}
		for _, s := range []string{secretVal, "rt1", "rt2", "at1"} {
			if strings.Contains(e.Error(), s) {
				t.Fatalf("error leaks %q: %v", s, e)
			}
		}
	}
	if !errors.Is(errs[2], cloud.ErrAuth) {
		t.Fatalf("bad refresh token should be ErrAuth, got %v", errs[2])
	}
}

func TestUnreachableAndContext(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	f.srv.Close()
	if _, err := p.List(context.Background(), ""); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.List(ctx, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestEndpointOverrideIgnoredForPublicHosts(t *testing.T) {
	p, err := New(cloud.Settings{
		Values:  map[string]string{"endpoint": "https://evil.example.com", "client_id": "a"},
		Secrets: map[string]string{"client_secret": "s", "refresh_token": "r"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.api != "https://graph.microsoft.com" {
		t.Fatalf("override honoured: %s", p.api)
	}
	p, _ = New(cloud.Settings{
		Values:  map[string]string{"region": "cn", "site_id": "contoso.sharepoint.com,aa,bb", "client_id": "a"},
		Secrets: map[string]string{"client_secret": "s", "refresh_token": "r"},
	})
	if p.api != "https://microsoftgraph.chinacloudapi.cn" || !strings.HasPrefix(p.drive, "/sites/contoso.sharepoint.com") {
		t.Fatalf("region/site: %s %s", p.api, p.drive)
	}
}
