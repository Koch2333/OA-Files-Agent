// Package azureblob is the Azure Blob Storage provider (including Azure China, *.blob.core.chinacloudapi.cn).
package azureblob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"

	"oa-agent/backend/internal/cloud"
)

const (
	maxEntries = 5000
	opTimeout  = 30 * time.Second
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "azure_blob",
		Name:        "Azure Blob Storage",
		Group:       "cloud",
		Description: "Microsoft Azure Blob Storage（含世纪互联 Azure 中国）。",
		DocsURL:     "https://learn.microsoft.com/azure/storage/blobs/storage-blobs-introduction",
		Fields: []cloud.Field{
			{Key: "account", Label: "存储账户名", Type: cloud.TText, Required: true, Placeholder: "mystorageaccount", Help: "存储账户名称（3–24 位小写字母和数字）。"},
			{Key: "container", Label: "容器（Container）", Type: cloud.TText, Required: true, Placeholder: "archive"},
			{Key: "endpoint", Label: "服务地址（可选）", Type: cloud.TText, Placeholder: "https://mystorageaccount.blob.core.windows.net",
				Help: "留空 = https://<账户名>.blob.core.windows.net。Azure 中国（世纪互联）填 https://<账户名>.blob.core.chinacloudapi.cn；Azurite 本地模拟器填 http://127.0.0.1:10000/devstoreaccount1。"},
			{Key: "auth", Label: "认证方式", Type: cloud.TSelect, Default: "account_key", Options: []cloud.Option{{Value: "account_key", Label: "账户密钥"}, {Value: "sas", Label: "SAS 令牌"}}},
			{Key: "account_key", Label: "账户密钥", Type: cloud.TPassword, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "account_key"},
				Help: "Azure 门户 → 存储账户 → 安全性和网络 → 访问密钥（Base64 字符串）。"},
			{Key: "sas_token", Label: "SAS 令牌", Type: cloud.TPassword, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "sas"},
				Help: "容器级 SAS，需要“读取、列出”权限；用于归档同步还需要“创建、写入、删除”。可带或不带开头的 ?。"},
		},
		Factory: New,
	})
}

type provider struct {
	cc       *container.Client
	basePath string
	hc       *http.Client
}

func validation(msg string) error { return &cloud.ValidationError{Code: "INVALID_VALUE", Msg: msg} }

// New builds the client (no network traffic).
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	account, cont := s.Get("account"), s.Get("container")
	if account == "" || cont == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写存储账户名和容器"}
	}
	if strings.ContainsAny(account+cont, "/\\?#@: ") {
		return nil, validation("存储账户名或容器名无效")
	}
	ep := strings.TrimRight(s.Get("endpoint"), "/")
	if ep == "" {
		ep = "https://" + account + ".blob.core.windows.net"
	}
	u, err := url.Parse(ep)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, validation("服务地址无效")
	}
	if err := cloud.CheckHost(u.Hostname()); err != nil {
		return nil, err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/" + cont
	u.RawPath = ""

	hc := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opTimeout,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
	}}
	opts := &container.ClientOptions{ClientOptions: azcore.ClientOptions{
		Transport: hc,
		Retry:     policy.RetryOptions{MaxRetries: 2, RetryDelay: 200 * time.Millisecond, MaxRetryDelay: 2 * time.Second, TryTimeout: 5 * time.Minute},
	}}
	var cc *container.Client
	switch s.Get("auth") {
	case "sas":
		sas := strings.TrimPrefix(strings.TrimSpace(s.Secrets["sas_token"]), "?")
		if sas == "" {
			return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“SAS 令牌”"}
		}
		cc, err = container.NewClientWithNoCredential(u.String()+"?"+sas, opts)
	default:
		cred, cerr := azblob.NewSharedKeyCredential(account, s.Secrets["account_key"])
		if cerr != nil {
			return nil, validation("账户密钥格式无效（应为 Base64）")
		}
		cc, err = container.NewClientWithSharedKeyCredential(u.String(), cred, opts)
	}
	if err != nil {
		return nil, validation("服务地址无效")
	}
	return &provider{cc: cc, basePath: s.BasePath, hc: hc}, nil
}

func (p *provider) Close() error { p.hc.CloseIdleConnections(); return nil }

// mapErr converts SDK errors to cloud sentinels without leaking response bodies.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	for _, s := range []error{cloud.ErrNotFound, cloud.ErrExists, cloud.ErrAuth, cloud.ErrPermission, cloud.ErrUnreachable} {
		if errors.Is(err, s) {
			return err
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var re *azcore.ResponseError
	if errors.As(err, &re) {
		switch re.ErrorCode {
		case "AuthenticationFailed", "InvalidAuthenticationInfo", "InvalidHeaderValue_auth":
			return fmt.Errorf("%w", cloud.ErrAuth)
		case "AuthorizationFailure", "AuthorizationPermissionMismatch", "AuthorizationProtocolMismatch", "AuthorizationResourceTypeMismatch", "AuthorizationServiceMismatch", "AccountIsDisabled", "InsufficientAccountPermissions":
			return fmt.Errorf("%w", cloud.ErrPermission)
		case "BlobNotFound", "ContainerNotFound", "ResourceNotFound", "ParentNotFound":
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case "BlobAlreadyExists", "ConditionNotMet", "ResourceAlreadyExists":
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		switch re.StatusCode {
		case 401:
			return fmt.Errorf("%w", cloud.ErrAuth)
		case 403:
			return fmt.Errorf("%w", cloud.ErrPermission)
		case 404:
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case 412:
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		return fmt.Errorf("azure: server error %d %s", re.StatusCode, re.ErrorCode)
	}
	var ne net.Error
	var oe *net.OpError
	if errors.As(err, &ne) || errors.As(err, &oe) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	msg := err.Error()
	if strings.Contains(msg, "dial") || strings.Contains(msg, "connection") || strings.Contains(msg, "no such host") {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return errors.New("azure: request failed")
}

func (p *provider) key(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	k := cloud.Join(p.basePath, rel)
	if k != "" {
		for _, c := range strings.Split(k, "/") {
			if c == ".." || c == "." {
				return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
			}
		}
	}
	return k, nil
}

func prefixOf(k string) string {
	if k == "" {
		return ""
	}
	return k + "/"
}

func (p *provider) relOf(key string) string {
	if p.basePath != "" {
		key = strings.TrimPrefix(key, p.basePath+"/")
	}
	return key
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	k, err := p.key(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	prefix := prefixOf(k)
	delim := "/"
	pager := p.cc.NewListBlobsHierarchyPager(delim, &container.ListBlobsHierarchyOptions{Prefix: &prefix, MaxResults: int32Ptr(1000)})
	out := []cloud.Entry{}
	seen := false
	dup := map[string]bool{}
	add := func(e cloud.Entry) {
		if !dup[e.Path] {
			dup[e.Path] = true
			out = append(out, e)
		}
	}
	for pager.More() && len(out) < maxEntries {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, mapErr(err)
		}
		for _, bp := range page.Segment.BlobPrefixes {
			if bp == nil || bp.Name == nil {
				continue
			}
			seen = true
			rel := p.relOf(strings.TrimSuffix(*bp.Name, "/"))
			if rel != "" {
				add(cloud.Entry{Name: path.Base(rel), Path: rel, IsDir: true})
			}
		}
		for _, b := range page.Segment.BlobItems {
			if b == nil || b.Name == nil {
				continue
			}
			seen = true
			if *b.Name == prefix { // zero-byte "dir/" marker
				continue
			}
			rel := p.relOf(*b.Name)
			e := cloud.Entry{Name: path.Base(rel), Path: rel}
			if b.Properties != nil {
				if b.Properties.ContentLength != nil {
					e.Size = *b.Properties.ContentLength
				}
				if b.Properties.LastModified != nil {
					e.ModTime = *b.Properties.LastModified
				}
			}
			add(e)
		}
	}
	if len(out) > maxEntries {
		out = out[:maxEntries]
	}
	if !seen && dir != "" {
		return nil, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	return out, nil
}

func int32Ptr(i int32) *int32 { return &i }

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	k, err := p.key(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if k != "" {
		props, err := p.cc.NewBlobClient(k).GetProperties(ctx, nil)
		if err == nil {
			e := cloud.Entry{Name: path.Base(rel), Path: rel}
			if props.ContentLength != nil {
				e.Size = *props.ContentLength
			}
			if props.LastModified != nil {
				e.ModTime = *props.LastModified
			}
			return e, nil
		}
		if me := mapErr(err); !errors.Is(me, cloud.ErrNotFound) {
			return cloud.Entry{}, me
		}
	}
	prefix := prefixOf(k)
	pager := p.cc.NewListBlobsFlatPager(&container.ListBlobsFlatOptions{Prefix: &prefix, MaxResults: int32Ptr(1)})
	if pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return cloud.Entry{}, mapErr(err)
		}
		if len(page.Segment.BlobItems) > 0 {
			name := ""
			if rel != "" {
				name = path.Base(rel)
			}
			return cloud.Entry{Name: name, Path: rel, IsDir: true}, nil
		}
	}
	return cloud.Entry{}, fmt.Errorf("%w", cloud.ErrNotFound)
}

type body struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = mapErr(err)
	}
	return n, err
}

func (b body) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	k, err := p.key(rel)
	if err != nil {
		return nil, 0, err
	}
	if k == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	octx, cancel := context.WithCancel(ctx)
	resp, err := p.cc.NewBlobClient(k).DownloadStream(octx, nil)
	if err != nil {
		cancel()
		return nil, 0, mapErr(err)
	}
	size := int64(-1)
	if resp.ContentLength != nil {
		size = *resp.ContentLength
	}
	return body{ReadCloser: resp.Body, cancel: cancel}, size, nil
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

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	k, err := p.key(rel)
	if err != nil {
		return err
	}
	if k == "" || size < 0 {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	// Pre-check for servers/emulators that ignore the conditional header.
	if _, err := p.Stat(ctx, rel); err == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	etag := azcore.ETagAny
	cr := &countReader{r: io.LimitReader(r, size)}
	bb := p.cc.NewBlockBlobClient(k)
	_, err = bb.UploadStream(ctx, cr, &blockblob.UploadStreamOptions{
		BlockSize:   4 << 20,
		Concurrency: 2,
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: &etag},
		},
	})
	if err != nil {
		return mapErr(err)
	}
	var one [1]byte
	if cr.n != size {
		p.cleanup(ctx, k)
		return errors.New("azure: size mismatch")
	}
	if n, _ := r.Read(one[:]); n > 0 {
		p.cleanup(ctx, k)
		return errors.New("azure: source longer than declared size")
	}
	return nil
}

func (p *provider) cleanup(ctx context.Context, k string) {
	p.cc.NewBlobClient(k).Delete(ctx, nil)
}

func (p *provider) Remove(ctx context.Context, rel string) error {
	k, err := p.key(rel)
	if err != nil {
		return err
	}
	if k == "" {
		return fmt.Errorf("%w", cloud.ErrPermission)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	_, err = p.cc.NewBlobClient(k).Delete(ctx, nil)
	return mapErr(err)
}
