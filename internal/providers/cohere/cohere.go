// Package cohere wraps Cohere's chat API. Cohere uses a non-OpenAI shape
// for chat (single message + conversation history) so we transform the
// OpenAI-compatible chat completions request into Cohere's `/v2/chat`
// shape and the response back. Embeddings use Cohere's `/v2/embed`.
//
// The streaming format is SSE-with-event-types; we map the chat-content-
// delta events to OpenAI-shaped chunks for downstream consumers.
package cohere

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

const defaultBaseURL = "https://api.cohere.com"

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    providers.SharedHTTPClient,
	}
}

func (c *Client) Type() string { return "cohere" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}

// cohereChatRequest mirrors the V2 chat shape — Cohere accepts a list of
// turn-based messages similar to OpenAI's, but with role names "user",
// "assistant", "system", and "tool".
type cohereChatRequest struct {
	Model       string          `json:"model"`
	Messages    []cohereMessage `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

type cohereMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type cohereChatResponse struct {
	ID      string `json:"id"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	FinishReason string `json:"finish_reason"`
	Usage        struct {
		Tokens struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"tokens"`
	} `json:"usage"`
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	body := cohereChatRequest{
		Model:       req.Model,
		Messages:    toCohereMessages(req.Messages),
		Temperature: req.Temperature,
		MaxTokens:   req.OutputTokenLimit(),
	}
	resp, err := c.do(ctx, "POST", "/v2/chat", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, providerError(resp)
	}
	var cr cohereChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("decode cohere response: %w", err)
	}
	out := &providers.ChatResponse{
		ID:    cr.ID,
		Model: req.Model,
		Choices: []providers.ChatChoice{
			{
				Index: 0,
				Message: providers.ChatMessage{
					Role:    "assistant",
					Content: joinContent(cr.Message.Content),
				},
				FinishReason: cr.FinishReason,
			},
		},
		Usage: providers.Usage{
			PromptTokens:     cr.Usage.Tokens.InputTokens,
			CompletionTokens: cr.Usage.Tokens.OutputTokens,
			TotalTokens:      cr.Usage.Tokens.InputTokens + cr.Usage.Tokens.OutputTokens,
		},
	}
	return out, nil
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	body := cohereChatRequest{
		Model:       req.Model,
		Messages:    toCohereMessages(req.Messages),
		Stream:      true,
		Temperature: req.Temperature,
		MaxTokens:   req.OutputTokenLimit(),
	}
	resp, err := c.do(ctx, "POST", "/v2/chat", body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, providerError(resp)
	}
	pr, pw := io.Pipe()
	go translateStream(resp.Body, pw, req.Model)
	return pr, nil
}

// translateStream converts Cohere's event-type SSE stream into OpenAI-
// shaped delta chunks downstream consumers can pass through.
func translateStream(src io.ReadCloser, dst *io.PipeWriter, model string) {
	defer src.Close()
	defer dst.Close()
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var evt struct {
			Type  string `json:"type"`
			Delta struct {
				Message struct {
					Content struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"delta"`
		}
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			continue
		}
		var chunk map[string]any
		switch evt.Type {
		case "content-delta":
			chunk = openAIChunk(id, model, evt.Delta.Message.Content.Text, "")
		case "message-end":
			chunk = openAIChunk(id, model, "", evt.Delta.FinishReason)
		default:
			continue
		}
		raw, _ := json.Marshal(chunk)
		_, _ = fmt.Fprintf(dst, "data: %s\n\n", raw)
	}
	_, _ = fmt.Fprint(dst, "data: [DONE]\n\n")
}

func openAIChunk(id, model, deltaText, finishReason string) map[string]any {
	choice := map[string]any{"index": 0, "delta": map[string]any{}}
	if deltaText != "" {
		choice["delta"] = map[string]any{"role": "assistant", "content": deltaText}
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	return map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
}

type cohereEmbedRequest struct {
	Model     string   `json:"model"`
	InputType string   `json:"input_type"`
	Texts     []string `json:"texts"`
}

type cohereEmbedResponse struct {
	ID         string `json:"id"`
	Embeddings struct {
		Float [][]float32 `json:"float"`
	} `json:"embeddings"`
	Meta struct {
		BilledUnits struct {
			InputTokens int `json:"input_tokens"`
		} `json:"billed_units"`
	} `json:"meta"`
}

func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	if req.Input.IsTokenized() {
		return nil, providers.UnsupportedTokenInput(c.Type())
	}
	inputs := req.Input.Texts
	body := cohereEmbedRequest{
		Model:     req.Model,
		InputType: "search_document",
		Texts:     inputs,
	}
	resp, err := c.do(ctx, "POST", "/v2/embed", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, providerError(resp)
	}
	var er cohereEmbedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("decode cohere embed: %w", err)
	}
	out := &providers.EmbeddingResponse{
		Object: "list",
		Model:  req.Model,
		Usage:  providers.Usage{PromptTokens: er.Meta.BilledUnits.InputTokens, TotalTokens: er.Meta.BilledUnits.InputTokens},
	}
	for i, e := range er.Embeddings.Float {
		out.Data = append(out.Data, providers.EmbeddingVec{Index: i, Embedding: e, Object: "embedding"})
	}
	return out, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var b io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		b = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, b)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

func toCohereMessages(msgs []providers.ChatMessage) []cohereMessage {
	out := make([]cohereMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, cohereMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

func joinContent(parts []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

func providerError(resp *http.Response) error {
	body := providers.ReadErrorBody(resp.Body)
	return &providers.UpstreamError{Provider: "cohere", StatusCode: resp.StatusCode, Message: string(body)}
}
