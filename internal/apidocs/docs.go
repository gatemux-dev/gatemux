// Package apidocs serves an offline-capable API explorer and versioned OpenAPI.
package apidocs

import (
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
)

//go:embed assets/*
var assets embed.FS

var pathParameter = regexp.MustCompile(`\{([^}]+)\}`)

// Mount snapshots registered API routes. Call after registering handlers and
// before the SPA wildcard. No request, credential or deployment data is included.
func Mount(r chi.Router) error {
	spec, err := Specification(r)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	serveSpec := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(encoded)
	}
	r.Get("/openapi/v1.json", serveSpec)
	r.Get("/openapi.json", serveSpec)
	for route, file := range map[string]string{"/docs": "index.html", "/docs/": "index.html", "/docs/explorer.js": "explorer.js", "/docs/style.css": "style.css"} {
		body, err := assets.ReadFile("assets/" + file)
		if err != nil {
			return err
		}
		contentType := "text/html; charset=utf-8"
		if strings.HasSuffix(file, ".js") {
			contentType = "text/javascript; charset=utf-8"
		} else if strings.HasSuffix(file, ".css") {
			contentType = "text/css; charset=utf-8"
		}
		r.Get(route, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", contentType)
			_, _ = w.Write(body)
		})
	}
	return nil
}

func Specification(r chi.Router) (map[string]any, error) {
	paths := map[string]any{}
	err := chi.Walk(r, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, "*") {
			return nil
		} // opaque operator proxies and SPA are not API contracts
		method = strings.ToLower(method)
		if method == "head" || method == "options" {
			return nil
		}
		tag, security := "Public", []any{}
		if strings.HasPrefix(route, "/v1/") {
			tag = "Inference"
			security = []any{map[string]any{"VirtualKey": []string{}}}
		}
		if strings.HasPrefix(route, "/admin/") || strings.HasPrefix(route, "/me/") || route == "/auth/me" || route == "/auth/logout" || route == "/health/drain" {
			tag = "Control plane"
			security = []any{map[string]any{"SessionCookie": []string{}}, map[string]any{"AdminOrSessionBearer": []string{}}}
		}
		description := "Route registered by this gateway. Control-plane authorization remains role- and team-scoped. Generic schemas are permissive inventories, not a promise that every possible field is accepted."
		operation := map[string]any{"operationId": method + "_" + strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(strings.TrimPrefix(route, "/")), "summary": strings.ToUpper(method) + " " + route, "tags": []string{tag}, "security": security, "description": description}
		parameters := []any{}
		for _, match := range pathParameter.FindAllStringSubmatch(route, -1) {
			parameters = append(parameters, map[string]any{"name": match[1], "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
		if method == "get" && (strings.HasPrefix(route, "/admin/") || strings.HasPrefix(route, "/me/")) {
			for _, name := range []string{"limit", "offset"} {
				parameters = append(parameters, map[string]any{"name": name, "in": "query", "schema": map[string]any{"type": "integer", "minimum": 0}, "description": "Used by collection endpoints that support pagination."})
			}
		}
		operation["parameters"] = parameters
		responses := map[string]any{"2XX": map[string]any{"description": "Success; shape and status depend on the operation.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{}}}}, "default": map[string]any{"description": "Authentication, validation, policy or upstream error.", "content": map[string]any{"application/json": map[string]any{"schema": ref("Error")}}}}
		if method == "post" || method == "put" || method == "patch" {
			schema := map[string]any{"type": "object", "additionalProperties": true}
			media := "application/json"
			switch route {
			case "/v1/chat/completions":
				schema = ref("ChatRequest")
			case "/v1/embeddings":
				schema = ref("EmbeddingRequest")
			case "/v1/responses":
				schema = ref("ResponseRequest")
			case "/v1/audio/transcriptions", "/v1/audio/translations":
				media = "multipart/form-data"
				schema = map[string]any{"type": "object", "required": []string{"model", "file"}, "properties": map[string]any{"model": map[string]any{"type": "string"}, "file": map[string]any{"type": "string", "format": "binary"}}}
			}
			operation["requestBody"] = map[string]any{"required": strings.HasPrefix(route, "/v1/"), "content": map[string]any{media: map[string]any{"schema": schema}}}
		}
		if route == "/v1/chat/completions" || route == "/v1/responses" {
			responses["200"] = map[string]any{"description": "JSON response or named SSE events when stream=true. Streams remain charged/admitted until terminal event or disconnect.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": true}}, "text/event-stream": map[string]any{"schema": map[string]any{"type": "string"}}}}
			delete(responses, "2XX")
		}
		operation["responses"] = responses
		if route == "/admin/keys/{id}/budget" {
			operation["description"] = "Admin or owning-team manager only. Key cap uses the team's UTC day/month window. Null or zero means no key-specific cap; other scope budgets remain enforced. Changing the cap does not reset usage."
			if method == "patch" {
				operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "additionalProperties": false, "required": []string{"usd_limit_cents"}, "properties": map[string]any{"usd_limit_cents": map[string]any{"type": []string{"integer", "null"}, "minimum": 0, "maximum": 9007199254740991}}}}}}
				delete(responses, "2XX")
				responses["204"] = map[string]any{"description": "Cap updated; no response body. Revoked keys cannot be edited."}
			}
			if method == "get" {
				delete(responses, "2XX")
				responses["200"] = map[string]any{"description": "Current cap, UTC window and used_cents including reserved estimates and settled/earlier usage.", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "required": []string{"period", "window_start", "window_end", "used_cents"}, "properties": map[string]any{"limit_cents": map[string]any{"type": "integer"}, "used_cents": map[string]any{"type": "integer"}, "period": map[string]any{"type": "string", "enum": []string{"day", "month"}}, "window_start": map[string]any{"type": "string", "format": "date-time"}, "window_end": map[string]any{"type": "string", "format": "date-time"}}}}}}
			}
		}
		if route == "/healthz" || route == "/readyz" {
			operation["responses"] = map[string]any{"200": map[string]any{"description": "Healthy or ready", "content": map[string]any{"text/plain": map[string]any{"schema": map[string]any{"type": "string"}}}}, "503": map[string]any{"description": "Not ready"}}
		}
		if route == "/health/drain" {
			operation["description"] = "Administrator-only irreversible drain for this process: readiness returns 503 and new inference is rejected; existing requests may finish. Does not stop the process. Send SIGTERM after routing removal. No request body is required."
			delete(operation, "requestBody")
		}
		item, ok := paths[route].(map[string]any)
		if !ok {
			item = map[string]any{}
			paths[route] = item
		}
		item[method] = operation
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk API routes: %w", err)
	}
	return map[string]any{"openapi": "3.1.0", "info": map[string]any{"title": "GateMux Gateway API", "version": "1.0.0", "description": "Versioned v1 inference contract and control-plane route inventory. Native provider capabilities and experimental endpoint limits are documented in the repository supported-surface contract."}, "servers": []any{map[string]any{"url": "/"}}, "paths": paths, "components": map[string]any{"schemas": schemas(), "securitySchemes": map[string]any{"VirtualKey": map[string]any{"type": "http", "scheme": "bearer", "description": "Team-owned GateMux virtual key. Never send the upstream provider key."}, "AdminOrSessionBearer": map[string]any{"type": "http", "scheme": "bearer", "description": "Admin master key or session token; RBAC still applies."}, "SessionCookie": map[string]any{"type": "apiKey", "in": "cookie", "name": "gatemux_session"}}}}, nil
}

func ref(name string) map[string]any { return map[string]any{"$ref": "#/components/schemas/" + name} }

func schemas() map[string]any {
	text := map[string]any{"type": "string", "minLength": 1}
	tokens := map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "integer", "minimum": 0}}
	return map[string]any{
		"Error":            map[string]any{"type": "object", "required": []string{"error"}, "properties": map[string]any{"error": map[string]any{"type": "object", "properties": map[string]any{"message": text, "type": text}}}},
		"ChatRequest":      map[string]any{"type": "object", "required": []string{"model", "messages"}, "additionalProperties": true, "properties": map[string]any{"model": text, "messages": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "object", "required": []string{"role"}, "additionalProperties": true, "properties": map[string]any{"role": text, "content": map[string]any{"type": []string{"string", "array", "null"}}}}}, "stream": map[string]any{"type": "boolean", "default": false}, "max_tokens": map[string]any{"type": "integer", "minimum": 1}, "user": map[string]any{"type": "string"}}},
		"EmbeddingRequest": map[string]any{"type": "object", "required": []string{"model", "input"}, "additionalProperties": true, "properties": map[string]any{"model": text, "input": map[string]any{"oneOf": []any{text, map[string]any{"type": "array", "minItems": 1, "items": text}, tokens, map[string]any{"type": "array", "minItems": 1, "items": tokens}}}, "dimensions": map[string]any{"type": "integer", "minimum": 1}, "encoding_format": map[string]any{"enum": []string{"float", "base64"}}, "user": map[string]any{"type": "string"}}},
		"ResponseRequest":  map[string]any{"type": "object", "required": []string{"model", "input"}, "additionalProperties": true, "properties": map[string]any{"model": text, "input": map[string]any{"type": []string{"string", "array"}, "minLength": 1, "minItems": 1}, "stream": map[string]any{"type": "boolean", "default": false}, "store": map[string]any{"type": []string{"boolean", "null"}, "default": true}, "previous_response_id": map[string]any{"type": []string{"string", "null"}}, "max_output_tokens": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000000, "default": 1024}}},
	}
}
