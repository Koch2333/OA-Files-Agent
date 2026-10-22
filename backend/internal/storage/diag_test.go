package storage

import (
	"context"
	"strings"
	"testing"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/diag"
)

func TestPermanentReplicationFailureIsReported(t *testing.T) {
	s, st, _, arch := newSvc(t)
	r := diag.New(diag.Options{FlushEvery: 20 * time.Millisecond, RatePerSec: 100000, Logf: func(string, ...any) {}})
	r.Attach(st.Pool)
	old := diag.SetDefault(r)
	defer func() { diag.SetDefault(old); r.Close() }()
	w := newWorld("wdiag")
	ctx := context.Background()
	seedArchive(t, st, arch, "a/one.pdf")
	v := create(t, s, "A", "wdiag", func(in *Input) { in.Replicate = "manual" })
	s.Replicate(ctx, v.ID, "", "t")
	// transient failures are not reported until they become permanent
	w.putErr = cloud.ErrUnreachable
	c, _ := st.ClaimReplication(ctx)
	s.replicateOne(ctx, c)
	diag.Flush(5 * time.Second)
	if _, total, _ := diag.List(ctx, st.Pool, diag.Filter{}); total != 0 {
		t.Fatalf("a retry that will happen automatically is not an error report: %d", total)
	}
	st.Pool.Exec(ctx, `UPDATE replications SET next_attempt_at=now()`)
	w.putErr = cloud.ErrPermission
	c, _ = st.ClaimReplication(ctx)
	s.replicateOne(ctx, c)
	diag.Flush(5 * time.Second)
	list, total, _ := diag.List(ctx, st.Pool, diag.Filter{Source: "storage"})
	if total != 1 || list[0].Code != "REPLICATION_FAILED" {
		t.Fatalf("%d %+v", total, list)
	}
	d, _ := diag.Get(ctx, st.Pool, list[0].ID)
	if d.Details["replication_id"] != c.ID || d.Details["connection_id"] != v.ID || !strings.Contains(d.Message, "权限不足") {
		t.Fatalf("%+v", d)
	}
}
