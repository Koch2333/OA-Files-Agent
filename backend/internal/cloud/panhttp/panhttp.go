// Package panhttp holds the small HTTP/spool helpers shared by the cloud-drive providers (aliyunpan, baidupan,
// pan123): a SafeDialer-based client, bounded body reads, ctx-aware sleeping and a temp-file spool that hashes
// the upload body (these services need MD5 values before the first byte can be sent).
package panhttp

import (
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"oa-agent/backend/internal/cloud"
)

// MaxJSON bounds every JSON API response.
const MaxJSON = 8 << 20

// BackoffBase is the first retry delay; tests lower it.
var BackoffBase = time.Second

// MaxAttempts is the number of attempts for rate-limited calls.
const MaxAttempts = 3

// Backoff returns the delay before retry number attempt (1-based).
func Backoff(attempt int) time.Duration { return BackoffBase << (attempt - 1) }

// NewClient returns a client that only dials through cloud.SafeDialer, follows at most 3 redirects and (like
// net/http) drops the Authorization header when a redirect leaves the original host.
func NewClient(timeout time.Duration) *http.Client {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           cloud.SafeDialer(10 * time.Second).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       60 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("too many redirects")
		}
		return nil
	}}
}

// Do performs a request and maps transport failures to cloud.ErrUnreachable (never echoing the URL, which may
// carry tokens in its query string).
func Do(ctx context.Context, hc *http.Client, req *http.Request) (*http.Response, error) {
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%w: request failed", cloud.ErrUnreachable)
	}
	return resp, nil
}

// ReadBody reads at most limit bytes and closes the body. Larger bodies are an error.
func ReadBody(resp *http.Response, limit int64) ([]byte, error) {
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read failed", cloud.ErrUnreachable)
	}
	if int64(len(b)) > limit {
		return nil, errors.New("response too large")
	}
	return b, nil
}

// Sleep waits d or until ctx ends.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// LoopbackHost reports whether host is localhost or a loopback IP (used to allow plain http only for tests).
func LoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// Spooled is the upload body written to a temp file, with its whole-file MD5 and per-block MD5s.
type Spooled struct {
	f         *os.File
	Size      int64
	MD5       string
	BlockMD5s []string
}

// Spool copies exactly size bytes of r to a temp file. blockSize > 0 also computes the MD5 of each block.
func Spool(r io.Reader, size, blockSize int64) (*Spooled, error) {
	f, err := os.CreateTemp("", "oa-pan-*")
	if err != nil {
		return nil, errors.New("cannot create temp file")
	}
	s := &Spooled{f: f, Size: size}
	fail := func(e error) (*Spooled, error) { s.Close(); return nil, e }
	whole := md5.New()
	var done int64
	for done < size {
		n := size - done
		if blockSize > 0 && n > blockSize {
			n = blockSize
		}
		blk := md5.New()
		w := io.MultiWriter(f, whole, blk)
		c, err := io.CopyN(w, r, n)
		done += c
		if err != nil {
			if errors.Is(err, io.EOF) {
				return fail(io.ErrUnexpectedEOF)
			}
			return fail(err)
		}
		if blockSize > 0 {
			s.BlockMD5s = append(s.BlockMD5s, hex.EncodeToString(blk.Sum(nil)))
		}
	}
	var extra [1]byte
	if n, _ := r.Read(extra[:]); n > 0 {
		return fail(errors.New("content longer than declared size"))
	}
	s.MD5 = hex.EncodeToString(whole.Sum(nil))
	return s, nil
}

// Section returns a reader over [off, off+n).
func (s *Spooled) Section(off, n int64) *io.SectionReader { return io.NewSectionReader(s.f, off, n) }

// MD5Range hashes [off, off+n).
func (s *Spooled) MD5Range(off, n int64) (string, error) {
	h := md5.New()
	if _, err := io.Copy(h, s.Section(off, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Close removes the temp file.
func (s *Spooled) Close() error {
	name := s.f.Name()
	s.f.Close()
	return os.Remove(name)
}
