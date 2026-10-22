// Package dingtalk is the DingTalk Drive (钉钉钉盘 / 企业存储) storage provider. It talks to the official
// enterprise-internal-app OpenAPI with the standard library only.
//
// API facts (checked against the official docs at help.dingtalk.io, which mirror open.dingtalk.com):
//   - POST /v1.0/oauth2/accessToken {appKey,appSecret} -> {accessToken, expireIn(seconds, 7200)}
//   - GET  /v1.0/storage/spaces/{spaceId}/dentries?parentId&unionId&maxResults(max 50)&nextToken, root parentId "0";
//     items carry id, uuid, name, type FILE|FOLDER, size, modifiedTime (ISO 8601), status NORMAL|DELETED|EXPIRED
//   - POST /v1.0/storage/spaces/{spaceId}/dentries/{parentId}/folders?unionId {name, option.conflictStrategy}
//   - POST /v1.0/storage/spaces/{spaceId}/dentries/{dentryId}/downloadInfos/query?unionId
//   - POST /v2.0/storage/spaces/files/{parentDentryUuid}/uploadInfos/query?unionId
//   - POST /v2.0/storage/spaces/files/{parentDentryUuid}/commit?unionId {uploadKey,name,option{size,conflictStrategy}}
//   - DELETE /v1.0/storage/spaces/{spaceId}/dentries/{dentryId}?unionId&toRecycleBin (asynchronous, returns taskId)
//
// Not verifiable from the docs and therefore marked "unverified against official docs" where used:
//   - the dentryUuid of the space ROOT (needed to upload directly into the root): we try
//     POST .../dentries/0/query; when that yields no uuid, uploads into the root fail with ErrUnsupported and the
//     user is told to set a base path inside a folder. Folders listed through the API carry their own uuid, so no
//     dentryId->dentryUuid conversion API is needed (the documented conversion API is for knowledge-base docs).
//   - the exact error-code strings for authentication failures and rate limits (only the documented codes
//     invalidClientIdOrSecret, permissionDenied, orgAuthLevelNotEnough, spaceNotExist, dentryNotExist,
//     dentryNameConflict, dentryAlreadyExist, fileArchived, *QuotaInsufficient, operationTimeout are certain).
//   - the JSON error body shape ({"code","message"}); inferred from the SDK samples in the docs.
//
// Remove uses the documented asynchronous delete API (permanent, toRecycleBin=false); the task is not polled.
// It refuses folders and exists only for the connection test's marker file.
package dingtalk

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"oa-agent/backend/internal/cloud"
)

const (
	defaultEndpoint = "https://api.dingtalk.com"
	maxJSONBody     = 4 << 20
	maxEntries      = 5000
	pageSize        = 50
	maxPages        = 200
	listTTL         = 60 * time.Second
	tokenMargin     = 5 * time.Minute
	apiTimeout      = 30 * time.Second
	maxRedirects    = 2

	// MonthlyDownloadInfoQuota is the documented monthly call quota of the download-info API on the standard edition.
	MonthlyDownloadInfoQuota = 10000
)

var (
	// ErrQuota: rate limit or monthly call quota exhausted.
	ErrQuota = errors.New("dingtalk API quota or rate limit reached")
	// ErrCapacity: the space or organization has no storage capacity left.
	ErrCapacity = errors.New("dingtalk storage capacity insufficient")
	// ErrRetryable: a transient condition (timeout, archived file being unarchived, concurrency control).
	ErrRetryable = errors.New("temporary dingtalk error, retry later")
	// ErrAmbiguous: two siblings share the same name, so the path cannot be resolved safely.
	ErrAmbiguous = errors.New("ambiguous path: several items share the same name")
	// ErrBadRequest: DingTalk rejected the request parameters or the file name.
	ErrBadRequest = errors.New("request rejected by dingtalk")
)

// Hint turns an error of this provider into a Chinese user-facing hint ("" when there is nothing specific to add).
func Hint(err error) string {
	switch {
	case errors.Is(err, ErrQuota):
		return "钉钉接口调用受限：标准版“获取文件下载信息”接口每月仅有 10,000 次调用额度，且有频率限制；大批量导入请分批进行，或升级钉钉版本后重试"
	case errors.Is(err, ErrCapacity):
		return "钉钉存储空间容量不足，请清理或扩容后重试"
	case errors.Is(err, ErrRetryable):
		return "钉钉服务暂时不可用或文件正在解冻，请稍后重试"
	case errors.Is(err, ErrAmbiguous):
		return "钉盘中存在同名项目，无法确定路径，请先重命名其中一个"
	case errors.Is(err, ErrBadRequest):
		return "钉钉拒绝了请求：请检查文件名是否含有 * \" < > 等不支持的字符或以“.”结尾"
	case errors.Is(err, cloud.ErrPermission):
		return "权限不足：请确认应用已开通企业存储读写权限、操作人对该空间有权限、服务器 IP 已加入白名单"
	case errors.Is(err, cloud.ErrAuth):
		return "认证失败：请检查 AppKey/AppSecret，应用须为企业内部应用"
	}
	return ""
}

func init() {
	cloud.Register(cloud.Kind{
		ID:          "dingtalk",
		Name:        "钉钉钉盘",
		Group:       "enterprise",
		Description: "钉钉企业存储（钉盘）：通过企业内部应用的官方 API 读取与同步文件。标准版下载接口每月有 10,000 次调用额度。",
		DocsURL:     "https://open.dingtalk.com/",
		Fields: []cloud.Field{
			{Key: "app_key", Label: "AppKey（Client ID）", Type: cloud.TText, Required: true,
				Help: "在钉钉开发者后台（open-dev.dingtalk.com）→ 应用开发 → 企业内部应用 → 选择应用 → 凭证与基础信息 中查看。应用必须是“企业内部应用”，并已开通“企业存储”的读、写权限（Storage.File.Read / Storage.File.Write、Storage.DownloadInfo.Read、Storage.UploadInfo.Read），同时在“服务器出口 IP”中加入本服务器的公网 IP 白名单。"},
			{Key: "app_secret", Label: "AppSecret（Client Secret）", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "同一页面的 AppSecret，加密保存，不会再次显示。"},
			{Key: "space_id", Label: "空间 ID（spaceId）", Type: cloud.TText, Required: true,
				Help: "钉盘空间的 ID。通过“新建空间/查询空间列表”接口返回的 id 获得（企业共享空间或应用创建的空间）。"},
			{Key: "union_id", Label: "操作人 unionId", Type: cloud.TText, Required: true,
				Help: "执行文件操作的组织成员的 unionId，该成员必须对上述空间有相应读写权限。可在“查询用户详情”接口（需通讯录权限）返回的 unionId 字段获得，或在开发者后台的 API 调试工具中查询。"},
			{Key: "endpoint", Label: "API 地址", Type: cloud.TText, Default: defaultEndpoint, Placeholder: defaultEndpoint,
				Help: "默认 https://api.dingtalk.com；国际版可填 https://api.dingtalk.io。必须是 https。"},
		},
		Factory: factory,
	})
}

type config struct {
	Endpoint, AppKey, AppSecret, SpaceID, UnionID, Base string
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	cfg := config{
		Endpoint:  s.Get("endpoint"),
		AppKey:    s.Get("app_key"),
		AppSecret: strings.TrimSpace(s.Secrets["app_secret"]),
		SpaceID:   s.Get("space_id"),
		UnionID:   s.Get("union_id"),
		Base:      s.BasePath,
	}
	p, err := newProvider(cfg, nil, time.Now)
	if err != nil {
		return nil, err
	}
	// Fail fast on wrong credentials.
	if _, err := p.getToken(ctx); err != nil {
		return nil, err
	}
	return p, nil
}

type node struct {
	id, uuid, name string
	dir            bool
	size           int64
	mod            time.Time
}

type listing struct {
	nodes     []node
	truncated bool
	at        time.Time
}

// Provider implements cloud.Provider for DingTalk Drive.
type Provider struct {
	cfg  config
	base string // endpoint without trailing slash
	api  *http.Client
	dl   *http.Client
	now  func() time.Time

	calls         atomic.Int64
	downloadCalls atomic.Int64

	tokMu sync.Mutex
	token string
	tokAt time.Time // refresh at or after this instant

	mu       sync.Mutex
	listings map[string]listing
	rootUUID string
}

// APICalls is the number of requests sent to the DingTalk API (token requests included) since the provider was created.
func (p *Provider) APICalls() int64 { return p.calls.Load() }

// DownloadInfoCalls counts calls of the quota-limited download-info API (see MonthlyDownloadInfoQuota).
func (p *Provider) DownloadInfoCalls() int64 { return p.downloadCalls.Load() }

func newProvider(cfg config, tlsCfg *tls.Config, now func() time.Time) (*Provider, error) {
	if cfg.AppKey == "" || cfg.AppSecret == "" || cfg.SpaceID == "" || cfg.UnionID == "" {
		return nil, fmt.Errorf("%w: app_key, app_secret, space_id and union_id are required", cloud.ErrAuth)
	}
	ep := cfg.Endpoint
	if ep == "" {
		ep = defaultEndpoint
	}
	u, err := url.Parse(ep)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("%w: endpoint must be an https URL", ErrBadRequest)
	}
	if err := cloud.CheckHost(u.Hostname()); err != nil {
		return nil, err
	}
	tr := func() *http.Transport {
		return &http.Transport{
			DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
			TLSClientConfig:       tlsCfg,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 60 * time.Second,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       60 * time.Second,
		}
	}
	p := &Provider{
		cfg:  cfg,
		base: strings.TrimRight(u.String(), "/"),
		now:  now,
		api: &http.Client{Transport: tr(), Timeout: apiTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		dl:       &http.Client{Transport: tr(), CheckRedirect: checkRedirect},
		listings: map[string]listing{},
	}
	return p, nil
}

// checkRedirect allows at most maxRedirects hops, https only (signed storage URLs).
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect to non-https URL refused")
	}
	return nil
}

func (p *Provider) Close() error {
	p.api.CloseIdleConnections()
	p.dl.CloseIdleConnections()
	return nil
}

// ---- errors ----

type apiError struct {
	op       string
	status   int
	code     string
	sentinel error
}

func (e *apiError) Error() string {
	s := fmt.Sprintf("dingtalk %s: %v (http %d", e.op, e.sentinel, e.status)
	if e.code != "" {
		s += ", code " + e.code
	}
	return s + ")"
}
func (e *apiError) Unwrap() error { return e.sentinel }

func safeCode(c string) string {
	var b strings.Builder
	for _, r := range c {
		if b.Len() >= 64 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func mapError(status int, code string) error {
	l := strings.ToLower(code)
	switch {
	case l == "permissiondenied" || l == "orgauthlevelnotenough" || l == "dentryuploadforbidden":
		return cloud.ErrPermission
	case l == "spacenotexist" || l == "dentrynotexist":
		return cloud.ErrNotFound
	case l == "dentrynameconflict" || l == "dentryalreadyexist":
		return cloud.ErrExists
	case strings.HasSuffix(l, "quotainsufficient"):
		return ErrCapacity
	case l == "invalidclientidorsecret", strings.Contains(l, "invalidauthentication"), strings.Contains(l, "invalidtoken"),
		strings.Contains(l, "accesstoken"), strings.HasPrefix(l, "forbidden.app"), strings.Contains(l, "authfail"): // unverified against official docs
		return cloud.ErrAuth
	case status == http.StatusTooManyRequests, strings.Contains(l, "throttl"), strings.Contains(l, "ratelimit"),
		strings.Contains(l, "qpslimit"), strings.Contains(l, "apicalllimit"), strings.Contains(l, "quotaexceed"): // unverified against official docs
		return ErrQuota
	case l == "filearchived", l == "operationtimeout", l == "operationconcurrentcontrol", status == 503:
		return ErrRetryable
	case status == http.StatusUnauthorized:
		return cloud.ErrAuth
	case status == http.StatusForbidden:
		return cloud.ErrPermission
	case status == http.StatusNotFound:
		return cloud.ErrNotFound
	case strings.HasPrefix(l, "paramerror"), l == "dentrynameillegal", status == 400:
		return ErrBadRequest
	case status >= 500:
		return ErrRetryable
	}
	return ErrBadRequest
}

func (p *Provider) netErr(ctx context.Context, op string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("dingtalk %s: %w", op, ctx.Err())
	}
	// The raw error may contain signed URLs or internal addresses; do not expose it.
	return fmt.Errorf("dingtalk %s: %w", op, cloud.ErrUnreachable)
}

// ---- HTTP plumbing ----

func (p *Provider) getToken(ctx context.Context) (string, error) {
	p.tokMu.Lock() // held while refreshing: concurrent callers wait for one request (single flight)
	defer p.tokMu.Unlock()
	if p.token != "" && p.now().Before(p.tokAt) {
		return p.token, nil
	}
	body, _ := json.Marshal(map[string]string{"appKey": p.cfg.AppKey, "appSecret": p.cfg.AppSecret})
	var out struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int64  `json:"expireIn"`
	}
	if err := p.send(ctx, "token", http.MethodPost, "/v1.0/oauth2/accessToken", nil, "", body, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("dingtalk token: %w", cloud.ErrAuth)
	}
	ttl := time.Duration(out.ExpireIn) * time.Second
	if ttl <= 0 {
		ttl = 7200 * time.Second
	}
	margin := tokenMargin
	if ttl/2 < margin {
		margin = ttl / 2
	}
	p.token = out.AccessToken
	p.tokAt = p.now().Add(ttl - margin)
	return p.token, nil
}

func (p *Provider) invalidateToken(bad string) {
	p.tokMu.Lock()
	if p.token == bad {
		p.token = ""
	}
	p.tokMu.Unlock()
}

// send performs one API request (no redirects, JSON body limited to 4 MiB).
func (p *Provider) send(ctx context.Context, op, method, path string, q url.Values, token string, body []byte, out any) error {
	u := p.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return fmt.Errorf("dingtalk %s: %w", op, ErrBadRequest)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("x-acs-dingtalk-access-token", token)
	}
	p.calls.Add(1)
	resp, err := p.api.Do(req)
	if err != nil {
		return p.netErr(ctx, op, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSONBody+1))
	if err != nil {
		return p.netErr(ctx, op, err)
	}
	if len(data) > maxJSONBody {
		return fmt.Errorf("dingtalk %s: %w (response too large)", op, ErrBadRequest)
	}
	if resp.StatusCode/100 != 2 {
		var e struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(data, &e)
		code := safeCode(e.Code)
		if resp.StatusCode/100 == 3 {
			code = ""
		}
		return &apiError{op: op, status: resp.StatusCode, code: code, sentinel: mapError(resp.StatusCode, code)}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("dingtalk %s: %w (malformed response)", op, ErrBadRequest)
		}
	}
	return nil
}

// call performs an authenticated API call; unionId is added to every request. A 401 refreshes the token once.
func (p *Provider) call(ctx context.Context, op, method, path string, q url.Values, in, out any) error {
	var body []byte
	if in != nil {
		body, _ = json.Marshal(in)
	}
	if q == nil {
		q = url.Values{}
	}
	q.Set("unionId", p.cfg.UnionID)
	for attempt := 0; ; attempt++ {
		tok, err := p.getToken(ctx)
		if err != nil {
			return err
		}
		err = p.send(ctx, op, method, path, q, tok, body, out)
		var ae *apiError
		if attempt == 0 && errors.As(err, &ae) && ae.status == http.StatusUnauthorized {
			p.invalidateToken(tok)
			continue
		}
		return err
	}
}

func (p *Provider) spacePath(parts ...string) string {
	s := "/v1.0/storage/spaces/" + url.PathEscape(p.cfg.SpaceID)
	for _, x := range parts {
		s += "/" + url.PathEscape(x)
	}
	return s
}

// ---- dentry model ----

type dentryJSON struct {
	ID           string `json:"id"`
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Size         int64  `json:"size"`
	Status       string `json:"status"`
	ModifiedTime string `json:"modifiedTime"`
}

func (d dentryJSON) node() node {
	return node{id: d.ID, uuid: d.UUID, name: d.Name, dir: d.Type == "FOLDER", size: d.Size, mod: parseTime(d.ModifiedTime)}
}

func parseTime(s string) time.Time {
	for _, l := range []string{time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02T15:04:05.999999999Z0700", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(l, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

var rootNode = node{id: "0", dir: true}

func (p *Provider) fetchChildren(ctx context.Context, parentID string) ([]node, bool, error) {
	var out []node
	token := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"parentId": {parentID}, "maxResults": {fmt.Sprint(pageSize)}}
		if token != "" {
			q.Set("nextToken", token)
		}
		var resp struct {
			Dentries  []dentryJSON `json:"dentries"`
			NextToken string       `json:"nextToken"`
		}
		if err := p.call(ctx, "list", http.MethodGet, p.spacePath("dentries"), q, nil, &resp); err != nil {
			return nil, false, err
		}
		for _, d := range resp.Dentries {
			if d.Status == "DELETED" || d.Status == "EXPIRED" || d.Name == "" {
				continue
			}
			out = append(out, d.node())
		}
		if len(out) > maxEntries {
			return out[:maxEntries], true, nil
		}
		if resp.NextToken == "" || resp.NextToken == token {
			return out, false, nil
		}
		token = resp.NextToken
	}
	return out, true, nil
}

// children returns the (cached, 60 s) listing of the folder at full path dirPath whose node is dn.
func (p *Provider) children(ctx context.Context, dirPath string, dn node) (listing, error) {
	p.mu.Lock()
	l, ok := p.listings[dirPath]
	p.mu.Unlock()
	if ok && p.now().Sub(l.at) < listTTL {
		return l, nil
	}
	ns, trunc, err := p.fetchChildren(ctx, dn.id)
	if err != nil {
		return listing{}, err
	}
	l = listing{nodes: ns, truncated: trunc, at: p.now()}
	p.mu.Lock()
	p.listings[dirPath] = l
	p.mu.Unlock()
	return l, nil
}

func (p *Provider) invalidate(dirPath string) {
	p.mu.Lock()
	delete(p.listings, dirPath)
	p.mu.Unlock()
}

func splitLast(full string) (dir, name string) {
	if i := strings.LastIndex(full, "/"); i >= 0 {
		return full[:i], full[i+1:]
	}
	return "", full
}

// pick finds the single child called name. ok=false: not present. More than one: ErrAmbiguous.
func pick(l listing, name string) (node, bool, error) {
	var found []node
	for _, n := range l.nodes {
		if n.name == name {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		if l.truncated {
			return node{}, false, fmt.Errorf("dingtalk: folder has more than %d entries: %w", maxEntries, cloud.ErrTooLarge)
		}
		return node{}, false, nil
	case 1:
		return found[0], true, nil
	}
	return node{}, false, fmt.Errorf("dingtalk: %w", ErrAmbiguous)
}

// resolve walks the path level by level from the space root.
func (p *Provider) resolve(ctx context.Context, full string) (node, error) {
	if full == "" {
		return rootNode, nil
	}
	parent, name := splitLast(full)
	pn, err := p.resolve(ctx, parent)
	if err != nil {
		return node{}, err
	}
	if !pn.dir {
		return node{}, cloud.ErrNotFound
	}
	l, err := p.children(ctx, parent, pn)
	if err != nil {
		return node{}, err
	}
	n, ok, err := pick(l, name)
	if err != nil {
		return node{}, err
	}
	if !ok {
		return node{}, cloud.ErrNotFound
	}
	return n, nil
}

func (p *Provider) full(rel string) (string, error) {
	c, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return cloud.Join(p.cfg.Base, c), nil
}

func entryOf(rel string, n node) cloud.Entry {
	name := n.name
	if rel == "" {
		name = ""
	}
	return cloud.Entry{Name: name, Path: rel, IsDir: n.dir, Size: n.size, ModTime: n.mod}
}

// ---- cloud.Provider ----

func (p *Provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	rel, err := cloud.CleanRel(dir)
	if err != nil {
		return nil, err
	}
	full := cloud.Join(p.cfg.Base, rel)
	dn, err := p.resolve(ctx, full)
	if err != nil {
		return nil, err
	}
	if !dn.dir {
		return nil, cloud.ErrNotFound
	}
	l, err := p.children(ctx, full, dn)
	if err != nil {
		return nil, err
	}
	seen := map[string]int{}
	for _, n := range l.nodes {
		seen[n.name]++
	}
	out := make([]cloud.Entry, 0, len(l.nodes))
	for _, n := range l.nodes {
		if seen[n.name] > 1 {
			return nil, fmt.Errorf("dingtalk: %w", ErrAmbiguous)
		}
		e := entryOf(cloud.Join(rel, n.name), n)
		e.Name = n.name
		out = append(out, e)
	}
	cloud.SortEntries(out)
	return out, nil
}

func (p *Provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	n, err := p.resolve(ctx, cloud.Join(p.cfg.Base, rel))
	if err != nil {
		return cloud.Entry{}, err
	}
	return entryOf(rel, n), nil
}

type headerInfo struct {
	ResourceURLs []string          `json:"resourceUrls"`
	Headers      map[string]string `json:"headers"`
}

func (p *Provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	full, err := p.full(rel)
	if err != nil {
		return nil, 0, err
	}
	n, err := p.resolve(ctx, full)
	if err != nil {
		return nil, 0, err
	}
	if n.dir || n.id == "" {
		return nil, 0, cloud.ErrNotFound
	}
	var resp struct {
		Protocol            string     `json:"protocol"`
		HeaderSignatureInfo headerInfo `json:"headerSignatureInfo"`
		headerInfo                     // the docs' field table lists these at top level; accept both
	}
	p.downloadCalls.Add(1)
	err = p.call(ctx, "download-info", http.MethodPost, p.spacePath("dentries", n.id, "downloadInfos", "query"), nil,
		map[string]any{"option": map[string]any{"preferIntranet": false}}, &resp)
	if err != nil {
		return nil, 0, err
	}
	info := resp.HeaderSignatureInfo
	if len(info.ResourceURLs) == 0 {
		info = resp.headerInfo
	}
	if len(info.ResourceURLs) == 0 {
		return nil, 0, fmt.Errorf("dingtalk download-info: %w (no download URL)", ErrBadRequest)
	}
	var lastErr error
	for _, u := range info.ResourceURLs {
		body, err := p.fetch(ctx, u, info.Headers)
		if err == nil {
			return body, n.size, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, 0, lastErr
}

func (p *Provider) fetch(ctx context.Context, rawURL string, headers map[string]string) (io.ReadCloser, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return nil, fmt.Errorf("dingtalk download: %w (non-https URL)", cloud.ErrUnreachable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dingtalk download: %w", ErrBadRequest)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := p.dl.Do(req)
	if err != nil {
		return nil, p.netErr(ctx, "download", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &apiError{op: "download", status: resp.StatusCode, sentinel: mapError(resp.StatusCode, "")}
	}
	return resp.Body, nil
}

func validName(n string) bool {
	if n == "" || strings.HasSuffix(n, ".") || len(n) > 255 {
		return false
	}
	return !strings.ContainsAny(n, "\t*\"<>`\\")
}

// parentUUID returns the dentryUuid DingTalk requires for upload calls.
func (p *Provider) parentUUID(ctx context.Context, n node) (string, error) {
	if n.id != "0" {
		if n.uuid != "" {
			return n.uuid, nil
		}
	} else {
		p.mu.Lock()
		u := p.rootUUID
		p.mu.Unlock()
		if u != "" {
			return u, nil
		}
	}
	// Unverified against official docs: whether dentryId "0" is accepted by the dentry-info API.
	var resp struct {
		Dentry dentryJSON `json:"dentry"`
	}
	err := p.call(ctx, "dentry-info", http.MethodPost, p.spacePath("dentries", n.id, "query"), nil, map[string]any{}, &resp)
	if err != nil || resp.Dentry.UUID == "" {
		return "", fmt.Errorf("dingtalk: cannot determine the folder uuid needed for uploads; set the base path to a folder inside the space: %w", cloud.ErrUnsupported)
	}
	if n.id == "0" {
		p.mu.Lock()
		p.rootUUID = resp.Dentry.UUID
		p.mu.Unlock()
	}
	return resp.Dentry.UUID, nil
}

func (p *Provider) createFolder(ctx context.Context, parent node, name string) (node, error) {
	var resp struct {
		Dentry dentryJSON `json:"dentry"`
	}
	err := p.call(ctx, "create-folder", http.MethodPost, p.spacePath("dentries", parent.id, "folders"), nil,
		map[string]any{"name": name, "option": map[string]any{"conflictStrategy": "RETURN_ERROR_IF_EXISTS"}}, &resp)
	if err != nil {
		return node{}, err
	}
	n := resp.Dentry.node()
	n.dir = true
	if n.id == "" {
		return node{}, fmt.Errorf("dingtalk create-folder: %w (no id returned)", ErrBadRequest)
	}
	return n, nil
}

// ensureDir resolves a folder path, creating missing levels.
func (p *Provider) ensureDir(ctx context.Context, full string) (node, error) {
	cur, curPath := rootNode, ""
	if full == "" {
		return cur, nil
	}
	for _, comp := range strings.Split(full, "/") {
		if !validName(comp) {
			return node{}, fmt.Errorf("dingtalk: folder name %q: %w", comp, ErrBadRequest)
		}
		l, err := p.children(ctx, curPath, cur)
		if err != nil {
			return node{}, err
		}
		n, ok, err := pick(l, comp)
		if err != nil {
			return node{}, err
		}
		if !ok {
			n, err = p.createFolder(ctx, cur, comp)
			p.invalidate(curPath)
			if errors.Is(err, cloud.ErrExists) { // lost a race: somebody created it meanwhile
				l, lerr := p.children(ctx, curPath, cur)
				if lerr != nil {
					return node{}, lerr
				}
				var found bool
				if n, found, err = pick(l, comp); err == nil && !found {
					err = cloud.ErrExists
				}
			}
			if err != nil {
				return node{}, err
			}
		}
		if !n.dir {
			return node{}, fmt.Errorf("dingtalk: %q is a file, not a folder: %w", comp, cloud.ErrExists)
		}
		cur, curPath = n, cloud.Join(curPath, comp)
	}
	return cur, nil
}

func (p *Provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if rel == "" || size < 0 {
		return fmt.Errorf("dingtalk: %w", ErrBadRequest)
	}
	dirFull, name := splitLast(full)
	if !validName(name) {
		return fmt.Errorf("dingtalk: file name: %w", ErrBadRequest)
	}
	p.invalidate(dirFull) // never trust a stale listing for the existence check
	if _, err := p.resolve(ctx, full); err == nil {
		return cloud.ErrExists
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	parent, err := p.ensureDir(ctx, dirFull)
	if err != nil {
		return err
	}
	puuid, err := p.parentUUID(ctx, parent)
	if err != nil {
		return err
	}
	var up struct {
		UploadKey           string     `json:"uploadKey"`
		HeaderSignatureInfo headerInfo `json:"headerSignatureInfo"`
	}
	err = p.call(ctx, "upload-info", http.MethodPost, "/v2.0/storage/spaces/files/"+url.PathEscape(puuid)+"/uploadInfos/query", nil,
		map[string]any{"protocol": "HEADER_SIGNATURE", "option": map[string]any{
			"storageDriver": "DINGTALK", "preferIntranet": false,
			"preCheckParam": map[string]any{"name": name, "size": size}}}, &up)
	if err != nil {
		return err
	}
	if up.UploadKey == "" || len(up.HeaderSignatureInfo.ResourceURLs) == 0 {
		return fmt.Errorf("dingtalk upload-info: %w (no upload target)", ErrBadRequest)
	}
	if err := p.put(ctx, up.HeaderSignatureInfo, r, size); err != nil {
		return err
	}
	var cm struct {
		Dentry dentryJSON `json:"dentry"`
	}
	// RETURN_ERROR_IF_EXISTS is the only strategy ever sent: it guarantees nothing is overwritten or renamed.
	err = p.call(ctx, "commit", http.MethodPost, "/v2.0/storage/spaces/files/"+url.PathEscape(puuid)+"/commit", nil,
		map[string]any{"uploadKey": up.UploadKey, "name": name, "option": map[string]any{
			"size": size, "conflictStrategy": "RETURN_ERROR_IF_EXISTS"}}, &cm)
	p.invalidate(dirFull)
	if err != nil {
		return err
	}
	if cm.Dentry.Size != size && cm.Dentry.ID != "" && cm.Dentry.Size != 0 {
		return fmt.Errorf("dingtalk commit: %w (stored size differs)", ErrBadRequest)
	}
	return nil
}

func (p *Provider) put(ctx context.Context, info headerInfo, r io.Reader, size int64) error {
	u := info.ResourceURLs[0]
	if pu, err := url.Parse(u); err != nil || pu.Scheme != "https" {
		return fmt.Errorf("dingtalk upload: %w (non-https URL)", cloud.ErrUnreachable)
	}
	body := io.LimitReader(r, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, io.NopCloser(body))
	if err != nil {
		return fmt.Errorf("dingtalk upload: %w", ErrBadRequest)
	}
	req.ContentLength = size
	if size == 0 {
		req.Body = http.NoBody
	}
	for k, v := range info.Headers {
		req.Header.Set(k, v)
	}
	resp, err := p.dl.Do(req)
	if err != nil {
		return p.netErr(ctx, "upload", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode/100 != 2 {
		return &apiError{op: "upload", status: resp.StatusCode, sentinel: mapError(resp.StatusCode, "")}
	}
	return nil
}

// Remove deletes one FILE (permanently; the API is asynchronous and the task is not polled). Folders are refused.
func (p *Provider) Remove(ctx context.Context, rel string) error {
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return cloud.ErrUnsupported
	}
	n, err := p.resolve(ctx, full)
	if err != nil {
		return err
	}
	if n.dir {
		return fmt.Errorf("dingtalk: refusing to delete a folder: %w", cloud.ErrUnsupported)
	}
	err = p.call(ctx, "delete", http.MethodDelete, p.spacePath("dentries", n.id), url.Values{"toRecycleBin": {"false"}}, nil, nil)
	dirFull, _ := splitLast(full)
	p.invalidate(dirFull)
	return err
}
