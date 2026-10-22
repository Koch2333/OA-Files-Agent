package baidupan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"oa-agent/backend/internal/cloud/panhttp"
)

// The fake mirrors the xpan API as understood from the docs/OpenList; NOT checked against the real service.

type fnode struct {
	fsid int64
	dir  bool
	data []byte
}

type fake struct {
	mu        sync.Mutex
	srv       *httptest.Server
	nodes     map[string]*fnode // absolute path -> node ("/" implicit)
	seq       int64
	refresh   string
	access    string
	rotations int
	expireAll bool
	rate      int
	pending   map[string]map[int][]byte
	deleted   []string
	leakAuth  bool
	precreate int
}

const (
	secret = "SECRET-baidu"
	rt0    = "RT-0"
)

func newFake(t *testing.T) *fake {
	f := &fake{nodes: map[string]*fnode{}, refresh: rt0, access: "AT-1", pending: map[string]map[int][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth", f.oauth)
	mux.HandleFunc("/rest/2.0/xpan/file", f.xfile)
	mux.HandleFunc("/rest/2.0/xpan/multimedia", f.meta)
	mux.HandleFunc("/rest/2.0/pcs/superfile2", f.slice)
	mux.HandleFunc("/dl/", f.dl)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	o, a, c, b := oauthURL, apiBase, pcsBase, panhttp.BackoffBase
	oauthURL, apiBase, pcsBase, panhttp.BackoffBase = f.srv.URL+"/oauth", f.srv.URL+"/rest/2.0", f.srv.URL, time.Millisecond
	t.Cleanup(func() { oauthURL, apiBase, pcsBase, panhttp.BackoffBase = o, a, c, b })
	return f
}

func out(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func errno(w http.ResponseWriter, n int) { out(w, map[string]any{"errno": n}) }

func (f *fake) put(path string, dir bool, data []byte) *fnode {
	f.seq++
	n := &fnode{fsid: f.seq, dir: dir, data: data}
	f.nodes[path] = n
	return n
}

func (f *fake) oauth(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	if q.Get("client_secret") != secret || q.Get("refresh_token") != f.refresh {
		w.WriteHeader(400)
		out(w, map[string]string{"error": "invalid_grant", "error_description": "bad " + q.Get("refresh_token")})
		return
	}
	f.rotations++
	f.refresh = "RT-" + strconv.Itoa(f.rotations)
	f.access = "AT-" + strconv.Itoa(f.rotations+1)
	out(w, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": 2592000})
}

// authed checks the access token (errno -6 when wrong); callers hold no lock.
func (f *fake) authed(w http.ResponseWriter, tok string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := tok == f.access
	if f.expireAll {
		f.expireAll, ok = false, false
	}
	if !ok {
		errno(w, -6)
	}
	return ok
}

func (f *fake) xfile(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !f.authed(w, q.Get("access_token")) {
		return
	}
	r.ParseForm()
	f.mu.Lock()
	defer f.mu.Unlock()
	switch q.Get("method") {
	case "list":
		if f.rate > 0 {
			f.rate--
			errno(w, 31034)
			return
		}
		dir := strings.TrimRight(q.Get("dir"), "/")
		if n, ok := f.nodes[dir]; dir != "" && (!ok || !n.dir) {
			errno(w, -9)
			return
		}
		var names []string
		for p := range f.nodes {
			if i := strings.LastIndex(p, "/"); p[:i] == dir {
				names = append(names, p)
			}
		}
		sort.Strings(names)
		start, _ := strconv.Atoi(q.Get("start"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		if limit > 2 {
			limit = 2 // server-side page cap smaller than requested
		}
		items := []map[string]any{}
		for i := start; i < len(names) && i < start+limit; i++ {
			n := f.nodes[names[i]]
			d := 0
			if n.dir {
				d = 1
			}
			items = append(items, map[string]any{"fs_id": n.fsid, "path": names[i], "server_filename": names[i][strings.LastIndex(names[i], "/")+1:], "size": len(n.data), "isdir": d, "server_mtime": 1700000000})
		}
		out(w, map[string]any{"errno": 0, "list": items})
	case "precreate":
		f.precreate++
		p := r.PostForm.Get("path")
		if r.PostForm.Get("rtype") != "0" {
			errno(w, 2)
			return
		}
		if _, ok := f.nodes[p]; ok {
			out(w, map[string]any{"errno": 0, "return_type": 2})
			return
		}
		var bl []string
		json.Unmarshal([]byte(r.PostForm.Get("block_list")), &bl)
		id := "up" + strconv.Itoa(len(f.pending))
		f.pending[id] = map[int][]byte{}
		out(w, map[string]any{"errno": 0, "return_type": 1, "uploadid": id, "block_list": []int{}})
	case "create":
		p := r.PostForm.Get("path")
		if r.PostForm.Get("rtype") != "0" {
			errno(w, 2)
			return
		}
		if _, ok := f.nodes[p]; ok {
			errno(w, -8)
			return
		}
		if r.PostForm.Get("isdir") == "1" {
			f.put(p, true, nil)
			out(w, map[string]any{"errno": 0, "path": p})
			return
		}
		up := f.pending[r.PostForm.Get("uploadid")]
		var data []byte
		for i := 0; i < len(up); i++ {
			data = append(data, up[i]...)
		}
		if strconv.Itoa(len(data)) != r.PostForm.Get("size") {
			errno(w, 31365)
			return
		}
		f.put(p, false, data)
		out(w, map[string]any{"errno": 0, "path": p})
	case "filemanager":
		var l []string
		json.Unmarshal([]byte(r.PostForm.Get("filelist")), &l)
		for _, p := range l {
			delete(f.nodes, p)
			f.deleted = append(f.deleted, p)
		}
		out(w, map[string]any{"errno": 0})
	default:
		errno(w, 2)
	}
}

func (f *fake) meta(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !f.authed(w, q.Get("access_token")) {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for p, n := range f.nodes {
		if q.Get("fsids") == fmt.Sprintf("[%d]", n.fsid) {
			out(w, map[string]any{"errno": 0, "list": []map[string]string{{"dlink": f.srv.URL + "/dl/" + strings.TrimPrefix(p, "/") + "?x=1"}}})
			return
		}
	}
	errno(w, 31066)
}

func (f *fake) dl(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" || r.Header.Get("User-Agent") != "pan.baidu.com" || r.URL.Query().Get("access_token") != f.access {
		f.leakAuth = r.Header.Get("Authorization") != ""
		w.WriteHeader(403)
		return
	}
	w.Write(f.nodes["/"+strings.TrimPrefix(r.URL.Path, "/dl/")].data)
}

func (f *fake) slice(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !f.authed(w, q.Get("access_token")) {
		return
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errno(w, 2)
		return
	}
	file, _, _ := r.FormFile("file")
	b, _ := io.ReadAll(file)
	seq, _ := strconv.Atoi(q.Get("partseq"))
	f.mu.Lock()
	f.pending[q.Get("uploadid")][seq] = b
	f.mu.Unlock()
	out(w, map[string]any{"request_id": 1})
}

func settings(base string, persist func(k, v string)) cloud.Settings {
	return cloud.Settings{Values: map[string]string{"app_key": "ak"}, BasePath: base, Persist: persist,
		Secrets: map[string]string{"app_secret": secret, "refresh_token": rt0}}
}

func connect(t *testing.T, base string, persist func(k, v string)) cloud.Provider {
	t.Helper()
	p, err := New(context.Background(), settings(base, persist))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestRotationPersist(t *testing.T) {
	f := newFake(t)
	got := map[string]string{}
	connect(t, "", func(k, v string) { got[k] = v })
	if got["refresh_token"] != "RT-1" {
		t.Fatalf("persisted %v", got)
	}
	f2 := newFake(t)
	p := connect(t, "", nil) // nil Persist tolerated
	f2.mu.Lock()
	f2.expireAll = true
	f2.mu.Unlock()
	f2.put("/x", false, []byte("1"))
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if f2.rotations != 2 {
		t.Fatalf("rotations %d", f2.rotations)
	}
	_ = f
}

func TestBadCredentials(t *testing.T) {
	newFake(t)
	s := settings("", nil)
	s.Secrets["refresh_token"] = "WRONG-RT"
	_, err := New(context.Background(), s)
	if !errors.Is(err, cloud.ErrAuth) {
		t.Fatal(err)
	}
	for _, x := range []string{"WRONG-RT", secret, "invalid_grant"} {
		if strings.Contains(err.Error(), x) {
			t.Fatalf("leak %q in %v", x, err)
		}
	}
}

func TestListStatOpen(t *testing.T) {
	f := newFake(t)
	f.put("/apps", true, nil)
	f.put("/apps/me", true, nil)
	for i := 0; i < 5; i++ {
		f.put(fmt.Sprintf("/apps/me/f%d.txt", i), false, []byte(strings.Repeat("y", i+1)))
	}
	f.put("/apps/me/sub", true, nil)
	f.put("/apps/me/sub/d.txt", false, []byte("deep"))
	p := connect(t, "apps/me", nil)
	ctx := context.Background()
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 6 {
		t.Fatalf("%d %v", len(es), err)
	}
	e, err := p.Stat(ctx, "sub/d.txt")
	if err != nil || e.Size != 4 || e.Path != "sub/d.txt" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := p.Stat(ctx, "zzz"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "nodir"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	rc, size, err := p.Open(ctx, "sub/d.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "deep" || size != 4 || f.leakAuth {
		t.Fatalf("%q %d", b, size)
	}
}

func TestPutNewProtocol(t *testing.T) {
	f := newFake(t)
	f.put("/apps", true, nil)
	f.put("/apps/me", true, nil)
	p := connect(t, "apps/me", nil)
	ctx := context.Background()
	size := sliceSize*2 + 77
	data := bytes.Repeat([]byte("0123456789"), size/10+1)[:size]
	if err := p.PutNew(ctx, "a/b/big.bin", bytes.NewReader(data), int64(size)); err != nil {
		t.Fatal(err)
	}
	n := f.nodes["/apps/me/a/b/big.bin"]
	if n == nil || !bytes.Equal(n.data, data) || !f.nodes["/apps/me/a"].dir || !f.nodes["/apps/me/a/b"].dir {
		t.Fatal("upload/parents wrong")
	}
	// conflict: pre-check refuses without any precreate
	before := f.precreate
	if err := p.PutNew(ctx, "a/b/big.bin", strings.NewReader("zz"), 2); !errors.Is(err, cloud.ErrExists) || f.precreate != before {
		t.Fatalf("%v", err)
	}
	if !bytes.Equal(f.nodes["/apps/me/a/b/big.bin"].data, data) {
		t.Fatal("overwritten")
	}
	// server-side conflict (race): precreate answers return_type 2
	f.mu.Lock()
	f.put("/apps/me/a/race.txt", false, []byte("orig"))
	f.mu.Unlock()
	pp := p.(*provider)
	err := pp.api(ctx, "/xpan/file", map[string][]string{"method": {"precreate"}}, map[string][]string{"path": {"/apps/me/a/race.txt"}, "rtype": {"0"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// empty files are unsupported
	if err := p.PutNew(ctx, "e.txt", strings.NewReader(""), 0); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatal(err)
	}
	// remove
	if err := p.PutNew(ctx, "m.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "m.txt"); err != nil || len(f.deleted) != 1 || f.nodes["/apps/me/m.txt"] != nil {
		t.Fatalf("%v", err)
	}
	if err := p.Remove(ctx, "a"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "ghost"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFake(t)
	p := connect(t, "", nil)
	f.mu.Lock()
	f.rate = 2
	f.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rate = 9
	f.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err == nil {
		t.Fatal("expected failure after 3 attempts")
	}
}

func TestTraversalAndUnreachable(t *testing.T) {
	f := newFake(t)
	p := connect(t, "", nil)
	for _, bad := range []string{"../x", "a/../b", "/abs", `a\b`, "a//b"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if err := p.PutNew(context.Background(), bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	f.srv.Close()
	if _, err := New(context.Background(), settings("", nil)); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatal(err)
	}
}

func TestKind(t *testing.T) {
	if _, ok := cloud.KindByID("baidu_netdisk"); !ok {
		t.Fatal("missing")
	}
}
