// Package webdav is the WebDAV storage provider (Nextcloud/ownCloud, Synology, SharePoint libraries and the
// OpenList/AList gateway). It is a small hand-written client on net/http.
package webdav

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"oa-agent/backend/internal/cloud"
)

const (
	maxEntries = 5000
	maxXMLSize = 32 << 20
	opTimeout  = 30 * time.Second
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "webdav",
		Name:        "WebDAV",
		Group:       "gateway",
		Description: "WebDAV：Nextcloud/ownCloud、群晖、SharePoint 文档库，以及 OpenList/AList 网关（可间接访问各类网盘）。",
		Fields: []cloud.Field{
			{Key: "url", Label: "WebDAV 地址", Type: cloud.TText, Required: true, Placeholder: "https://cloud.example.com/remote.php/dav/files/user",
				Help: "完整的集合 URL，仅支持 http/https。Nextcloud：https://host/remote.php/dav/files/用户名；群晖：https://host:5006；OpenList/AList：http://host:5244/dav。"},
			{Key: "auth", Label: "认证方式", Type: cloud.TSelect, Default: "basic", Options: []cloud.Option{{Value: "basic", Label: "用户名 + 密码"}, {Value: "bearer", Label: "Bearer 令牌"}}},
			{Key: "username", Label: "用户名", Type: cloud.TText, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "basic"}},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "basic"},
				Help: "Nextcloud/ownCloud 建议使用“应用密码”。"},
			{Key: "token", Label: "访问令牌", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "bearer"}},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}},
				Help: "仅在内网自签名证书时开启。"},
		},
		Factory: New,
	})
}

type provider struct {
	hc       *http.Client
	base     *url.URL // base collection URL, path without trailing slash
	basePath string   // connection base path (relative to base)
	auth     string   // full Authorization header value or ""
}

// New connects (no network traffic) a WebDAV provider.
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	raw := s.Get("url")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "WebDAV 地址无效（仅支持 http/https，且不含用户信息）"}
	}
	if err := cloud.CheckHost(u.Hostname()); err != nil {
		return nil, err
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	p := &provider{base: u, basePath: s.BasePath}
	switch s.Get("auth") {
	case "bearer":
		p.auth = "Bearer " + s.Secrets["token"]
	default:
		if s.Get("username") != "" || s.Secrets["password"] != "" {
			req, _ := http.NewRequest("GET", "http://x", nil)
			req.SetBasicAuth(s.Get("username"), s.Secrets["password"])
			p.auth = req.Header.Get("Authorization")
		}
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opTimeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.Bool("insecure_skip_verify", false)},
	}
	origHost := u.Host
	p.hc = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many redirects")
		}
		if !strings.EqualFold(req.URL.Host, origHost) {
			return errors.New("redirect to another host refused")
		}
		return nil
	}}
	return p, nil
}

func (p *provider) Close() error { p.hc.CloseIdleConnections(); return nil }

// fullPath maps a provider-relative path to the absolute URL path (decoded form).
func (p *provider) fullPath(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	full := cloud.Join(p.basePath, rel)
	for _, c := range strings.Split(full, "/") {
		if c == ".." || c == "." {
			return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
		}
	}
	if full == "" {
		return p.base.Path, nil
	}
	return p.base.Path + "/" + full, nil
}

func (p *provider) urlFor(abs string, dir bool) string {
	u := *p.base
	segs := strings.Split(abs, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	u.Path = abs
	u.RawPath = strings.Join(segs, "/")
	if dir && !strings.HasSuffix(u.RawPath, "/") {
		u.Path += "/"
		u.RawPath += "/"
	}
	return u.String()
}

func (p *provider) do(ctx context.Context, method, abs string, dir bool, hdr map[string]string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, p.urlFor(abs, dir), body)
	if err != nil {
		return nil, fmt.Errorf("%w: bad request", cloud.ErrUnreachable)
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	if p.auth != "" {
		req.Header.Set("Authorization", p.auth)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := p.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ne net.Error
		if errors.Is(err, cloud.ErrUnreachable) || errors.As(err, &ne) || strings.Contains(err.Error(), "redirect") {
			return nil, fmt.Errorf("%w", cloud.ErrUnreachable)
		}
		return nil, fmt.Errorf("%w: request failed", cloud.ErrUnreachable)
	}
	return resp, nil
}

func statusErr(code int) error {
	switch {
	case code == 401:
		return fmt.Errorf("%w", cloud.ErrAuth)
	case code == 403:
		return fmt.Errorf("%w", cloud.ErrPermission)
	case code == 404 || code == 410:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case code == 412:
		return fmt.Errorf("%w", cloud.ErrExists)
	case code == 423:
		return errors.New("webdav: resource is locked")
	case code == 507:
		return errors.New("webdav: insufficient storage")
	}
	return fmt.Errorf("webdav: unexpected status %d", code)
}

func drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
}

type multistatus struct {
	Responses []davResponse `xml:"response"`
}
type davResponse struct {
	Href     string     `xml:"href"`
	Propstat []propstat `xml:"propstat"`
	Status   string     `xml:"status"`
}
type propstat struct {
	Status string `xml:"status"`
	Prop   struct {
		Length       string `xml:"getcontentlength"`
		Modified     string `xml:"getlastmodified"`
		ResourceType struct {
			Collection *struct{} `xml:"collection"`
		} `xml:"resourcetype"`
	} `xml:"prop"`
}

const propfindBody = `<?xml version="1.0" encoding="utf-8"?><d:propfind xmlns:d="DAV:"><d:prop><d:resourcetype/><d:getcontentlength/><d:getlastmodified/></d:prop></d:propfind>`

type item struct {
	abs   string // decoded absolute path, no trailing slash
	entry cloud.Entry
}

func (p *provider) propfind(ctx context.Context, abs string, depth string, dir bool) ([]item, error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	resp, err := p.do(ctx, "PROPFIND", abs, dir, map[string]string{"Depth": depth, "Content-Type": "application/xml"}, strings.NewReader(propfindBody), int64(len(propfindBody)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 207 {
		drain(resp)
		if resp.StatusCode == 200 {
			return nil, errors.New("webdav: server does not speak WebDAV at this URL")
		}
		return nil, statusErr(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxXMLSize+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read failed", cloud.ErrUnreachable)
	}
	if len(data) > maxXMLSize {
		return nil, errors.New("webdav: listing too large")
	}
	return p.parse(data)
}

func (p *provider) parse(data []byte) ([]item, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = nil
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	var ms multistatus
	if err := dec.Decode(&ms); err != nil {
		return nil, errors.New("webdav: invalid multistatus response")
	}
	var out []item
	for _, r := range ms.Responses {
		href := strings.TrimSpace(r.Href)
		u, err := url.Parse(href)
		if err != nil {
			continue
		}
		abs := u.Path // url.Parse percent-decodes
		isDir := strings.HasSuffix(abs, "/")
		abs = path.Clean("/" + strings.Trim(abs, "/"))
		if abs == "/" {
			abs = ""
		}
		e := cloud.Entry{IsDir: isDir}
		ok := len(r.Propstat) == 0
		for _, ps := range r.Propstat {
			if !strings.Contains(ps.Status, " 200") && ps.Status != "" {
				continue
			}
			ok = true
			if ps.Prop.ResourceType.Collection != nil {
				e.IsDir = true
			}
			if n := strings.TrimSpace(ps.Prop.Length); n != "" {
				fmt.Sscanf(n, "%d", &e.Size)
			}
			if m := strings.TrimSpace(ps.Prop.Modified); m != "" {
				if t, err := http.ParseTime(m); err == nil {
					e.ModTime = t
				}
			}
		}
		if !ok {
			continue
		}
		if e.IsDir {
			e.Size = 0
		}
		out = append(out, item{abs: abs, entry: e})
	}
	return out, nil
}

// rel converts an absolute decoded path to a path relative to the connection base; ok=false when outside.
func (p *provider) rel(abs string) (string, bool) {
	root := p.base.Path
	if p.basePath != "" {
		root += "/" + p.basePath
	}
	root = path.Clean("/" + strings.Trim(root, "/"))
	if root == "/" {
		root = ""
	}
	if abs == root {
		return "", true
	}
	if strings.HasPrefix(abs, root+"/") {
		return strings.TrimPrefix(abs, root+"/"), true
	}
	return "", false
}

// norm normalises our own absolute path the same way parse does.
func norm(abs string) string {
	a := path.Clean("/" + strings.Trim(abs, "/"))
	if a == "/" {
		return ""
	}
	return a
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	abs, err := p.fullPath(dir)
	if err != nil {
		return nil, err
	}
	items, err := p.propfind(ctx, abs, "1", true)
	if err != nil {
		return nil, err
	}
	self := norm(abs)
	out := []cloud.Entry{}
	for _, it := range items {
		if it.abs == self {
			continue
		}
		rel, ok := p.rel(it.abs)
		if !ok || rel == "" || path.Dir(rel) != path.Clean(orDot(dir)) {
			continue
		}
		e := it.entry
		e.Path = rel
		e.Name = path.Base(rel)
		out = append(out, e)
		if len(out) >= maxEntries {
			break
		}
	}
	return out, nil
}

func orDot(s string) string {
	if s == "" {
		return "."
	}
	return s
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	abs, err := p.fullPath(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	items, err := p.propfind(ctx, abs, "0", false)
	if err != nil {
		return cloud.Entry{}, err
	}
	for _, it := range items {
		if it.abs == norm(abs) {
			e := it.entry
			e.Path = rel
			e.Name = path.Base(rel)
			if rel == "" {
				e.Name = ""
			}
			return e, nil
		}
	}
	if len(items) == 1 {
		e := items[0].entry
		e.Path, e.Name = rel, path.Base(rel)
		return e, nil
	}
	return cloud.Entry{}, fmt.Errorf("%w", cloud.ErrNotFound)
}

type bodyCloser struct {
	io.Reader
	c io.Closer
	f func()
}

func (b bodyCloser) Close() error { b.f(); return b.c.Close() }

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	abs, err := p.fullPath(rel)
	if err != nil {
		return nil, 0, err
	}
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	ctx, cancel := context.WithCancel(ctx)
	resp, err := p.do(ctx, "GET", abs, false, nil, nil, -1)
	if err != nil {
		cancel()
		return nil, 0, err
	}
	if resp.StatusCode != 200 {
		drain(resp)
		cancel()
		return nil, 0, statusErr(resp.StatusCode)
	}
	return bodyCloser{Reader: resp.Body, c: resp.Body, f: cancel}, resp.ContentLength, nil
}

func (p *provider) mkcol(ctx context.Context, abs string) error {
	resp, err := p.do(ctx, "MKCOL", abs, true, nil, nil, -1)
	if err != nil {
		return err
	}
	drain(resp)
	switch resp.StatusCode {
	case 200, 201, 405:
		return nil // 405: already exists
	case 409:
		return nil // some servers answer 409 for an existing collection; PUT will surface a real problem
	}
	return statusErr(resp.StatusCode)
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	abs, err := p.fullPath(rel)
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
	// create each missing parent level (below the connection root only)
	root := p.base.Path
	parts := strings.Split(strings.TrimPrefix(abs, root+"/"), "/")
	cur := root
	for _, seg := range parts[:len(parts)-1] {
		cur += "/" + seg
		if err := p.mkcol(ctx, cur); err != nil {
			return err
		}
	}
	// the body is streamed with the exact length; a short or long source is detected locally
	cr := &countReader{r: io.LimitReader(r, size+1)}
	body := io.Reader(cr)
	if size == 0 {
		body = http.NoBody
	}
	resp, err := p.do(ctx, "PUT", abs, false, map[string]string{"If-None-Match": "*", "Content-Type": "application/octet-stream"}, body, size)
	if err != nil {
		return err
	}
	drain(resp)
	if cr.n > size {
		p.cleanup(ctx, abs)
		return errors.New("webdav: source longer than declared size")
	}
	switch resp.StatusCode {
	case 200, 201, 204:
	default:
		return statusErr(resp.StatusCode)
	}
	got, err := p.Stat(ctx, rel)
	if err != nil {
		return err
	}
	if got.Size != size {
		p.cleanup(ctx, abs)
		return fmt.Errorf("webdav: size mismatch after upload (%d != %d)", got.Size, size)
	}
	return nil
}

func (p *provider) cleanup(ctx context.Context, abs string) {
	if resp, err := p.do(ctx, "DELETE", abs, false, nil, nil, -1); err == nil {
		drain(resp)
	}
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.n += int64(n)
	return n, err
}

func (p *provider) Remove(ctx context.Context, rel string) error {
	abs, err := p.fullPath(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	resp, err := p.do(ctx, "DELETE", abs, false, nil, nil, -1)
	if err != nil {
		return err
	}
	drain(resp)
	if resp.StatusCode/100 == 2 {
		return nil
	}
	return statusErr(resp.StatusCode)
}
