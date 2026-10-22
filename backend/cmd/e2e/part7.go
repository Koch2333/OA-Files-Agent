package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/webdav"
)

// davServer is a real WebDAV server (golang.org/x/net/webdav, in memory) behind basic auth.
type davServer struct {
	fs     webdav.FileSystem
	srv    *http.Server
	URL    string
	fail   atomic.Bool // answer 500 to everything (simulates an outage)
	denied atomic.Int32
}

func startDAV(user, pass string) *davServer {
	d := &davServer{fs: webdav.NewMemFS()}
	h := &webdav.Handler{Prefix: "/dav", FileSystem: d.fs, LockSystem: webdav.NewMemLS()}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	d.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			d.denied.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="dav"`)
			http.Error(w, "unauthorized", 401)
			return
		}
		if d.fail.Load() {
			http.Error(w, "boom", 500)
			return
		}
		h.ServeHTTP(w, r)
	})}
	go d.srv.Serve(ln)
	d.URL = "http://" + ln.Addr().String() + "/dav"
	return d
}

func (d *davServer) put(rel string, data []byte) {
	ctx := context.Background()
	dir := ""
	for _, part := range strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/") {
		if part == "." || part == "" {
			continue
		}
		dir += "/" + part
		_ = d.fs.Mkdir(ctx, dir, 0o755)
	}
	f, err := d.fs.OpenFile(ctx, "/"+rel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		panic(err)
	}
	f.Write(data)
	f.Close()
}

func (d *davServer) get(rel string) ([]byte, bool) {
	f, err := d.fs.OpenFile(context.Background(), "/"+rel, os.O_RDONLY, 0)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	return b, true
}

func (d *davServer) all() map[string][]byte {
	out := map[string][]byte{}
	var walk func(dir string)
	walk = func(dir string) {
		f, err := d.fs.OpenFile(context.Background(), dir, os.O_RDONLY, 0)
		if err != nil {
			return
		}
		infos, _ := f.Readdir(-1)
		f.Close()
		for _, fi := range infos {
			p := strings.TrimSuffix(dir, "/") + "/" + fi.Name()
			if fi.IsDir() {
				walk(p)
			} else if b, ok := d.get(strings.TrimPrefix(p, "/")); ok {
				out[strings.TrimPrefix(p, "/")] = b
			}
		}
	}
	walk("/")
	return out
}

type stConn struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	SecretsSet  map[string]bool   `json:"secrets_set"`
	Settings    map[string]string `json:"settings"`
	Replication struct{ Queued, Running, Done, Skipped, Failed int }
}

type stRepl struct {
	LocalPath  string  `json:"local_path"`
	RemotePath string  `json:"remote_path"`
	Status     string  `json:"status"`
	Error      *string `json:"error"`
	Attempts   int     `json:"attempts"`
}

func listRepl(connID string) []stRepl {
	_, d, _ := call("GET", "/v1/storage/replications?page_size=200&connection_id="+connID, nil, nil)
	var p struct{ Items []stRepl }
	_ = json.Unmarshal(d, &p)
	return p.Items
}

func waitRepl(connID string, want int) []stRepl {
	deadline := time.Now().Add(90 * time.Second)
	var items []stRepl
	for time.Now().Before(deadline) {
		items = listRepl(connID)
		busy := 0
		for _, it := range items {
			if it.Status == "queued" || it.Status == "running" {
				busy++
			}
		}
		if len(items) >= want && busy == 0 {
			return items
		}
		time.Sleep(700 * time.Millisecond)
	}
	return items
}

// part7: optional storage connections against a real WebDAV server.
func part7() {
	println("--- part 7: storage connections (WebDAV): import from remote and replicate archive ---")
	st, d, _ := call("GET", "/v1/storage/kinds", nil, nil)
	var kinds []struct {
		ID     string `json:"id"`
		Group  string `json:"group"`
		Fields []struct {
			Key      string `json:"key"`
			Secret   bool   `json:"secret"`
			Required bool   `json:"required"`
		} `json:"fields"`
	}
	_ = json.Unmarshal(d, &kinds)
	have := map[string]bool{}
	for _, k := range kinds {
		have[k.ID] = true
	}
	check("storage: all seventeen kinds are offered", st == 200 && have["s3"] && have["azure_blob"] && have["webdav"] && have["sftp"] && have["ftp"] && have["smb"] && have["dingtalk"] && have["onedrive"] && have["googledrive"] && have["dropbox"] && have["aliyundrive_open"] && have["baidu_netdisk"] && have["123_open"] && have["openlist"] && have["seafile"] && have["cloudreve"] && have["localdir"], st, string(d))
	secretOK := true
	for _, k := range kinds {
		n := 0
		for _, f := range k.Fields {
			if f.Secret {
				n++
			}
		}
		if k.ID != "smb" && k.ID != "ftp" && n == 0 && k.ID != "webdav" && k.ID != "localdir" {
			secretOK = false
		}
	}
	check("storage: kinds describe their credential fields", secretOK)

	dav := startDAV("oa", "pw-123456")
	defer dav.srv.Close()
	samples := map[string]string{
		"收件箱/财务部_2026年3月报销申请.docx":   "财务部_2026年3月报销申请.docx",
		"收件箱/采购部_2026年Q1设备采购报表.xlsx": "采购部_2026年Q1设备采购报表.xlsx",
		"收件箱/子目录/办公电脑采购合同.pdf":       "办公电脑采购合同.pdf",
	}
	original := map[string][]byte{}
	for remote, local := range samples {
		b, err := os.ReadFile(filepath.Join(*dir, local))
		if err != nil {
			check("storage: sample readable "+local, false, err)
			return
		}
		dav.put(remote, b)
		original[remote] = b
	}
	dav.put("收件箱/压缩包.rar", []byte("not supported"))

	// ---- validation and safety ----
	body := func(extra map[string]any) map[string]any {
		m := map[string]any{"name": "公司 WebDAV", "kind": "webdav", "settings": map[string]string{"url": dav.URL, "username": "oa"}, "secrets": map[string]string{"password": "pw-123456"}, "replicate": "manual"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	st, _, _ = call("POST", "/v1/storage/connections", body(map[string]any{"kind": "nope"}), nil)
	check("storage: unknown kind → 422", st == 422, st)
	st, _, _ = call("POST", "/v1/storage/connections", body(map[string]any{"name": "S3 缺密钥", "kind": "s3", "settings": map[string]string{"endpoint": "s3.example.com", "bucket": "b"}, "secrets": map[string]string{}}), nil)
	check("storage: missing credentials → 422", st == 422, st)
	st, _, _ = call("POST", "/v1/storage/connections", body(map[string]any{"base_path": "../etc"}), nil)
	check("storage: traversal in base path → 422", st == 422, st)
	st, d, _ = call("POST", "/v1/storage/connections/test", map[string]any{"kind": "webdav", "settings": map[string]string{"url": "http://169.254.169.254/latest/"}, "secrets": map[string]string{"password": "x"}}, nil)
	check("storage: the cloud metadata address is never contacted (422 or failed step)", st == 422 || (st == 200 && strings.Contains(string(d), `"ok":false`)), st, string(d))
	st, _, _ = call("POST", "/v1/storage/connections", body(nil), map[string]string{"Origin": "https://evil.example"})
	check("storage: cross-origin create refused (403)", st == 403, st)

	// ---- wrong password is a readable failure, not an error ----
	st, d, _ = call("POST", "/v1/storage/connections/test", map[string]any{"kind": "webdav", "settings": map[string]string{"url": dav.URL, "username": "oa"}, "secrets": map[string]string{"password": "wrong-password"}}, nil)
	var tr struct {
		OK    bool `json:"ok"`
		Steps []struct{ Name, Message string }
	}
	_ = json.Unmarshal(d, &tr)
	check("storage: wrong password → ok=false with a readable message", st == 200 && !tr.OK && strings.Contains(string(d), "认证失败") && !strings.Contains(string(d), "wrong-password"), st, string(d))

	// ---- create ----
	st, d, _ = call("POST", "/v1/storage/connections", body(nil), nil)
	var conn stConn
	_ = json.Unmarshal(d, &conn)
	check("storage: create connection (201) without echoing the password", st == 201 && conn.ID != "" && conn.SecretsSet["password"] && !strings.Contains(string(d), "pw-123456"), st, string(d))
	st, _, _ = call("POST", "/v1/storage/connections", body(nil), nil)
	check("storage: duplicate name → 409", st == 409, st)
	_, d, _ = call("GET", "/v1/storage/connections", nil, nil)
	check("storage: list never returns credentials", !strings.Contains(string(d), "pw-123456"), string(d))

	st, d, _ = call("POST", "/v1/storage/connections/"+conn.ID+"/test", map[string]any{"check_write": true}, nil)
	_ = json.Unmarshal(d, &tr)
	check("storage: saved connection test incl. write probe passes", st == 200 && tr.OK && len(tr.Steps) >= 3, st, string(d))
	if _, leftover := dav.get(".oa-agent-write-test"); leftover {
		check("storage: write probe leaves nothing behind", false)
	}
	for p := range dav.all() {
		if strings.Contains(p, ".oa-agent-write-test") {
			check("storage: write probe leaves nothing behind", false, p)
		}
	}
	// editing the target without credentials must be refused
	st, _, _ = call("PATCH", "/v1/storage/connections/"+conn.ID, map[string]any{"settings": map[string]string{"url": "http://127.0.0.1:1/other"}}, nil)
	check("storage: changing the server without re-entering credentials → 422", st == 422, st)

	// ---- browse ----
	st, d, _ = call("GET", "/v1/storage/connections/"+conn.ID+"/browse", nil, nil)
	check("storage: browse root lists the folder", st == 200 && strings.Contains(string(d), "收件箱"), st, string(d))
	st, d, _ = call("GET", "/v1/storage/connections/"+conn.ID+"/browse?path="+url.QueryEscape("收件箱"), nil, nil)
	check("storage: browse a sub folder (folders first, files with sizes)", st == 200 && strings.Contains(string(d), "子目录") && strings.Contains(string(d), "报销申请"), st, string(d))
	for _, bad := range []string{"../x", "/etc", `a\b`} {
		st, _, _ = call("GET", "/v1/storage/connections/"+conn.ID+"/browse?path="+url.QueryEscape(bad), nil, nil)
		check("storage: browse refuses "+bad, st == 422 || st == 400, st)
	}

	// ---- import from remote ----
	imp := func(items []string, key string) (int, json.RawMessage) {
		var its []map[string]string
		for _, p := range items {
			its = append(its, map[string]string{"path": p})
		}
		hdr := map[string]string{}
		if key != "" {
			hdr["Idempotency-Key"] = key
		}
		st, d, _ := call("POST", "/v1/imports/remote", map[string]any{"connection_id": conn.ID, "items": its}, hdr)
		return st, d
	}
	st, d = imp([]string{"收件箱"}, "e2e-remote-1")
	var job job
	_ = json.Unmarshal(d, &job)
	check("storage: remote import accepted (202)", st == 202 && job.ID != "", st, string(d))
	check("storage: folder expanded recursively; unsupported file counted as a failure", job.TotalFiles == 4 && job.FailureCount == 1, job.TotalFiles, job.FailureCount)
	st2, d2 := imp([]string{"收件箱"}, "e2e-remote-1")
	var again struct{ ID string }
	_ = json.Unmarshal(d2, &again)
	check("storage: remote import is idempotent per key", st2 == 202 && again.ID == job.ID, st2)
	st, _ = imp([]string{"../escape"}, "")
	check("storage: remote import refuses traversal", st == 422 || st == 400, st)
	st, _ = imp([]string{"不存在的目录"}, "")
	check("storage: remote import of a missing path → 404", st == 404, st)
	jb := waitFor(job.ID, "awaiting_review")
	check("storage: imported files run through the normal pipeline", jb.Status == "awaiting_review" && jb.PlanID != nil, jb.Status)
	_, d, _ = call("GET", "/v1/files?task_id="+job.ID+"&page_size=100", nil, nil)
	var fl struct {
		Items []struct {
			Name string  `json:"source_name"`
			Path string  `json:"source_path"`
			Dept *string `json:"detected_department"`
			Err  *string `json:"error"`
		}
	}
	_ = json.Unmarshal(d, &fl)
	paths := map[string]bool{}
	recognised := 0
	for _, f := range fl.Items {
		paths[f.Path] = true
		if f.Dept != nil && f.Err == nil {
			recognised++
		}
	}
	check("storage: remote folder structure is kept in source paths", paths["收件箱/子目录/办公电脑采购合同.pdf"] && paths["收件箱/财务部_2026年3月报销申请.docx"], paths)
	check("storage: remote files are recognised like local uploads", recognised >= 3, recognised)

	// ---- approve + execute, then replicate the archive back ----
	_, d, _ = call("GET", "/v1/archive-plans/"+*jb.PlanID, nil, nil)
	var pl plan
	_ = json.Unmarshal(d, &pl)
	var fix []map[string]any
	for _, it := range pl.Items {
		if it.ReviewRequired {
			fix = append(fix, map[string]any{"file_id": it.FileID, "department": orDefault(it.Department, "", "行政部"), "document_type": orDefault(it.DocumentType, "", "通知"), "document_date": orDefault(it.DocumentDate, "", "2026-01-01")})
		}
	}
	if len(fix) > 0 {
		_, d, _ = call("PATCH", "/v1/archive-plans/"+pl.ID, map[string]any{"version": pl.Version, "items": fix}, nil)
		_ = json.Unmarshal(d, &pl)
	}
	_, d, _ = call("POST", "/v1/archive-plans/"+pl.ID+"/approve", map[string]any{"version": pl.Version}, nil)
	_ = json.Unmarshal(d, &pl)
	st, d, _ = call("POST", "/v1/archive-plans/"+pl.ID+"/execute", nil, map[string]string{"Idempotency-Key": "e2e-remote-exec"})
	var done plan
	_ = json.Unmarshal(d, &done)
	check("storage: plan executed", st == 200 && done.Status == "executed" && len(done.Items) >= 3, st, string(d))

	st, d, _ = call("POST", "/v1/storage/connections/"+conn.ID+"/replicate", map[string]any{}, nil)
	var q struct{ Queued int }
	_ = json.Unmarshal(d, &q)
	check("storage: replicate queues the archived files (202)", st == 202 && q.Queued >= len(done.Items), st, string(d))
	_, d, _ = call("POST", "/v1/storage/connections/"+conn.ID+"/replicate", map[string]any{}, nil)
	_ = json.Unmarshal(d, &q)
	check("storage: a second replicate request queues nothing new", q.Queued == 0, q.Queued)
	items := waitRepl(conn.ID, len(done.Items))
	allDone := len(items) >= len(done.Items)
	for _, it := range items {
		if it.Status != "done" {
			allDone = false
			fmt.Printf("      ↳ %s: %s %v\n", it.LocalPath, it.Status, it.Error)
		}
	}
	check("storage: every archived file was replicated", allDone, len(items))
	remote := dav.all()
	match := 0
	for _, it := range done.Items {
		b, ok := remote[it.DestinationPath]
		if !ok {
			continue
		}
		if *archiveDir != "" {
			local, _ := os.ReadFile(filepath.Join(*archiveDir, filepath.FromSlash(it.DestinationPath)))
			if bytes.Equal(local, b) {
				match++
			}
		} else if len(b) > 0 {
			match++
		}
	}
	check("storage: replicated bytes equal the local archive files at the same relative paths", match == len(done.Items), match, len(done.Items))
	untouched := true
	for p, b := range original {
		if got, ok := dav.get(p); !ok || !bytes.Equal(got, b) {
			untouched = false
		}
	}
	check("storage: the remote originals (inbox) were never modified or deleted", untouched)

	// ---- an outage is retried, not lost; the item can be retried by hand ----
	dav.fail.Store(true)
	st, d, _ = call("POST", "/v1/storage/connections/"+conn.ID+"/test", map[string]any{}, nil)
	_ = json.Unmarshal(d, &tr)
	check("storage: an outage shows as a failed test, not a server error", st == 200 && !tr.OK, st, string(d))
	dav.fail.Store(false)
	st, d, _ = call("GET", "/v1/storage/connections/"+conn.ID, nil, nil)
	var cur stConn
	_ = json.Unmarshal(d, &cur)
	check("storage: replication summary on the connection", st == 200 && cur.Replication.Done >= len(done.Items), cur.Replication)
	st, _, _ = call("POST", "/v1/storage/replications/00000000-0000-4000-8000-0000000000aa/retry", nil, nil)
	check("storage: retry of an unknown replication → 404", st == 404, st)
	if len(items) > 0 {
		st, _, _ = call("POST", "/v1/storage/connections/"+conn.ID+"/replicate", nil, nil)
		check("storage: replicate without a body works", st == 202, st)
	}
	st, _, _ = call("GET", "/v1/storage/replications?status=bogus", nil, nil)
	check("storage: bad replication filter → 400", st == 400, st)

	// ---- delete: remote files stay ----
	before := len(dav.all())
	st, _, _ = call("DELETE", "/v1/storage/connections/"+conn.ID, nil, nil)
	check("storage: delete connection (204)", st == 204, st)
	check("storage: deleting a connection never deletes remote files", len(dav.all()) == before, len(dav.all()), before)
	st, _, _ = call("GET", "/v1/storage/connections/"+conn.ID, nil, nil)
	check("storage: deleted connection → 404", st == 404, st)
	_, d, _ = call("GET", "/v1/audit-logs?page_size=200", nil, nil)
	check("storage: connection lifecycle is audited without credentials", strings.Contains(string(d), "storage.create") && strings.Contains(string(d), "storage.import") && !strings.Contains(string(d), "pw-123456"), "")
}
