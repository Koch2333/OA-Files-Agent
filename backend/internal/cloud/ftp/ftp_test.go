package ftp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	"oa-agent/backend/internal/cloud"
)

type driver struct {
	fs   afero.Fs
	mode ftpserver.TLSRequirement
	cert *tls.Config
}

func (d *driver) GetSettings() (*ftpserver.Settings, error) {
	return &ftpserver.Settings{ListenAddr: "127.0.0.1:0", TLSRequired: d.mode, IdleTimeout: 30}, nil
}
func (d *driver) ClientConnected(ftpserver.ClientContext) (string, error) { return "hi", nil }
func (d *driver) ClientDisconnected(ftpserver.ClientContext)              {}
func (d *driver) AuthUser(_ ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	if user == "anonymous" || (user == "alice" && pass == "s3cret") {
		return d.fs, nil
	}
	return nil, errors.New("bad credentials")
}
func (d *driver) GetTLSConfig() (*tls.Config, error) {
	if d.cert == nil {
		return nil, errors.New("no tls")
	}
	return d.cert, nil
}

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

type srv struct {
	port int
	fs   afero.Fs
}

func start(t *testing.T, mode ftpserver.TLSRequirement, withTLS bool) *srv {
	t.Helper()
	d := &driver{fs: afero.NewMemMapFs(), mode: mode}
	if withTLS {
		d.cert = selfSigned(t)
	}
	s := ftpserver.NewFtpServer(d)
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go s.Serve()
	t.Cleanup(func() { s.Stop() })
	_, ps, _ := net.SplitHostPort(s.Addr())
	port, _ := strconv.Atoi(ps)
	return &srv{port: port, fs: d.fs}
}

func (s *srv) settings(tlsMode string) cloud.Settings {
	return cloud.Settings{
		Values:  map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(s.port), "tls": tlsMode, "username": "alice", "insecure_skip_verify": "true"},
		Secrets: map[string]string{"password": "s3cret"},
	}
}

func open(t *testing.T, set cloud.Settings) cloud.Provider {
	t.Helper()
	p, err := cloud.Open(context.Background(), "ftp", set)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func behaviour(t *testing.T, s *srv, p cloud.Provider) {
	ctx := context.Background()
	body := "hello 世界 with spaces"
	if err := p.PutNew(ctx, "a b/c/file one.txt", strings.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutNew: %v", err)
	}
	got, err := afero.ReadFile(s.fs, "/a b/c/file one.txt")
	if err != nil || string(got) != body {
		t.Fatalf("content %q %v", got, err)
	}
	if err := p.PutNew(ctx, "a b/c/file one.txt", strings.NewReader("OVERWRITE"), 9); !errors.Is(err, cloud.ErrExists) {
		t.Fatalf("second PutNew: %v", err)
	}
	got, _ = afero.ReadFile(s.fs, "/a b/c/file one.txt")
	if string(got) != body {
		t.Fatalf("overwritten: %q", got)
	}
	// failures leave nothing behind
	if err := p.PutNew(ctx, "a b/c/short.txt", strings.NewReader("abc"), 10); err == nil {
		t.Fatal("short body must fail")
	}
	if err := p.PutNew(ctx, "a b/c/long.txt", strings.NewReader("abcdef"), 3); err == nil {
		t.Fatal("long body must fail")
	}
	ents, _ := afero.ReadDir(s.fs, "/a b/c")
	if len(ents) != 1 || ents[0].Name() != "file one.txt" {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("leftovers: %v", names)
	}
	if err := p.PutNew(ctx, "a b/c/file one.txt/x", strings.NewReader("x"), 1); err == nil {
		t.Fatal("file in the way must fail")
	}
	empty := "empty.txt"
	if err := p.PutNew(ctx, empty, strings.NewReader(""), 0); err != nil {
		t.Fatalf("empty file: %v", err)
	}

	root, err := p.List(ctx, "")
	if err != nil || len(root) != 2 {
		t.Fatalf("List root: %v %v", root, err)
	}
	cloud.SortEntries(root)
	if root[0].Name != "a b" || !root[0].IsDir || root[0].Path != "a b" || root[1].Name != empty {
		t.Fatalf("root entries: %+v", root)
	}
	l, err := p.List(ctx, "a b/c")
	if err != nil || len(l) != 1 || l[0].Path != "a b/c/file one.txt" || l[0].Size != int64(len(body)) {
		t.Fatalf("List: %+v %v", l, err)
	}
	if _, err := p.List(ctx, "nope"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("List missing: %v", err)
	}
	st, err := p.Stat(ctx, "a b/c/file one.txt")
	if err != nil || st.IsDir || st.Size != int64(len(body)) {
		t.Fatalf("Stat: %+v %v", st, err)
	}
	if st, err := p.Stat(ctx, "a b"); err != nil || !st.IsDir {
		t.Fatalf("Stat dir: %+v %v", st, err)
	}
	if _, err := p.Stat(ctx, "a b/zzz"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Stat missing: %v", err)
	}
	rc, n, err := p.Open(ctx, "a b/c/file one.txt")
	if err != nil || n != int64(len(body)) {
		t.Fatalf("Open: %v %d", err, n)
	}
	// the shared connection stays usable while a download is in flight
	if _, err := p.Stat(ctx, "empty.txt"); err != nil {
		t.Fatalf("Stat during download: %v", err)
	}
	b, err := io.ReadAll(rc)
	if err != nil || string(b) != body {
		t.Fatalf("Open content %q %v", b, err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := p.Open(ctx, "a b"); err == nil {
		t.Fatal("opening a directory must fail")
	}
	if _, _, err := p.Open(ctx, "missing"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Open missing: %v", err)
	}
	if err := p.Remove(ctx, "a b/c"); err == nil {
		t.Fatal("folders are never removed")
	}
	if err := p.Remove(ctx, "a b/c/file one.txt"); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(ctx, "a b/c/file one.txt"); !errors.Is(err, cloud.ErrNotFound) {
		t.Fatalf("Remove missing: %v", err)
	}
	for _, bad := range []string{"../x", "/etc", `a\b`, "a//b", "."} {
		if _, err := p.List(ctx, bad); err == nil {
			t.Errorf("List %q accepted", bad)
		}
		if err := p.PutNew(ctx, bad, strings.NewReader("x"), 1); err == nil {
			t.Errorf("PutNew %q accepted", bad)
		}
	}
	r := cloud.Probe(ctx, p, true, ".oa-agent-test-1")
	if !r.OK || len(r.Steps) != 2 {
		t.Fatalf("probe: %+v", r)
	}
	if ok, _ := afero.Exists(s.fs, "/.oa-agent-test-1"); ok {
		t.Fatal("marker left behind")
	}
}

func TestPlainUserPassword(t *testing.T) {
	s := start(t, ftpserver.ClearOrEncrypted, false)
	behaviour(t, s, open(t, s.settings("none")))
}

func TestAnonymous(t *testing.T) {
	s := start(t, ftpserver.ClearOrEncrypted, false)
	set := s.settings("none")
	delete(set.Values, "username")
	delete(set.Secrets, "password")
	p := open(t, set)
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
}

func TestWrongPassword(t *testing.T) {
	s := start(t, ftpserver.ClearOrEncrypted, false)
	set := s.settings("none")
	set.Secrets["password"] = "bad"
	if _, err := cloud.Open(context.Background(), "ftp", set); !errors.Is(err, cloud.ErrAuth) {
		t.Fatalf("wrong password: %v", err)
	}
}

func TestExplicitTLS(t *testing.T) {
	s := start(t, ftpserver.MandatoryEncryption, true)
	behaviour(t, s, open(t, s.settings("explicit")))
	// certificate verification is on by default: the self-signed cert must be refused
	set := s.settings("explicit")
	delete(set.Values, "insecure_skip_verify")
	if _, err := cloud.Open(context.Background(), "ftp", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("self-signed without skip: %v", err)
	}
	// a server that requires TLS refuses plain logins
	if _, err := cloud.Open(context.Background(), "ftp", s.settings("none")); err == nil {
		t.Fatal("plain login on a TLS-only server must fail")
	}
}

func TestImplicitTLS(t *testing.T) {
	s := start(t, ftpserver.ImplicitEncryption, true)
	behaviour(t, s, open(t, s.settings("implicit")))
	set := s.settings("implicit")
	delete(set.Values, "insecure_skip_verify")
	if _, err := cloud.Open(context.Background(), "ftp", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("self-signed without skip: %v", err)
	}
}

func TestReconnectAfterServerDrop(t *testing.T) {
	s := start(t, ftpserver.ClearOrEncrypted, false)
	p := open(t, s.settings("none")).(*provider)
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.sess.kill() // simulate the server dropping the idle connection
	p.mu.Unlock()
	if _, err := p.List(context.Background(), ""); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
}

// hostileServer is a minimal FTP server whose PASV reply points at the metadata address. A correct client ignores
// the announced IP and talks to the control peer only.
func hostileServer(t *testing.T, announce string) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				w := func(f string, a ...any) { fmt.Fprintf(c, f+"\r\n", a...) }
				w("220 hostile")
				br := bufio.NewReader(c)
				var data net.Listener
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					cmd := strings.ToUpper(strings.Fields(line + " ")[0])
					switch cmd {
					case "USER":
						w("331 pass")
					case "PASS":
						w("230 ok")
					case "FEAT":
						w("211-Features:\r\n UTF8\r\n211 End")
					case "OPTS", "TYPE":
						w("200 ok")
					case "EPSV":
						w("500 no")
					case "PASV":
						data, _ = net.Listen("tcp", "127.0.0.1:0")
						p := data.Addr().(*net.TCPAddr).Port
						w("227 Entering Passive Mode (%s,%d,%d)", strings.ReplaceAll(announce, ".", ","), p/256, p%256)
					case "LIST":
						w("150 here")
						if dc, err := data.Accept(); err == nil {
							fmt.Fprintf(dc, "-rw-r--r--   1 u g 3 Jan 01 2024 x.txt\r\n")
							dc.Close()
						}
						data.Close()
						w("226 done")
					case "QUIT":
						w("221 bye")
						return
					default:
						w("500 ?")
					}
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestPasvIPIgnored(t *testing.T) {
	port := hostileServer(t, "169.254.169.254")
	set := cloud.Settings{Values: map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(port), "tls": "none"}}
	p := open(t, set)
	l, err := p.List(context.Background(), "")
	if err != nil || len(l) != 1 || l[0].Name != "x.txt" {
		t.Fatalf("PASV with a hostile IP must still use the control peer: %v %v", l, err)
	}
}

func TestSafeDialerRefusesMetadata(t *testing.T) {
	set := cloud.Settings{Values: map[string]string{"host": "169.254.169.254", "tls": "none"}}
	var ve *cloud.ValidationError
	if _, err := cloud.Open(context.Background(), "ftp", set); !errors.As(err, &ve) {
		t.Fatalf("CheckHost should refuse: %v", err)
	}
	if _, err := cloud.SafeDialer(time.Second).DialContext(context.Background(), "tcp", "169.254.169.254:21"); err == nil {
		t.Fatal("dialer must refuse")
	}
}

func TestUnreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	set := cloud.Settings{Values: map[string]string{"host": "127.0.0.1", "port": strconv.Itoa(port), "tls": "none"}}
	if _, err := cloud.Open(context.Background(), "ftp", set); !errors.Is(err, cloud.ErrUnreachable) {
		t.Fatalf("closed port: %v", err)
	}
}

func TestValidate(t *testing.T) {
	if _, _, err := cloud.Validate("ftp", map[string]string{"host": "h"}, nil, nil); err != nil {
		t.Fatalf("valid minimal: %v", err)
	}
	if v, _, err := cloud.Validate("ftp", map[string]string{"host": "h", "tls": "implicit", "port": "990"}, map[string]string{"password": "p"}, nil); err != nil || v["tls"] != "implicit" {
		t.Fatalf("valid full: %v", err)
	}
	for name, v := range map[string]map[string]string{
		"no host":  {},
		"bad tls":  {"host": "h", "tls": "maybe"},
		"bad port": {"host": "h", "port": "70000"},
		"unknown":  {"host": "h", "x": "1"},
	} {
		if _, _, err := cloud.Validate("ftp", v, nil, nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	k, _ := cloud.KindByID("ftp")
	if k.Group != "enterprise" {
		t.Fatal("group")
	}
}
