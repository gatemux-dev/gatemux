package providers

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEmbeddingInputUnmarshalString(t *testing.T) {
	var req EmbeddingRequest
	if err := json.Unmarshal([]byte(`{"model":"embed","input":"hello"}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := req.Input.Len(), 1; got != want {
		t.Fatalf("len(input) = %d, want %d", got, want)
	}
	if got, want := req.Input.Texts[0], "hello"; got != want {
		t.Fatalf("input[0] = %q, want %q", got, want)
	}
}

func TestEmbeddingInputUnmarshalArray(t *testing.T) {
	var req EmbeddingRequest
	if err := json.Unmarshal([]byte(`{"model":"embed","input":["hello","world"]}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := req.Input.Len(), 2; got != want {
		t.Fatalf("len(input) = %d, want %d", got, want)
	}
	if got, want := req.Input.Texts[1], "world"; got != want {
		t.Fatalf("input[1] = %q, want %q", got, want)
	}
}

func TestEmbeddingInputUnmarshalRejectsInvalidShape(t *testing.T) {
	var req EmbeddingRequest
	if err := json.Unmarshal([]byte(`{"model":"embed","input":123}`), &req); err == nil {
		t.Fatal("expected unmarshal error for non-string input")
	}
}

func TestChatRequestPreservesUnknownAndMultimodalFields(t *testing.T) {
	body := []byte(`{
		"model":"public-alias",
		"messages":[{
			"role":"user",
			"content":[
				{"type":"text","text":"describe this"},
				{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}}
			],
			"name":"analyst"
		}],
		"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],
		"tool_choice":"required",
		"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object"}}},
		"reasoning_effort":"high",
		"top_p":0.8,
		"provider_extension":{"enabled":true}
	}`)
	var req ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, want := req.Messages[0].Content, "describe this"; got != want {
		t.Fatalf("text projection = %q, want %q", got, want)
	}

	req.Model = "upstream-model"
	encoded, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	var original map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode marshaled request: %v", err)
	}
	if err := json.Unmarshal(body, &original); err != nil {
		t.Fatalf("decode original request: %v", err)
	}
	if got["model"] != "upstream-model" {
		t.Fatalf("model = %v, want upstream-model", got["model"])
	}
	for _, field := range []string{"messages", "tools", "tool_choice", "response_format", "reasoning_effort", "top_p", "provider_extension"} {
		if !reflect.DeepEqual(got[field], original[field]) {
			t.Errorf("field %q changed:\n got: %#v\nwant: %#v", field, got[field], original[field])
		}
	}
}

func TestChatRequestMergesStreamOptions(t *testing.T) {
	var req ChatRequest
	if err := json.Unmarshal([]byte(`{
		"model":"alias",
		"messages":[{"role":"user","content":"hello"}],
		"stream":false,
		"stream_options":{"include_obfuscation":true}
	}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	req.Stream = true
	req.StreamOptions = &StreamOptions{IncludeUsage: true}
	encoded, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got struct {
		Stream        bool `json:"stream"`
		StreamOptions struct {
			IncludeUsage       bool `json:"include_usage"`
			IncludeObfuscation bool `json:"include_obfuscation"`
		} `json:"stream_options"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Stream || !got.StreamOptions.IncludeUsage || !got.StreamOptions.IncludeObfuscation {
		t.Fatalf("stream options were not merged: %s", encoded)
	}
}

func TestChatResponsePreservesModernResponseFields(t *testing.T) {
	body := []byte(`{
		"id":"chatcmpl-1",
		"object":"chat.completion",
		"created":123,
		"model":"provider-model",
		"system_fingerprint":"fp_123",
		"choices":[{
			"index":0,
			"message":{"role":"assistant","content":null,"refusal":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},
			"finish_reason":"tool_calls",
			"logprobs":{"content":[]}
		}],
		"usage":{"prompt_tokens":12,"completion_tokens":7,"total_tokens":19,"prompt_tokens_details":{"cached_tokens":8},"completion_tokens_details":{"reasoning_tokens":3}}
	}`)
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := resp.Usage.PromptTokensDetails.CachedTokens; got != 8 {
		t.Fatalf("cached tokens = %d, want 8", got)
	}
	if got := resp.Usage.CompletionTokensDetails.ReasoningTokens; got != 3 {
		t.Fatalf("reasoning tokens = %d, want 3", got)
	}
	resp.Model = "public-alias"
	encoded, err := json.Marshal(&resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	var original map[string]any
	_ = json.Unmarshal(encoded, &got)
	_ = json.Unmarshal(body, &original)
	if got["model"] != "public-alias" {
		t.Fatalf("model = %v, want public-alias", got["model"])
	}
	for _, field := range []string{"id", "object", "created", "system_fingerprint", "choices", "usage"} {
		if !reflect.DeepEqual(got[field], original[field]) {
			t.Errorf("field %q changed:\n got: %#v\nwant: %#v", field, got[field], original[field])
		}
	}
}

func TestEmbeddingWirePreservesOptionsAndBase64Response(t *testing.T) {
	var req EmbeddingRequest
	if err := json.Unmarshal([]byte(`{"model":"alias","input":"hello","dimensions":256,"encoding_format":"base64","user":"customer-1"}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	req.Model = "text-embedding-upstream"
	encoded, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	var requestMap map[string]any
	_ = json.Unmarshal(encoded, &requestMap)
	if requestMap["model"] != "text-embedding-upstream" || requestMap["dimensions"] != float64(256) || requestMap["encoding_format"] != "base64" || requestMap["user"] != "customer-1" {
		t.Fatalf("embedding options were not preserved: %s", encoded)
	}

	responseBody := []byte(`{"object":"list","model":"provider-model","data":[{"object":"embedding","index":0,"embedding":"AQID"}],"usage":{"prompt_tokens":1,"total_tokens":1},"vendor_meta":{"region":"eu"}}`)
	var resp EmbeddingResponse
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		t.Fatalf("unmarshal base64 response: %v", err)
	}
	resp.Model = "alias"
	encoded, err = json.Marshal(&resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var responseMap map[string]any
	_ = json.Unmarshal(encoded, &responseMap)
	data := responseMap["data"].([]any)[0].(map[string]any)
	if data["embedding"] != "AQID" || responseMap["model"] != "alias" || !reflect.DeepEqual(responseMap["vendor_meta"], map[string]any{"region": "eu"}) {
		t.Fatalf("embedding response fields were not preserved: %s", encoded)
	}
}
