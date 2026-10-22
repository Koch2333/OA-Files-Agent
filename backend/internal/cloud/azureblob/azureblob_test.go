package azureblob

// The Azure provider is verified against a hand-written fake that implements the small REST subset used here
// (List Blobs hierarchy/flat, Put Blob, Put Block + Put Block List, Get Blob, Get Blob Properties, Delete Blob). It
// is NOT the real service and does not validate SharedKey signatures; it only checks that credentials are sent.

import (
	"context"
	"encoding/base64"
	"encoding/xml"
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
)

type blobData struct {
	data []byte
	mod  time.Time
}

type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	blobs  map[string]*blobData
	blocks map[string][]byte // staged block id -> data (keyed by blob+id)
	// behaviour switches
	ignoreINM bool   // emulate a server that ignores If-None-Match
	sas       string // when set, requests must carry this sig query value instead of an Authorization header
	denyAuth  string // "auth" => AuthenticationFailed, "perm" => AuthorizationFailure
	hideHead  string // HEAD for this blob answers 404 (simulates a write racing the Stat pre-check)
	seenINM   []string
	seenAuth  []string
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, blobs: map[string]*blobData{}, blocks: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) fail(w http.ResponseWriter, status int, code string) {
	w.Header().Set("x-ms-error-code", code)
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r := w; r != nil {
		io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?><Error><Code>`+code+`</Code><Message>msg</Message></Error>`)
	}
}

func (f *fake) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenAuth = append(f.seenAuth, r.Header.Get("Authorization"))
	if f.sas != "" {
		if r.URL.Query().Get("sig") != f.sas || r.Header.Get("Authorization") != "" {
			f.fail(w, 403, "AuthenticationFailed")
			return
		}
	} else if !strings.HasPrefix(r.Header.Get("Authorization"), "SharedKey acct:") {
		f.fail(w, 403, "AuthenticationFailed")
		return
	}
	switch f.denyAuth {
	case "auth":
		f.fail(w, 403, "AuthenticationFailed")
		return
	case "perm":
		f.fail(w, 403, "AuthorizationFailure")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if parts[0] != "cont" {
		f.fail(w, 404, "ContainerNotFound")
		return
	}
	name := ""
	if len(parts) == 2 {
		name = parts[1]
	}
	q := r.URL.Query()
	switch {
	case r.Method == "GET" && q.Get("comp") == "list":
		f.list(w, q)
	case r.Method == "PUT" && q.Get("comp") == "block":
		b, _ := io.ReadAll(r.Body)
		f.blocks[name+"|"+q.Get("blockid")] = b
		w.WriteHeader(201)
	case r.Method == "PUT" && q.Get("comp") == "blocklist":
		f.seenINM = append(f.seenINM, r.Header.Get("If-None-Match"))
		if _, ok := f.blobs[name]; ok && !f.ignoreINM && r.Header.Get("If-None-Match") == "*" {
			f.fail(w, 409, "BlobAlreadyExists")
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var bl struct {
			Latest []string `xml:"Latest"`
		}
		xml.Unmarshal(raw, &bl)
		var all []byte
		for _, id := range bl.Latest {
			all = append(all, f.blocks[name+"|"+id]...)
		}
		f.blobs[name] = &blobData{data: all, mod: time.Now().UTC()}
		w.WriteHeader(201)
	case r.Method == "PUT":
		f.seenINM = append(f.seenINM, r.Header.Get("If-None-Match"))
		if _, ok := f.blobs[name]; ok && !f.ignoreINM && r.Header.Get("If-None-Match") == "*" {
			f.fail(w, 409, "BlobAlreadyExists")
			return
		}
		b, _ := io.ReadAll(r.Body)
		f.blobs[name] = &blobData{data: b, mod: time.Now().UTC()}
		w.WriteHeader(201)
	case r.Method == "HEAD" && name == f.hideHead:
		f.fail(w, 404, "BlobNotFound")
	case r.Method == "GET" || r.Method == "HEAD":
		b, ok := f.blobs[name]
		if !ok {
			f.fail(w, 404, "BlobNotFound")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b.data)))
		w.Header().Set("Last-Modified", b.mod.Format(http.TimeFormat))
		w.Header().Set("ETag", `"0x1"`)
		w.Header().Set("x-ms-blob-type", "BlockBlob")
		if r.Method == "GET" {
			w.Write(b.data)
		}
	case r.Method == "DELETE":
		if _, ok := f.blobs[name]; !ok {
			f.fail(w, 404, "BlobNotFound")
			return
		}
		delete(f.blobs, name)
		w.WriteHeader(202)
	default:
		f.fail(w, 400, "InvalidUri")
	}
}

func (f *fake) list(w http.ResponseWriter, q map[string][]string) {
	get := func(k string) string {
		if v := q[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	prefix, delim, marker := get("prefix"), get("delimiter"), get("marker")
	max := 5000
	if m := get("maxresults"); m != "" {
		max, _ = strconv.Atoi(m)
	}
	if max > 2 && f.t.Name() != "" { // small pages so pagination is always exercised
		max = 2
	}
	var names []string
	for n := range f.blobs {
		if strings.HasPrefix(n, prefix) {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	type item struct {
		name   string
		prefix bool
	}
	var items []item
	seen := map[string]bool{}
	for _, n := range names {
		rest := strings.TrimPrefix(n, prefix)
		if delim != "" {
			if i := strings.Index(rest, delim); i >= 0 {
				p := prefix + rest[:i+1]
				if !seen[p] {
					seen[p] = true
					items = append(items, item{p, true})
				}
				continue
			}
		}
		items = append(items, item{n, false})
	}
	start := 0
	if marker != "" {
		for i, it := range items {
			if it.name > marker {
				start = i
				break
			}
			start = i + 1
		}
	}
	end := start + max
	next := ""
	if end < len(items) {
		next = items[end-1].name
	} else {
		end = len(items)
	}
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><EnumerationResults ServiceEndpoint="x" ContainerName="cont"><Prefix>` + xmlEsc(prefix) + `</Prefix><Blobs>`)
	for _, it := range items[start:end] {
		if it.prefix {
			sb.WriteString(`<BlobPrefix><Name>` + xmlEsc(it.name) + `</Name></BlobPrefix>`)
			continue
		}
		b := f.blobs[it.name]
		sb.WriteString(fmt.Sprintf(`<Blob><Name>%s</Name><Properties><Last-Modified>%s</Last-Modified><Content-Length>%d</Content-Length><BlobType>BlockBlob</BlobType></Properties></Blob>`, xmlEsc(it.name), b.mod.Format(http.TimeFormat), len(b.data)))
	}
	sb.WriteString(`</Blobs><NextMarker>` + xmlEsc(next) + `</NextMarker></EnumerationResults>`)
	w.Header().Set("Content-Type", "application/xml")
	io.WriteString(w, sb.String())
}

func xmlEsc(s string) string {
	var sb strings.Builder
	xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

func (f *fake) connect(t *testing.T, base string, v map[string]string, sec map[string]string) cloud.Provider {
	t.Helper()
	vals := map[string]string{"account": "acct", "container": "cont", "endpoint": f.srv.URL}
	for k, x := range v {
		vals[k] = x
	}
	if sec == nil {
		sec = map[string]string{"account_key": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))}
	}
	p, err := New(context.Background(), cloud.Settings{Values: vals, Secrets: sec, BasePath: base})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func put(t *testing.T, p cloud.Provider, name, body string) {
	t.Helper()
	if err := p.PutNew(context.Background(), name, strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

func read(t *testing.T, p cloud.Provider, name string) string {
	t.Helper()
	rc, size, err := p.Open(context.Background(), name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil || int64(len(b)) != size {
		t.Fatalf("read %s: %v size=%d len=%d", name, err, size, len(b))
	}
	return string(b)
}

func TestRoundTripListStatRemove(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil, nil)
	ctx := context.Background()
	names := []string{"a.txt", "财务部/2026/合同 一.pdf", "财务部/readme.txt", "dir with space/x y#z.txt", "x/y/z/deep.bin"}
	for _, n := range names {
		put(t, p, n, "body of "+n)
	}
	for _, n := range names {
		if got := read(t, p, n); got != "body of "+n {
			t.Fatalf("%s: %q", n, got)
		}
		e, err := p.Stat(ctx, n)
		if err != nil || e.IsDir || e.Size != int64(len("body of "+n)) || e.Path != n {
			t.Fatalf("stat %s: %+v %v", n, e, err)
		}
	}
	for _, h := range f.seenINM {
		if h != "*" {
			t.Fatalf("every create must carry If-None-Match: * (saw %q)", f.seenINM)
		}
	}
	for _, a := range f.seenAuth {
		if !strings.HasPrefix(a, "SharedKey acct:") {
			t.Fatalf("auth header missing: %q", a)
		}
	}
	ents, err := p.List(ctx, "")
	if err != nil || len(ents) != 4 {
		t.Fatalf("root (paged by 2): %+v %v", ents, err)
	}
	cloud.SortEntries(ents)
	if !ents[0].IsDir || ents[0].Name != "dir with space" || ents[3].Name != "a.txt" {
		t.Fatalf("%+v", ents)
	}
	sub, err := p.List(ctx, "财务部")
	if err != nil || len(sub) != 2 {
		t.Fatalf("%+v %v", sub, err)
	}
	cloud.SortEntries(sub)
	if sub[0].Name != "2026" || !sub[0].IsDir || sub[0].Path != "财务部/2026" || sub[1].Path != "财务部/readme.txt" {
		t.Fatalf("%+v", sub)
	}
	if d, err := p.Stat(ctx, "x/y"); err != nil || !d.IsDir {
		t.Fatalf("prefix stat: %+v %v", d, err)
	}
	if d, err := p.Stat(ctx, ""); err != nil || !d.IsDir {
		t.Fatalf("root stat: %+v %v", d, err)
	}
	if _, err := p.Stat(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, _, err := p.Open(ctx, "nope.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if _, err := p.List(ctx, "nodir"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	if err := p.Remove(ctx, "a.txt"); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("%v", err)
	}
	put(t, p, "empty.txt", "")
	if e, err := p.Stat(ctx, "empty.txt"); err != nil || e.Size != 0 {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestMarkerBlobSkipped(t *testing.T) {
	f := newFake(t)
	f.blobs["marked/"] = &blobData{mod: time.Now()}
	f.blobs["marked/inner.txt"] = &blobData{data: []byte("i"), mod: time.Now()}
	p := f.connect(t, "", nil, nil)
	ents, err := p.List(context.Background(), "marked")
	if err != nil || len(ents) != 1 || ents[0].Name != "inner.txt" {
		t.Fatalf("%+v %v", ents, err)
	}
}

func TestPutNewNeverOverwrites(t *testing.T) {
	for _, ignore := range []bool{false, true} {
		f := newFake(t)
		f.ignoreINM = ignore
		p := f.connect(t, "", nil, nil)
		put(t, p, "dup/file.txt", "ORIGINAL")
		err := p.PutNew(context.Background(), "dup/file.txt", strings.NewReader("REPLACED!"), 9)
		if !errors.Is(err, cloud.ErrExists) || read(t, p, "dup/file.txt") != "ORIGINAL" {
			t.Fatalf("ignore=%v: %v", ignore, err)
		}
	}
	f := newFake(t)
	p := f.connect(t, "", nil, nil)
	f.blobs["race.txt"] = &blobData{data: []byte("theirs"), mod: time.Now()}
	// header-only path: the Stat pre-check is blinded, so only If-None-Match protects the blob
	f.hideHead = "race.txt"
	etagErr := p.PutNew(context.Background(), "race.txt", strings.NewReader("mine"), 4)
	if !errors.Is(etagErr, cloud.ErrExists) || string(f.blobs["race.txt"].data) != "theirs" {
		t.Fatalf("%v", etagErr)
	}
}

func TestMultiBlockUpload(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil, nil)
	big := strings.Repeat("0123456789abcdef", 600_000) // ~9.6 MB > one 4 MiB block
	put(t, p, "big.bin", big)
	if string(f.blobs["big.bin"].data) != big {
		t.Fatal("multi-block content differs")
	}
	if len(f.seenINM) == 0 || f.seenINM[len(f.seenINM)-1] != "*" {
		t.Fatalf("block list commit must be conditional: %q", f.seenINM)
	}
}

func TestSizeMismatch(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil, nil)
	if err := p.PutNew(context.Background(), "short.txt", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short source")
	}
	if _, ok := f.blobs["short.txt"]; ok {
		t.Fatal("truncated blob must be removed")
	}
	if err := p.PutNew(context.Background(), "long.txt", strings.NewReader("abcdef"), 3); err == nil {
		t.Fatal("long source")
	}
	if _, ok := f.blobs["long.txt"]; ok {
		t.Fatal("blob must be removed")
	}
}

func TestBasePathMapping(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "归档/2026 年", nil, nil)
	put(t, p, "文件 1.txt", "hi")
	put(t, p, "子目录/b.txt", "yo")
	if _, ok := f.blobs["归档/2026 年/子目录/b.txt"]; !ok {
		t.Fatalf("blob names: %v", f.blobs)
	}
	ents, err := p.List(context.Background(), "")
	if err != nil || len(ents) != 2 {
		t.Fatalf("%+v %v", ents, err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Path, "归档") {
			t.Fatalf("%+v", e)
		}
	}
	f.blobs["归档/2026 年x/o.txt"] = &blobData{mod: time.Now()}
	if ents, _ := p.List(context.Background(), ""); len(ents) != 2 {
		t.Fatalf("%+v", ents)
	}
	if _, err := p.Stat(context.Background(), "../x"); err == nil {
		t.Fatal("escape")
	}
}

func TestSASAuth(t *testing.T) {
	f := newFake(t)
	f.sas = "SIG123"
	for _, tok := range []string{"sv=2022&sig=SIG123", "?sv=2022&sig=SIG123"} {
		p := f.connect(t, "", map[string]string{"auth": "sas"}, map[string]string{"sas_token": tok})
		put(t, p, "s.txt", "x")
		if read(t, p, "s.txt") != "x" {
			t.Fatal("read")
		}
		f.blobs = map[string]*blobData{}
	}
	bad := f.connect(t, "", map[string]string{"auth": "sas"}, map[string]string{"sas_token": "sig=WRONG"})
	if _, err := bad.List(context.Background(), ""); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("%v", err)
	}
}

func TestErrorMapping(t *testing.T) {
	f := newFake(t)
	p := f.connect(t, "", nil, nil)
	f.denyAuth = "auth"
	if _, err := p.List(context.Background(), ""); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("%v", err)
	}
	if _, err := p.Stat(context.Background(), "a"); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("%v", err)
	}
	f.denyAuth = "perm"
	if err := p.PutNew(context.Background(), "a", strings.NewReader("x"), 1); !errors.Is(err, cloud.ErrPermission) {
		t.Fatalf("%v", err)
	}
	f.denyAuth = ""
	missing := f.connect(t, "", map[string]string{"container": "other"}, nil)
	if _, err := missing.List(context.Background(), ""); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("container missing: %v", err)
	}
	if r := cloud.Probe(context.Background(), p, true, ".oa-test-marker"); !r.OK || len(r.Steps) != 2 {
		t.Fatalf("%+v", r)
	}
	if len(f.blobs) != 0 {
		t.Fatalf("marker must be removed: %v", f.blobs)
	}
	f.denyAuth = "auth"
	if r := cloud.Probe(context.Background(), p, true, "m"); r.OK || !strings.Contains(r.Steps[0].Message, "认证失败") {
		t.Fatalf("%+v", r)
	}
}

func TestUnreachable(t *testing.T) {
	s := httptest.NewServer(http.NotFoundHandler())
	u := s.URL
	s.Close()
	f := &fake{}
	f.srv = &httptest.Server{URL: u}
	p := f.connect(t, "", nil, nil)
	if _, err := p.List(context.Background(), ""); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("%v", err)
	}
}

func TestValidateAndFactory(t *testing.T) {
	key := map[string]string{"account_key": "a2V5"}
	cases := []struct {
		name string
		v, s map[string]string
		ok   bool
	}{
		{"key", map[string]string{"account": "a", "container": "c"}, key, true},
		{"sas", map[string]string{"account": "a", "container": "c", "auth": "sas"}, map[string]string{"sas_token": "sig=x"}, true},
		{"endpoint", map[string]string{"account": "a", "container": "c", "endpoint": "https://a.blob.core.chinacloudapi.cn"}, key, true},
		{"no account", map[string]string{"container": "c"}, key, false},
		{"no container", map[string]string{"account": "a"}, key, false},
		{"no key", map[string]string{"account": "a", "container": "c"}, nil, false},
		{"sas without token", map[string]string{"account": "a", "container": "c", "auth": "sas"}, key, false},
		{"bad auth", map[string]string{"account": "a", "container": "c", "auth": "oauth"}, key, false},
	}
	for _, c := range cases {
		if _, _, err := cloud.Validate("azure_blob", c.v, c.s, nil); (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	bads := []map[string]string{
		{"account": "a", "container": "c", "endpoint": "ftp://x"},
		{"account": "a", "container": "c", "endpoint": "http://169.254.169.254"},
		{"account": "a/b", "container": "c"},
		{"account": "a", "container": "c?x"},
	}
	for _, v := range bads {
		if _, err := New(context.Background(), cloud.Settings{Values: v, Secrets: map[string]string{"account_key": "a2V5"}}); err == nil {
			t.Errorf("%v must be rejected", v)
		}
	}
	if _, err := New(context.Background(), cloud.Settings{Values: map[string]string{"account": "a", "container": "c"}, Secrets: map[string]string{"account_key": "not base64!"}}); err == nil {
		t.Error("bad key")
	}
	if k, ok := cloud.KindByID("azure_blob"); !ok || k.Group != "cloud" {
		t.Fatal("registration")
	}
}
