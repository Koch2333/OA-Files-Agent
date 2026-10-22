//go:build faultinject

package api

import (
	"errors"
	"net/http"

	"oa-agent/backend/internal/diag"
)

// Test-only fault hook, compiled in ONLY with `-tags faultinject` (the e2e driver's diagnostics part uses it; normal
// and release builds contain no such endpoint). Admin only through the central route table.
func init() {
	routeAccess["GET /v1/test/fault"] = adm
}

func (s *Server) faultRoutes() {
	s.handle("/v1/test/fault", map[string]http.HandlerFunc{"GET": func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("kind") {
		case "panic":
			panic("faultinject: deliberate handler panic")
		case "error":
			s.internal(w, r, "内部错误", errors.New("faultinject: deliberate internal error password=FAULTCANARY"))
		case "goroutine":
			diag.Go("api.faultinject", func() { panic("faultinject: deliberate goroutine panic") })
			s.ok(w, 202, map[string]any{"started": true})
		default:
			s.ok(w, 200, map[string]any{"faultinject": true})
		}
	}})
}
