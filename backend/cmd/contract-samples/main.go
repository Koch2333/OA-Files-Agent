// Contract-samples exercises the actual HTTP handler without opening network ports.
// Dependency probes are deterministic stand-ins; these are not database integration tests.
package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"

	"oa-agent/backend/internal/api"
)

type sample struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    json.RawMessage   `json:"body"`
}

func main() {
	var samples []sample
	for _, tc := range []struct{ method, path, database, worker string }{
		{"GET", "/healthz", "ok", "ok"},
		{"GET", "/readyz", "ok", "ok"},
		{"GET", "/readyz", "not_configured", "not_configured"},
		{"GET", "/readyz", "unavailable", "unauthorized"},
		{"GET", "/readyz", "migration_required", "invalid_response"},
		{"GET", "/readyz", "timeout", "timeout"},
		{"GET", "/readyz", "ok", "unavailable"},
		{"POST", "/healthz", "ok", "ok"},
		{"POST", "/readyz", "ok", "ok"},
		{"GET", "/missing", "ok", "ok"},
	} {
		w := httptest.NewRecorder()
		api.New(func(context.Context) string { return tc.database }, func(context.Context) string { return tc.worker }).ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		samples = append(samples, sample{tc.method, tc.path, w.Code, headers, w.Body.Bytes()})
	}
	if err := json.NewEncoder(os.Stdout).Encode(samples); err != nil {
		panic(err)
	}
}
