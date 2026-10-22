package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"oa-agent/backend/internal/diag"
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
	// ToolSkillNotLoaded: the model called a skill tool before load_skill.
	ToolSkillNotLoaded = "skill_not_loaded"
	// ToolRetryLimit: a per-file attempt cap (OCR reruns, vision reads) is spent.
	ToolRetryLimit = "retry_limit"
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
	// Available, when set, hides the tool (and rejects calls as unknown) for
	// sessions where it returns false, e.g. vision tools without a vision model.
	Available func(s *Session) bool
}

// Registry is a fixed set of tools. It has no shell/file-write tool and the
// model cannot register tools, so document content cannot widen permissions.
type Registry struct {
	tools     map[string]Tool
	skills    map[string]Skill
	toolSkill map[string]string // tool name -> owning skill (locked until loaded)
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]Tool{}, skills: map[string]Skill{}, toolSkill: map[string]string{}}
}

// RegisterSkill adds a skill and locks its tools until load_skill unlocks
// them. It panics on programmer errors (duplicate, unknown or already owned tool).
func (r *Registry) RegisterSkill(sk Skill) {
	if sk.Name == "" || sk.Instructions == "" {
		panic("harness: incomplete skill " + sk.Name)
	}
	if _, dup := r.skills[sk.Name]; dup {
		panic("harness: duplicate skill " + sk.Name)
	}
	for _, n := range sk.Tools {
		if _, ok := r.tools[n]; !ok {
			panic("harness: skill " + sk.Name + " names unknown tool " + n)
		}
		if owner, taken := r.toolSkill[n]; taken {
			panic("harness: tool " + n + " already owned by skill " + owner)
		}
		r.toolSkill[n] = sk.Name
	}
	r.skills[sk.Name] = sk
}

// Skills returns the registered skills sorted by name.
func (r *Registry) Skills() []Skill {
	out := make([]Skill, 0, len(r.skills))
	for _, sk := range r.skills {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// usable reports whether tool t may be offered/executed for session s
// (nil = no session: only base tools).
func (r *Registry) usable(t Tool, s *Session) bool {
	if owner, locked := r.toolSkill[t.Name]; locked && (s == nil || !s.loaded[owner]) {
		return false
	}
	return t.Available == nil || (s != nil && t.Available(s))
}

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

// Specs returns the base (always available) tool definitions in a stable
// order: tools owned by a skill or gated by Available are not included.
func (r *Registry) Specs() []ToolSpec { return r.SpecsFor(nil) }

// SpecsFor returns the tools offered to the model for session s: the base
// tools plus the tools of the skills s has loaded (and that are available).
func (r *Registry) SpecsFor(s *Session) []ToolSpec {
	out := make([]ToolSpec, 0, len(r.tools))
	for _, n := range r.Names() {
		t := r.tools[n]
		if !r.usable(t, s) {
			continue
		}
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
	if t.Available != nil && (s == nil || !t.Available(s)) {
		return "", &ToolError{Code: ToolUnknown, Message: "no such tool"}
	}
	if owner, locked := r.toolSkill[name]; locked && (s == nil || !s.loaded[owner]) {
		return "", &ToolError{Code: ToolSkillNotLoaded, Message: "tool is locked: call load_skill with name \"" + owner + "\" first"}
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
				diag.ReportPanic("pipeline.tool."+t.Name, p)
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
