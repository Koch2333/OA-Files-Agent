// Adapted from OpenList (AGPL-3.0) drivers/google_drive
//
// Package googledrive is the Google Drive (API v3) storage provider. Drive addresses files by ID, so paths are
// resolved name by name below the configured root folder. Only list, stat, download, upload (never overwriting)
// and single-file delete are implemented.
//
// API facts (Drive API v3 docs):
//   - token: POST https://oauth2.googleapis.com/token (grant_type=refresh_token)
//   - list/lookup: GET /drive/v3/files?q='<id>' in parents and trashed=false [and name='x']&supportsAllDrives&includeItemsFromAllDrives
//   - download: GET /drive/v3/files/{id}?alt=media (Google-native documents cannot be downloaded this way)
//   - folders: POST /drive/v3/files {name, mimeType: application/vnd.google-apps.folder, parents}
//   - upload: resumable session, POST /upload/drive/v3/files?uploadType=resumable (Location header), then PUT chunks
//     with Content-Range (multiples of 256 KiB); 308 = more expected, 200/201 = finished
//   - delete: DELETE /drive/v3/files/{id} (permanent, no trash)
//
// RACE (documented, not avoidable): Drive allows several items with the same name in one folder and has no
// "create only if absent" option. PutNew therefore checks for the name and then creates the file; two writers racing
// on the same name can both succeed, producing duplicates. Nothing is ever overwritten or modified in that case.
// Duplicate names are resolved deterministically: the oldest item (createdTime, then id) wins, and List hides the
// younger duplicates so every listed path resolves to the listed item. Parent folders are created by name the same
// way. Native Google Docs/Sheets/Slides are not listed and cannot be opened (ErrUnsupported).
package googledrive

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/cloud/oauthhttp"
)

const (
	defaultAPI    = "https://www.googleapis.com/drive/v3"
	defaultUpload = "https://www.googleapis.com/upload/drive/v3"
	defaultToken  = "https://oauth2.googleapis.com/token"
	folderMime    = "application/vnd.google-apps.folder"
	appsPrefix    = "application/vnd.google-apps."
	chunkSize     = 32 * 256 << 10 // 8 MiB, a multiple of 256 KiB
	maxPages      = 500
	fileFields    = "id,name,mimeType,size,modifiedTime,createdTime"
)

var idRe = regexp.MustCompile(`^[A-Za-z0-9_\-]{1,200}$`)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "googledrive",
		Name:        "Google Drive",
		Group:       "cloud",
		Description: "Google Drive（含共享云端硬盘）。使用 OAuth 刷新令牌访问，不会覆盖已有文件；Google 文档/表格等在线文档不会列出。",
		DocsURL:     "https://developers.google.com/drive/api/guides/about-sdk",
		Fields: []cloud.Field{
			{Key: "client_id", Label: "客户端 ID", Type: cloud.TText, Required: true,
				Help: "Google Cloud 控制台 → API 和服务 → 凭据 中创建的 OAuth 客户端 ID，并启用 Google Drive API。"},
			{Key: "client_secret", Label: "客户端密钥", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "同一 OAuth 客户端的密钥，加密保存。"},
			{Key: "refresh_token", Label: "Refresh Token", Type: cloud.TPassword, Required: true, Secret: true,
				Help: "授权时 scope 需包含 https://www.googleapis.com/auth/drive（仅读取可用 drive.readonly），并以 access_type=offline 取得。"},
			{Key: "root_folder_id", Label: "根文件夹 ID", Type: cloud.TText, Default: "root", Placeholder: "root",
				Help: "留空或 root 表示“我的云端硬盘”；也可填某个文件夹的 ID（地址栏 folders/ 之后的部分）。基础路径在其下解析。"},
			{Key: "drive_id", Label: "共享云端硬盘 ID（可选）", Type: cloud.TText,
				Help: "访问共享云端硬盘时填写其 ID；此时根文件夹 ID 留空即表示该共享盘的根目录。"},
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

type node struct {
	id, name, mime string
	size           int64
	mod, created   time.Time
}

func (n node) dir() bool { return n.mime == folderMime }

// Provider implements cloud.Provider for Google Drive.
type Provider struct {
	api, upload string
	rootID      string
	driveID     string
	base        string
	hc          *http.Client
	d, anon     *oauthhttp.Doer
	tokens      *oauthhttp.TokenSource
	insecure    bool // test endpoint over http

	mu   sync.Mutex
	dirs map[string]string // full folder path (under the root) -> folder ID
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
	root, driveID := s.Get("root_folder_id"), s.Get("drive_id")
	if driveID != "" && !idRe.MatchString(driveID) {
		return nil, errors.New("共享云端硬盘 ID 格式无效")
	}
	if root == "" {
		root = "root"
		if driveID != "" {
			root = driveID
		}
	}
	if !idRe.MatchString(root) {
		return nil, errors.New("根文件夹 ID 格式无效")
	}
	api, up, tok := defaultAPI, defaultUpload, defaultToken
	insecure := false
	if ep := oauthhttp.Endpoint("", s.Get("endpoint")); ep != "" { // test hook, loopback/private hosts only
		api, up, tok = ep+"/drive/v3", ep+"/upload/drive/v3", ep+"/token"
		insecure = strings.HasPrefix(ep, "http://")
	}
	hc := oauthhttp.NewClient()
	oa := oauthhttp.NewOAuth(&oauthhttp.OAuth{Name: "googledrive", HC: hc, TokenURL: tok, ClientID: clientID, ClientSecret: secret, Persist: s.Persist}, refresh)
	p := &Provider{api: api, upload: up, rootID: root, driveID: driveID, base: base, hc: hc, insecure: insecure, dirs: map[string]string{}}
	p.tokens = &oauthhttp.TokenSource{Refresh: oa.Refresh}
	p.d = &oauthhttp.Doer{Name: "googledrive", HC: hc, Token: p.tokens.Get, ShouldRetry403: func(detail string) bool {
		return strings.Contains(detail, "rateLimitExceeded") || strings.Contains(detail, "RateLimitExceeded") || strings.Contains(detail, "backendError")
	}}
	p.anon = p.d.Anon()
	return p, nil
}

func (p *Provider) Close() error { p.hc.CloseIdleConnections(); return nil }

func qstr(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

type fileJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     string `json:"size"`
	Modified string `json:"modifiedTime"`
	Created  string `json:"createdTime"`
}

func (f fileJSON) node() node {
	n := node{id: f.ID, name: f.Name, mime: f.MimeType}
	n.size, _ = strconv.ParseInt(f.Size, 10, 64)
	n.mod, _ = time.Parse(time.RFC3339, f.Modified)
	n.created, _ = time.Parse(time.RFC3339, f.Created)
	return n
}

func (p *Provider) getJSON(ctx context.Context, op, u string, v any) error {
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

// children returns the non-trashed children of parent (optionally only those called name), oldest first.
func (p *Provider) children(ctx context.Context, parent, name string) ([]node, error) {
	q := qstr(parent) + " in parents and trashed=false"
	if name != "" {
		q += " and name=" + qstr(name)
	}
	var out []node
	token := ""
	for page := 0; page < maxPages; page++ {
		v := url.Values{
			"q": {q}, "fields": {"nextPageToken,files(" + fileFields + ")"}, "pageSize": {"1000"},
			"supportsAllDrives": {"true"}, "includeItemsFromAllDrives": {"true"}, "orderBy": {"createdTime,name"},
		}
		if p.driveID != "" {
			v.Set("corpora", "drive")
			v.Set("driveId", p.driveID)
		}
		if token != "" {
			v.Set("pageToken", token)
		}
		var res struct {
			Files []fileJSON `json:"files"`
			Next  string     `json:"nextPageToken"`
		}
		if err := p.getJSON(ctx, "list", p.api+"/files?"+v.Encode(), &res); err != nil {
			return nil, err
		}
		for _, f := range res.Files {
			out = append(out, f.node())
		}
		if res.Next == "" {
			break
		}
		token = res.Next
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].created.Equal(out[j].created) {
			return out[i].created.Before(out[j].created)
		}
		return out[i].id < out[j].id
	})
	return out, nil
}

// step finds the child called name: when wantDir is true only folders count (traversal).
func (p *Provider) step(ctx context.Context, parent, name string, wantDir bool) (node, error) {
	ns, err := p.children(ctx, parent, name)
	if err != nil {
		return node{}, err
	}
	for _, n := range ns {
		if n.name == name && (!wantDir || n.dir()) {
			return n, nil
		}
	}
	return node{}, fmt.Errorf("googledrive: %w", cloud.ErrNotFound)
}

func split(full string) []string {
	if full == "" {
		return nil
	}
	return strings.Split(full, "/")
}

// folderID resolves a folder path below the root, optionally creating missing folders.
func (p *Provider) folderID(ctx context.Context, full string, create bool) (string, error) {
	id := p.rootID
	parts := split(full)
	for i := range parts {
		cur := strings.Join(parts[:i+1], "/")
		p.mu.Lock()
		cached, ok := p.dirs[cur]
		p.mu.Unlock()
		if ok {
			id = cached
			continue
		}
		n, err := p.step(ctx, id, parts[i], true)
		if errors.Is(err, cloud.ErrNotFound) && create {
			// A file with that name (not a folder) must not be shadowed silently.
			if any, aerr := p.step(ctx, id, parts[i], false); aerr == nil && !any.dir() {
				return "", fmt.Errorf("googledrive: %w", cloud.ErrExists)
			}
			n, err = p.mkdir(ctx, id, parts[i])
		}
		if err != nil {
			return "", err
		}
		id = n.id
		p.mu.Lock()
		p.dirs[cur] = id
		p.mu.Unlock()
	}
	return id, nil
}

func (p *Provider) mkdir(ctx context.Context, parent, name string) (node, error) {
	body, _ := oauthhttp.JSONBody(map[string]any{"name": name, "mimeType": folderMime, "parents": []string{parent}})
	u := p.api + "/files?supportsAllDrives=true&fields=" + url.QueryEscape(fileFields)
	resp, err := p.d.Do(ctx, "mkdir", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		return req, err
	})
	if err != nil {
		return node{}, err
	}
	var f fileJSON
	if err := oauthhttp.DecodeJSON(resp, &f); err != nil {
		return node{}, err
	}
	if f.ID == "" {
		return node{}, &oauthhttp.StatusError{Provider: "googledrive", Op: "mkdir", Kind: oauthhttp.ErrServer}
	}
	return f.node(), nil
}

// resolve finds the item at rel (a path relative to the base path). "" is the base folder itself.
func (p *Provider) resolve(ctx context.Context, rel string) (node, error) {
	full, err := oauthhttp.SafePath(p.base, rel)
	if err != nil {
		return node{}, err
	}
	if full == "" {
		return node{id: p.rootID, name: "", mime: folderMime}, nil
	}
	parts := split(full)
	parent, err := p.folderID(ctx, strings.Join(parts[:len(parts)-1], "/"), false)
	if err != nil {
		return node{}, err
	}
	return p.step(ctx, parent, parts[len(parts)-1], false)
}

func entryOf(n node, rel string) cloud.Entry {
	return cloud.Entry{Name: n.name, Path: rel, IsDir: n.dir(), Size: n.size, ModTime: n.mod}
}

func native(n node) bool { return !n.dir() && strings.HasPrefix(n.mime, appsPrefix) }

func (p *Provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	d, err := p.resolve(ctx, dir)
	if err != nil {
		return nil, err
	}
	if !d.dir() {
		return nil, fmt.Errorf("googledrive: %w", cloud.ErrNotFound)
	}
	ns, err := p.children(ctx, d.id, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []cloud.Entry
	for _, n := range ns { // oldest first: the first of duplicates is what resolve() picks
		if n.name == "" || seen[n.name] {
			continue
		}
		seen[n.name] = true
		if native(n) || strings.Contains(n.name, "/") {
			continue
		}
		rel := n.name
		if dir != "" {
			rel = dir + "/" + n.name
		}
		out = append(out, entryOf(n, rel))
	}
	cloud.SortEntries(out)
	return out, nil
}

func (p *Provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	n, err := p.resolve(ctx, rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	if n.name == "" {
		n.name = rel
	}
	return entryOf(n, rel), nil
}

func (p *Provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	n, err := p.resolve(ctx, rel)
	if err != nil {
		return nil, 0, err
	}
	if n.dir() || native(n) {
		return nil, 0, fmt.Errorf("googledrive open: %w", cloud.ErrUnsupported)
	}
	u := p.api + "/files/" + url.PathEscape(n.id) + "?alt=media&supportsAllDrives=true"
	resp, err := p.d.Do(ctx, "download", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	})
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

func (p *Provider) Remove(ctx context.Context, rel string) error {
	if rel == "" {
		return cloud.ErrUnsupported
	}
	n, err := p.resolve(ctx, rel)
	if err != nil {
		return err
	}
	if n.dir() {
		return fmt.Errorf("googledrive remove: %w", cloud.ErrUnsupported) // only files are ever removed
	}
	u := p.api + "/files/" + url.PathEscape(n.id) + "?supportsAllDrives=true"
	resp, err := p.d.Do(ctx, "remove", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	})
	if err != nil {
		return err
	}
	oauthhttp.Drain(resp)
	return nil
}

func (p *Provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	full, err := oauthhttp.SafePath(p.base, rel)
	if err != nil {
		return err
	}
	if rel == "" || size < 0 {
		return cloud.ErrUnsupported
	}
	parts := split(full)
	name := parts[len(parts)-1]
	parent, err := p.folderID(ctx, strings.Join(parts[:len(parts)-1], "/"), true)
	if err != nil {
		return err
	}
	// Check-then-create: see the RACE note in the package comment.
	if ns, err := p.children(ctx, parent, name); err != nil {
		return err
	} else if len(ns) > 0 {
		return fmt.Errorf("googledrive: %w", cloud.ErrExists)
	}
	return p.putResumable(ctx, parent, name, r, size)
}

func (p *Provider) putResumable(ctx context.Context, parent, name string, r io.Reader, size int64) error {
	meta, _ := oauthhttp.JSONBody(map[string]any{"name": name, "parents": []string{parent}})
	u := p.upload + "/files?uploadType=resumable&supportsAllDrives=true&fields=id"
	resp, err := p.d.Do(ctx, "upload-session", func(ctx context.Context) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, oauthhttp.BytesReader(meta))
		if req != nil {
			req.Header.Set("Content-Type", "application/json; charset=UTF-8")
			req.Header.Set("X-Upload-Content-Type", "application/octet-stream")
			req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(size, 10))
		}
		return req, err
	})
	if err != nil {
		return err
	}
	loc := resp.Header.Get("Location")
	oauthhttp.Drain(resp)
	lu, perr := url.Parse(loc)
	if perr != nil || lu.Host == "" || (lu.Scheme != "https" && !(lu.Scheme == "http" && p.insecure)) {
		return &oauthhttp.StatusError{Provider: "googledrive", Op: "upload-session", Kind: oauthhttp.ErrServer}
	}
	done := false
	defer func() {
		if !done {
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			defer cancel()
			if cr, cerr := p.anon.Do(cctx, "upload-cancel", func(c context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(c, http.MethodDelete, loc, nil)
			}); cerr == nil {
				oauthhttp.Drain(cr)
			}
		}
	}()
	if size == 0 {
		cr, err := p.anon.Do(ctx, "upload", func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, loc, nil)
			if req != nil {
				req.Header.Set("Content-Range", "bytes */0")
			}
			return req, err
		})
		if err != nil {
			return err
		}
		oauthhttp.Drain(cr)
		done = true
		return nil
	}
	for off := int64(0); off < size; {
		n := min(int64(chunkSize), size-off)
		chunk, err := oauthhttp.ReadChunk(r, n)
		if err != nil {
			return err
		}
		rng := fmt.Sprintf("bytes %d-%d/%d", off, off+n-1, size)
		cr, err := p.anon.Do(ctx, "upload", func(ctx context.Context) (*http.Request, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, loc, oauthhttp.BytesReader(chunk))
			if req != nil {
				req.ContentLength = n
				req.Header.Set("Content-Range", rng)
			}
			return req, err
		})
		if err != nil {
			return err
		}
		code := cr.StatusCode
		oauthhttp.Drain(cr)
		off += n
		if off >= size && code == http.StatusPermanentRedirect { // 308: the service did not accept the last chunk
			return &oauthhttp.StatusError{Provider: "googledrive", Op: "upload", Status: code, Kind: oauthhttp.ErrServer}
		}
	}
	done = true
	return nil
}
