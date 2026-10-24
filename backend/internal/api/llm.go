package api

import (
	"context"
	"errors"
	"net/http"

	"oa-agent/backend/internal/llm"
)

// LLMManager is the recognition-engine configuration backend (implemented by llm.Manager).
type LLMManager interface {
	AIManager
	Presets() []llm.Preset
	Config() llm.ConfigView
	Save(ctx context.Context, in llm.Input) (llm.ConfigView, error)
	Test(ctx context.Context, in llm.TestInput) (llm.TestResult, error)
	Models(ctx context.Context, baseURL, apiKey string) ([]string, error)
	OAuthLLM
}

func (s *Server) llmRoutes() {
	s.handle("/v1/llm/presets", map[string]http.HandlerFunc{"GET": s.llmPresets})
	s.handle("/v1/llm/config", map[string]http.HandlerFunc{"GET": s.llmGet, "PUT": s.llmPut})
	s.handle("/v1/llm/test", map[string]http.HandlerFunc{"POST": s.llmTest})
	s.handle("/v1/llm/models", map[string]http.HandlerFunc{"POST": s.llmModels})
	s.aiRoutes()
	s.llmOAuthRoutes()
}

func (s *Server) llmErr(w http.ResponseWriter, r *http.Request, err error) {
	var ve *llm.ValidationError
	var ue *llm.UpstreamError
	switch {
	case errors.As(err, &ve):
		s.fail(w, r, 422, ve.Code, ve.Msg, nil)
	case errors.As(err, &ue):
		s.fail(w, r, 502, "UPSTREAM_ERROR", ue.Msg, nil)
	default:
		s.internal(w, r, "内部错误", err)
	}
}

func (s *Server) llmPresets(w http.ResponseWriter, r *http.Request) { s.ok(w, 200, s.d.LLM.Presets()) }
func (s *Server) llmGet(w http.ResponseWriter, r *http.Request)     { s.ok(w, 200, s.d.LLM.Config()) }

func (s *Server) llmPut(w http.ResponseWriter, r *http.Request) {
	var in llm.Input
	if !s.decode(w, r, &in) {
		return
	}
	cfg, err := s.d.LLM.Save(r.Context(), in)
	if err != nil {
		s.llmErr(w, r, err)
		return
	}
	// The audit text never contains the key.
	s.d.Store.Audit(r.Context(), actorOf(r), "llm.config", "llm", "识别引擎："+cfg.Mode+" / "+cfg.PresetID+" / "+cfg.Model)
	s.ok(w, 200, cfg)
}

func (s *Server) llmTest(w http.ResponseWriter, r *http.Request) {
	var in llm.TestInput
	if !s.decode(w, r, &in) {
		return
	}
	res, err := s.d.LLM.Test(r.Context(), in)
	if err != nil {
		s.llmErr(w, r, err)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) llmModels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	models, err := s.d.LLM.Models(r.Context(), in.BaseURL, in.APIKey)
	if err != nil {
		s.llmErr(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"models": models})
}
