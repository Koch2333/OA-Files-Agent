// Adapted from OpenList (AGPL-3.0) drivers/baidu_netdisk
//
// Package baidupan is the 百度网盘 (baidu_netdisk) storage provider on the official Baidu Pan open platform
// (xpan). The user supplies their own app key / secret key and OAuth refresh token; OpenList's online token
// relay and its "crack" download modes are intentionally not supported.
//
// API facts (from the open platform docs / OpenList; none checked against a live account):
//   - GET https://openapi.baidu.com/oauth/2.0/token?grant_type=refresh_token&refresh_token&client_id&client_secret
//     -> {access_token, refresh_token (ROTATES), expires_in}; errors {error, error_description}
//   - GET  /rest/2.0/xpan/file?method=list&dir=<absolute path>&start&limit   (missing dir: errno -9)
//   - GET  /rest/2.0/xpan/multimedia?method=filemetas&fsids=[id]&dlink=1 -> list[0].dlink; the dlink is fetched with
//     &access_token=… and User-Agent "pan.baidu.com" and answers with a redirect to the CDN
//   - POST /rest/2.0/xpan/file?method=precreate (path,size,isdir=0,autoinit=1,rtype=0,block_list=["md5",…])
//   - POST https://d.pcs.baidu.com/rest/2.0/pcs/superfile2?method=upload&type=tmpfile&path&uploadid&partseq (multipart "file")
//   - POST /rest/2.0/xpan/file?method=create (path,size,isdir,uploadid,block_list,rtype=0)
//   - POST /rest/2.0/xpan/file?method=filemanager&opera=delete (async=0, filelist=["path"]); moves to the recycle bin
//
// Never overwrites: rtype=0 ("do not rename, return a conflict") on precreate and create after an existence
// check; errno -8/31061 map to cloud.ErrExists, and a precreate that does not report a brand-new file
// (return_type != 1) is refused as an existing file.
//
// Apps with "app folder" permission may only touch /apps/<app name>/…; set the connection base path to that.
// Baidu rejects empty files, so PutNew refuses size 0 with ErrUnsupported. The upload domain is the fixed
// d.pcs.baidu.com (the dynamic "locateupload" lookup is not used).
package baidupan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/panhttp"
)

// Variables so tests can point the provider at an httptest server.
var (
	oauthURL = "https://openapi.baidu.com/oauth/2.0/token"
	apiBase  = "https://pan.baidu.com/rest/2.0"
	pcsBase  = "https://d.pcs.baidu.com"
)

const (
	apiTimeout  = 30 * time.Second
	listLimit   = 1000
	maxPages    = 1000
	sliceSize   = 4 << 20
	maxSlices   = 2048
	tokenMargin = 5 * time.Minute
	userAgent   = "pan.baidu.com"
)

var (
	errTokenExpired = errors.New("baidupan: access token expired")
	errRate         = errors.New("baidupan: rate limited")
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "baidu_netdisk",
		Name:        "百度网盘",
		Group:       "cloud",
		Description: "百度网盘开放平台（官方 xpan API）：使用自己申请的应用 AppKey/SecretKey 与 refresh_token。",
		DocsURL:     "https://pan.baidu.com/union/doc/",
		Fields: []cloud.Field{
			{Key: "app_key", Label: "AppKey", Type: cloud.TText, Required: true,
				Help: "在百度网盘开放平台（pan.baidu.com/union）创建应用后获得。应用为“应用目录”权限时，只能访问 /apps/应用名/ 下的文件，请把“基础路径”设为 apps/应用名。"},
			{Key: "app_secret", Label: "SecretKey", Type: cloud.TPassword, Required: true, Secret: true},
			{Key: "refresh_token", Label: "Refresh Token", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "通过官方 OAuth 授权获得。每次刷新后令牌会轮换，系统会自动保存新的令牌。"},
		},
		Factory: New,
	})
}

type provider struct {
	hc      *http.Client
	dl      *http.Client
	base    string // base path without leading slash
	appKey  string
	secret  string
	persist func(key, value string)

	mu      sync.Mutex
	refresh string
	access  string
	exp     time.Time
}

// New connects and exchanges the refresh token (fails fast on wrong credentials).
func New(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	p := &provider{
		hc:      panhttp.NewClient(apiTimeout),
		dl:      panhttp.NewClient(0),
		base:    s.BasePath,
		appKey:  s.Get("app_key"),
		secret:  strings.TrimSpace(s.Secrets["app_secret"]),
		refresh: strings.TrimSpace(s.Secrets["refresh_token"]),
		persist: s.Persist,
	}
	if p.appKey == "" || p.secret == "" || p.refresh == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写 AppKey、SecretKey 与 Refresh Token"}
	}
	if _, err := p.token(ctx, ""); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *provider) Close() error {
	p.hc.CloseIdleConnections()
	p.dl.CloseIdleConnections()
	return nil
}

func (p *provider) token(ctx context.Context, bad string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.access != "" && p.access != bad && time.Now().Before(p.exp.Add(-tokenMargin)) {
		return p.access, nil
	}
	if bad != "" && p.access != "" && p.access != bad {
		return p.access, nil
	}
	q := url.Values{
		"grant_type": {"refresh_token"}, "refresh_token": {p.refresh}, "client_id": {p.appKey}, "client_secret": {p.secret},
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, oauthURL+"?"+q.Encode(), nil)
		if err != nil {
			return "", fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		resp, err := panhttp.Do(ctx, p.hc, req)
		if err != nil {
			return "", err
		}
		raw, err := panhttp.ReadBody(resp, panhttp.MaxJSON)
		if err != nil {
			return "", err
		}
		if resp.StatusCode == 429 && attempt < panhttp.MaxAttempts {
			if err := panhttp.Sleep(ctx, panhttp.Backoff(attempt)); err != nil {
				return "", err
			}
			continue
		}
		if resp.StatusCode >= 300 || json.Unmarshal(raw, &out) != nil || out.AccessToken == "" {
			return "", fmt.Errorf("百度网盘：刷新令牌失败，请检查 AppKey/SecretKey 与 Refresh Token 是否有效: %w", cloud.ErrAuth)
		}
		break
	}
	p.access = out.AccessToken
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 2592000
	}
	p.exp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	if out.RefreshToken != "" && out.RefreshToken != p.refresh {
		p.refresh = out.RefreshToken // the old one is invalidated: keep working in memory even if persisting fails
		if p.persist != nil {
			p.persist("refresh_token", out.RefreshToken)
		}
	}
	return p.access, nil
}

func mapErrno(n int) error {
	switch n {
	case 0:
		return nil
	case -6, 110, 111, 31045:
		return errTokenExpired
	case 31034, 2131:
		return errRate
	case -9, 31066:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case -8, 31061:
		return fmt.Errorf("%w", cloud.ErrExists)
	case -7, 31064:
		return fmt.Errorf("百度网盘：没有权限访问该路径（应用目录权限只允许 /apps/应用名/）: %w", cloud.ErrPermission)
	case -10:
		return errors.New("百度网盘：空间容量不足")
	}
	return fmt.Errorf("百度网盘：服务返回错误（errno %d）", n)
}

// withToken runs fn with a valid token: one refresh-and-retry on token expiry, MaxAttempts on rate limiting.
func (p *provider) withToken(ctx context.Context, fn func(tok string) error) error {
	bad, refreshed := "", false
	for attempt := 1; ; {
		tok, err := p.token(ctx, bad)
		if err != nil {
			return err
		}
		err = fn(tok)
		switch {
		case errors.Is(err, errTokenExpired):
			if refreshed {
				return fmt.Errorf("百度网盘：访问令牌无效: %w", cloud.ErrAuth)
			}
			bad, refreshed = tok, true
		case errors.Is(err, errRate):
			if attempt >= panhttp.MaxAttempts {
				return errors.New("百度网盘：请求过于频繁，请稍后重试")
			}
			if err := panhttp.Sleep(ctx, panhttp.Backoff(attempt)); err != nil {
				return err
			}
			attempt++
		default:
			return err
		}
	}
}

type errBody struct {
	Errno     int `json:"errno"`
	ErrorCode int `json:"error_code"`
}

// parse maps an HTTP response (already read) to an error or decodes it into out.
func parse(status int, raw []byte, out any) error {
	var eb errBody
	jerr := json.Unmarshal(raw, &eb)
	if jerr == nil {
		if eb.Errno != 0 {
			return mapErrno(eb.Errno)
		}
		if eb.ErrorCode != 0 {
			return mapErrno(eb.ErrorCode)
		}
	}
	if status == 401 {
		return errTokenExpired
	}
	if status == 429 {
		return errRate
	}
	if status >= 300 {
		return fmt.Errorf("百度网盘：服务返回 HTTP %d", status)
	}
	if jerr != nil {
		return errors.New("百度网盘：无法解析服务响应")
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errors.New("百度网盘：无法解析服务响应")
		}
	}
	return nil
}

// api calls an xpan endpoint (GET when form == nil, else POST form). query must not contain access_token.
func (p *provider) api(ctx context.Context, path string, query, form url.Values, out any) error {
	return p.withToken(ctx, func(tok string) error {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("access_token", tok)
		var req *http.Request
		var err error
		if form == nil {
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path+"?"+q.Encode(), nil)
		} else {
			req, err = http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path+"?"+q.Encode(), strings.NewReader(form.Encode()))
			if req != nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		}
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("User-Agent", userAgent)
		resp, err := panhttp.Do(ctx, p.hc, req)
		if err != nil {
			return err
		}
		raw, err := panhttp.ReadBody(resp, panhttp.MaxJSON)
		if err != nil {
			return err
		}
		return parse(resp.StatusCode, raw, out)
	})
}

// ---- model ----

type item struct {
	FsID     int64  `json:"fs_id"`
	Path     string `json:"path"`
	Name     string `json:"server_filename"`
	Size     int64  `json:"size"`
	IsDir    int    `json:"isdir"`
	ServerMT int64  `json:"server_mtime"`
}

func abs(full string) string { return "/" + full }

func (p *provider) full(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return cloud.Join(p.base, rel), nil
}

// listAbs lists an absolute Baidu directory (all pages).
func (p *provider) listAbs(ctx context.Context, dir string) ([]item, error) {
	var all []item
	start := 0
	for page := 0; page < maxPages; page++ {
		var out struct {
			List []item `json:"list"`
		}
		err := p.api(ctx, "/xpan/file", url.Values{
			"method": {"list"}, "dir": {dir}, "start": {strconv.Itoa(start)}, "limit": {strconv.Itoa(listLimit)},
		}, nil, &out)
		if err != nil {
			return nil, err
		}
		if len(out.List) == 0 {
			return all, nil
		}
		all = append(all, out.List...)
		start += len(out.List)
	}
	return nil, errors.New("百度网盘：目录项目过多")
}

func (i item) name() string {
	if i.Name != "" {
		return i.Name
	}
	return i.Path[strings.LastIndex(i.Path, "/")+1:]
}

func entry(rel string, i item) cloud.Entry {
	path := i.name()
	if rel != "" {
		path = rel + "/" + i.name()
	}
	e := cloud.Entry{Name: i.name(), Path: path, IsDir: i.IsDir == 1, ModTime: time.Unix(i.ServerMT, 0)}
	if !e.IsDir {
		e.Size = i.Size
	}
	return e
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	full, err := p.full(dir)
	if err != nil {
		return nil, err
	}
	items, err := p.listAbs(ctx, abs(full))
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Entry, 0, len(items))
	for _, i := range items {
		out = append(out, entry(dir, i))
	}
	return out, nil
}

func split(full string) (dir, name string) {
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "", full
	}
	return full[:i], full[i+1:]
}

// lookup finds one item by listing its parent (Baidu has no stat-by-path call that is documented for xpan).
func (p *provider) lookup(ctx context.Context, rel string) (item, error) {
	full, err := p.full(rel)
	if err != nil {
		return item{}, err
	}
	if full == "" {
		return item{}, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	d, name := split(full)
	items, err := p.listAbs(ctx, abs(d))
	if err != nil {
		return item{}, err
	}
	for _, i := range items {
		if i.name() == name {
			return i, nil
		}
	}
	return item{}, fmt.Errorf("%w", cloud.ErrNotFound)
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	if rel == "" {
		full, _ := p.full("")
		var out struct{}
		err := p.api(ctx, "/xpan/file", url.Values{"method": {"list"}, "dir": {abs(full)}, "start": {"0"}, "limit": {"1"}}, nil, &out)
		if err != nil {
			return cloud.Entry{}, err
		}
		return cloud.Entry{IsDir: true}, nil
	}
	i, err := p.lookup(ctx, rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	d, _ := split(rel)
	return entry(d, i), nil
}

// ---- download ----

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	i, err := p.lookup(ctx, rel)
	if err != nil {
		return nil, 0, err
	}
	if i.IsDir == 1 {
		return nil, 0, fmt.Errorf("%w: is a folder", cloud.ErrNotFound)
	}
	var meta struct {
		List []struct {
			Dlink string `json:"dlink"`
		} `json:"list"`
	}
	err = p.api(ctx, "/xpan/multimedia", url.Values{
		"method": {"filemetas"}, "fsids": {"[" + strconv.FormatInt(i.FsID, 10) + "]"}, "dlink": {"1"},
	}, nil, &meta)
	if err != nil {
		return nil, 0, err
	}
	if len(meta.List) == 0 || meta.List[0].Dlink == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	u, err := url.Parse(meta.List[0].Dlink)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && panhttp.LoopbackHost(u.Hostname()))) {
		return nil, 0, errors.New("百度网盘：服务返回了无效的下载地址")
	}
	var resp *http.Response
	err = p.withToken(ctx, func(tok string) error {
		q := u.Query()
		q.Set("access_token", tok)
		uu := *u
		uu.RawQuery = q.Encode()
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, uu.String(), nil)
		if e != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("User-Agent", userAgent)
		r, e := panhttp.Do(ctx, p.dl, req)
		if e != nil {
			return e
		}
		switch r.StatusCode {
		case 200:
			resp = r
			return nil
		case 429:
			r.Body.Close()
			return errRate
		case 401:
			r.Body.Close()
			return errTokenExpired
		case 403:
			r.Body.Close()
			return fmt.Errorf("百度网盘：下载被拒绝: %w", cloud.ErrPermission)
		case 404:
			r.Body.Close()
			return fmt.Errorf("%w", cloud.ErrNotFound)
		}
		r.Body.Close()
		return fmt.Errorf("百度网盘：下载失败（HTTP %d）", r.StatusCode)
	})
	if err != nil {
		return nil, 0, err
	}
	size := resp.ContentLength
	if size < 0 {
		size = i.Size
	}
	return resp.Body, size, nil
}

// ---- upload ----

func (p *provider) mkdirAll(ctx context.Context, full string) error {
	if full == "" {
		return nil
	}
	// The directory itself is probed first so ancestors such as "/" and "/apps" (not listable with app-folder
	// permission) are never touched when the directory already exists.
	var out struct{}
	err := p.api(ctx, "/xpan/file", url.Values{"method": {"list"}, "dir": {abs(full)}, "start": {"0"}, "limit": {"1"}}, nil, &out)
	if err == nil {
		return nil
	}
	if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	d, _ := split(full)
	if err := p.mkdirAll(ctx, d); err != nil {
		return err
	}
	err = p.api(ctx, "/xpan/file", url.Values{"method": {"create"}}, url.Values{
		"path": {abs(full)}, "size": {"0"}, "isdir": {"1"}, "rtype": {"0"},
	}, nil)
	if errors.Is(err, cloud.ErrExists) {
		// exists already (concurrent creation, or the probe misfired): only fine when it is a folder
		pd, name := split(full)
		items, lerr := p.listAbs(ctx, abs(pd))
		if lerr != nil {
			return lerr
		}
		for _, i := range items {
			if i.name() == name && i.IsDir == 1 {
				return nil
			}
		}
		return fmt.Errorf("百度网盘：存在同名文件，无法创建文件夹: %w", cloud.ErrExists)
	}
	return err
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	if rel == "" {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if size <= 0 {
		return fmt.Errorf("百度网盘不支持上传空文件: %w", cloud.ErrUnsupported)
	}
	if (size+sliceSize-1)/sliceSize > maxSlices {
		return fmt.Errorf("%w", cloud.ErrTooLarge)
	}
	dir, _ := split(full)
	if err := p.mkdirAll(ctx, dir); err != nil {
		return err
	}
	// Existence check right before upload; rtype=0 below makes the server refuse a conflicting path.
	if _, err := p.lookup(ctx, rel); err == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	sp, err := panhttp.Spool(r, size, sliceSize)
	if err != nil {
		return fmt.Errorf("百度网盘：读取上传内容失败: %w", err)
	}
	defer sp.Close()
	blockList, _ := json.Marshal(sp.BlockMD5s)
	path := abs(full)
	var pre struct {
		ReturnType int    `json:"return_type"`
		UploadID   string `json:"uploadid"`
	}
	err = p.api(ctx, "/xpan/file", url.Values{"method": {"precreate"}}, url.Values{
		"path": {path}, "size": {strconv.FormatInt(size, 10)}, "isdir": {"0"}, "autoinit": {"1"},
		"rtype": {"0"}, "block_list": {string(blockList)},
	}, &pre)
	if err != nil {
		return err
	}
	if pre.ReturnType != 1 {
		// 2/3: the cloud reports the file (or a same-named one) as already existing: never touch it.
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	if pre.UploadID == "" {
		return errors.New("百度网盘：预上传失败")
	}
	for seq := 0; seq < len(sp.BlockMD5s); seq++ {
		off := int64(seq) * sliceSize
		n := size - off
		if n > sliceSize {
			n = sliceSize
		}
		if err := p.uploadSlice(ctx, path, pre.UploadID, seq, filenameOf(full), sp, off, n); err != nil {
			return err
		}
	}
	var created struct {
		Path string `json:"path"`
	}
	err = p.api(ctx, "/xpan/file", url.Values{"method": {"create"}}, url.Values{
		"path": {path}, "size": {strconv.FormatInt(size, 10)}, "isdir": {"0"}, "rtype": {"0"},
		"uploadid": {pre.UploadID}, "block_list": {string(blockList)},
	}, &created)
	if err != nil {
		return err
	}
	if created.Path != "" && created.Path != path {
		return errors.New("百度网盘：服务端改动了目标文件名，已中止")
	}
	return nil
}

func filenameOf(full string) string { _, n := split(full); return n }

func (p *provider) uploadSlice(ctx context.Context, path, uploadID string, seq int, name string, sp *panhttp.Spooled, off, n int64) error {
	return p.withToken(ctx, func(tok string) error {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, sp.Section(off, n)); err != nil {
			return err
		}
		mw.Close()
		q := url.Values{
			"method": {"upload"}, "access_token": {tok}, "type": {"tmpfile"}, "path": {path},
			"uploadid": {uploadID}, "partseq": {strconv.Itoa(seq)},
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, pcsBase+"/rest/2.0/pcs/superfile2?"+q.Encode(), &buf)
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("User-Agent", userAgent)
		resp, err := panhttp.Do(ctx, p.dl, req)
		if err != nil {
			return err
		}
		raw, err := panhttp.ReadBody(resp, panhttp.MaxJSON)
		if err != nil {
			return err
		}
		return parse(resp.StatusCode, raw, nil)
	})
}

// Remove moves one file to the Baidu recycle bin (filemanager delete); folders are refused.
func (p *provider) Remove(ctx context.Context, rel string) error {
	i, err := p.lookup(ctx, rel)
	if err != nil {
		return err
	}
	if i.IsDir == 1 {
		return fmt.Errorf("%w: refusing to delete a folder", cloud.ErrUnsupported)
	}
	full, _ := p.full(rel)
	fl, _ := json.Marshal([]string{abs(full)})
	return p.api(ctx, "/xpan/file", url.Values{"method": {"filemanager"}, "opera": {"delete"}}, url.Values{
		"async": {"0"}, "filelist": {string(fl)}, "ondup": {"fail"},
	}, nil)
}
