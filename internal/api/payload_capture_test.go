package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/guardrails"
)

// payloadFor waits for the usage row and returns its captured bodies; ok is
// false when nothing was captured.
func payloadFor(t *testing.T, f *chatFixture, requestID string) (req, resp map[string]any, ok bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var id int64
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT id FROM usage_log WHERE request_id=$1`, requestID).Scan(&id); err == nil {
			var rawReq, rawResp []byte
			err := f.Store.Pool.QueryRow(context.Background(), `SELECT request_body, response_body FROM usage_log_payloads WHERE usage_id=$1`, id).Scan(&rawReq, &rawResp)
			if err != nil {
				return nil, nil, false
			}
			_ = json.Unmarshal(rawReq, &req)
			_ = json.Unmarshal(rawResp, &resp)
			return req, resp, true
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage row %s not recorded", requestID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func sseUpstream(t *testing.T, chunks ...string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		for i, c := range chunks {
			finish := "null"
			if i == len(chunks)-1 {
				finish = `"stop"`
			}
			_, _ = io.WriteString(w, `data: {"model":"up","choices":[{"index":0,"delta":{"content":`+strings.ReplaceAll(jsonString(c), "\n", "")+`},"finish_reason":`+finish+`}]}`+"\n\n")
		}
		_, _ = io.WriteString(w, `data: {"model":"up","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

func sendStream(t *testing.T, f *chatFixture, key, content string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+f.Alias+`","messages":[{"role":"user","content":`+jsonString(content)+`}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+key)
	id := "capture-" + randHex(6)
	req.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("stream status %d: %s", w.Code, w.Body)
	}
	return id
}

// Streams record the request and the assembled reply when the team has
// capture on, and nothing when it is off.
func TestStreamPayloadCapture(t *testing.T) {
	f := newChatFixture(t)
	setStreamUpstream(t, f, sseUpstream(t, "Hello", " world").URL, "openai")
	team := f.createTeam("capture-stream")
	f.upsertPricing(100, 200)
	key := f.issueKey(team, nil)

	id := sendStream(t, f, key, "hi there")
	if _, _, ok := payloadFor(t, f, id); ok {
		t.Fatal("captured with capture off")
	}

	if err := f.Store.SetTeamCapturePayloads(context.Background(), team.Slug, true); err != nil {
		t.Fatal(err)
	}
	id = sendStream(t, f, key, "hi there")
	req, resp, ok := payloadFor(t, f, id)
	if !ok {
		t.Fatal("stream payload not captured")
	}
	if !strings.Contains(mustJSON(req), "hi there") {
		t.Fatalf("request = %v", req)
	}
	choices, _ := resp["choices"].([]any)
	if resp["streamed"] != true || len(choices) != 1 {
		t.Fatalf("response = %v", resp)
	}
	msg := choices[0].(map[string]any)["message"].(map[string]any)
	if msg["content"] != "Hello world" || choices[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("assembled reply = %v", choices[0])
	}
}

// Guarded streams keep only the request: chunks are seen before redaction.
func TestGuardedStreamCapturesRequestOnly(t *testing.T) {
	f := newChatFixture(t)
	setStreamUpstream(t, f, sseUpstream(t, "a secret", " reply").URL, "openai")
	team := f.createTeam("capture-guarded")
	f.upsertPricing(100, 200)
	key := f.issueKey(team, nil)
	setGuardScope(t, f.Store, "team", team.Slug, []guardrails.Policy{testGuardPolicy("redact", "post", "secret")})
	if err := f.Store.SetTeamCapturePayloads(context.Background(), team.Slug, true); err != nil {
		t.Fatal(err)
	}
	id := sendStream(t, f, key, "hello")
	req, resp, ok := payloadFor(t, f, id)
	if !ok || req == nil {
		t.Fatal("guarded stream request not captured")
	}
	if resp != nil {
		t.Fatalf("guarded stream response captured before redaction: %v", resp)
	}
}

// A request that fails at the provider keeps its body, so the failure can
// be inspected and replayed.
func TestFailedRequestCapturesBody(t *testing.T) {
	f := newChatFixture(t)
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, `{"error":{"message":"boom"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(failing.Close)
	setStreamUpstream(t, f, failing.URL, "openai")
	team := f.createTeam("capture-failure")
	f.upsertPricing(100, 200)
	key := f.issueKey(team, nil)
	if err := f.Store.SetTeamCapturePayloads(context.Background(), team.Slug, true); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+f.Alias+`","messages":[{"role":"user","content":"why did this fail"}]}`))
	req.Header.Set("Authorization", "Bearer "+key)
	id := "capture-fail-" + randHex(6)
	req.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if w.Code < 500 {
		t.Fatalf("status %d, want a provider failure", w.Code)
	}
	body, resp, ok := payloadFor(t, f, id)
	if !ok || !strings.Contains(mustJSON(body), "why did this fail") {
		t.Fatalf("failed request body not captured: %v", body)
	}
	if resp != nil {
		t.Fatalf("failed request has a response body: %v", resp)
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
