// Package oauthhttp holds the HTTP plumbing shared by the OAuth based storage providers (OneDrive, Google Drive,
// Dropbox): a client built on cloud.SafeDialer, an access-token cache with a refresh-token flow, retries with
// Retry-After, 401-then-refresh handling, and error mapping onto the cloud sentinels. Error texts never contain
// tokens, secrets, URLs or response bodies; the (truncated) body is kept in StatusError.Detail for matching only.
package oauthhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
)

const (
	// MaxJSON bounds every JSON body read from a provider.
	MaxJSON = 8 << 20
	// MaxAttempts is the number of tries for retryable failures (429, 5xx, transient network errors on GET).
	MaxAttempts  = 3
	maxDetail    = 4096
	tokenMargin  = 2 * time.Minute
	maxRedirects = 5
)

var (
	// BackoffBase and MaxRetryAfter are variables so tests can shorten the waits.
	BackoffBase   = 500 * time.Millisecond
	MaxRetryAfter = 30 * time.Second

	// ErrConflict: the provider reported a name conflict (HTTP 409/412). Providers map it to cloud.ErrExists.
	ErrConflict = errors.New("conflict")
	// ErrRateLimit: HTTP 429 persisted after the retries.
	ErrRateLimit = errors.New("rate limited")
	// ErrServer: HTTP 5xx persisted after the retries, or another unexpected status.
	ErrServer = errors.New("unexpected server response")
)

// StatusError is an HTTP failure. Error() is safe to log and show; Detail is the raw (truncated) response body
// and must never be shown.
type StatusError struct {
	Provider string
	Op       string
	Status   int
	Kind     error
	Detail   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s %s: %s (HTTP %d)", e.Provider, e.Op, hint(e.Kind), e.Status)
}
func (e *StatusError) Unwrap() error { return e.Kind }

func hint(kind error) string {
	switch {
	case errors.Is(kind, cloud.ErrAuth):
		return "认证失败：请检查 Client ID、Client Secret 与 Refresh Token 是否正确、是否已被撤销"
	case errors.Is(kind, cloud.ErrPermission):
		return "权限不足：应用未获得读写文件的授权，或账号无权访问该位置"
	case errors.Is(kind, cloud.ErrNotFound):
		return "路径不存在"
	case errors.Is(kind, cloud.ErrExists), errors.Is(kind, ErrConflict):
		return "名称冲突"
	case errors.Is(kind, cloud.ErrTooLarge):
		return "文件过大或存储空间不足"
	case errors.Is(kind, ErrRateLimit):
		return "请求过于频繁（已重试），请稍后再试"
	case errors.Is(kind, cloud.ErrUnreachable):
		return "无法连接服务器"
	}
	return "服务返回了意外的响应（已重试），请稍后再试"
}

// KindForStatus maps an HTTP status to a sentinel.
func KindForStatus(status int) error {
	switch {
	case status == 401:
		return cloud.ErrAuth
	case status == 403:
		return cloud.ErrPermission
	case status == 404 || status == 410:
		return cloud.ErrNotFound
	case status == 409 || status == 412:
		return ErrConflict
	case status == 413 || status == 507:
		return cloud.ErrTooLarge
	case status == 429:
		return ErrRateLimit
	}
	return ErrServer
}

// NewClient returns an HTTP client for provider traffic: SafeDialer, no overall timeout (the context governs;
// downloads may be long), header/handshake timeouts, and a redirect policy that refuses https->http downgrades and
// long chains. The Authorization header is dropped when a redirect leaves the host.
func NewClient() *http.Client {
	tr := &http.Transport{
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return errors.New("redirect to non-https URL refused")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}

// Endpoint returns override when (and only when) it is an http(s) URL whose host is localhost, a loopback or a
// private address, else def. It exists so tests can point a provider at an httptest server; the production form
// never offers the setting and the validator rejects unknown keys.
func Endpoint(def, override string) string {
	override = strings.TrimSpace(override)
	if override == "" {
		return def
	}
	u, err := url.Parse(override)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Hostname() == "" {
		return def
	}
	h := u.Hostname()
	if h != "localhost" {
		ip, err := netip.ParseAddr(h)
		if err != nil || !(ip.IsLoopback() || ip.IsPrivate()) {
			return def
		}
	}
	return strings.TrimRight(u.String(), "/")
}

// SafePath validates base and rel and joins them; the result never leaves base.
func SafePath(base, rel string) (string, error) {
	b, err := cloud.CleanRel(base)
	if err != nil {
		return "", err
	}
	r, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return cloud.Join(b, r), nil
}

// ---- token cache ----

// TokenSource caches an access token and refreshes it shortly before it expires or on demand.
type TokenSource struct {
	// Refresh obtains a new access token and its lifetime.
	Refresh func(ctx context.Context) (string, time.Duration, error)
	Now     func() time.Time

	mu  sync.Mutex
	tok string
	exp time.Time
}

// Get returns a valid token. When stale is the token the caller just saw rejected, a refresh is forced unless
// another goroutine already replaced it (single flight).
func (t *TokenSource) Get(ctx context.Context, stale string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now
	if t.Now != nil {
		now = t.Now
	}
	if t.tok != "" && t.tok != stale && now().Before(t.exp) {
		return t.tok, nil
	}
	tok, ttl, err := t.Refresh(ctx)
	if err != nil {
		return "", err
	}
	if ttl <= 0 {
		ttl = time.Hour
	}
	margin := tokenMargin
	if ttl <= 2*margin {
		margin = ttl / 4
	}
	t.tok, t.exp = tok, now().Add(ttl-margin)
	return tok, nil
}

// ---- requests ----

// Doer sends requests with retries and the 401 refresh. Token == nil sends no Authorization header.
type Doer struct {
	Name  string // provider name used in error messages
	HC    *http.Client
	Token func(ctx context.Context, stale string) (string, error)
	// ShouldRetry403 is consulted for HTTP 403 (e.g. Google's rateLimitExceeded) with the truncated body.
	ShouldRetry403 func(detail string) bool
}

// Anon returns a Doer that never adds an Authorization header (pre-authenticated upload URLs).
func (d *Doer) Anon() *Doer {
	c := *d
	c.Token = nil
	return &c
}

func sleep(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryAfter(h http.Header, attempt int) time.Duration {
	wait := BackoffBase << (attempt - 1)
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			wait = time.Duration(n) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			wait = time.Until(t)
		}
	}
	if wait < 0 {
		wait = 0
	}
	if wait > MaxRetryAfter {
		wait = MaxRetryAfter
	}
	return wait
}

func readDetail(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxDetail))
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	return string(b)
}

// Do sends the request built by build (called once per attempt, so bodies are replayable). It returns the response
// for status < 400 (the caller closes the body) and a *StatusError otherwise. Network errors become
// cloud.ErrUnreachable; context errors are returned wrapped.
func (d *Doer) Do(ctx context.Context, op string, build func(ctx context.Context) (*http.Request, error)) (*http.Response, error) {
	refreshed := false
	stale := ""
	attempt := 1
	for {
		req, err := build(ctx)
		if err != nil {
			return nil, err
		}
		used := ""
		if d.Token != nil {
			tok, err := d.Token(ctx, stale)
			if err != nil {
				return nil, err
			}
			used = tok
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := d.HC.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("%s %s: %w", d.Name, op, ctx.Err())
			}
			idem := req.Method == http.MethodGet || req.Method == http.MethodHead
			if idem && attempt < MaxAttempts {
				if err := sleep(ctx, retryAfter(http.Header{}, attempt)); err != nil {
					return nil, err
				}
				attempt++
				continue
			}
			// The raw error may contain URLs or addresses; do not expose it.
			return nil, &StatusError{Provider: d.Name, Op: op, Kind: cloud.ErrUnreachable}
		}
		if resp.StatusCode < 400 {
			return resp, nil
		}
		status := resp.StatusCode
		hdr := resp.Header
		detail := readDetail(resp)
		if status == 401 && d.Token != nil && !refreshed {
			refreshed = true
			stale = used
			continue
		}
		retry := status == 429 || status == 500 || status == 502 || status == 503 || status == 504 ||
			(status == 403 && d.ShouldRetry403 != nil && d.ShouldRetry403(detail))
		if retry && attempt < MaxAttempts {
			if err := sleep(ctx, retryAfter(hdr, attempt)); err != nil {
				return nil, err
			}
			attempt++
			continue
		}
		return nil, &StatusError{Provider: d.Name, Op: op, Status: status, Kind: KindForStatus(status), Detail: detail}
	}
}

// DecodeJSON reads at most MaxJSON bytes of the body into v and closes it.
func DecodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, MaxJSON+1))
	if err != nil {
		return &StatusError{Provider: "cloud", Op: "read", Status: resp.StatusCode, Kind: cloud.ErrUnreachable}
	}
	if len(b) > MaxJSON {
		return &StatusError{Provider: "cloud", Op: "read", Status: resp.StatusCode, Kind: ErrServer}
	}
	if v == nil {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return &StatusError{Provider: "cloud", Op: "decode", Status: resp.StatusCode, Kind: ErrServer}
	}
	return nil
}

// Drain discards and closes a response body.
func Drain(resp *http.Response) {
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
}

// JSONBody returns a replayable JSON request body.
func JSONBody(v any) ([]byte, error) { return json.Marshal(v) }

// BytesReader wraps b for a fresh request body.
func BytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

// ReadChunk reads exactly n bytes (n > 0) from r; a shorter stream is an error because PutNew's size is exact.
func ReadChunk(r io.Reader, n int64) ([]byte, error) {
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, errors.New("source ended before the declared size")
		}
		return nil, err
	}
	return buf, nil
}

// ---- OAuth refresh-token flow ----

// OAuth exchanges a refresh token for access tokens and persists a rotated refresh token.
type OAuth struct {
	Name         string
	HC           *http.Client
	TokenURL     string
	ClientID     string
	ClientSecret string
	Scope        string // optional
	PersistKey   string // settings key of the refresh token, default "refresh_token"
	Persist      func(key, value string)

	mu      sync.Mutex
	refresh string
}

// NewOAuth stores the initial refresh token.
func NewOAuth(o *OAuth, refreshToken string) *OAuth {
	o.refresh = refreshToken
	if o.PersistKey == "" {
		o.PersistKey = "refresh_token"
	}
	return o
}

// RefreshToken returns the refresh token currently in use (changes after a rotation).
func (o *OAuth) RefreshToken() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.refresh
}

// Refresh performs the refresh_token grant. 400/401/403 from the token endpoint are authentication failures.
func (o *OAuth) Refresh(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {o.RefreshToken()},
		"client_id":     {o.ClientID},
		"client_secret": {o.ClientSecret},
	}
	if o.Scope != "" {
		form.Set("scope", o.Scope)
	}
	body := form.Encode()
	d := &Doer{Name: o.Name, HC: o.HC}
	resp, err := d.Do(ctx, "token", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.TokenURL, strings.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		return req, nil
	})
	if err != nil {
		var se *StatusError
		if errors.As(err, &se) && (se.Status == 400 || se.Status == 401 || se.Status == 403) {
			return "", 0, &StatusError{Provider: o.Name, Op: "token", Status: se.Status, Kind: cloud.ErrAuth}
		}
		return "", 0, err
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		ExpiresIn    int64  `json:"expires_in"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := DecodeJSON(resp, &out); err != nil {
		return "", 0, err
	}
	if out.AccessToken == "" {
		return "", 0, &StatusError{Provider: o.Name, Op: "token", Status: 200, Kind: cloud.ErrAuth}
	}
	if out.RefreshToken != "" {
		o.mu.Lock()
		changed := out.RefreshToken != o.refresh
		o.refresh = out.RefreshToken
		o.mu.Unlock()
		if changed && o.Persist != nil {
			func() {
				defer func() { _ = recover() }() // a failing persist must never break the transfer
				o.Persist(o.PersistKey, out.RefreshToken)
			}()
		}
	}
	return out.AccessToken, time.Duration(out.ExpiresIn) * time.Second, nil
}
