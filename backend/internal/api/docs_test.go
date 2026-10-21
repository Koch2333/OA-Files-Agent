package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOfflineDocs(t *testing.T) {
	for _, path := range []string{"/docs", "/openapi.yaml"} {
		w := httptest.NewRecorder()
		New().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s status %d", path, w.Code)
		}
		if path == "/docs" {
			if !strings.Contains(w.Body.String(), `href="/openapi.yaml"`) || !strings.Contains(w.Body.String(), "openapi: 3.1.0") {
				t.Fatal("missing contract docs")
			}
			if strings.Contains(w.Body.String(), "<script") {
				t.Fatal("offline docs should not load scripts")
			}
		} else if w.Body.String() != string(openAPI) {
			t.Fatal("served contract differs from embedded source")
		}
		w = httptest.NewRecorder()
		New().ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 405 {
			t.Fatalf("%s POST status %d", path, w.Code)
		}
	}
}
