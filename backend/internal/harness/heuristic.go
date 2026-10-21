package harness

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Heuristic is a deterministic, offline Provider. It scripts the loop
// inspect_file -> extract_text -> (run_ocr) -> detect_template ->
// extract_metadata -> finish, reading only the tool observations in the
// message history (it is stateless). All inference is done by the rule engine
// inside the extract_metadata tool, so it is honest: no evidence means null.
type Heuristic struct{}

const nearlyEmptyRunes = 20

type heurState struct {
	called   map[string]bool
	obs      map[string]observation
	lastText observation
}

func (Heuristic) Chat(ctx context.Context, msgs []Message, _ []ToolSpec) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	st := heurState{called: map[string]bool{}, obs: map[string]observation{}}
	for _, m := range msgs {
		if m.Role != RoleTool {
			continue
		}
		var o observation
		_ = json.Unmarshal([]byte(m.Content), &o)
		st.called[m.Name] = true
		st.obs[m.Name] = o
	}
	call := func(name string) (Response, error) {
		return Response{ToolCalls: []ToolCall{{ID: "h_" + name + "_" + strconv.Itoa(len(msgs)), Name: name, Arguments: json.RawMessage(`{}`)}}}, nil
	}
	if !st.called["inspect_file"] {
		return call("inspect_file")
	}
	if !st.called["extract_text"] {
		return call("extract_text")
	}
	if !st.called["run_ocr"] && st.wantsOCR() {
		return call("run_ocr")
	}
	if !st.called["detect_template"] {
		return call("detect_template")
	}
	if !st.called["extract_metadata"] {
		return call("extract_metadata")
	}
	fa := FinalAnswer{ReviewRequired: true}
	if o := st.obs["extract_metadata"]; o.OK {
		var c Candidates
		if json.Unmarshal(o.Result, &c) == nil {
			fa = FinalAnswer{Department: c.Department, DocumentType: c.DocumentType, DocumentDate: c.DocumentDate, Evidence: c.Evidence, ReviewRequired: c.ReviewRequired}
		}
	}
	b, _ := json.Marshal(fa)
	return Response{Content: string(b)}, nil
}

// wantsOCR: images always need OCR; others when native text is (nearly) empty
// or extraction failed.
func (st heurState) wantsOCR() bool {
	if o := st.obs["inspect_file"]; o.OK {
		var r struct {
			Kind string `json:"kind"`
		}
		if json.Unmarshal(o.Result, &r) == nil && r.Kind == "image" {
			return true
		}
	}
	o := st.obs["extract_text"]
	if !o.OK {
		return true
	}
	var r struct {
		Text  string `json:"text"`
		Chars int    `json:"chars"`
	}
	if json.Unmarshal(o.Result, &r) != nil {
		return true
	}
	return r.Chars < nearlyEmptyRunes || utf8.RuneCountInString(strings.TrimSpace(r.Text)) < nearlyEmptyRunes
}
