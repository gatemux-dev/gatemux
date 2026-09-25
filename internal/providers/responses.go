package providers

import (
	"context"
	"encoding/json"
	"net/http"
)

// ResponsesProvider exposes the native protocol without coercing it into chat.
// Only a deployment with CapabilityResponses is eligible for this interface.
type ResponsesProvider interface {
	ResponseRequest(context.Context, string, string, json.RawMessage) (*http.Response, error)
}

type ResponseUsage struct {
	decoded, reported bool
	InputTokens       int `json:"input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
	InputDetails      *struct {
		CachedTokens     int `json:"cached_tokens"`
		CacheWriteTokens int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
	OutputDetails *CompletionTokensDetails `json:"output_tokens_details"`
}

func (u *ResponseUsage) UnmarshalJSON(raw []byte) error {
	type wire ResponseUsage
	var value wire
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	*u = ResponseUsage(value)
	u.decoded, u.reported = true, reportedCounters(raw, "input_tokens", "output_tokens")
	return nil
}

func (u *ResponseUsage) UsageReported() bool {
	if u == nil {
		return false
	}
	if u.decoded {
		return u.reported
	}
	return u.InputTokens >= 0 && u.OutputTokens >= 0 && (u.InputTokens > 0 || u.OutputTokens > 0)
}

func (u ResponseUsage) Canonical() Usage {
	out := Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: u.TotalTokens, CompletionTokensDetails: u.OutputDetails}
	if u.InputDetails != nil {
		out.PromptTokensDetails = &PromptTokensDetails{CachedTokens: u.InputDetails.CachedTokens}
		out.CacheCreationInputTokens = u.InputDetails.CacheWriteTokens
	}
	return out
}
