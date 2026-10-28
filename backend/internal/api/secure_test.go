package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsSecureBehindATerminatingProxy(t *testing.T) {
	req := func(xfp string) *http.Request {
		r := httptest.NewRequest("GET", "http://origin.internal/setup", nil)
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		return r
	}
	for name, tc := range map[string]struct {
		trusted bool
		public  string
		xfp     string
		want    bool
	}{
		"no proxy, plain http":                           {false, "", "", false},
		"no proxy, https public url is not enough":       {false, "https://oa.example.com", "", false},
		"no proxy, header ignored":                       {false, "", "https", false},
		"trusted proxy sends https":                      {true, "", "https", true},
		"trusted proxy sends http":                       {true, "https://oa.example.com", "http", false},
		"CDN without the header, https public url":       {true, "https://oa.example.com", "", true},
		"CDN without the header, http public url":        {true, "http://1.2.3.4:8080", "", false},
		"CDN without the header, no public url":          {true, "", "", false},
		"public url scheme is compared case-insensitive": {true, "HTTPS://OA.example.com", "", true},
	} {
		s := &Server{d: Deps{TrustedProxy: tc.trusted, PublicURL: tc.public}}
		if got := s.isSecure(req(tc.xfp)); got != tc.want {
			t.Errorf("%s: isSecure = %v, want %v", name, got, tc.want)
		}
	}
}
