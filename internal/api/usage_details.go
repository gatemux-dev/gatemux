package api

import (
	"encoding/json"
	"github.com/gatemux-dev/gatemux/internal/providers"
)

func usageDetailsJSON(u providers.Usage) json.RawMessage {
	if u.PromptTokensDetails == nil && u.CompletionTokensDetails == nil && u.CacheCreation == nil && u.CacheCreationInputTokens == 0 && u.CacheReadInputTokens == 0 {
		return nil
	}
	data, _ := json.Marshal(u)
	return data
}
