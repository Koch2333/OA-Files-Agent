package harness

import (
	"context"
	"encoding/json"
)

// Message roles.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// Message is one chat turn. Tool messages carry ToolCallID and Name; their
// Content is the JSON observation {"ok":..., "result"|"error":...}.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Usage is provider-reported token usage.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

// Response is one assistant turn: either tool calls, or (no tool calls) the
// final JSON answer in Content.
type Response struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// Provider is a tool-calling chat model.
type Provider interface {
	Chat(ctx context.Context, messages []Message, tools []ToolSpec) (Response, error)
}

// observation builders (shared by the loop and heuristic parsing).
type observation struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *obsError       `json:"error,omitempty"`
}

type obsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
