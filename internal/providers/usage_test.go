package providers

import (
	"io"
	"math"
	"strings"
	"testing"
)

func TestCanonicalNativeUsage(t *testing.T) {
	u, err := (AnthropicUsage{InputTokens: 10, OutputTokens: 5, CacheCreationInputTokens: 20, CacheReadInputTokens: 30}).Canonical()
	if err != nil || u.PromptTokens != 60 || u.TotalTokens != 65 || u.PromptTokensDetails.CachedTokens != 30 {
		t.Fatalf("Anthropic: %+v %v", u, err)
	}
	u, err = (GeminiUsage{PromptTokenCount: 60, CachedContentTokenCount: 30, CandidatesTokenCount: 5, ThoughtsTokenCount: 10, TotalTokenCount: 75}).Canonical()
	if err != nil || u.PromptTokens != 60 || u.CompletionTokens != 15 || u.TotalTokens != 75 || u.CompletionTokensDetails.ReasoningTokens != 10 {
		t.Fatalf("Gemini: %+v %v", u, err)
	}
	for _, native := range []AnthropicUsage{{InputTokens: -1}, {InputTokens: math.MaxInt, OutputTokens: 1}, {InputTokens: 1, CacheReadInputTokens: math.MaxInt}, {CacheCreationInputTokens: 2, CacheCreation: &CacheCreationDetails{Ephemeral1hInputTokens: 1}}} {
		if _, err := native.Canonical(); err == nil {
			t.Fatal("invalid native usage accepted")
		}
	}
}

func TestGeminiStreamUsageAndFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"complete", `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":3,"cachedContentTokenCount":4}}` + "\n\n", true},
		{"truncated", `data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}` + "\n\n", false},
		{"provider error", `data: {"error":{"message":"overloaded"}}` + "\n\n", false},
		{"invalid", "data: not json\n\n", false},
		{"overlong", "data: " + strings.Repeat("x", 1024*1024) + "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			go TranslateGeminiStream(io.NopCloser(strings.NewReader(tc.body)), w, "test")
			defer r.Close()
			body, err := io.ReadAll(r)
			if tc.valid {
				if err != nil || !strings.Contains(string(body), `"reasoning_tokens":3`) || !strings.Contains(string(body), `"completion_tokens":8`) || !strings.Contains(string(body), "[DONE]") {
					t.Fatalf("stream=%s error=%v", body, err)
				}
			} else if err == nil || strings.Contains(string(body), "[DONE]") {
				t.Fatalf("false success: %s %v", body, err)
			}
		})
	}
}
