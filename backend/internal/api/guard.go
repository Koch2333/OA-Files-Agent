package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// guard protects a local-only service from web pages in the user's browser:
//   - cross-site state-changing requests are refused (Origin / Sec-Fetch-Site must be same-origin);
//   - when listening on loopback, the Host header must be a loopback name (DNS rebinding).
func (s *Server) guard(w http.ResponseWriter, r *http.Request) bool {
	if s.d.StrictLocal {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.Trim(host, "[]"))
		ok := host == "localhost" || host == "127.0.0.1" || host == "::1"
		for _, a := range s.d.AllowedHosts {
			if strings.EqualFold(a, host) {
				ok = true
			}
		}
		if !ok {
			s.fail(w, r, 403, "HOST_NOT_ALLOWED", "不允许通过该主机名访问", nil)
			return false
		}
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if s.corsAllowed(r) {
		return true // an origin the operator explicitly allowed (OA_CORS_ORIGINS)
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		s.fail(w, r, 403, "CROSS_ORIGIN", "不允许跨站请求", nil)
		return false
	}
	if o := r.Header.Get("Origin"); o != "" && o != "null" {
		u, err := url.Parse(o)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			s.fail(w, r, 403, "CROSS_ORIGIN", "不允许跨站请求", nil)
			return false
		}
	} else if o == "null" {
		s.fail(w, r, 403, "CROSS_ORIGIN", "不允许跨站请求", nil)
		return false
	}
	return true
}

func isLoopbackRequest(r *http.Request) bool {
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}
