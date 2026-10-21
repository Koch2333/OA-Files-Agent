package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestReadinessReportsDatabaseState(t *testing.T) {
	for _, state := range []string{"ok", "unavailable", "migration_required"} {
		t.Run(state, func(t *testing.T) {
			w := httptest.NewRecorder()
			New(func(ctx context.Context) string {
				if _, ok := ctx.Deadline(); !ok {
					t.Error("missing dependency timeout")
				}
				return state
			}).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
			var body struct {
				Data struct {
					Dependencies map[string]string `json:"dependencies"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != 503 || body.Data.Dependencies["database"] != state || body.Data.Dependencies["worker"] != "not_configured" {
				t.Fatalf("unexpected readiness: %s", w.Body.String())
			}
		})
	}
}

func TestOverallReadiness(t *testing.T) {
	for _, tc := range []struct {
		database, worker string
		code             int
	}{
		{"ok", "ok", 200}, {"unavailable", "ok", 503}, {"ok", "unauthorized", 503}, {"ok", "timeout", 503}, {"ok", "invalid_response", 503},
	} {
		t.Run(tc.database+"/"+tc.worker, func(t *testing.T) {
			w := httptest.NewRecorder()
			New(func(context.Context) string { return tc.database }, func(context.Context) string { return tc.worker }).ServeHTTP(w, httptest.NewRequest("GET", "/readyz", nil))
			if w.Code != tc.code {
				t.Fatalf("status %d, want %d", w.Code, tc.code)
			}
		})
	}
}

func TestEndpoints(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"GET", "/healthz", 200}, {"GET", "/readyz", 503}, {"POST", "/healthz", 405}, {"GET", "/v1/imports", 404},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			New().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.status {
				t.Fatalf("status = %d, want %d", w.Code, tc.status)
			}
			if w.Header().Get("X-Request-ID") == "" {
				t.Fatal("missing request ID")
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
		})
	}
}
