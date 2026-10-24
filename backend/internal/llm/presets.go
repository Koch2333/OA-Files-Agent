// Package llm manages the recognition engine configuration: presets (DeepSeek, Qwen, …), a validated and
// persisted user configuration (free Base URL and model), a hot-swappable harness.Provider, connection
// tests and model discovery. API keys are encrypted at rest and never returned.
package llm

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
)

type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

type Preset struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	BaseURL      string  `json:"base_url"`
	Models       []Model `json:"models"`
	DefaultModel string  `json:"default_model"`
	NeedsAPIKey  bool    `json:"needs_api_key"`
	DocsURL      string  `json:"docs_url"`
	// KeyURL is the provider page where an API key is created by hand (the "open key page" link).
	KeyURL string `json:"key_url,omitempty"`
	// OAuthLogin is true when "sign in with the platform" is implemented for this preset (set from the flow registry).
	OAuthLogin bool `json:"oauth_login"`
	// VisionModels are suggestions for the optional vision fallback (models that accept image input).
	VisionModels []Model `json:"vision_models"`
	// CheckedAt is when the suggestions were last verified against the provider's documentation.
	CheckedAt string `json:"checked_at"`
	Note      string `json:"note"`
}

// presets.json is the built-in catalog. Model names change quickly, so the catalog is data, not code: an
// administrator can replace it with OA_LLM_PRESETS_FILE (same JSON shape) without rebuilding, and the UI
// always prefers the provider's live /models list over these suggestions.
//
//go:embed presets.json
var embeddedPresets []byte

var (
	catalogMu sync.RWMutex
	catalog   []Preset
)

func init() {
	ps, err := parsePresets(embeddedPresets)
	if err != nil {
		panic("embedded presets.json is invalid: " + err.Error())
	}
	catalog = ps
}

var presetIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// parsePresets validates a catalog. DeepSeek stays first and Qwen second in the built-in file; an override may
// order them differently. A "custom" entry is appended if missing so users can always type their own endpoint.
func parsePresets(raw []byte) ([]Preset, error) {
	var ps []Preset
	if err := json.Unmarshal(raw, &ps); err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, fmt.Errorf("catalog is empty")
	}
	seen := map[string]bool{}
	hasCustom := false
	for i := range ps {
		p := &ps[i]
		if !presetIDRe.MatchString(p.ID) || seen[p.ID] {
			return nil, fmt.Errorf("preset %d: invalid or duplicate id %q", i, p.ID)
		}
		seen[p.ID] = true
		if strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("preset %s: name is required", p.ID)
		}
		if p.BaseURL != "" {
			if _, err := NormalizeBaseURL(p.BaseURL); err != nil {
				return nil, fmt.Errorf("preset %s: %v", p.ID, err)
			}
		}
		if p.ID == "custom" {
			hasCustom = true
		}
		if p.Models == nil {
			p.Models = []Model{}
		}
		if p.VisionModels == nil {
			p.VisionModels = []Model{}
		}
		for _, list := range [][]Model{p.Models, p.VisionModels} {
			for _, m := range list {
				if validModel(m.ID) != nil {
					return nil, fmt.Errorf("preset %s: invalid model id %q", p.ID, m.ID)
				}
			}
		}
		if p.DefaultModel != "" {
			if validModel(p.DefaultModel) != nil {
				return nil, fmt.Errorf("preset %s: invalid default model", p.ID)
			}
		}
	}
	if !hasCustom {
		ps = append(ps, Preset{ID: "custom", Name: "自定义（OpenAI 兼容）", Description: "任意提供 /chat/completions 的服务。", Models: []Model{}, VisionModels: []Model{}, Note: "Base URL 通常以 /v1 结尾；公网地址必须使用 https。"})
	}
	return ps, nil
}

// LoadOverride replaces the catalog with the file's content. On any error the current catalog is kept.
func LoadOverride(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	ps, err := parsePresets(raw)
	if err != nil {
		return err
	}
	catalogMu.Lock()
	catalog = ps
	catalogMu.Unlock()
	return nil
}

// Presets returns a copy of the active catalog.
func Presets() []Preset {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	out := make([]Preset, len(catalog))
	copy(out, catalog)
	for i := range out {
		_, out[i].OAuthLogin = oauthFlows[out[i].ID]
	}
	return out
}

func presetByID(id string) (Preset, bool) {
	for _, p := range Presets() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
