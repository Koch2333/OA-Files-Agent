package pan123

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/panhttp"
)

// The fake mirrors the 123pan open API as understood from OpenList/docs; NOT checked against the real service.

type fnode struct {
	id     int64
	parent int64
	name   string
	dir    bool
	data   []byte
	trash  int
}

type fake struct {
	mu         sync.Mutex
	srv        *httptest.Server
	nodes      []*fnode
	seq        int64
	token      string
	tokens     int
	expireAll  bool
	rate       int
	pend       map[string]*pending
	trashed    []int64
	leakAuth   bool
	completes  int
	secretSeen bool
}

type pending struct {
	parent int64
	name   string
	etag   string
	size   int64
	slices map[int][]byte
}

const secret = "SECRET-123"

func newFake(t *testing.T) *fake {
	f := &fake{pend: map[string]*pending{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/access_token", f.accessToken)
	mux.HandleFunc("/api/v2/file/list", f.auth(f.list))
	mux.HandleFunc("/api/v1/file/download_info", f.auth(f.dlInfo))
	mux.HandleFunc("/upload/v1/file/mkdir", f.auth(f.mkdir))
	mux.HandleFunc("/upload/v2/file/create", f.auth(f.create))
	mux.HandleFunc("/upload/v2/file/slice", f.auth(f.slice))
	mux.HandleFunc("/upload/v2/file/upload_complete", f.auth(f.complete))
	mux.HandleFunc("/api/v1/file/trash", f.auth(f.trash))
	mux.HandleFunc("/dl/", f.dl)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	a, p, b := apiBase, pollInterval, panhttp.BackoffBase
	apiBase, pollInterval, panhttp.BackoffBase = f.srv.URL, time.Millisecond, time.Millisecond
	t.Cleanup(func() { apiBase, pollInterval, panhttp.BackoffBase = a, p, b })
	return f
}

func reply(w http.ResponseWriter, code int, msg string, data any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"code": code, "message": msg, "data": data})
}

func (f *fake) accessToken(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	if in["clientSecret"] != secret || r.Header.Get("Platform") != "open_platform" {
		reply(w, 401, "clientSecret wrong "+in["clientSecret"], nil)
		return
	}
	f.tokens++
	f.token = "TOK-" + strconv.Itoa(f.tokens)
	reply(w, 0, "ok", map[string]string{"accessToken": f.token, "expiredAt": time.Now().Add(2 * time.Hour).Format(time.RFC3339)})
}

func (f *fake) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+f.token
		if f.expireAll {
			f.expireAll, ok = false, false
		}
		f.mu.Unlock()
		if !ok {
			reply(w, 401, "tokens invalid", nil)
			return
		}
		h(w, r)
	}
}

func (f *fake) add(parent int64, name string, dir bool, data []byte) *fnode {
	f.seq++
	n := &fnode{id: 100 + f.seq, parent: parent, name: name, dir: dir, data: data}
	f.nodes = append(f.nodes, n)
	return n
}

func (f *fake) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rate > 0 {
		f.rate--
		reply(w, 429, "too fast", nil)
		return
	}
	parent, _ := strconv.ParseInt(r.URL.Query().Get("parentFileId"), 10, 64)
	last, _ := strconv.ParseInt(r.URL.Query().Get("lastFileId"), 10, 64)
	var kids []*fnode
	for _, n := range f.nodes {
		if n.parent == parent && n.id > last {
			kids = append(kids, n)
		}
	}
	next := int64(-1)
	if len(kids) > 2 { // page size 2
		kids = kids[:2]
		next = kids[1].id
	}
	list := []map[string]any{}
	for _, n := range kids {
		t := 0
		if n.dir {
			t = 1
		}
		list = append(list, map[string]any{"fileId": n.id, "filename": n.name, "type": t, "size": len(n.data), "updateAt": "2024-01-02 03:04:05", "trashed": n.trash})
	}
	reply(w, 0, "ok", map[string]any{"lastFileId": next, "fileList": list})
}

func (f *fake) byID(id int64) *fnode {
	for _, n := range f.nodes {
		if n.id == id {
			return n
		}
	}
	return nil
}

func (f *fake) dlInfo(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, _ := strconv.ParseInt(r.URL.Query().Get("fileId"), 10, 64)
	if f.byID(id) == nil {
		reply(w, 1, "文件不存在", nil)
		return
	}
	reply(w, 0, "ok", map[string]string{"downloadUrl": f.srv.URL + "/dl/" + strconv.FormatInt(id, 10)})
}

func (f *fake) dl(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		f.leakAuth = true
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/dl/"), 10, 64)
	w.Write(f.byID(id).data)
}

func (f *fake) mkdir(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		ParentID int64  `json:"parentID"`
		Name     string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	for _, n := range f.nodes {
		if n.parent == in.ParentID && n.name == in.Name {
			reply(w, 1, "目录已存在", nil)
			return
		}
	}
	n := f.add(in.ParentID, in.Name, true, nil)
	reply(w, 0, "ok", map[string]any{"dirID": n.id})
}

func (f *fake) create(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		Parent int64  `json:"parentFileId"`
		Name   string `json:"filename"`
		Etag   string `json:"etag"`
		Size   int64  `json:"size"`
		Dup    *int   `json:"duplicate"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if in.Dup != nil {
		reply(w, 1, "duplicate must not be sent", nil)
		return
	}
	for _, n := range f.nodes {
		if n.parent == in.Parent && n.name == in.Name && n.trash == 0 {
			reply(w, 1, "文件名已存在", nil)
			return
		}
	}
	id := "pre" + strconv.Itoa(len(f.pend))
	f.pend[id] = &pending{parent: in.Parent, name: in.Name, etag: in.Etag, size: in.Size, slices: map[int][]byte{}}
	reply(w, 0, "ok", map[string]any{"fileID": 0, "preuploadID": id, "reuse": false, "sliceSize": 10, "servers": []string{f.srv.URL}})
}

func (f *fake) slice(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		reply(w, 2, "bad form", nil)
		return
	}
	file, _, _ := r.FormFile("slice")
	b, _ := io.ReadAll(file)
	sum := md5.Sum(b)
	if hex.EncodeToString(sum[:]) != r.FormValue("sliceMD5") {
		reply(w, 3, "md5", nil)
		return
	}
	no, _ := strconv.Atoi(r.FormValue("sliceNo"))
	f.mu.Lock()
	f.pend[r.FormValue("preuploadID")].slices[no] = b
	f.mu.Unlock()
	reply(w, 0, "ok", nil)
}

func (f *fake) complete(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		ID string `json:"preuploadID"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	f.completes++
	if f.completes%2 == 1 { // first poll: not completed yet
		reply(w, 0, "ok", map[string]any{"completed": false, "fileID": 0})
		return
	}
	pd := f.pend[in.ID]
	var data []byte
	for i := 1; i <= len(pd.slices); i++ {
		data = append(data, pd.slices[i]...)
	}
	sum := md5.Sum(data)
	if int64(len(data)) != pd.size || hex.EncodeToString(sum[:]) != pd.etag {
		reply(w, 4, "etag mismatch", nil)
		return
	}
	n := f.add(pd.parent, pd.name, false, data)
	reply(w, 0, "ok", map[string]any{"completed": true, "fileID": n.id})
}

func (f *fake) trash(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		IDs []int64 `json:"fileIDs"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	for _, id := range in.IDs {
		f.byID(id).trash = 1
		f.trashed = append(f.trashed, id)
	}
	reply(w, 0, "ok", nil)
}

func (f *fake) find(path string) *fnode {
	parent := int64(0)
	var cur *fnode
	for _, s := range strings.Split(path, "/") {
		cur = nil
		for _, n := range f.nodes {
			if n.parent == parent && n.name == s && n.trash == 0 {
				cur = n
				break
			}
		}
		if cur == nil {
			return nil
		}
		parent = cur.id
	}
	return cur
}

func settings(base string) cloud.Settings {
	return cloud.Settings{Values: map[string]string{"client_id": "cid"}, Secrets: map[string]string{"client_secret": secret}, BasePath: base}
}

func connect(t *testing.T, base string) cloud.Provider {
	t.Helper()
	p, err := New(context.Background(), settings(base))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestBadCredentialsNoLeak(t *testing.T) {
	newFake(t)
	s := settings("")
	s.Secrets["client_secret"] = "WRONG-SECRET"
	_, err := New(context.Background(), s)
	if !errors.Is(err, cloud.ErrAuth) || strings.Contains(err.Error(), "WRONG-SECRET") || strings.Contains(err.Error(), "clientSecret") {
		t.Fatalf("%v", err)
	}
}

func TestListPaginationStatOpen(t *testing.T) {
	f := newFake(t)
	base := f.add(0, "base", true, nil)
	for i := 0; i < 5; i++ {
		f.add(base.id, "f"+strconv.Itoa(i)+".txt", false, []byte(strings.Repeat("z", i)))
	}
	gone := f.add(base.id, "gone.txt", false, []byte("x"))
	gone.trash = 1
	sub := f.add(base.id, "sub", true, nil)
	f.add(sub.id, "d.txt", false, []byte("deep"))
	f.add(base.id, "dup.txt", false, []byte("first"))
	f.add(base.id, "dup.txt", false, []byte("second"))
	p := connect(t, "base")
	ctx := context.Background()
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 8 { // 5 + sub + 2 dup, trashed hidden
		t.Fatalf("%d %v", len(es), err)
	}
	e, err := p.Stat(ctx, "sub/d.txt")
	if err != nil || e.Size != 4 || e.Path != "sub/d.txt" || e.ModTime.IsZero() {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err := p.Stat(ctx, "gone.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "none"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	rc, size, err := p.Open(ctx, "dup.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "first" || size != 5 || f.leakAuth {
		t.Fatalf("%q %d leak=%v", b, size, f.leakAuth)
	}
}

func TestPutNewProtocol(t *testing.T) {
	f := newFake(t)
	p := connect(t, "root")
	ctx := context.Background()
	data := []byte("0123456789abcdefghijKLMNO") // 3 slices of 10
	if err := p.PutNew(ctx, "a/b/f.bin", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	n := f.find("root/a/b/f.bin")
	if n == nil || !bytes.Equal(n.data, data) || f.find("root/a/b") == nil || f.completes < 2 {
		t.Fatal("upload wrong")
	}
	// conflict -> ErrExists, original untouched, no new pending upload
	pend := len(f.pend)
	if err := p.PutNew(ctx, "a/b/f.bin", strings.NewReader("xxxxx"), 5); !errors.Is(err, cloud.ErrExists) || len(f.pend) != pend {
		t.Fatalf("%v", err)
	}
	// server-side name conflict mapped from its message
	pp := p.(*provider)
	err := pp.api(ctx, http.MethodPost, "/upload/v2/file/create", nil, map[string]any{"parentFileId": f.find("root/a/b").id, "filename": "f.bin", "etag": "x", "size": 1}, nil)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("%v", err)
	}
	if err := p.PutNew(ctx, "e.txt", strings.NewReader(""), 0); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := p.PutNew(ctx, "short.txt", strings.NewReader("ab"), 5); err == nil {
		t.Fatal("short body accepted")
	}
	if err := p.PutNew(ctx, "m.txt", strings.NewReader("hello"), 5); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "m.txt"); err != nil || len(f.trashed) != 1 {
		t.Fatalf("%v", err)
	}
	if err := p.Remove(ctx, "a"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "ghost"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestAuthExpiryRetryAndRate(t *testing.T) {
	f := newFake(t)
	f.add(0, "x.txt", false, []byte("1"))
	p := connect(t, "")
	f.mu.Lock()
	f.expireAll = true
	f.mu.Unlock()
	if es, err := p.List(context.Background(), ""); err != nil || len(es) != 1 || f.tokens != 2 {
		t.Fatalf("%v tokens=%d", err, f.tokens)
	}
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
		t.Fatal("should fail after 3 attempts")
	}
}

func TestTraversalAndUnreachable(t *testing.T) {
	f := newFake(t)
	p := connect(t, "")
	for _, bad := range []string{"../x", "a/../b", "/abs", `a\b`, "a//b"} {
		if _, err := p.Stat(context.Background(), bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
		if err := p.PutNew(context.Background(), bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	f.srv.Close()
	if _, err := New(context.Background(), settings("")); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatal(err)
	}
}

func TestKind(t *testing.T) {
	if _, ok := cloud.KindByID("123_open"); !ok {
		t.Fatal("missing")
	}
}
