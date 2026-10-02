package apidocs

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestKeyBudgetContract(t *testing.T) {
	r := chi.NewRouter()
	noop := func(http.ResponseWriter, *http.Request) {}
	r.Get("/admin/keys/{id}/budget", noop)
	r.Patch("/admin/keys/{id}/budget", noop)
	spec, err := Specification(r)
	if err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	path := paths["/admin/keys/{id}/budget"].(map[string]any)
	patch := path["patch"].(map[string]any)
	if patch["responses"].(map[string]any)["204"] == nil || patch["requestBody"].(map[string]any)["required"] != true {
		t.Fatal("missing PATCH contract")
	}
	b, _ := json.Marshal(patch)
	for _, field := range []string{`"required":["usd_limit_cents"]`, `"additionalProperties":false`, `"maximum":9007199254740991`, `"minimum":0`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("missing schema constraint %s", field)
		}
	}
	get := path["get"].(map[string]any)
	if get["responses"].(map[string]any)["200"] == nil || get["requestBody"] != nil {
		t.Fatal("missing GET contract")
	}
	schema := get["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	exact, _ := schema["properties"].(map[string]any)["used_microcents"].(map[string]any)
	if exact["type"] != "string" || exact["pattern"] != "^(0|[1-9][0-9]{0,18})$" {
		t.Fatalf("used_microcents schema: %v", exact)
	}
	if b, _ := json.Marshal(schema["required"]); string(b) != `["period","window_start","window_end","used_cents"]` {
		t.Fatalf("required changed: %s", b)
	}
}
