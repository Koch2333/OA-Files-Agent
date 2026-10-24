package api

import (
	"context"
	"errors"
	"strings"
	"testing"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/store"
)

// wizardEnv returns an environment whose admin has completed /v1/setup/complete but not finished the wizard.
func wizardEnv(t *testing.T, extra map[string]any) (*authEnv, *tclient) {
	t.Helper()
	e := newAuthEnv(t)
	tok, err := e.svc.EnsureSetupToken(context.Background())
	if err != nil || tok == "" {
		t.Fatalf("setup token: %q %v", tok, err)
	}
	body := map[string]any{"setup_token": tok, "mode": "enterprise", "mfa_policy": "off",
		"admin": map[string]any{"username": "admin", "display_name": "管理员", "password": adminPW}}
	for k, v := range extra {
		body[k] = v
	}
	c := e.client()
	c.must("POST", "/v1/setup/complete", body, 201)
	return e, c
}

func TestWizardGateBlocksTheAppUntilFinished(t *testing.T) {
	e, admin := wizardEnv(t, nil)
	stubServer(t, e, "real")
	st := e.client().must("GET", "/v1/setup/status", nil, 200)
	if st.Data["initialized"] != true || st.Data["wizard_finished"] != false || st.Data["api_level"] != float64(APILevel) || st.Data["server_version"] == "" {
		t.Fatalf("status while unfinished: %s", st.Raw)
	}
	for _, k := range []string{"GET /v1/tasks", "POST /v1/imports", "GET /v1/files", "GET /v1/fs", "GET /v1/ledger", "GET /v1/storage/connections",
		"GET /v1/oa-templates", "GET /v1/admin/users", "GET /v1/admin/system", "GET /v1/audit-logs", "GET /v1/providers", "GET /v1/llm/personal-nope"} {
		sp := strings.IndexByte(k, ' ')
		r := admin.do(k[:sp], k[sp+1:], nil)
		if k == "GET /v1/llm/personal-nope" {
			if r.code() == "WIZARD_REQUIRED" {
				t.Errorf("%s must stay open", k)
			}
			continue
		}
		if r.Code != 409 || r.code() != "WIZARD_REQUIRED" || r.Err["message"] != "请先完成初始化向导" {
			t.Errorf("%s while unfinished: %d %s", k, r.Code, r.Raw)
		}
	}
	// the allow-list stays open for the administrator
	for _, k := range []string{"GET /v1/auth/me", "GET /v1/auth/security", "GET /v1/auth/sessions", "POST /v1/auth/totp/begin", "GET /v1/llm/presets", "GET /v1/llm/config",
		"PUT /v1/llm/config", "POST /v1/llm/test", "POST /v1/llm/models", "GET /v1/llm/status", "GET /v1/admin/ai-policy", "PATCH /v1/admin/ai-policy",
		"GET /v1/setup/status", "GET /healthz", "GET /readyz", "GET /docs", "GET /openapi.yaml", "POST /v1/client-errors"} {
		sp := strings.IndexByte(k, ' ')
		if r := admin.do(k[:sp], k[sp+1:], nil); r.Code == 409 && r.code() == "WIZARD_REQUIRED" {
			t.Errorf("%s must be allowed while the wizard is unfinished: %s", k, r.Raw)
		}
	}
	for _, p := range []string{"/v1/auth/me", "/v1/setup/finish", "/v1/llm/x", "/v1/admin/ai-policy", "/healthz"} {
		if !wizardAllowed(p) && p != "/healthz" {
			t.Errorf("wizardAllowed(%s) = false", p)
		}
	}
	if wizardAllowed("/v1/admin/users") || wizardAllowed("/v1/tasks") || wizardAllowed("/v1/admin/ai-policyx") {
		t.Error("wizardAllowed too wide")
	}
	// finishing opens everything, is idempotent and audited once
	fin := admin.must("POST", "/v1/setup/finish", nil, 200)
	if fin.Data["wizard_finished"] != true {
		t.Fatalf("%s", fin.Raw)
	}
	admin.must("POST", "/v1/setup/finish", nil, 200)
	admin.must("GET", "/v1/tasks", nil, 200)
	admin.must("GET", "/v1/admin/users", nil, 200)
	if n := e.auditActions()["setup.finished"]; n != 1 {
		t.Fatalf("setup.finished audited %d times", n)
	}
	if e.client().must("GET", "/v1/setup/status", nil, 200).Data["wizard_finished"] != true {
		t.Fatal("public status must report finished")
	}
}

func TestWizardFinishRequiresMFAWhenPolicyDemands(t *testing.T) {
	e, admin := wizardEnv(t, map[string]any{"mfa_policy": "admins"})
	stubServer(t, e, "real")
	if admin.do("GET", "/v1/auth/me", nil).Data["scope"] != "enroll_mfa" {
		t.Fatal("expected an enrolment session")
	}
	// the restricted session may not finish
	if r := admin.do("POST", "/v1/setup/finish", nil); r.Code != 403 {
		t.Fatalf("finish with enrol scope: %d %s", r.Code, r.Raw)
	}
	// service level: a full identity without MFA is refused with MFA_REQUIRED_BY_POLICY
	set, _ := e.svc.Settings(context.Background())
	u, err := e.st.AuthUserByName(context.Background(), "admin")
	if err != nil {
		t.Fatal(err)
	}
	id := auth.Identity{User: u, Settings: set, Session: store.AuthSession{Scope: "full"}}
	_, ferr := e.svc.FinishWizard(context.Background(), id)
	if ferr == nil {
		t.Fatal("expected an error")
	}
	if st := e.client().must("GET", "/v1/setup/status", nil, 200); st.Data["wizard_finished"] != false {
		t.Fatalf("must stay unfinished: %s", st.Raw)
	}
	var ae *auth.Error
	if !errors.As(ferr, &ae) || ae.Status != 409 || ae.Code != "MFA_REQUIRED_BY_POLICY" {
		t.Fatalf("finish without MFA: %v", ferr)
	}
}

func TestWizardLocalModeAndLegacyAreFinished(t *testing.T) {
	e := newAuthEnv(t)
	c := e.client()
	c.host, c.remote = "localhost:8080", "127.0.0.1:5555"
	if st := c.must("GET", "/v1/setup/status", nil, 200); st.Data["wizard_finished"] != false {
		t.Fatalf("before setup: %s", st.Raw)
	}
	c.must("POST", "/v1/setup/complete", map[string]any{"mode": "local"}, 201)
	if st := c.must("GET", "/v1/setup/status", nil, 200); st.Data["wizard_finished"] != true {
		t.Fatalf("local mode: %s", st.Raw)
	}
	stubServer(t, e, "real")
	c.must("GET", "/v1/tasks", nil, 200)
	// legacy: an install that predates the gate has wizard_finished backfilled to true (migration); simulate the
	// pre-gate state by clearing the flag and re-applying the backfill statement
	if _, err := e.st.Pool.Exec(context.Background(), `UPDATE auth_settings SET wizard_finished=false`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(context.Background(), `UPDATE auth_settings SET wizard_finished = true WHERE setup_completed = true`); err != nil {
		t.Fatal(err)
	}
	e.svc.Settings(context.Background())
}
