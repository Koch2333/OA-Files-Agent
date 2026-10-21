package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	maxToolArgBytes    = 16 << 10
	maxToolOutputBytes = 64 << 10
)

// Tool error codes surfaced to the model as observations.
const (
	ToolInvalidInput  = "invalid_input"
	ToolUnknown       = "unknown_tool"
	ToolTimeout       = "timeout"
	ToolFailed        = "tool_failed"
	ToolOutputInvalid = "output_invalid"
)

// ToolError is returned by Registry.Execute. Message is safe to show to the
// model; it never contains document text.
type ToolError struct {
	Code    string
	Message string
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

// ToolSpec is what the provider is told about a tool.
type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

// Tool is a read-only capability exposed to the model.
type Tool struct {
	Name         string
	Description  string
	InputSchema  map[string]any
	OutputSchema map[string]any
	Timeout      time.Duration
	Run          func(ctx context.Context, s *Session, args map[string]any) (any, error)
}

// Registry is a fixed set of tools. It has no shell/file-write tool and the
// model cannot register tools, so document content cannot widen permissions.
type Registry struct {
	tools map[string]Tool
}

func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool; it panics on programmer errors (duplicate/empty).
func (r *Registry) Register(t Tool) {
	if t.Name == "" || t.Run == nil || t.InputSchema == nil || t.OutputSchema == nil || t.Timeout <= 0 {
		panic("harness: incomplete tool " + t.Name)
	}
	if _, dup := r.tools[t.Name]; dup {
		panic("harness: duplicate tool " + t.Name)
	}
	r.tools[t.Name] = t
}

// Names returns registered tool names in sorted order.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Specs returns the provider-facing tool definitions in a stable order.
func (r *Registry) Specs() []ToolSpec {
	out := make([]ToolSpec, 0, len(r.tools))
	for _, n := range r.Names() {
		t := r.tools[n]
		out = append(out, ToolSpec{Name: t.Name, Description: t.Description, Parameters: t.InputSchema})
	}
	return out
}

// Execute validates input, runs the tool under its timeout, validates the
// output and returns it as compact JSON. Failures are *ToolError (or ctx
// errors when the parent context is done).
func (r *Registry) Execute(ctx context.Context, s *Session, name string, rawArgs json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", &ToolError{Code: ToolUnknown, Message: "no such tool"}
	}
	if len(rawArgs) > maxToolArgBytes {
		return "", &ToolError{Code: ToolInvalidInput, Message: "arguments too large"}
	}
	args := map[string]any{}
	if trimmed := bytes.TrimSpace(rawArgs); len(trimmed) > 0 && string(trimmed) != "null" {
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		if err := dec.Decode(&args); err != nil || dec.More() {
			return "", &ToolError{Code: ToolInvalidInput, Message: "arguments must be a JSON object"}
		}
	}
	if err := validateSchema(t.InputSchema, args, "args"); err != nil {
		return "", &ToolError{Code: ToolInvalidInput, Message: err.Error()}
	}
	cctx, cancel := context.WithTimeout(ctx, t.Timeout)
	defer cancel()
	type outcome struct {
		v   any
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ch <- outcome{err: errors.New("tool panicked")}
			}
		}()
		v, err := t.Run(cctx, s, args)
		ch <- outcome{v, err}
	}()
	var oc outcome
	select {
	case oc = <-ch:
	case <-cctx.Done():
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", &ToolError{Code: ToolTimeout, Message: "tool timed out"}
	}
	if oc.err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var te *ToolError
		if errors.As(oc.err, &te) {
			return "", te
		}
		var we *WorkerError
		if errors.As(oc.err, &we) {
			return "", &ToolError{Code: we.Code, Message: we.Message}
		}
		if errors.Is(oc.err, context.DeadlineExceeded) {
			return "", &ToolError{Code: ToolTimeout, Message: "tool timed out"}
		}
		return "", &ToolError{Code: ToolFailed, Message: "tool failed"}
	}
	b, err := marshalPlain(oc.v)
	if err != nil || len(b) > maxToolOutputBytes {
		return "", &ToolError{Code: ToolOutputInvalid, Message: "tool output unusable"}
	}
	var decoded any
	if err := json.Unmarshal(b, &decoded); err != nil {
		return "", &ToolError{Code: ToolOutputInvalid, Message: "tool output unusable"}
	}
	if err := validateSchema(t.OutputSchema, decoded, "output"); err != nil {
		return "", &ToolError{Code: ToolOutputInvalid, Message: fmt.Sprintf("tool output violates schema: %v", err)}
	}
	return string(b), nil
}

// marshalPlain is json.Marshal without HTML escaping so that delimiters
// around document data stay readable to the model.
func marshalPlain(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
