package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// fakeModel is a tiny OpenAI-compatible server: first turn asks for inspect_file, then answers with fixed JSON.
func fakeModel(key string, chatCalls *atomic.Int32, lastAuth *atomic.Value) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "bad key", 401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"e2e-model-b"},{"id":"e2e-model-a"}]}`)
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		lastAuth.Store(r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer "+key {
			http.Error(w, "bad key", 401)
			return
		}
		chatCalls.Add(1)
		var body struct {
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []any `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		hasTool := false
		for _, m := range body.Messages {
			if m.Role == "tool" {
				hasTool = true
			}
		}
		switch {
		case len(body.Tools) > 0 && !hasTool:
			fmt.Fprint(w, `{"choices":[{"message":{"content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"inspect_file","arguments":"{}"}}]}}]}`)
		default:
			ans, _ := json.Marshal(map[string]any{"department": "法务部", "document_type": "合同", "document_date": "2026-03-15", "evidence": []string{"e2e-fake-model 判定"}, "review_required": false})
			c, _ := json.Marshal(string(ans))
			fmt.Fprintf(w, `{"choices":[{"message":{"content":%s}}]}`, c)
		}
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String() + "/v1", func() { srv.Close() }
}

func putLLM(body map[string]any) (int, json.RawMessage) {
	st, d, _ := call("PUT", "/v1/llm/config", body, nil)
	return st, d
}

// part5 covers the recognition-engine settings: presets, validation, secret handling, hot swap, test, model list.
func part5() {
	println("--- part 5: LLM presets and engine settings ---")
	st, d, _ := call("GET", "/v1/llm/presets", nil, nil)
	var presets []struct {
		ID           string `json:"id"`
		BaseURL      string `json:"base_url"`
		DefaultModel string `json:"default_model"`
		NeedsAPIKey  bool   `json:"needs_api_key"`
	}
	_ = json.Unmarshal(d, &presets)
	check("llm: presets list DeepSeek first, Qwen second", st == 200 && len(presets) >= 5 && presets[0].ID == "deepseek" && presets[1].ID == "qwen" && presets[0].BaseURL != "" && presets[0].DefaultModel != "", st, string(d))

	st, d, _ = call("GET", "/v1/llm/config", nil, nil)
	var cfg struct {
		Mode       string `json:"mode"`
		PresetID   string `json:"preset_id"`
		APIKeySet  bool   `json:"api_key_set"`
		APIKeyHint string `json:"api_key_hint"`
		State      string `json:"state"`
	}
	_ = json.Unmarshal(d, &cfg)
	check("llm: default engine is offline rules with DeepSeek preselected", st == 200 && cfg.Mode == "heuristic" && cfg.PresetID == "deepseek" && !cfg.APIKeySet, st, string(d))

	// validation
	st, _ = putLLM(map[string]any{"mode": "llm", "preset_id": "deepseek", "base_url": "https://api.deepseek.com/v1", "model": "deepseek-chat"})
	check("llm: DeepSeek without a key → 422", st == 422, st)
	st, _ = putLLM(map[string]any{"mode": "llm", "preset_id": "custom", "base_url": "http://api.example.com/v1", "model": "m"})
	check("llm: plain http to a public host → 422", st == 422, st)
	st, _ = putLLM(map[string]any{"mode": "llm", "preset_id": "custom", "base_url": "http://169.254.169.254/", "model": "m"})
	check("llm: metadata address → 422", st == 422, st)
	st, _ = putLLM(map[string]any{"mode": "llm", "preset_id": "custom", "base_url": "https://x.example.com/v1"})
	check("llm: missing model → 422", st == 422, st)
	st, _ = putLLM(map[string]any{"mode": "wat"})
	check("llm: bad mode → 422", st == 422, st)
	st, _ = putLLM(map[string]any{"mode": "llm", "bogus": 1})
	check("llm: unknown field → 400", st == 400, st)

	// a real OpenAI-compatible endpoint (local fake) → save, no key echo, test, models, hot swap into the pipeline
	var chatCalls atomic.Int32
	var lastAuth atomic.Value
	base, stop := fakeModel("sk-e2e-secret-9876", &chatCalls, &lastAuth)
	defer stop()
	st, d = putLLM(map[string]any{"mode": "llm", "preset_id": "custom", "base_url": base, "model": "e2e-model-a", "api_key": "sk-e2e-secret-9876"})
	check("llm: save LLM mode (200) without echoing the key", st == 200 && !strings.Contains(string(d), "sk-e2e-secret") && strings.Contains(string(d), `"api_key_set":true`) && strings.Contains(string(d), "••••9876"), st, string(d))
	_, d, _ = call("GET", "/v1/llm/config", nil, nil)
	check("llm: GET never returns the key", !strings.Contains(string(d), "sk-e2e-secret"), string(d))
	st, d, _ = call("POST", "/v1/llm/test", map[string]any{"base_url": base, "model": "e2e-model-a"}, nil)
	var tr struct {
		OK          bool  `json:"ok"`
		ToolCalling *bool `json:"tool_calling"`
	}
	_ = json.Unmarshal(d, &tr)
	check("llm: test connection reuses the saved key for the same URL and detects tool calling", st == 200 && tr.OK && tr.ToolCalling != nil && *tr.ToolCalling, st, string(d))
	st, d, _ = call("POST", "/v1/llm/test", map[string]any{"base_url": base, "model": "e2e-model-a", "api_key": "sk-wrong-00000000"}, nil)
	_ = json.Unmarshal(d, &tr)
	check("llm: wrong key → ok=false with a readable message", st == 200 && !tr.OK && strings.Contains(string(d), "密钥无效"), st, string(d))
	st, d, _ = call("POST", "/v1/llm/models", map[string]any{"base_url": base}, nil)
	check("llm: model discovery lists /models (sorted)", st == 200 && strings.Contains(string(d), `["e2e-model-a","e2e-model-b"]`), st, string(d))
	st, _, _ = call("POST", "/v1/llm/models", map[string]any{"base_url": "http://evil.example.com/v1"}, nil)
	check("llm: model discovery refuses unsafe URLs → 422", st == 422, st)
	st, _, _ = call("PUT", "/v1/llm/config", map[string]any{"mode": "heuristic"}, map[string]string{"Origin": "https://evil.example"})
	check("llm: cross-origin settings change refused (403)", st == 403, st)

	// the pipeline now uses the model without a restart
	sample := filepath.Join(*dir, "采购部_2026年Q1设备采购报表.xlsx")
	before := chatCalls.Load()
	code, up := upload([]string{sample}, []string{"llm/采购部_2026年Q1设备采购报表.xlsx"}, "")
	var j job
	_ = json.Unmarshal(up, &j)
	check("llm: import accepted", code == 202, code)
	j = waitFor(j.ID, "awaiting_review")
	_, d, _ = call("GET", "/v1/files?task_id="+j.ID, nil, nil)
	var fl struct {
		Items []struct {
			Dept     *string  `json:"detected_department"`
			Evidence []string `json:"evidence"`
		}
	}
	_ = json.Unmarshal(d, &fl)
	used := len(fl.Items) == 1 && fl.Items[0].Dept != nil && *fl.Items[0].Dept == "法务部" && strings.Contains(strings.Join(fl.Items[0].Evidence, ""), "e2e-fake-model")
	check("llm: recognition ran through the configured model (hot swap, no restart)", used && chatCalls.Load() > before, chatCalls.Load()-before, string(d))
	if v, _ := lastAuth.Load().(string); v != "Bearer sk-e2e-secret-9876" {
		check("llm: the saved key was sent to the provider", false, v)
	} else {
		check("llm: the saved key was sent to the provider (Authorization header)", true)
	}
	st, d, _ = call("GET", "/v1/providers", nil, nil)
	check("llm: /v1/providers reflects the live engine", st == 200 && strings.Contains(string(d), "e2e-model-a") && !strings.Contains(string(d), "sk-"), st, string(d))

	// switching providers without a new key must not reuse the old provider's key
	st, d = putLLM(map[string]any{"mode": "llm", "preset_id": "qwen", "base_url": "https://dashscope.aliyuncs.com/compatible-mode/v1", "model": "qwen-plus"})
	check("llm: switching to another provider demands its own key (422 KEY_REQUIRED)", st == 422 && strings.Contains(string(d), "KEY_REQUIRED"), st, string(d))

	// leave the system in offline mode for the other checks
	st, d = putLLM(map[string]any{"mode": "heuristic"})
	check("llm: switch back to offline rules (200) keeps the form data", st == 200 && strings.Contains(string(d), `"mode":"heuristic"`) && strings.Contains(string(d), "e2e-model-a"), st, string(d))
	_, d, _ = call("GET", "/v1/audit-logs?page_size=100", nil, nil)
	check("llm: configuration changes are audited without the key", strings.Contains(string(d), "llm.config") && !strings.Contains(string(d), "sk-e2e"), "")
}
