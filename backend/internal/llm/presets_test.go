package llm

import (
	"regexp"
	"testing"
)

func TestShippedKeyHelperData(t *testing.T) {
	for _, id := range []string{"deepseek", "qwen", "openai"} {
		p, ok := presetByID(id)
		if !ok || len(p.KeyHosts) == 0 || p.KeyPattern == "" || p.KeyGuide == nil || len(p.KeyGuide.Steps) < 4 || p.KeyGuide.SuggestedName != "OA Agent" {
			t.Fatalf("%s: key helper data missing: %+v", id, p)
		}
	}
	re := regexp.MustCompile(mustPreset(t, "openai").KeyPattern)
	for key, want := range map[string]bool{"sk-proj-abcdefghijklmnopqrstuv": true, "sk-abcdefghijklmnopqrstuvwxyz": true, "sk-or-v1-abcdefghijklmnopqrstuvwxyz012345": false, "hello": false} {
		if re.MatchString(key) != want {
			t.Errorf("openai pattern on %q", key)
		}
	}
}

func mustPreset(t *testing.T, id string) Preset {
	p, ok := presetByID(id)
	if !ok {
		t.Fatal(id)
	}
	return p
}

func TestKeyHelperValidation(t *testing.T) {
	good := `[{"id":"x","name":"X","key_url":"https://a.example.com/k","key_hosts":[".example.com"]}]`
	if _, err := parsePresets([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"host not listed": `[{"id":"x","name":"X","key_url":"https://evil.test/k","key_hosts":[".example.com"]}]`,
		"too broad":       `[{"id":"x","name":"X","key_url":"https://a.com/k","key_hosts":[".com"]}]`,
		"wildcard":        `[{"id":"x","name":"X","key_url":"https://a.example.com/k","key_hosts":["*.example.com"]}]`,
		"http":            `[{"id":"x","name":"X","key_url":"http://a.example.com/k","key_hosts":[".example.com"]}]`,
		"userinfo":        `[{"id":"x","name":"X","key_url":"https://u@a.example.com/k","key_hosts":[".example.com"]}]`,
		"bad regexp":      `[{"id":"x","name":"X","key_pattern":"("}]`,
	} {
		if _, err := parsePresets([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if _, err := parsePresets([]byte(`[{"id":"x","name":"X","key_url":"http://127.0.0.1:9911/k","key_hosts":["127.0.0.1"]}]`)); err != nil {
		t.Errorf("loopback test host rejected: %v", err)
	}
}
