package harness

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// LLM failure codes (stable; shown to the UI and stored with the failed file).
const (
	LLMAuth        = "llm_auth"         // 401/403: key missing, invalid or without permission
	LLMNotFound    = "llm_not_found"    // 404: endpoint or model does not exist
	LLMRejected    = "llm_rejected"     // other 4xx: request refused (model name, parameters)
	LLMRateLimited = "llm_rate_limited" // 429
	LLMServer      = "llm_server"       // 5xx
	LLMTimeout     = "llm_timeout"      // 408 or client timeout
	LLMUnreachable = "llm_unreachable"  // connection refused/reset, DNS, TLS
	LLMBadResponse = "llm_bad_response" // truncated or malformed body
)

// MaxRetryAfter is the longest Retry-After the provider layer waits for inside one call; a longer
// value stops the automatic retries and is handed to the UI as a countdown instead.
const MaxRetryAfter = 8 * time.Second

// StatusFor builds the typed error for an HTTP status. attempts is filled in by the caller.
func classifyStatus(status int) (code string, retryable bool) {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return LLMAuth, false
	case status == http.StatusNotFound:
		return LLMNotFound, false
	case status == http.StatusRequestTimeout:
		return LLMTimeout, true
	case status == http.StatusTooManyRequests:
		return LLMRateLimited, true
	case status >= 500:
		return LLMServer, true
	}
	return LLMRejected, false
}

// classifyTransport maps a transport error (never including URL or key text) to a code.
func classifyTransport(err error) string {
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return LLMTimeout
	}
	return LLMUnreachable
}

// UserMessage is a short, user-safe Chinese description (no key, body or document text).
func (e *ProviderError) UserMessage() string {
	tail := ""
	if e.Attempts > 1 && e.Retryable {
		tail = "，已自动重试 " + strconv.Itoa(e.Attempts-1) + " 次"
	}
	st := ""
	if e.Status != 0 {
		st = "服务商返回 " + strconv.Itoa(e.Status) + "："
	}
	switch e.Code {
	case LLMAuth:
		return st + "API Key 无效或没有权限"
	case LLMNotFound:
		return st + "接口或模型不存在，请检查 Base URL 与模型名称"
	case LLMRateLimited:
		return st + "请求太频繁或额度不足" + tail
	case LLMServer:
		return st + "服务商暂时不可用" + tail
	case LLMTimeout:
		return "连接服务商超时" + tail
	case LLMUnreachable:
		return "无法连接服务商，请检查网络与 Base URL" + tail
	case LLMBadResponse:
		return "服务商返回的内容不完整" + tail
	case LLMRejected:
		return st + "服务商拒绝了请求，模型名称可能不受支持"
	}
	return "大模型调用失败" + tail
}

// AsProviderError unwraps a ProviderError from err.
func AsProviderError(err error) (*ProviderError, bool) {
	var pe *ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// LLMErrorInfo is the persisted/serialised form of a provider failure.
type LLMErrorInfo struct {
	Code              string `json:"code"`
	Status            int    `json:"status"`
	Retryable         bool   `json:"retryable"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Attempts          int    `json:"attempts"`
	Message           string `json:"message"`
}

// Info converts the error for storage.
func (e *ProviderError) Info() LLMErrorInfo {
	return LLMErrorInfo{Code: e.Code, Status: e.Status, Retryable: e.Retryable, RetryAfterSeconds: e.RetryAfterSec, Attempts: e.Attempts, Message: e.UserMessage()}
}

func parseRetryAfter(v string, now time.Time) int {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return s
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return int((d + time.Second - 1) / time.Second)
		}
	}
	return 0
}
