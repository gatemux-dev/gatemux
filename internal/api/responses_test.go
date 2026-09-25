package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
)

func nativeResponse(id, model string) map[string]any {
	return map[string]any{"id": id, "object": "response", "created_at": 1, "status": "completed", "error": nil, "incomplete_details": nil, "model": model, "output": []any{map[string]any{"id": "msg_test", "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "hello", "annotations": []any{}}}}}, "usage": map[string]any{"input_tokens": 8, "output_tokens": 2, "total_tokens": 10, "input_tokens_details": map[string]any{"cached_tokens": 4}, "output_tokens_details": map[string]any{"reasoning_tokens": 1}}, "tools": []any{}, "parallel_tool_calls": true, "tool_choice": "auto", "metadata": map[string]any{}, "store": true}
}

func newResponsesFixture(t *testing.T) (*chatFixture, *atomic.Int32, chan map[string]json.RawMessage) {
	f := newChatFixture(t)
	calls := &atomic.Int32{}
	bodies := make(chan map[string]json.RawMessage, 20)
	var mu sync.Mutex
	responses := map[string]map[string]any{}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/responses") {
			if strings.HasSuffix(r.URL.Path, "/chat/completions") {
				body, _ := io.ReadAll(r.Body)
				var req struct {
					Stream bool `json:"stream"`
				}
				_ = json.Unmarshal(body, &req)
				if req.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"chat_sdk\",\"object\":\"chat.completion.chunk\",\"model\":\"private\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			f.Mock.Config.Handler.ServeHTTP(w, r)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		id := strings.TrimPrefix(r.URL.Path, "/v1/responses/")
		if r.Method == "POST" {
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			select {
			case bodies <- body:
			default:
				t.Error("test fixture request limit exceeded")
			}
			var model, previous string
			_ = json.Unmarshal(body["model"], &model)
			_ = json.Unmarshal(body["previous_response_id"], &previous)
			if model != f.UpModel {
				t.Errorf("model not rewritten: %s", model)
			}
			if previous != "" && responses[previous] == nil {
				t.Errorf("previous ID not mapped: %s", previous)
			}
			id = fmt.Sprintf("resp_mock_%d", calls.Load())
			response := nativeResponse(id, model)
			response["previous_response_id"] = previous
			responses[id] = response
			if string(body["stream"]) == "true" {
				w.Header().Set("Content-Type", "text/event-stream")
				start := map[string]any{"id": id, "object": "response", "status": "in_progress", "model": model, "output": []any{}, "usage": nil}
				for _, event := range []map[string]any{{"type": "response.created", "sequence_number": 0, "response": start}, {"type": "response.output_text.delta", "sequence_number": 1, "delta": "hello", "output_index": 0, "content_index": 0, "item_id": "msg_test"}, {"type": "response.completed", "sequence_number": 2, "response": response}} {
					data, _ := json.Marshal(event)
					fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event["type"], data)
					w.(http.Flusher).Flush()
				}
				return
			}
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		if strings.HasSuffix(id, "/input_items") {
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}, "has_more": false})
			return
		}
		if responses[id] == nil {
			w.WriteHeader(404)
			return
		}
		if r.Method == "DELETE" {
			delete(responses, id)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "object": "response", "deleted": true})
			return
		}
		_ = json.NewEncoder(w).Encode(responses[id])
	}))
	t.Cleanup(mock.Close)
	setStreamUpstream(t, f, mock.URL, "openai")
	f.upsertPricing(100, 200)
	return f, calls, bodies
}

func responseHTTP(f *chatFixture, key, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	return w
}

func TestResponsesNativeRoundTripOwnershipAndDelete(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy=%t", legacy), func(t *testing.T) { testResponsesOwnedRoundTrip(t, legacy) })
	}
}

func testResponsesOwnedRoundTrip(t *testing.T, legacy bool) {
	f, calls, bodies := newResponsesFixture(t)
	team := f.createTeam("responses")
	key := f.issueKey(team, nil)
	otherKey := f.issueKey(team, nil)
	otherTeamKey := f.issueKey(f.createTeam("responses-other"), nil)
	request := fmt.Sprintf(`{"model":%q,"input":"hello","metadata":{"exact":9007199254740993},"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"text":{"format":{"type":"json_object"}},"reasoning":{"effort":"low"}}`, f.Alias)
	w := responseHTTP(f, key, "POST", "/v1/responses", request)
	if w.Code != 200 {
		t.Fatalf("create %d: %s", w.Code, w.Body)
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var id string
	_ = json.Unmarshal(response["id"], &id)
	if !responseIDPattern.MatchString(id) || string(response["model"]) != fmt.Sprintf("%q", f.Alias) {
		t.Fatalf("rewrite: %s", w.Body)
	}
	if !strings.HasPrefix(id, "resp_gatemux_") {
		t.Fatal("new create emitted legacy ID")
	}
	if legacy {
		oldID := strings.Replace(id, "resp_gatemux_", "resp_aiport_", 1)
		if _, err := f.Store.Pool.Exec(context.Background(), `UPDATE response_bindings SET id=$1 WHERE id=$2`, oldID, id); err != nil {
			t.Fatal(err)
		}
		id = oldID // Simulate a binding persisted by the pre-rename binary.
	}
	body := <-bodies
	if !strings.Contains(string(body["metadata"]), "9007199254740993") || len(body["tools"]) == 0 || string(body["max_output_tokens"]) != "1024" {
		t.Fatalf("wire fidelity: %+v", body)
	}
	for _, wrong := range []string{otherKey, otherTeamKey} {
		before := calls.Load()
		for _, method := range []string{"GET", "DELETE"} {
			w = responseHTTP(f, wrong, method, "/v1/responses/"+id, "")
			if w.Code != 404 {
				t.Fatalf("cross-principal %s: %d %s", method, w.Code, w.Body)
			}
		}
		w = responseHTTP(f, wrong, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"next","previous_response_id":%q}`, f.Alias, id))
		if w.Code != 404 || calls.Load() != before {
			t.Fatalf("cross-principal previous response reached upstream")
		}
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+id, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatalf("retrieve: %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":[{"type":"function_call_output","call_id":"call_test","output":"ok"}],"previous_response_id":%q}`, f.Alias, id))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"previous_response_id":"`+id+`"`) {
		t.Fatalf("multi-turn: %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+id+"/input_items?limit=10", "")
	if w.Code != 200 {
		t.Fatalf("input items: %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "DELETE", "/v1/responses/"+id, "")
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+id, "")
	if w.Code != 404 {
		t.Fatalf("deleted binding remained accessible: %d", w.Code)
	}
}

func TestResponsesStreamAndStateless(t *testing.T) {
	f, _, _ := newResponsesFixture(t)
	key := f.issueKey(f.createTeam("responses-stream"), nil)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","stream":true}`, f.Alias))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "event: response.completed") || strings.Contains(w.Body.String(), "resp_mock_") || strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatalf("stream: %d %s", w.Code, w.Body)
	}
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatal("stream leaked deployment permit")
	}
	w = responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","store":false}`, f.Alias))
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if !responseIDPattern.MatchString(result.ID) {
		t.Fatalf("stateless response invalid: %s", w.Body)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+result.ID, "")
	if w.Code != 404 {
		t.Fatalf("store=false persisted binding: %d", w.Code)
	}
}

func TestResponsesRejectUnsafeStateAndExpiredBinding(t *testing.T) {
	f, calls, _ := newResponsesFixture(t)
	key := f.issueKey(f.createTeam("response-policy"), nil)
	for _, tail := range []string{`"background":true`, `"conversation":"conv_other"`, `"prompt":{"id":"pmpt_other"}`, `"previous_response_id":"resp_upstream"`, `"tools":[{"type":"file_search","vector_store_ids":["vs_other"]}]`, `"input":[{"type":"item_reference","id":"msg_other"}]`, `"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file_other"}]}]`} {
		w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi",%s}`, f.Alias, tail))
		if w.Code != 400 {
			t.Fatalf("unsafe input accepted: %s => %d %s", tail, w.Code, w.Body)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe input reached upstream")
	}
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi"}`, f.Alias))
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if _, err := f.Store.Pool.Exec(context.Background(), `UPDATE response_bindings SET expires_at=NOW()-INTERVAL '1 second' WHERE id=$1`, result.ID); err != nil {
		t.Fatal(err)
	}
	w = responseHTTP(f, key, "GET", "/v1/responses/"+result.ID, "")
	if w.Code != 404 {
		t.Fatal("expired response returned")
	}
}

func TestResponsesInterruptedStreamConservesBudgetAndReleases(t *testing.T) {
	f := newChatFixture(t)
	done := make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_timeout\",\"object\":\"response\",\"status\":\"in_progress\",\"model\":\"test\",\"output\":[]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	f.Handler.Streaming = config.StreamingConfig{FirstEventTimeout: time.Second, IdleTimeout: 50 * time.Millisecond}
	f.upsertPricing(1000000, 1000000)
	key := f.issueKey(f.setTeamBudget(f.createTeam("response-budget"), 100000), nil)
	id := "response-timeout-" + randHex(4)
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hi","max_output_tokens":10,"stream":true}`, f.Alias)))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "gateway_stream_error") || strings.Contains(w.Body.String(), "response.completed") {
		t.Fatalf("false stream success: %s", w.Body)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("upstream did not cancel")
	}
	var estimated, settled int64
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT estimated_cost_cents,settled_cost_cents FROM budget_reservations WHERE request_id=$1`, id).Scan(&estimated, &settled); err != nil || settled != estimated || settled <= 0 {
		t.Fatalf("unknown usage budget=%d/%d %v", settled, estimated, err)
	}
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatal("stream leaked deployment permit")
	}
}
