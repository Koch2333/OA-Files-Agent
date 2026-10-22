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
	reruns   int          // rerun_ocr calls so far (successful or not)
	ocr      *ocrProgress // latest assess_ocr / rerun_ocr observation
}

// ocrProgress is what the latest OCR-rescue observation says.
type ocrProgress struct {
	Verdict   string   `json:"verdict"`
	HasDate   bool     `json:"has_date"`
	HasDept   bool     `json:"has_department"`
	Suggested []string `json:"suggested_profiles"`
}

// needsRescue: poor, or fair with neither a date nor a department found.
func (p ocrProgress) needsRescue() bool {
	return p.Verdict == verdictPoor || (p.Verdict == verdictFair && !p.HasDate && !p.HasDept)
}

// maxHeuristicReruns bounds the offline rescue (the model-driven path may use
// more). Vision is never used by this provider.
const maxHeuristicReruns = 2

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
		if m.Name == "rerun_ocr" {
			st.reruns++
		}
		if (m.Name == "assess_ocr" || m.Name == "rerun_ocr") && o.OK {
			var p ocrProgress
			if json.Unmarshal(o.Result, &p) == nil {
				st.ocr = &p
			}
		}
	}
	callArgs := func(name, args string) (Response, error) {
		return Response{ToolCalls: []ToolCall{{ID: "h_" + name + "_" + strconv.Itoa(len(msgs)), Name: name, Arguments: json.RawMessage(args)}}}, nil
	}
	call := func(name string) (Response, error) { return callArgs(name, `{}`) }
	if !st.called["inspect_file"] {
		return call("inspect_file")
	}
	if !st.called["extract_text"] {
		return call("extract_text")
	}
	if !st.called["run_ocr"] && st.wantsOCR() {
		return call("run_ocr")
	}
	if st.called["run_ocr"] && st.obs["run_ocr"].OK {
		if !st.called["load_skill"] {
			return callArgs("load_skill", `{"name":"`+SkillOCRRescue+`"}`)
		}
		if !st.called["assess_ocr"] {
			return call("assess_ocr")
		}
		if p := st.ocr; p != nil && p.needsRescue() && st.reruns < maxHeuristicReruns && len(p.Suggested) > 0 {
			return callArgs("rerun_ocr", `{"profile":"`+p.Suggested[0]+`"}`)
		}
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
