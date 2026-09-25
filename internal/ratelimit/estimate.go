package ratelimit

import "github.com/gatemux-dev/gatemux/internal/providers"

func EstimateChatTokens(req *providers.ChatRequest) int {
	prompt, completion := EstimateChatUsage(req)
	return prompt + completion
}

func EstimateChatUsage(req *providers.ChatRequest) (promptTokens, completionTokens int) {
	if req == nil {
		return 0, 0
	}
	for _, msg := range req.Messages {
		promptTokens += estimateTextTokens(msg.Content)
	}
	if req.OutputTokenLimit() != nil && *req.OutputTokenLimit() > 0 {
		completionTokens = *req.OutputTokenLimit()
	}
	if promptTokens == 0 && len(req.Messages) > 0 {
		promptTokens = len(req.Messages)
	}
	return promptTokens, completionTokens
}

func EstimateEmbeddingTokens(req *providers.EmbeddingRequest) int {
	if req == nil {
		return 0
	}
	total := 0
	if req.Input.IsTokenized() {
		for _, tokens := range req.Input.Tokens {
			total += len(tokens)
		}
		return total
	}
	for _, item := range req.Input.Texts {
		total += estimateTextTokens(item)
	}
	if total == 0 && req.Input.Len() > 0 {
		return req.Input.Len()
	}
	return total
}

func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}
