package aliyunpan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// The fake mirrors the Open API as understood from OpenList/official docs; NOT checked against the real service.

type node struct {
	id, parent, name string
	dir              bool
	data             []byte
	created          time.Time
}

type fake struct {
	mu        sync.Mutex
	srv       *httptest.Server
	nodes     map[string]*node
	seq       int
	refresh   string // currently valid refresh token
	rotations int
	access    string
	expireAll bool // next authenticated call answers AccessTokenExpired once
	rate      int  // number of 429 answers still to give on list
	uploads   map[string]map[int][]byte
	trashed   []string
	creates   int
	leakAuth  bool // an upload/download request carried Authorization
}

const (
	secret   = "SECRET-client"
	rtoken0  = "RT-0"
	driveRes = "drv-res"
)

func newFake(t *testing.T) *fake {
	f := &fake{nodes: map[string]*node{}, refresh: rtoken0, access: "AT-1", uploads: map[string]map[int][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/access_token", f.token)
	mux.HandleFunc("/adrive/v1.0/user/getDriveInfo", f.auth(f.driveInfo))
	mux.HandleFunc("/adrive/v1.0/openFile/list", f.auth(f.list))
	mux.HandleFunc("/adrive/v1.0/openFile/create", f.auth(f.create))
	mux.HandleFunc("/adrive/v1.0/openFile/complete", f.auth(f.complete))
	mux.HandleFunc("/adrive/v1.0/openFile/getDownloadUrl", f.auth(f.dlURL))
	mux.HandleFunc("/adrive/v1.0/openFile/recyclebin/trash", f.auth(f.trash))
	mux.HandleFunc("/up/", f.put)
	mux.HandleFunc("/dl/", f.get)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	old, oldB := apiBase, panhttp.BackoffBase
	apiBase, panhttp.BackoffBase = f.srv.URL, time.Millisecond
	t.Cleanup(func() { apiBase, panhttp.BackoffBase = old, oldB })
	return f
}

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func (f *fake) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in map[string]string
	json.NewDecoder(r.Body).Decode(&in)
	if in["client_secret"] != secret || in["refresh_token"] != f.refresh || in["grant_type"] != "refresh_token" {
		jsonOut(w, 400, map[string]string{"code": "InvalidParameter.RefreshToken", "message": "bad " + in["refresh_token"]})
		return
	}
	f.rotations++
	f.refresh = "RT-" + strconv.Itoa(f.rotations)
	f.access = "AT-" + strconv.Itoa(f.rotations+1)
	jsonOut(w, 200, map[string]any{"access_token": f.access, "refresh_token": f.refresh, "expires_in": 7200})
}

func (f *fake) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+f.access
		if f.expireAll {
			f.expireAll, ok = false, false
		}
		f.mu.Unlock()
		if !ok {
			jsonOut(w, 401, map[string]string{"code": "AccessTokenInvalid", "message": "x"})
			return
		}
		h(w, r)
	}
}

func (f *fake) driveInfo(w http.ResponseWriter, r *http.Request) {
	jsonOut(w, 200, map[string]string{"default_drive_id": "d", "resource_drive_id": driveRes, "backup_drive_id": "drv-bak"})
}

func (f *fake) add(parent, name string, dir bool, data []byte) *node {
	f.seq++
	n := &node{id: "id" + strconv.Itoa(f.seq), parent: parent, name: name, dir: dir, data: data, created: time.Unix(int64(1000+f.seq), 0)}
	f.nodes[n.id] = n
	return n
}

func (n *node) json() map[string]any {
	t := "file"
	if n.dir {
		t = "folder"
	}
	return map[string]any{"file_id": n.id, "name": n.name, "type": t, "size": len(n.data),
		"created_at": n.created.UTC().Format(time.RFC3339), "updated_at": n.created.UTC().Format(time.RFC3339)}
}

func (f *fake) list(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rate > 0 {
		f.rate--
		jsonOut(w, 429, map[string]string{"code": "TooManyRequests", "message": "slow"})
		return
	}
	var in struct {
		Drive, Parent, Marker string
	}
	var raw map[string]any
	json.NewDecoder(r.Body).Decode(&raw)
	in.Drive, _ = raw["drive_id"].(string)
	in.Parent, _ = raw["parent_file_id"].(string)
	in.Marker, _ = raw["marker"].(string)
	if in.Drive != driveRes {
		jsonOut(w, 403, map[string]string{"code": "ForbiddenDrive", "message": ""})
		return
	}
	var kids []*node
	for i := 1; i <= f.seq; i++ { // deterministic order
		if n, ok := f.nodes["id"+strconv.Itoa(i)]; ok && n.parent == in.Parent {
			kids = append(kids, n)
		}
	}
	start := 0
	if in.Marker != "" {
		start, _ = strconv.Atoi(in.Marker)
	}
	end := start + 2 // page size 2 regardless of limit
	next := ""
	if end < len(kids) {
		next = strconv.Itoa(end)
	} else {
		end = len(kids)
	}
	items := []map[string]any{}
	for _, n := range kids[start:end] {
		items = append(items, n.json())
	}
	jsonOut(w, 200, map[string]any{"items": items, "next_marker": next})
}

func (f *fake) create(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	var in struct {
		Parent string `json:"parent_file_id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Mode   string `json:"check_name_mode"`
		Size   int    `json:"size"`
		Parts  []struct {
			N int `json:"part_number"`
		} `json:"part_info_list"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	if in.Mode != "refuse" {
		jsonOut(w, 400, map[string]string{"code": "BadMode", "message": ""})
		return
	}
	for _, n := range f.nodes {
		if n.parent == in.Parent && n.name == in.Name {
			jsonOut(w, 409, map[string]string{"code": "AlreadyExist.File", "message": "exists"})
			return
		}
	}
	n := f.add(in.Parent, in.Name, in.Type == "folder", nil)
	if in.Type == "folder" {
		jsonOut(w, 200, n.json())
		return
	}
	up := "up" + n.id
	f.uploads[up] = map[int][]byte{}
	var parts []map[string]any
	for _, p := range in.Parts {
		parts = append(parts, map[string]any{"part_number": p.N, "upload_url": fmt.Sprintf("%s/up/%s/%d", f.srv.URL, up, p.N)})
	}
	jsonOut(w, 200, map[string]any{"file_id": n.id, "upload_id": up, "part_info_list": parts})
}

func (f *fake) put(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		f.leakAuth = true
	}
	p := strings.Split(strings.TrimPrefix(r.URL.Path, "/up/"), "/")
	n, _ := strconv.Atoi(p[1])
	b, _ := io.ReadAll(r.Body)
	f.uploads[p[0]][n] = b
	w.WriteHeader(200)
}

func (f *fake) complete(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		FileID   string `json:"file_id"`
		UploadID string `json:"upload_id"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	var data []byte
	for i := 1; i <= len(f.uploads[in.UploadID]); i++ {
		data = append(data, f.uploads[in.UploadID][i]...)
	}
	f.nodes[in.FileID].data = data
	jsonOut(w, 200, f.nodes[in.FileID].json())
}

func (f *fake) dlURL(w http.ResponseWriter, r *http.Request) {
	var in struct {
		FileID string `json:"file_id"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	jsonOut(w, 200, map[string]string{"url": f.srv.URL + "/dl/" + in.FileID})
}

func (f *fake) get(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		f.leakAuth = true
	}
	n := f.nodes[strings.TrimPrefix(r.URL.Path, "/dl/")]
	w.Write(n.data)
}

func (f *fake) trash(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var in struct {
		FileID string `json:"file_id"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	delete(f.nodes, in.FileID)
	f.trashed = append(f.trashed, in.FileID)
	jsonOut(w, 200, map[string]any{})
}

func (f *fake) find(path string) *node {
	parent := "root"
	var cur *node
	for _, s := range strings.Split(path, "/") {
		cur = nil
		for i := 1; i <= f.seq; i++ {
			if n, ok := f.nodes["id"+strconv.Itoa(i)]; ok && n.parent == parent && n.name == s {
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

func settings(base string, persist func(k, v string)) cloud.Settings {
	return cloud.Settings{
		Values:   map[string]string{"client_id": "cid", "drive_type": "resource"},
		Secrets:  map[string]string{"client_secret": secret, "refresh_token": rtoken0},
		BasePath: base, Persist: persist,
	}
}

func connect(t *testing.T, f *fake, base string, persist func(k, v string)) cloud.Provider {
	t.Helper()
	p, err := New(context.Background(), settings(base, persist))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestRotationPersistAndNilPersist(t *testing.T) {
	f := newFake(t)
	got := map[string]string{}
	connect(t, f, "", func(k, v string) { got[k] = v })
	if got["refresh_token"] != "RT-1" || f.rotations != 1 {
		t.Fatalf("persisted %v rotations %d", got, f.rotations)
	}
	// nil Persist still works; a later refresh uses the rotated token held in memory.
	f2 := newFake(t)
	p := connect(t, f2, "", nil)
	f2.mu.Lock()
	f2.expireAll = true
	f2.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if f2.rotations != 2 {
		t.Fatalf("rotations %d, want 2 (refresh on auth failure with rotated token)", f2.rotations)
	}
}

func TestBadCredentialsAreErrAuthWithoutSecrets(t *testing.T) {
	f := newFake(t)
	s := settings("", nil)
	s.Secrets["refresh_token"] = "WRONG-REFRESH"
	_, err := New(context.Background(), s)
	if !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	for _, bad := range []string{"WRONG-REFRESH", secret, "bad "} {
		if strings.Contains(err.Error(), bad) {
			t.Fatalf("error leaks %q: %v", bad, err)
		}
	}
	_ = f
}

func TestListPaginationStatOpen(t *testing.T) {
	f := newFake(t)
	f.mu.Lock()
	d := f.add("root", "base", true, nil)
	for i := 0; i < 5; i++ {
		f.add(d.id, fmt.Sprintf("f%d.txt", i), false, []byte(strings.Repeat("x", i)))
	}
	sub := f.add(d.id, "sub", true, nil)
	f.add(sub.id, "deep.txt", false, []byte("deep"))
	// duplicate name: the oldest wins deterministically
	f.add(d.id, "dup.txt", false, []byte("first"))
	f.add(d.id, "dup.txt", false, []byte("second"))
	f.mu.Unlock()
	p := connect(t, f, "base", nil)
	ctx := context.Background()
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 8 {
		t.Fatalf("list %v %v", len(es), err)
	}
	e, err := p.Stat(ctx, "sub/deep.txt")
	if err != nil || e.Size != 4 || e.IsDir || e.Path != "sub/deep.txt" {
		t.Fatalf("stat %+v %v", e, err)
	}
	if e, _ := p.Stat(ctx, "sub"); !e.IsDir {
		t.Fatal("sub should be dir")
	}
	if _, err := p.Stat(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := p.List(ctx, "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	rc, size, err := p.Open(ctx, "dup.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "first" || size != 5 {
		t.Fatalf("open %q %d", b, size)
	}
	if f.leakAuth {
		t.Fatal("download URL received Authorization")
	}
	if _, _, err := p.Open(ctx, "sub"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestPutNewMultipartParentsConflictRemove(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "root-dir", nil)
	ctx := context.Background()
	// Shrink nothing: force several parts by using a size above minPart.
	size := minPart*2 + 123
	data := bytes.Repeat([]byte("abcdefghij"), size/10+1)[:size]
	if err := p.PutNew(ctx, "a/b/big.bin", bytes.NewReader(data), int64(size)); err != nil {
		t.Fatal(err)
	}
	n := f.find("root-dir/a/b/big.bin")
	if n == nil || !bytes.Equal(n.data, data) {
		t.Fatal("content mismatch")
	}
	if len(f.uploads["up"+n.id]) != 3 {
		t.Fatalf("parts %d", len(f.uploads["up"+n.id]))
	}
	if f.leakAuth {
		t.Fatal("part upload received Authorization")
	}
	// Existing target: ErrExists, and the original stays untouched.
	before := f.creates
	err := p.PutNew(ctx, "a/b/big.bin", strings.NewReader("zzz"), 3)
	if !errors.Is(err, cloud.ErrExists) || f.creates != before || !bytes.Equal(f.find("root-dir/a/b/big.bin").data, data) {
		t.Fatalf("conflict handling: %v", err)
	}
	// Race: exists on the server only after our check -> server refuse maps to ErrExists.
	f.mu.Lock()
	dirID := f.find("root-dir/a").id
	f.mu.Unlock()
	_ = dirID
	// small file + Remove
	if err := p.PutNew(ctx, "a/marker.txt", strings.NewReader("hi"), 2); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a/marker.txt"); err != nil || len(f.trashed) != 1 {
		t.Fatalf("remove %v %v", err, f.trashed)
	}
	if err := p.Remove(ctx, "a/b"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatalf("folder remove must be refused: %v", err)
	}
	if err := p.Remove(ctx, "a/ghost"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatal(err)
	}
	// short body
	if err := p.PutNew(ctx, "a/short.txt", strings.NewReader("ab"), 5); err == nil {
		t.Fatal("short body should fail")
	}
}

func TestServerRefuseMapsToErrExists(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil).(*provider)
	// Simulate the race: the listing in PutNew sees nothing, create then reports AlreadyExist.
	f.mu.Lock()
	f.add("root", "x.txt", false, []byte("orig"))
	f.mu.Unlock()
	err := p.call(context.Background(), "/adrive/v1.0/openFile/create", map[string]any{
		"drive_id": driveRes, "parent_file_id": "root", "name": "x.txt", "type": "file", "check_name_mode": "refuse"}, nil)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatal(err)
	}
}

func TestRateLimitRetries(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	f.mu.Lock()
	f.rate = 2
	f.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.rate = 10
	f.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err == nil {
		t.Fatal("should give up after 3 attempts")
	}
}

func TestTraversalRejected(t *testing.T) {
	f := newFake(t)
	p := connect(t, f, "", nil)
	ctx := context.Background()
	for _, bad := range []string{"../x", "a/../b", "/abs", `a\b`, "a//b", "./a"} {
		if _, err := p.Stat(ctx, bad); err == nil {
			t.Fatalf("Stat(%q) accepted", bad)
		}
		if err := p.PutNew(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Fatalf("PutNew(%q) accepted", bad)
		}
		if _, _, err := p.Open(ctx, bad); err == nil {
			t.Fatalf("Open(%q) accepted", bad)
		}
	}
}

func TestUnreachable(t *testing.T) {
	f := newFake(t)
	f.srv.Close()
	_, err := New(context.Background(), settings("", nil))
	if !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("want ErrUnreachable, got %v", err)
	}
}

func TestKindRegistered(t *testing.T) {
	k, ok := cloud.KindByID("aliyundrive_open")
	if !ok || k.Group != "cloud" {
		t.Fatal("kind missing")
	}
	_, _, err := cloud.Validate("aliyundrive_open", map[string]string{"client_id": "a", "drive_type": "backup"},
		map[string]string{"client_secret": "s", "refresh_token": "r"}, nil)
	if err != nil {
		t.Fatal(err)
	}
}
