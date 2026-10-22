// Adapted from OpenList (AGPL-3.0) drivers/onedrive
//
// Package onedrive is the OneDrive / SharePoint (Microsoft Graph) storage provider. Only list, stat, download,
// upload (never overwriting) and single-file delete are implemented.
//
// API facts (Microsoft Graph v1.0 docs):
//   - token: POST {login}/{tenant}/oauth2/v2.0/token (grant_type=refresh_token); a new refresh_token may be returned
//   - children: GET {drive}/root:/{path}:/children (follow @odata.nextLink), item: GET {drive}/root:/{path}
//   - download: GET {drive}/root:/{path}:/content answers 302 to a short-lived pre-authenticated URL
//   - create folder: POST {drive}/root:/{parent}:/children {"name","folder":{},"@microsoft.graph.conflictBehavior":"fail"}
//   - upload <= 4 MB: PUT {drive}/root:/{path}:/content?@microsoft.graph.conflictBehavior=fail
//   - larger: POST {drive}/root:/{path}:/createUploadSession {"item":{"@microsoft.graph.conflictBehavior":"fail"}},
//     then PUT Content-Range chunks (multiples of 320 KiB) to the returned uploadUrl, without Authorization
//   - delete: DELETE {drive}/root:/{path} (204)
//
// Never overwriting relies on conflictBehavior=fail, which the service enforces atomically (HTTP 409
// nameAlreadyExists). The drive is /me/drive, /drives/{drive_id} or /sites/{site_id}/drive.
package onedrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/oauthhttp"
)

const (
	simpleLimit = 3 << 20        // below this a single PUT is used (Graph allows up to 4 MB)
	chunkSize   = 32 * 320 << 10 // 10 MiB, a multiple of 320 KiB as required by Graph
	pageSize    = 200
	maxPages    = 500
)

var (
	idRe     = regexp.MustCompile(`^[A-Za-z0-9!_.,\-]{1,300}$`)
	tenantRe = regexp.MustCompile(`^[A-Za-z0-9.\-]{1,100}$`)
)

type region struct{ login, graph string }

var regions = map[string]region{
	"global": {"https://login.microsoftonline.com", "https://graph.microsoft.com"},
	"cn":     {"https://login.chinacloudapi.cn", "https://microsoftgraph.chinacloudapi.cn"},
}

func init() {
	cloud.Register(cloud.Kind{
		ID:          "onedrive",
		Name:        "OneDrive / SharePoint",
		Group:       "cloud",
		Description: "Microsoft OneDrive、OneDrive for Business 与 SharePoint 文档库（Microsoft Graph）。使用 OAuth 刷新令牌访问，不会覆盖已有文件。",
		DocsURL:     "https://learn.microsoft.com/graph/api/resources/onedrive",
		Fields: []cloud.Field{
			{Key: "region", Label: "云环境", Type: cloud.TSelect, Default: "global",
				Options: []cloud.Option{{Value: "global", Label: "全球版（microsoft.com）"}, {Value: "cn", Label: "世纪互联（中国）"}},
				Help:    "世纪互联运营的 Microsoft 365 选择“世纪互联”。"},
			{Key: "tenant", Label: "租户", Type: cloud.TText, Default: "common", Placeholder: "common",
				Help: "租户 ID 或域名；个人账号与多租户应用填 common，仅限单一组织的应用填该组织的租户 ID。"},
			{Key: "client_id", Label: "应用程序（客户端）ID", Type: cloud.TText, Required: true,
				Help: "在 Azure 门户 → 应用注册 中创建应用，需授予委托权限 Files.ReadWrite.All 与 offline_access（SharePoint 另需 Sites.Read.All）。"},
			{Key: "client_secret", Label: "客户端密码", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "应用注册 → 证书和密码 中创建的客户端密码的值，加密保存。"},
			{Key: "refresh_token", Label: "Refresh Token", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "通过 OAuth 授权码流程取得的刷新令牌（scope 含 offline_access）。微软轮换令牌时会自动更新保存的值。"},
			{Key: "drive_id", Label: "云端硬盘 ID（可选）", Type: cloud.TText,
				Help: "留空使用当前账号自己的 OneDrive；填写则访问指定的 drive（形如 b!xxxx）。"},
			{Key: "site_id", Label: "SharePoint 站点 ID（可选）", Type: cloud.TText,
				Help: "填写后使用该站点的默认文档库（形如 contoso.sharepoint.com,guid,guid）；同时填写云端硬盘 ID 时以云端硬盘 ID 为准。"},
		},
		Factory: factory,
	})
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	p, err := New(s)
	if err != nil {
		return nil, err
	}
	if _, err := p.tokens.Get(ctx, ""); err != nil { // fail fast on wrong credentials
		p.Close()
		return nil, err
	}
	return p, nil
}

// Provider implements cloud.Provider for Microsoft Graph.
type Provider struct {
	api     string // graph base without trailing slash
	apiHost string
	drive   string // "/me/drive", "/drives/{id}" or "/sites/{id}/drive"
	base    string
	hc      *http.Client
	d       *oauthhttp.Doer
	anon    *oauthhttp.Doer
	oauth   *oauthhttp.OAuth
	tokens  *oauthhttp.TokenSource

	mu    sync.Mutex
	known map[string]bool // folders known to exist (relative to the drive root)
}

// New builds the provider without contacting the service.
func New(s cloud.Settings) (*Provider, error) {
	reg, ok := regions[s.Get("region")]
	if !ok {
		reg = regions["global"]
	}
	tenant := s.Get("tenant")
	if tenant == "" {
		tenant = "common"
	}
	if !tenantRe.MatchString(tenant) {
		return nil, errors.New("租户格式无效")
	}
	clientID, secret, refresh := s.Get("client_id"), strings.TrimSpace(s.Secrets["client_secret"]), strings.TrimSpace(s.Secrets["refresh_token"])
	if clientID == "" || secret == "" || refresh == "" {
		return nil, fmt.Errorf("%w: client_id, client_secret and refresh_token are required", cloud.ErrAuth)
	}
	base, err := oauthhttp.SafePath(s.BasePath, "")
	if err != nil {
		return nil, err
	}
	login, graph := reg.login, reg.graph
	if ep := oauthhttp.Endpoint("", s.Get("endpoint")); ep != "" { // test hook, honoured for loopback/private hosts only
		login, graph = ep, ep
	}
	drive := "/me/drive"
	switch {
	case s.Get("drive_id") != "":
		if !idRe.MatchString(s.Get("drive_id")) {
			return nil, errors.New("云端硬盘 ID 格式无效")
		}
		drive = "/drives/" + url.PathEscape(s.Get("drive_id"))
	case s.Get("site_id") != "":
		if !idRe.MatchString(s.Get("site_id")) {
			return nil, errors.New("站点 ID 格式无效")
		}
		drive = "/sites/" + url.PathEscape(s.Get("site_id")) + "/drive"
	}
	u, err := url.Parse(graph)
	if err != nil {
		return nil, err
	}
	hc := oauthhttp.NewClient()
	p := &Provider{api: graph, apiHost: u.Host, drive: drive, base: base, hc: hc, known: map[string]bool{}}
	p.oauth = oauthhttp.NewOAuth(&oauthhttp.OAuth{
		Name: "onedrive", HC: hc, TokenURL: login + "/" + tenant + "/oauth2/v2.0/token",
		ClientID: clientID, ClientSecret: secret, Persist: s.Persist,
	}, refresh)
	p.tokens = &oauthhttp.TokenSource{Refresh: p.oauth.Refresh}
	p.d = &oauthhttp.Doer{Name: "onedrive", HC: hc, Token: p.tokens.Get}
	p.anon = p.d.Anon()
	return p, nil
}

func (p *Provider) Close() error { p.hc.CloseIdleConnections(); return nil }

// ---- paths ----

func esc(full string) string {
	parts := strings.Split(full, "/")
	for i, s := range parts {
		parts[i] = strings.ReplaceAll(url.PathEscape(s), ":", "%3A")
	}
	return strings.Join(parts, "/")
}

// itemURL is the URL of the item; suffix is "", "/content" or "/children" etc.
func (p *Provider) itemURL(full, suffix string) string {
	if full == "" {
		return p.api + p.drive + "/root" + suffix
	}
	if suffix == "" {
		return p.api + p.drive + "/root:/" + esc(full)
	}
	return p.api + p.drive + "/root:/" + esc(full) + ":" + suffix
}

func (p *Provider) full(rel string) (string, error) { return oauthhttp.SafePath(p.base, rel) }

func relOf(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

// ---- API ----

type driveItem struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	Folder   *struct{} `json:"folder"`
	Modified string    `json:"lastModifiedDateTime"`
	Next     string    `json:"@odata.nextLink"`
}

func (it driveItem) entry(rel string) cloud.Entry {
	e := cloud.Entry{Name: it.Name, Path: rel, IsDir: it.Folder != nil, Size: it.Size}
	if t, err := time.Parse(time.RFC3339, it.Modified); err == nil {
		e.ModTime = t
	}
	return e
}

func (p *Provider) get(ctx context.Context, op, u string, v any) error {
	resp, err := p.d.Do(ctx, op, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if req != nil {
			req.Header.Set("Accept", "application/json")
		}
		return req, err
	})
	if err != nil {
		return err
	}
	return oauthhttp.DecodeJSON(resp, v)
}

const selectFields = "$select=id,name,size,folder,lastModifiedDateTime"

func (p *Provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	if _, err := cloud.CleanRel(dir); err != nil {
		return nil, err
	}
	full, err := p.full(dir)
	if err != nil {
		return nil, err
	}
	next := p.itemURL(full, "/children") + "?$top=" + strconv.Itoa(pageSize) + "&" + selectFields
	var out []cloud.Entry
	for page := 0; next != "" && page < maxPages; page++ {
		var res struct {
			Value []driveItem `json:"value"`
			Next  string      `json:"@odata.nextLink"`
		}
		if err := p.get(ctx, "list", next, &res); err != nil {
			return nil, err
		}
		for _, it := range res.Value {
			if it.Name != "" {
				out = append(out, it.entry(relOf(dir, it.Name)))
			}
		}
		next = res.Next
		if next != "" {
			// The continuation URL is followed with the bearer token: it must stay on the API host.
			nu, err := url.Parse(next)
			if err != nil || nu.Host != p.apiHost {
				return nil, &oauthhttp.StatusError{Provider: "onedrive", Op: "list", Kind: oauthhttp.ErrServer}
			}
		}
	}
	cloud.SortEntries(out)
	return out, nil
}

func (p *Provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	full, err := p.full(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	var it driveItem
	if err := p.get(ctx, "stat", p.itemURL(full, "")+"?"+selectFields, &it); err != nil {
		return cloud.Entry{}, err
	}
	if it.Name == "" { // the drive root has a name too, but be defensive
		it.Name = rel
	}
	return it.entry(rel), nil
}

func (p *Provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	full, err := p.full(rel)
	if err != nil || full == "" {
		if err == nil {
			err = cloud.ErrUnsupported
		}
		return nil, 0, err
	}
	u := p.itemURL(full, "/content")
	resp, err := p.d.Do(ctx, "download", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

func (p *Provider) Remove(ctx context.Context, rel string) error {
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if full == "" {
		return cloud.ErrUnsupported
	}
	e, err := p.Stat(ctx, rel)
	if err != nil {
		return err
	}
	if e.IsDir {
		return fmt.Errorf("onedrive remove: %w", cloud.ErrUnsupported) // only files are ever removed
	}
	u := p.itemURL(full, "")
	resp, err := p.d.Do(ctx, "remove", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	})
	if err != nil {
		return err
	}
	oauthhttp.Drain(resp)
	return nil
}

// conflict maps a name conflict to cloud.ErrExists.
func conflict(err error) error {
	var se *oauthhttp.StatusError
	if errors.As(err, &se) && se.Status == http.StatusConflict {
		return fmt.Errorf("onedrive: %w", cloud.ErrExists)
	}
	return err
}

// ensureDir creates every missing folder of dir (a full path relative to the drive root).
func (p *Provider) ensureDir(ctx context.Context, dir string) error {
	if dir == "" {
		return nil
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		p.mu.Lock()
		ok := p.known[cur]
		p.mu.Unlock()
		if ok {
			continue
		}
		var it driveItem
		err := p.get(ctx, "stat", p.itemURL(cur, "")+"?"+selectFields, &it)
		switch {
		case err == nil:
			if it.Folder == nil {
				return fmt.Errorf("onedrive: %w", cloud.ErrExists) // a file occupies the folder's name
			}
		case errors.Is(err, cloud.ErrNotFound):
			parent := ""
			if i > 0 {
				parent = strings.Join(parts[:i], "/")
			}
			body, _ := oauthhttp.JSONBody(map[string]any{"name": parts[i], "folder": map[string]any{}, "@microsoft.graph.conflictBehavior": "fail"})
			u := p.itemURL(parent, "/children")
			resp, err := p.d.Do(ctx, "mkdir", func(ctx context.Context) (*http.Request, error) {
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(body))
				if req != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				return req, err
			})
			if err != nil {
				var se *oauthhttp.StatusError
				if !(errors.As(err, &se) && se.Status == http.StatusConflict) { // 409: created concurrently
					return err
				}
			} else {
				oauthhttp.Drain(resp)
			}
		default:
			return err
		}
		p.mu.Lock()
		p.known[cur] = true
		p.mu.Unlock()
	}
	return nil
}

func (p *Provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if rel == "" || size < 0 {
		return cloud.ErrUnsupported
	}
	if i := strings.LastIndex(full, "/"); i >= 0 {
		if err := p.ensureDir(ctx, full[:i]); err != nil {
			return err
		}
	}
	if size <= simpleLimit {
		return p.putSimple(ctx, full, r, size)
	}
	return p.putSession(ctx, full, r, size)
}

func (p *Provider) putSimple(ctx context.Context, full string, r io.Reader, size int64) error {
	var data []byte
	if size > 0 {
		var err error
		if data, err = oauthhttp.ReadChunk(r, size); err != nil {
			return err
		}
	}
	u := p.itemURL(full, "/content") + "?@microsoft.graph.conflictBehavior=fail"
	resp, err := p.d.Do(ctx, "upload", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, oauthhttp.BytesReader(data))
		if req != nil {
			req.ContentLength = int64(len(data))
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		return req, err
	})
	if err != nil {
		return conflict(err)
	}
	oauthhttp.Drain(resp)
	return nil
}

func (p *Provider) putSession(ctx context.Context, full string, r io.Reader, size int64) (err error) {
	body, _ := oauthhttp.JSONBody(map[string]any{"item": map[string]any{"@microsoft.graph.conflictBehavior": "fail"}})
	u := p.itemURL(full, "/createUploadSession")
	resp, err := p.d.Do(ctx, "upload-session", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return conflict(err)
	}
	var sess struct {
		UploadURL string `json:"uploadUrl"`
	}
	if err := oauthhttp.DecodeJSON(resp, &sess); err != nil {
		return err
	}
	su, perr := url.Parse(sess.UploadURL)
	if perr != nil || (su.Scheme != "https" && !(su.Scheme == "http" && strings.HasPrefix(p.api, "http://"))) || su.Host == "" {
		return &oauthhttp.StatusError{Provider: "onedrive", Op: "upload-session", Kind: oauthhttp.ErrServer}
	}
	done := false
	defer func() {
		if !done { // best-effort cancel of the abandoned session
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			if cr, cerr := p.anon.Do(cctx, "upload-cancel", func(c context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(c, http.MethodDelete, sess.UploadURL, nil)
			}); cerr == nil {
				oauthhttp.Drain(cr)
			}
		}
	}()
	for off := int64(0); off < size; {
		n := min(int64(chunkSize), size-off)
		chunk, err := oauthhttp.ReadChunk(r, n)
		if err != nil {
			return err
		}
		rng := fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, size)
		cr, err := p.anon.Do(ctx, "upload-chunk", func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, sess.UploadURL, oauthhttp.BytesReader(chunk))
			if req != nil {
				req.ContentLength = n
				req.Header.Set("Content-Range", rng)
				req.Header.Set("Content-Type", "application/octet-stream")
			}
			return req, err
		})
		if err != nil {
			return conflict(err)
		}
		oauthhttp.Drain(cr)
		off += n
	}
	done = true
	return nil
}
