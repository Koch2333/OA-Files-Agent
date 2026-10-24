package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"oa-agent/backend/internal/llm"
	"oa-agent/backend/internal/store"
)

type fakeOAuth struct {
	stubLLM
	res llm.OAuthCallbackResult
	got [3]string
}

func (f *fakeOAuth) OAuthCallback(state, code, errParam string) llm.OAuthCallbackResult {
	f.got = [3]string{state, code, errParam}
	return f.res
}

func TestOAuthCallbackRedirectsAndNeverEchoesInput(t *testing.T) {
	f := &fakeOAuth{res: llm.OAuthCallbackResult{Redirect: true, OK: true}}
	s := newServer(Deps{LLM: f, Store: &store.Store{}})
	do := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.serve(w, httptest.NewRequest("GET", target, nil))
		return w
	}
	w := do("/v1/llm/oauth/callback?state=abc&code=xyz")
	if w.Code != 302 || w.Header().Get("Location") != "/settings?llm_oauth=ok" || w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	if f.got != [3]string{"abc", "xyz", ""} {
		t.Fatalf("%v", f.got)
	}
	f.res = llm.OAuthCallbackResult{Redirect: true, Reason: "used"}
	if w := do("/v1/llm/oauth/callback?state=abc&code=xyz"); w.Header().Get("Location") != "/settings?llm_oauth=used" {
		t.Fatal(w.Header())
	}
	f.res = llm.OAuthCallbackResult{Redirect: false, OK: true}
	w = do("/v1/llm/oauth/callback?state=%3Cscript%3E&code=%3Cb%3E")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "授权已完成") || strings.Contains(w.Body.String(), "<script>") || !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	f.res = llm.OAuthCallbackResult{Reason: "denied"}
	if w := do("/v1/llm/oauth/callback?error=access_denied"); !strings.Contains(w.Body.String(), "授权未完成") {
		t.Fatal(w.Body.String())
	}
}

func TestOAuthCallbackURL(t *testing.T) {
	req := func(host string) *http.Request {
		r := httptest.NewRequest("POST", "/v1/llm/oauth/start", nil)
		r.Host = host
		return r
	}
	s := &Server{} // no Auth: unit-test/local shape
	if u, ok := s.oauthCallbackURL(req("127.0.0.1:18081")); !ok || u != "http://127.0.0.1:18081/v1/llm/oauth/callback" {
		t.Fatalf("%q %v", u, ok)
	}
	if u, ok := s.oauthCallbackURL(req("localhost:8080")); !ok || !strings.HasPrefix(u, "http://localhost:8080/") {
		t.Fatalf("%q %v", u, ok)
	}
	// a forged Host never becomes the callback; without OA_PUBLIC_URL there is none
	if _, ok := s.oauthCallbackURL(req("evil.example")); ok {
		t.Fatal("remote host must not be trusted")
	}
	s.d.PublicURL = "https://oa.example.com/"
	if u, ok := s.oauthCallbackURL(req("evil.example")); !ok || u != "https://oa.example.com/v1/llm/oauth/callback" {
		t.Fatalf("%q %v", u, ok)
	}
}
