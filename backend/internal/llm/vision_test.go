package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type visionSrv struct {
	srv      *httptest.Server
	got      atomic.Value // last decoded request
	calls    atomic.Int32
	wantAuth string
}

func newVisionSrv(t *testing.T, key string) *visionSrv {
	v := &visionSrv{wantAuth: "Bearer " + key}
	v.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if r.Header.Get("Authorization") != v.wantAuth {
			http.Error(w, "no", 401)
			return
		}
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &body)
		v.got.Store(body)
		v.calls.Add(1)
		if body.Model == "text-only-model" && strings.Contains(string(raw), "image_url") {
			http.Error(w, `{"error":"images not supported"}`, 400)
			return
		}
		if strings.Contains(string(raw), "image_url") {
			if strings.Contains(body.Model, "parts") { // some providers answer with content parts
				w.Write([]byte(`{"choices":[{"message":{"content":[{"type":"text","text":"第一行\n"},{"type":"text","text":"第二行"}]}}]}`))
				return
			}
			w.Write([]byte(`{"choices":[{"message":{"content":"  转写结果：2026年3月15日  "}}]}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(v.srv.Close)
	return v
}

func TestVisionIsOffByDefaultAndNeedsExplicitOptIn(t *testing.T) {
	v := newVisionSrv(t, "sk-vision-1234567")
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	if m.Transcriber() != nil {
		t.Fatal("fresh install: no vision")
	}
	// LLM on, vision model typed but not allowed → still nil (images must not leave without the opt-in)
	if _, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567", VisionModel: "vl-model"}); err != nil {
		t.Fatal(err)
	}
	if m.Transcriber() != nil {
		t.Fatal("a configured vision model without allow_vision must stay disabled")
	}
	cfg, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: v.srv.URL + "/v1", Model: "m", VisionModel: "vl-model", AllowVision: true})
	if err != nil || !cfg.AllowVision || cfg.VisionModel != "vl-model" {
		t.Fatalf("enable: %v %+v", err, cfg)
	}
	if m.Transcriber() == nil {
		t.Fatal("vision must be available once allowed")
	}
	// offline mode switches vision off immediately but remembers the setting
	if _, err = m.Save(ctx, Input{Mode: ModeHeuristic}); err != nil {
		t.Fatal(err)
	}
	if m.Transcriber() != nil {
		t.Fatal("offline rules must never send images anywhere")
	}
	if c := m.Config(); !c.AllowVision || c.VisionModel != "vl-model" {
		t.Fatalf("form data is preserved: %+v", c)
	}
}

func TestVisionValidation(t *testing.T) {
	v := newVisionSrv(t, "sk-vision-1234567")
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	base := Input{Mode: ModeLLM, PresetID: "custom", BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567"}
	in := base
	in.AllowVision = true
	if _, err := m.Save(ctx, in); err == nil || !strings.Contains(err.Error(), "视觉模型") {
		t.Fatalf("allow_vision without a model: %v", err)
	}
	in = base
	in.VisionModel = "bad model!"
	if _, err := m.Save(ctx, in); err == nil {
		t.Fatal("invalid vision model name must be rejected")
	}
	if m.Config().Mode != ModeHeuristic {
		t.Fatal("rejected saves change nothing")
	}
}

func TestTranscribeSendsImageContentAndParsesAnswers(t *testing.T) {
	v := newVisionSrv(t, "sk-vision-1234567")
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	if _, err := m.Save(ctx, Input{Mode: ModeLLM, PresetID: "custom", BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567", VisionModel: "vl-model", AllowVision: true}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Transcriber().Transcribe(ctx, probeImage(), "image/png", "逐字转写")
	if err != nil || out != "转写结果：2026年3月15日" {
		t.Fatalf("transcribe: %q %v", out, err)
	}
	body := v.got.Load().(struct {
		Model    string `json:"model"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	})
	var parts []map[string]any
	_ = json.Unmarshal(body.Messages[0].Content, &parts)
	if body.Model != "vl-model" || len(parts) != 2 || parts[0]["type"] != "text" || parts[1]["type"] != "image_url" {
		t.Fatalf("request shape: %+v %v", body.Model, parts)
	}
	if url := parts[1]["image_url"].(map[string]any)["url"].(string); !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("image must be inlined as a data URL: %.40s", url)
	}
	// providers that answer with content parts
	t2 := m.newTranscriber(v.srv.URL+"/v1", "vl-parts", "sk-vision-1234567", 5e9)
	if out, err = t2.Transcribe(ctx, probeImage(), "image/png", "x"); err != nil || out != "第一行\n第二行" {
		t.Fatalf("content parts: %q %v", out, err)
	}
	// guards
	if _, err = m.Transcriber().Transcribe(ctx, nil, "image/png", "x"); err == nil {
		t.Fatal("empty image")
	}
	if _, err = m.Transcriber().Transcribe(ctx, probeImage(), "image/gif", "x"); err == nil {
		t.Fatal("only png/jpeg")
	}
	if _, err = m.Transcriber().Transcribe(ctx, make([]byte, maxVisionImage+1), "image/png", "x"); err == nil {
		t.Fatal("oversize image")
	}
	// errors are readable and never contain the key
	bad := m.newTranscriber(v.srv.URL+"/v1", "vl-model", "sk-wrong-00000000", 5e9)
	if _, err = bad.Transcribe(ctx, probeImage(), "image/png", "x"); err == nil || !strings.Contains(err.Error(), "密钥无效") || strings.Contains(err.Error(), "sk-") {
		t.Fatalf("401: %v", err)
	}
}

func TestConnectionTestChecksVisionModelSeparately(t *testing.T) {
	v := newVisionSrv(t, "sk-vision-1234567")
	m, _ := newMgr(t, Seed{})
	ctx := context.Background()
	res, err := m.Test(ctx, TestInput{BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567"})
	if err != nil || res.VisionOK != nil {
		t.Fatalf("no vision model → not tested: %v %+v", err, res)
	}
	res, _ = m.Test(ctx, TestInput{BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567", VisionModel: "vl-model"})
	if !res.OK || res.VisionOK == nil || !*res.VisionOK {
		t.Fatalf("vision ok: %+v", res)
	}
	res, _ = m.Test(ctx, TestInput{BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567", VisionModel: "text-only-model"})
	if !res.OK || res.VisionOK == nil || *res.VisionOK || !strings.Contains(res.Message, "视觉模型不可用") {
		t.Fatalf("a text-only model must be reported as not usable for vision, main connection still ok: %+v", res)
	}
	if _, err = m.Test(ctx, TestInput{BaseURL: v.srv.URL + "/v1", Model: "m", APIKey: "sk-vision-1234567", VisionModel: "bad model!"}); err == nil {
		t.Fatal("invalid vision model name is a validation error")
	}
}

func TestPresetsCarryVisionSuggestions(t *testing.T) {
	by := map[string]Preset{}
	for _, p := range Presets() {
		by[p.ID] = p
		if p.VisionModels == nil {
			t.Errorf("%s: vision_models must be [] not null", p.ID)
		}
		for _, vm := range p.VisionModels {
			if validModel(vm.ID) != nil {
				t.Errorf("%s vision model %q invalid", p.ID, vm.ID)
			}
		}
	}
	if len(by["qwen"].VisionModels) == 0 || by["qwen"].VisionModels[0].ID != "qwen3.7-plus" || len(by["deepseek"].VisionModels) == 0 || len(by["openai"].VisionModels) != 0 {
		t.Fatalf("vision suggestions: qwen=%v deepseek=%v", by["qwen"].VisionModels, by["deepseek"].VisionModels)
	}
}
