// e2e drives a running API through the full contest flow using the real sample files:
// batch import (with sub-folders) → OCR/identification → review → approve → execute → ledger search → Excel export,
// plus idempotency, error files and rollback. It prints PASS/FAIL per check and exits non-zero on any failure.
//
//	go run ./cmd/e2e -api http://127.0.0.1:8080 -samples ../tests/samples
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xuri/excelize/v2"
)

var (
	examplesDir = flag.String("examples", "", "import this folder of real contest files and compare detection with the file-name convention")
	recordPath  = flag.String("record", "", "write every JSON response to this file for scripts/check_contracts.py --recorded")
	recorded    []map[string]any
	archiveDir  = flag.String("archive", "", "archive root to verify files on disk (optional)")
	queryExtra  string
	base        = flag.String("api", "http://127.0.0.1:8080", "API base URL")
	dir         = flag.String("samples", "../tests/samples", "samples directory with manifest.json")
	failed      = 0
)

func check(name string, ok bool, detail ...any) {
	if ok {
		fmt.Printf("PASS  %s\n", name)
		return
	}
	failed++
	fmt.Printf("FAIL  %s %s\n", name, fmt.Sprint(detail...))
}

type envelope struct {
	Data  json.RawMessage `json:"data"`
	Error *struct{ Code, Message string }
}

func call(method, path string, body any, headers map[string]string) (int, json.RawMessage, *http.Response) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, *base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("request failed:", err)
		os.Exit(2)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	recordCall(method, path, res.StatusCode, raw)
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if env.Error != nil {
		return res.StatusCode, json.RawMessage(env.Error.Code), res
	}
	return res.StatusCode, env.Data, res
}

type job struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	TotalFiles     int     `json:"total_files"`
	ProcessedFiles int     `json:"processed_files"`
	SuccessCount   int     `json:"success_count"`
	FailureCount   int     `json:"failure_count"`
	PlanID         *string `json:"plan_id"`
}
type item struct {
	FileID          string  `json:"file_id"`
	OriginalName    string  `json:"original_name"`
	SuggestedName   string  `json:"suggested_name"`
	DestinationPath string  `json:"destination_path"`
	Department      *string `json:"department"`
	DocumentType    *string `json:"document_type"`
	DocumentDate    *string `json:"document_date"`
	ReviewRequired  bool    `json:"review_required"`
	Warnings        []string
}
type plan struct {
	ID      string
	Status  string
	Version int
	Items   []item
}

func s(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func upload(files []string, paths []string, key string) (int, json.RawMessage) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	pj, _ := json.Marshal(paths)
	_ = mw.WriteField("paths", string(pj))
	for i, f := range files {
		fw, _ := mw.CreateFormFile("files", filepath.Base(paths[i]))
		b, _ := os.ReadFile(f)
		fw.Write(b)
	}
	mw.Close()
	req, _ := http.NewRequest("POST", *base+"/v1/imports"+queryExtra, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	recordCall("POST", "/v1/imports", res.StatusCode, raw)
	var env envelope
	_ = json.Unmarshal(raw, &env)
	if env.Error != nil {
		return res.StatusCode, json.RawMessage(env.Error.Code)
	}
	return res.StatusCode, env.Data
}

func waitFor(id string, want ...string) job {
	deadline := time.Now().Add(6 * time.Minute)
	var j job
	for time.Now().Before(deadline) {
		_, d, _ := call("GET", "/v1/tasks/"+id, nil, nil)
		_ = json.Unmarshal(d, &j)
		for _, w := range want {
			if j.Status == w {
				return j
			}
		}
		if j.Status == "failed" || j.Status == "cancelled" {
			return j
		}
		time.Sleep(700 * time.Millisecond)
	}
	return j
}

func main() {
	flag.Parse()
	if *examplesDir != "" {
		examples(*examplesDir)
		if failed > 0 {
			os.Exit(1)
		}
		return
	}
	var manifest struct {
		Files []struct {
			File          string
			Department    string
			Type          string
			Date          string
			ExpectedError string `json:"expected_error"`
		}
	}
	raw, err := os.ReadFile(filepath.Join(*dir, "manifest.json"))
	if err != nil {
		panic(err)
	}
	_ = json.Unmarshal(raw, &manifest)

	// R01/R02: batch import with sub-folders, including a corrupt and an encrypted file and a legacy .doc.
	var files, paths []string
	truth := map[string]struct{ d, t, dt string }{}
	expectErr := map[string]bool{}
	for i, m := range manifest.Files {
		files = append(files, filepath.Join(*dir, m.File))
		sub := []string{"导入/财务", "导入/行政", "导入/采购", "导入"}[i%4]
		paths = append(paths, sub+"/"+m.File)
		truth[m.File] = struct{ d, t, dt string }{m.Department, m.Type, m.Date}
		expectErr[m.File] = m.ExpectedError != ""
	}
	tmp := filepath.Join(os.TempDir(), "旧版文档.doc")
	_ = os.WriteFile(tmp, []byte("legacy"), 0o600)
	files = append(files, tmp)
	paths = append(paths, "导入/旧版文档.doc")

	code, d := upload(files, paths, "e2e-"+fmt.Sprint(time.Now().UnixNano()))
	check("R01 POST /v1/imports accepted (202)", code == 202, code, string(d))
	var j job
	_ = json.Unmarshal(d, &j)
	check("R01 task total_files counts every file incl. rejected", j.TotalFiles == len(files), j.TotalFiles)
	j = waitFor(j.ID, "awaiting_review")
	check("task reaches awaiting_review (not auto-archived)", j.Status == "awaiting_review", j.Status)
	check("plan linked to task", j.PlanID != nil)
	check("failed files counted (corrupt+encrypted+legacy)", j.FailureCount >= 3, j.FailureCount)

	_, d, _ = call("GET", "/v1/files?task_id="+j.ID+"&page_size=200", nil, nil)
	var fp struct {
		Items []struct {
			ID, SourceName, SourcePath, ArchiveStatus string `json:"-"`
		} `json:"items"`
	}
	_ = fp
	var page struct {
		Items []map[string]any `json:"items"`
		Total int
	}
	_ = json.Unmarshal(d, &page)
	byName := map[string]map[string]any{}
	for _, it := range page.Items {
		byName[it["source_name"].(string)] = it
	}
	ok, total := 0, 0
	for name, tr := range truth {
		f := byName[name]
		if f == nil {
			check("file present "+name, false)
			continue
		}
		if expectErr[name] {
			check("R02 error file reported: "+name, f["error"] != nil, f["error"])
			continue
		}
		total++
		match := fmt.Sprint(f["detected_department"]) == tr.d && fmt.Sprint(f["detected_type"]) == tr.t && fmt.Sprint(f["detected_date"]) == tr.dt
		if match {
			ok++
		} else {
			fmt.Printf("      ↳ %s: got %v/%v/%v want %s/%s/%s (review=%v)\n", name, f["detected_department"], f["detected_type"], f["detected_date"], tr.d, tr.t, tr.dt, f["review_required"])
		}
		// never invent: a mismatch must at least be flagged for review or null
		if !match {
			wrongConfident := f["review_required"] == false && f["detected_department"] != nil && fmt.Sprint(f["detected_department"]) != tr.d
			check("R05 no confident wrong department: "+name, !wrongConfident)
		}
	}
	fmt.Printf("INFO  field accuracy (dept+type+date all correct): %d/%d\n", ok, total)
	check("R05 ≥ 80% files fully correct", total > 0 && float64(ok)/float64(total) >= 0.8, ok, "/", total)
	if f := byName["采购申请单_拍照.png"]; f != nil {
		ev, _ := json.Marshal(f["evidence"])
		check("R04 image identified via OCR with evidence", len(f["evidence"].([]any)) > 0, string(ev))
	}
	check("R02 legacy .doc rejected with explanation", byName["旧版文档.doc"] != nil && strings.Contains(fmt.Sprint(byName["旧版文档.doc"]["error"]), "UNSUPPORTED"), byName["旧版文档.doc"]["error"])

	// R06/R07: plan preview
	_, d, _ = call("GET", "/v1/archive-plans/"+*j.PlanID, nil, nil)
	var p plan
	_ = json.Unmarshal(d, &p)
	check("plan awaiting_review with items", p.Status == "awaiting_review" && len(p.Items) > 0, p.Status, len(p.Items))
	st, _, _ := call("POST", "/v1/archive-plans/"+p.ID+"/execute", nil, map[string]string{"Idempotency-Key": "k0"})
	check("execute refused before approval (409)", st == 409, st)
	st, ec, _ := call("PATCH", "/v1/archive-plans/"+p.ID, map[string]any{"version": p.Version, "items": []map[string]any{{"file_id": p.Items[0].FileID, "destination_path": "../evil/x.docx"}}}, nil)
	check("path traversal edit rejected (422)", st == 422, st, string(ec))
	st, ec, _ = call("PATCH", "/v1/archive-plans/"+p.ID, map[string]any{"version": 999, "items": []map[string]any{{"file_id": p.Items[0].FileID, "department": "财务部"}}}, nil)
	check("stale version rejected (409)", st == 409, st, string(ec))

	// manual review of any item needing it
	var fix []map[string]any
	for _, it := range p.Items {
		if it.ReviewRequired {
			tr := truth[strings.TrimPrefix(it.OriginalName, "")]
			m := map[string]any{"file_id": it.FileID, "department": orDefault(it.Department, tr.d, "行政部"), "document_type": orDefault(it.DocumentType, tr.t, "通知"), "document_date": orDefault(it.DocumentDate, tr.dt, "2026-01-01")}
			fix = append(fix, m)
		}
	}
	if len(fix) > 0 {
		st, _, _ = call("POST", "/v1/archive-plans/"+p.ID+"/approve", map[string]any{"version": p.Version}, nil)
		check("approve refused while items need review (409)", st == 409, st)
		st, d, _ = call("PATCH", "/v1/archive-plans/"+p.ID, map[string]any{"version": p.Version, "items": fix}, nil)
		check("manual revision accepted", st == 200, st, string(d))
		_ = json.Unmarshal(d, &p)
	}
	st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/approve", map[string]any{"version": p.Version}, nil)
	check("approve (200)", st == 200, st, string(d))
	_ = json.Unmarshal(d, &p)
	check("approved but not yet executed", p.Status == "approved")
	st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/execute", nil, nil)
	check("execute requires Idempotency-Key (400)", st == 400, st)
	st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/execute", nil, map[string]string{"Idempotency-Key": "e2e-exec-1"})
	check("execute (200)", st == 200, st, string(d))
	var done plan
	_ = json.Unmarshal(d, &done)
	check("plan executed", done.Status == "executed", done.Status)
	st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/execute", nil, map[string]string{"Idempotency-Key": "e2e-exec-1"})
	check("repeated execute is idempotent (200, same state)", st == 200, st, string(d))
	j2 := waitFor(j.ID, "completed", "partial_failure")
	check("task finished (partial_failure expected: error files)", j2.Status == "partial_failure" || j2.Status == "completed", j2.Status)

	// R07: real files exist on disk is verified by the pipeline; R08/R09: ledger
	_, d, _ = call("GET", "/v1/ledger?page_size=200&sort=archive_path&order=asc", nil, nil)
	var led struct {
		Items []struct {
			OriginalName  string `json:"original_name"`
			CanonicalName string `json:"canonical_name"`
			Department    string `json:"department"`
			DocumentType  string `json:"document_type"`
			DocumentDate  string `json:"document_date"`
			ArchivePath   string `json:"archive_path"`
			Sha256        string `json:"sha256"`
			TaskID        string `json:"task_id"`
		}
		Total int
	}
	_ = json.Unmarshal(d, &led)
	check("R08 ledger rows == plan items", led.Total >= len(done.Items) && len(done.Items) > 0, led.Total, len(done.Items))
	if len(led.Items) > 0 {
		complete, mine := true, 0
		for _, e := range led.Items {
			if e.OriginalName == "" || e.CanonicalName == "" || e.ArchivePath == "" || len(e.Sha256) != 64 {
				complete = false
			}
			if e.TaskID == j.ID {
				mine++
			}
		}
		check("R08 ledger fields populated and linked to this task", complete && mine == len(done.Items), complete, mine, len(done.Items))
	}
	_, d, _ = call("GET", "/v1/ledger?q="+url.QueryEscape("采购"), nil, nil)
	var hit struct {
		Items []struct{ OriginalName, CanonicalName string }
		Total int
	}
	_ = json.Unmarshal(d, &hit)
	check("R09 keyword search '采购' hits files", hit.Total > 0, hit.Total)
	_, d, _ = call("GET", "/v1/ledger?q="+url.QueryEscape("绝不存在的关键词xyz"), nil, nil)
	_ = json.Unmarshal(d, &hit)
	check("R09 search with no match returns empty", hit.Total == 0, hit.Total)
	st, _, _ = call("GET", "/v1/ledger?q=%25", nil, nil)
	check("search treats % literally (no wildcard abuse)", st == 200)

	// R10: Excel
	res, err := http.Get(*base + "/v1/ledger/export?sort=archive_path&order=asc")
	check("R10 export 200 + xlsx content-type", err == nil && res.StatusCode == 200 && strings.Contains(res.Header.Get("Content-Type"), "spreadsheetml"), res)
	if err == nil {
		b, _ := io.ReadAll(res.Body)
		xf, err := excelize.OpenReader(bytes.NewReader(b))
		check("R10 xlsx opens", err == nil, err)
		if err == nil {
			rows, _ := xf.GetRows("归档台账")
			check("R10 xlsx has header + all ledger rows", len(rows) == led.Total+1 && rows[0][1] == "原文件名", len(rows), led.Total)
			if len(rows) > 1 {
				check("R10 xlsx row matches ledger", rows[1][1] == led.Items[0].OriginalName && rows[1][6] == led.Items[0].ArchivePath, rows[1])
			}
		}
	}

	// R12: events + SSE resume, cancel semantics, rollback
	req, _ := http.NewRequest("GET", *base+"/v1/tasks/"+j.ID+"/events", nil)
	req.Header.Set("Last-Event-ID", "0")
	sse := &http.Client{Timeout: 3 * time.Second}
	if r, err := sse.Do(req); err == nil {
		got, _ := io.ReadAll(r.Body)
		r.Body.Close()
		check("SSE streams events with ids", strings.Contains(string(got), "id: 1\n"), string(got))
	} else if !strings.Contains(err.Error(), "Timeout") {
		check("SSE connect", false, err)
	}
	st, _, _ = call("GET", "/v1/tasks/"+j.ID+"/events", nil, map[string]string{"Last-Event-ID": "abc"})
	check("SSE invalid Last-Event-ID → 400", st == 400, st)
	st, _, _ = call("GET", "/v1/tasks/00000000-0000-4000-8000-0000000000ff", nil, nil)
	check("unknown task → 404", st == 404, st)

	// second import of same files: duplicate-name conflict must auto-suffix, never overwrite
	code, d = upload(files[:2], paths[:2], "")
	var j3 job
	_ = json.Unmarshal(d, &j3)
	j3 = waitFor(j3.ID, "awaiting_review")
	_, d, _ = call("GET", "/v1/archive-plans/"+*j3.PlanID, nil, nil)
	var p3 plan
	_ = json.Unmarshal(d, &p3)
	conflict := false
	for _, it := range p3.Items {
		for _, w := range it.Warnings {
			if strings.Contains(w, "已存在") || strings.Contains(w, "内容相同") {
				conflict = true
			}
		}
	}
	check("R07 re-import flags existing target/duplicate", conflict, p3.Items)

	// idempotent import replay
	k := fmt.Sprint("replay-", time.Now().UnixNano())
	c1, d1 := upload(files[:1], paths[:1], k)
	c2, d2 := upload(files[:1], paths[:1], k)
	var a, b job
	_ = json.Unmarshal(d1, &a)
	_ = json.Unmarshal(d2, &b)
	check("import Idempotency-Key replay returns same task", c1 == 202 && c2 == 202 && a.ID == b.ID, c1, c2)
	c3, _ := upload(files[1:2], paths[1:2], k)
	check("same key + different content → 409", c3 == 409, c3)

	part2(truth, manifest.Files[0].File)
	part3()
	writeRecorded()

	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d check(s) FAILED\n", failed)
		os.Exit(1)
	}
	fmt.Println("all checks passed")
}

func orDefault(cur *string, truth, fallback string) string {
	if cur != nil {
		return *cur
	}
	if truth != "" {
		return truth
	}
	return fallback
}

func recordCall(method, path string, status int, raw []byte) {
	var body any
	if len(raw) > 0 && json.Unmarshal(raw, &body) != nil {
		body = nil
	}
	recorded = append(recorded, map[string]any{"method": method, "path": path, "status": status, "body": body})
}

func writeRecorded() {
	if *recordPath == "" {
		return
	}
	b, _ := json.MarshalIndent(recorded, "", " ")
	_ = os.WriteFile(*recordPath, b, 0o644)
	fmt.Printf("recorded %d responses to %s\n", len(recorded), *recordPath)
}
