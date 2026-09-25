package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type Provider interface {
	Type() string
	Capabilities() Capabilities
	ChatCompletion(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
	// ChatCompletionStream returns OpenAI-shaped SSE bytes. Adapters for
	// non-OpenAI upstreams are responsible for translating internally.
	// Opening and all subsequent reads must honor ctx cancellation. Closing
	// the returned body must unblock reads. Successful streams end in [DONE].
	ChatCompletionStream(ctx context.Context, req *ChatRequest) (io.ReadCloser, error)
	Embeddings(ctx context.Context, req *EmbeddingRequest) (*EmbeddingResponse, error)
}

type Capability string

const (
	CapabilityChat       Capability = "chat"
	CapabilityStreamChat Capability = "stream_chat"
	CapabilityEmbeddings Capability = "embeddings"
	CapabilityResponses  Capability = "responses"
)

type Capabilities struct {
	Chat       bool
	StreamChat bool
	Embeddings bool
	Responses  bool
}

func (c Capabilities) Supports(capability Capability) bool {
	switch capability {
	case CapabilityChat:
		return c.Chat
	case CapabilityStreamChat:
		return c.Chat && c.StreamChat
	case CapabilityEmbeddings:
		return c.Embeddings
	case CapabilityResponses:
		return c.Responses
	default:
		return true
	}
}

type ChatRequest struct {
	Model               string         `json:"model"`
	Messages            []ChatMessage  `json:"messages"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	MaxTokens           *int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	User                string         `json:"user,omitempty"`

	// raw retains the complete client request. AI gateway request shapes evolve
	// much faster than this binary, so OpenAI-compatible adapters must not drop
	// fields they do not yet understand (tools, response_format, reasoning,
	// provider-specific extra_body fields, and future additions). MarshalJSON
	// overlays the routing fields above on a copy of raw.
	raw map[string]json.RawMessage
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`

	// raw preserves multimodal content arrays and fields such as tool_calls,
	// tool_call_id, name, refusal, and reasoning_content. Content remains a
	// string because translating adapters and guardrails need a cheap text view.
	raw             map[string]json.RawMessage
	originalRole    string
	originalContent string
}

type ChatResponse struct {
	ID      string       `json:"id"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`

	// raw is populated for OpenAI-compatible upstreams. The handler only
	// rewrites Model, so returning this raw envelope preserves tool calls,
	// refusals, logprobs, fingerprints, reasoning, and future response fields.
	raw map[string]json.RawMessage
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type EmbeddingRequest struct {
	Model string         `json:"model"`
	Input EmbeddingInput `json:"input"`
	User  string         `json:"user,omitempty"`

	// raw preserves dimensions, encoding_format, user, and provider-specific
	// parameters while the router replaces only Model.
	raw map[string]json.RawMessage
}

type EmbeddingResponse struct {
	Object string         `json:"object,omitempty"`
	Model  string         `json:"model"`
	Data   []EmbeddingVec `json:"data"`
	Usage  Usage          `json:"usage"`

	// raw permits base64 embeddings and future response fields to pass through
	// even when Data only has a typed float-vector representation for adapters.
	raw map[string]json.RawMessage
}

type EmbeddingVec struct {
	Object    string    `json:"object,omitempty"`
	Index     int       `json:"index"`
	Embedding []float32 `json:"embedding"`
}

type Usage struct {
	PromptTokens             int                      `json:"prompt_tokens"`
	CompletionTokens         int                      `json:"completion_tokens"`
	TotalTokens              int                      `json:"total_tokens"`
	PromptTokensDetails      *PromptTokensDetails     `json:"prompt_tokens_details,omitempty"`
	CompletionTokensDetails  *CompletionTokensDetails `json:"completion_tokens_details,omitempty"`
	CacheCreationInputTokens int                      `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int                      `json:"cache_read_input_tokens,omitempty"`
	CacheCreation            *CacheCreationDetails    `json:"cache_creation,omitempty"`
}

// ReportedUsage distinguishes a genuinely reported zero from missing counters.
// A malformed/negative usage object is uncertainty, never evidence of free work.
func ReportedUsage(raw json.RawMessage, completion bool) bool {
	names := []string{"prompt_tokens"}
	if completion {
		names = append(names, "completion_tokens")
	}
	return reportedCounters(raw, names...)
}

func reportedCounters(raw json.RawMessage, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for _, name := range names {
		value, ok := fields[name]
		var n int
		if !ok || string(value) == "null" || json.Unmarshal(value, &n) != nil || n < 0 {
			return false
		}
	}
	return true
}

func (r *ChatResponse) UsageReported() bool {
	if r.raw != nil {
		return ReportedUsage(r.raw["usage"], true)
	}
	return r.Usage.PromptTokens >= 0 && r.Usage.CompletionTokens >= 0 && (r.Usage.PromptTokens > 0 || r.Usage.CompletionTokens > 0)
}
func (r *EmbeddingResponse) UsageReported() bool {
	if r.raw != nil {
		return ReportedUsage(r.raw["usage"], false)
	}
	return r.Usage.PromptTokens > 0
}

type CacheCreationDetails struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
	AudioTokens  int `json:"audio_tokens,omitempty"`
}

type CompletionTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	AudioTokens     int `json:"audio_tokens,omitempty"`
}

// UnmarshalJSON captures the entire request and also extracts the small typed
// projection used by routing, admission, guardrails, and translating adapters.
func (r *ChatRequest) UnmarshalJSON(data []byte) error {
	type requestWire struct {
		Model               string         `json:"model"`
		Messages            []ChatMessage  `json:"messages"`
		Stream              bool           `json:"stream,omitempty"`
		StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
		Temperature         *float64       `json:"temperature,omitempty"`
		MaxTokens           *int           `json:"max_tokens,omitempty"`
		MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
		User                string         `json:"user,omitempty"`
	}
	var wire requestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = ChatRequest{
		Model:               wire.Model,
		Messages:            wire.Messages,
		Stream:              wire.Stream,
		StreamOptions:       wire.StreamOptions,
		Temperature:         wire.Temperature,
		MaxTokens:           wire.MaxTokens,
		MaxCompletionTokens: wire.MaxCompletionTokens,
		User:                wire.User,
		raw:                 raw,
	}
	return nil
}

// MarshalJSON overlays fields GateMux is allowed to modify on the original
// request. Unknown fields are forwarded byte-for-byte at the JSON-value level.
func (r ChatRequest) MarshalJSON() ([]byte, error) {
	if r.raw == nil {
		type plain ChatRequest
		return json.Marshal(plain(r))
	}
	raw := cloneRawMap(r.raw)
	if err := putJSON(raw, "model", r.Model); err != nil {
		return nil, err
	}
	if r.Messages != nil {
		if err := putJSON(raw, "messages", r.Messages); err != nil {
			return nil, err
		}
	}
	if r.Stream || raw["stream"] != nil {
		if err := putJSON(raw, "stream", r.Stream); err != nil {
			return nil, err
		}
	}
	if r.StreamOptions != nil {
		var options map[string]json.RawMessage
		if existing := raw["stream_options"]; existing != nil {
			_ = json.Unmarshal(existing, &options)
		}
		if options == nil {
			options = map[string]json.RawMessage{}
		}
		if err := putJSON(options, "include_usage", r.StreamOptions.IncludeUsage); err != nil {
			return nil, err
		}
		if err := putJSON(raw, "stream_options", options); err != nil {
			return nil, err
		}
	}
	if r.Temperature != nil {
		if err := putJSON(raw, "temperature", r.Temperature); err != nil {
			return nil, err
		}
	}
	if r.MaxTokens != nil {
		if err := putJSON(raw, "max_tokens", r.MaxTokens); err != nil {
			return nil, err
		}
	}
	if r.MaxCompletionTokens != nil {
		if err := putJSON(raw, "max_completion_tokens", r.MaxCompletionTokens); err != nil {
			return nil, err
		}
	}
	if r.User != "" {
		if err := putJSON(raw, "user", r.User); err != nil {
			return nil, err
		}
	}
	return json.Marshal(raw)
}

// OutputTokenLimit is shared by translating adapters and token admission.
func (r *ChatRequest) OutputTokenLimit() *int {
	if r.MaxCompletionTokens != nil {
		return r.MaxCompletionTokens
	}
	return r.MaxTokens
}

// BoundCustomerCompletion gives cost/TPM-controlled requests an explicit output
// ceiling. Compatible adapters preserve the client's modern or legacy field.
func (r *ChatRequest) BoundCustomerCompletion() (int, error) {
	if r.MaxTokens != nil && r.MaxCompletionTokens != nil {
		return 0, fmt.Errorf("specify only one of max_tokens and max_completion_tokens")
	}
	if r.OutputTokenLimit() == nil {
		limit := 1024
		r.MaxCompletionTokens = &limit
	}
	limit := *r.OutputTokenLimit()
	if limit < 1 || limit > 1000000 {
		return 0, fmt.Errorf("customer-controlled completion limit must be 1–1000000")
	}
	n := 1
	if value := r.raw["n"]; len(value) > 0 && string(value) != "null" {
		if err := json.Unmarshal(value, &n); err != nil || n < 1 || n > 128 {
			return 0, fmt.Errorf("n must be 1–128 for customer-controlled requests")
		}
	}
	return limit * n, nil
}

func (m *ChatMessage) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var role string
	if value := raw["role"]; value != nil {
		if err := json.Unmarshal(value, &role); err != nil {
			return fmt.Errorf("message role: %w", err)
		}
	}
	content, err := textContent(raw["content"])
	if err != nil {
		return err
	}
	*m = ChatMessage{
		Role:            role,
		Content:         content,
		raw:             raw,
		originalRole:    role,
		originalContent: content,
	}
	return nil
}

func (m ChatMessage) MarshalJSON() ([]byte, error) {
	if m.raw == nil {
		type plain ChatMessage
		return json.Marshal(plain(m))
	}
	raw := cloneRawMap(m.raw)
	if m.Role != m.originalRole {
		if err := putJSON(raw, "role", m.Role); err != nil {
			return nil, err
		}
	}
	// Preserve arrays and null exactly unless a translating adapter or a
	// guardrail intentionally replaced the text projection.
	if m.Content != m.originalContent {
		if err := putJSON(raw, "content", m.Content); err != nil {
			return nil, err
		}
	}
	return json.Marshal(raw)
}

func (r *ChatResponse) UnmarshalJSON(data []byte) error {
	type responseWire struct {
		ID      string       `json:"id"`
		Model   string       `json:"model"`
		Choices []ChatChoice `json:"choices"`
		Usage   Usage        `json:"usage"`
	}
	var wire responseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = ChatResponse{ID: wire.ID, Model: wire.Model, Choices: wire.Choices, Usage: wire.Usage, raw: raw}
	return nil
}

func (r ChatResponse) MarshalJSON() ([]byte, error) {
	if r.raw == nil {
		type plain ChatResponse
		return json.Marshal(plain(r))
	}
	raw := cloneRawMap(r.raw)
	if err := putJSON(raw, "model", r.Model); err != nil {
		return nil, err
	}
	return json.Marshal(raw)
}

func (r *EmbeddingRequest) UnmarshalJSON(data []byte) error {
	type requestWire struct {
		Model string         `json:"model"`
		Input EmbeddingInput `json:"input"`
		User  string         `json:"user,omitempty"`
	}
	var wire requestWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*r = EmbeddingRequest{Model: wire.Model, Input: wire.Input, User: wire.User, raw: raw}
	return nil
}

func (r EmbeddingRequest) MarshalJSON() ([]byte, error) {
	if r.raw == nil {
		type plain EmbeddingRequest
		return json.Marshal(plain(r))
	}
	raw := cloneRawMap(r.raw)
	if err := putJSON(raw, "model", r.Model); err != nil {
		return nil, err
	}
	return json.Marshal(raw)
}

func (r *EmbeddingResponse) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var wire struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Usage  Usage  `json:"usage"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	var vectors []EmbeddingVec
	// A base64 encoding is a valid OpenAI response. Decode float vectors when
	// present for internal adapters, but never fail the whole response merely
	// because the wire representation is a string.
	if dataField := raw["data"]; dataField != nil {
		_ = json.Unmarshal(dataField, &vectors)
	}
	*r = EmbeddingResponse{Object: wire.Object, Model: wire.Model, Data: vectors, Usage: wire.Usage, raw: raw}
	return nil
}

func (r EmbeddingResponse) MarshalJSON() ([]byte, error) {
	if r.raw == nil {
		type plain EmbeddingResponse
		return json.Marshal(plain(r))
	}
	raw := cloneRawMap(r.raw)
	if err := putJSON(raw, "model", r.Model); err != nil {
		return nil, err
	}
	return json.Marshal(raw)
}

func cloneRawMap(src map[string]json.RawMessage) map[string]json.RawMessage {
	dst := make(map[string]json.RawMessage, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func putJSON(dst map[string]json.RawMessage, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	dst[key] = encoded
	return nil
}

func textContent(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("message content must be a string, array, or null")
	}
	var out string
	for _, part := range parts {
		for _, key := range []string{"text", "content"} {
			var value string
			if field := part[key]; field != nil && json.Unmarshal(field, &value) == nil {
				if out != "" {
					out += "\n"
				}
				out += value
				break
			}
		}
	}
	return out, nil
}
