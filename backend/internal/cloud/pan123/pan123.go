// Adapted from OpenList (AGPL-3.0) drivers/123_open
//
// Package pan123 is the 123云盘 Open (123_open) storage provider on the official 123pan open platform
// (open-api.123pan.com) using the developer's own client_id/client_secret (client-credentials style access
// token). OpenList's online token relay and direct-link signing are intentionally not supported.
//
// API facts (from the open platform docs / OpenList; none checked against a live account):
//   - POST /api/v1/access_token {clientID,clientSecret} (header Platform: open_platform) -> {code,data{accessToken,expiredAt RFC3339}}
//   - GET  /api/v2/file/list?parentFileId&limit(<=100)&lastFileId -> data{lastFileId (-1 = end), fileList[]}; type 1 = folder
//   - GET  /api/v1/file/download_info?fileId -> data.downloadUrl (fetched without Authorization)
//   - POST /upload/v1/file/mkdir {parentID,name}
//   - POST /upload/v2/file/create {parentFileId,filename,etag(md5),size,containDir} -> data{fileID,preuploadID,reuse,sliceSize,servers[]}
//   - POST <server>/upload/v2/file/slice (multipart preuploadID,sliceNo,sliceMD5,slice), then
//     POST /upload/v2/file/upload_complete {preuploadID}, polled until data.completed
//   - POST /api/v1/file/trash {fileIDs} (Remove moves the file to the recycle bin)
//   - envelope {code,message,data}; code 0 = ok, 401 = token invalid, 429 = rate limited
//
// Never overwrites: the create call omits "duplicate" (so it must not silently rename or replace), and an
// existence check runs right before it. UNVERIFIED: whether the service refuses a same-named create by itself;
// if it does not, a concurrent writer between the check and create could still produce a second same-named
// file (it never replaces one). Server answers whose message says the name exists map to cloud.ErrExists.
// 123pan rejects empty files, so PutNew refuses size 0 with ErrUnsupported.
package pan123

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/panhttp"
)

// Variables so tests can point the provider at an httptest server / speed up polling.
var (
	apiBase      = "https://open-api.123pan.com"
	pollInterval = time.Second
)

const (
	apiTimeout  = 30 * time.Second
	listLimit   = 100
	maxPages    = 1000
	pollTries   = 60
	tokenMargin = 5 * time.Minute
)

var (
	errTokenExpired = errors.New("pan123: access token expired")
	errRate         = errors.New("pan123: rate limited")
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "123_open",
		Name:        "123云盘 Open",
		Group:       "cloud",
		Description: "123云盘开放平台（官方 API）：使用自己申请的 client_id/client_secret 访问网盘。",
		DocsURL:     "https://www.123pan.com/developer",
		Fields: []cloud.Field{
			{Key: "client_id", Label: "Client ID", Type: cloud.TText, Required: true,
				Help: "在 123云盘开放平台（123pan.com/developer）申请后获得。"},
			{Key: "client_secret", Label: "Client Secret", Type: cloud.TPassword, Required: true, Secret: true},
		},
		Factory: New,
	})
}

type provider struct {
	hc       *http.Client
	dl       *http.Client
	base     string
	clientID string
	secret   string

	mu     sync.Mutex
	access string
	exp    time.Time
}

// New connects and obtains an access token (fails fast on wrong credentials).
func New(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	p := &provider{
		hc: panhttp.NewClient(apiTimeout), dl: panhttp.NewClient(0), base: s.BasePath,
		clientID: s.Get("client_id"), secret: strings.TrimSpace(s.Secrets["client_secret"]),
	}
	if p.clientID == "" || p.secret == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写 Client ID 与 Client Secret"}
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
	body, _ := json.Marshal(map[string]string{"clientID": p.clientID, "clientSecret": p.secret})
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/api/v1/access_token", bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Platform", "open_platform")
		resp, err := panhttp.Do(ctx, p.hc, req)
		if err != nil {
			return "", err
		}
		raw, err := panhttp.ReadBody(resp, panhttp.MaxJSON)
		if err != nil {
			return "", err
		}
		var out struct {
			Code int `json:"code"`
			Data struct {
				AccessToken string `json:"accessToken"`
				ExpiredAt   string `json:"expiredAt"`
			} `json:"data"`
		}
		perr := json.Unmarshal(raw, &out)
		if resp.StatusCode == 429 || out.Code == 429 {
			if attempt < panhttp.MaxAttempts {
				if err := panhttp.Sleep(ctx, panhttp.Backoff(attempt)); err != nil {
					return "", err
				}
				continue
			}
			return "", errors.New("123云盘：请求过于频繁，请稍后重试")
		}
		if resp.StatusCode >= 300 || perr != nil || out.Code != 0 || out.Data.AccessToken == "" {
			return "", fmt.Errorf("123云盘：获取访问令牌失败，请检查 Client ID/Secret: %w", cloud.ErrAuth)
		}
		p.access = out.Data.AccessToken
		p.exp = time.Now().Add(2 * time.Hour)
		if t, err := time.Parse(time.RFC3339, out.Data.ExpiredAt); err == nil {
			p.exp = t
		}
		return p.access, nil
	}
}

type envelope struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func mapCode(status, code int, msg string) error {
	switch {
	case code == 401 || status == 401:
		return errTokenExpired
	case code == 429 || status == 429:
		return errRate
	case strings.Contains(msg, "不存在"):
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case strings.Contains(msg, "已存在") || strings.Contains(msg, "重复") || strings.Contains(msg, "同名"):
		return fmt.Errorf("%w", cloud.ErrExists)
	case status == 403 || strings.Contains(msg, "权限"):
		return fmt.Errorf("123云盘：没有权限执行该操作: %w", cloud.ErrPermission)
	}
	return fmt.Errorf("123云盘：服务返回错误（%d）", code)
}

func parse(status int, raw []byte, out any) error {
	var e envelope
	if err := json.Unmarshal(raw, &e); err != nil {
		if status >= 300 {
			return mapCode(status, 0, "")
		}
		return errors.New("123云盘：无法解析服务响应")
	}
	if e.Code != 0 || status >= 300 {
		return mapCode(status, e.Code, e.Message)
	}
	if out != nil && len(e.Data) > 0 {
		if err := json.Unmarshal(e.Data, out); err != nil {
			return errors.New("123云盘：无法解析服务响应")
		}
	}
	return nil
}

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
				return fmt.Errorf("123云盘：访问令牌无效: %w", cloud.ErrAuth)
			}
			bad, refreshed = tok, true
		case errors.Is(err, errRate):
			if attempt >= panhttp.MaxAttempts {
				return errors.New("123云盘：请求过于频繁，请稍后重试")
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

func (p *provider) api(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	return p.withToken(ctx, func(tok string) error {
		u := apiBase + path
		if len(query) > 0 {
			u += "?" + query.Encode()
		}
		req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Platform", "open_platform")
		req.Header.Set("Content-Type", "application/json")
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

type file struct {
	FileID   int64  `json:"fileId"`
	Name     string `json:"filename"`
	Type     int    `json:"type"`
	Size     int64  `json:"size"`
	UpdateAt string `json:"updateAt"`
	Trashed  int    `json:"trashed"`
}

func (f file) isDir() bool { return f.Type == 1 }

func (f file) mod() time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", f.UpdateAt, time.FixedZone("UTC+8", 8*3600))
	if err != nil {
		return time.Time{}
	}
	return t
}

func (p *provider) children(ctx context.Context, parent int64) ([]file, error) {
	var all []file
	last := int64(0)
	for page := 0; page < maxPages; page++ {
		var out struct {
			LastFileID int64  `json:"lastFileId"`
			FileList   []file `json:"fileList"`
		}
		err := p.api(ctx, http.MethodGet, "/api/v2/file/list", url.Values{
			"parentFileId": {strconv.FormatInt(parent, 10)}, "limit": {strconv.Itoa(listLimit)},
			"lastFileId": {strconv.FormatInt(last, 10)},
		}, nil, &out)
		if err != nil {
			return nil, err
		}
		for _, f := range out.FileList {
			if f.Trashed == 0 {
				all = append(all, f)
			}
		}
		if out.LastFileID == -1 {
			return all, nil
		}
		last = out.LastFileID
	}
	return nil, errors.New("123云盘：目录项目过多")
}

func pick(items []file, name string, wantDir bool) (file, bool) {
	var c []file
	for _, f := range items {
		if f.Name == name && (!wantDir || f.isDir()) {
			c = append(c, f)
		}
	}
	if len(c) == 0 {
		return file{}, false
	}
	sort.Slice(c, func(i, j int) bool { return c[i].FileID < c[j].FileID })
	return c[0], true
}

func segs(full string) []string {
	if full == "" {
		return nil
	}
	return strings.Split(full, "/")
}

func split(full string) (dir, name string) {
	i := strings.LastIndex(full, "/")
	if i < 0 {
		return "", full
	}
	return full[:i], full[i+1:]
}

func (p *provider) full(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return cloud.Join(p.base, rel), nil
}

func (p *provider) resolveDir(ctx context.Context, full string) (int64, error) {
	id := int64(0)
	for _, s := range segs(full) {
		items, err := p.children(ctx, id)
		if err != nil {
			return 0, err
		}
		f, ok := pick(items, s, true)
		if !ok {
			return 0, fmt.Errorf("%w", cloud.ErrNotFound)
		}
		id = f.FileID
	}
	return id, nil
}

func entry(rel string, f file) cloud.Entry {
	path := f.Name
	if rel != "" {
		path = rel + "/" + f.Name
	}
	e := cloud.Entry{Name: f.Name, Path: path, IsDir: f.isDir(), ModTime: f.mod()}
	if !e.IsDir {
		e.Size = f.Size
	}
	return e
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	full, err := p.full(dir)
	if err != nil {
		return nil, err
	}
	id, err := p.resolveDir(ctx, full)
	if err != nil {
		return nil, err
	}
	items, err := p.children(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]cloud.Entry, 0, len(items))
	for _, f := range items {
		out = append(out, entry(dir, f))
	}
	return out, nil
}

func (p *provider) lookup(ctx context.Context, rel string) (file, error) {
	full, err := p.full(rel)
	if err != nil {
		return file{}, err
	}
	if full == "" {
		return file{}, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	d, name := split(full)
	pid, err := p.resolveDir(ctx, d)
	if err != nil {
		return file{}, err
	}
	items, err := p.children(ctx, pid)
	if err != nil {
		return file{}, err
	}
	f, ok := pick(items, name, false)
	if !ok {
		return file{}, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	return f, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	if rel == "" {
		full, _ := p.full("")
		if _, err := p.resolveDir(ctx, full); err != nil {
			return cloud.Entry{}, err
		}
		return cloud.Entry{IsDir: true}, nil
	}
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	d, _ := split(rel)
	return entry(d, f), nil
}

func okURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && panhttp.LoopbackHost(u.Hostname()))) {
		return nil, errors.New("123云盘：服务返回了无效的地址")
	}
	return u, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return nil, 0, err
	}
	if f.isDir() {
		return nil, 0, fmt.Errorf("%w: is a folder", cloud.ErrNotFound)
	}
	var out struct {
		DownloadURL string `json:"downloadUrl"`
	}
	if err := p.api(ctx, http.MethodGet, "/api/v1/file/download_info", url.Values{"fileId": {strconv.FormatInt(f.FileID, 10)}}, nil, &out); err != nil {
		return nil, 0, err
	}
	u, err := okURL(out.DownloadURL)
	if err != nil {
		return nil, 0, err
	}
	// The download URL is pre-authorized: no Authorization header is sent to it.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
	}
	resp, err := panhttp.Do(ctx, p.dl, req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 403:
			return nil, 0, fmt.Errorf("123云盘：下载地址被拒绝: %w", cloud.ErrPermission)
		case 404:
			return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
		}
		return nil, 0, fmt.Errorf("123云盘：下载失败（HTTP %d）", resp.StatusCode)
	}
	size := resp.ContentLength
	if size < 0 {
		size = f.Size
	}
	return resp.Body, size, nil
}

func (p *provider) mkdir(ctx context.Context, parent int64, name string) (int64, error) {
	items, err := p.children(ctx, parent)
	if err != nil {
		return 0, err
	}
	if f, ok := pick(items, name, true); ok {
		return f.FileID, nil
	}
	if _, ok := pick(items, name, false); ok {
		return 0, fmt.Errorf("123云盘：存在同名文件，无法创建文件夹: %w", cloud.ErrExists)
	}
	var out struct {
		DirID int64 `json:"dirID"`
	}
	err = p.api(ctx, http.MethodPost, "/upload/v1/file/mkdir", nil, map[string]any{"parentID": parent, "name": name}, &out)
	if err == nil && out.DirID != 0 {
		return out.DirID, nil
	}
	// The create may have raced or the response lacked the id: look again before failing.
	items, lerr := p.children(ctx, parent)
	if lerr == nil {
		if f, ok := pick(items, name, true); ok {
			return f.FileID, nil
		}
	}
	if err == nil {
		err = errors.New("123云盘：创建文件夹失败")
	}
	return 0, err
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
		return fmt.Errorf("123云盘不支持上传空文件: %w", cloud.ErrUnsupported)
	}
	dir, name := split(full)
	pid := int64(0)
	for _, s := range segs(dir) {
		if pid, err = p.mkdir(ctx, pid, s); err != nil {
			return err
		}
	}
	items, err := p.children(ctx, pid)
	if err != nil {
		return err
	}
	if _, ok := pick(items, name, false); ok {
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	sp, err := panhttp.Spool(r, size, 0)
	if err != nil {
		return fmt.Errorf("123云盘：读取上传内容失败: %w", err)
	}
	defer sp.Close()
	var cr struct {
		FileID      int64    `json:"fileID"`
		PreuploadID string   `json:"preuploadID"`
		Reuse       bool     `json:"reuse"`
		SliceSize   int64    `json:"sliceSize"`
		Servers     []string `json:"servers"`
	}
	err = p.api(ctx, http.MethodPost, "/upload/v2/file/create", nil, map[string]any{
		"parentFileId": pid, "filename": name, "etag": sp.MD5, "size": size, "containDir": false,
	}, &cr)
	if err != nil {
		return err
	}
	if cr.Reuse && cr.FileID != 0 {
		return nil
	}
	if cr.PreuploadID == "" || cr.SliceSize <= 0 || len(cr.Servers) == 0 {
		return errors.New("123云盘：创建上传任务失败")
	}
	srv, err := okURL(cr.Servers[0])
	if err != nil {
		return err
	}
	n := int((size + cr.SliceSize - 1) / cr.SliceSize)
	for i := 0; i < n; i++ {
		off := int64(i) * cr.SliceSize
		l := size - off
		if l > cr.SliceSize {
			l = cr.SliceSize
		}
		if err := p.uploadSlice(ctx, strings.TrimRight(srv.String(), "/"), cr.PreuploadID, i+1, name, sp, off, l); err != nil {
			return err
		}
	}
	for i := 0; i < pollTries; i++ {
		var cp struct {
			Completed bool  `json:"completed"`
			FileID    int64 `json:"fileID"`
		}
		err := p.api(ctx, http.MethodPost, "/upload/v2/file/upload_complete", nil, map[string]any{"preuploadID": cr.PreuploadID}, &cp)
		if err == nil && cp.Completed && cp.FileID != 0 {
			return nil
		}
		if err != nil && (errors.Is(err, cloud.ErrAuth) || errors.Is(err, cloud.ErrUnreachable) || ctx.Err() != nil) {
			return err
		}
		if e := panhttp.Sleep(ctx, pollInterval); e != nil {
			return e
		}
	}
	return errors.New("123云盘：等待上传完成超时")
}

func (p *provider) uploadSlice(ctx context.Context, server, preID string, no int, name string, sp *panhttp.Spooled, off, n int64) error {
	md5s, err := sp.MD5Range(off, n)
	if err != nil {
		return err
	}
	return p.withToken(ctx, func(tok string) error {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("preuploadID", preID)
		mw.WriteField("sliceNo", strconv.Itoa(no))
		mw.WriteField("sliceMD5", md5s)
		fw, err := mw.CreateFormFile("slice", fmt.Sprintf("%s.part%d", name, no))
		if err != nil {
			return err
		}
		if _, err := io.Copy(fw, sp.Section(off, n)); err != nil {
			return err
		}
		mw.Close()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+"/upload/v2/file/slice", &buf)
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Platform", "open_platform")
		req.Header.Set("Content-Type", mw.FormDataContentType())
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

// Remove moves one file to the 123pan recycle bin (file/trash); folders are refused.
func (p *provider) Remove(ctx context.Context, rel string) error {
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return err
	}
	if f.isDir() {
		return fmt.Errorf("%w: refusing to delete a folder", cloud.ErrUnsupported)
	}
	return p.api(ctx, http.MethodPost, "/api/v1/file/trash", nil, map[string]any{"fileIDs": []int64{f.FileID}}, nil)
}
