package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

func TestChatCompletionLosslessRoundTrip(t *testing.T) {
	var upstreamRequest map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.Path, "/chat/completions"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Authorization"), "Bearer provider-key"; got != want {
			t.Errorf("authorization = %q, want %q", got, want)
		}
		if err := json.NewDecoder(r.Body).Decode(&upstreamRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-1",
			"object":"chat.completion",
			"created":123,
			"model":"provider-model",
			"system_fingerprint":"fp_provider",
			"choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},"finish_reason":"tool_calls","logprobs":null}],
			"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":6}}
		}`)
	}))
	defer upstream.Close()

	clientRequest := []byte(`{
		"model":"public-alias",
		"messages":[{"role":"user","content":[{"type":"text","text":"use a tool"},{"type":"image_url","image_url":{"url":"https://example.test/image.png"}}]}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"tool_choice":"required",
		"response_format":{"type":"json_object"},
		"reasoning_effort":"medium",
		"parallel_tool_calls":true,
		"vendor_option":{"mode":"fast"}
	}`)
	var req providers.ChatRequest
	if err := json.Unmarshal(clientRequest, &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	req.Model = "provider-model"

	resp, err := New("provider-key", upstream.URL).ChatCompletion(context.Background(), &req)
	if err != nil {
		t.Fatalf("ChatCompletion: %v", err)
	}
	if got, want := upstreamRequest["model"], "provider-model"; got != want {
		t.Fatalf("upstream model = %v, want %v", got, want)
	}
	var original map[string]any
	_ = json.Unmarshal(clientRequest, &original)
	for _, field := range []string{"messages", "tools", "tool_choice", "response_format", "reasoning_effort", "parallel_tool_calls", "vendor_option"} {
		if !reflect.DeepEqual(upstreamRequest[field], original[field]) {
			t.Errorf("upstream field %q changed:\n got: %#v\nwant: %#v", field, upstreamRequest[field], original[field])
		}
	}

	resp.Model = "public-alias"
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var clientResponse map[string]any
	if err := json.Unmarshal(encoded, &clientResponse); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if clientResponse["model"] != "public-alias" || clientResponse["system_fingerprint"] != "fp_provider" {
		t.Fatalf("response envelope not preserved: %s", encoded)
	}
	choice := clientResponse["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if _, ok := message["tool_calls"]; !ok {
		t.Fatalf("tool_calls missing from response: %s", encoded)
	}
	usage := clientResponse["usage"].(map[string]any)
	details := usage["prompt_tokens_details"].(map[string]any)
	if details["cached_tokens"] != float64(6) {
		t.Fatalf("cached token details missing from response: %s", encoded)
	}
}
