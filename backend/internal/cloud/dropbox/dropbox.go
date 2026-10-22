// Adapted from OpenList (AGPL-3.0) drivers/dropbox
//
// Package dropbox is the Dropbox (API v2) storage provider. Only list, stat, download, upload (never overwriting)
// and single-file delete are implemented.
//
// API facts (Dropbox HTTP API docs):
//   - token: POST https://api.dropboxapi.com/oauth2/token (grant_type=refresh_token, client_id, client_secret)
//   - POST /2/files/list_folder and /list_folder/continue, POST /2/files/get_metadata (JSON bodies; "" is the root,
//     other paths start with "/"; matching is case-insensitive)
//   - POST content.dropboxapi.com/2/files/download with the argument in the Dropbox-API-Arg header (JSON, non-ASCII
//     characters must be \u-escaped)
//   - POST content.dropboxapi.com/2/files/upload (up to 150 MB per request) with mode "add" and autorename false:
//     an existing file makes the call fail with HTTP 409 path/conflict, atomically; parent folders are created
//     automatically. Larger files use upload_session/start, append_v2 and finish with the same commit info.
//   - POST /2/files/delete_v2
//
// Not covered: Dropbox Business team namespaces (Dropbox-API-Path-Root / Select-User headers).
package dropbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf16"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/oauthhttp"
)

const (
	defaultAPI     = "https://api.dropboxapi.com/2"
	defaultContent = "https://content.dropboxapi.com/2"
	defaultToken   = "https://api.dropboxapi.com/oauth2/token"
	chunkSize      = 8 << 20 // single request up to this size, sessions use it as the chunk size
	maxPages       = 500
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "dropbox",
		Name:        "Dropbox",
		Group:       "cloud",
		Description: "Dropbox 个人或团队成员空间。使用 OAuth 刷新令牌访问，不会覆盖已有文件。",
		DocsURL:     "https://www.dropbox.com/developers/documentation/http/overview",
		Fields: []cloud.Field{
			{Key: "client_id", Label: "App key", Type: cloud.TText, Required: true,
				Help: "在 Dropbox App Console 创建应用后，Settings 页中的 App key。需勾选 files.metadata.read、files.content.read、files.content.write 权限。"},
			{Key: "client_secret", Label: "App secret", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "同一页面的 App secret，加密保存。"},
			{Key: "refresh_token", Label: "Refresh Token", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "授权时使用 token_access_type=offline 取得的长期刷新令牌。"},
		},
		Factory: factory,
	})
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	p, err := New(s)
	if err != nil {
		return nil, err
	}
	if _, err := p.tokens.Get(ctx, ""); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

// Provider implements cloud.Provider for Dropbox.
type Provider struct {
	api, content string
	base         string
	hc           *http.Client
	d            *oauthhttp.Doer
	tokens       *oauthhttp.TokenSource
}

// New builds the provider without contacting the service.
func New(s cloud.Settings) (*Provider, error) {
	clientID, secret, refresh := s.Get("client_id"), strings.TrimSpace(s.Secrets["client_secret"]), strings.TrimSpace(s.Secrets["refresh_token"])
	if clientID == "" || secret == "" || refresh == "" {
		return nil, fmt.Errorf("%w: client_id, client_secret and refresh_token are required", cloud.ErrAuth)
	}
	base, err := oauthhttp.SafePath(s.BasePath, "")
	if err != nil {
		return nil, err
	}
	api, content, tok := defaultAPI, defaultContent, defaultToken
	if ep := oauthhttp.Endpoint("", s.Get("endpoint")); ep != "" { // test hook, loopback/private hosts only
		api, content, tok = ep+"/2", ep+"/2", ep+"/oauth2/token"
	}
	hc := oauthhttp.NewClient()
	oa := oauthhttp.NewOAuth(&oauthhttp.OAuth{Name: "dropbox", HC: hc, TokenURL: tok, ClientID: clientID, ClientSecret: secret, Persist: s.Persist}, refresh)
	p := &Provider{api: api, content: content, base: base, hc: hc}
	p.tokens = &oauthhttp.TokenSource{Refresh: oa.Refresh}
	p.d = &oauthhttp.Doer{Name: "dropbox", HC: hc, Token: p.tokens.Get}
	return p, nil
}

func (p *Provider) Close() error { p.hc.CloseIdleConnections(); return nil }

// dbPath converts a validated relative path to a Dropbox path.
func (p *Provider) dbPath(rel string) (string, error) {
	full, err := oauthhttp.SafePath(p.base, rel)
	if err != nil {
		return "", err
	}
	if full == "" {
		return "", nil
	}
	return "/" + full, nil
}

// headerJSON encodes v as JSON that is safe in an HTTP header: every non-ASCII character is \u-escaped.
func headerJSON(v any) string {
	b, _ := json.Marshal(v)
	var sb strings.Builder
	for _, r := range string(b) {
		if r < 0x7f {
			sb.WriteRune(r)
			continue
		}
		if r > 0xffff {
			r1, r2 := utf16.EncodeRune(r)
			fmt.Fprintf(&sb, `\u%04x\u%04x`, r1, r2)
		} else {
			fmt.Fprintf(&sb, `\u%04x`, r)
		}
	}
	return sb.String()
}

// mapErr turns Dropbox's endpoint-specific HTTP 409 errors into the cloud sentinels.
func mapErr(op string, err error) error {
	var se *oauthhttp.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusConflict {
		return err
	}
	var body struct {
		Summary string `json:"error_summary"`
	}
	_ = json.Unmarshal([]byte(se.Detail), &body)
	sum := body.Summary
	kind := oauthhttp.ErrServer
	switch {
	case strings.Contains(sum, "not_found"), strings.Contains(sum, "not_folder"):
		kind = cloud.ErrNotFound
	case strings.Contains(sum, "conflict"):
		kind = cloud.ErrExists
	case strings.Contains(sum, "insufficient_space"):
		kind = cloud.ErrTooLarge
	case strings.Contains(sum, "no_write_permission"), strings.Contains(sum, "restricted_content"):
		kind = cloud.ErrPermission
	}
	return &oauthhttp.StatusError{Provider: "dropbox", Op: op, Status: se.Status, Kind: kind, Detail: sum}
}

func (p *Provider) rpc(ctx context.Context, op, endpoint string, arg any, out any) error {
	body, _ := oauthhttp.JSONBody(arg)
	u := p.api + endpoint
	resp, err := p.d.Do(ctx, op, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return mapErr(op, err)
	}
	if out == nil {
		oauthhttp.Drain(resp)
		return nil
	}
	return oauthhttp.DecodeJSON(resp, out)
}

type metadata struct {
	Tag      string `json:".tag"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified string `json:"server_modified"`
}

func (m metadata) entry(rel string) cloud.Entry {
	e := cloud.Entry{Name: m.Name, Path: rel, IsDir: m.Tag == "folder", Size: m.Size}
	if t, err := time.Parse(time.RFC3339, m.Modified); err == nil {
		e.ModTime = t
	}
	return e
}

func (p *Provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	dp, err := p.dbPath(dir)
	if err != nil {
		return nil, err
	}
	var out []cloud.Entry
	endpoint, arg := "/files/list_folder", any(map[string]any{"path": dp, "limit": 2000, "include_deleted": false})
	for page := 0; page < maxPages; page++ {
		var res struct {
			Entries []metadata `json:"entries"`
			Cursor  string     `json:"cursor"`
			HasMore bool       `json:"has_more"`
		}
		if err := p.rpc(ctx, "list", endpoint, arg, &res); err != nil {
			return nil, err
		}
		for _, m := range res.Entries {
			if m.Tag != "file" && m.Tag != "folder" || m.Name == "" {
				continue
			}
			rel := m.Name
			if dir != "" {
				rel = dir + "/" + m.Name
			}
			out = append(out, m.entry(rel))
		}
		if !res.HasMore {
			break
		}
		endpoint, arg = "/files/list_folder/continue", map[string]any{"cursor": res.Cursor}
	}
	cloud.SortEntries(out)
	return out, nil
}

func (p *Provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	dp, err := p.dbPath(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	if dp == "" { // get_metadata does not accept the root
		return cloud.Entry{Name: "", Path: rel, IsDir: true}, nil
	}
	var m metadata
	if err := p.rpc(ctx, "stat", "/files/get_metadata", map[string]any{"path": dp}, &m); err != nil {
		return cloud.Entry{}, err
	}
	return m.entry(rel), nil
}

func (p *Provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	dp, err := p.dbPath(rel)
	if err != nil {
		return nil, 0, err
	}
	if dp == "" {
		return nil, 0, cloud.ErrUnsupported
	}
	arg := headerJSON(map[string]any{"path": dp})
	u := p.content + "/files/download"
	resp, err := p.d.Do(ctx, "download", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
		if req != nil {
			req.Header.Set("Dropbox-API-Arg", arg)
		}
		return req, err
	})
	if err != nil {
		return nil, 0, mapErr("download", err)
	}
	return resp.Body, resp.ContentLength, nil
}

func (p *Provider) Remove(ctx context.Context, rel string) error {
	dp, err := p.dbPath(rel)
	if err != nil {
		return err
	}
	if dp == "" {
		return cloud.ErrUnsupported
	}
	e, err := p.Stat(ctx, rel)
	if err != nil {
		return err
	}
	if e.IsDir {
		return fmt.Errorf("dropbox remove: %w", cloud.ErrUnsupported) // only files are ever removed
	}
	return p.rpc(ctx, "remove", "/files/delete_v2", map[string]any{"path": dp}, nil)
}

func commitInfo(dp string) map[string]any {
	return map[string]any{"path": dp, "mode": "add", "autorename": false, "mute": true, "strict_conflict": true}
}

func (p *Provider) sendContent(ctx context.Context, op, endpoint string, arg any, data []byte, out any) error {
	argH := headerJSON(arg)
	u := p.content + endpoint
	resp, err := p.d.Do(ctx, op, func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(data))
		if req != nil {
			req.ContentLength = int64(len(data))
			req.Header.Set("Dropbox-API-Arg", argH)
			req.Header.Set("Content-Type", "application/octet-stream")
		}
		return req, err
	})
	if err != nil {
		return mapErr(op, err)
	}
	if out == nil {
		oauthhttp.Drain(resp)
		return nil
	}
	return oauthhttp.DecodeJSON(resp, out)
}

func (p *Provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	dp, err := p.dbPath(rel)
	if err != nil {
		return err
	}
	if dp == "" || size < 0 {
		return cloud.ErrUnsupported
	}
	if size <= chunkSize {
		var data []byte
		if size > 0 {
			if data, err = oauthhttp.ReadChunk(r, size); err != nil {
				return err
			}
		}
		return p.sendContent(ctx, "upload", "/files/upload", commitInfo(dp), data, nil)
	}
	// Fail fast before transferring a large file; the finish call still enforces mode "add" atomically.
	if _, err := p.Stat(ctx, rel); err == nil {
		return fmt.Errorf("dropbox: %w", cloud.ErrExists)
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	var sess struct {
		ID string `json:"session_id"`
	}
	first, err := oauthhttp.ReadChunk(r, chunkSize)
	if err != nil {
		return err
	}
	if err := p.sendContent(ctx, "upload-start", "/files/upload_session/start", map[string]any{"close": false}, first, &sess); err != nil {
		return err
	}
	if sess.ID == "" {
		return &oauthhttp.StatusError{Provider: "dropbox", Op: "upload-start", Kind: oauthhttp.ErrServer}
	}
	off := int64(len(first))
	for off < size {
		n := min(int64(chunkSize), size-off)
		chunk, err := oauthhttp.ReadChunk(r, n)
		if err != nil {
			return err
		}
		cursor := map[string]any{"session_id": sess.ID, "offset": off}
		if off+n == size {
			return p.sendContent(ctx, "upload-finish", "/files/upload_session/finish", map[string]any{"cursor": cursor, "commit": commitInfo(dp)}, chunk, nil)
		}
		if err := p.sendContent(ctx, "upload-append", "/files/upload_session/append_v2", map[string]any{"cursor": cursor, "close": false}, chunk, nil); err != nil {
			return err
		}
		off += n
	}
	return &oauthhttp.StatusError{Provider: "dropbox", Op: "upload", Kind: oauthhttp.ErrServer} // unreachable: size > chunkSize
}
