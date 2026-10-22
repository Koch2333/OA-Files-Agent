// Package smb is the SMB2/3 (CIFS) share provider (NTLM authentication). Paths are given relative to the base
// path with forward slashes and converted for the share; names that Windows cannot store faithfully (backslash,
// colon, reserved characters, trailing dots or spaces) are rejected instead of being silently rewritten.
package smb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	smb2 "github.com/cloudsoda/go-smb2"

	"oa-agent/backend/internal/cloud"
)

const (
	listCap     = 5000
	dialTimeout = 15 * time.Second
)

// ErrShareNotFound is wrapped together with cloud.ErrNotFound when the server does not know the share name.
var ErrShareNotFound = errors.New("the share name does not exist on the server")

func init() {
	cloud.Register(cloud.Kind{
		ID:          "smb",
		Name:        "SMB 共享（Windows / NAS）",
		Group:       "enterprise",
		Description: "访问 Windows 文件服务器或 NAS 的 SMB 共享（仅 SMB2/3，NTLM 认证）。",
		Fields: []cloud.Field{
			{Key: "host", Label: "主机", Type: cloud.TText, Required: true, Placeholder: "fileserver.corp.local", Help: "域名或 IP，不要带 \\\\ 前缀。"},
			{Key: "port", Label: "端口", Type: cloud.TNumber, Default: "445", Placeholder: "445"},
			{Key: "share", Label: "共享名", Type: cloud.TText, Required: true, Placeholder: "documents", Help: "共享文件夹的名称（\\\\主机\\共享名 中的“共享名”）。"},
			{Key: "domain", Label: "域", Type: cloud.TText, Placeholder: "CORP", Help: "工作组环境留空。"},
			{Key: "username", Label: "用户名", Type: cloud.TText, Required: true},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true},
		},
		Factory: factory,
	})
}

type provider struct {
	base    string // backslash separated, no leading/trailing separator
	session *smb2.Session
	share   *smb2.Share
	once    sync.Once
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	host := s.Get("host")
	if err := cloud.CheckHost(host); err != nil {
		return nil, err
	}
	share := s.Get("share")
	if err := checkShare(share); err != nil {
		return nil, err
	}
	if s.Get("username") == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“用户名”"}
	}
	base, err := baseToNative(s.BasePath)
	if err != nil {
		return nil, err
	}
	host = strings.Trim(host, "[]")
	addr := net.JoinHostPort(host, strconv.Itoa(s.Int("port", 445)))
	conn, err := cloud.SafeDialer(dialTimeout).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	d := &smb2.Dialer{Initiator: &smb2.NTLMInitiator{
		User: s.Get("username"), Password: s.Secrets["password"], Domain: s.Get("domain"),
	}}
	sess, err := d.DialConn(ctx, conn, host)
	if err != nil {
		_ = conn.Close()
		return nil, mapErr(ctx, err)
	}
	_ = conn.SetDeadline(time.Time{})
	sess = sess.WithContext(ctx)
	fs, err := sess.Mount(share)
	if err != nil {
		_ = sess.Logoff()
		_ = conn.Close()
		return nil, mapErr(ctx, err)
	}
	return &provider{base: base, session: sess, share: fs}, nil
}

func checkShare(share string) error {
	if share == "" {
		return &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“共享名”"}
	}
	if strings.ContainsAny(share, "\\/:*?\"<>|\x00") || len(share) > 80 {
		return &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "“共享名”无效"}
	}
	return nil
}

// checkName rejects components that SMB/Windows would alter or interpret.
func checkName(c string) error {
	if c == "" || c == "." || c == ".." || strings.ContainsAny(c, "\\:*?\"<>|\x00") ||
		strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") {
		return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径无效"}
	}
	for _, r := range c {
		if r < 0x20 {
			return &cloud.ValidationError{Code: "INVALID_PATH", Msg: "路径含有控制字符"}
		}
	}
	return nil
}

func baseToNative(b string) (string, error) {
	b = strings.Trim(strings.TrimSpace(b), "/")
	if b == "" {
		return "", nil
	}
	parts := strings.Split(b, "/")
	for _, c := range parts {
		if err := checkName(c); err != nil {
			return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "起始目录无效"}
		}
	}
	return strings.Join(parts, `\`), nil
}

// native converts a provider-relative path to the share path ("" = share root, which the library spells "").
func (p *provider) native(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	var parts []string
	if rel != "" {
		parts = strings.Split(rel, "/")
		for _, c := range parts {
			if err := checkName(c); err != nil {
				return "", err
			}
		}
	}
	full := strings.Join(parts, `\`)
	switch {
	case p.base == "":
		return full, nil
	case full == "":
		return p.base, nil
	}
	return p.base + `\` + full, nil
}

// NTSTATUS values (MS-ERREF) the provider distinguishes.
const (
	stAccessDenied    = 0xC0000022
	stNameNotFound    = 0xC0000034
	stNameCollision   = 0xC0000035
	stPathNotFound    = 0xC000003A
	stNoSuchFile      = 0xC000000F
	stNotADirectory   = 0xC0000103
	stNoSuchUser      = 0xC0000064
	stWrongPassword   = 0xC000006A
	stLogonFailure    = 0xC000006D
	stAccountRestrict = 0xC000006E
	stPasswordExpired = 0xC0000071
	stAccountDisabled = 0xC0000072
	stAccountLocked   = 0xC0000234
	stBadNetworkPath  = 0xC00000BE
	stBadNetworkName  = 0xC00000CC
	stNetworkDenied   = 0xC00000CA
)

func mapErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	for _, s := range []error{cloud.ErrNotFound, cloud.ErrExists, cloud.ErrAuth, cloud.ErrPermission, cloud.ErrUnreachable, cloud.ErrUnsupported, cloud.ErrTooLarge} {
		if errors.Is(err, s) {
			return err
		}
	}
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	var re *smb2.ResponseError
	if errors.As(err, &re) {
		switch re.Code {
		case stLogonFailure, stWrongPassword, stNoSuchUser, stAccountRestrict, stPasswordExpired, stAccountDisabled, stAccountLocked:
			return fmt.Errorf("%w", cloud.ErrAuth)
		case stAccessDenied, stNetworkDenied:
			return fmt.Errorf("%w", cloud.ErrPermission)
		case stNameNotFound, stPathNotFound, stNoSuchFile, stNotADirectory:
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case stBadNetworkName:
			return fmt.Errorf("%w: %w", cloud.ErrNotFound, ErrShareNotFound)
		case stNameCollision:
			return fmt.Errorf("%w", cloud.ErrExists)
		case stBadNetworkPath:
			return fmt.Errorf("%w", cloud.ErrUnreachable)
		}
		return fmt.Errorf("SMB server replied status 0x%08X", re.Code)
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w", cloud.ErrPermission)
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	var te *smb2.TransportError
	var ne net.Error
	var oe *net.OpError
	if errors.As(err, &te) || errors.As(err, &ne) || errors.As(err, &oe) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, io.ErrClosedPipe) || errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(err.Error(), "connection") {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return errors.New("SMB operation failed")
}

func (p *provider) Close() error {
	var err error
	p.once.Do(func() {
		_ = p.share.Umount()
		err = p.session.Logoff()
	})
	return err
}

func (p *provider) fs(ctx context.Context) *smb2.Share { return p.share.WithContext(ctx) }

func entryOf(rel string, fi os.FileInfo) cloud.Entry {
	return cloud.Entry{Name: fi.Name(), Path: rel, IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	n, err := p.native(dir)
	if err != nil {
		return nil, err
	}
	fis, err := p.fs(ctx).ReadDir(n)
	if err != nil {
		return nil, mapErr(ctx, err)
	}
	out := make([]cloud.Entry, 0, len(fis))
	for _, fi := range fis {
		name := fi.Name()
		if name == "." || name == ".." || fi.Mode()&os.ModeSymlink != 0 || checkName(name) != nil {
			continue // symlinks/reparse points and names we could not address again are not offered
		}
		if len(out) >= listCap {
			break
		}
		out = append(out, entryOf(path.Join(dir, name), fi))
	}
	return out, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	n, err := p.native(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	fi, err := p.fs(ctx).Stat(n)
	if err != nil {
		return cloud.Entry{}, mapErr(ctx, err)
	}
	e := entryOf(rel, fi)
	if rel == "" {
		e.Name = ""
	}
	return e, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	n, err := p.native(rel)
	if err != nil {
		return nil, 0, err
	}
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	fs := p.fs(ctx)
	f, err := fs.Open(n)
	if err != nil {
		return nil, 0, mapErr(ctx, err)
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, mapErr(ctx, err)
	}
	if fi.IsDir() {
		_ = f.Close()
		return nil, 0, fmt.Errorf("%w: not a regular file", cloud.ErrNotFound)
	}
	return f, fi.Size(), nil
}

func (p *provider) mkdirAll(fs *smb2.Share, native string) error {
	if native == "" {
		return nil
	}
	parts := strings.Split(native, `\`)
	for i := range parts {
		d := strings.Join(parts[:i+1], `\`)
		fi, err := fs.Stat(d)
		if err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%w: a file is in the way of a folder", cloud.ErrExists)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) && !isNotFound(err) {
			return err
		}
		if err := fs.Mkdir(d, 0o755); err != nil {
			if fi, serr := fs.Stat(d); serr == nil && fi.IsDir() {
				continue // created concurrently
			}
			return err
		}
	}
	return nil
}

func isNotFound(err error) bool {
	var re *smb2.ResponseError
	return errors.As(err, &re) && (re.Code == stNameNotFound || re.Code == stPathNotFound || re.Code == stNoSuchFile)
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) (err error) {
	n, err := p.native(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	defer func() {
		if err != nil {
			err = mapErr(ctx, err)
		}
	}()
	fs := p.fs(ctx)
	if _, serr := fs.Stat(n); serr == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(serr, os.ErrNotExist) && !isNotFound(serr) {
		return serr
	}
	if i := strings.LastIndex(n, `\`); i >= 0 {
		if err := p.mkdirAll(fs, n[:i]); err != nil {
			return err
		}
	}
	// O_EXCL maps to FILE_CREATE: the server refuses when the name exists, so nothing is ever truncated.
	f, err := fs.OpenFile(n, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			// A fresh context so cleanup still runs after cancellation. We created the file (O_EXCL succeeded).
			cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			_ = p.fs(cctx).Remove(n)
		}
	}()
	w, err := io.Copy(f, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if w != size {
		return fmt.Errorf("size mismatch: wrote %d of %d bytes", w, size)
	}
	if err := f.Close(); err != nil {
		return err
	}
	fi, err := fs.Stat(n)
	if err != nil {
		return err
	}
	if fi.Size() != size {
		return fmt.Errorf("remote size %d differs from %d", fi.Size(), size)
	}
	ok = true
	return nil
}

func (p *provider) Remove(ctx context.Context, rel string) error {
	n, err := p.native(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrUnsupported)
	}
	fs := p.fs(ctx)
	fi, err := fs.Stat(n)
	if err != nil {
		return mapErr(ctx, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%w: folders are never removed", cloud.ErrUnsupported)
	}
	return mapErr(ctx, fs.Remove(n))
}
