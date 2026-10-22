package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/llm"
)

type diagEnv struct {
	*authEnv
	admin, operator, viewer *tclient
	rep                     *diag.Reporter
	logDir                  string
}

func newDiagEnv(t *testing.T) *diagEnv {
	t.Helper()
	e := newAuthEnv(t)
	admin := e.setup(nil)
	d := &diagEnv{authEnv: e, admin: admin, operator: e.user(admin, "olga", "operator"), viewer: e.user(admin, "vera", "viewer")}
	d.rep = diag.New(diag.Options{FlushEvery: 20 * time.Millisecond, RatePerSec: 100000, Logf: func(string, ...any) {}})
	d.rep.Attach(e.st.Pool)
	old := diag.SetDefault(d.rep)
	t.Cleanup(func() { diag.SetDefault(old); d.rep.Close() })
	d.logDir = t.TempDir()
	e.srv.d.Checks = []DependencyCheck{func(context.Context) string { return "ok" }, func(context.Context) string { return "ok" }}
	e.srv.d.Diag = DiagInfo{StartedAt: time.Now().Add(-90 * time.Second), Standalone: true, ArchiveRoot: t.TempDir(), LogDir: d.logDir}
	return d
}

func (d *diagEnv) flush() { d.rep.Flush(5 * time.Second) }

func (d *diagEnv) seed(e diag.Entry) string {
	id := d.rep.Report(context.Background(), e)
	d.flush()
	return id
}

func TestDiagAuthzMatrix(t *testing.T) {
	d := newDiagEnv(t)
	id := d.seed(diag.Entry{Source: "api", Code: "X", Message: "seed"})
	anon := d.client()
	for _, rt := range []struct{ m, p string }{
		{"GET", "/v1/admin/system"}, {"GET", "/v1/admin/error-reports"}, {"GET", "/v1/admin/error-reports/" + id},
		{"PATCH", "/v1/admin/error-reports/" + id}, {"DELETE", "/v1/admin/error-reports/" + id}, {"GET", "/v1/admin/diagnostics/bundle"},
	} {
		assertCode(t, anon.do(rt.m, rt.p, nil), 401, "UNAUTHENTICATED")
		for name, c := range map[string]*tclient{"viewer": d.viewer, "operator": d.operator} {
			if r := c.do(rt.m, rt.p, map[string]any{"resolved": true}); r.Code != 403 || r.code() != "FORBIDDEN" {
				t.Errorf("%s %s as %s: %d %s", rt.m, rt.p, name, r.Code, r.Raw)
			}
		}
	}
	assertCode(t, anon.do("POST", "/v1/client-errors", map[string]any{"message": "x"}), 401, "UNAUTHENTICATED")
	for name, c := range map[string]*tclient{"viewer": d.viewer, "operator": d.operator, "admin": d.admin} {
		if r := c.do("POST", "/v1/client-errors", map[string]any{"message": "boom " + name}); r.Code != 202 {
			t.Errorf("client-errors as %s: %d %s", name, r.Code, r.Raw)
		}
	}
	// writes need CSRF
	saved := d.admin.csrf
	d.admin.csrf = ""
	assertCode(t, d.admin.do("PATCH", "/v1/admin/error-reports/"+id, map[string]any{"resolved": true}), 403, "CSRF_INVALID")
	assertCode(t, d.admin.do("DELETE", "/v1/admin/error-reports/"+id, nil), 403, "CSRF_INVALID")
	d.admin.csrf = saved
}

func TestDiagSystem(t *testing.T) {
	d := newDiagEnv(t)
	r := d.admin.must("GET", "/v1/admin/system", nil, 200)
	for _, k := range []string{"version", "commit", "build_mode", "go_version", "os", "standalone", "started_at", "uptime_seconds", "database", "worker", "disk", "last_exit_unclean", "open_error_reports"} {
		if _, ok := r.Data[k]; !ok {
			t.Errorf("missing %s: %s", k, r.Raw)
		}
	}
	if r.Data["standalone"] != true || r.Data["uptime_seconds"].(float64) < 89 || r.Data["last_exit_unclean"] != false || r.Data["open_error_reports"].(float64) != 0 {
		t.Fatalf("%s", r.Raw)
	}
	if db := r.Data["database"].(map[string]any); db["ok"] != true || db["message"] == "" {
		t.Fatal(db)
	}
	disk := r.Data["disk"].(map[string]any)
	if disk["total_bytes"].(float64) <= 0 || disk["free_bytes"].(float64) > disk["total_bytes"].(float64) || disk["archive_path"] == "" {
		t.Fatal(disk)
	}
	// worker down, an unclean exit recorded
	d.srv.d.Checks[1] = func(context.Context) string { return "unavailable" }
	diag.ReportPrevRun(context.Background(), diag.PrevRun{Unclean: true, Info: diag.RunInfo{PID: 4242}, CrashFiles: []string{"crash-x.txt"}})
	d.flush()
	r = d.admin.must("GET", "/v1/admin/system", nil, 200)
	if w := r.Data["worker"].(map[string]any); w["ok"] != false || !strings.Contains(w["message"].(string), "unavailable") {
		t.Fatal(w)
	}
	if r.Data["last_exit_unclean"] != true || r.Data["open_error_reports"].(float64) != 1 {
		t.Fatalf("%s", r.Raw)
	}
	// stays until an admin resolves that report
	list := d.admin.must("GET", "/v1/admin/error-reports?source=supervisor", nil, 200)
	id := list.Data["items"].([]any)[0].(map[string]any)["id"].(string)
	d.admin.must("PATCH", "/v1/admin/error-reports/"+id, map[string]any{"resolved": true}, 200)
	r = d.admin.must("GET", "/v1/admin/system", nil, 200)
	if r.Data["last_exit_unclean"] != false || r.Data["open_error_reports"].(float64) != 0 {
		t.Fatalf("%s", r.Raw)
	}
	// database down: still answers 200
	d.srv.d.Checks[0] = func(context.Context) string { return "unavailable" }
	r = d.admin.must("GET", "/v1/admin/system", nil, 200)
	if r.Data["database"].(map[string]any)["ok"] != false || r.Data["last_exit_unclean"] != false {
		t.Fatalf("%s", r.Raw)
	}
}

func TestDiagReportsListDetailPatchDelete(t *testing.T) {
	d := newDiagEnv(t)
	id1 := d.seed(diag.Entry{Source: "api", Code: "INTERNAL", Message: "GET /v1/x → 500", RequestID: "rid-aaa", User: "alice", Stack: "oa/x.f()\n\t/x.go:1", Details: map[string]any{"route": "/v1/x"}})
	d.seed(diag.Entry{Source: "api", Code: "INTERNAL", Message: "GET /v1/x → 500", RequestID: "rid-bbb", User: "bob", Stack: "oa/x.f()\n\t/x.go:9"})
	id2 := d.seed(diag.Entry{Source: "pipeline", Severity: "panic", Code: "PANIC", Message: "boom in loop", TaskID: "task-1"})
	r := d.admin.must("GET", "/v1/admin/error-reports", nil, 200)
	if r.Data["total"].(float64) != 2 || r.Data["page"].(float64) != 1 || r.Data["page_size"].(float64) != 20 || len(r.Data["items"].([]any)) != 2 {
		t.Fatalf("%s", r.Raw)
	}
	it := findItem(r.Data["items"].([]any), id1)
	for _, k := range []string{"id", "source", "severity", "code", "message", "count", "first_seen", "last_seen", "resolved", "request_id"} {
		if _, ok := it[k]; !ok {
			t.Errorf("list item lacks %s: %v", k, it)
		}
	}
	if it["count"].(float64) != 2 || it["request_id"] != "rid-bbb" || it["resolved"] != false {
		t.Fatalf("%v", it)
	}
	for q, want := range map[string]int{"?source=pipeline": 1, "?severity=panic": 1, "?severity=error": 1, "?resolved=true": 0, "?resolved=false": 2, "?state=open": 2, "?state=resolved": 0, "?state=all": 2, "?q=rid-aaa": 1, "?q=BOOM": 1, "?q=INTERNAL": 1, "?q=zzzz": 0, "?page=2&page_size=1": 2} {
		got := d.admin.must("GET", "/v1/admin/error-reports"+q, nil, 200)
		n := len(got.Data["items"].([]any))
		if q == "?page=2&page_size=1" {
			if got.Data["total"].(float64) != 2 || n != 1 || got.Data["page"].(float64) != 2 || got.Data["page_size"].(float64) != 1 {
				t.Errorf("%s: %s", q, got.Raw)
			}
			continue
		}
		if n != want {
			t.Errorf("%s: got %d want %d", q, n, want)
		}
	}
	for _, q := range []string{"?source=nope", "?severity=nope", "?resolved=maybe", "?state=nope", "?q=" + strings.Repeat("x", 201)} {
		assertCode(t, d.admin.do("GET", "/v1/admin/error-reports"+q, nil), 400, "INVALID_ARGUMENT")
	}
	det := d.admin.must("GET", "/v1/admin/error-reports/"+id1, nil, 200)
	if det.Data["stack"] == "" || det.Data["count"].(float64) != 2 || len(det.Data["occurrences"].([]any)) != 2 || det.Data["details"].(map[string]any)["route"] != "/v1/x" {
		t.Fatalf("%s", det.Raw)
	}
	oc := det.Data["occurrences"].([]any)[0].(map[string]any)
	if oc["request_id"] != "rid-bbb" || oc["user"] != "bob" || oc["at"] == "" {
		t.Fatalf("%v", oc)
	}
	if d2 := d.admin.must("GET", "/v1/admin/error-reports/"+id2, nil, 200); d2.Data["details"].(map[string]any)["task_id"] != "task-1" {
		t.Fatalf("%s", d2.Raw)
	}
	assertCode(t, d.admin.do("GET", "/v1/admin/error-reports/00000000-0000-4000-8000-000000000009", nil), 404, "NOT_FOUND")
	assertCode(t, d.admin.do("PATCH", "/v1/admin/error-reports/00000000-0000-4000-8000-000000000009", map[string]any{"resolved": true}), 404, "NOT_FOUND")
	assertCode(t, d.admin.do("DELETE", "/v1/admin/error-reports/00000000-0000-4000-8000-000000000009", nil), 404, "NOT_FOUND")
	assertCode(t, d.admin.do("PATCH", "/v1/admin/error-reports/"+id1, map[string]any{}), 400, "INVALID_ARGUMENT")
	assertCode(t, d.admin.do("PATCH", "/v1/admin/error-reports/"+id1, map[string]any{"resolved": true, "count": 0}), 400, "INVALID_JSON")
	p := d.admin.must("PATCH", "/v1/admin/error-reports/"+id1, map[string]any{"resolved": true}, 200)
	if p.Data["resolved"] != true || p.Data["id"] != id1 {
		t.Fatalf("%s", p.Raw)
	}
	if got := d.admin.must("GET", "/v1/admin/error-reports?resolved=true", nil, 200); len(got.Data["items"].([]any)) != 1 {
		t.Fatal(string(got.Raw))
	}
	// recurrence re-opens
	d.seed(diag.Entry{Source: "api", Code: "INTERNAL", Message: "GET /v1/x → 500", Stack: "oa/x.f()\n\t/x.go:5"})
	if d1 := d.admin.must("GET", "/v1/admin/error-reports/"+id1, nil, 200); d1.Data["resolved"] != false || d1.Data["count"].(float64) != 3 {
		t.Fatalf("%s", d1.Raw)
	}
	d.admin.must("PATCH", "/v1/admin/error-reports/"+id1, map[string]any{"resolved": false}, 200)
	if r := d.admin.do("DELETE", "/v1/admin/error-reports/"+id1, nil); r.Code != 204 || len(r.Raw) != 0 {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	assertCode(t, d.admin.do("GET", "/v1/admin/error-reports/"+id1, nil), 404, "NOT_FOUND")
	acts := d.auditActions()
	if acts["diagnostics.resolve"] != 1 || acts["diagnostics.reopen"] != 1 || acts["diagnostics.delete"] != 1 {
		t.Fatalf("audit %v", acts)
	}
}

func findItem(items []any, id string) map[string]any {
	for _, i := range items {
		if m := i.(map[string]any); m["id"] == id {
			return m
		}
	}
	return map[string]any{}
}

// testRoutes adds handlers that fail on purpose; they sit behind the real middleware (admin only by default deny).
func (d *diagEnv) testRoutes() {
	d.srv.handle("/v1/test/panic", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) {
		panic("handler exploded password=CANARYPANICPW token: CANARYPANICTOK")
	}})
	d.srv.handle("/v1/test/store/{id}", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) {
		d.srv.storeErr(w, r, fmt.Errorf("pq: connection to postgres://oa:CANARYDBPW@db/oa lost for item %s", r.PathValue("id")), "x")
	}})
	d.srv.handle("/v1/test/fail", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) {
		d.srv.fail(w, r, 500, "INTERNAL", "内部错误", nil)
	}})
	d.srv.handle("/v1/test/user", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) {
		d.srv.fail(w, r, 409, "VERSION_CONFLICT", "user error", nil)
	}})
}

func TestHandlerPanicAndFailRecorded(t *testing.T) {
	d := newDiagEnv(t)
	d.testRoutes()
	r := d.admin.do("GET", "/v1/test/panic", nil)
	if r.Code != 500 || r.code() != "INTERNAL" {
		t.Fatalf("%d %s", r.Code, r.Raw)
	}
	rid, _ := r.Err["request_id"].(string)
	if rid == "" || rid != r.Header.Get("X-Request-ID") {
		t.Fatalf("500 body must carry request_id: %s", r.Raw)
	}
	repID, _ := r.Err["details"].(map[string]any)["report_id"].(string)
	if repID == "" {
		t.Fatalf("no report_id in details: %s", r.Raw)
	}
	// the process is alive and serves the next request
	d.admin.must("GET", "/v1/admin/system", nil, 200)
	d.flush()
	det := d.admin.must("GET", "/v1/admin/error-reports/"+repID, nil, 200)
	if det.Data["severity"] != "panic" || det.Data["source"] != "api" || det.Data["request_id"] != rid ||
		!strings.Contains(det.Data["message"].(string), "/v1/test/panic") || det.Data["stack"] == "" {
		t.Fatalf("%s", det.Raw)
	}
	if oc := det.Data["occurrences"].([]any)[0].(map[string]any); oc["user"] != "admin" {
		t.Fatalf("user missing: %v", oc)
	}

	// a 5xx through fail
	for i := 0; i < 3; i++ {
		f := d.admin.do("GET", "/v1/test/fail", nil)
		if f.Code != 500 || f.Err["request_id"] == "" {
			t.Fatalf("%d %s", f.Code, f.Raw)
		}
	}
	// 4xx are not recorded
	assertCode(t, d.admin.do("GET", "/v1/test/user", nil), 409, "VERSION_CONFLICT")
	// storeErr default branch: ids differ, the report groups; the DB password is redacted
	s1 := d.admin.do("GET", "/v1/test/store/aaaaaaaa-bbbb-4ccc-8ddd-000000000001", nil)
	d.admin.do("GET", "/v1/test/store/aaaaaaaa-bbbb-4ccc-8ddd-000000000002", nil)
	assertCode(t, s1, 500, "INTERNAL")
	d.flush()
	list := d.admin.must("GET", "/v1/admin/error-reports", nil, 200)
	items := list.Data["items"].([]any)
	if len(items) != 3 { // panic, fail, store
		t.Fatalf("%d groups: %s", len(items), list.Raw)
	}
	counts := map[string]float64{}
	for _, it := range items {
		m := it.(map[string]any)
		counts[m["severity"].(string)+"/"+m["code"].(string)] += m["count"].(float64)
	}
	if counts["panic/PANIC"] != 1 || counts["error/INTERNAL"] != 5 {
		t.Fatalf("%v", counts)
	}
	assertNoLeak(t, list.Raw, "CANARY")
	for _, it := range items {
		det := d.admin.must("GET", "/v1/admin/error-reports/"+it.(map[string]any)["id"].(string), nil, 200)
		assertNoLeak(t, det.Raw, "CANARY")
	}
	assertNoLeak(t, r.Raw, "CANARY")
	if sys := d.admin.must("GET", "/v1/admin/system", nil, 200); sys.Data["open_error_reports"].(float64) != 3 {
		t.Fatalf("%s", sys.Raw)
	}
}

func assertNoLeak(t *testing.T, raw []byte, needles ...string) {
	t.Helper()
	for _, n := range needles {
		if bytes.Contains(raw, []byte(n)) {
			t.Fatalf("response leaks %q: %s", n, raw)
		}
	}
}

func TestClientErrors(t *testing.T) {
	d := newDiagEnv(t)
	body := func(msg string) map[string]any {
		return map[string]any{"message": msg, "stack": "TypeError: x\n    at run (https://h/assets/index-Ab12Cd34.js:1:23456)\n    at n (https://h/assets/vendor-Zz99Yy88.js:2:9)",
			"route": "/tasks/1", "component": "TaskList", "app_version": "1.0.0", "request_id": "rid-client"}
	}
	r := d.viewer.must("POST", "/v1/client-errors", body("Cannot read properties of undefined (reading 'x')"), 202)
	id, _ := r.Data["report_id"].(string)
	if len(id) != 36 {
		t.Fatalf("%s", r.Raw)
	}
	b2 := body("Cannot read properties of undefined (reading 'x')")
	b2["stack"] = "TypeError: x\n    at run (http://other/assets/index-Qq77Ww66.js:9:111)\n    at n (http://other/assets/vendor-Hh11Jj22.js:5:7)"
	if r2 := d.operator.must("POST", "/v1/client-errors", b2, 202); r2.Data["report_id"] != id {
		t.Fatal("same client error with other bundle hashes/line numbers must group")
	}
	d.flush()
	det := d.admin.must("GET", "/v1/admin/error-reports/"+id, nil, 200)
	if det.Data["source"] != "client" || det.Data["count"].(float64) != 2 || det.Data["request_id"] != "rid-client" ||
		det.Data["details"].(map[string]any)["component"] != "TaskList" {
		t.Fatalf("%s", det.Raw)
	}
	// redaction + truncation
	long := strings.Repeat("a b ", 400)
	r3 := d.viewer.must("POST", "/v1/client-errors", map[string]any{"message": "failed password=CANARYCLI1 " + long, "stack": "at x Bearer CANARYCLIENTBEARER123 " + strings.Repeat("y", 3000)}, 202)
	d.flush()
	det = d.admin.must("GET", "/v1/admin/error-reports/"+r3.Data["report_id"].(string), nil, 200)
	assertNoLeak(t, det.Raw, "CANARY")
	if n := len([]rune(det.Data["message"].(string))); n > 520 {
		t.Fatalf("message not truncated: %d", n)
	}
	// validation
	assertCode(t, d.viewer.do("POST", "/v1/client-errors", map[string]any{"message": "  "}), 400, "INVALID_ARGUMENT")
	assertCode(t, d.viewer.do("POST", "/v1/client-errors", map[string]any{"stack": "x"}), 400, "INVALID_ARGUMENT")
	assertCode(t, d.viewer.do("POST", "/v1/client-errors", map[string]any{"message": "x", "extra": 1}), 400, "INVALID_JSON")
	big := d.viewer.do("POST", "/v1/client-errors", map[string]any{"message": "x", "stack": strings.Repeat("z", 9000)})
	assertCode(t, big, 400, "BODY_TOO_LARGE")
	// per-user rate limit: 20/min, 429 with Retry-After; other users are unaffected
	lim := d.operator
	var last resp
	n := 0
	for i := 0; i < 25; i++ {
		last = lim.do("POST", "/v1/client-errors", map[string]any{"message": fmt.Sprintf("flood %d", i)})
		if last.Code == 429 {
			break
		}
		n++
	}
	assertCode(t, last, 429, "RATE_LIMITED")
	if n >= 21 || n < 17 || last.Header.Get("Retry-After") == "" {
		t.Fatalf("accepted %d before 429; Retry-After=%q", n, last.Header.Get("Retry-After"))
	}
	if r := d.admin.do("POST", "/v1/client-errors", map[string]any{"message": "other user"}); r.Code != 202 {
		t.Fatalf("limit must be per user: %d", r.Code)
	}
}

func TestDiagBundleHasNoSecrets(t *testing.T) {
	d := newDiagEnv(t)
	// canaries everywhere a secret could come from
	t.Setenv("OA_WORKER_TOKEN", "CANARY-ENV-WORKER-TOKEN-0123456789")
	t.Setenv("OA_SECRET_KEY", "CANARYENVSECRETKEY")
	t.Setenv("OA_LLM_API_KEY", "sk-CANARYENVLLMKEY0123456789")
	t.Setenv("OA_DATABASE_URL", "postgres://oa:CANARYENVDBPASS@127.0.0.1/oa")
	t.Setenv("OA_LISTEN_ADDR", "127.0.0.1:8080")
	diag.RegisterSecret("CANARY-ENV-WORKER-TOKEN-0123456789")
	defer diag.ResetSecrets()
	logs := "2026/10/22 start ok user alice@example.com home /home/alice/x\n" +
		"request Authorization: Bearer CANARYLOGBEARER12345\n" +
		"login failed password=CANARYLOGPW cookie oa_session=CANARYLOGSESSION\n" +
		"SETUP TOKEN: ABCDEFGH23456723ABCDEFGH234567ABCDEFGH\n" +
		"worker token CANARY-ENV-WORKER-TOKEN-0123456789 seen\n" +
		"dial postgres://oa:CANARYLOGDBPW@h/oa\n"
	must(t, os.WriteFile(filepath.Join(d.logDir, "oa-agent.log"), []byte(logs), 0o600))
	must(t, os.WriteFile(filepath.Join(d.logDir, "worker.log"), []byte("api_key=CANARYWORKERLOGKEY\nfine line\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(d.logDir, "postgres.log"), []byte("secret: CANARYPGLOG\n"), 0o600))
	must(t, os.WriteFile(filepath.Join(d.logDir, "crash-20260101T000000Z.txt"), []byte("crash with token=CANARYCRASH1\n"), 0o600))
	d.srv.d.Diag.Config = func() map[string]any {
		return map[string]any{"listen_addr": "127.0.0.1:8080", "api_key": "CANARYCFGKEY", "database_url": "postgres://oa:CANARYCFGDBPW@h/oa", "nested": map[string]any{"password": "CANARYCFGPW", "ok": 1}}
	}
	d.srv.d.LLM = stubLLMConfig{}
	// an old report that was stored before redaction rules existed
	_, err := d.st.Pool.Exec(context.Background(), `INSERT INTO error_reports(id, fingerprint, source, severity, code, message, stack, details, first_seen, last_seen)
	  VALUES (gen_random_uuid(), 'legacy', 'api', 'error', 'X', 'failed with password=CANARYDBROW', 'at x token: CANARYDBSTACK', '{"api_key":"CANARYDBDETAIL","note":"Bearer CANARYDBBEARER12345"}', now(), now())`)
	must(t, err)
	d.seed(diag.Entry{Source: "api", Code: "INTERNAL", Message: "regular failure", Details: map[string]any{"password": "CANARYENTRY"}})

	req := httptest.NewRequest("GET", "http://localhost:8080/v1/admin/diagnostics/bundle", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: d.admin.cookie})
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "oa-agent-diagnostics-") {
		t.Fatalf("%d %v", rec.Code, rec.Header())
	}
	files := unzip(t, rec.Body.Bytes())
	for _, want := range []string{"README.txt", "VERSION.txt", "environment.txt", "config.json", "system.json", "migrations.json", "logs/oa-agent.log", "logs/worker.log", "logs/postgres.log", "error-reports.json", "crash/crash-20260101T000000Z.txt"} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle lacks %s (has %v)", want, keys(files))
		}
	}
	for name, content := range files {
		if strings.Contains(content, "CANARY") || strings.Contains(content, "ABCDEFGH2345") {
			t.Errorf("%s leaks a secret:\n%s", name, content)
		}
		for _, pii := range []string{"alice@example.com", "/home/alice"} {
			if strings.Contains(content, pii) {
				t.Errorf("%s leaks %s", name, pii)
			}
		}
	}
	if !strings.Contains(files["logs/oa-agent.log"], "start ok") || !strings.Contains(files["environment.txt"], "OA_LISTEN_ADDR=127.0.0.1:8080") ||
		!strings.Contains(files["environment.txt"], "OA_WORKER_TOKEN=[set]") {
		t.Fatalf("expected harmless content kept:\n%s\n%s", files["logs/oa-agent.log"], files["environment.txt"])
	}
	var cfg map[string]any
	must(t, json.Unmarshal([]byte(files["config.json"]), &cfg))
	llmCfg := cfg["llm"].(map[string]any)
	if llmCfg["base_host"] != "llm.example.com:8443" || llmCfg["model"] != "m1" || cfg["listen_addr"] != "127.0.0.1:8080" || cfg["standalone"] != true {
		t.Fatalf("%v", cfg)
	}
	if !strings.Contains(files["error-reports.json"], "regular failure") || !strings.Contains(files["README.txt"], "不包含") {
		t.Fatal("reports/readme missing")
	}
	if d.auditActions()["diagnostics.bundle"] != 1 {
		t.Fatal("bundle download must be audited")
	}
}

type stubLLMConfig struct{ LLMManager }

func (stubLLMConfig) Config() llm.ConfigView {
	return llm.ConfigView{Mode: "llm", PresetID: "p", BaseURL: "https://user:CANARYURLPW@llm.example.com:8443/v1?key=CANARYQ", Model: "m1", APIKeySet: true, APIKeyHint: "sk-…CANARYHINT"}
}

func TestDiagBundleSizeCap(t *testing.T) {
	d := newDiagEnv(t)
	line := strings.Repeat("noise line without secrets 0123456789 ", 20) + "\n"
	f, err := os.Create(filepath.Join(d.logDir, "oa-agent.log"))
	must(t, err)
	for i := 0; i < 60000; i++ { // ~46 MB
		f.WriteString(line)
	}
	f.Close()
	req := httptest.NewRequest("GET", "http://localhost:8080/v1/admin/diagnostics/bundle", nil)
	req.RemoteAddr = "192.0.2.10:5555"
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: d.admin.cookie})
	rec := httptest.NewRecorder()
	d.h.ServeHTTP(rec, req)
	if rec.Code != 200 || rec.Body.Len() > 20<<20 {
		t.Fatalf("%d size %d", rec.Code, rec.Body.Len())
	}
	files := unzip(t, rec.Body.Bytes())
	if n := len(files["logs/oa-agent.log"]); n > bundleLogTail+1024 || n < bundleLogTail/2 {
		t.Fatalf("log tail %d", n)
	}
}

func TestEvery500CarriesRequestID(t *testing.T) {
	d := newDiagEnv(t)
	d.testRoutes()
	for _, p := range []string{"/v1/test/fail", "/v1/test/panic", "/v1/test/store/x"} {
		r := d.admin.do("GET", p, nil)
		if r.Code != 500 || r.Err["request_id"] == "" || r.Err["request_id"] != r.Header.Get("X-Request-ID") {
			t.Errorf("%s: %d %s", p, r.Code, r.Raw)
		}
	}
}

func unzip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		must(t, err)
		raw, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(raw)
	}
	return out
}

func keys(m map[string]string) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
