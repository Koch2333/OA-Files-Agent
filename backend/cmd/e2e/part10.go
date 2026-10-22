package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// part10: error reports and diagnostics (enterprise mode, real PostgreSQL). The deliberate server-side faults use the
// test-only /v1/test/fault hook that exists only in binaries built with `-tags faultinject`; without it that
// sub-check is skipped and says so.
func part10() {
	fmt.Println("--- part 10: diagnostics (system info, error reports, client errors, diagnostic bundle)")
	suffix := fmt.Sprint(time.Now().Unix() % 100000)
	admin := adminActor()
	anon := newActor()
	viewer := mkUser(admin, "e2e-diag-viewer-"+suffix, "viewer")
	operator := mkUser(admin, "e2e-diag-operator-"+suffix, "operator")

	// access control
	for _, p := range []string{"/v1/admin/system", "/v1/admin/error-reports", "/v1/admin/diagnostics/bundle"} {
		check("10 anonymous GET "+p+" → 401", anon.do("GET", p, nil).Status == 401)
		check("10 viewer GET "+p+" → 403", viewer.do("GET", p, nil).Status == 403)
		check("10 operator GET "+p+" → 403", operator.do("GET", p, nil).Status == 403)
	}
	check("10 anonymous POST /v1/client-errors → 401", anon.do("POST", "/v1/client-errors", map[string]any{"message": "x"}).Status == 401)

	// system info
	r := admin.do("GET", "/v1/admin/system", nil)
	db, _ := r.Data["database"].(map[string]any)
	wk, _ := r.Data["worker"].(map[string]any)
	disk, _ := r.Data["disk"].(map[string]any)
	check("10 GET /v1/admin/system: version, mode, runtime, uptime", r.Status == 200 && r.s("version") != "" && r.s("go_version") != "" && r.s("os") != "" && (r.s("build_mode") == "debug" || r.s("build_mode") == "release") && r.Data["uptime_seconds"] != nil, string(r.Raw))
	check("10 system: database and worker healthy", db["ok"] == true && wk["ok"] == true, string(r.Raw))
	check("10 system: disk of the archive root", disk["total_bytes"] != nil && disk["free_bytes"] != nil && fmt.Sprint(disk["archive_path"]) != "", string(r.Raw))
	check("10 system: last_exit_unclean / open_error_reports / standalone present", r.Data["last_exit_unclean"] != nil && r.Data["open_error_reports"] != nil && r.Data["standalone"] != nil)
	openBefore, _ := r.Data["open_error_reports"].(float64)

	// client errors
	stack := "TypeError: x is undefined\n    at run (https://h/assets/index-Ab12Cd34.js:1:23456)\n    at n (https://h/assets/vendor-Zz99Yy88.js:2:9)"
	letters := strings.Map(func(r rune) rune { return r - '0' + 'a' }, suffix) // digits are normalised away by the fingerprint; letters keep this run's report unique
	msg := "e2e client error " + letters
	c1 := viewer.do("POST", "/v1/client-errors", map[string]any{"message": msg, "stack": stack + "\nContext: password=E2ECANARY", "route": "/tasks", "component": "TaskList", "app_version": "1.0.0", "request_id": "e2e-" + suffix})
	id := c1.s("report_id")
	check("10 POST /v1/client-errors → 202 {report_id} (viewer allowed)", c1.Status == 202 && len(id) == 36, string(c1.Raw))
	c2 := operator.do("POST", "/v1/client-errors", map[string]any{"message": msg, "route": "/tasks", "component": "TaskList", "stack": strings.ReplaceAll(strings.ReplaceAll(stack, "Ab12Cd34", "Qq77Ww66"), ":1:23456", ":9:1")})
	check("10 same client error with other bundle hash/line numbers groups into one report", c2.Status == 202 && c2.s("report_id") == id, string(c2.Raw))
	check("10 client error: empty message → 400", viewer.do("POST", "/v1/client-errors", map[string]any{"message": ""}).Status == 400)
	check("10 client error: oversized body → 400", viewer.do("POST", "/v1/client-errors", map[string]any{"message": "x", "stack": strings.Repeat("z", 9000)}).Status == 400)
	d := waitReport(admin, id)
	oc, _ := d.Data["occurrences"].([]any)
	check("10 detail: source=client, count=2, occurrences, stack", d.Status == 200 && d.s("source") == "client" && d.Data["count"] == float64(2) && len(oc) == 2 && d.s("stack") != "", string(d.Raw))
	check("10 detail: secrets redacted before storage", !strings.Contains(string(d.Raw), "E2ECANARY"), string(d.Raw))

	// list / filters / patch / delete
	waitReport(admin, id)
	l := admin.do("GET", "/v1/admin/error-reports?source=client&q="+url.QueryEscape(suffix), nil)
	check("10 list: filters source+q, paged envelope", l.Status == 200 && l.Data["total"] == float64(1) && l.Data["page"] != nil && l.Data["page_size"] != nil, string(l.Raw))
	check("10 list: bad filter → 400", admin.do("GET", "/v1/admin/error-reports?severity=nope", nil).Status == 400)
	check("10 unknown report → 404", admin.do("GET", "/v1/admin/error-reports/00000000-0000-4000-8000-000000000009", nil).Status == 404)
	p := admin.do("PATCH", "/v1/admin/error-reports/"+id, map[string]any{"resolved": true})
	check("10 PATCH resolved=true", p.Status == 200 && p.Data["resolved"] == true, string(p.Raw))
	viewer.do("POST", "/v1/client-errors", map[string]any{"message": msg, "stack": stack})
	time.Sleep(1200 * time.Millisecond)
	d = admin.do("GET", "/v1/admin/error-reports/"+id, nil)
	check("10 a resolved report re-opens when it recurs", d.Data["resolved"] == false && d.Data["count"] == float64(3), string(d.Raw))

	// deliberate server-side faults (faultinject build only)
	probe := admin.do("GET", "/v1/test/fault", nil)
	if probe.Status != 200 {
		fmt.Println("INFO  server-side fault checks skipped: the API was not built with -tags faultinject (no /v1/test/fault hook)")
	} else {
		pr := admin.do("GET", "/v1/test/fault?kind=panic", nil)
		rep, _ := pr.Err["details"].(map[string]any)
		check("10 handler panic → 500 INTERNAL with request_id and report_id", pr.Status == 500 && pr.Code == "INTERNAL" && pr.Err["request_id"] != "" && rep["report_id"] != nil, string(pr.Raw))
		check("10 the process survived the panic", admin.do("GET", "/v1/admin/system", nil).Status == 200)
		time.Sleep(1200 * time.Millisecond)
		pd := admin.do("GET", fmt.Sprintf("/v1/admin/error-reports/%v", rep["report_id"]), nil)
		check("10 panic report: severity=panic, source=api, stack, request id", pd.s("severity") == "panic" && pd.s("source") == "api" && pd.s("stack") != "" && pd.s("request_id") == pr.Err["request_id"], string(pd.Raw))
		er := admin.do("GET", "/v1/test/fault?kind=error", nil)
		check("10 5xx via the shared fail path → 500 with request_id", er.Status == 500 && er.Err["request_id"] != "", string(er.Raw))
		time.Sleep(1200 * time.Millisecond) // async writer
		ls := admin.do("GET", "/v1/admin/error-reports?q=faultinject", nil)
		check("10 deliberate error recorded and redacted", ls.Status == 200 && ls.Data["total"] != nil && ls.Data["total"] != float64(0) && !strings.Contains(string(ls.Raw), "FAULTCANARY"), string(ls.Raw))
		gr := admin.do("GET", "/v1/test/fault?kind=goroutine", nil)
		time.Sleep(1200 * time.Millisecond)
		check("10 goroutine panic does not kill the process and is recorded", gr.Status == 202 && admin.do("GET", "/v1/admin/system", nil).Status == 200 && admin.do("GET", "/v1/admin/error-reports?q=goroutine+panic", nil).Data["total"] != float64(0))
	}

	// diagnostic bundle
	t0 := time.Now()
	req, _ := http.NewRequest("GET", *base+"/v1/admin/diagnostics/bundle", nil)
	res, err := admin.c.Do(req)
	var body []byte
	if err == nil {
		body, _ = io.ReadAll(res.Body)
		res.Body.Close()
	}
	check("10 bundle: 200 application/zip with attachment name", err == nil && res.StatusCode == 200 && res.Header.Get("Content-Type") == "application/zip" && strings.Contains(res.Header.Get("Content-Disposition"), "oa-agent-diagnostics-"), err)
	zr, zerr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	names := map[string]string{}
	if zerr == nil {
		for _, f := range zr.File {
			rc, _ := f.Open()
			raw, _ := io.ReadAll(rc)
			rc.Close()
			names[f.Name] = string(raw)
		}
	}
	for _, n := range []string{"README.txt", "VERSION.txt", "environment.txt", "config.json", "system.json", "migrations.json", "error-reports.json"} {
		_, ok := names[n]
		check("10 bundle contains "+n, ok, zerr)
	}
	leak := false
	for n, c := range names {
		for _, bad := range []string{"E2ECANARY", *adminPassFlag, e2ePW, "$argon2", "FAULTCANARY"} {
			if strings.Contains(c, bad) {
				leak = true
				fmt.Println("      bundle file", n, "contains", bad)
			}
		}
	}
	check("10 bundle contains no planted secret, password or hash", !leak)
	check("10 bundle size is bounded and quick", len(body) < 20<<20 && time.Since(t0) < 30*time.Second, len(body))
	_ = openBefore

	// cleanup: delete the client report; audit
	check("10 DELETE report → 204", admin.do("DELETE", "/v1/admin/error-reports/"+id, nil).Status == 204)
	check("10 deleted report → 404", admin.do("GET", "/v1/admin/error-reports/"+id, nil).Status == 404)
	au := admin.do("GET", "/v1/audit-logs?page_size=200", nil)
	seen := map[string]bool{}
	items, _ := au.Data["items"].([]any)
	for _, it := range items {
		seen[it.(map[string]any)["action"].(string)] = true
	}
	for _, act := range []string{"diagnostics.bundle", "diagnostics.resolve", "diagnostics.delete"} {
		check("10 audit log has "+act, seen[act])
	}

	// rate limit last (it spends the viewer's budget): 20/min per user
	limited, retry := 0, ""
	for i := 0; i < 30; i++ {
		x := viewer.do("POST", "/v1/client-errors", map[string]any{"message": fmt.Sprintf("flood %d %s", i, suffix)})
		if x.Status == 429 {
			limited++
			retry = x.Header.Get("Retry-After")
			break
		}
	}
	check("10 client-errors per-user rate limit → 429 with Retry-After", limited == 1 && retry != "", retry)
	check("10 other users are not limited by it", operator.do("POST", "/v1/client-errors", map[string]any{"message": "other user " + suffix}).Status == 202)
}

// waitReport polls the report detail: reports are written asynchronously (about 0.5 s).
func waitReport(a *actor, id string) reply {
	var r reply
	for i := 0; i < 30; i++ {
		r = a.do("GET", "/v1/admin/error-reports/"+id, nil)
		if r.Status == 200 {
			return r
		}
		time.Sleep(200 * time.Millisecond)
	}
	return r
}
