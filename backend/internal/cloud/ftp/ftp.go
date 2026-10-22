// Package ftp is the FTP / FTPS (explicit and implicit TLS) storage provider. FTP connections are not safe for
// concurrent use, so operations on the shared control connection are serialized; streaming reads use a private
// connection so a slow reader never blocks other calls.
package ftp

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"net/textproto"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	jftp "github.com/jlaffaye/ftp"

	"oa-agent/backend/internal/cloud"
)

const (
	listCap     = 5000
	dialTimeout = 15 * time.Second
	opTimeout   = 30 * time.Second
	idleProbe   = 20 * time.Second
)

func init() {
	cloud.Register(cloud.Kind{
		ID:          "ftp",
		Name:        "FTP / FTPS",
		Group:       "enterprise",
		Description: "访问 FTP 服务器；建议启用 TLS（显式 FTPS 或隐式 FTPS），明文 FTP 会以明文传输密码和文件。",
		Fields: []cloud.Field{
			{Key: "host", Label: "主机", Type: cloud.TText, Required: true, Placeholder: "ftp.example.com", Help: "域名或 IP，不要带协议前缀。"},
			{Key: "port", Label: "端口", Type: cloud.TNumber, Default: "21", Placeholder: "21", Help: "隐式 FTPS 通常为 990。"},
			{Key: "tls", Label: "加密方式", Type: cloud.TSelect, Default: "explicit", Options: []cloud.Option{
				{Value: "none", Label: "不加密（明文 FTP）"}, {Value: "explicit", Label: "显式 TLS（AUTH TLS）"}, {Value: "implicit", Label: "隐式 TLS（FTPS）"}}},
			{Key: "username", Label: "用户名", Type: cloud.TText, Default: "anonymous", Placeholder: "anonymous", Help: "留空表示匿名登录。"},
			{Key: "password", Label: "密码", Type: cloud.TPassword, Secret: true},
			{Key: "insecure_skip_verify", Label: "跳过证书校验", Type: cloud.TSelect, Default: "false", Options: []cloud.Option{{Value: "false", Label: "否（推荐）"}, {Value: "true", Label: "是（自签名证书，不安全）"}}, ShowIf: nil, Help: "仅在使用自签名证书且信任该网络时开启。"},
		},
		Factory: factory,
	})
}

type config struct {
	addr, host   string
	tlsMode      string
	user, pass   string
	skipVerify   bool
	tlsConfig    *tls.Config
	base         string
	dialer       func() *net.Dialer
	connectLimit time.Duration
}

// session is one logged-in FTP connection plus the sockets it opened (so ctx cancellation can abort it).
type session struct {
	sc       *jftp.ServerConn
	mu       sync.Mutex
	socks    []net.Conn
	ctrlIP   netip.Addr
	dials    int
	lastUsed time.Time
}

func (s *session) track(c net.Conn) {
	s.mu.Lock()
	s.socks = append(s.socks, c)
	s.mu.Unlock()
}

func (s *session) kill() {
	s.mu.Lock()
	socks := s.socks
	s.mu.Unlock()
	for _, c := range socks {
		_ = c.Close()
	}
}

func (s *session) close() {
	if s.sc != nil {
		_ = s.sc.Quit()
	}
	s.kill()
}

type provider struct {
	cfg    *config
	mu     sync.Mutex // serializes operations on sess
	sess   *session
	closed bool
}

func factory(ctx context.Context, s cloud.Settings) (cloud.Provider, error) {
	host := s.Get("host")
	if err := cloud.CheckHost(host); err != nil {
		return nil, err
	}
	base := strings.TrimSpace(s.BasePath)
	for _, c := range strings.Split(base, "/") {
		if c == ".." || strings.ContainsAny(c, "\\\x00") {
			return nil, &cloud.ValidationError{Code: "INVALID_PATH", Msg: "起始目录无效"}
		}
	}
	if base != "/" {
		base = strings.TrimRight(base, "/")
	}
	mode := s.Get("tls")
	if mode == "" {
		mode = "explicit"
	}
	if mode != "none" && mode != "explicit" && mode != "implicit" {
		return nil, &cloud.ValidationError{Code: "INVALID_VALUE", Msg: "“加密方式”的取值无效"}
	}
	user := s.Get("username")
	if user == "" {
		user = "anonymous"
	}
	pass := s.Secrets["password"]
	if pass == "" && user == "anonymous" {
		pass = "anonymous@localhost"
	}
	cfg := &config{
		addr:    net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(s.Int("port", 21))),
		host:    strings.Trim(host, "[]"),
		tlsMode: mode, user: user, pass: pass,
		skipVerify: s.Bool("insecure_skip_verify", false),
		base:       base,
	}
	if mode != "none" {
		cfg.tlsConfig = &tls.Config{ServerName: cfg.host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.skipVerify,
			ClientSessionCache: tls.NewLRUClientSessionCache(16)}
	}
	p := &provider{cfg: cfg}
	sess, err := p.connect(ctx)
	if err != nil {
		return nil, err
	}
	p.sess = sess
	return p, nil
}

// connect dials and logs in. Every socket goes through cloud.SafeDialer; data connections must reach the same IP
// as the control connection, so a hostile PASV/EPSV reply cannot redirect a transfer to another machine.
func (p *provider) connect(ctx context.Context) (*session, error) {
	c := p.cfg
	s := &session{lastUsed: time.Now()}
	dialer := cloud.SafeDialer(dialTimeout)
	dialFn := func(network, address string) (net.Conn, error) {
		s.mu.Lock()
		s.dials++
		first := s.dials == 1
		s.mu.Unlock()
		dctx, cancel := context.WithTimeout(ctx, dialTimeout)
		defer cancel()
		conn, err := dialer.DialContext(dctx, network, address)
		if err != nil {
			return nil, err
		}
		s.track(conn)
		ap, perr := netip.ParseAddrPort(conn.RemoteAddr().String())
		if perr != nil {
			conn.Close()
			return nil, perr
		}
		if first {
			s.ctrlIP = ap.Addr().Unmap()
		} else if ap.Addr().Unmap() != s.ctrlIP || cloud.BlockedIP(ap.Addr()) {
			conn.Close()
			return nil, fmt.Errorf("%w: data connection redirected", cloud.ErrUnreachable)
		}
		if c.tlsConfig == nil {
			return conn, nil
		}
		tc := tls.Client(conn, c.tlsConfig.Clone())
		if !first {
			return tc, nil // lazily handshakes on first read/write, like the library does
		}
		if c.tlsMode == "implicit" {
			_ = conn.SetDeadline(time.Now().Add(dialTimeout))
			if err := tc.HandshakeContext(dctx); err != nil {
				conn.Close()
				return nil, err
			}
			_ = conn.SetDeadline(time.Time{})
			return tc, nil
		}
		return conn, nil // explicit: the library upgrades after AUTH TLS
	}
	opts := []jftp.DialOption{jftp.DialWithDialFunc(dialFn), jftp.DialWithShutTimeout(opTimeout), jftp.DialWithTrustPasvIP(false)}
	switch c.tlsMode {
	case "explicit":
		opts = append(opts, jftp.DialWithExplicitTLS(c.tlsConfig))
	case "implicit":
		opts = append(opts, jftp.DialWithTLS(c.tlsConfig))
	}
	stop := context.AfterFunc(ctx, s.kill)
	defer stop()
	sc, err := jftp.Dial(c.addr, opts...)
	if err != nil {
		s.kill()
		return nil, mapErr(ctx, err)
	}
	s.sc = sc
	if err := sc.Login(c.user, c.pass); err != nil {
		s.close()
		var te *textproto.Error
		if errors.As(err, &te) && (te.Code == 530 || te.Code == 331 || te.Code == 332) {
			return nil, fmt.Errorf("%w", cloud.ErrAuth)
		}
		if errors.As(err, &te) && te.Code/100 == 5 {
			return nil, fmt.Errorf("%w", cloud.ErrAuth)
		}
		return nil, mapErr(ctx, err)
	}
	return s, nil
}

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
	var te *textproto.Error
	if errors.As(err, &te) {
		msg := strings.ToLower(te.Msg)
		switch {
		case te.Code == 530 || te.Code == 532:
			return fmt.Errorf("%w", cloud.ErrAuth)
		case te.Code == 421:
			return fmt.Errorf("%w", cloud.ErrUnreachable)
		case te.Code == 553 || strings.Contains(msg, "permission") || strings.Contains(msg, "denied") || strings.Contains(msg, "not allowed"):
			return fmt.Errorf("%w", cloud.ErrPermission)
		case te.Code == 550 || te.Code == 450:
			return fmt.Errorf("%w", cloud.ErrNotFound)
		}
		return fmt.Errorf("FTP server replied %d", te.Code)
	}
	var cert *tls.CertificateVerificationError
	if errors.As(err, &cert) {
		return fmt.Errorf("%w: TLS certificate is not trusted", cloud.ErrUnreachable)
	}
	var ne net.Error
	var oe *net.OpError
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, os.ErrDeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &ne) || errors.As(err, &oe) ||
		strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "connection") {
		return fmt.Errorf("%w", cloud.ErrUnreachable)
	}
	return errors.New("FTP operation failed")
}

func isTransport(err error) bool {
	var te *textproto.Error
	return err != nil && !errors.As(err, &te) && !errors.Is(err, cloud.ErrNotFound) && !errors.Is(err, cloud.ErrExists) &&
		!errors.Is(err, cloud.ErrPermission) && !errors.Is(err, cloud.ErrUnsupported) && !errors.Is(err, cloud.ErrAuth)
}

// do runs fn on the shared connection, reconnecting when it died. idempotent operations are retried once after a
// transport failure.
func (p *provider) do(ctx context.Context, idempotent bool, fn func(sc *jftp.ServerConn) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return fmt.Errorf("%w: connection closed", cloud.ErrUnreachable)
	}
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if p.sess != nil && time.Since(p.sess.lastUsed) > idleProbe {
			if err := p.sess.sc.NoOp(); err != nil {
				p.sess.close()
				p.sess = nil
			}
		}
		if p.sess == nil {
			s, err := p.connect(ctx)
			if err != nil {
				return err
			}
			p.sess = s
		}
		s := p.sess
		stop := context.AfterFunc(ctx, s.kill)
		err := fn(s.sc)
		stop()
		s.lastUsed = time.Now()
		if err == nil {
			return nil
		}
		err = mapErr(ctx, err)
		if isTransport(err) {
			s.close()
			p.sess = nil
			if idempotent && attempt == 0 && ctx.Err() == nil {
				continue
			}
		}
		return err
	}
}

func (p *provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.sess != nil {
		p.sess.close()
		p.sess = nil
	}
	return nil
}

func (p *provider) abs(rel string) (string, error) {
	rel, err := cloud.CleanRel(rel)
	if err != nil {
		return "", err
	}
	return cloud.Join(p.cfg.base, rel), nil
}

// lookup finds one entry by listing its parent. ok=false means it does not exist.
func lookup(sc *jftp.ServerConn, full string) (*jftp.Entry, bool, error) {
	dir, name := path.Split(full)
	if dir != "/" {
		dir = strings.TrimRight(dir, "/")
	}
	if name == "" {
		return nil, false, nil
	}
	ents, err := sc.List(dir)
	if err != nil {
		var te *textproto.Error
		if errors.As(err, &te) && (te.Code == 550 || te.Code == 450) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, e := range ents {
		if e.Name == name {
			return e, true, nil
		}
	}
	return nil, false, nil
}

func toEntry(rel string, e *jftp.Entry) cloud.Entry {
	return cloud.Entry{Name: e.Name, Path: rel, IsDir: e.Type == jftp.EntryTypeFolder, Size: int64(e.Size), ModTime: e.Time}
}

func (p *provider) List(ctx context.Context, dir string) ([]cloud.Entry, error) {
	full, err := p.abs(dir)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	var out []cloud.Entry
	err = p.do(ctx, true, func(sc *jftp.ServerConn) error {
		if dir != "" {
			e, ok, err := lookup(sc, full)
			if err != nil {
				return err
			}
			if !ok || e.Type != jftp.EntryTypeFolder {
				return fmt.Errorf("%w", cloud.ErrNotFound)
			}
		}
		ents, err := sc.List(full)
		if err != nil {
			return err
		}
		out = out[:0]
		for _, e := range ents {
			if e.Name == "." || e.Name == ".." || e.Name == "" || strings.ContainsAny(e.Name, "/\\") || e.Type == jftp.EntryTypeLink {
				continue
			}
			if len(out) >= listCap {
				break
			}
			out = append(out, toEntry(path.Join(dir, e.Name), e))
		}
		return nil
	})
	return out, err
}

func (p *provider) Stat(ctx context.Context, rel string) (cloud.Entry, error) {
	full, err := p.abs(rel)
	if err != nil {
		return cloud.Entry{}, err
	}
	if rel == "" {
		return cloud.Entry{IsDir: true}, p.List2(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	var out cloud.Entry
	err = p.do(ctx, true, func(sc *jftp.ServerConn) error {
		e, ok, err := lookup(sc, full)
		if err != nil {
			return err
		}
		if !ok || e.Type == jftp.EntryTypeLink {
			return fmt.Errorf("%w", cloud.ErrNotFound)
		}
		out = toEntry(rel, e)
		return nil
	})
	return out, err
}

// List2 only checks that the root is reachable (Stat of the root).
func (p *provider) List2(ctx context.Context) error {
	_, err := p.List(ctx, "")
	return err
}

// ownedReader closes the private connection that backs a download.
type ownedReader struct {
	r    io.ReadCloser
	sess *session
	once sync.Once
	stop func() bool
}

func (o *ownedReader) Read(b []byte) (int, error) { return o.r.Read(b) }
func (o *ownedReader) Close() error {
	var err error
	o.once.Do(func() {
		o.stop()
		err = o.r.Close()
		o.sess.close()
	})
	return err
}

func (p *provider) Open(ctx context.Context, rel string) (io.ReadCloser, int64, error) {
	full, err := p.abs(rel)
	if err != nil {
		return nil, 0, err
	}
	if rel == "" {
		return nil, 0, fmt.Errorf("%w", cloud.ErrNotFound)
	}
	var size int64 = -1
	sctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	err = p.do(sctx, true, func(sc *jftp.ServerConn) error {
		e, ok, err := lookup(sc, full)
		if err != nil {
			return err
		}
		if !ok || e.Type != jftp.EntryTypeFile {
			return fmt.Errorf("%w", cloud.ErrNotFound)
		}
		size = int64(e.Size)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	// The download runs on its own connection so the shared one stays free.
	s, err := p.connect(ctx)
	if err != nil {
		return nil, 0, err
	}
	resp, err := s.sc.Retr(full)
	if err != nil {
		s.close()
		return nil, 0, mapErr(ctx, err)
	}
	return &ownedReader{r: resp, sess: s, stop: context.AfterFunc(ctx, s.kill)}, size, nil
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

func tempName(name string) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	if len(name) > 120 {
		name = ""
	}
	return "." + name + ".oa-part-" + hex.EncodeToString(b[:])
}

func (p *provider) PutNew(ctx context.Context, rel string, r io.Reader, size int64) error {
	full, err := p.abs(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrExists)
	}
	return p.do(ctx, false, func(sc *jftp.ServerConn) (err error) {
		if _, ok, err := lookup(sc, full); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		dir := path.Dir(full)
		if err := mkdirAll(sc, dir); err != nil {
			return err
		}
		tmp := path.Join(dir, tempName(path.Base(full)))
		cr := &countReader{r: io.LimitReader(r, size+1)}
		stored := false
		defer func() {
			if err != nil && stored {
				if sc.Delete(tmp) != nil {
					// the control connection may be dead: remove the partial file over a fresh one
					cctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
					defer cancel()
					if s2, cerr := p.connect(cctx); cerr == nil {
						_ = s2.sc.Delete(tmp)
						s2.close()
					}
				}
			}
		}()
		stored = true // the server may keep a partial file even when STOR fails
		if err := sc.Stor(tmp, cr); err != nil {
			return err
		}
		if cr.n != size {
			return fmt.Errorf("size mismatch: sent %d of %d bytes", cr.n, size)
		}
		e, ok, err := lookup(sc, tmp)
		if err != nil {
			return err
		}
		if !ok || int64(e.Size) != size {
			return errors.New("uploaded size does not match")
		}
		if _, ok, err := lookup(sc, full); err != nil {
			return err
		} else if ok {
			return fmt.Errorf("%w", cloud.ErrExists)
		}
		if err := sc.Rename(tmp, full); err != nil {
			if _, ok, _ := lookup(sc, full); ok {
				return fmt.Errorf("%w", cloud.ErrExists)
			}
			return err
		}
		stored = false
		return nil
	})
}

// mkdirAll creates every missing level of dir, checking each level first.
func mkdirAll(sc *jftp.ServerConn, dir string) error {
	var levels []string
	for d := dir; d != "" && d != "." && d != "/"; d = path.Dir(d) {
		levels = append(levels, d)
	}
	for i := len(levels) - 1; i >= 0; i-- {
		d := levels[i]
		e, ok, err := lookup(sc, d)
		if err != nil {
			return err
		}
		if ok {
			if e.Type != jftp.EntryTypeFolder {
				return fmt.Errorf("%w: a file is in the way of a folder", cloud.ErrExists)
			}
			continue
		}
		if err := sc.MakeDir(d); err != nil {
			if e, ok, _ := lookup(sc, d); ok && e.Type == jftp.EntryTypeFolder {
				continue
			}
			return err
		}
	}
	return nil
}

func (p *provider) Remove(ctx context.Context, rel string) error {
	full, err := p.abs(rel)
	if err != nil {
		return err
	}
	if rel == "" {
		return fmt.Errorf("%w", cloud.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	return p.do(ctx, false, func(sc *jftp.ServerConn) error {
		e, ok, err := lookup(sc, full)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w", cloud.ErrNotFound)
		}
		if e.Type != jftp.EntryTypeFile {
			return fmt.Errorf("%w: only files are removed", cloud.ErrUnsupported)
		}
		return sc.Delete(full)
	})
}
