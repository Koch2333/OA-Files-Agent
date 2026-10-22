package api

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"oa-agent/backend/internal/buildinfo"
	"oa-agent/backend/internal/logx"
)

// statusWriter records the response status for debug request logs. It keeps Flush (SSE) and Unwrap working.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += int64(n)
	return n, err
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

// debugRequests logs method, path (never the query string or headers), status, size and duration in debug builds.
func debugRequests(next func(http.ResponseWriter, *http.Request)) func(http.ResponseWriter, *http.Request) {
	if !buildinfo.Debug {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		logx.Debugf("http %s %s -> %d %dB %s rid=%s", r.Method, redactPath(r.URL.Path), sw.status, sw.bytes, time.Since(start).Round(time.Millisecond), w.Header().Get("X-Request-ID"))
	}
}

// redactPath hides bearer-like path segments (invitation tokens) from debug request logs.
func redactPath(p string) string {
	const pre = "/v1/auth/invitations/"
	if !strings.HasPrefix(p, pre) {
		return p
	}
	rest := p[len(pre):]
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return pre + "{token}" + rest[i:]
	}
	return pre + "{token}"
}
