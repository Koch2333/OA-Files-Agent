package dingtalk

import (
	"context"
	"crypto/tls"
	"crypto/x509"
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
)

// The fake below mirrors the DingTalk API as documented at help.dingtalk.io; it has NOT been checked against
// the real service.

const (
	tAppKey    = "key123"
	tAppSecret = "SECRET-xyz"
	tSpace     = "sp1"
	tUnion     = "union42"
)

type fnode struct {
	id, uuid, parent, name string
	dir                    bool
	data                   []byte
	mod                    string
}

type fake struct {
	t   *testing.T
	srv *httptest.Server
	mu  sync.Mutex

	nodes   map[string]*fnode
	nextID  int
	expire  int64
	tokenN  int
	revoked map[string]bool

	uploads                                  map[string][]byte
	problems                                 []string
	listCalls, tokenCalls, downloadInfoCalls int
	strategies                               []string
	rootQueryOK                              bool
	failCode                                 map[string][2]string // op -> {status, code}
	injectBeforeCommit                       func(f *fake)
	badFirstURL                              bool
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, nodes: map[string]*fnode{}, expire: 7200, revoked: map[string]bool{}, uploads: map[string][]byte{},
		rootQueryOK: true, failCode: map[string][2]string{}}
	f.nodes["0"] = &fnode{id: "0", uuid: "ROOTUUID", dir: true}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1.0/oauth2/accessToken", f.token)
	mux.HandleFunc("GET /v1.0/storage/spaces/{space}/dentries", f.list)
	mux.HandleFunc("POST /v1.0/storage/spaces/{space}/dentries/{id}/folders", f.mkdir)
	mux.HandleFunc("POST /v1.0/storage/spaces/{space}/dentries/{id}/downloadInfos/query", f.dlInfo)
	mux.HandleFunc("POST /v1.0/storage/spaces/{space}/dentries/{id}/query", f.info)
	mux.HandleFunc("DELETE /v1.0/storage/spaces/{space}/dentries/{id}", f.del)
	mux.HandleFunc("POST /v2.0/storage/spaces/files/{uuid}/uploadInfos/query", f.upInfo)
	mux.HandleFunc("POST /v2.0/storage/spaces/files/{uuid}/commit", f.commit)
	mux.HandleFunc("GET /oss/down/{id}", f.ossGet)
	mux.HandleFunc("PUT /oss/up/{key}", f.ossPut)
	mux.HandleFunc("GET /oss/bad", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 403) })
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) problem(s string, a ...any) {
	f.mu.Lock()
	f.problems = append(f.problems, fmt.Sprintf(s, a...))
	f.mu.Unlock()
}

func jerr(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"code": code, "message": "secret detail " + code, "requestid": "r"})
}

func jok(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// auth checks token + unionId + injected failures; false = response already written.
func (f *fake) auth(w http.ResponseWriter, r *http.Request, op string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := r.Header.Get("x-acs-dingtalk-access-token")
	if tok == "" || f.revoked[tok] || !strings.HasPrefix(tok, "tok-") {
		jerr(w, 401, "InvalidAuthentication")
		return false
	}
	if r.URL.Query().Get("unionId") != tUnion {
		f.problems = append(f.problems, "missing unionId on "+op)
		jerr(w, 400, "paramError.unionId")
		return false
	}
	if fc, ok := f.failCode[op]; ok {
		st, _ := strconv.Atoi(fc[0])
		jerr(w, st, fc[1])
		return false
	}
	return true
}

func (f *fake) token(w http.ResponseWriter, r *http.Request) {
	var b struct{ AppKey, AppSecret string }
	json.NewDecoder(r.Body).Decode(&b)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokenCalls++
	if b.AppKey != tAppKey || b.AppSecret != tAppSecret {
		jerr(w, 400, "invalidClientIdOrSecret")
		return
	}
	f.tokenN++
	jok(w, map[string]any{"accessToken": fmt.Sprintf("tok-%d", f.tokenN), "expireIn": f.expire})
}

func (f *fake) add(parent, name string, dir bool, data string) *fnode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.addLocked(parent, name, dir, data)
}

func (f *fake) addLocked(parent, name string, dir bool, data string) *fnode {
	f.nextID++
	n := &fnode{id: fmt.Sprintf("id%d", f.nextID), uuid: fmt.Sprintf("uu%d", f.nextID), parent: parent, name: name, dir: dir, data: []byte(data), mod: "2024-05-06T07:08:09Z"}
	f.nodes[n.id] = n
	return n
}

func dj(n *fnode) map[string]any {
	d := map[string]any{"id": n.id, "uuid": n.uuid, "name": n.name, "status": "NORMAL", "type": "FILE", "parentId": n.parent, "modifiedTime": n.mod}
	if n.dir {
		d["type"] = "FOLDER"
	} else {
		d["size"] = len(n.data)
	}
	return d
}

func (f *fake) children(parent string) []*fnode {
	var out []*fnode
	for _, n := range f.nodes {
		if n.parent == parent && n.id != "0" {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (f *fake) list(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "list") {
		return
	}
	q := r.URL.Query()
	if mr, _ := strconv.Atoi(q.Get("maxResults")); mr < 1 || mr > 50 {
		jerr(w, 400, "paramError.maxResults")
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCalls++
	if _, ok := f.nodes[q.Get("parentId")]; !ok {
		jerr(w, 404, "dentryNotExist")
		return
	}
	all := f.children(q.Get("parentId"))
	off, _ := strconv.Atoi(q.Get("nextToken"))
	end := off + 2 // tiny pages force pagination
	resp := map[string]any{}
	if end < len(all) {
		resp["nextToken"] = strconv.Itoa(end)
	} else {
		end = len(all)
	}
	items := []map[string]any{}
	for _, n := range all[off:end] {
		items = append(items, dj(n))
	}
	resp["dentries"] = items
	jok(w, resp)
}

func (f *fake) mkdir(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "create-folder") {
		return
	}
	var b struct {
		Name   string
		Option struct{ ConflictStrategy string }
	}
	json.NewDecoder(r.Body).Decode(&b)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.strategies = append(f.strategies, "mkdir:"+b.Option.ConflictStrategy)
	parent := r.PathValue("id")
	if _, ok := f.nodes[parent]; !ok {
		jerr(w, 404, "dentryNotExist")
		return
	}
	for _, c := range f.children(parent) {
		if c.name == b.Name {
			jerr(w, 400, "dentryAlreadyExist")
			return
		}
	}
	jok(w, map[string]any{"dentry": dj(f.addLocked(parent, b.Name, true, ""))})
}

func (f *fake) info(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "dentry-info") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.rootQueryOK {
		jerr(w, 404, "dentryNotExist")
		return
	}
	n, ok := f.nodes[r.PathValue("id")]
	if !ok {
		jerr(w, 404, "dentryNotExist")
		return
	}
	jok(w, map[string]any{"dentry": dj(n)})
}

func (f *fake) dlInfo(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "download-info") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downloadInfoCalls++
	n, ok := f.nodes[r.PathValue("id")]
	if !ok || n.dir {
		jerr(w, 404, "dentryNotExist")
		return
	}
	urls := []string{f.srv.URL + "/oss/down/" + n.id}
	if f.badFirstURL {
		urls = append([]string{f.srv.URL + "/oss/bad"}, urls...)
	}
	jok(w, map[string]any{"protocol": "HEADER_SIGNATURE", "headerSignatureInfo": map[string]any{
		"resourceUrls": urls, "headers": map[string]string{"x-oss-auth": "sig-" + n.id}, "expirationSeconds": 900}})
}

func (f *fake) ossGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Header.Get("x-oss-auth") != "sig-"+id {
		jerr(w, 403, "SignatureDoesNotMatch")
		return
	}
	f.mu.Lock()
	n := f.nodes[id]
	f.mu.Unlock()
	w.Write(n.data)
}

func (f *fake) upInfo(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "upload-info") {
		return
	}
	var b struct {
		Protocol string
		Option   struct {
			StorageDriver string
			PreCheckParam struct {
				Name string
				Size int64
			}
		}
	}
	json.NewDecoder(r.Body).Decode(&b)
	f.mu.Lock()
	defer f.mu.Unlock()
	if b.Protocol != "HEADER_SIGNATURE" || b.Option.PreCheckParam.Name == "" {
		jerr(w, 400, "paramError")
		return
	}
	key := "key-" + b.Option.PreCheckParam.Name + "-" + strconv.FormatInt(b.Option.PreCheckParam.Size, 10)
	jok(w, map[string]any{"uploadKey": key, "protocol": "HEADER_SIGNATURE", "headerSignatureInfo": map[string]any{
		"resourceUrls": []string{f.srv.URL + "/oss/up/" + url.PathEscape(key)}, "headers": map[string]string{"x-oss-put": "psig"}, "expirationSeconds": 900}})
}

func (f *fake) ossPut(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-oss-put") != "psig" {
		jerr(w, 403, "SignatureDoesNotMatch")
		return
	}
	if r.ContentLength < 0 {
		f.problem("PUT without Content-Length")
	}
	data, _ := io.ReadAll(r.Body)
	if int64(len(data)) != r.ContentLength {
		f.problem("PUT length mismatch")
	}
	f.mu.Lock()
	f.uploads[r.PathValue("key")] = data
	f.mu.Unlock()
}

func (f *fake) commit(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "commit") {
		return
	}
	var b struct {
		UploadKey, Name string
		Option          struct {
			Size             int64
			ConflictStrategy string
		}
	}
	json.NewDecoder(r.Body).Decode(&b)
	if f.injectBeforeCommit != nil {
		f.injectBeforeCommit(f)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.strategies = append(f.strategies, "commit:"+b.Option.ConflictStrategy)
	var parent *fnode
	for _, n := range f.nodes {
		if n.uuid == r.PathValue("uuid") {
			parent = n
		}
	}
	if parent == nil {
		jerr(w, 404, "dentryNotExist")
		return
	}
	data, ok := f.uploads[url.PathEscape(b.UploadKey)]
	if !ok {
		data, ok = f.uploads[b.UploadKey]
	}
	if !ok {
		jerr(w, 400, "paramError.uploadKey")
		return
	}
	if b.Option.Size != int64(len(data)) {
		jerr(w, 400, "paramError.size")
		return
	}
	for _, c := range f.children(parent.id) {
		if c.name == b.Name {
			if b.Option.ConflictStrategy == "RETURN_ERROR_IF_EXISTS" {
				jerr(w, 400, "dentryNameConflict")
				return
			}
			f.problems = append(f.problems, "unsafe strategy "+b.Option.ConflictStrategy)
		}
	}
	n := f.addLocked(parent.id, b.Name, false, string(data))
	jok(w, map[string]any{"dentry": dj(n)})
}

func (f *fake) del(w http.ResponseWriter, r *http.Request) {
	if !f.auth(w, r, "delete") {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Query().Get("toRecycleBin") != "false" {
		f.problems = append(f.problems, "toRecycleBin not false")
	}
	if _, ok := f.nodes[r.PathValue("id")]; !ok {
		jerr(w, 404, "dentryNotExist")
		return
	}
	delete(f.nodes, r.PathValue("id"))
	jok(w, map[string]any{"async": true, "taskId": "t1"})
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func (f *fake) provider(base string) (*Provider, *clock) {
	f.t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(f.srv.Certificate())
	clk := &clock{t: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
	p, err := newProvider(config{Endpoint: f.srv.URL, AppKey: tAppKey, AppSecret: tAppSecret, SpaceID: tSpace, UnionID: tUnion, Base: base},
		&tls.Config{RootCAs: pool}, clk.Now)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { p.Close() })
	return p, clk
}

func (f *fake) check() {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.problems) > 0 {
		f.t.Fatalf("fake API saw problems: %v", f.problems)
	}
}

var ctx = context.Background()

func TestRegisteredKind(t *testing.T) {
	k, ok := cloud.KindByID("dingtalk")
	if !ok || k.Group != "enterprise" || k.DocsURL != "https://open.dingtalk.com/" || k.Factory == nil {
		t.Fatalf("kind: %+v ok=%v", k, ok)
	}
	want := map[string]bool{"app_key": true, "space_id": true, "union_id": true, "endpoint": false, "app_secret": true}
	for _, f := range k.Fields {
		req, ok := want[f.Key]
		if !ok || f.Required != req || f.Help == "" || f.Label == "" {
			t.Errorf("field %+v", f)
		}
		delete(want, f.Key)
		if f.Key == "app_secret" && !f.Secret {
			t.Error("app_secret must be secret")
		}
		if f.Key == "endpoint" && f.Default != "https://api.dingtalk.com" {
			t.Error("endpoint default")
		}
	}
	if len(want) != 0 {
		t.Errorf("missing fields %v", want)
	}
}

func TestValidate(t *testing.T) {
	vals := map[string]string{"app_key": "k", "space_id": "s", "union_id": "u"}
	sec := map[string]string{"app_secret": "x"}
	if _, _, err := cloud.Validate("dingtalk", vals, sec, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cloud.Validate("dingtalk", vals, nil, map[string]bool{"app_secret": true}); err != nil {
		t.Fatalf("stored secret: %v", err)
	}
	if _, _, err := cloud.Validate("dingtalk", vals, nil, nil); err == nil {
		t.Error("missing secret accepted")
	}
	for _, drop := range []string{"app_key", "space_id", "union_id"} {
		v := map[string]string{}
		for k, x := range vals {
			if k != drop {
				v[k] = x
			}
		}
		if _, _, err := cloud.Validate("dingtalk", v, sec, nil); err == nil {
			t.Errorf("missing %s accepted", drop)
		}
	}
	if _, _, err := cloud.Validate("dingtalk", map[string]string{"app_key": "k", "space_id": "s", "union_id": "u", "bogus": "1"}, sec, nil); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, ep := range []string{"http://api.dingtalk.com", "https://169.254.169.254", "ftp://x", "https://u:p@x.com", "https://"} {
		if _, err := newProvider(config{Endpoint: ep, AppKey: "a", AppSecret: "b", SpaceID: "c", UnionID: "d"}, nil, time.Now); err == nil {
			t.Errorf("endpoint %q accepted", ep)
		}
	}
	if _, err := newProvider(config{AppKey: "a", AppSecret: "b", SpaceID: "c", UnionID: "d"}, nil, time.Now); err != nil {
		t.Errorf("default endpoint: %v", err)
	}
}

func TestFactoryBadCredentials(t *testing.T) {
	_, err := factory(ctx, cloud.Settings{Values: map[string]string{"space_id": "s", "union_id": "u"}, Secrets: map[string]string{"app_secret": "b"}})
	if err == nil {
		t.Fatal("expected error")
	}
	f := newFake(t)
	p, _ := f.provider("")
	p.cfg.AppSecret = "wrong"
	_, err = p.List(ctx, "")
	if !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
	if strings.Contains(err.Error(), "wrong") || strings.Contains(err.Error(), tAppSecret) {
		t.Errorf("secret leaked: %v", err)
	}
}

func seed(f *fake) {
	a := f.add("0", "a", true, "")
	f.add("0", "root1.txt", false, "hello")
	f.add("0", "root2.txt", false, "x")
	f.add("0", "z", true, "")
	b := f.add(a.id, "b", true, "")
	f.add(b.id, "deep.pdf", false, "PDFDATA")
}

func TestListStatNestedPagination(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("")
	es, err := p.List(ctx, "")
	if err != nil || len(es) != 4 {
		t.Fatalf("root: %v %v", es, err)
	}
	if !es[0].IsDir || es[0].Name != "a" || es[2].Name != "root1.txt" || es[2].Size != 5 || es[2].Path != "root1.txt" {
		t.Errorf("entries %+v", es)
	}
	if es[2].ModTime.IsZero() {
		t.Error("mod time not parsed")
	}
	es, err = p.List(ctx, "a/b")
	if err != nil || len(es) != 1 || es[0].Path != "a/b/deep.pdf" {
		t.Fatalf("nested: %v %v", es, err)
	}
	st, err := p.Stat(ctx, "a/b/deep.pdf")
	if err != nil || st.IsDir || st.Size != 7 {
		t.Fatalf("stat: %+v %v", st, err)
	}
	if _, err := p.Stat(ctx, "a/nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	if _, err := p.List(ctx, "root1.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("list file: %v", err)
	}
	if _, err := p.Stat(ctx, "../x"); err == nil {
		t.Error("path escape accepted")
	}
	root, err := p.Stat(ctx, "")
	if err != nil || !root.IsDir {
		t.Errorf("root stat %+v %v", root, err)
	}
	f.check()
	if p.APICalls() < 5 {
		t.Errorf("APICalls = %d", p.APICalls())
	}
}

func TestBasePath(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("a")
	es, err := p.List(ctx, "b")
	if err != nil || len(es) != 1 || es[0].Path != "b/deep.pdf" {
		t.Fatalf("%v %v", es, err)
	}
	rc, size, err := p.Open(ctx, "b/deep.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if string(b) != "PDFDATA" || size != 7 {
		t.Errorf("%q %d", b, size)
	}
}

func TestAmbiguity(t *testing.T) {
	f := newFake(t)
	seed(f)
	f.add("0", "dup", true, "")
	f.add("0", "dup", true, "")
	f.add("0", "dupfile", false, "1")
	f.add("0", "dupfile", false, "2")
	p, _ := f.provider("")
	if _, err := p.Stat(ctx, "dup"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("stat dir: %v", err)
	}
	if _, err := p.Stat(ctx, "dupfile"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("stat file: %v", err)
	}
	if _, err := p.List(ctx, ""); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("list: %v", err)
	}
	if _, err := p.Stat(ctx, "dup/x"); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("below: %v", err)
	}
	if err := p.PutNew(ctx, "dupfile", strings.NewReader("n"), 1); !errors.Is(err, ErrAmbiguous) {
		t.Errorf("put: %v", err)
	}
}

func TestListingCacheTTL(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, clk := f.provider("")
	p.List(ctx, "a")
	n := f.listCalls
	p.List(ctx, "a")
	p.Stat(ctx, "a")
	if f.listCalls != n {
		t.Fatalf("cache miss within TTL: %d -> %d", n, f.listCalls)
	}
	clk.Add(59 * time.Second)
	p.List(ctx, "a")
	if f.listCalls != n {
		t.Fatalf("expired early")
	}
	clk.Add(2 * time.Second)
	p.List(ctx, "a")
	if f.listCalls == n {
		t.Fatalf("cache did not expire after TTL")
	}
}

func TestOpenRetriesNextURLAndCounts(t *testing.T) {
	f := newFake(t)
	seed(f)
	f.badFirstURL = true
	p, _ := f.provider("")
	rc, size, err := p.Open(ctx, "root1.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" || size != 5 {
		t.Errorf("%q %d", b, size)
	}
	if p.DownloadInfoCalls() != 1 {
		t.Errorf("download-info calls %d", p.DownloadInfoCalls())
	}
	if _, _, err := p.Open(ctx, "a"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("open dir: %v", err)
	}
	if _, _, err := p.Open(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("open missing: %v", err)
	}
	f.check()
}

func TestPutNewCreatesFoldersAndNeverOverwrites(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("")
	if err := p.PutNew(ctx, "x/y/new.txt", strings.NewReader("content!"), 8); err != nil {
		t.Fatal(err)
	}
	rc, _, err := p.Open(ctx, "x/y/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "content!" {
		t.Errorf("stored %q", b)
	}
	// second write: ErrExists, content untouched
	if err := p.PutNew(ctx, "x/y/new.txt", strings.NewReader("OTHER"), 5); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	// existing seed file
	if err := p.PutNew(ctx, "root1.txt", strings.NewReader("zzzzz"), 5); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	rc, _, _ = p.Open(ctx, "root1.txt")
	b, _ = io.ReadAll(rc)
	rc.Close()
	if string(b) != "hello" {
		t.Errorf("overwritten: %q", b)
	}
	f.mu.Lock()
	for _, s := range f.strategies {
		if s != "mkdir:RETURN_ERROR_IF_EXISTS" && s != "commit:RETURN_ERROR_IF_EXISTS" {
			t.Errorf("strategy %q", s)
		}
	}
	f.mu.Unlock()
	f.check()
}

func TestPutNewRaceIsCaughtByCommit(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("")
	f.injectBeforeCommit = func(f *fake) { f.add("0", "race.txt", false, "winner") }
	err := p.PutNew(ctx, "race.txt", strings.NewReader("loser"), 5)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	f.injectBeforeCommit = nil
	rc, _, err := p.Open(ctx, "race.txt")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != "winner" {
		t.Errorf("content %q", b)
	}
	f.check()
}

func TestPutNewRootUUID(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider("")
	if err := p.PutNew(ctx, "r.txt", strings.NewReader("abc"), 3); err != nil {
		t.Fatal(err)
	}
	f2 := newFake(t)
	f2.rootQueryOK = false
	p2, _ := f2.provider("")
	err := p2.PutNew(ctx, "r.txt", strings.NewReader("abc"), 3)
	if !errors.Is(err, cloud.ErrUnsupported) {
		t.Fatalf("want ErrUnsupported, got %v", err)
	}
	// but nested folders work without the root uuid
	if err := p2.PutNew(ctx, "dir/r.txt", strings.NewReader("abc"), 3); err != nil {
		t.Fatal(err)
	}
	f.check()
	f2.check()
}

func TestPutNewRejectsBadNames(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider("")
	for _, n := range []string{"a*b.txt", "x.", "q<.txt", "d/e\".txt"} {
		if err := p.PutNew(ctx, n, strings.NewReader("a"), 1); !errors.Is(err, ErrBadRequest) {
			t.Errorf("%q: %v", n, err)
		}
	}
	if err := p.PutNew(ctx, "a*b/x.txt", strings.NewReader("a"), 1); !errors.Is(err, ErrBadRequest) {
		t.Errorf("folder name: %v", err)
	}
}

func TestRemove(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("")
	if err := p.Remove(ctx, "a"); !errors.Is(err, cloud.ErrUnsupported) {
		t.Errorf("folder remove: %v", err)
	}
	if err := p.Remove(ctx, "root1.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Stat(ctx, "root1.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("still there: %v", err)
	}
	if err := p.Remove(ctx, "root1.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("second remove: %v", err)
	}
	f.check()
}

func TestProbeWithFake(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, _ := f.provider("")
	rep := cloud.Probe(ctx, p, true, "oa-agent-test-1.txt")
	if !rep.OK || len(rep.Steps) != 2 {
		t.Fatalf("%+v", rep)
	}
	if _, err := p.Stat(ctx, "oa-agent-test-1.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Errorf("marker left behind: %v", err)
	}
}

func TestTokenCacheRefreshAndSingleFlight(t *testing.T) {
	f := newFake(t)
	seed(f)
	p, clk := f.provider("")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p.List(ctx, "") }()
	}
	wg.Wait()
	if f.tokenCalls != 1 {
		t.Fatalf("token requests = %d, want 1", f.tokenCalls)
	}
	clk.Add(7200*time.Second - 301*time.Second)
	p.List(ctx, "a")
	if f.tokenCalls != 1 {
		t.Fatalf("refreshed too early: %d", f.tokenCalls)
	}
	clk.Add(2 * time.Second) // inside the 5-minute margin
	p.List(ctx, "z")
	if f.tokenCalls != 2 {
		t.Fatalf("not refreshed before expiry: %d", f.tokenCalls)
	}
	// server-side revocation (401) triggers one transparent refresh
	f.mu.Lock()
	f.revoked["tok-2"] = true
	f.mu.Unlock()
	clk.Add(61 * time.Second)
	if _, err := p.List(ctx, "a/b"); err != nil {
		t.Fatalf("after revocation: %v", err)
	}
	if f.tokenCalls != 3 {
		t.Fatalf("token requests = %d", f.tokenCalls)
	}
	f.check() // includes: unionId present on every call
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		code   string
		want   error
	}{
		{403, "permissionDenied", cloud.ErrPermission},
		{403, "orgAuthLevelNotEnough", cloud.ErrPermission},
		{404, "spaceNotExist", cloud.ErrNotFound},
		{404, "dentryNotExist", cloud.ErrNotFound},
		{401, "InvalidAuthentication", cloud.ErrAuth},
		{400, "invalidClientIdOrSecret", cloud.ErrAuth},
		{429, "Throttling.Api", ErrQuota},
		{403, "Forbidden.QpsLimit", ErrQuota},
		{400, "spaceQuotaInsufficient", ErrCapacity},
		{400, "fileArchived", ErrRetryable},
		{503, "operationTimeout", ErrRetryable},
		{500, "systemError", ErrRetryable},
		{400, "paramError", ErrBadRequest},
		{400, "dentryNameConflict", cloud.ErrExists},
	}
	for _, c := range cases {
		f := newFake(t)
		seed(f)
		f.failCode["list"] = [2]string{strconv.Itoa(c.status), c.code}
		p, _ := f.provider("")
		_, err := p.List(ctx, "")
		if !errors.Is(err, c.want) {
			t.Errorf("%d %s: got %v want %v", c.status, c.code, err, c.want)
			continue
		}
		for _, s := range []string{tAppSecret, "tok-", "secret detail", f.srv.URL, "127.0.0.1"} {
			if strings.Contains(err.Error(), s) {
				t.Errorf("%s: error leaks %q: %v", c.code, s, err)
			}
		}
	}
	if h := Hint(ErrQuota); !strings.Contains(h, "10,000") {
		t.Errorf("quota hint: %q", h)
	}
	if Hint(fmt.Errorf("w: %w", ErrRetryable)) == "" || Hint(errors.New("x")) != "" {
		t.Error("hint behaviour")
	}
}

func TestUnreachableDoesNotLeak(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider("")
	f.srv.Close()
	_, err := p.List(ctx, "")
	if !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("address leaked: %v", err)
	}
}

func TestRedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request { r, _ := http.NewRequest("GET", raw, nil); return r }
	if checkRedirect(mk("https://a/x"), make([]*http.Request, 2)) != nil {
		t.Error("2 hops must be allowed")
	}
	if checkRedirect(mk("https://a/x"), make([]*http.Request, 3)) == nil {
		t.Error("3 hops must be refused")
	}
	if checkRedirect(mk("http://a/x"), make([]*http.Request, 1)) == nil {
		t.Error("http redirect must be refused")
	}
}

func TestAPIDoesNotFollowRedirects(t *testing.T) {
	f := newFake(t)
	p, _ := f.provider("")
	// a token endpoint answering 302 must surface as an error, not be followed
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://169.254.169.254/", 302)
	}))
	defer srv.Close()
	p.base = srv.URL
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	p.api.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: pool}
	if _, err := p.List(ctx, ""); err == nil {
		t.Fatal("redirect followed")
	}
}
