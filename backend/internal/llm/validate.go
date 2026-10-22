package llm

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// ValidationError is a user-correctable configuration problem (HTTP 422).
type ValidationError struct{ Code, Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func bad(code, msg string) error { return &ValidationError{code, msg} }

var modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/@+\-]{0,119}$`)

// NormalizeBaseURL validates and canonicalises a base URL:
//   - http(s) only, no credentials, query or fragment;
//   - link-local/metadata (169.254/16, fe80::/10) and unspecified addresses are never allowed;
//   - plain http is accepted only for localhost and private/loopback addresses (so keys never cross the internet in clear).
func NormalizeBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", bad("INVALID_BASE_URL", "请填写 Base URL")
	}
	if len(raw) > 300 {
		return "", bad("INVALID_BASE_URL", "Base URL 过长")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", bad("INVALID_BASE_URL", "Base URL 格式不正确，应形如 https://api.example.com/v1")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", bad("INVALID_BASE_URL", "Base URL 只支持 http 或 https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", bad("INVALID_BASE_URL", "Base URL 不能包含用户名密码、查询参数或锚点")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return "", bad("INVALID_BASE_URL", "Base URL 不能包含空白或控制字符")
		}
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	if ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return "", bad("INVALID_BASE_URL", "不允许使用链路本地/未指定地址")
	}
	if u.Scheme == "http" {
		local := host == "localhost" || (ip != nil && (ip.IsLoopback() || ip.IsPrivate()))
		if !local {
			return "", bad("INVALID_BASE_URL", "公网地址必须使用 https（http 仅允许本机或内网地址），以免密钥明文传输")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), nil
}

func validModel(m string) error {
	m = strings.TrimSpace(m)
	if m == "" {
		return bad("MODEL_REQUIRED", "请填写或选择模型名称")
	}
	if !modelRe.MatchString(m) {
		return bad("INVALID_MODEL", "模型名称只能包含字母、数字及 . _ : / @ + -")
	}
	return nil
}

func validKey(k string) error {
	if len(k) < 8 || len(k) > 400 {
		return bad("INVALID_API_KEY", "API Key 长度不正确")
	}
	for _, r := range k {
		if unicode.IsSpace(r) || unicode.IsControl(r) || r > 126 {
			return bad("INVALID_API_KEY", "API Key 不能包含空格、换行或非 ASCII 字符（请检查是否粘贴了多余内容）")
		}
	}
	return nil
}

// KeyHint shows only the tail of a key.
func KeyHint(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "••••"
	}
	return "••••" + k[len(k)-4:]
}
