package pipeline

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"strconv"
	"strings"

	"oa-agent/backend/internal/diag"
	"oa-agent/backend/internal/harness"
	"oa-agent/backend/internal/imports"
	"oa-agent/backend/internal/llm"
)

// ErrorReportHook, when set, receives pipeline errors worth an operator's attention (source "pipeline" or "llm").
// Messages and codes never contain secrets, document text or prompts. (Tests use it; production reports go to diag.)
var ErrorReportHook func(source, code, message string)

// reportError is the single place the pipeline reports an unexpected failure to diagnostics.
func reportError(source, code, message string, err error) {
	reportTask(source, code, message, err, "", "", nil)
}

// reportTask records an internal failure (not a user error such as an encrypted or corrupt file) with the task and
// file ids and file metadata only (id, size, extension) — never the document's text.
func reportTask(source, code, message string, err error, taskID, fileID string, meta map[string]any) string {
	text := message
	if err != nil {
		text += ": " + err.Error()
	}
	if ErrorReportHook != nil {
		ErrorReportHook(source, code, message)
	}
	d := map[string]any{}
	for k, v := range meta {
		d[k] = v
	}
	if fileID != "" {
		d["file_id"] = fileID
	}
	id := diag.Report(context.Background(), diag.Entry{Source: source, Code: code, Message: text, TaskID: taskID, Details: d})
	if id == "" {
		log.Printf("error report [%s/%s] %s", source, code, text)
	}
	return id
}

// reportAnalyzeFailure records an analysis failure that is not the file's own fault: provider (LLM) errors, task model
// problems, worker faults. Encrypted, corrupt, unsupported or oversized files are user errors and are not reported.
func (s *Service) reportAnalyzeFailure(job imports.Job, f imports.File, err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	meta := map[string]any{"ext": filepath.Ext(f.SourceName), "size_bytes": f.SizeBytes, "llm_source": job.LLMSource, "model": job.LLMModel}
	var we *harness.WorkerError
	var pe *harness.ProviderError
	var tme *llm.TaskModelError
	switch {
	case errors.As(err, &we):
		switch we.Code {
		case harness.CodeEncrypted, harness.CodeUnreadableFile, harness.CodeUnsupportedFormat, harness.CodeFileTooLarge, harness.CodeFileNotFound:
			return
		}
		reportTask("worker", "WORKER_"+we.Code, "文档解析服务失败", &harness.WorkerError{Code: we.Code, Status: we.Status}, job.ID, f.ID, meta)
	case errors.As(err, &pe):
		code := "PROVIDER_ERROR"
		if pe.Status != 0 {
			code = "PROVIDER_HTTP_" + strconv.Itoa(pe.Status)
		} else if pe.Code != "" {
			code = "PROVIDER_" + strings.ToUpper(strings.TrimPrefix(pe.Code, "llm_"))
		}
		reportTask("llm", code, "大模型调用失败", pe, job.ID, f.ID, meta)
	case errors.As(err, &tme):
		reportTask("llm", "TASK_MODEL_UNAVAILABLE", "任务绑定的大模型不可用", nil, job.ID, f.ID, meta)
	case errors.Is(err, context.DeadlineExceeded):
		reportTask("pipeline", "ANALYZE_TIMEOUT", "文件识别超时", nil, job.ID, f.ID, meta)
	default:
		reportTask("pipeline", "ANALYZE_FAILED", "文件识别失败", err, job.ID, f.ID, meta)
	}
}

// processHook lets tests inject a fault at the start of job processing (nil in production).
var processHook func(imports.Job)
