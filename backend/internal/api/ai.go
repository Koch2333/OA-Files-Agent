package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"oa-agent/backend/internal/auth"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/llm"
)

// AIManager is the part of llm.Manager behind the AI workbench: per-user status and resolution, personal
// configurations and the administrator policy.
type AIManager interface {
	UserStatus(ctx context.Context, sub llm.Subject) (llm.UserStatus, error)
	Resolve(ctx context.Context, sub llm.Subject, requested string) (llm.Resolution, error)
	PersonalConfig(ctx context.Context, sub llm.Subject) (llm.ConfigView, error)
	SavePersonal(ctx context.Context, sub llm.Subject, in llm.Input) (llm.ConfigView, error)
	DeletePersonal(ctx context.Context, sub llm.Subject) error
	TestPersonal(ctx context.Context, sub llm.Subject, in llm.TestInput) (llm.TestResult, error)
	ModelsPersonal(ctx context.Context, sub llm.Subject, baseURL, apiKey string) ([]string, error)
	Policy(ctx context.Context) (llm.Policy, error)
	UpdatePolicy(ctx context.Context, p llm.PolicyPatch) (llm.Policy, error)
}

func (s *Server) aiRoutes() {
	s.handle("/v1/llm/status", map[string]http.HandlerFunc{"GET": s.llmStatus})
	s.handle("/v1/llm/personal", map[string]http.HandlerFunc{"GET": s.personalGet, "PUT": s.personalPut, "DELETE": s.personalDelete})
	s.handle("/v1/llm/personal/test", map[string]http.HandlerFunc{"POST": s.personalTest})
	s.handle("/v1/llm/personal/models", map[string]http.HandlerFunc{"POST": s.personalModels})
	s.handle("/v1/admin/ai-policy", map[string]http.HandlerFunc{"GET": s.aiPolicyGet, "PATCH": s.aiPolicyPatch})
}

// subjectOf is the signed-in user (local mode: the built-in admin; no auth wired: the same, for unit tests).
func subjectOf(r *http.Request) llm.Subject {
	if id, ok := identityOf(r); ok {
		return llm.Subject{ID: id.User.ID, Username: id.User.Username, Role: id.User.Role}
	}
	return llm.Subject{ID: auth.LocalUserName, Username: auth.LocalUserName, Role: "admin"}
}

func (s *Server) aiErr(w http.ResponseWriter, r *http.Request, err error) {
	var pe *llm.PolicyError
	if errors.As(err, &pe) {
		s.fail(w, r, 403, pe.Code, pe.Msg, nil)
		return
	}
	s.llmErr(w, r, err)
}

func (s *Server) llmStatus(w http.ResponseWriter, r *http.Request) {
	st, err := s.d.LLM.UserStatus(r.Context(), subjectOf(r))
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.ok(w, 200, st)
}

var personalLimiters sync.Map // *Server -> *auth.Limiter

// personalRate allows 10 personal test/model calls per minute and user; beyond that 429 with Retry-After.
func (s *Server) personalRate(w http.ResponseWriter, r *http.Request) bool {
	v, _ := personalLimiters.LoadOrStore(s, auth.NewLimiter(10, 6*time.Second, nil))
	ok, wait := v.(*auth.Limiter).Allow(subjectOf(r).ID)
	if ok {
		return true
	}
	secs := int((wait + time.Second - 1) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	s.fail(w, r, 429, "RATE_LIMITED", "测试过于频繁，请稍后再试", map[string]any{"retry_after": secs})
	return false
}

func (s *Server) personalGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.d.LLM.PersonalConfig(r.Context(), subjectOf(r))
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.ok(w, 200, v)
}

func (s *Server) personalPut(w http.ResponseWriter, r *http.Request) {
	var in llm.Input
	if !s.decode(w, r, &in) {
		return
	}
	v, err := s.d.LLM.SavePersonal(r.Context(), subjectOf(r), in)
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	// The audit text never contains the key or the base URL.
	s.d.Store.Audit(r.Context(), actorOf(r), "llm.personal.config", "llm:"+subjectOf(r).Username, "个人大模型："+v.Mode+" / "+v.PresetID+" / "+v.Model)
	s.ok(w, 200, v)
}

func (s *Server) personalDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.d.LLM.DeletePersonal(r.Context(), subjectOf(r)); err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "llm.personal.delete", "llm:"+subjectOf(r).Username, "删除个人大模型配置")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) personalTest(w http.ResponseWriter, r *http.Request) {
	var in llm.TestInput
	if !s.decode(w, r, &in) {
		return
	}
	if !s.personalRate(w, r) {
		return
	}
	res, err := s.d.LLM.TestPersonal(r.Context(), subjectOf(r), in)
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.ok(w, 200, res)
}

func (s *Server) personalModels(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
	}
	if !s.decode(w, r, &in) {
		return
	}
	if !s.personalRate(w, r) {
		return
	}
	models, err := s.d.LLM.ModelsPersonal(r.Context(), subjectOf(r), in.BaseURL, in.APIKey)
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.ok(w, 200, map[string]any{"models": models})
}

func (s *Server) aiPolicyGet(w http.ResponseWriter, r *http.Request) {
	p, err := s.d.LLM.Policy(r.Context())
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.ok(w, 200, p)
}

func (s *Server) aiPolicyPatch(w http.ResponseWriter, r *http.Request) {
	var in llm.PolicyPatch
	if !s.decode(w, r, &in) {
		return
	}
	p, err := s.d.LLM.UpdatePolicy(r.Context(), in)
	if err != nil {
		s.aiErr(w, r, err)
		return
	}
	s.d.Store.Audit(r.Context(), actorOf(r), "ai.policy", "ai-policy", fmt.Sprintf("AI 策略：个人 API=%v，个人 API 允许内网=%v，全自动归档=%v", p.AllowPersonalLLM, p.AllowPersonalPrivateHosts, p.AllowAutoArchive))
	s.ok(w, 200, p)
}

// ---- task options (imports) ----

// importOptions is the contract's ImportOptions.
type importOptions struct {
	Instruction string `json:"instruction"`
	Automation  string `json:"automation"`
	LLMSource   string `json:"llm_source"`
	AutoAck     *struct {
		Version  string `json:"version"`
		Accepted *bool  `json:"accepted"`
	} `json:"auto_ack"`
}

// optErr is a refusal of the task options with the HTTP answer to give.
type optErr struct {
	status    int
	code, msg string
}

func (e *optErr) Error() string { return e.code }

func (e *optErr) asImportError() *imports.Error {
	return &imports.Error{Status: e.status, Code: e.code, Message: e.msg}
}

// taskAI validates the options for the caller and resolves the model. Everything the fully automatic mode needs is
// enforced here, on the server, before anything is stored: policy, a real model, the operator role and the
// acknowledgement of the current risk text.
func (s *Server) taskAI(ctx context.Context, sub llm.Subject, o importOptions) (imports.TaskAI, *optErr) {
	instruction := strings.TrimSpace(o.Instruction)
	if n := len([]rune(instruction)); n > harness.MaxInstructionRunes {
		return imports.TaskAI{}, &optErr{422, "INSTRUCTION_TOO_LONG", fmt.Sprintf("任务说明最多 %d 字（当前 %d 字）", harness.MaxInstructionRunes, n)}
	}
	for _, c := range instruction {
		if unicode.IsControl(c) && c != '\n' && c != '\r' && c != '\t' {
			return imports.TaskAI{}, &optErr{422, "INVALID_INSTRUCTION", "任务说明含有不允许的控制字符"}
		}
	}
	automation := o.Automation
	switch automation {
	case "":
		automation = imports.AutomationReview
	case imports.AutomationReview, imports.AutomationAuto:
	default:
		return imports.TaskAI{}, &optErr{400, "INVALID_OPTIONS", "automation 须为 review 或 auto"}
	}
	requested := o.LLMSource
	switch requested {
	case "":
		requested = "auto"
	case "auto", imports.SourceGlobal, imports.SourcePersonal:
	default:
		return imports.TaskAI{}, &optErr{400, "INVALID_OPTIONS", "llm_source 须为 auto、global 或 personal"}
	}
	res, err := s.d.LLM.Resolve(ctx, sub, requested)
	if err != nil {
		var pe *llm.PolicyError
		var ve *llm.ValidationError
		switch {
		case errors.As(err, &pe):
			return imports.TaskAI{}, &optErr{403, pe.Code, pe.Msg}
		case errors.As(err, &ve):
			return imports.TaskAI{}, &optErr{422, ve.Code, ve.Msg}
		}
		return imports.TaskAI{}, &optErr{500, "INTERNAL", "内部错误"}
	}
	if instruction != "" && res.Source == imports.SourceNone {
		return imports.TaskAI{}, &optErr{422, "LLM_REQUIRED", "任务说明需要已配置的大模型；请先配置大模型（离线规则无法理解说明）"}
	}
	ai := imports.TaskAI{Instruction: instruction, Automation: automation, Source: res.Source, Owner: sub.ID, Model: res.Model}
	ai.Fingerprint = fmt.Sprintf("%q|%s|%s", instruction, automation, requested)
	if automation == imports.AutomationAuto {
		if sub.Role != "operator" && sub.Role != "admin" {
			return imports.TaskAI{}, &optErr{403, "FORBIDDEN", "当前角色没有权限使用全自动模式"}
		}
		pol, perr := s.d.LLM.Policy(ctx)
		if perr != nil {
			return imports.TaskAI{}, &optErr{500, "INTERNAL", "内部错误"}
		}
		if !pol.AllowAutoArchive {
			return imports.TaskAI{}, &optErr{403, "AUTO_ARCHIVE_DISABLED", "管理员已关闭全自动归档"}
		}
		if res.Source == imports.SourceNone || res.Mode != llm.ModeLLM {
			return imports.TaskAI{}, &optErr{422, "LLM_REQUIRED", "全自动模式必须使用已配置的大模型，离线规则不能用于全自动归档"}
		}
		if o.AutoAck == nil || o.AutoAck.Accepted == nil || !*o.AutoAck.Accepted || o.AutoAck.Version != llm.AutoAckVersion {
			return imports.TaskAI{}, &optErr{422, "AUTO_ACK_REQUIRED", "使用全自动模式必须确认当前版本的风险提示（auto_ack.version=" + llm.AutoAckVersion + "，accepted=true）"}
		}
		ai.AckVersion, ai.AckBy = llm.AutoAckVersion, sub.Username
		ai.Fingerprint += "|ack:" + llm.AutoAckVersion
	}
	return ai, nil
}

// parseOptions decodes the options JSON strictly (unknown fields are rejected).
func parseOptions(raw []byte) (importOptions, *optErr) {
	var o importOptions
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&o); err != nil {
		return o, &optErr{400, "INVALID_OPTIONS", "options 不是有效的 ImportOptions JSON 或含未知字段"}
	}
	if dec.More() {
		return o, &optErr{400, "INVALID_OPTIONS", "options 只能包含一个 JSON 对象"}
	}
	return o, nil
}

// withImportAI prepares ctx for Import: plain imports get the default resolution (so the task records which model
// runs it); an "options" multipart part replaces it after strict validation.
func (s *Server) withImportAI(ctx context.Context, r *http.Request) (context.Context, *optErr) {
	if s.d.LLM == nil {
		return ctx, nil
	}
	sub := subjectOf(r)
	def, e := s.taskAI(ctx, sub, importOptions{})
	if e != nil {
		return ctx, e
	}
	ctx = imports.WithAI(ctx, def)
	ctx = imports.WithOptionsHandler(ctx, func(raw []byte) (imports.TaskAI, error) {
		o, oe := parseOptions(raw)
		if oe != nil {
			return imports.TaskAI{}, oe.asImportError()
		}
		ai, oe := s.taskAI(ctx, sub, o)
		if oe != nil {
			return imports.TaskAI{}, oe.asImportError()
		}
		return ai, nil
	})
	return ctx, nil
}

// auditAutoAccepted records the acknowledgement of the fully automatic risk text (once per created task).
func (s *Server) auditAutoAccepted(ctx context.Context, r *http.Request, job imports.Job, replay bool) {
	if job.Automation != imports.AutomationAuto || replay {
		return
	}
	s.d.Store.Audit(ctx, actorOf(r), "ai.auto_archive.accepted", job.ID,
		fmt.Sprintf("用户 %s 确认全自动归档风险提示（版本 %s），任务 %s，模型 %s", actorOf(r), job.AckVersion, job.ID, job.LLMModel))
}

// createdSummary / createdDetails make the SSE trace say how the task was configured (automatic mode, model).
func createdSummary(j imports.Job) string {
	s := fmt.Sprintf("任务已创建，共 %d 个文件", j.TotalFiles)
	if j.Automation == imports.AutomationAuto {
		s += fmt.Sprintf("；全自动模式：由大模型 %s 决定并自动归档，无需人工审批（已确认风险提示）", j.LLMModel)
	}
	return s
}

func createdDetails(j imports.Job) map[string]any {
	if j.Automation != imports.AutomationAuto && j.LLMSource == imports.SourceNone && j.Instruction == "" {
		return nil
	}
	return map[string]any{"automation": j.Automation, "llm_source": j.LLMSource, "model": j.LLMModel, "instruction": j.Instruction != ""}
}
