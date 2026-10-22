// Package sftp is the SFTP (SSH) storage provider. The SSH host key must be pinned (SHA256 fingerprint); a first
// connection without a pin fails with *cloud.HostKeyError so the caller can ask the user to confirm and retry.
package sftp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"oa-agent/backend/internal/cloud"
)

const (
	listCap        = 5000
	dialTimeout    = 15 * time.Second
	hardlinkExtOpe = "hardlink@openssh.com"
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "sftp",
		Name:        "SFTP（SSH）",
		Group:       "enterprise",
		Description: "通过 SSH 文件传输协议访问 Linux 服务器、NAS 等；首次连接需确认服务器主机密钥指纹。",
		Fields: []cloud.Field{
			{Key: "host", Label: "主机", Type: cloud.TText, Required: true, Placeholder: "sftp.example.com", Help: "域名或 IP，不要带协议前缀。"},
			{Key: "port", Label: "端口", Type: cloud.TNumber, Default: "22", Placeholder: "22"},
			{Key: "username", Label: "用户名", Type: cloud.TText, Required: true},
			{Key: "auth", Label: "认证方式", Type: cloud.TSelect, Default: "password", Options: []cloud.Option{{Value: "password", Label: "密码"}, {Value: "key", Label: "私钥"}}},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "password"}},
			{Key: "private_key", Label: "私钥", Type: cloud.TTextarea, Secret: true, Required: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "key"}, Help: "粘贴 PEM 或 OpenSSH 格式的私钥全文。"},
			{Key: "passphrase", Label: "私钥口令", Type: cloud.TPassword, Secret: true, ShowIf: &cloud.ShowIf{Key: "auth", Equals: "key"}, Help: "私钥未加密时留空。"},
			{Key: "host_key", Label: "已信任的主机密钥指纹", Type: cloud.TText, Managed: true, Help: "SHA256 指纹；首次连接由您确认后自动写入，之后不一致将拒绝连接。"},
		},
		Factory: factory,
	})
}

// Fingerprint returns the "SHA256:base64" fingerprint of a public key.
func Fingerprint(k ssh.PublicKey) string { return ssh.FingerprintSHA256(k) }

type provider struct {
	base string
	ssh  *ssh.Client
	c    *pkgsftp.Client
	once sync.Once
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	host := s.Get("host")
	if err := cloud.CheckHost(host); err != nil {
		return nil, err
	}
	if s.Get("username") == "" {
		return nil, &cloud.ValidationError{Code: "REQUIRED", Msg: "请填写“用户名”"}
	}
	base, err := cleanBase(s.BasePath)
	if err != nil {
		return nil, err
	}
	var methods []ssh.AuthMethod
	if s.Get("auth") == "key" {
		var signer ssh.Signer
		pem := []byte(s.Secrets["private_key"])
		if pp := s.Secrets["passphrase"]; pp != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pp))
		} else {
			signer, err = ssh.ParsePrivateKey(pem)
		}
		if err != nil {
			var missing *ssh.PassphraseMissingError
			if errors.As(err, &missing) {
				return nil, fmt.Errorf("%w: the private key needs a passphrase", cloud.ErrAuth)
			}
			return nil, fmt.Errorf("%w: the private key cannot be read", cloud.ErrAuth)
		}
		methods = []ssh.AuthMethod{ssh.PublicKeys(signer)}
	} else {
		methods = []ssh.AuthMethod{ssh.Password(s.Secrets["password"])}
	}

	pin := s.Get("host_key")
	var hkErr error
	cfg := &ssh.ClientConfig{
		User: s.Get("username"),
		Auth: methods,
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			fp := Fingerprint(key)
			switch {
			case pin == "":
				hkErr = &cloud.HostKeyError{Fingerprint: fp}
			case subtle.ConstantTimeCompare([]byte(pin), []byte(fp)) != 1:
				hkErr = &cloud.HostKeyError{Fingerprint: fp, Changed: true}
			}
			return hkErr
		},
		Timeout: dialTimeout,
	}

	addr := net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(s.Int("port", 22)))
	conn, err := cloud.SafeDialer(dialTimeout).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", cloud.ErrUnreachable, redact(err))
	}
	// Bound the handshake by ctx and a deadline; clear both once it is done.
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	stop()
	if err != nil {
		_ = conn.Close()
		if hkErr != nil {
			return nil, hkErr
		}
		return nil, mapErr(err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(cc, chans, reqs)
	sc, err := pkgsftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("%w: SFTP subsystem unavailable", cloud.ErrUnreachable)
	}
	return &provider{base: base, ssh: client, c: sc}, nil
}

func cleanBase(b string) (string, error) {
	b = strings.TrimSpace(b)
	if b == "" {
		return "", nil
	}
	for _, c := range strings.Split(b, "/") {
		if c == ".." || strings.ContainsAny(c, "\\\x00") {
			return "", &cloud.ValidationError{Code: "INVALID_PATH", Msg: "起始目录无效"}
		}
	}
	if b != "/" {
		b = strings.TrimRight(b, "/")
	}
	return b, nil
}

// redact drops the (possibly internal-address-bearing) details of a dial error.
func redact(err error) string {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	return "connection failed"
}

func mapErr(err error) error {
	var hk *cloud.HostKeyError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &hk),
		errors.Is(err, cloud.ErrNotFound), errors.Is(err, cloud.ErrExists), errors.Is(err, cloud.ErrAuth),
		errors.Is(err, cloud.ErrPermission), errors.Is(err, cloud.ErrUnreachable), errors.Is(err, cloud.ErrUnsupported),
		errors.Is(err, cloud.ErrTooLarge):
		return err
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w", cloud.ErrNotFound)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w", cloud.ErrPermission)
	case errors.Is(err, os.ErrExist):
		return fmt.Errorf("%w", cloud.ErrExists)
	case strings.Contains(err.Error(), "unable to authenticate"), strings.Contains(err.Error(), "no supported methods remain"):
		return fmt.Errorf("%w", cloud.ErrAuth)
	}
	var se *pkgsftp.StatusError
	if errors.As(err, &se) {
		switch se.Code {
		case 2:
			return fmt.Errorf("%w", cloud.ErrNotFound)
		case 3:
			return fmt.Errorf("%w", cloud.ErrPermission)
		case 7, 12:
			return fmt.Errorf("%w", cloud.ErrUnreachable)
		}
		return errors.New("SFTP operation failed")
	}
	var ne net.Error
	var oe *net.OpError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded) ||
		errors.As(err, &ne) || errors.As(err, &oe) || strings.Contains(err.Error(), "handshake failed") ||
		strings.Contains(err.Error(), "connection") {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return errors.New("SFTP operation failed")
}

// guard makes ctx cancellation abort a blocked operation by closing the connection.
func (p *provider) guard(ctx context.Context) func() {
	if err := ctx.Err(); err != nil {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() { _ = p.Close() })
	return func() { stop() }
}

func (p *provider) abs(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	full := cloud.Join(p.base, rel)
	if full == "" {
		return ".", nil
	}
	return full, nil
}

func (p *provider) Close() error {
	var err error
	p.once.Do(func() {
		_ = p.c.Close()
		err = p.ssh.Close()
	})
	return err
}

func entryOf(rel string, fi os.FileInfo) cloud.Entry {
	return cloud.Entry{Name: fi.Name(), Path: rel, IsDir: fi.IsDir(), Size: fi.Size(), ModTime: fi.ModTime()}
}

func (p *provider) ctxErr(ctx context.Context, err error) error {
	if cerr := ctx.Err(); cerr != nil && err != nil {
		return cerr
	}
	return mapErr(err)
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	full, err := p.abs(dir)
	if err != nil {
		return nil, err
	}
	defer p.guard(ctx)()
	fis, err := p.c.ReadDir(full)
	if err != nil {
		return nil, p.ctxErr(ctx, err)
	}
	out := make([]cloud.Entry, 0, len(fis))
	for _, fi := range fis {
		n := fi.Name()
		if n == "." || n == ".." || fi.Mode()&os.ModeSymlink != 0 {
			continue // symlinks are never followed
		}
		if len(out) >= listCap {
			break
		}
		out = append(out, entryOf(path.Join(dir, n), fi))
	}
	return out, nil
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	full, err := p.abs(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	defer p.guard(ctx)()
	fi, err := p.c.Lstat(full)
	if err != nil {
		return cloud.Entry{}, p.ctxErr(ctx, err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return cloud.Entry{}, fmt.Errorf("%w: symbolic links are not followed", cloud.ErrNotFound)
	}
	e := entryOf(rel, fi)
	if rel == "" {
		e.Name = ""
	}
	return e, nil
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	full, err := p.abs(rel)
	if err != nil {
		return nil, 0, err
	}
	stop := p.guard(ctx)
	defer stop()
	fi, err := p.c.Lstat(full)
	if err != nil {
		return nil, 0, p.ctxErr(ctx, err)
	}
	if fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, 0, fmt.Errorf("%w: not a regular file", cloud.ErrNotFound)
	}
	f, err := p.c.Open(full)
	if err != nil {
		return nil, 0, p.ctxErr(ctx, err)
	}
	return f, fi.Size(), nil
}

func (p *provider) mkdirAll(dir string) error {
	var levels []string
	for d := dir; d != "" && d != "." && d != "/"; d = path.Dir(d) {
		levels = append(levels, d)
		if path.Dir(d) == d {
			break
		}
	}
	for i := len(levels) - 1; i >= 0; i-- {
		d := levels[i]
		fi, err := p.c.Lstat(d)
		if err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%w: a file is in the way of a folder", cloud.ErrExists)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := p.c.Mkdir(d); err != nil {
			if fi, serr := p.c.Lstat(d); serr == nil && fi.IsDir() {
				continue // created concurrently
			}
			return err
		}
	}
	return nil
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) (err error) {
	full, err := p.abs(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return cloud.ErrExists
	}
	defer p.guard(ctx)()
	defer func() {
		if err != nil {
			err = p.ctxErr(ctx, err)
		}
	}()
	if _, serr := p.c.Lstat(full); serr == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(serr, os.ErrNotExist) {
		return serr
	}
	dir, name := path.Split(full)
	dir = strings.TrimRight(dir, "/")
	if dir == "" && strings.HasPrefix(full, "/") {
		dir = "/"
	}
	if dir != "" {
		if err := p.mkdirAll(dir); err != nil {
			return err
		}
	}
	tmp := path.Join(dir, tempName(name))
	f, err := p.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = f.Close()
			_ = p.c.Remove(tmp)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("size mismatch: wrote %d of %d bytes", n, size)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if fi, err := p.c.Lstat(tmp); err != nil {
		return err
	} else if fi.Size() != size {
		return fmt.Errorf("remote size %d differs from %d", fi.Size(), size)
	}
	// Final existence check, then a rename that cannot replace: hard link when the server offers it (atomic,
	// fails when the target exists), otherwise Stat + SSH_FXP_RENAME (which OpenSSH refuses over an existing file).
	if _, serr := p.c.Lstat(full); serr == nil {
		return fmt.Errorf("%w", cloud.ErrExists)
	} else if !errors.Is(serr, os.ErrNotExist) {
		return serr
	}
	if _, ok := p.c.HasExtension(hardlinkExtOpe); ok {
		if lerr := p.c.Link(tmp, full); lerr != nil {
			if _, serr := p.c.Lstat(full); serr == nil {
				return fmt.Errorf("%w", cloud.ErrExists)
			}
			return lerr
		}
		cleanup = false
		_ = p.c.Remove(tmp)
		return nil
	}
	if err := p.c.Rename(tmp, full); err != nil {
		if _, serr := p.c.Lstat(full); serr == nil {
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		return err
	}
	cleanup = false
	return nil
}

func tempName(name string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	if len(name) > 120 {
		name = ""
	}
	return "." + name + ".oa-part-" + hex.EncodeToString(b[:])
}

func (p *provider) Remove(ctx context.Context, rel string) error {
	full, err := p.abs(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return cloud.ErrUnsupported
	}
	defer p.guard(ctx)()
	fi, err := p.c.Lstat(full)
	if err != nil {
		return p.ctxErr(ctx, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("%w: folders are never removed", cloud.ErrUnsupported)
	}
	return p.ctxErr(ctx, p.c.Remove(full))
}
