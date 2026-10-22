package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"oa-agent/backend/internal/harness"
)

const (
	maxVisionImage  = 4 << 20
	maxVisionOutput = 20000
)

// Transcriber reads a page image with the configured vision model (OpenAI-compatible image_url content).
// It is a snapshot of the settings at the time it was created; callers obtain a fresh one per use.
type Transcriber struct {
	http       *http.Client
	endpoint   string
	model, key string
	timeout    time.Duration
}

// Transcriber returns nil unless vision is switched on, configured, and the engine is usable. The page image
// leaves the machine only in that case.
func (m *Manager) Transcriber() *Transcriber {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set.Mode != ModeLLM || m.state != "ok" || !m.set.AllowVision || m.set.VisionModel == "" {
		return nil
	}
	return newTranscriber(m.http, m.set.BaseURL, m.set.VisionModel, m.key, 60*time.Second)
}

func newTranscriber(client *http.Client, base, model, key string, timeout time.Duration) *Transcriber {
	return &Transcriber{http: client, endpoint: strings.TrimRight(base, "/") + "/chat/completions", model: model, key: key, timeout: timeout}
}

// Transcribe sends one image and returns the model's text (capped). Errors are worded for users and never
// contain the key or response bodies.
func (t *Transcriber) Transcribe(ctx context.Context, img []byte, mime, instruction string) (string, error) {
	if len(img) == 0 || len(img) > maxVisionImage {
		return "", errors.New("图片大小不合适")
	}
	if mime != "image/png" && mime != "image/jpeg" {
		return "", errors.New("仅支持 PNG/JPEG 图片")
	}
	payload, _ := json.Marshal(map[string]any{
		"model": t.model, "temperature": 0, "max_tokens": 3000,
		"messages": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": instruction},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)}},
		}}},
	})
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, t.endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("请求无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	res, err := t.http.Do(req)
	if err != nil {
		return "", errors.New("无法连接视觉模型服务")
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 2<<20))
	if res.StatusCode != 200 {
		return "", errors.New(describe(&harness.ProviderError{Status: res.StatusCode}))
	}
	var body struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(raw, &body) != nil || len(body.Choices) == 0 {
		return "", errors.New("视觉模型返回格式不正确")
	}
	text := contentText(body.Choices[0].Message.Content)
	if utf8.RuneCountInString(text) > maxVisionOutput {
		text = string([]rune(text)[:maxVisionOutput])
	}
	return strings.TrimSpace(text), nil
}

// contentText accepts a plain string or an array of {type:text,text:...} parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var b strings.Builder
		for _, p := range parts {
			b.WriteString(p.Text)
		}
		return b.String()
	}
	return ""
}

// probeImage is a tiny generated PNG for the vision connection test (no document data is ever used in tests).
func probeImage() []byte {
	img := image.NewGray(image.Rect(0, 0, 96, 48))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for x := 8; x < 88; x += 16 {
		for y := 10; y < 38; y++ {
			for dx := 0; dx < 6; dx++ {
				img.SetGray(x+dx, y, color.Gray{Y: 0})
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// testVision checks that the vision model accepts an image and answers.
func testVision(ctx context.Context, client *http.Client, base, model, key string) (bool, string) {
	t := newTranscriber(client, base, model, key, 40*time.Second)
	out, err := t.Transcribe(ctx, probeImage(), "image/png", "Reply with one short word describing this image.")
	if err != nil {
		return false, "视觉模型不可用：" + err.Error()
	}
	if out == "" {
		return false, "视觉模型返回了空内容：该模型可能不支持图片输入"
	}
	return true, ""
}

func (m *Manager) newTranscriber(base, model, key string, timeout time.Duration) *Transcriber {
	return newTranscriber(m.http, base, model, key, timeout)
}
