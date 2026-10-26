package api

import (
	"context"
	"strings"
	"testing"

	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/store"
)

func TestClassificationOptionsMergeBuiltinLedgerAndPlan(t *testing.T) {
	e := newAIEnv(t)
	ctx := context.Background()
	b := imports.Batch{Job: imports.Job{ID: newUUID(t), Status: "queued", TotalFiles: 1, CreatedAt: nowUTC(), UpdatedAt: nowUTC()}, Files: []imports.File{}, Failures: []imports.Failure{}}
	fid := newUUID(t)
	b.Files = append(b.Files, imports.File{ID: fid, TaskID: b.Job.ID, SourceName: "a.pdf", SourcePath: "a.pdf", MIMEType: "application/pdf", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SizeBytes: 3, Evidence: []string{}, ArchiveStatus: "pending", CreatedAt: nowUTC(), StoredPath: "/spool/a.pdf"})
	if _, _, err := e.st.Save(ctx, b); err != nil {
		t.Fatal(err)
	}
	dep, typ, date := "法务部", "函件", "2026-03-15"
	if _, err := e.st.CreatePlan(ctx, b.Job.ID, nil, []store.PlanItem{{FileID: fid, OriginalName: "a.pdf", SuggestedName: "a.pdf", SourcePath: "a.pdf", DestinationPath: "x/a.pdf", Department: &dep, DocumentType: &typ, DocumentDate: &date, Warnings: []string{}}}); err != nil {
		t.Fatal(err)
	}
	vals := func(path string, key string) []string {
		r := e.viewer.must("GET", path, nil, 200)
		var out []string
		for _, o := range r.Data[key].([]any) {
			out = append(out, o.(map[string]any)["value"].(string))
		}
		return out
	}
	d := vals("/v1/classification/options", "departments")
	if d[0] != "财务部" || d[len(d)-1] != "其他" {
		t.Fatalf("built-in first, 其他 last: %v", d)
	}
	for _, v := range d {
		if v == "法务部" {
			t.Fatalf("plan values only with task_id: %v", d)
		}
	}
	d = vals("/v1/classification/options?task_id="+b.Job.ID, "departments")
	if d[len(d)-2] != "法务部" || d[len(d)-1] != "其他" {
		t.Fatalf("plan value expected before 其他: %v", d)
	}
	if tp := vals("/v1/classification/options?task_id="+b.Job.ID, "types"); tp[len(tp)-2] != "函件" {
		t.Fatalf("types: %v", tp)
	}
	assertCode(t, e.viewer.do("GET", "/v1/classification/options?task_id=nope", nil), 400, "INVALID_ARGUMENT")
}

func TestPlanPatchValidatesCategoryValues(t *testing.T) {
	e := newAIEnv(t)
	body := func(dep string) map[string]any {
		return map[string]any{"version": 1, "items": []any{map[string]any{"file_id": newUUID(t), "department": dep}}}
	}
	for _, bad := range []string{"a/b", "x:y", "..", strings.Repeat("好", 61)} {
		assertCode(t, e.op.do("PATCH", "/v1/archive-plans/"+newUUID(t), body(bad)), 422, "INVALID_ARGUMENT")
	}
}
