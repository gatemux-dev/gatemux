package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/router"
)

func TestAdminDeploymentConcurrencyRoundTripAndClear(t *testing.T) {
	e := newTestEnv(t)
	name := "concurrency-dep-" + randHex(4)

	code, body := e.POST("/admin/deployments", e.MasterKey, map[string]any{
		"name":                  name,
		"provider_type":         "openai_compatible",
		"upstream_model":        "local-model",
		"base_url":              "http://127.0.0.1:1/v1",
		"max_parallel_requests": 17,
		"supports_chat":         true,
		"supports_stream_chat":  true,
	})
	if code != http.StatusCreated {
		t.Fatalf("create got %d, want 201 (body=%s)", code, body)
	}
	var created DeploymentResponse
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.MaxParallelRequests == nil || *created.MaxParallelRequests != 17 {
		t.Fatalf("created max_parallel_requests = %v, want 17", created.MaxParallelRequests)
	}

	code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{
		"max_parallel_requests": 0,
	})
	if code != http.StatusOK {
		t.Fatalf("clear got %d, want 200 (body=%s)", code, body)
	}
	var cleared DeploymentResponse
	if err := json.Unmarshal(body, &cleared); err != nil {
		t.Fatalf("decode clear response: %v", err)
	}
	if cleared.MaxParallelRequests != nil {
		t.Fatalf("cleared max_parallel_requests = %v, want nil/unlimited", cleared.MaxParallelRequests)
	}
	if cleared.BaseURL == nil || *cleared.BaseURL != "http://127.0.0.1:1/v1" {
		t.Fatalf("max-only patch changed base_url: %v", cleared.BaseURL)
	}
	if cleared.Capabilities == nil || !cleared.Capabilities.Chat || !cleared.Capabilities.StreamChat {
		t.Fatalf("max-only patch changed capabilities: %+v", cleared.Capabilities)
	}

	code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{
		"max_parallel_requests": -1,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("negative limit got %d, want 400 (body=%s)", code, body)
	}
}

func TestChatSaturatedPrimaryFallsBack(t *testing.T) {
	f := newChatFixture(t)
	maxParallel := 1
	baseURL := f.BaseURL
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", f.UpModel, f.CredEnv, &baseURL, nil, nil, &maxParallel); err != nil {
		t.Fatalf("limit primary: %v", err)
	}

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": "cmpl-fallback", "object": "chat.completion", "model": "fallback-upstream",
			"choices": []map[string]any{{
				"index": 0, "message": map[string]any{"role": "assistant", "content": "from-fallback"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
	}))
	t.Cleanup(fallback.Close)
	fallbackName := "fallback-" + randHex(4)
	fallbackURL := fallback.URL + "/v1"
	if _, err := f.Store.UpsertDeployment(context.Background(), fallbackName, "openai", "fallback-upstream", f.CredEnv, &fallbackURL, nil, nil, nil); err != nil {
		t.Fatalf("create fallback: %v", err)
	}
	if _, err := f.Store.UpsertAlias(context.Background(), f.Alias, []string{f.DepName, fallbackName}); err != nil {
		t.Fatalf("update alias: %v", err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh registry: %v", err)
	}

	held, _, ok := f.Registry.TryAcquireDeployment(f.DepName)
	if !ok {
		t.Fatal("could not reserve primary deployment")
	}
	defer held.Release()

	team := f.createTeam("fallback")
	rawKey := f.issueKey(team, nil)
	code, body := f.chatPOST(rawKey)
	if code != http.StatusOK {
		t.Fatalf("chat got %d, want 200 (body=%s)", code, body)
	}
	if !strings.Contains(string(body), "from-fallback") {
		t.Fatalf("response did not come from fallback: %s", body)
	}
}

func TestChatAllDeploymentsSaturatedReturnsOverload(t *testing.T) {
	f := newChatFixture(t)
	maxParallel := 1
	baseURL := f.BaseURL
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", f.UpModel, f.CredEnv, &baseURL, nil, nil, &maxParallel); err != nil {
		t.Fatalf("limit deployment: %v", err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh registry: %v", err)
	}
	held, _, ok := f.Registry.TryAcquireDeployment(f.DepName)
	if !ok {
		t.Fatal("could not reserve deployment")
	}
	defer held.Release()

	team := f.createTeam("saturated")
	rawKey := f.issueKey(team, nil)
	reqBody, _ := json.Marshal(map[string]any{
		"model":    f.Alias,
		"messages": []map[string]string{{"role": "user", "content": "hello"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	f.Router.ServeHTTP(rec, req)
	body := rec.Body.Bytes()
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("chat got %d, want 503 (body=%s)", rec.Code, body)
	}
	if got := readErrorType(body); got != "server_overloaded" {
		t.Fatalf("error.type = %q, want server_overloaded (body=%s)", got, body)
	}
	if got := rec.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
}

func TestOpenStreamPermitLivesUntilCallerReleases(t *testing.T) {
	f := newChatFixture(t)
	maxParallel := 1
	baseURL := f.BaseURL
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", f.UpModel, f.CredEnv, &baseURL, nil, nil, &maxParallel); err != nil {
		t.Fatalf("limit deployment: %v", err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh registry: %v", err)
	}
	h := &V1Handler{Router: f.Registry}
	_, body, permit, err := h.openStreamWithFallback(context.Background(), f.Alias, router.ResolveContext{}, func(context.Context, *router.Resolved) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("data: [DONE]\n\n")), nil
	})
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	defer body.Close()
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 1 {
		t.Fatalf("state before caller release = %+v, want in_flight=1", state)
	}
	h.releaseUpstreamPermit(permit)
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatalf("state after caller release = %+v, want in_flight=0", state)
	}
}
