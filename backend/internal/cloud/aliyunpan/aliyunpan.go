// Adapted from OpenList (AGPL-3.0) drivers/aliyundrive_open
//
// Package aliyunpan is the 阿里云盘 Open (aliyundrive_open) storage provider. It uses only the official open
// platform (openapi.alipan.com) with the user's own client_id/client_secret and OAuth refresh token; the
// third-party "online API" token relay of OpenList is intentionally not supported.
//
// API facts (from the official open platform docs / OpenList; none checked against a live account):
//   - POST /oauth/access_token {client_id,client_secret,grant_type:refresh_token,refresh_token} ->
//     {access_token, refresh_token (ROTATES), expires_in}
//   - POST /adrive/v1.0/user/getDriveInfo -> {default_drive_id, resource_drive_id, backup_drive_id}
//   - POST /adrive/v1.0/openFile/list {drive_id,parent_file_id,limit<=100,marker} -> {items,next_marker}
//   - POST /adrive/v1.0/openFile/getDownloadUrl {drive_id,file_id,expire_sec} -> {url}
//   - POST /adrive/v1.0/openFile/create {..., check_name_mode:"refuse", part_info_list} -> {file_id,upload_id,part_info_list[].upload_url}
//   - PUT each part to its upload_url (no Authorization), POST /adrive/v1.0/openFile/complete
//   - POST /adrive/v1.0/openFile/recyclebin/trash (Remove moves the file to the recycle bin; it is not purged)
//
// Never overwrites: create uses check_name_mode=refuse (atomic on the server) after an existence check; a
// conflict code (AlreadyExist*) maps to cloud.ErrExists.
package aliyunpan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/panhttp"
)

// apiBase is a variable only so tests can point it at an httptest server.
var apiBase = "https://openapi.alipan.com"

const (
	apiTimeout  = 30 * time.Second
	listLimit   = 100
	maxPages    = 1000
	minPart     = 20 << 20
	maxParts    = 10000
	tokenMargin = 2 * time.Minute
)

var (
	errTokenExpired = errors.New("aliyunpan: access token expired")
	errRate         = errors.New("aliyunpan: rate limited")
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "aliyundrive_open",
		Name:        "阿里云盘 Open",
		Group:       "cloud",
		Description: "阿里云盘开放平台（官方 API）：使用自己申请的应用 client_id/client_secret 与 refresh_token 访问备份盘或资源库。",
		DocsURL:     "https://www.alipan.com/developer",
		Fields: []cloud.Field{
			{Key: "client_id", Label: "Client ID（AppID）", Type: cloud.TText, Required: true,
				Help: "在阿里云盘开放平台（alipan.com/developer）创建应用后获得。"},
			{Key: "client_secret", Label: "Client Secret（AppSecret）", Type: cloud.TPassword, Required: true, Secret: true},
			{Key: "refresh_token", Label: "Refresh Token", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "通过官方 OAuth 授权获得。每次刷新后令牌会轮换，系统会自动保存新的令牌。"},
			{Key: "drive_type", Label: "空间类型", Type: cloud.TSelect, Default: "resource",
				Options: []cloud.Option{{Value: "resource", Label: "资源库"}, {Value: "backup", Label: "备份盘"}}},
		},
		Factory: New,
	})
}

type provider struct {
	hc       *http.Client
	dl       *http.Client // downloads (no response-header timeout)
	base     string
	clientID string
	secret   string
	driveID  string
	persist  func(key, value string)

	mu      sync.Mutex
	refresh string
	access  string
	exp     time.Time
}

// New connects: it exchanges the refresh token and resolves the drive id (fails fast on bad credentials).
func New(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	p := &provider{
		hc:       panhttp.NewClient(apiTimeout),
		dl:       panhttp.NewClient(0),
		base:     s.BasePath,
		clientID: s.Get("client_id"),
		secret:   strings.TrimSpace(s.Secrets["client_secret"]),
		refresh:  strings.TrimSpace(s.Secrets["refresh_token"]),
		persist:  s.Persist,
	}
	if p.clientID == "" || p.secret == "" || p.refresh == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写 Client ID、Client Secret 与 Refresh Token"}
	}
	dt := s.Get("drive_type")
	if dt == "" {
		dt = "resource"
	}
	if dt != "resource" && dt != "backup" {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "空间类型无效"}
	}
	var info struct {
		Default  string `json:"default_drive_id"`
		Resource string `json:"resource_drive_id"`
		Backup   string `json:"backup_drive_id"`
	}
	if err := p.call(ctx, "/adrive/v1.0/user/getDriveInfo", map[string]any{}, &info); err != nil {
		p.hc.CloseIdleConnections()
		return nil, err
	}
	p.driveID = map[string]string{"resource": info.Resource, "backup": info.Backup}[dt]
	if p.driveID == "" {
		p.hc.CloseIdleConnections()
		return nil, fmt.Errorf("阿里云盘：该账号没有可用的%s空间: %w", map[string]string{"resource": "资源库", "backup": "备份盘"}[dt], cloud.ErrPermission)
	}
	return p, nil
}

func (p *provider) Close() error {
	p.hc.CloseIdleConnections()
	p.dl.CloseIdleConnections()
	return nil
}

// ---- tokens ----

// token returns a valid access token. bad is the token that just failed (forces a refresh unless another
// goroutine already replaced it).
func (p *provider) token(ctx context.Context, bad string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.access != "" && p.access != bad && time.Now().Before(p.exp.Add(-tokenMargin)) {
		return p.access, nil
	}
	if bad != "" && p.access != "" && p.access != bad {
		return p.access, nil
	}
	body, _ := json.Marshal(map[string]string{
		"client_id": p.clientID, "client_secret": p.secret, "grant_type": "refresh_token", "refresh_token": p.refresh,
	})
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	for attempt := 1; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/oauth/access_token", bytes.NewReader(body))
		if err != nil {
			return "", fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Content-Type", "application/json")
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
			return "", fmt.Errorf("阿里云盘：刷新令牌失败，请检查 Client ID/Secret 与 Refresh Token 是否有效: %w", cloud.ErrAuth)
		}
		break
	}
	p.access = out.AccessToken
	if out.ExpiresIn <= 0 {
		out.ExpiresIn = 7200
	}
	p.exp = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	if out.RefreshToken != "" && out.RefreshToken != p.refresh {
		// The old refresh token is invalidated: keep the new one in memory first, then try to persist it.
		p.refresh = out.RefreshToken
		if p.persist != nil {
			p.persist("refresh_token", out.RefreshToken)
		}
	}
	return p.access, nil
}

// ---- API calls ----

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func safeCode(c string) string {
	if len(c) > 64 {
		c = c[:64]
	}
	for _, r := range c {
		if !(r == '.' || r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "unknown"
		}
	}
	return c
}

func mapErr(status int, code string) error {
	lc := strings.ToLower(code)
	switch {
	case code == "AccessTokenInvalid" || code == "AccessTokenExpired" || code == "I400JD" || status == 401:
		return errTokenExpired
	case status == 429 || strings.HasPrefix(lc, "toomanyrequests") || strings.Contains(lc, "throttl"):
		return errRate
	case strings.HasPrefix(lc, "notfound") || status == 404:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case strings.Contains(lc, "alreadyexist") || strings.Contains(lc, "conflict") || status == 409:
		return fmt.Errorf("%w", cloud.ErrExists)
	case strings.HasPrefix(lc, "forbidden") || strings.Contains(lc, "permission") || status == 403:
		return fmt.Errorf("阿里云盘：没有权限执行该操作: %w", cloud.ErrPermission)
	}
	return fmt.Errorf("阿里云盘：服务返回错误（%d %s）", status, safeCode(code))
}

// call POSTs a JSON API request with token handling, one refresh-and-retry on auth expiry and up to
// MaxAttempts attempts when rate limited.
func (p *provider) call(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	bad, refreshed := "", false
	for attempt := 1; ; {
		tok, err := p.token(ctx, bad)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+path, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		resp, err := panhttp.Do(ctx, p.hc, req)
		if err != nil {
			return err
		}
		raw, err := panhttp.ReadBody(resp, panhttp.MaxJSON)
		if err != nil {
			return err
		}
		var ae apiError
		if resp.StatusCode >= 300 || (json.Unmarshal(raw, &ae) == nil && ae.Code != "") {
			_ = json.Unmarshal(raw, &ae)
			merr := mapErr(resp.StatusCode, ae.Code)
			switch {
			case errors.Is(merr, errTokenExpired):
				if refreshed {
					return fmt.Errorf("阿里云盘：访问令牌无效: %w", cloud.ErrAuth)
				}
				bad, refreshed = tok, true
				continue
			case errors.Is(merr, errRate):
				if attempt >= panhttp.MaxAttempts {
					return errors.New("阿里云盘：请求过于频繁，请稍后重试")
				}
				if err := panhttp.Sleep(ctx, panhttp.Backoff(attempt)); err != nil {
					return err
				}
				attempt++
				continue
			}
			return merr
		}
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return errors.New("阿里云盘：无法解析服务响应")
			}
		}
		return nil
	}
}

// ---- file model & path resolution ----

type file struct {
	FileID    string    `json:"file_id"`
	Name      string    `json:"name"`
	FileName  string    `json:"file_name"`
	Type      string    `json:"type"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (f file) name() string {
	if f.Name != "" {
		return f.Name
	}
	return f.FileName
}
func (f file) isDir() bool { return f.Type == "folder" }

func (p *provider) children(ctx context.Context, parentID string) ([]file, error) {
	var all []file
	marker := ""
	for page := 0; page < maxPages; page++ {
		var out struct {
			Items      []file `json:"items"`
			NextMarker string `json:"next_marker"`
		}
		err := p.call(ctx, "/adrive/v1.0/openFile/list", map[string]any{
			"drive_id": p.driveID, "parent_file_id": parentID, "limit": listLimit, "marker": marker,
		}, &out)
		if err != nil {
			return nil, err
		}
		all = append(all, out.Items...)
		if out.NextMarker == "" {
			return all, nil
		}
		marker = out.NextMarker
	}
	return nil, errors.New("阿里云盘：目录项目过多")
}

// pick chooses deterministically among same-named siblings: oldest created, then smallest id.
func pick(items []file, name string, wantDir bool) (file, bool) {
	var c []file
	for _, f := range items {
		if f.name() == name && (!wantDir || f.isDir()) {
			c = append(c, f)
		}
	}
	if len(c) == 0 {
		return file{}, false
	}
	sort.SliceStable(c, func(i, j int) bool {
		if !c[i].CreatedAt.Equal(c[j].CreatedAt) {
			return c[i].CreatedAt.Before(c[j].CreatedAt)
		}
		return c[i].FileID < c[j].FileID
	})
	return c[0], true
}

func segs(full string) []string {
	if full == "" {
		return nil
	}
	return strings.Split(full, "/")
}

// resolveDir returns the folder id of an absolute (base-joined) path; ErrNotFound when a segment is missing.
func (p *provider) resolveDir(ctx context.Context, full string) (string, error) {
	id := "root"
	for _, s := range segs(full) {
		items, err := p.children(ctx, id)
		if err != nil {
			return "", err
		}
		f, ok := pick(items, s, true)
		if !ok {
			return "", fmt.Errorf("%w", cloud.ErrNotFound)
		}
		id = f.FileID
	}
	return id, nil
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

func entry(rel string, f file) cloud.Entry {
	path := f.name()
	if rel != "" {
		path = rel + "/" + f.name()
	}
	e := cloud.Entry{Name: f.name(), Path: path, IsDir: f.isDir(), ModTime: f.UpdatedAt}
	if !f.isDir() {
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
		return file{}, errors.New("root")
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
		if _, err := p.resolveDir(ctx, p.base); err != nil {
			return cloud.Entry{}, err
		}
		return cloud.Entry{IsDir: true}, nil
	}
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	return entry(parentRel(rel), f), nil
}

func parentRel(rel string) string {
	d, _ := split(rel)
	return d
}

// ---- download ----

func okURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && panhttp.LoopbackHost(u.Hostname()))) {
		return nil, errors.New("阿里云盘：服务返回了无效的下载地址")
	}
	return u, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return nil, 0, err
	}
	if f.isDir() {
		return nil, 0, fmt.Errorf("%w: is a folder", cloud.ErrNotFound)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := p.call(ctx, "/adrive/v1.0/openFile/getDownloadUrl", map[string]any{
		"drive_id": p.driveID, "file_id": f.FileID, "expire_sec": 900,
	}, &out); err != nil {
		return nil, 0, err
	}
	u, err := okURL(out.URL)
	if err != nil {
		return nil, 0, err
	}
	// The pre-signed URL carries its own authorization: no Authorization header is sent.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
	}
	dl := p.dl
	resp, err := panhttp.Do(ctx, dl, req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		switch resp.StatusCode {
		case 403:
			return nil, 0, fmt.Errorf("阿里云盘：下载地址被拒绝: %w", cloud.ErrPermission)
		case 404:
			return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
		}
		return nil, 0, fmt.Errorf("阿里云盘：下载失败（HTTP %d）", resp.StatusCode)
	}
	size := resp.ContentLength
	if size < 0 {
		size = f.Size
	}
	return resp.Body, size, nil
}

// ---- upload ----

func partSizeFor(size int64) int64 {
	ps := int64(minPart)
	if need := (size + maxParts - 1) / maxParts; need > ps {
		ps = need
	}
	return ps
}

type partInfo struct {
	PartNumber int    `json:"part_number"`
	UploadURL  string `json:"upload_url"`
}

func (p *provider) mkdir(ctx context.Context, parentID, name string) (string, error) {
	items, err := p.children(ctx, parentID)
	if err != nil {
		return "", err
	}
	if f, ok := pick(items, name, true); ok {
		return f.FileID, nil
	}
	if _, ok := pick(items, name, false); ok {
		return "", fmt.Errorf("阿里云盘：存在同名文件，无法创建文件夹: %w", cloud.ErrExists)
	}
	var out file
	err = p.call(ctx, "/adrive/v1.0/openFile/create", map[string]any{
		"drive_id": p.driveID, "parent_file_id": parentID, "name": name, "type": "folder", "check_name_mode": "refuse",
	}, &out)
	if errors.Is(err, cloud.ErrExists) { // created concurrently
		items, lerr := p.children(ctx, parentID)
		if lerr != nil {
			return "", lerr
		}
		if f, ok := pick(items, name, true); ok {
			return f.FileID, nil
		}
	}
	if err != nil {
		return "", err
	}
	if out.FileID == "" {
		return "", errors.New("阿里云盘：创建文件夹失败")
	}
	return out.FileID, nil
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	if rel == "" {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	full, err := p.full(rel)
	if err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: unknown size", cloud.ErrUnsupported)
	}
	dir, name := split(full)
	pid := "root"
	for _, s := range segs(dir) {
		if pid, err = p.mkdir(ctx, pid, s); err != nil {
			return err
		}
	}
	// Existence check right before create; check_name_mode=refuse below is the atomic guard.
	items, err := p.children(ctx, pid)
	if err != nil {
		return err
	}
	if _, ok := pick(items, name, false); ok {
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	ps := partSizeFor(size)
	count := int((size + ps - 1) / ps)
	body := map[string]any{
		"drive_id": p.driveID, "parent_file_id": pid, "name": name, "type": "file",
		"check_name_mode": "refuse", "size": size,
	}
	if count > 0 {
		pl := make([]map[string]int, count)
		for i := range pl {
			pl[i] = map[string]int{"part_number": i + 1}
		}
		body["part_info_list"] = pl
	}
	var created struct {
		FileID       string     `json:"file_id"`
		UploadID     string     `json:"upload_id"`
		RapidUpload  bool       `json:"rapid_upload"`
		PartInfoList []partInfo `json:"part_info_list"`
	}
	if err := p.call(ctx, "/adrive/v1.0/openFile/create", body, &created); err != nil {
		return err
	}
	if created.FileID == "" || created.UploadID == "" || (count > 0 && len(created.PartInfoList) < count) {
		return errors.New("阿里云盘：创建上传任务失败")
	}
	urls := map[int]string{}
	for _, pi := range created.PartInfoList {
		urls[pi.PartNumber] = pi.UploadURL
	}
	buf := make([]byte, ps)
	if size < ps {
		buf = make([]byte, size)
	}
	var sent int64
	for n := 1; n <= count; n++ {
		l := size - sent
		if l > ps {
			l = ps
		}
		chunk := buf[:l]
		if _, err := io.ReadFull(r, chunk); err != nil {
			return fmt.Errorf("阿里云盘：读取上传内容失败: %w", io.ErrUnexpectedEOF)
		}
		if err := p.putPart(ctx, created.FileID, created.UploadID, n, count, urls, chunk); err != nil {
			return err
		}
		sent += l
	}
	return p.call(ctx, "/adrive/v1.0/openFile/complete", map[string]any{
		"drive_id": p.driveID, "file_id": created.FileID, "upload_id": created.UploadID,
	}, nil)
}

func (p *provider) putPart(ctx context.Context, fileID, uploadID string, n, count int, urls map[int]string, chunk []byte) error {
	refreshedURLs := false
	for attempt := 1; ; attempt++ {
		u, err := okURL(urls[n])
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, u.String(), bytes.NewReader(chunk))
		if err != nil {
			return fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
		}
		req.ContentLength = int64(len(chunk))
		resp, err := panhttp.Do(ctx, p.hc, req)
		status := 0
		if err == nil {
			status = resp.StatusCode
			io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
		} else if ctx.Err() != nil {
			return err
		}
		// 409 means the part already exists (OpenList treats it as success as well).
		if err == nil && (status == 200 || status == 409) {
			return nil
		}
		if err == nil && status == 403 && !refreshedURLs {
			// pre-signed URL expired: ask for fresh ones once
			refreshedURLs = true
			pl := make([]map[string]int, count)
			for i := range pl {
				pl[i] = map[string]int{"part_number": i + 1}
			}
			var out struct {
				PartInfoList []partInfo `json:"part_info_list"`
			}
			if e := p.call(ctx, "/adrive/v1.0/openFile/getUploadUrl", map[string]any{
				"drive_id": p.driveID, "file_id": fileID, "upload_id": uploadID, "part_info_list": pl,
			}, &out); e != nil {
				return e
			}
			for _, pi := range out.PartInfoList {
				urls[pi.PartNumber] = pi.UploadURL
			}
			continue
		}
		if attempt >= panhttp.MaxAttempts {
			if err != nil {
				return err
			}
			return fmt.Errorf("阿里云盘：上传分片失败（HTTP %d）", status)
		}
		if e := panhttp.Sleep(ctx, panhttp.Backoff(attempt)); e != nil {
			return e
		}
	}
}

// Remove moves one file to the recycle bin (recyclebin/trash); folders are refused.
func (p *provider) Remove(ctx context.Context, rel string) error {
	if rel == "" {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	f, err := p.lookup(ctx, rel)
	if err != nil {
		return err
	}
	if f.isDir() {
		return fmt.Errorf("%w: refusing to delete a folder", cloud.ErrUnsupported)
	}
	return p.call(ctx, "/adrive/v1.0/openFile/recyclebin/trash", map[string]any{
		"drive_id": p.driveID, "file_id": f.FileID,
	}, nil)
}
