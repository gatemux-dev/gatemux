package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Token IDs are tokenizer-specific. Compatible providers receive them unchanged;
// translating adapters must explicitly reject them instead of stringifying IDs.
type EmbeddingInput struct {
	Texts  []string
	Tokens [][]int
	single bool
}

func (i EmbeddingInput) Len() int {
	if i.IsTokenized() {
		return len(i.Tokens)
	}
	return len(i.Texts)
}

func (i EmbeddingInput) IsTokenized() bool { return len(i.Tokens) > 0 }

func (i EmbeddingInput) MarshalJSON() ([]byte, error) {
	if i.IsTokenized() {
		if i.single {
			return json.Marshal(i.Tokens[0])
		}
		return json.Marshal(i.Tokens)
	}
	if i.single && len(i.Texts) == 1 {
		return json.Marshal(i.Texts[0])
	}
	return json.Marshal(i.Texts)
}

func (i *EmbeddingInput) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	invalid := func() error {
		return fmt.Errorf("input must be a non-empty string, string array, token array, or array of token arrays; token IDs must be non-negative integers")
	}
	if len(data) == 0 {
		return invalid()
	}
	var out EmbeddingInput
	if data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil || text == "" {
			return invalid()
		}
		out.Texts, out.single = []string{text}, true
	} else {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil || len(items) == 0 {
			return invalid()
		}
		parseTokens := func(raw []json.RawMessage) ([]int, error) {
			if len(raw) == 0 {
				return nil, invalid()
			}
			tokens := make([]int, len(raw))
			for n, item := range raw {
				if bytes.Equal(bytes.TrimSpace(item), []byte("null")) {
					return nil, invalid()
				}
				if err := json.Unmarshal(item, &tokens[n]); err != nil || tokens[n] < 0 {
					return nil, invalid()
				}
			}
			return tokens, nil
		}
		switch items[0][0] {
		case '"':
			for _, item := range items {
				var text string
				if err := json.Unmarshal(item, &text); err != nil || text == "" {
					return invalid()
				}
				out.Texts = append(out.Texts, text)
			}
		case '[':
			for _, item := range items {
				var row []json.RawMessage
				if err := json.Unmarshal(item, &row); err != nil {
					return invalid()
				}
				tokens, err := parseTokens(row)
				if err != nil {
					return err
				}
				out.Tokens = append(out.Tokens, tokens)
			}
		default:
			tokens, err := parseTokens(items)
			if err != nil {
				return err
			}
			out.Tokens, out.single = [][]int{tokens}, true
		}
	}
	*i = out
	return nil
}

func UnsupportedTokenInput(provider string) error {
	return &UpstreamError{Provider: provider, StatusCode: 400, Message: "this provider adapter cannot translate tokenizer-specific embedding token IDs; use text input or an OpenAI-compatible deployment"}
}
