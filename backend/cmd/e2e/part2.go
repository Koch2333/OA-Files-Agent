package main

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// part2 covers custom OA templates (R03), single-file correction, rollback, cancel and retry.
func part2(truth map[string]struct{ d, t, dt string }, first string) {
	println("--- part 2: templates, correction, rollback, cancel/retry ---")
	sample := filepath.Join(*dir, "财务部_2026年3月报销申请.docx")

	st, ec, _ := call("POST", "/v1/oa-templates", map[string]any{"name": "空模板", "match_rules": map[string]any{}, "field_mappings": map[string]any{}}, nil)
	check("R03 template without any keyword rejected (422)", st == 422, st, string(ec))
	st, ec, _ = call("POST", "/v1/oa-templates", map[string]any{"name": "x", "match_rules": map[string]any{"filename_keywords": []string{"a"}}, "field_mappings": map[string]any{"date_source": "bogus"}}, nil)
	check("R03 invalid date_source rejected (422)", st == 422, st, string(ec))
	st, d, _ := call("POST", "/v1/oa-templates", map[string]any{
		"name": "E2E 报销模板", "priority": 5, "enabled": true,
		"match_rules":    map[string]any{"filename_keywords": []string{"报销"}, "text_keywords": []string{"费用报销申请单"}},
		"field_mappings": map[string]any{"department": "财务部", "document_type": "申请", "date_source": "ocr_text"},
	}, nil)
	check("R03 template created (201)", st == 201, st, string(d))
	var tpl struct {
		ID      string `json:"id"`
		Enabled bool   `json:"enabled"`
	}
	_ = json.Unmarshal(d, &tpl)
	st, d, _ = call("PATCH", "/v1/oa-templates/"+tpl.ID, map[string]any{"enabled": false}, nil)
	check("R03 template disable (PATCH)", st == 200 && strings.Contains(string(d), `"enabled":false`), st, string(d))
	st, _, _ = call("PATCH", "/v1/oa-templates/"+tpl.ID, map[string]any{"enabled": true}, nil)
	check("R03 template re-enable", st == 200, st)

	test := func(path string) (int, struct {
		Matched    bool    `json:"matched"`
		Department *string `json:"department"`
	}) {
		var out struct {
			Matched    bool    `json:"matched"`
			Department *string `json:"department"`
		}
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", filepath.Base(path))
		b, _ := os.ReadFile(path)
		fw.Write(b)
		mw.Close()
		req, _ := http.NewRequest("POST", *base+"/v1/oa-templates/"+tpl.ID+"/test", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, out
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		recordCall("POST", "/v1/oa-templates/"+tpl.ID+"/test", res.StatusCode, raw)
		var env envelope
		_ = json.Unmarshal(raw, &env)
		_ = json.Unmarshal(env.Data, &out)
		return res.StatusCode, out
	}
	code, r := test(sample)
	check("R03 template test: matching sample hits", code == 200 && r.Matched && r.Department != nil && *r.Department == "财务部", code, r)
	code, r = test(filepath.Join(*dir, "采购部_2026年Q1设备采购报表.xlsx"))
	check("R03 template test: non-matching sample misses", code == 200 && !r.Matched, code, r)

	// import with the template applied
	queryExtra = "?template_id=" + tpl.ID
	c, d := upload([]string{sample}, []string{"模板/财务部_2026年3月报销申请.docx"}, "")
	queryExtra = ""
	check("R03 import with template_id accepted", c == 202, c, string(d))
	var j job
	_ = json.Unmarshal(d, &j)
	j = waitFor(j.ID, "awaiting_review")
	_, d, _ = call("GET", "/v1/files?task_id="+j.ID, nil, nil)
	var fl struct {
		Items []struct {
			ID       string   `json:"id"`
			Evidence []string `json:"evidence"`
			Dept     *string  `json:"detected_department"`
		}
	}
	_ = json.Unmarshal(d, &fl)
	usedTpl := false
	if len(fl.Items) == 1 {
		for _, e := range fl.Items[0].Evidence {
			if strings.Contains(e, "模板") {
				usedTpl = true
			}
		}
	}
	check("R03 recognition evidence cites the template", usedTpl, fl.Items)
	c, _ = upload([]string{sample}, []string{"x.docx"}, "")
	queryExtra = "?template_id=00000000-0000-4000-8000-000000000999"
	c, _ = upload([]string{sample}, []string{"x.docx"}, "")
	queryExtra = ""
	check("import with unknown template_id rejected (400)", c == 400, c)

	// correction (single file, confirm-before-apply)
	if len(fl.Items) == 1 {
		fid := fl.Items[0].ID
		st, d, _ = call("POST", "/v1/files/"+fid+"/correction-proposals", map[string]any{"instruction": "这个文件看不明白"}, nil)
		check("correction: un-understandable instruction → 422", st == 422, st, string(d))
		st, d, _ = call("POST", "/v1/files/"+fid+"/correction-proposals", map[string]any{"instruction": "应属人事部，类型是通知，日期 2026-05-06"}, nil)
		var cp struct {
			ID      string
			Changes []struct{ Field string }
			Impact  struct {
				Scope       string `json:"scope"`
				RuleChanges bool   `json:"rule_changes"`
			}
		}
		_ = json.Unmarshal(d, &cp)
		check("correction proposal lists before/after (201), single-file, no rule change", st == 201 && len(cp.Changes) == 3 && cp.Impact.Scope == "single_file" && !cp.Impact.RuleChanges, st, string(d))
		_, d, _ = call("GET", "/v1/files/"+fid, nil, nil)
		check("correction not applied before approval", strings.Contains(string(d), `"detected_department":"财务部"`), string(d))
		st, d, _ = call("POST", "/v1/correction-proposals/"+cp.ID+"/approve", nil, nil)
		check("correction approve (200)", st == 200, st, string(d))
		_, d, _ = call("GET", "/v1/files/"+fid, nil, nil)
		check("correction applied to the file", strings.Contains(string(d), `"detected_department":"人事部"`) && strings.Contains(string(d), `"detected_date":"2026-05-06"`), string(d))
		_, d, _ = call("GET", "/v1/archive-plans/"+*j.PlanID, nil, nil)
		var p plan
		_ = json.Unmarshal(d, &p)
		check("correction mirrored into pending plan with new path", len(p.Items) == 1 && strings.HasPrefix(p.Items[0].DestinationPath, "人事部/2026/通知/"), p.Items)
		st, _, _ = call("POST", "/v1/correction-proposals/"+cp.ID+"/approve", nil, nil)
		check("correction cannot be applied twice (409)", st == 409, st)

		// approve + execute + rollback
		st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/approve", map[string]any{"version": p.Version}, nil)
		check("approve corrected plan", st == 200, st, string(d))
		_ = json.Unmarshal(d, &p)
		st, d, _ = call("POST", "/v1/archive-plans/"+p.ID+"/execute", nil, map[string]string{"Idempotency-Key": "part2-exec"})
		var done struct {
			OperationID *string `json:"operation_id"`
			Items       []item
		}
		_ = json.Unmarshal(d, &done)
		check("execute corrected plan; operation_id exposed", st == 200 && done.OperationID != nil, st, string(d))
		if done.OperationID != nil && len(done.Items) == 1 {
			full := ""
			if *archiveDir != "" {
				full = filepath.Join(*archiveDir, filepath.FromSlash(done.Items[0].DestinationPath))
				_, err := os.Stat(full)
				check("R07 archived file exists on disk at planned path", err == nil, full, err)
			}
			st, d, _ = call("POST", "/v1/archive-operations/"+*done.OperationID+"/rollback", nil, nil)
			check("rollback (200)", st == 200, st, string(d))
			if full != "" {
				_, err := os.Stat(full)
				check("rollback removed the published copy", os.IsNotExist(err), err)
			}
			_, d, _ = call("GET", "/v1/ledger?q="+"2026-05-06", nil, nil)
			check("rolled-back entry no longer in ledger", !strings.Contains(string(d), "人事部/2026/通知/2026-05-06"), string(d))
			st, _, _ = call("POST", "/v1/archive-operations/"+*done.OperationID+"/rollback", nil, nil)
			check("second rollback refused (409)", st == 409, st)
		}
	}

	// cancel / retry
	c, d = upload([]string{filepath.Join(*dir, manifestFirst(first))}, []string{"c/" + first}, "")
	var cj job
	_ = json.Unmarshal(d, &cj)
	st, _, _ = call("POST", "/v1/tasks/"+cj.ID+"/cancel", nil, nil)
	check("cancel running/queued task (200)", st == 200, st)
	cj = waitFor(cj.ID, "cancelled")
	check("task ends cancelled", cj.Status == "cancelled", cj.Status)
	st, _, _ = call("POST", "/v1/tasks/"+cj.ID+"/retry", nil, nil)
	check("retry cancelled task (200)", st == 200, st)
	cj = waitFor(cj.ID, "awaiting_review")
	check("retried task reaches awaiting_review", cj.Status == "awaiting_review", cj.Status)
	st, _, _ = call("POST", "/v1/tasks/"+cj.ID+"/retry", nil, nil)
	check("retry of non-failed task refused (409)", st == 409, st)
}

func manifestFirst(f string) string { return f }

// part3 exercises the read/admin endpoints and validation errors so every documented response is checked.
func part3() {
	println("--- part 3: admin endpoints, validation, template delete ---")
	st, d, _ := call("GET", "/v1/providers", nil, nil)
	check("providers lists a provider without secrets", st == 200 && strings.Contains(string(d), "state") && !strings.Contains(strings.ToLower(string(d)), "key"), st, string(d))
	st, d, _ = call("GET", "/v1/audit-logs?page_size=5", nil, nil)
	check("audit logs recorded (200)", st == 200 && strings.Contains(string(d), "plan.execute"), st, string(d))
	st, d, _ = call("GET", "/v1/archive-rules", nil, nil)
	check("default archive rule present", st == 200 && strings.Contains(string(d), "{department}/{year}/{type}"), st, string(d))
	st, d, _ = call("POST", "/v1/archive-rules", map[string]any{"name": "按月", "naming_pattern": "{year}{month}{day}_{original}", "directory_pattern": "{year}/{month}/{department}", "enabled": false}, nil)
	check("create valid archive rule (201)", st == 201, st, string(d))
	st, _, _ = call("POST", "/v1/archive-rules", map[string]any{"name": "坏", "naming_pattern": "{bogus}_{original}", "directory_pattern": "{department}"}, nil)
	check("rule with unknown placeholder rejected (422)", st == 422, st)
	st, _, _ = call("POST", "/v1/archive-rules", map[string]any{"name": "穿越", "naming_pattern": "{original}", "directory_pattern": "../{department}"}, nil)
	check("rule with path traversal rejected (422)", st == 422, st)
	st, _, _ = call("POST", "/v1/archive-rules", map[string]any{"name": "x", "unknown_field": 1}, nil)
	check("unknown JSON field rejected (400)", st == 400, st)

	st, d, _ = call("GET", "/v1/oa-templates", nil, nil)
	var ts []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var env envelope
	_ = json.Unmarshal([]byte(`{"data":`+string(d)+`}`), &env)
	_ = json.Unmarshal(d, &ts)
	check("list templates (200)", st == 200 && len(ts) > 0, st, string(d))
	if len(ts) > 0 {
		st, _, _ = call("GET", "/v1/oa-templates/"+ts[0].ID, nil, nil)
		check("get template (200)", st == 200, st)
		st, _, _ = call("DELETE", "/v1/oa-templates/"+ts[0].ID, nil, nil)
		check("delete template (204)", st == 204, st)
		st, _, _ = call("GET", "/v1/oa-templates/"+ts[0].ID, nil, nil)
		check("deleted template → 404", st == 404, st)
		st, _, _ = call("DELETE", "/v1/oa-templates/"+ts[0].ID, nil, nil)
		check("delete again → 404", st == 404, st)
		st, _, _ = call("PATCH", "/v1/oa-templates/"+ts[0].ID, map[string]any{"enabled": true}, nil)
		check("patch deleted template → 404", st == 404, st)
	}
	st, _, _ = call("PATCH", "/v1/oa-templates/not-a-uuid", map[string]any{"enabled": true}, nil)
	check("malformed id → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/oa-templates", map[string]any{"bogus": 1}, nil)
	check("template unknown field → 400", st == 400, st)

	st, _, _ = call("GET", "/v1/tasks?status=bogus", nil, nil)
	check("tasks bad status → 400", st == 400, st)
	st, _, _ = call("GET", "/v1/tasks?page_size=2&page=1", nil, nil)
	check("tasks pagination (200)", st == 200, st)
	st, _, _ = call("GET", "/v1/files?date_from=2026-13-40", nil, nil)
	check("files bad date → 400", st == 400, st)
	st, _, _ = call("GET", "/v1/files/00000000-0000-4000-8000-0000000000aa", nil, nil)
	check("unknown file → 404", st == 404, st)
	st, _, _ = call("GET", "/v1/archive-plans/00000000-0000-4000-8000-0000000000aa", nil, nil)
	check("unknown plan → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/archive-plans/00000000-0000-4000-8000-0000000000aa/approve", map[string]any{"version": 1}, nil)
	check("approve unknown plan → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/archive-plans/00000000-0000-4000-8000-0000000000aa/approve", map[string]any{}, nil)
	check("approve without version → 400", st == 400, st)
	st, _, _ = call("POST", "/v1/archive-plans/00000000-0000-4000-8000-0000000000aa/execute", nil, map[string]string{"Idempotency-Key": "z"})
	check("execute unknown plan → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/archive-operations/00000000-0000-4000-8000-0000000000aa/rollback", nil, nil)
	check("rollback unknown op → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/files/00000000-0000-4000-8000-0000000000aa/correction-proposals", map[string]any{"instruction": "改成财务部"}, nil)
	check("correction on unknown file → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/files/00000000-0000-4000-8000-0000000000aa/correction-proposals", map[string]any{"instruction": ""}, nil)
	check("correction empty instruction → 400", st == 400, st)
	st, _, _ = call("POST", "/v1/correction-proposals/00000000-0000-4000-8000-0000000000aa/approve", nil, nil)
	check("approve unknown correction → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/tasks/00000000-0000-4000-8000-0000000000aa/cancel", nil, nil)
	check("cancel unknown task → 404", st == 404, st)
	st, _, _ = call("POST", "/v1/tasks/00000000-0000-4000-8000-0000000000aa/retry", nil, nil)
	check("retry unknown task → 404", st == 404, st)
	st, _, _ = call("GET", "/v1/tasks/00000000-0000-4000-8000-0000000000aa/events", nil, nil)
	check("events unknown task → 404", st == 404, st)
	// completed task cannot be cancelled
	_, d, _ = call("GET", "/v1/tasks?status=partial_failure&page_size=1", nil, nil)
	var tp struct{ Items []job }
	_ = json.Unmarshal(d, &tp)
	if len(tp.Items) > 0 {
		st, _, _ = call("POST", "/v1/tasks/"+tp.Items[0].ID+"/cancel", nil, nil)
		check("cancel finished task → 409", st == 409, st)
	}
	var big bytes.Buffer
	mw := multipart.NewWriter(&big)
	_ = mw.WriteField("paths", "[]")
	mw.Close()
	req, _ := http.NewRequest("POST", *base+"/v1/imports", &big)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if res, err := http.DefaultClient.Do(req); err == nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		recordCall("POST", "/v1/imports", res.StatusCode, raw)
		check("import with no files → 400", res.StatusCode == 400, res.StatusCode)
	}
	// 101 files → 413
	var many bytes.Buffer
	mw = multipart.NewWriter(&many)
	var ps []string
	for i := 0; i < 101; i++ {
		ps = append(ps, "f/a"+string(rune('a'+i%26))+strings.Repeat("x", i/26)+".pdf")
	}
	pj, _ := json.Marshal(ps)
	_ = mw.WriteField("paths", string(pj))
	for _, p := range ps {
		fw, _ := mw.CreateFormFile("files", filepath.Base(p))
		fw.Write([]byte("x"))
	}
	mw.Close()
	req, _ = http.NewRequest("POST", *base+"/v1/imports", &many)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if res, err := http.DefaultClient.Do(req); err == nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		recordCall("POST", "/v1/imports", res.StatusCode, raw)
		check("import of 101 files → 413", res.StatusCode == 413, res.StatusCode)
	}
	// path traversal in upload paths manifest
	var trav bytes.Buffer
	mw = multipart.NewWriter(&trav)
	_ = mw.WriteField("paths", `["../../evil.docx"]`)
	fw, _ := mw.CreateFormFile("files", "evil.docx")
	fw.Write([]byte("x"))
	mw.Close()
	req, _ = http.NewRequest("POST", *base+"/v1/imports", &trav)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if res, err := http.DefaultClient.Do(req); err == nil {
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		recordCall("POST", "/v1/imports", res.StatusCode, raw)
		check("upload path traversal rejected (400)", res.StatusCode == 400, res.StatusCode)
	}
}
