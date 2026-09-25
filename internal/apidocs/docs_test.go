package apidocs

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSpecAndOfflineExplorer(t *testing.T) {
	r := chi.NewRouter()
	noop := func(http.ResponseWriter, *http.Request) {}
	r.Post("/v1/chat/completions", noop)
	r.Post("/v1/embeddings", noop)
	r.Get("/admin/teams/{slug}", noop)
	r.Post("/health/drain", noop)
	if err := Mount(r); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/openapi/v1.json", nil))
	var spec map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &spec); err != nil {
		t.Fatal(err)
	}
	if spec["openapi"] != "3.1.0" {
		t.Fatal("wrong spec version")
	}
	paths := spec["paths"].(map[string]any)
	drain := paths["/health/drain"].(map[string]any)["post"].(map[string]any)
	if len(drain["security"].([]any)) != 2 || drain["requestBody"] != nil || !strings.Contains(drain["description"].(string), "Administrator-only") {
		t.Fatal("drain contract must require admin credentials without a body")
	}
	if paths["/v1/embeddings"] == nil || paths["/admin/teams/{slug}"] == nil || paths["/v1/responses"] != nil {
		t.Fatal("spec not tied to registered routes")
	}
	for _, path := range []string{"/docs", "/docs/explorer.js", "/docs/style.css"} {
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Body.Len() == 0 {
			t.Fatalf("missing asset %s", path)
		}
		if strings.Contains(w.Body.String(), "https://") {
			t.Fatal("docs depend on external assets")
		}
	}
}
