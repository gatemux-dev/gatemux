package providers

import (
	"fmt"
	"math"
)

// Anthropic input_tokens excludes both cache reads and writes. Normalize once
// at the adapter boundary to the gateway's inclusive prompt_tokens convention.
type AnthropicUsage struct {
	InputTokens              int                   `json:"input_tokens"`
	OutputTokens             int                   `json:"output_tokens"`
	CacheCreationInputTokens int                   `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int                   `json:"cache_read_input_tokens"`
	CacheCreation            *CacheCreationDetails `json:"cache_creation,omitempty"`
}

func (u AnthropicUsage) Canonical() (Usage, error) {
	if u.CacheCreation != nil {
		write, err := tokenSum(u.CacheCreation.Ephemeral5mInputTokens, u.CacheCreation.Ephemeral1hInputTokens)
		if err != nil {
			return Usage{}, err
		}
		if u.CacheCreationInputTokens != 0 && u.CacheCreationInputTokens != write {
			return Usage{}, fmt.Errorf("inconsistent cache creation usage")
		}
		u.CacheCreationInputTokens = write
	}
	input, err := tokenSum(u.InputTokens, u.CacheCreationInputTokens, u.CacheReadInputTokens)
	if err != nil {
		return Usage{}, err
	}
	total, err := tokenSum(input, u.OutputTokens)
	if err != nil {
		return Usage{}, err
	}
	return Usage{PromptTokens: input, CompletionTokens: u.OutputTokens, TotalTokens: total,
		CacheCreationInputTokens: u.CacheCreationInputTokens, CacheReadInputTokens: u.CacheReadInputTokens, CacheCreation: u.CacheCreation,
		PromptTokensDetails: &PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}}, nil
}

// Gemini includes cache reads in promptTokenCount but reports thoughts outside
// candidatesTokenCount. Canonical completion totals include reasoning once.
type GeminiUsage struct {
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

func (u GeminiUsage) Canonical() (Usage, error) {
	output, err := tokenSum(u.CandidatesTokenCount, u.ThoughtsTokenCount)
	if err != nil {
		return Usage{}, err
	}
	total, err := tokenSum(u.PromptTokenCount, output)
	if err != nil {
		return Usage{}, err
	}
	if u.CachedContentTokenCount < 0 || u.CachedContentTokenCount > u.PromptTokenCount || u.TotalTokenCount < 0 {
		return Usage{}, fmt.Errorf("invalid Gemini usage")
	}
	return Usage{PromptTokens: u.PromptTokenCount, CompletionTokens: output, TotalTokens: total,
		PromptTokensDetails:     &PromptTokensDetails{CachedTokens: u.CachedContentTokenCount},
		CompletionTokensDetails: &CompletionTokensDetails{ReasoningTokens: u.ThoughtsTokenCount}}, nil
}

func tokenSum(counts ...int) (int, error) {
	total := 0
	for _, count := range counts {
		if count < 0 || total > math.MaxInt-count {
			return 0, fmt.Errorf("invalid or overflowing token usage")
		}
		total += count
	}
	return total, nil
}

// SetCompletionTokens validates cumulative native stream output counts.
func (u *Usage) SetCompletionTokens(output int) error {
	total, err := tokenSum(u.PromptTokens, output)
	if err != nil {
		return err
	}
	u.CompletionTokens, u.TotalTokens = output, total
	return nil
}
