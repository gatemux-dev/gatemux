package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestResponsesBindingDoesNotFollowDeploymentReconfiguration(t *testing.T) {
	f, calls, _ := newResponsesFixture(t)
	key := f.issueKey(f.createTeam("bound-target"), nil)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello"}`, f.Alias))
	var response struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 {
		t.Fatalf("create: %s", w.Body)
	}
	before := calls.Load()
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", "reconfigured-model", f.CredEnv, &f.BaseURL, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+response.ID, "")
	if w.Code != 503 || calls.Load() != before {
		t.Fatalf("binding followed reconfiguration: %d %s", w.Code, w.Body)
	}
}

func TestResponsesCapabilityAndBudgetDenyBeforeUpstream(t *testing.T) {
	f, calls, _ := newResponsesFixture(t)
	team := f.setTeamBudget(f.createTeam("response-deny"), 1)
	key := f.issueKey(team, nil)
	f.upsertPricing(1000000, 1000000)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi"}`, f.Alias))
	if w.Code != 403 || calls.Load() != 0 {
		t.Fatalf("budget: %d %s", w.Code, w.Body)
	}
	key = f.issueKey(f.createTeam("response-caps"), nil)
	f.setDeploymentCapabilities(&store.DeploymentCapabilities{Embeddings: true})
	w = responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi"}`, f.Alias))
	if w.Code != 503 || calls.Load() != 0 {
		t.Fatalf("embedding-only handled Responses: %d %s", w.Code, w.Body)
	}
}

func TestResponsesAcceptedStallDoesNotRetryOrRefund(t *testing.T) {
	f := newChatFixture(t)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	f.Handler.Streaming = config.StreamingConfig{FirstEventTimeout: 40 * time.Millisecond, IdleTimeout: 40 * time.Millisecond}
	f.upsertPricing(1000000, 1000000)
	key := f.issueKey(f.setTeamBudget(f.createTeam("response-stall"), 100000), nil)
	id := "response-stall-" + randHex(4)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hi","stream":true}`, f.Alias)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if w.Code != 504 || calls.Load() != 1 {
		t.Fatalf("stall: %d %s calls=%d", w.Code, w.Body, calls.Load())
	}
	var estimate, settled int64
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT estimated_cost_cents,settled_cost_cents FROM budget_reservations WHERE request_id=$1`, id).Scan(&estimate, &settled); err != nil || settled != estimate || settled == 0 {
		t.Fatalf("accepted request refunded: %d/%d %v", settled, estimate, err)
	}
}

func TestAdminResponsesCapabilityPreservesExistingPolicy(t *testing.T) {
	e := newTestEnv(t)
	name := "responses-cap-" + randHex(4)
	code, body := e.POST("/admin/deployments", e.MasterKey, map[string]any{"name": name, "provider_type": "openai_compatible", "upstream_model": "test", "supports_chat": false, "supports_embeddings": true, "supports_responses": false})
	if code != 201 {
		t.Fatalf("create %d %s", code, body)
	}
	code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{"supports_responses": true})
	var d DeploymentResponse
	_ = json.Unmarshal(body, &d)
	if code != 200 || d.Capabilities == nil || d.Capabilities.Chat || !d.Capabilities.Embeddings || d.Capabilities.Responses == nil || !*d.Capabilities.Responses {
		t.Fatalf("capabilities not preserved: %d %s", code, body)
	}
}

func TestAdminResponsesOnlyPatchPreservesAdapterDefaults(t *testing.T) {
	e := newTestEnv(t)
	name := "responses-defaults-" + randHex(4)
	code, body := e.POST("/admin/deployments", e.MasterKey, map[string]any{"name": name, "provider_type": "anthropic", "upstream_model": "test", "credential_ref": "UNSET_TEST_ANTHROPIC_KEY"})
	if code != 201 {
		t.Fatalf("create %d %s", code, body)
	}
	code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{"supports_responses": false})
	var d DeploymentResponse
	_ = json.Unmarshal(body, &d)
	if code != 200 || d.Capabilities == nil || !d.Capabilities.Chat || !d.Capabilities.StreamChat || d.Capabilities.Embeddings || d.Capabilities.Responses == nil || *d.Capabilities.Responses {
		t.Fatalf("provider defaults changed: %d %s", code, body)
	}
}

func TestResponsesRejectMismatchedSSEEventName(t *testing.T) {
	f := newChatFixture(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		event, _ := json.Marshal(map[string]any{"type": "response.completed", "response": nativeResponse("resp_test", "test")})
		fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", event)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	key := f.issueKey(f.createTeam("response-invalid-event"), nil)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello","stream":true}`, f.Alias))
	if w.Code != 502 || strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("bad event forwarded: %d %s", w.Code, w.Body)
	}
}

func TestResponsesDeleteAcceptsNoContent(t *testing.T) {
	f := newChatFixture(t)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(nativeResponse("resp_test", "test"))
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	key := f.issueKey(f.createTeam("response-delete-204"), nil)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello"}`, f.Alias))
	var response struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "DELETE", "/v1/responses/"+response.ID, "")
	if w.Code != 200 {
		t.Fatalf("delete %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+response.ID, "")
	if w.Code != 404 {
		t.Fatalf("binding survived deletion: %d", w.Code)
	}
}
