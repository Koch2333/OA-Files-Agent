package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// OpenAIConfig configures an OpenAI-compatible chat completions endpoint.
type OpenAIConfig struct {
	BaseURL      string        // e.g. https://api.openai.com/v1 or a local server
	Model        string        // required
	APIKey       string        // sent as Bearer token if non-empty
	Timeout      time.Duration // per HTTP attempt (default 60s)
	MaxRetries   int           // retries after the first attempt on 429/5xx/network (default 3; negative = 0)
	BackoffBase  time.Duration // default 500ms, doubled per retry with jitter
	MaxBackoff   time.Duration // default 8s
	Temperature  float64
	MaxResponse  int64 // body cap in bytes (default 2 MiB)
	HTTPClient   *http.Client
	ExtraHeaders map[string]string
}

// OpenAIConfigFromEnv reads OA_LLM_BASE_URL, OA_LLM_MODEL, OA_LLM_API_KEY and
// OA_LLM_TIMEOUT_SECONDS. The API key never leaves the server process.
func OpenAIConfigFromEnv() OpenAIConfig {
	c := OpenAIConfig{BaseURL: os.Getenv("OA_LLM_BASE_URL"), Model: os.Getenv("OA_LLM_MODEL"), APIKey: os.Getenv("OA_LLM_API_KEY")}
	if n, err := strconv.Atoi(os.Getenv("OA_LLM_TIMEOUT_SECONDS")); err == nil && n > 0 {
		c.Timeout = time.Duration(n) * time.Second
	}
	return c
}

// ProviderError describes a failed provider call. It never contains the API key
// or the response body.
type ProviderError struct {
	Status int
	Msg    string
}

func (e *ProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("provider error (HTTP %d): %s", e.Status, e.Msg)
	}
	return "provider error: " + e.Msg
}

// OpenAICompatible implements Provider for /chat/completions with tools.
type OpenAICompatible struct {
	cfg      OpenAIConfig
	endpoint string
	sleep    func(context.Context, time.Duration) error
}

func NewOpenAICompatible(cfg OpenAIConfig) (*OpenAICompatible, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openai-compatible provider requires base URL and model")
	}
	if !strings.HasPrefix(cfg.BaseURL, "http://") && !strings.HasPrefix(cfg.BaseURL, "https://") {
		return nil, errors.New("base URL must be http(s)")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	} else if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.BackoffBase <= 0 {
		cfg.BackoffBase = 500 * time.Millisecond
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 8 * time.Second
	}
	if cfg.MaxResponse <= 0 {
		cfg.MaxResponse = 2 << 20
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	return &OpenAICompatible{cfg: cfg, endpoint: strings.TrimSuffix(cfg.BaseURL, "/") + "/chat/completions", sleep: sleepCtx}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type oaiMessage struct {
	Role       string    `json:"role"`
	Content    *string   `json:"content"`
	ToolCalls  []oaiCall `json:"tool_calls,omitempty"`
	ToolCallID string    `json:"tool_call_id,omitempty"`
	Name       string    `json:"name,omitempty"`
}

type oaiCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Content   *string   `json:"content"`
			ToolCalls []oaiCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// Chat implements Provider.
func (p *OpenAICompatible) Chat(ctx context.Context, messages []Message, tools []ToolSpec) (Response, error) {
	om := make([]oaiMessage, 0, len(messages))
	for _, m := range messages {
		x := oaiMessage{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.Role == RoleTool {
			x.Name = m.Name
		}
		if m.Content != "" || len(m.ToolCalls) == 0 {
			c := m.Content
			x.Content = &c
		}
		for _, tc := range m.ToolCalls {
			var oc oaiCall
			oc.ID, oc.Type = tc.ID, "function"
			oc.Function.Name = tc.Name
			oc.Function.Arguments = string(tc.Arguments)
			if oc.Function.Arguments == "" {
				oc.Function.Arguments = "{}"
			}
			x.ToolCalls = append(x.ToolCalls, oc)
		}
		om = append(om, x)
	}
	body := map[string]any{"model": p.cfg.Model, "messages": om, "temperature": p.cfg.Temperature}
	if len(tools) > 0 {
		ts := make([]map[string]any, 0, len(tools))
		for _, t := range tools {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": t.Parameters}})
		}
		body["tools"] = ts
		body["tool_choice"] = "auto"
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	var last error
	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := p.sleep(ctx, p.backoff(attempt, last)); err != nil {
				return Response{}, err
			}
		}
		resp, retry, err := p.once(ctx, payload)
		if err == nil {
			return resp, nil
		}
		last = err
		if !retry || ctx.Err() != nil {
			if ctx.Err() != nil {
				return Response{}, ctx.Err()
			}
			return Response{}, err
		}
	}
	return Response{}, last
}

type retryAfterError struct {
	*ProviderError
	after time.Duration
}

func (e *retryAfterError) Unwrap() error { return e.ProviderError }

func (p *OpenAICompatible) backoff(attempt int, last error) time.Duration {
	var ra *retryAfterError
	if errors.As(last, &ra) && ra.after > 0 {
		if ra.after > p.cfg.MaxBackoff {
			return p.cfg.MaxBackoff
		}
		return ra.after
	}
	d := p.cfg.BackoffBase << (attempt - 1)
	if d > p.cfg.MaxBackoff || d <= 0 {
		d = p.cfg.MaxBackoff
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

func (p *OpenAICompatible) once(ctx context.Context, payload []byte) (Response, bool, error) {
	actx, cancel := context.WithTimeout(ctx, p.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(actx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Response{}, false, &ProviderError{Msg: "invalid request"}
	}
	req.Header.Set("Content-Type", "application/json")
	if p.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)
	}
	for k, v := range p.cfg.ExtraHeaders {
		req.Header.Set(k, v)
	}
	res, err := p.cfg.HTTPClient.Do(req)
	if err != nil {
		return Response{}, true, &ProviderError{Msg: "request failed"}
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, p.cfg.MaxResponse+1))
	if err != nil {
		return Response{}, true, &ProviderError{Status: res.StatusCode, Msg: "reading response failed"}
	}
	if int64(len(raw)) > p.cfg.MaxResponse {
		return Response{}, false, &ProviderError{Status: res.StatusCode, Msg: "response too large"}
	}
	if res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
		pe := &ProviderError{Status: res.StatusCode, Msg: "retryable status"}
		ra := &retryAfterError{ProviderError: pe}
		if s, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && s >= 0 {
			ra.after = time.Duration(s) * time.Second
		}
		return Response{}, true, ra
	}
	if res.StatusCode != http.StatusOK {
		return Response{}, false, &ProviderError{Status: res.StatusCode, Msg: "request rejected"}
	}
	var or oaiResponse
	if err := json.Unmarshal(raw, &or); err != nil || len(or.Choices) == 0 {
		return Response{}, false, &ProviderError{Status: res.StatusCode, Msg: "malformed response"}
	}
	msg := or.Choices[0].Message
	out := Response{Usage: Usage{PromptTokens: or.Usage.PromptTokens, CompletionTokens: or.Usage.CompletionTokens}}
	if msg.Content != nil {
		out.Content = *msg.Content
	}
	for _, tc := range msg.ToolCalls {
		args := strings.TrimSpace(tc.Function.Arguments)
		if args == "" {
			args = "{}"
		}
		out.ToolCalls = append(out.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: json.RawMessage(args)})
	}
	return out, false, nil
}
