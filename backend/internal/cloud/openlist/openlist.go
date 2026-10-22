// Adapted from OpenList (AGPL-3.0) drivers/alist_v3 and drivers/openlist (API calls) and server/handles (fs list,
// get, put, mkdir, remove, auth login).
//
// Package openlist is the OpenList / AList v3 native-API storage provider (kind "openlist"). It talks to the
// /api/fs endpoints directly instead of going through WebDAV.
//
// API facts used (from the OpenList sources): every JSON reply is {"code","message","data"} with HTTP 200 even on
// failure; code 200 is success, 401 invalid/expired token, 402 two-factor code needed, 403 forbidden (also
// "file exists" from put with Overwrite:false), 500 generic (message "object not found" for missing paths).
//   - POST /api/auth/login {username,password,otp_code} -> data.token (JWT, sent raw in the Authorization header)
//   - POST /api/fs/list {path,password,page,per_page,refresh} -> data.content[], data.total
//   - POST /api/fs/get {path,password} -> data{name,size,is_dir,modified,raw_url,sign}
//   - POST /api/fs/mkdir {path} (recursive, idempotent), POST /api/fs/remove {dir,names}
//   - PUT /api/fs/put with headers File-Path (URL-escaped), Overwrite: false, Content-Length, raw body
//
// Race: put with "Overwrite: false" makes the server check existence first, but the check and the write are not
// atomic on the server, so a concurrent writer could still be overwritten. PutNew additionally checks Stat itself.
// Messages from the server are used only to classify errors and are never echoed.
package openlist

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

const (
	pageSize   = 500
	maxEntries = 20000
	maxPages   = 200
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "openlist",
		Name:        "OpenList / AList（原生接口）",
		Group:       "gateway",
		Description: "OpenList / AList v3 原生 API（/api/fs），可访问其挂载的各类网盘与本地存储；无需开启 WebDAV。",
		DocsURL:     "https://doc.oplist.org/",
		Fields: []cloud.Field{
			{Key: "url", Label: "服务地址", Type: cloud.TText, Required: true, Placeholder: "http://192.168.1.10:5244",
				Help: "OpenList 站点地址（不含 /api），支持 http/https 与内网地址。"},
			{Key: "auth", Label: "认证方式", Type: cloud.TSelect, Default: "password", Options: []cloud.Option{{Value: "password", Label: "用户名 + 密码"}, {Value: "token", Label: "长期令牌"}}},
			{Key: "username", Label: "用户名", Type: cloud.TText, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"}},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"}},
			{Key: "otp_secret", Label: "两步验证密钥（可选）", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"},
				Help: "账号启用两步验证时填写 Base32 密钥，系统自动生成验证码；令牌过期后可自动重新登录。"},
			{Key: "otp_code", Label: "两步验证码（可选）", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"},
				Help: "仅对首次登录有效的一次性验证码；建议改填上面的密钥。"},
			{Key: "token", Label: "访问令牌", Type: cloud.TPassword, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "token"},
				Help: "管理后台“设置 → 其他”中的令牌，或登录得到的 JWT。"},
			{Key: "meta_password", Label: "目录访问密码（可选）", Type: cloud.TPassword, Secret: true, Help: "目录设置了元信息密码时填写。"},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}},
				Help: "仅在内网自签名证书时开启。"},
		},
		Factory: New,
	})
}

type provider struct {
	c        *cloudhttp.Client
	basePath string
	username string
	password string
	otpSec   string
	otpCode  string
	metaPass string
	canLogin bool

	mu    sync.Mutex
	token string
}

// New builds the provider; no network traffic happens until the first call.
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	c, err := cloudhttp.New(s.Get("url"), s.Bool("insecure_skip_verify", false))
	if err != nil {
		return nil, err
	}
	p := &provider{c: c, basePath: s.BasePath, metaPass: s.Secrets["meta_password"]}
	if s.Get("auth") == "token" {
		p.token = s.Secrets["token"]
	} else {
		p.username, p.password = s.Get("username"), s.Secrets["password"]
		p.otpSec, p.otpCode = s.Secrets["otp_secret"], s.Secrets["otp_code"]
		p.canLogin = p.username != ""
	}
	return p, nil
}

func (p *provider) Close() error { p.c.Close(); return nil }

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// totp returns the RFC 6238 code (SHA-1, 30 s, 6 digits) for a Base32 secret.
func totp(secret string, now time.Time) (string, error) {
	secret = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.TrimRight(secret, "="))
	if err != nil {
		return "", errors.New("两步验证密钥格式无效")
	}
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(now.Unix()/30))
	h := hmac.New(sha1.New, key)
	h.Write(msg[:])
	sum := h.Sum(nil)
	off := sum[len(sum)-1] & 0xf
	v := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", v), nil
}

func classify(env envelope) error {
	msg := strings.ToLower(env.Message)
	switch env.Code {
	case 200:
		return nil
	case 401:
		return fmt.Errorf("%w", cloud.ErrAuth)
	case 402:
		return fmt.Errorf("%w（需要有效的两步验证码）", cloud.ErrAuth)
	case 403:
		if strings.Contains(msg, "exist") {
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		return fmt.Errorf("%w", cloud.ErrPermission)
	case 404:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	}
	switch {
	case strings.Contains(msg, "not found") || strings.Contains(msg, "no such"):
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case strings.Contains(msg, "already exist") || strings.Contains(msg, "file exists"):
		return fmt.Errorf("%w", cloud.ErrExists)
	case strings.Contains(msg, "permission") || strings.Contains(msg, "password is incorrect"):
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	return fmt.Errorf("服务器返回错误（code %d）", env.Code)
}

func (p *provider) tok() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.token
}

// login obtains a fresh JWT unless another goroutine already replaced the stale one.
func (p *provider) login(ctx context.Context, stale string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != stale {
		return nil
	}
	if !p.canLogin {
		return fmt.Errorf("%w", cloud.ErrAuth)
	}
	code := p.otpCode
	if p.otpSec != "" {
		c, err := totp(p.otpSec, time.Now())
		if err != nil {
			return err
		}
		code = c
	}
	body, _ := json.Marshal(map[string]string{"username": p.username, "password": p.password, "otp_code": code})
	resp, err := p.c.DoBytes(ctx, "POST", p.c.URL("/api/auth/login", nil), map[string]string{"Content-Type": "application/json"}, body)
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		cloudhttp.Drain(resp)
		if resp.StatusCode == 401 || resp.StatusCode == 403 {
			return fmt.Errorf("%w", cloud.ErrAuth)
		}
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	var env envelope
	if err := cloudhttp.ReadJSON(resp, &env); err != nil {
		return err
	}
	if env.Code != 200 {
		if env.Code == 402 {
			return classify(env)
		}
		return fmt.Errorf("%w", cloud.ErrAuth) // wrong user/password (401, or 400/403/500 on older servers)
	}
	var d struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(env.Data, &d) != nil || d.Token == "" {
		return errors.New("登录响应中没有令牌")
	}
	p.token = d.Token
	return nil
}

func (p *provider) headers() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if t := p.tok(); t != "" {
		h["Authorization"] = t
	}
	return h
}

// call performs a JSON API call, logging in first when there is no token and once more on 401.
func (p *provider) call(ctx context.Context, endpoint string, in any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, cloudhttp.APITimeout)
	defer cancel()
	body, _ := json.Marshal(in)
	if p.tok() == "" && p.canLogin {
		if err := p.login(ctx, ""); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		used := p.tok()
		resp, err := p.c.DoBytes(ctx, "POST", p.c.URL("/api/"+endpoint, nil), p.headers(), body)
		if err != nil {
			return err
		}
		var env envelope
		if resp.StatusCode != 200 && resp.StatusCode != 401 {
			cloudhttp.Drain(resp)
			return cloudhttp.StatusErr(resp.StatusCode)
		}
		if resp.StatusCode == 401 {
			cloudhttp.Drain(resp)
			env.Code = 401
		} else if err := cloudhttp.ReadJSON(resp, &env); err != nil {
			return err
		}
		if env.Code == 401 && attempt == 0 && p.canLogin {
			if err := p.login(ctx, used); err != nil {
				return err
			}
			continue
		}
		if err := classify(env); err != nil {
			return err
		}
		if out != nil && len(env.Data) > 0 {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return errors.New("服务器响应格式无法识别")
			}
		}
		return nil
	}
}

type obj struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
	Sign     string    `json:"sign"`
	RawURL   string    `json:"raw_url"`
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	abs, err := cloudhttp.Abs(p.basePath, dir)
	if err != nil {
		return nil, err
	}
	out := []cloud.Entry{}
	seen := 0
	for page := 1; page <= maxPages; page++ {
		var r struct {
			Content []obj `json:"content"`
			Total   int64 `json:"total"`
		}
		err := p.call(ctx, "fs/list", map[string]any{"path": abs, "password": p.metaPass, "page": page, "per_page": pageSize, "refresh": false}, &r)
		if err != nil {
			return nil, err
		}
		for _, o := range r.Content {
			if o.Name == "" || strings.ContainsAny(o.Name, "/\\") || o.Name == "." || o.Name == ".." {
				continue
			}
			e := cloud.Entry{Name: o.Name, Path: path.Join(dir, o.Name), IsDir: o.IsDir, Size: o.Size, ModTime: o.Modified}
			if e.IsDir {
				e.Size = 0
			}
			out = append(out, e)
		}
		seen += len(r.Content)
		if len(r.Content) == 0 || int64(seen) >= r.Total || len(out) >= maxEntries {
			break
		}
	}
	return out, nil
}

func (p *provider) get(ctx context.Context, rel string) (obj, error) {
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return obj{}, err
	}
	var o obj
	if err := p.call(ctx, "fs/get", map[string]string{"path": abs, "password": p.metaPass}, &o); err != nil {
		return obj{}, err
	}
	return o, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	o, err := p.get(ctx, rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	e := cloud.Entry{Name: path.Base(rel), Path: rel, IsDir: o.IsDir, Size: o.Size, ModTime: o.Modified}
	if rel == "" {
		e.Name = ""
	}
	if e.IsDir {
		e.Size = 0
	}
	return e, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	o, err := p.get(ctx, rel)
	if err != nil {
		return nil, 0, err
	}
	if o.IsDir {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	abs, _ := cloudhttp.Abs(p.basePath, rel)
	link := o.RawURL
	if u, err := url.Parse(link); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		// empty or relative raw_url: use the server's own download endpoint
		q := url.Values{}
		if o.Sign != "" {
			q.Set("sign", o.Sign)
		}
		link = p.c.URL("/d"+cloudhttp.EscapePath(abs), q)
	}
	hdr := map[string]string{}
	if t := p.tok(); t != "" && p.c.SameHost(link) {
		hdr["Authorization"] = t // other hosts (direct links) never receive the token
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := p.c.Do(ctx, "GET", link, hdr, nil, -1)
	if err != nil {
		cancel()
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		cloudhttp.Drain(resp)
		cancel()
		return nil, 0, cloudhttp.StatusErr(resp.StatusCode)
	}
	return cloudhttp.BodyCloser{Reader: resp.Body, C: resp.Body, Cancel: cancel}, resp.ContentLength, nil
}

func (p *provider) mkdir(ctx context.Context, rel string) error {
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return err
	}
	return p.call(ctx, "fs/mkdir", map[string]string{"path": abs}, nil)
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	if _, err := p.Stat(ctx, rel); err == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	if dir := path.Dir(rel); dir != "." {
		if err := p.mkdir(ctx, dir); err != nil {
			return err
		}
	}
	if p.tok() == "" && p.canLogin {
		if err := p.login(ctx, ""); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		used := p.tok()
		body := io.Reader(&cloudhttp.CountReader{R: io.LimitReader(r, size+1)})
		if size == 0 {
			body = http.NoBody
		}
		hdr := map[string]string{
			"File-Path":    url.PathEscape(abs),
			"Overwrite":    "false",
			"Content-Type": "application/octet-stream",
			"Password":     p.metaPass,
		}
		if used != "" {
			hdr["Authorization"] = used
		}
		cr, _ := body.(*cloudhttp.CountReader)
		resp, err := p.c.Do(ctx, "PUT", p.c.URL("/api/fs/put", nil), hdr, body, size)
		if err != nil {
			return err
		}
		if cr != nil && cr.N > size {
			cloudhttp.Drain(resp)
			p.cleanup(ctx, rel)
			return errors.New("源文件比声明的长度更长，已中止上传")
		}
		var env envelope
		switch resp.StatusCode {
		case 200:
			if err := cloudhttp.ReadJSON(resp, &env); err != nil {
				return err
			}
		case 401:
			cloudhttp.Drain(resp)
			env.Code = 401
		default:
			cloudhttp.Drain(resp)
			return cloudhttp.StatusErr(resp.StatusCode)
		}
		if env.Code == 401 && attempt == 0 && p.canLogin && size == 0 {
			// only a body-less upload can be replayed; streamed bodies are consumed
			if err := p.login(ctx, used); err != nil {
				return err
			}
			continue
		}
		if err := classify(env); err != nil {
			return err
		}
		break
	}
	got, err := p.Stat(ctx, rel)
	if err != nil {
		return err
	}
	if got.Size != size {
		p.cleanup(ctx, rel)
		return fmt.Errorf("上传后大小不一致（%d != %d）", got.Size, size)
	}
	return nil
}

func (p *provider) cleanup(ctx context.Context, rel string) { _ = p.Remove(ctx, rel) }

// Remove deletes a single file (never a folder).
func (p *provider) Remove(ctx context.Context, rel string) error {
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	o, err := p.get(ctx, rel)
	if err != nil {
		return err
	}
	if o.IsDir {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	abs, _ := cloudhttp.Abs(p.basePath, rel)
	return p.call(ctx, "fs/remove", map[string]any{"dir": path.Dir(abs), "names": []string{path.Base(abs)}}, nil)
}
