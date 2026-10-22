// Package cloudhttp is the small shared HTTP layer of the self-hosted private-cloud providers (OpenList, Seafile,
// Cloudreve): a client that dials through cloud.SafeDialer, validates the server URL like the WebDAV provider
// (http or https, LAN and loopback allowed, link-local blocked), never forwards credentials to another host or
// across an https->http downgrade, retries 429/5xx with backoff, bounds JSON bodies and never puts URLs, secrets
// or raw server bodies into errors.
package cloudhttp

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
	"time"

	"oa-agent/backend/internal/cloud"
)

const (
	MaxJSON     = 8 << 20
	APITimeout  = 30 * time.Second
	maxAttempts = 3
)

// Backoff is the first retry delay (doubled per attempt); tests shorten it.
var Backoff = 400 * time.Millisecond

// Client is an HTTP client bound to one server base URL.
type Client struct {
	HC   *http.Client
	Base *url.URL // path without trailing slash
}

// New validates the user-typed server URL and builds the client.
func New(raw string, insecureSkipVerify bool) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "服务器地址无效（仅支持 http/https，且不含用户信息）"}
	}
	if err := cloud.CheckHost(u.Hostname()); err != nil {
		return nil, err
	}
	u.RawQuery, u.Fragment = "", ""
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Minute,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecureSkipVerify},
	}
	c := &Client{Base: u}
	c.HC = &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if !strings.EqualFold(req.URL.Host, via[0].URL.Host) || (via[0].URL.Scheme == "https" && req.URL.Scheme != "https") {
			req.Header.Del("Authorization")
			req.Header.Del("Cookie")
		}
		return nil
	}}
	return c, nil
}

func (c *Client) Close() { c.HC.CloseIdleConnections() }

// URL builds an absolute URL below the base from an already-decoded path ("/api/x") and optional query.
func (c *Client) URL(p string, q url.Values) string {
	u := *c.Base
	u.Path = c.Base.Path + p
	u.RawPath = ""
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// EscapePath percent-escapes each segment of a "/"-separated path.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// SameHost reports whether the absolute URL points to the server's own host (credentials may be sent there).
func (c *Client) SameHost(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Host, c.Base.Host) && u.Scheme == c.Base.Scheme
}

// Do sends one request. When body is nil or a *bytes.Reader/bytes slice (see DoBytes) it may be retried by the
// caller; Do itself never retries. Network failures map to cloud.ErrUnreachable without leaking the URL.
func (c *Client) Do(ctx context.Context, method, rawURL string, hdr map[string]string, body io.Reader, size int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	if body != nil && size >= 0 {
		req.ContentLength = size
		if size == 0 {
			req.Body = http.NoBody
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.HC.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return resp, nil
}

// DoBytes sends a replayable request, retrying 429 and 5xx up to 3 attempts with exponential backoff.
func (c *Client) DoBytes(ctx context.Context, method, rawURL string, hdr map[string]string, body []byte) (*http.Response, error) {
	delay := Backoff
	for attempt := 1; ; attempt++ {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		resp, err := c.Do(ctx, method, rawURL, hdr, r, int64(len(body)))
		if err != nil {
			if attempt >= maxAttempts || !errors.Is(err, cloud.ErrUnreachable) {
				return nil, err
			}
		} else if (resp.StatusCode != 429 && resp.StatusCode < 500) || attempt >= maxAttempts {
			return resp, nil
		} else {
			Drain(resp)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		delay *= 2
	}
}

// Drain discards (a bounded part of) the body and closes it.
func Drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	resp.Body.Close()
}

// ReadJSON decodes a bounded JSON body into v and closes the body.
func ReadJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxJSON+1))
	if err != nil {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	if len(data) > MaxJSON {
		return errors.New("服务器响应过大")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return errors.New("服务器响应格式无法识别（地址是否指向正确的服务？）")
	}
	return nil
}

// StatusErr maps an HTTP status to the cloud error vocabulary (never echoes bodies).
func StatusErr(code int) error {
	switch {
	case code == 401:
		return fmt.Errorf("%w", cloud.ErrAuth)
	case code == 403:
		return fmt.Errorf("%w", cloud.ErrPermission)
	case code == 404 || code == 410:
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case code == 429:
		return errors.New("服务器请求过于频繁，请稍后重试")
	case code >= 500:
		return fmt.Errorf("服务器暂时不可用（HTTP %d）", code)
	}
	return fmt.Errorf("服务器拒绝了请求（HTTP %d）", code)
}

// CountReader counts bytes read.
type CountReader struct {
	R io.Reader
	N int64
}

func (c *CountReader) Read(b []byte) (int, error) {
	n, err := c.R.Read(b)
	c.N += int64(n)
	return n, err
}

// BodyCloser ties a cancel func to a response body.
type BodyCloser struct {
	io.Reader
	C      io.Closer
	Cancel func()
}

func (b BodyCloser) Close() error {
	err := b.C.Close()
	if b.Cancel != nil {
		b.Cancel()
	}
	return err
}

// Abs converts a provider-relative path to an absolute "/"-path below the connection base path, rejecting
// anything that could escape it.
func Abs(basePath, rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	full := cloud.Join(basePath, rel)
	for _, c := range strings.Split(full, "/") {
		if c == ".." || c == "." {
			return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
		}
	}
	return "/" + strings.Trim(full, "/"), nil
}
