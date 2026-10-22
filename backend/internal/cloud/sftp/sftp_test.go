package sftp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pkgsftp "github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"oa-agent/backend/internal/cloud"
)

type server struct {
	addr     string
	port     int
	root     string
	hostKey  ssh.Signer
	clientPK ssh.PublicKey
	ln       net.Listener
}

func newSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, priv
}

func startServer(t *testing.T, clientPub ssh.PublicKey) *server {
	t.Helper()
	hk, _ := newSigner(t)
	srv := &server{root: t.TempDir(), hostKey: hk, clientPK: clientPub}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "alice" && string(pw) == "s3cret" {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == "alice" && clientPub != nil && bytes.Equal(k.Marshal(), clientPub.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hk)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv.ln = ln
	srv.port = ln.Addr().(*net.TCPAddr).Port
	srv.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					conn.Close()
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "session" {
						nc.Reject(ssh.UnknownChannelType, "no")
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						return
					}
					go func() {
						for rq := range creqs {
							ok := rq.Type == "subsystem" && len(rq.Payload) >= 8 && string(rq.Payload[4:]) == "sftp"
							rq.Reply(ok, nil)
							if ok {
								s, err := pkgsftp.NewServer(ch, pkgsftp.WithServerWorkingDirectory(srv.root))
								if err == nil {
									_ = s.Serve()
								}
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return srv
}

func (s *server) settings(pin string) cloud.Settings {
	return cloud.Settings{
		Values:  map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(s.port), "username": "alice", "host_key": pin},
		Secrets: map[string]string{"password": "s3cret"},
	}
}

func connect(t *testing.T, s *server, set cloud.Settings) cloud.Provider {
	t.Helper()
	p, err := cloud.Open(context.Background(), "sftp", set)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestHostKeyFlow(t *testing.T) {
	s := startServer(t, nil)
	ctx := context.Background()
	_, err := cloud.Open(ctx, "sftp", s.settings(""))
	var hk *cloud.HostKeyError
	if !errors.As(err, &hk) || hk.Changed || hk.Fingerprint != Fingerprint(s.hostKey.PublicKey()) {
		t.Fatalf("empty pin: %v", err)
	}
	if !strings.HasPrefix(hk.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint format %q", hk.Fingerprint)
	}
	p, err := cloud.Open(ctx, "sftp", s.settings(hk.Fingerprint))
	if err != nil {
		t.Fatalf("matching pin: %v", err)
	}
	p.Close()
	_, err = cloud.Open(ctx, "sftp", s.settings("SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"))
	if !errors.As(err, &hk) || !hk.Changed || hk.Fingerprint != Fingerprint(s.hostKey.PublicKey()) {
		t.Fatalf("wrong pin: %v", err)
	}
}

func TestAuth(t *testing.T) {
	_, priv := newSigner(t)
	signer, _ := ssh.NewSignerFromKey(priv)
	s := startServer(t, signer.PublicKey())
	pin := Fingerprint(s.hostKey.PublicKey())
	ctx := context.Background()

	bad := s.settings(pin)
	bad.Secrets["password"] = "nope"
	if _, err := cloud.Open(ctx, "sftp", bad); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}

	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatal(err)
	}
	key := cloud.Settings{
		Values:  map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(s.port), "username": "alice", "auth": "key", "host_key": pin},
		Secrets: map[string]string{"private_key": string(pem.EncodeToMemory(blk))},
	}
	p, err := cloud.Open(ctx, "sftp", key)
	if err != nil {
		t.Fatalf("key auth: %v", err)
	}
	p.Close()
	key.Secrets["private_key"] = "not a key"
	if _, err := cloud.Open(ctx, "sftp", key); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("garbage key: %v", err)
	}
	// a passphrase-protected key
	enc, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte("pp"))
	if err != nil {
		t.Fatal(err)
	}
	key.Secrets["private_key"] = string(pem.EncodeToMemory(enc))
	if _, err := cloud.Open(ctx, "sftp", key); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("missing passphrase: %v", err)
	}
	key.Secrets["passphrase"] = "pp"
	p, err = cloud.Open(ctx, "sftp", key)
	if err != nil {
		t.Fatalf("passphrase key: %v", err)
	}
	p.Close()
}

func TestOperations(t *testing.T) {
	s := startServer(t, nil)
	set := s.settings(Fingerprint(s.hostKey.PublicKey()))
	set.BasePath = "base"
	if err := os.Mkdir(filepath.Join(s.root, "base"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := connect(t, s, set)
	ctx := context.Background()

	body := "hello 世界"
	if err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(s.root, "base", "a", "b", "c.txt"))
	if string(got) != body {
		t.Fatalf("content %q", got)
	}
	// second write never touches the file
	err := p.PutNew(ctx, "a/b/c.txt", strings.NewReader("OVERWRITE"), 9)
	if !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("second PutNew: %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(s.root, "base", "a", "b", "c.txt"))
	if string(got) != body {
		t.Fatalf("content changed: %q", got)
	}
	// no temp leftovers
	ents, _ := os.ReadDir(filepath.Join(s.root, "base", "a", "b"))
	if len(ents) != 1 {
		t.Fatalf("leftovers: %v", ents)
	}
	// a file in the way of a folder
	if err := p.PutNew(ctx, "a/b/c.txt/d", strings.NewReader("x"), 1); err == nil {
		t.Fatal("file in the way must fail")
	}
	// size mismatch cleans up the temp file and creates nothing
	if err := p.PutNew(ctx, "a/short.txt", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short body must fail")
	}
	if err := p.PutNew(ctx, "a/long.txt", strings.NewReader("abcdef"), 3); err == nil {
		t.Fatal("long body must fail")
	}
	ents, _ = os.ReadDir(filepath.Join(s.root, "base", "a"))
	if len(ents) != 1 || ents[0].Name() != "b" {
		t.Fatalf("failed writes left files: %v", ents)
	}
	// read error mid-stream
	if err := p.PutNew(ctx, "a/err.txt", io.MultiReader(strings.NewReader("ab"), errReader{}), 10); err == nil {
		t.Fatal("reader error must fail")
	}
	ents, _ = os.ReadDir(filepath.Join(s.root, "base", "a"))
	if len(ents) != 1 {
		t.Fatalf("reader error left files: %v", ents)
	}

	root, err := p.List(ctx, "")
	if err != nil || len(root) != 1 || root[0].Name != "a" || !root[0].IsDir || root[0].Path != "a" {
		t.Fatalf("List root: %v %v", root, err)
	}
	l, err := p.List(ctx, "a/b")
	if err != nil || len(l) != 1 || l[0].Path != "a/b/c.txt" || l[0].Size != int64(len(body)) {
		t.Fatalf("List: %v %v", l, err)
	}
	if _, err := p.List(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("List missing: %v", err)
	}
	st, err := p.Stat(ctx, "a/b/c.txt")
	if err != nil || st.IsDir || st.Size != int64(len(body)) {
		t.Fatalf("Stat: %v %v", st, err)
	}
	if _, err := p.Stat(ctx, "a/zzz"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Stat missing: %v", err)
	}
	rc, n, err := p.Open(ctx, "a/b/c.txt")
	if err != nil || n != int64(len(body)) {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if string(b) != body {
		t.Fatalf("Open content %q", b)
	}
	if _, _, err := p.Open(ctx, "a/b"); err == nil {
		t.Fatal("opening a directory must fail")
	}
	if err := p.Remove(ctx, "a/b"); err == nil {
		t.Fatal("folders are never removed")
	}
	if err := p.Remove(ctx, "a/b/c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a/b/c.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Remove missing: %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestPathRejection(t *testing.T) {
	s := startServer(t, nil)
	p := connect(t, s, s.settings(Fingerprint(s.hostKey.PublicKey())))
	ctx := context.Background()
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", `a\b`, "a//b", "."} {
		if _, err := p.List(ctx, bad); err == nil {
			t.Errorf("List %q accepted", bad)
		}
		if _, err := p.Stat(ctx, bad); err == nil {
			t.Errorf("Stat %q accepted", bad)
		}
		if _, _, err := p.Open(ctx, bad); err == nil {
			t.Errorf("Open %q accepted", bad)
		}
		if err := p.PutNew(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("PutNew %q accepted", bad)
		}
		if err := p.Remove(ctx, bad); err == nil {
			t.Errorf("Remove %q accepted", bad)
		}
	}
	if _, err := cleanBase("../x"); err == nil {
		t.Error("base with .. accepted")
	}
}

func TestListCap(t *testing.T) {
	s := startServer(t, nil)
	p := connect(t, s, s.settings(Fingerprint(s.hostKey.PublicKey())))
	for i := 0; i < listCap+20; i++ {
		if err := os.WriteFile(filepath.Join(s.root, "f"+strconv.Itoa(i)), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l, err := p.List(context.Background(), "")
	if err != nil || len(l) != listCap {
		t.Fatalf("cap: %d %v", len(l), err)
	}
}

func TestProbe(t *testing.T) {
	s := startServer(t, nil)
	p := connect(t, s, s.settings(Fingerprint(s.hostKey.PublicKey())))
	r := cloud.Probe(context.Background(), p, true, ".oa-agent-test-123")
	if !r.OK || len(r.Steps) != 2 {
		t.Fatalf("probe: %+v", r)
	}
	ents, _ := os.ReadDir(s.root)
	if len(ents) != 0 {
		t.Fatalf("marker left behind: %v", ents)
	}
}

func TestSafeDialerRefusesMetadata(t *testing.T) {
	set := cloud.Settings{
		Values:  map[string]string{"host": "169.254.169.254", "username": "u"},
		Secrets: map[string]string{"password": "p"},
	}
	_, err := cloud.Open(context.Background(), "sftp", set)
	var ve *cloud.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("CheckHost should refuse: %v", err)
	}
	// hostnames resolving to a blocked address are caught by the dialer itself
	d := cloud.SafeDialer(time.Second)
	if _, err := d.DialContext(context.Background(), "tcp", "169.254.169.254:22"); err == nil {
		t.Fatal("dialer must refuse")
	}
}

func TestUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	defer ln.Close()
	set := cloud.Settings{
		Values:  map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(port), "username": "u"},
		Secrets: map[string]string{"password": "p"},
	}
	if _, err := cloud.Open(context.Background(), "sftp", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("closing listener: %v", err)
	}
	ln.Close()
	if _, err := cloud.Open(context.Background(), "sftp", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("closed port: %v", err)
	}
}

func TestValidate(t *testing.T) {
	sec := map[string]string{"password": "p"}
	if _, _, err := cloud.Validate("sftp", map[string]string{"host": "h", "username": "u"}, sec, nil); err != nil {
		t.Fatalf("valid: %v", err)
	}
	cases := map[string]struct {
		v, s map[string]string
	}{
		"no host":         {map[string]string{"username": "u"}, sec},
		"no username":     {map[string]string{"host": "h"}, sec},
		"no password":     {map[string]string{"host": "h", "username": "u"}, nil},
		"key without key": {map[string]string{"host": "h", "username": "u", "auth": "key"}, sec},
		"bad auth":        {map[string]string{"host": "h", "username": "u", "auth": "x"}, sec},
		"bad port":        {map[string]string{"host": "h", "username": "u", "port": "0"}, sec},
	}
	for name, c := range cases {
		if _, _, err := cloud.Validate("sftp", c.v, c.s, nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, _, err := cloud.Validate("sftp", map[string]string{"host": "h", "username": "u", "auth": "key"}, map[string]string{"private_key": "k"}, nil); err != nil {
		t.Fatalf("key auth: %v", err)
	}
	k, _ := cloud.KindByID("sftp")
	if k.Group != "enterprise" {
		t.Fatal("group")
	}
	var managed bool
	for _, f := range k.Fields {
		if f.Key == "host_key" && f.Managed {
			managed = true
		}
	}
	if !managed {
		t.Fatal("host_key must be a managed field")
	}
}
