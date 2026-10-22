package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"strings"
)

var (
	setupTokenFlag = flag.String("setup-token", "", "setup token, or the path of the API log file that contains the line `SETUP TOKEN: <token>`")
	adminUserFlag  = flag.String("admin-user", "e2eadmin", "administrator created by the wizard (or used to log in when already initialised)")
	adminPassFlag  = flag.String("admin-password", "Tr1cky-Harbor-Lantern-77", "password of that administrator")
	noAuthFlag     = flag.Bool("no-auth", false, "do not log in (server without authentication)")

	csrfToken string // sent as X-CSRF-Token on every non-GET request of the shared client
)

// csrfTransport adds the CSRF header to unsafe requests made through http.DefaultClient.
type csrfTransport struct{ next http.RoundTripper }

func (t csrfTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	switch r.Method {
	case "GET", "HEAD", "OPTIONS":
	default:
		if csrfToken != "" && r.Header.Get("X-CSRF-Token") == "" {
			r = r.Clone(r.Context())
			r.Header.Set("X-CSRF-Token", csrfToken)
		}
	}
	return t.next.RoundTrip(r)
}

func readSetupToken() string {
	v := strings.TrimSpace(*setupTokenFlag)
	if v == "" {
		return ""
	}
	if b, err := os.ReadFile(v); err == nil {
		ms := regexp.MustCompile(`SETUP TOKEN: ([A-Z2-7]+)`).FindAllStringSubmatch(string(b), -1)
		if len(ms) == 0 {
			return ""
		}
		return ms[len(ms)-1][1] // the newest start wins
	}
	return v
}

// noteCSRF remembers the token of a login / setup response.
func noteCSRF(data json.RawMessage) {
	var d struct {
		CSRF string `json:"csrf_token"`
	}
	if json.Unmarshal(data, &d) == nil && d.CSRF != "" {
		csrfToken = d.CSRF
	}
}

// ensureLogin installs the cookie jar + CSRF header on the shared client, then runs the setup wizard when the
// system is fresh (using the token the server printed) or logs in as the administrator.
func ensureLogin() {
	if *noAuthFlag {
		return
	}
	jar, _ := cookiejar.New(nil)
	http.DefaultClient.Jar = jar
	http.DefaultClient.Transport = csrfTransport{http.DefaultTransport}
	st, d, _ := call("GET", "/v1/setup/status", nil, nil)
	var status struct {
		Initialized bool `json:"initialized"`
		Loopback    bool `json:"loopback"`
	}
	_ = json.Unmarshal(d, &status)
	if st != 200 {
		fmt.Println("cannot read /v1/setup/status:", st, string(d))
		os.Exit(2)
	}
	if !status.Initialized {
		body := map[string]any{"mode": "enterprise", "mfa_policy": "off", "setup_token": readSetupToken(),
			"admin": map[string]any{"username": *adminUserFlag, "display_name": "E2E 管理员", "password": *adminPassFlag}}
		if u, err := url.Parse(*base); err == nil && u.Hostname() == "localhost" {
			body["rp_id"], body["origins"] = "localhost", []string{u.Scheme + "://" + u.Host}
		}
		st, d, _ = call("POST", "/v1/setup/complete", body, nil)
		check("setup wizard completes with the printed token (201)", st == 201, st, string(d))
		noteCSRF(d)
		// the wizard is a server-side gate: the application stays closed until POST /v1/setup/finish
		st, d, _ = call("GET", "/v1/tasks", nil, nil)
		check("wizard gate: /v1/tasks before finish → 409 WIZARD_REQUIRED", st == 409 && strings.Contains(string(d), "WIZARD_REQUIRED"), st, string(d))
		st, d, _ = call("POST", "/v1/setup/finish", nil, nil)
		check("wizard finish (200, wizard_finished=true)", st == 200 && strings.Contains(string(d), `"wizard_finished":true`), st, string(d))
		st, d, _ = call("POST", "/v1/setup/finish", nil, nil)
		check("wizard finish is idempotent (200)", st == 200, st, string(d))
	} else {
		st, d, _ = call("POST", "/v1/auth/login", map[string]any{"username": *adminUserFlag, "password": *adminPassFlag}, nil)
		check("administrator login (200)", st == 200, st, string(d))
		noteCSRF(d)
	}
	if csrfToken == "" {
		fmt.Println("no session was established; cannot continue")
		os.Exit(2)
	}
}
