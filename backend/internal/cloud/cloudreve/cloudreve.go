// Adapted from OpenList (AGPL-3.0) drivers/cloudreve_v4
//
// Package cloudreve is the Cloudreve v4 storage provider (kind "cloudreve"), using the /api/v4 REST API.
//
// API facts used (OpenList driver; the official v4 API reference at cloudrevev4.apifox.cn could not be fetched in
// full, so details beyond /site/ping are taken from the OpenList driver and are marked unverified in the report):
//   - every reply is {"code","msg","data"}; code 0 is success, 401 token expired, 40016 path does not exist,
//     40004 object exists, 40020 credentials invalid
//   - GET /site/config/login (login_captcha), POST /session/token {email,password} -> {token:{access_token,
//     refresh_token,access_expires,refresh_expires}}, POST /session/token/refresh {refresh_token} -> same token object
//   - GET /file?uri=&page_size=&page=&next_page_token= -> {files[],pagination{next_token,is_cursor},storage_policy}
//   - GET /file/info?uri=, POST /file/url {uris,download:true} -> {urls[{url}]}, POST /file/create {type,uri,
//     error_on_conflict}, DELETE /file {uris,unlink,skip_soft_delete}
//   - PUT /file/upload {uri,size,policy_id,last_modified,mime_type} -> upload session; without entity_type
//     "version" the server rejects an existing target with code 40004, which is the fail-if-exists conflict policy.
//     Chunks go to POST /file/upload/{session}/{index} (local/relay), POST <upload_url>?chunk=N with the credential
//     (remote slave), presigned PUT + CompleteMultipartUpload (s3) or ranged PUT (onedrive), then the callback.
//
// A refresh token rotated by the server is stored back with Settings.Persist (when it is set) under the secret key
// "refresh_token". Captcha logins, WebAuthn and two-factor are not supported. Other storage policies (ks3, oss,
// cos, upyun, qiniu, obs...) return ErrUnsupported on upload; listing and download work for all.
package cloudreve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/cloudhttp"
)

const (
	listPage      = 100
	maxPages      = 500
	maxEntries    = 20000
	maxChunk      = 512 << 20
	defaultRoot   = "cloudreve://my"
	codeExpired   = 401
	codeNotExist  = 40016
	codeExists    = 40004
	codeBadCreds  = 40020
	expiryMargin  = time.Minute
	chunkAttempts = 3
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "cloudreve",
		Name:        "Cloudreve v4",
		Group:       "enterprise",
		Description: "Cloudreve v4 私有云盘：用户名密码登录（自动续期令牌），读取与归档写入“我的文件”下的目录。",
		DocsURL:     "https://docs.cloudreve.org/",
		Fields: []cloud.Field{
			{Key: "url", Label: "服务地址", Type: cloud.TText, Required: true, Placeholder: "https://cloudreve.example.com",
				Help: "Cloudreve 站点地址（不含 /api），支持 http/https 与内网地址。仅支持 v4。"},
			{Key: "username", Label: "邮箱 / 用户名", Type: cloud.TText, Required: true},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true, Required: true,
				Help: "不支持开启登录验证码、Passkey 或两步验证的账号。"},
			{Key: "root_uri", Label: "根 URI", Type: cloud.TText, Default: defaultRoot, Placeholder: defaultRoot,
				Help: "文件系统根，默认 cloudreve://my（我的文件）；基础路径相对于它。"},
			{Key: "refresh_token", Label: "刷新令牌（自动维护）", Type: cloud.TPassword, Secret: true, Managed: true,
				Help: "登录后由系统自动保存，无需填写。"},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}},
				Help: "仅在内网自签名证书时开启。"},
		},
		Factory: New,
	})
}

type provider struct {
	c        *cloudhttp.Client
	basePath string
	root     string
	username string
	password string
	persist  func(key, value string)

	mu             sync.Mutex
	access         string
	refresh        string
	accessExpires  time.Time
	refreshExpires time.Time
}

// New builds the provider (no network traffic).
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	c, err := cloudhttp.New(s.Get("url"), s.Bool("insecure_skip_verify", false))
	if err != nil {
		return nil, err
	}
	root := strings.TrimRight(s.Get("root_uri"), "/")
	if root == "" {
		root = defaultRoot
	}
	if !strings.HasPrefix(root, "cloudreve://") || strings.ContainsAny(root, "?#\\ ") {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "根 URI 无效（应形如 cloudreve://my）"}
	}
	return &provider{c: c, basePath: s.BasePath, root: root, username: s.Get("username"), password: s.Secrets["password"],
		refresh: s.Secrets["refresh_token"], persist: s.Persist}, nil
}

func (p *provider) Close() error { p.c.Close(); return nil }

// uri maps a provider-relative path to a cloudreve:// URI.
func (p *provider) uri(rel string) (string, error) {
	abs, err := cloudhttp.Abs(p.basePath, rel)
	if err != nil {
		return "", err
	}
	if abs == "/" {
		return p.root, nil
	}
	return p.root + cloudhttp.EscapePath(abs), nil
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func classify(env envelope) error {
	switch env.Code {
	case 0:
		return nil
	case codeExpired:
		return fmt.Errorf("%w", cloud.ErrAuth)
	case codeBadCreds:
		return fmt.Errorf("%w", cloud.ErrAuth)
	case codeNotExist:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case codeExists:
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	return fmt.Errorf("服务器返回错误（code %d）", env.Code)
}

type tokenObj struct {
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	AccessExpires  string `json:"access_expires"`
	RefreshExpires string `json:"refresh_expires"`
}

// raw sends an unauthenticated-or-bearer JSON call and decodes the envelope; no re-auth logic.
func (p *provider) raw(ctx context.Context, method, apiPath string, q url.Values, in any, bearer string) (envelope, error) {
	var body []byte
	hdr := map[string]string{"Accept": "application/json"}
	if in != nil {
		body, _ = json.Marshal(in)
		hdr["Content-Type"] = "application/json"
	}
	if bearer != "" {
		hdr["Authorization"] = "Bearer " + bearer
	}
	resp, err := p.c.DoBytes(ctx, method, p.c.URL("/api/v4"+apiPath, q), hdr, body)
	if err != nil {
		return envelope{}, err
	}
	var env envelope
	if resp.StatusCode == 401 {
		cloudhttp.Drain(resp)
		return envelope{Code: codeExpired}, nil
	}
	if resp.StatusCode/100 != 2 && resp.StatusCode != 400 && resp.StatusCode != 404 && resp.StatusCode != 409 {
		cloudhttp.Drain(resp)
		return envelope{}, cloudhttp.StatusErr(resp.StatusCode)
	}
	if err := cloudhttp.ReadJSON(resp, &env); err != nil {
		if resp.StatusCode/100 != 2 {
			return envelope{}, cloudhttp.StatusErr(resp.StatusCode)
		}
		return envelope{}, err
	}
	return env, nil
}

func parseTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (p *provider) store(t tokenObj) {
	p.access = t.AccessToken
	changed := t.RefreshToken != "" && t.RefreshToken != p.refresh
	if t.RefreshToken != "" {
		p.refresh = t.RefreshToken
	}
	p.accessExpires, p.refreshExpires = parseTime(t.AccessExpires), parseTime(t.RefreshExpires)
	if changed && p.persist != nil {
		func() {
			defer func() { _ = recover() }() // a failing persist must never break the session
			p.persist("refresh_token", p.refresh)
		}()
	}
}

func (p *provider) loginLocked(ctx context.Context) error {
	if p.username == "" || p.password == "" {
		return fmt.Errorf("%w", cloud.ErrAuth)
	}
	env, err := p.raw(ctx, "GET", "/site/config/login", nil, nil, "")
	if err == nil && env.Code == 0 {
		var cfg struct {
			LoginCaptcha bool `json:"login_captcha"`
		}
		if json.Unmarshal(env.Data, &cfg) == nil && cfg.LoginCaptcha {
			return fmt.Errorf("%w：服务器启用了登录验证码", cloud.ErrUnsupported)
		}
	}
	env, err = p.raw(ctx, "POST", "/session/token", nil, map[string]string{"email": p.username, "password": p.password}, "")
	if err != nil {
		return err
	}
	if env.Code != 0 {
		if env.Code == codeExpired || env.Code == codeBadCreds || env.Code == 40001 || env.Code == 40002 {
			return fmt.Errorf("%w", cloud.ErrAuth)
		}
		return classify(env)
	}
	var d struct {
		Token tokenObj `json:"token"`
	}
	if json.Unmarshal(env.Data, &d) != nil || d.Token.AccessToken == "" {
		return errors.New("登录响应中没有令牌")
	}
	p.store(d.Token)
	return nil
}

func (p *provider) refreshLocked(ctx context.Context) error {
	if p.refresh == "" || (!p.refreshExpires.IsZero() && p.refreshExpires.Before(time.Now())) {
		return p.loginLocked(ctx)
	}
	env, err := p.raw(ctx, "POST", "/session/token/refresh", nil, map[string]string{"refresh_token": p.refresh}, "")
	if err != nil {
		return err
	}
	var t tokenObj
	if env.Code != 0 || json.Unmarshal(env.Data, &t) != nil || t.AccessToken == "" {
		p.refresh = "" // rejected or unusable: fall back to the password
		return p.loginLocked(ctx)
	}
	p.store(t)
	return nil
}

// bearer returns a valid access token, refreshing or logging in when missing or about to expire. stale, when
// non-empty, is a token the server just rejected.
func (p *provider) bearer(ctx context.Context, stale string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	expired := p.access == "" || p.access == stale || (!p.accessExpires.IsZero() && time.Now().Add(expiryMargin).After(p.accessExpires))
	if expired {
		if err := p.refreshLocked(ctx); err != nil {
			return "", err
		}
	}
	return p.access, nil
}

// call performs an authenticated API call, re-authenticating once on 401, and decodes data into out.
func (p *provider) call(ctx context.Context, method, apiPath string, q url.Values, in any, out any) error {
	ctx, cancel := context.WithTimeout(ctx, cloudhttp.APITimeout)
	defer cancel()
	stale := ""
	for attempt := 0; ; attempt++ {
		tok, err := p.bearer(ctx, stale)
		if err != nil {
			return err
		}
		env, err := p.raw(ctx, method, apiPath, q, in, tok)
		if err != nil {
			return err
		}
		if env.Code == codeExpired && attempt == 0 {
			stale = tok
			continue
		}
		if err := classify(env); err != nil {
			return err
		}
		if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
			if err := json.Unmarshal(env.Data, out); err != nil {
				return errors.New("服务器响应格式无法识别")
			}
		}
		return nil
	}
}

type file struct {
	Type      int       `json:"type"` // 0 file, 1 folder
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

type listResp struct {
	Files      []file `json:"files"`
	Pagination struct {
		NextToken string `json:"next_token"`
		IsCursor  bool   `json:"is_cursor"`
	} `json:"pagination"`
	StoragePolicy struct {
		ID string `json:"id"`
	} `json:"storage_policy"`
}

func (p *provider) listPage(ctx context.Context, uri string, page int, token string, size int) (listResp, error) {
	q := url.Values{"uri": {uri}, "page_size": {strconv.Itoa(size)}, "page": {strconv.Itoa(page)}}
	if token != "" {
		q.Set("next_page_token", token)
	}
	var r listResp
	err := p.call(ctx, "GET", "/file", q, nil, &r)
	return r, err
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	uri, err := p.uri(dir)
	if err != nil {
		return nil, err
	}
	out := []cloud.Entry{}
	page, token, first := 0, "", ""
	seen := map[string]bool{}
	for i := 0; i < maxPages; i++ {
		r, err := p.listPage(ctx, uri, page, token, listPage)
		if err != nil {
			return nil, err
		}
		for _, f := range r.Files {
			if f.Name == "" || strings.Contains(f.Name, "/") || f.Name == "." || f.Name == ".." || seen[f.Name] {
				continue
			}
			e := cloud.Entry{Name: f.Name, Path: path.Join(dir, f.Name), IsDir: f.Type == 1, Size: f.Size, ModTime: f.UpdatedAt}
			if e.IsDir {
				e.Size = 0
			}
			seen[f.Name] = true
			out = append(out, e)
		}
		if len(r.Files) == 0 || len(out) >= maxEntries {
			break
		}
		if r.Pagination.NextToken != "" {
			token = r.Pagination.NextToken
		} else if !r.Pagination.IsCursor && r.Files[0].Name != first {
			// offset paging: servers may cap page_size silently, so go on until an empty page (or a repeated one)
			first = r.Files[0].Name
			page++
		} else {
			break
		}
	}
	return out, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	uri, err := p.uri(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	var f file
	if err := p.call(ctx, "GET", "/file/info", url.Values{"uri": {uri}}, nil, &f); err != nil {
		return cloud.Entry{}, err
	}
	e := cloud.Entry{Name: path.Base(rel), Path: rel, IsDir: f.Type == 1, Size: f.Size, ModTime: f.UpdatedAt}
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
	uri, err := p.uri(rel)
	if err != nil {
		return nil, 0, err
	}
	var r struct {
		Urls []struct {
			URL string `json:"url"`
		} `json:"urls"`
	}
	if err := p.call(ctx, "POST", "/file/url", nil, map[string]any{"uris": []string{uri}, "download": true}, &r); err != nil {
		return nil, 0, err
	}
	if len(r.Urls) == 0 || r.Urls[0].URL == "" {
		return nil, 0, errors.New("服务器没有返回下载地址")
	}
	link := r.Urls[0].URL
	if u, err := url.Parse(link); err != nil || u.Host == "" {
		if err != nil || !strings.HasPrefix(link, "/") {
			return nil, 0, errors.New("服务器返回的下载地址无效")
		}
		link = p.c.URL("", nil) + link // site-relative
	} else if u.Scheme != "http" && u.Scheme != "https" {
		return nil, 0, errors.New("服务器返回的下载地址无效")
	}
	hdr := map[string]string{}
	if p.c.SameHost(link) {
		if tok, err := p.bearer(ctx, ""); err == nil && tok != "" {
			hdr["Authorization"] = "Bearer " + tok
		}
	}
	dctx, cancel := context.WithCancel(ctx)
	resp, err := p.c.Do(dctx, "GET", link, hdr, nil, -1)
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

func (p *provider) mkdirs(ctx context.Context, rel string) error {
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
		uri, _ := p.uri(cur)
		err = p.call(ctx, "POST", "/file/create", nil, map[string]any{"type": "folder", "uri": uri, "error_on_conflict": true}, nil)
		if err != nil && !errors.Is(err, cloud.ErrExists) {
			return err
		}
	}
	return nil
}

type uploadSession struct {
	SessionID     string   `json:"session_id"`
	ChunkSize     int64    `json:"chunk_size"`
	URI           string   `json:"uri"`
	CompleteURL   string   `json:"completeURL"`
	Callback      string   `json:"callback_secret"`
	UploadURLs    []string `json:"upload_urls"`
	Credential    string   `json:"credential"`
	StoragePolicy struct {
		Type  string `json:"type"`
		Relay bool   `json:"relay"`
	} `json:"storage_policy"`
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	uri, err := p.uri(rel)
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
	parent := path.Dir(rel)
	if parent == "." {
		parent = ""
	} else if err := p.mkdirs(ctx, parent); err != nil {
		return err
	}
	if size == 0 {
		// an empty file is created directly; uploading zero bytes can leave a stuck lock
		return p.call(ctx, "POST", "/file/create", nil, map[string]any{"type": "file", "uri": uri, "error_on_conflict": true}, nil)
	}
	parentURI, _ := p.uri(parent)
	pl, err := p.listPage(ctx, parentURI, 0, "", 10)
	if err != nil {
		return err
	}
	var sess uploadSession
	err = p.call(ctx, "PUT", "/file/upload", nil, map[string]any{
		"uri": uri, "size": size, "policy_id": pl.StoragePolicy.ID, "last_modified": time.Now().UnixMilli(), "mime_type": "",
	}, &sess)
	if err != nil {
		return err // code 40004 (target exists) arrives as cloud.ErrExists
	}
	cr := &cloudhttp.CountReader{R: io.LimitReader(r, size+1)}
	err = p.transfer(ctx, sess, cr, size)
	if err == nil && cr.N > size {
		err = errors.New("源文件比声明的长度更长，已中止上传")
	}
	if err != nil {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cloudhttp.APITimeout)
		_ = p.call(cctx, "DELETE", "/file/upload", nil, map[string]any{"id": sess.SessionID, "uri": sess.URI}, nil)
		cancel()
		return err
	}
	var got cloud.Entry
	for i := 0; i < 3; i++ {
		got, err = p.Stat(ctx, rel)
		if err == nil || !errors.Is(err, cloud.ErrNotFound) {
			break
		}
		time.Sleep(cloudhttp.Backoff)
	}
	if err != nil {
		return err
	}
	if got.Size != size {
		_ = p.Remove(ctx, rel)
		return fmt.Errorf("上传后大小不一致（%d != %d）", got.Size, size)
	}
	return nil
}

func (p *provider) transfer(ctx context.Context, s uploadSession, r io.Reader, size int64) error {
	chunk := s.ChunkSize
	if chunk <= 0 {
		chunk = size
	}
	if chunk > maxChunk {
		return fmt.Errorf("%w", cloud.ErrTooLarge)
	}
	typ := s.StoragePolicy.Type
	if s.StoragePolicy.Relay {
		typ = "local"
	}
	switch typ {
	case "local", "remote", "onedrive", "s3":
	default:
		return fmt.Errorf("%w：存储策略类型 %q 暂不支持写入", cloud.ErrUnsupported, typ)
	}
	buf := make([]byte, chunk)
	var etags []string
	for done, idx := int64(0), 0; done < size; idx++ {
		n := chunk
		if size-done < n {
			n = size - done
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return errors.New("读取源文件失败或源文件比声明的短")
		}
		data := buf[:n]
		var err error
		for attempt := 1; attempt <= chunkAttempts; attempt++ {
			switch typ {
			case "local":
				err = p.putLocal(ctx, s, idx, data)
			case "remote":
				err = p.putRemote(ctx, s, idx, data)
			case "onedrive":
				err = p.putOneDrive(ctx, s, done, size, data)
			case "s3":
				var etag string
				etag, err = p.putS3(ctx, s, idx, data)
				if err == nil {
					etags = append(etags, etag)
				}
			}
			if err == nil || attempt == chunkAttempts || ctx.Err() != nil || errors.Is(err, cloud.ErrAuth) || errors.Is(err, cloud.ErrPermission) {
				break
			}
			time.Sleep(cloudhttp.Backoff * time.Duration(attempt))
		}
		if err != nil {
			return err
		}
		done += n
	}
	switch typ {
	case "s3":
		return p.finishS3(ctx, s, etags)
	case "onedrive":
		return p.call(ctx, "POST", "/callback/onedrive/"+url.PathEscape(s.SessionID)+"/"+url.PathEscape(s.Callback), nil, map[string]string{}, nil)
	}
	return nil
}

// bytesPost sends a chunk and returns the response (body owned by the caller).
func (p *provider) send(ctx context.Context, method, link string, hdr map[string]string, data []byte) (*http.Response, error) {
	return p.c.Do(ctx, method, link, hdr, bytes.NewReader(data), int64(len(data)))
}

func (p *provider) putLocal(ctx context.Context, s uploadSession, idx int, data []byte) error {
	tok, err := p.bearer(ctx, "")
	if err != nil {
		return err
	}
	link := p.c.URL("/api/v4/file/upload/"+url.PathEscape(s.SessionID)+"/"+strconv.Itoa(idx), nil)
	resp, err := p.send(ctx, "POST", link, map[string]string{"Authorization": "Bearer " + tok, "Content-Type": "application/octet-stream"}, data)
	if err != nil {
		return err
	}
	return envelopeOK(resp)
}

func (p *provider) putRemote(ctx context.Context, s uploadSession, idx int, data []byte) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("服务器没有返回上传地址")
	}
	link := s.UploadURLs[0] + "?chunk=" + strconv.Itoa(idx)
	resp, err := p.send(ctx, "POST", link, map[string]string{"Authorization": s.Credential, "Content-Type": "application/octet-stream"}, data)
	if err != nil {
		return err
	}
	return envelopeOK(resp)
}

func (p *provider) putOneDrive(ctx context.Context, s uploadSession, off, total int64, data []byte) error {
	if len(s.UploadURLs) == 0 {
		return errors.New("服务器没有返回上传地址")
	}
	resp, err := p.send(ctx, "PUT", s.UploadURLs[0], map[string]string{
		"Content-Range": fmt.Sprintf("bytes %d-%d/%d", off, off+int64(len(data))-1, total)}, data)
	if err != nil {
		return err
	}
	cloudhttp.Drain(resp)
	switch resp.StatusCode {
	case 200, 201, 202:
		return nil
	}
	return cloudhttp.StatusErr(resp.StatusCode)
}

func (p *provider) putS3(ctx context.Context, s uploadSession, idx int, data []byte) (string, error) {
	if idx >= len(s.UploadURLs) {
		return "", errors.New("服务器提供的上传地址不足")
	}
	resp, err := p.send(ctx, "PUT", s.UploadURLs[idx], nil, data)
	if err != nil {
		return "", err
	}
	etag := resp.Header.Get("ETag")
	cloudhttp.Drain(resp)
	if resp.StatusCode != 200 {
		return "", cloudhttp.StatusErr(resp.StatusCode)
	}
	if etag == "" {
		return "", errors.New("存储服务没有返回 ETag")
	}
	return etag, nil
}

func (p *provider) finishS3(ctx context.Context, s uploadSession, etags []string) error {
	var b strings.Builder
	b.WriteString("<CompleteMultipartUpload>")
	for i, e := range etags {
		fmt.Fprintf(&b, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", i+1, e)
	}
	b.WriteString("</CompleteMultipartUpload>")
	resp, err := p.send(ctx, "POST", s.CompleteURL, map[string]string{"Content-Type": "application/xml"}, []byte(b.String()))
	if err != nil {
		return err
	}
	cloudhttp.Drain(resp)
	if resp.StatusCode != 200 {
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	return p.call(ctx, "GET", "/callback/s3/"+url.PathEscape(s.SessionID)+"/"+url.PathEscape(s.Callback), nil, nil, nil)
}

func envelopeOK(resp *http.Response) error {
	if resp.StatusCode/100 != 2 {
		cloudhttp.Drain(resp)
		return cloudhttp.StatusErr(resp.StatusCode)
	}
	var env envelope
	if err := cloudhttp.ReadJSON(resp, &env); err != nil {
		return err
	}
	return classify(env)
}

// Remove deletes a single file (never a folder).
func (p *provider) Remove(ctx context.Context, rel string) error {
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	uri, err := p.uri(rel)
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
	return p.call(ctx, "DELETE", "/file", nil, map[string]any{"uris": []string{uri}, "unlink": false, "skip_soft_delete": true}, nil)
}
