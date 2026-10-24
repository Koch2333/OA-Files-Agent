package api

import (
	"context"
	"errors"
	"net/http"

	"oa-agent/backend/internal/dbadmin"
	"oa-agent/backend/internal/sqldb"
)

// DatabaseAdmin is the admin workflow behind /v1/admin/database* (implemented by dbadmin.Service).
type DatabaseAdmin interface {
	Status(ctx context.Context) (dbadmin.StatusView, error)
	Test(ctx context.Context, r dbadmin.ConnRequest, actor string) (dbadmin.TestResult, error)
	Validate(ctx context.Context, r dbadmin.ConnRequest, actor string) (dbadmin.Report, error)
	SavePending(ctx context.Context, r dbadmin.ConnRequest, confirm bool, actor string) (dbadmin.StatusView, error)
	DiscardPending(ctx context.Context, actor string) error
	PlanInit(ctx context.Context, target, actor string) (dbadmin.InitPreview, error)
	Initialize(ctx context.Context, target, token, actor string) (dbadmin.InitPreview, error)
}

func (s *Server) databaseRoutes() {
	s.handle("/v1/admin/database", map[string]http.HandlerFunc{"GET": s.dbStatus, "PUT": s.dbSave})
	s.handle("/v1/admin/database/pending", map[string]http.HandlerFunc{"DELETE": s.dbDiscard})
	s.handle("/v1/admin/database/test", map[string]http.HandlerFunc{"POST": s.dbTest})
	s.handle("/v1/admin/database/validate-permissions", map[string]http.HandlerFunc{"POST": s.dbValidate})
	s.handle("/v1/admin/database/initialize", map[string]http.HandlerFunc{"POST": s.dbInitialize})
}

func (s *Server) dbFail(w http.ResponseWriter, r *http.Request, err error) {
	var ve *dbadmin.ValidationError
	switch {
	case errors.As(err, &ve):
		s.fail(w, r, 422, "VALIDATION_FAILED", "目标数据库未通过校验："+ve.Error(), map[string]any{"checks": ve.Report.Checks, "isolation": ve.Report.Isolation})
	case errors.Is(err, dbadmin.ErrReadOnly):
		s.fail(w, r, 409, "CONFIG_READ_ONLY", err.Error(), nil)
	case errors.Is(err, dbadmin.ErrInvalid):
		s.fail(w, r, 400, "INVALID_ARGUMENT", sqldb.RedactDSN(err.Error()), nil)
	case errors.Is(err, dbadmin.ErrNoPending):
		s.fail(w, r, 404, "NOT_FOUND", "没有待生效的数据库配置", nil)
	case errors.Is(err, dbadmin.ErrBadToken):
		s.fail(w, r, 409, "CONFIRMATION_REQUIRED", "确认令牌无效或已过期：请重新预览 DDL 后再确认", nil)
	case errors.Is(err, dbadmin.ErrNotValidated):
		s.fail(w, r, 422, "VALIDATION_FAILED", sqldb.RedactDSN(err.Error()), nil)
	default:
		s.internal(w, r, "数据库管理操作失败", err)
	}
}

func (s *Server) dbStatus(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.DBAdmin.Status(r.Context())
	if err != nil {
		s.dbFail(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) dbTest(w http.ResponseWriter, r *http.Request) {
	var in dbadmin.ConnRequest
	if !s.decode(w, r, &in) {
		return
	}
	actor, _ := s.actor(r)
	res, err := s.d.DBAdmin.Test(r.Context(), in, actor)
	if err != nil {
		s.dbFail(w, r, err)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) dbValidate(w http.ResponseWriter, r *http.Request) {
	var in dbadmin.ConnRequest
	if !s.decode(w, r, &in) {
		return
	}
	actor, _ := s.actor(r)
	rep, err := s.d.DBAdmin.Validate(r.Context(), in, actor)
	if err != nil {
		s.dbFail(w, r, err)
		return
	}
	s.ok(w, 200, rep)
}

func (s *Server) dbSave(w http.ResponseWriter, r *http.Request) {
	var in struct {
		dbadmin.ConnRequest
		ConfirmSwitch bool `json:"confirm_switch"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !s.fresh(w, r) {
		return
	}
	actor, _ := s.actor(r)
	v, err := s.d.DBAdmin.SavePending(r.Context(), in.ConnRequest, in.ConfirmSwitch, actor)
	if err != nil {
		s.dbFail(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) dbDiscard(w http.ResponseWriter, r *http.Request) {
	if !s.fresh(w, r) {
		return
	}
	actor, _ := s.actor(r)
	if err := s.d.DBAdmin.DiscardPending(r.Context(), actor); err != nil {
		s.dbFail(w, r, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) dbInitialize(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Target            string `json:"target"`
		DryRun            bool   `json:"dry_run"`
		ConfirmationToken string `json:"confirmation_token"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if in.Target != "" && in.Target != "pending" && in.Target != "active" {
		s.fail(w, r, 400, "INVALID_ARGUMENT", "target 取值无效", nil)
		return
	}
	actor, _ := s.actor(r)
	if in.DryRun || in.ConfirmationToken == "" {
		pv, err := s.d.DBAdmin.PlanInit(r.Context(), in.Target, actor)
		if err != nil {
			s.dbFail(w, r, err)
			return
		}
		s.ok(w, 200, pv)
		return
	}
	if !s.fresh(w, r) {
		return
	}
	pv, err := s.d.DBAdmin.Initialize(r.Context(), in.Target, in.ConfirmationToken, actor)
	if err != nil {
		s.dbFail(w, r, err)
		return
	}
	s.ok(w, 200, pv)
}
