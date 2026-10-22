// Adapted from OpenList (AGPL-3.0) drivers/seafile
//
// Package seafile is the Seafile storage provider (kind "seafile") using the classic /api2 web API.
//
// API facts used (OpenList driver + Seafile web API docs):
//   - POST /api2/auth-token/ (form username, password) -> {"token"}; requests carry "Authorization: Token <t>"
//   - GET  /api2/repos/ -> libraries [{id,name,encrypted,...}]
//   - GET  /api2/repos/{repo}/dir/?p=/path -> [{type:"dir"|"file",name,size,mtime(unix)}]; 404 when missing
//   - GET  /api2/repos/{repo}/file/?p=/path&reuse=1 -> a JSON string with a one-time download URL on the file server
//   - GET  /api2/repos/{repo}/upload-link/?p=/dir -> JSON string upload URL; multipart POST (fields parent_dir,
//     replace, file) to <url>?ret-json=1 -> [{name,id,size}]
//   - POST /api2/repos/{repo}/dir/?p=/path (form operation=mkdir); DELETE /api2/repos/{repo}/file/?p=/path
//
// Never overwriting: upload uses replace=0. Seafile's documented behaviour for replace=0 on an existing name is to
// store the upload under a new name ("a (1).txt") rather than fail, so PutNew checks existence first and, when the
// server still reports a different stored name (a concurrent creator won the race), deletes the stray copy it
// just created and returns ErrExists. Older servers answer HTTP 441 for an existing file; that maps to ErrExists too.
// Encrypted libraries are not supported. Credentials are never sent to the (possibly different) file-server host.
package seafile

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "seafile",
		Name:        "Seafile",
		Group:       "enterprise",
		Description: "Seafile 私有云：通过账号令牌或用户名密码访问指定资料库及其子目录。",
		DocsURL:     "https://manual.seafile.com/latest/develop/web_api_v2.1/",
		Fields: []cloud.Field{
			{Key: "url", Label: "服务地址", Type: cloud.TText, Required: true, Placeholder: "https://seafile.example.com",
				Help: "Seafile 站点地址（含端口，不含路径），支持 http/https 与内网地址。"},
			{Key: "auth", Label: "认证方式", Type: cloud.TSelect, Default: "password", Options: []cloud.Option{{Value: "password", Label: "用户名 + 密码"}, {Value: "token", Label: "账号令牌"}}},
			{Key: "username", Label: "用户名（邮箱）", Type: cloud.TText, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"}},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"},
				Help: "不支持启用了两步验证的账号，请改用账号令牌。"},
			{Key: "token", Label: "账号令牌", Type: cloud.TPassword, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "token"},
				Help: "个人设置中的 API Token。"},
			{Key: "library", Label: "资料库（ID 或名称）", Type: cloud.TText, Required: true, Help: "资料库名称或 ID；子目录请填在“基础路径”中。不支持加密资料库。"},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}},
				Help: "仅在内网自签名证书时开启。"},
		},
		Factory: New,
	})
}

type provider struct {
	c        *cloudhttp.Client
	basePath string
	library  string
	username string
	password string
	canLogin bool

	mu    sync.Mutex
	token string
	repo  string // resolved library id
}

// New builds the provider (no network traffic).
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	c, err := cloudhttp.New(s.Get("url"), s.Bool("insecure_skip_verify", false))
	if err != nil {
		return nil, err
	}
	if s.Get("library") == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“资料库（ID 或名称）”"}
	}
	p := &provider{c: c, basePath: s.BasePath, library: s.Get("library")}
	if s.Get("auth") == "token" {
		p.token = s.Secrets["token"]
	} else {
		p.username, p.password = s.Get("username"), s.Secrets["password"]
		p.canLogin = p.username != ""
	}
	return p, nil
}

func (p *provider) Close() error { p.c.Close(); return nil }

func (p *provider) tok() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.token
}

func (p *provider) login(ctx context.Context, stale string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token != stale {
		return nil
	}
	if !p.canLogin {
		return fmt.Errorf("%w", cloud.ErrAuth)
	}
	form := url.Values{"username": {p.username}, "password": {p.password}}
	resp, err := p.c.DoBytes(ctx, "POST", p.c.URL("/api2/auth-token/", nil), map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, []byte(form.Encode()))
	if err != nil {
		return err
	}
	if resp.StatusCode != 200 {
		cloudhttp.Drain(resp)
		if resp.StatusCode == 400 || resp.StatusCode == 401 || resp.StatusCode == 403 {
			return fmt.Errorf("%w", cloud.ErrAuth)
		}
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	var t struct {
		Token string `json:"token"`
	}
	if err := cloudhttp.ReadJSON(resp, &t); err != nil {
		return err
	}
	if t.Token == "" {
		return errors.New("登录响应中没有令牌")
	}
	p.token = t.Token
	return nil
}

// api sends one authenticated request to the API (path under the base URL), logging in on demand and once on 401.
// The caller owns a non-nil response body.
func (p *provider) api(ctx context.Context, method, apiPath string, q url.Values, form url.Values) (*http.Response, error) {
	if p.tok() == "" && p.canLogin {
		if err := p.login(ctx, ""); err != nil {
			return nil, err
		}
	}
	var body []byte
	hdr := map[string]string{}
	if form != nil {
		body = []byte(form.Encode())
		hdr["Content-Type"] = "application/x-www-form-urlencoded"
	}
	for attempt := 0; ; attempt++ {
		used := p.tok()
		if used != "" {
			hdr["Authorization"] = "Token " + used
		}
		resp, err := p.c.DoBytes(ctx, method, p.c.URL(apiPath, q), hdr, body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == 401 && attempt == 0 && p.canLogin {
			cloudhttp.Drain(resp)
			if err := p.login(ctx, used); err != nil {
				return nil, err
			}
			continue
		}
		return resp, nil
	}
}

// jsonCall performs api and decodes a 2xx JSON reply into out; other statuses map to cloud errors.
func (p *provider) jsonCall(ctx context.Context, method, apiPath string, q, form url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, cloudhttp.APITimeout)
	defer cancel()
	resp, err := p.api(ctx, method, apiPath, q, form)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		cloudhttp.Drain(resp)
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	if out == nil {
		cloudhttp.Drain(resp)
		return nil
	}
	return cloudhttp.ReadJSON(resp, out)
}

func (p *provider) repoID(ctx context.Context) (string, error) {
	p.mu.Lock()
	r := p.repo
	p.mu.Unlock()
	if r != "" {
		return r, nil
	}
	var repos []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Encrypted bool   `json:"encrypted"`
	}
	if err := p.jsonCall(ctx, "GET", "/api2/repos/", nil, nil, &repos); err != nil {
		return "", err
	}
	var hit []string
	for _, x := range repos {
		if x.ID == p.library {
			hit = []string{x.ID}
			break
		}
		if x.Name == p.library {
			if x.Encrypted {
				return "", errors.New("不支持加密资料库")
			}
			hit = append(hit, x.ID)
		}
	}
	switch len(hit) {
	case 0:
		return "", fmt.Errorf("%w", cloud.ErrNotFound)
	case 1:
		p.mu.Lock()
		p.repo = hit[0]
		p.mu.Unlock()
		return hit[0], nil
	}
	return "", errors.New("存在多个同名资料库，请改用资料库 ID")
}

type item struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

func (p *provider) dir(ctx context.Context, repo, abs string) ([]item, error) {
	var items []item
	if err := p.jsonCall(ctx, "GET", "/api2/repos/"+url.PathEscape(repo)+"/dir/", url.Values{"p": {abs}}, nil, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func entryOf(it item, rel string) cloud.Entry {
	e := cloud.Entry{Name: it.Name, Path: rel, IsDir: it.Type == "dir", Size: it.Size}
	if it.Mtime > 0 {
		e.ModTime = time.Unix(it.Mtime, 0)
	}
	if e.IsDir {
		e.Size = 0
	}
	return e
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	abs, err := cloudhttp.Abs(p.basePath, dir)
	if err != nil {
		return nil, err
	}
	repo, err := p.repoID(ctx)
	if err != nil {
		return nil, err
	}
	items, err := p.dir(ctx, repo, abs)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Entry, 0, len(items))
	for _, it := range items {
		if it.Name == "" || strings.Contains(it.Name, "/") || it.Name == "." || it.Name == ".." {
			continue
		}
		out = append(out, entryOf(it, path.Join(dir, it.Name)))
	}
	return out, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	if _, err := cloudhttp.Abs(p.basePath, rel); err != nil {
		return cloud.Entry{}, err
	}
	if rel == "" {
		if _, err := p.List(ctx, ""); err != nil {
			return cloud.Entry{}, err
		}
		return cloud.Entry{IsDir: true}, nil
	}
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	}
	abs, _ := cloudhttp.Abs(p.basePath, parent)
	repo, err := p.repoID(ctx)
	if err != nil {
		return cloud.Entry{}, err
	}
	items, err := p.dir(ctx, repo, abs)
	if err != nil {
		return cloud.Entry{}, err
	}
	for _, it := range items {
		if it.Name == path.Base(rel) {
			return entryOf(it, rel), nil
		}
	}
	return cloud.Entry{}, fmt.Errorf("%w", cloud.ErrNotFound)
}

// quoted decodes a JSON string reply such as the download or upload link.
func quoted(resp *http.Response) (string, error) {
	var s string
	if err := cloudhttp.ReadJSON(resp, &s); err != nil {
		return "", err
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("服务器返回的链接无效")
	}
	return s, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return nil, 0, err
	}
	repo, err := p.repoID(ctx)
	if err != nil {
		return nil, 0, err
	}
	lctx, cancel := context.WithTimeout(ctx, cloudhttp.APITimeout)
	resp, err := p.api(lctx, "GET", "/api2/repos/"+url.PathEscape(repo)+"/file/", url.Values{"p": {abs}, "reuse": {"1"}}, nil)
	if err != nil {
		cancel()
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		cloudhttp.Drain(resp)
		cancel()
		return nil, 0, cloudhttp.StatusErr(resp.StatusCode)
	}
	link, err := quoted(resp)
	cancel()
	if err != nil {
		return nil, 0, err
	}
	hdr := map[string]string{}
	if t := p.tok(); t != "" && p.c.SameHost(link) {
		hdr["Authorization"] = "Token " + t
	}
	dctx, dcancel := context.WithCancel(ctx)
	dresp, err := p.c.Do(dctx, "GET", link, hdr, nil, -1)
	if err != nil {
		dcancel()
		return nil, 0, err
	}
	if dresp.StatusCode != 200 {
		cloudhttp.Drain(dresp)
		dcancel()
		return nil, 0, cloudhttp.StatusErr(dresp.StatusCode)
	}
	return cloudhttp.BodyCloser{Reader: dresp.Body, C: dresp.Body, Cancel: dcancel}, dresp.ContentLength, nil
}

func (p *provider) mkdirs(ctx context.Context, repo, rel string) error {
	cur := ""
	for _, seg := range strings.Split(rel, "/") {
		cur = path.Join(cur, seg)
		e, err := p.Stat(ctx, cur)
		if err == nil {
			if !e.IsDir {
				return fmt.Errorf("%w", cloud.ErrExists)
			}
			continue
		}
		if !errors.Is(err, cloud.ErrNotFound) {
			return err
		}
		abs, _ := cloudhttp.Abs(p.basePath, cur)
		err = p.jsonCall(ctx, "POST", "/api2/repos/"+url.PathEscape(repo)+"/dir/", url.Values{"p": {abs}}, url.Values{"operation": {"mkdir"}}, nil)
		if err != nil {
			// a concurrent creator may have won; accept when the folder exists now
			if e2, e2err := p.Stat(ctx, cur); e2err == nil && e2.IsDir {
				continue
			}
			return err
		}
	}
	return nil
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	_, err := cloudhttp.Abs(p.basePath, rel)
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
	repo, err := p.repoID(ctx)
	if err != nil {
		return err
	}
	parentRel := path.Dir(rel)
	if parentRel == "." {
		parentRel = ""
	} else if err := p.mkdirs(ctx, repo, parentRel); err != nil {
		return err
	}
	parentAbs, _ := cloudhttp.Abs(p.basePath, parentRel)
	name := path.Base(rel)

	lctx, cancel := context.WithTimeout(ctx, cloudhttp.APITimeout)
	resp, err := p.api(lctx, "GET", "/api2/repos/"+url.PathEscape(repo)+"/upload-link/", url.Values{"p": {parentAbs}}, nil)
	if err != nil {
		cancel()
		return err
	}
	if resp.StatusCode != 200 {
		cloudhttp.Drain(resp)
		cancel()
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	link, err := quoted(resp)
	cancel()
	if err != nil {
		return err
	}
	if u, _ := url.Parse(link); u != nil {
		q := u.Query()
		q.Set("ret-json", "1")
		u.RawQuery = q.Encode()
		link = u.String()
	}

	// multipart envelope with an exact Content-Length so the file is streamed, not buffered
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	var b [12]byte
	rand.Read(b[:])
	_ = mw.SetBoundary("oaagent" + hex.EncodeToString(b[:]))
	mw.WriteField("parent_dir", parentAbs)
	mw.WriteField("replace", "0")
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeQuotes(name)))
	h.Set("Content-Type", "application/octet-stream")
	if _, err := mw.CreatePart(h); err != nil {
		return err
	}
	tail := []byte("\r\n--" + mw.Boundary() + "--\r\n")
	cr := &cloudhttp.CountReader{R: io.LimitReader(r, size+1)}
	body := io.MultiReader(bytes.NewReader(head.Bytes()), cr, bytes.NewReader(tail))
	total := int64(head.Len()) + size + int64(len(tail))
	resp, err = p.c.Do(ctx, "POST", link, map[string]string{"Content-Type": mw.FormDataContentType()}, body, total)
	if err != nil {
		return err
	}
	if cr.N > size {
		cloudhttp.Drain(resp)
		_ = p.Remove(ctx, rel)
		return errors.New("源文件比声明的长度更长，已中止上传")
	}
	switch {
	case resp.StatusCode == 441:
		cloudhttp.Drain(resp)
		return fmt.Errorf("%w", cloud.ErrExists)
	case resp.StatusCode != 200:
		cloudhttp.Drain(resp)
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	var res []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}
	if err := cloudhttp.ReadJSON(resp, &res); err != nil {
		return err
	}
	if len(res) != 1 {
		return errors.New("服务器上传响应异常")
	}
	if res[0].Name != name {
		// replace=0 stored the file under a different name: someone created the target meanwhile
		stray := path.Join(parentRel, res[0].Name)
		_ = p.Remove(ctx, stray)
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	if res[0].Size != 0 && res[0].Size != size {
		_ = p.Remove(ctx, rel)
		return fmt.Errorf("上传后大小不一致（%d != %d）", res[0].Size, size)
	}
	return nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"", "\r", "", "\n", "").Replace(s)
}

// Remove deletes a single file (never a folder).
func (p *provider) Remove(ctx context.Context, rel string) error {
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return err
	}
	e, err := p.Stat(ctx, rel)
	if err != nil {
		return err
	}
	if e.IsDir {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	repo, err := p.repoID(ctx)
	if err != nil {
		return err
	}
	return p.jsonCallOK(ctx, "DELETE", "/api2/repos/"+url.PathEscape(repo)+"/file/", url.Values{"p": {abs}})
}

// jsonCallOK is jsonCall for replies whose body is irrelevant ("success" strings).
func (p *provider) jsonCallOK(ctx context.Context, method, apiPath string, q url.Values) error {
	return p.jsonCall(ctx, method, apiPath, q, nil, nil)
}
