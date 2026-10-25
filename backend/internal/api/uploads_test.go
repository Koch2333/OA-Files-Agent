package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/imports"
)

// rawDo sends a non-JSON request through the same stack as tclient.do (cookie, CSRF, remote address, headers).
func (c *tclient) rawDo(method, path string, body []byte, hdr map[string]string) resp {
	req := httptest.NewRequest(method, "http://"+c.host+path, bytes.NewReader(body))
	req.RemoteAddr = c.remote
	if c.cookie != "" {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: c.cookie})
	}
	if c.csrf != "" && method != "GET" && method != "HEAD" {
		req.Header.Set("X-CSRF-Token", c.csrf)
	}
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	c.env.h.ServeHTTP(rec, req)
	out := resp{Code: rec.Code, Header: rec.Header(), Raw: rec.Body.Bytes()}
	var env struct {
		Data  map[string]any `json:"data"`
		Error map[string]any `json:"error"`
	}
	_ = json.Unmarshal(out.Raw, &env)
	out.Data, out.Err = env.Data, env.Error
	return out
}

func newUploadEnv(t *testing.T, mode string) (*authEnv, *tclient, string, *imports.Service) {
	t.Helper()
	e := newAuthEnv(t)
	spool := t.TempDir()
	svc := &imports.Service{Store: e.st, Root: spool}
	srv := newServer(Deps{Store: e.st, Auth: e.svc, Backend: stubBackend{}, Imports: svc})
	e.srv, e.h = srv, http.HandlerFunc(srv.serve)
	var c *tclient
	if mode == "local" {
		e.svc.EnsureSetupToken(t.Context())
		c = e.client()
		c.remote = "127.0.0.1:4000"
		c.host = "localhost:8080"
		c.must("POST", "/v1/setup/complete", map[string]any{"mode": "local"}, 201)
	} else {
		c = e.setup(nil)
	}
	return e, c, spool, svc
}

func TestChunkedUploadOverHTTPLocalMode(t *testing.T) {
	_, c, _, _ := newUploadEnv(t, "local")
	data := bytes.Repeat([]byte("hello-world-"), 2000)
	h := sha256.Sum256(data)
	cfg := c.must("GET", "/v1/imports/config", nil, 200)
	if cfg.Data["max_upload_bytes"] != nil || cfg.Data["max_batch_files"] != nil || cfg.Data["local_path_import"] != true {
		t.Fatalf("config: %v", cfg.Data)
	}
	cr := c.must("POST", "/v1/imports/uploads", map[string]any{"name": "合同.pdf", "path": "法务/合同.pdf", "size": len(data), "sha256": hex.EncodeToString(h[:])}, 201)
	id, batch := cr.str("id"), cr.str("batch_id")
	put := func(off int, chunk []byte) resp {
		return c.rawDo("PUT", "/v1/imports/uploads/"+id+"?offset="+strconv.Itoa(off), chunk, map[string]string{"Content-Type": "application/offset+octet-stream"})
	}
	if r := put(0, data[:10000]); r.Code != 200 || r.Header.Get("Upload-Offset") != "10000" {
		t.Fatalf("chunk1: %d %s", r.Code, r.Raw)
	}
	if r := put(0, data[:10000]); r.Code != 200 || r.Header.Get("Upload-Offset") != "10000" {
		t.Fatalf("duplicate chunk: %d %s", r.Code, r.Raw)
	}
	r := put(20000, data[20000:])
	if r.Code != 409 || r.code() != "OFFSET_MISMATCH" || r.Header.Get("Upload-Offset") != "10000" {
		t.Fatalf("gap: %d %s", r.Code, r.Raw)
	}
	if d, _ := r.Err["details"].(map[string]any); d["offset"] != float64(10000) {
		t.Fatalf("details: %v", r.Err)
	}
	head := c.rawDo("HEAD", "/v1/imports/uploads/"+id, nil, nil)
	if head.Code != 200 || head.Header.Get("Upload-Offset") != "10000" || head.Header.Get("Upload-Length") != strconv.Itoa(len(data)) {
		t.Fatalf("HEAD: %d %v", head.Code, head.Header)
	}
	if r := put(10000, data[10000:]); r.Code != 200 {
		t.Fatalf("rest: %d %s", r.Code, r.Raw)
	}
	done := c.must("POST", "/v1/imports/uploads/"+id+"/complete", nil, 200)
	if done.Data["complete"] != true || done.str("sha256") != hex.EncodeToString(h[:]) {
		t.Fatalf("complete: %v", done.Data)
	}
	st := c.must("GET", "/v1/imports/batches/"+batch, nil, 200)
	if files, _ := st.Data["files"].([]any); len(files) != 1 {
		t.Fatalf("batch: %v", st.Data)
	}
	task := c.rawDo("POST", "/v1/imports/batches/"+batch+"/commit", nil, map[string]string{"Idempotency-Key": "k-1"})
	if task.Code != 202 || task.Data["id"] == nil || task.Data["total_files"] != float64(1) {
		t.Fatalf("commit: %d %s", task.Code, task.Raw)
	}
	again := c.rawDo("POST", "/v1/imports/batches/"+batch+"/commit", nil, map[string]string{"Idempotency-Key": "k-1"})
	if again.Code != 202 || again.Data["id"] != task.Data["id"] {
		t.Fatalf("replay: %d %s", again.Code, again.Raw)
	}
	// abort of an unknown session
	c.must("DELETE", "/v1/imports/uploads/"+batch+"_"+batch, nil, 404)
}

func TestDiskSpaceRefusalIs507(t *testing.T) {
	_, c, _, svc := newUploadEnv(t, "local")
	svc.Limits = imports.Limits{DiskReserve: 1 << 20, FreeSpace: func(string) (uint64, error) { return 5 << 20, nil }}
	r := c.do("POST", "/v1/imports/uploads", map[string]any{"name": "huge.pdf", "size": int64(50) << 30})
	if r.Code != 507 || r.code() != "INSUFFICIENT_STORAGE" || !strings.Contains(r.Err["message"].(string), "磁盘空间不足") {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
}

func TestMultipartHasNoSizeCapAndStreams(t *testing.T) {
	_, c, spool, _ := newUploadEnv(t, "local")
	_ = spool
	var buf bytes.Buffer
	boundary := "XBOUNDARYX"
	buf.WriteString("--" + boundary + "\r\nContent-Disposition: form-data; name=\"files\"; filename=\"big.pdf\"\r\nContent-Type: application/pdf\r\n\r\n")
	buf.Write(bytes.Repeat([]byte("z"), 30<<20)) // above the old 20 MiB cap
	buf.WriteString("\r\n--" + boundary + "--\r\n")
	r := c.rawDo("POST", "/v1/imports", buf.Bytes(), map[string]string{"Content-Type": "multipart/form-data; boundary=" + boundary})
	if r.Code != 202 || r.Data["total_files"] != float64(1) || r.Data["failure_count"] != float64(0) {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
}

func TestFromPathsIsLocalModeAndLoopbackOnly(t *testing.T) {
	_, c, spool, _ := newUploadEnv(t, "local")
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.pdf"), []byte("AAAA"), 0o644)
	body := map[string]any{"paths": []string{src}}
	// not from this machine
	remote := c.clone()
	remote.remote = "192.0.2.10:5000"
	assertCode(t, remote.do("POST", "/v1/imports/from-paths", body), 403, "FORBIDDEN") // local mode refuses non-loopback before the handler
	// looks local but came through a proxy
	proxied := c.clone()
	proxied.hdr["X-Forwarded-For"] = "203.0.113.9"
	if r := proxied.do("POST", "/v1/imports/from-paths", body); r.Code != 403 {
		t.Fatalf("proxied: %d %s", r.Code, r.Raw)
	}
	// the real thing
	r := c.do("POST", "/v1/imports/from-paths", body)
	if r.Code != 202 {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	batch := r.str("batch_id")
	var last map[string]any
	for i := 0; i < 400; i++ {
		g := c.must("GET", "/v1/imports/batches/"+batch, nil, 200)
		last, _ = g.Data["path_import"].(map[string]any)
		if last != nil && last["state"] == "done" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if last == nil || last["state"] != "done" || last["task_id"] == nil || last["copied"] != float64(1) {
		t.Fatalf("path import: %v", last)
	}
	if _, err := os.Stat(filepath.Join(src, "a.pdf")); err != nil {
		t.Fatal("source must remain")
	}
	// its own spool is off limits
	bad := c.do("POST", "/v1/imports/from-paths", map[string]any{"paths": []string{spool}})
	if bad.Code != 202 { // accepted, but the path fails per file
		t.Fatalf("%d %s", bad.Code, bad.Raw)
	}
}

func TestFromPathsRefusedInEnterpriseMode(t *testing.T) {
	_, admin, _, _ := newUploadEnv(t, "enterprise")
	admin.remote = "127.0.0.1:4000" // even from the machine itself
	r := admin.do("POST", "/v1/imports/from-paths", map[string]any{"paths": []string{t.TempDir()}})
	if r.Code != 403 || r.code() != "LOCAL_ONLY" {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	if cfg := admin.must("GET", "/v1/imports/config", nil, 200); cfg.Data["local_path_import"] != false {
		t.Fatalf("config must not advertise local-path import: %v", cfg.Data)
	}
}
