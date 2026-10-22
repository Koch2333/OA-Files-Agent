// Package s3 is the S3-compatible storage provider (AWS S3, Alibaba OSS, Tencent COS, Huawei OBS, MinIO,
// Cloudflare R2, GCS interoperability, ...), built on minio-go.
package s3

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"path"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"oa-agent/backend/internal/cloud"
	"oa-agent/backend/internal/diag"
)

const (
	maxEntries   = 5000
	opTimeout    = 30 * time.Second
	singlePutMax = 64 << 20
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "s3",
		Name:        "S3 兼容对象存储",
		Group:       "cloud",
		Description: "AWS S3、阿里云 OSS、腾讯云 COS、华为云 OBS、MinIO、Cloudflare R2、Google Cloud Storage（HMAC 密钥）等兼容 S3 协议的存储。",
		DocsURL:     "https://docs.aws.amazon.com/AmazonS3/latest/userguide/Welcome.html",
		Fields: []cloud.Field{
			{Key: "endpoint", Label: "服务地址（Endpoint）", Type: cloud.TText, Placeholder: "oss-cn-hangzhou.aliyuncs.com",
				Help: "留空 = AWS S3（s3.amazonaws.com）。不含 http(s):// 前缀，可带端口。常见值：阿里云 OSS oss-cn-hangzhou.aliyuncs.com；腾讯云 COS cos.ap-guangzhou.myqcloud.com；华为云 OBS obs.cn-north-4.myhuaweicloud.com；MinIO 192.168.1.10:9000；Cloudflare R2 <账户ID>.r2.cloudflarestorage.com；Google Cloud Storage storage.googleapis.com（需创建 HMAC 密钥）。"},
			{Key: "region", Label: "区域（Region）", Type: cloud.TText, Placeholder: "cn-hangzhou",
				Help: "AWS 例如 us-east-1、cn-north-1；阿里云 OSS 例如 cn-hangzhou；腾讯云 COS 例如 ap-guangzhou；华为云 OBS 例如 cn-north-4；Cloudflare R2 填 auto；MinIO 通常留空。留空时自动探测或使用 us-east-1。"},
			{Key: "bucket", Label: "存储桶（Bucket）", Type: cloud.TText, Required: true, Placeholder: "my-bucket",
				Help: "存储桶名称。腾讯云 COS 的名称含 AppID，例如 examplebucket-1250000000。"},
			{Key: "use_ssl", Label: "使用 HTTPS", Type: cloud.TSelect, Default: "true", Options: []cloud.Option{{Value: "true", Label: "是（推荐）"}, {Value: "false", Label: "否（HTTP，仅限内网 MinIO 等）"}}},
			{Key: "path_style", Label: "寻址风格", Type: cloud.TSelect, Default: "auto", Options: []cloud.Option{
				{Value: "auto", Label: "自动"}, {Value: "true", Label: "路径风格（host/bucket）"}, {Value: "false", Label: "虚拟主机风格（bucket.host）"}},
				Help: "自动：服务地址为 IP、带端口或 localhost（MinIO 等）时用路径风格，否则（OSS/COS/OBS/AWS 等）用虚拟主机风格。"},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}},
				Help: "仅在内网自签名证书时开启。"},
			{Key: "access_key", Label: "Access Key", Type: cloud.TPassword, Secret: true, Required: true, Help: "AWS Access Key ID / 阿里云 AccessKey ID / 腾讯云 SecretId 等。建议使用只授予该桶所需权限的子账号密钥。"},
			{Key: "secret_key", Label: "Secret Key", Type: cloud.TPassword, Secret: true, Required: true},
			{Key: "session_token", Label: "Session Token（可选）", Type: cloud.TPassword, Secret: true, Help: "仅使用临时凭证（STS）时填写。"},
		},
		Factory: New,
	})
}

type provider struct {
	cli      *minio.Client
	bucket   string
	basePath string
	tr       *http.Transport
}

// splitEndpoint normalises the user's endpoint: strips an accidental scheme (which then decides use_ssl when
// use_ssl is not set explicitly), trailing slashes, and returns the host[:port].
func splitEndpoint(raw string, useSSL bool, sslSet bool) (string, bool, error) {
	ep := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(ep, "https://"):
		ep = strings.TrimPrefix(ep, "https://")
		if !sslSet {
			useSSL = true
		}
	case strings.HasPrefix(ep, "http://"):
		ep = strings.TrimPrefix(ep, "http://")
		if !sslSet {
			useSSL = false
		}
	}
	ep = strings.TrimRight(ep, "/")
	if ep == "" {
		return "s3.amazonaws.com", useSSL, nil
	}
	host := ep
	if h, _, err := net.SplitHostPort(ep); err == nil {
		host = h
	}
	if err := cloud.CheckHost(host); err != nil {
		return "", false, err
	}
	return ep, useSSL, nil
}

// usePathStyle decides the addressing style. mode is "auto", "true" or "false".
func usePathStyle(endpoint, mode string) bool {
	switch strings.ToLower(mode) {
	case "true":
		return true
	case "false":
		return false
	}
	if _, _, err := net.SplitHostPort(endpoint); err == nil {
		return true // explicit port
	}
	host := strings.Trim(endpoint, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	_, err := netip.ParseAddr(host)
	return err == nil
}

// New builds the client (no network traffic).
func New(_ context.Context, s cloud.Settings) (cloud.Provider, error) {
	_, sslSet := s.Values["use_ssl"]
	ep, useSSL, err := splitEndpoint(s.Get("endpoint"), s.Bool("use_ssl", true), sslSet && s.Get("use_ssl") != "")
	if err != nil {
		return nil, err
	}
	bucket := s.Get("bucket")
	if bucket == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“存储桶（Bucket）”"}
	}
	region := s.Get("region")
	pathStyle := usePathStyle(ep, s.Get("path_style"))
	if region == "" && (pathStyle || ep != "s3.amazonaws.com") {
		region = "us-east-1" // avoids a bucket-location lookup that many S3 clones do not implement
	}
	lookup := minio.BucketLookupDNS
	if pathStyle {
		lookup = minio.BucketLookupPath
	}
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opTimeout,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.Bool("insecure_skip_verify", false)},
	}
	cli, err := minio.New(ep, &minio.Options{
		Creds:        credentials.NewStaticV4(s.Secrets["access_key"], s.Secrets["secret_key"], s.Secrets["session_token"]),
		Secure:       useSSL,
		Region:       region,
		BucketLookup: lookup,
		Transport:    tr,
		MaxRetries:   3,
	})
	if err != nil {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "服务地址无效"}
	}
	return &provider{cli: cli, bucket: bucket, basePath: s.BasePath, tr: tr}, nil
}

func (p *provider) Close() error { p.tr.CloseIdleConnections(); return nil }

// mapErr turns a minio/network error into one of the cloud sentinels without leaking the raw message.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, cloud.ErrNotFound) || errors.Is(err, cloud.ErrExists) || errors.Is(err, cloud.ErrAuth) || errors.Is(err, cloud.ErrPermission) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	resp := minio.ToErrorResponse(err)
	if resp.Code != "" || resp.StatusCode != 0 {
		switch resp.Code {
		case "NoSuchKey", "NoSuchBucket", "NotFound", "NoSuchVersion":
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case "AccessDenied", "AllAccessDisabled", "AccountProblem", "Forbidden":
			return fmt.Errorf("%w", cloud.ErrPermission)
		case "InvalidAccessKeyId", "SignatureDoesNotMatch", "InvalidToken", "ExpiredToken", "InvalidSecurity", "AuthorizationHeaderMalformed", "Unauthorized":
			return fmt.Errorf("%w", cloud.ErrAuth)
		case "PreconditionFailed", "ConditionalRequestConflict", "BucketAlreadyOwnedByYou":
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return fmt.Errorf("%w", cloud.ErrAuth)
		case http.StatusForbidden:
			return fmt.Errorf("%w", cloud.ErrPermission)
		case http.StatusNotFound:
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case http.StatusPreconditionFailed:
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		if resp.StatusCode >= 500 || resp.Code != "" {
			return fmt.Errorf("s3: server error %d %s", resp.StatusCode, resp.Code)
		}
	}
	var ne net.Error
	if errors.Is(err, cloud.ErrUnreachable) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	var oe *net.OpError
	if errors.As(err, &oe) {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	if strings.Contains(err.Error(), "dial") || strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "no such host") {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return errors.New("s3: request failed")
}

// key maps a provider-relative path to the object key (no leading/trailing slash).
func (p *provider) key(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	k := cloud.Join(p.basePath, rel)
	for _, c := range strings.Split(k, "/") {
		if k != "" && (c == ".." || c == ".") {
			return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
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

// relOf converts an object key under the base path to a provider-relative path.
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
	out := []cloud.Entry{}
	seen := false
	dup := map[string]bool{}
	for oi := range p.cli.ListObjects(ctx, p.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: false, MaxKeys: 1000}) {
		if oi.Err != nil {
			return nil, mapErr(oi.Err)
		}
		seen = true
		if oi.Key == prefix { // the zero-byte "dir/" marker
			continue
		}
		isDir := strings.HasSuffix(oi.Key, "/")
		rel := p.relOf(strings.TrimSuffix(oi.Key, "/"))
		if rel == "" {
			continue
		}
		e := cloud.Entry{Name: path.Base(rel), Path: rel, IsDir: isDir, ModTime: oi.LastModified}
		if !isDir {
			e.Size = oi.Size
		}
		if dup[e.Path] {
			continue // some servers report a "dir/" marker both as a prefix and as an object
		}
		dup[e.Path] = true
		out = append(out, e)
		if len(out) >= maxEntries {
			break
		}
	}
	if !seen && dir != "" {
		return nil, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	return out, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	k, err := p.key(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	var permErr error
	if k != "" {
		oi, err := p.cli.StatObject(ctx, p.bucket, k, minio.StatObjectOptions{})
		if err == nil {
			return cloud.Entry{Name: path.Base(rel), Path: rel, Size: oi.Size, ModTime: oi.LastModified}, nil
		}
		me := mapErr(err)
		if errors.Is(me, cloud.ErrPermission) {
			// HEAD responses carry no error body, so a 403 is ambiguous (bad key or missing permission);
			// the listing below returns a body-bearing error that tells the two apart.
			permErr = me
		} else if !errors.Is(me, cloud.ErrNotFound) {
			return cloud.Entry{}, me
		}
	}
	// a "folder" exists when any object has the prefix
	ictx, icancel := context.WithCancel(ctx)
	defer icancel()
	for oi := range p.cli.ListObjects(ictx, p.bucket, minio.ListObjectsOptions{Prefix: prefixOf(k), Recursive: true, MaxKeys: 1}) {
		if oi.Err != nil {
			return cloud.Entry{}, mapErr(oi.Err)
		}
		name := ""
		if rel != "" {
			name = path.Base(rel)
		}
		return cloud.Entry{Name: name, Path: rel, IsDir: true}, nil
	}
	if permErr != nil {
		return cloud.Entry{}, permErr
	}
	return cloud.Entry{}, fmt.Errorf("%w", cloud.ErrNotFound)
}

type object struct {
	*minio.Object
	cancel context.CancelFunc
}

func (o *object) Read(b []byte) (int, error) {
	n, err := o.Object.Read(b)
	if err != nil && err != io.EOF {
		err = mapErr(err)
	}
	return n, err
}

func (o *object) Close() error { err := o.Object.Close(); o.cancel(); return err }

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	k, err := p.key(rel)
	if err != nil {
		return nil, 0, err
	}
	if k == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	octx, cancel := context.WithCancel(ctx)
	obj, err := p.cli.GetObject(octx, p.bucket, k, minio.GetObjectOptions{})
	if err != nil {
		cancel()
		return nil, 0, mapErr(err)
	}
	st, err := withTimeout(ctx, func() (minio.ObjectInfo, error) { return obj.Stat() })
	if err != nil {
		obj.Close()
		cancel()
		return nil, 0, mapErr(err)
	}
	return &object{Object: obj, cancel: cancel}, st.Size, nil
}

func withTimeout[T any](ctx context.Context, f func() (T, error)) (T, error) {
	type res struct {
		v   T
		err error
	}
	ch := make(chan res, 1)
	go func() {
		var v T
		var err error
		if id := diag.Safe("storage.cloud_op", func() { v, err = f() }); id != "" {
			var z T
			v, err = z, errors.New("internal error in storage provider (report "+diag.ShortID(id)+")")
		}
		ch <- res{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(opTimeout):
		var z T
		return z, context.DeadlineExceeded
	case <-ctx.Done():
		var z T
		return z, ctx.Err()
	}
}

type checkedReader struct {
	r io.Reader
	n int64
}

func (c *checkedReader) Read(b []byte) (int, error) {
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
	// Pre-check for servers that ignore the conditional header.
	if _, err := p.Stat(ctx, rel); err == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(err, cloud.ErrNotFound) {
		return err
	}
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: size <= singlePutMax}
	opts.SetMatchETagExcept("*")
	cr := &checkedReader{r: io.LimitReader(r, size)}
	tctx := ctx
	if size <= singlePutMax {
		var cancel context.CancelFunc
		tctx, cancel = context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
	}
	info, err := p.cli.PutObject(tctx, p.bucket, k, cr, size, opts)
	if err != nil {
		return mapErr(err)
	}
	if info.Size != size || cr.n != size {
		// the source was shorter than declared; we created a truncated object, so remove it
		p.cli.RemoveObject(ctx, p.bucket, k, minio.RemoveObjectOptions{})
		return errors.New("s3: size mismatch")
	}
	// the source must not hold more than declared
	var one [1]byte
	if n, _ := r.Read(one[:]); n > 0 {
		p.cli.RemoveObject(ctx, p.bucket, k, minio.RemoveObjectOptions{})
		return errors.New("s3: source longer than declared size")
	}
	return nil
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
	return mapErr(p.cli.RemoveObject(ctx, p.bucket, k, minio.RemoveObjectOptions{}))
}
