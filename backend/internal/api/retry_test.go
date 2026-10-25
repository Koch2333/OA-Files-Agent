package api

import (
	"context"
	"testing"
)

// Retry is operator-only, answers a second click with the current state (no error) and only re-queues failed work.
func TestRetryTaskIsIdempotentAndRoleChecked(t *testing.T) {
	e := newAIEnv(t)
	r := e.importMP(e.op, basic(""), nil)
	if r.Code != 201 && r.Code != 202 && r.Code != 200 {
		t.Fatalf("import %d %s", r.Code, r.Raw)
	}
	id, _ := r.Data["id"].(string)
	if id == "" {
		t.Fatalf("no id: %s", r.Raw)
	}
	ctx := context.Background()
	if _, err := e.st.Pool.Exec(ctx, `UPDATE import_jobs SET status='failed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := e.viewer.do("POST", "/v1/tasks/"+id+"/retry", nil); got.Code != 403 {
		t.Fatalf("viewer: %d", got.Code)
	}
	first := e.op.must("POST", "/v1/tasks/"+id+"/retry", nil, 200)
	if first.Data["status"] != "queued" {
		t.Fatalf("first: %v", first.Data)
	}
	second := e.op.must("POST", "/v1/tasks/"+id+"/retry", nil, 200) // double click: current state, not 409
	if second.Data["status"] != "queued" {
		t.Fatalf("second: %v", second.Data)
	}
	// a finished task that has nothing failed is still a conflict
	if _, err := e.st.Pool.Exec(ctx, `UPDATE import_jobs SET status='completed' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := e.op.do("POST", "/v1/tasks/"+id+"/retry", nil); got.Code != 409 {
		t.Fatalf("completed: %d", got.Code)
	}
}
