package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

const (
	defaultBaseURL     = "https://api.anthropic.com/v1"
	anthropicVersion   = "2023-06-01"
	defaultMaxTokens   = 1024
	maxScannerTokenLen = 1024 * 1024
)

type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	return &Client{apiKey: apiKey, baseURL: baseURL, http: providers.SharedHTTPClient}
}

func (c *Client) Type() string { return "anthropic" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: false}
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	resp, err := c.postMessage(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: c.Type(), StatusCode: resp.StatusCode, Message: string(buf)}
	}

	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode anthropic response: %w", err)
	}

	usage, err := out.Usage.Canonical()
	if err != nil {
		return nil, fmt.Errorf("decode anthropic usage: %w", err)
	}
	return &providers.ChatResponse{
		ID:    out.ID,
		Model: out.Model,
		Choices: []providers.ChatChoice{{
			Index: 0,
			Message: providers.ChatMessage{
				Role:    "assistant",
				Content: joinTextBlocks(out.Content),
			},
			FinishReason: mapStopReason(out.StopReason),
		}},
		Usage: usage,
	}, nil
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	resp, err := c.postMessage(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		buf := providers.ReadErrorBody(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{Provider: c.Type(), StatusCode: resp.StatusCode, Message: string(buf)}
	}

	pr, pw := io.Pipe()
	go TranslateStream(resp.Body, pw)
	return pr, nil
}

func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return nil, fmt.Errorf("anthropic: embeddings not supported")
}

func (c *Client) postMessage(ctx context.Context, req *providers.ChatRequest, stream bool) (*http.Response, error) {
	payload, err := buildAnthropicRequest(req, stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
	httpReq.Header.Set("content-type", "application/json")
	if stream {
		httpReq.Header.Set("accept", "text/event-stream")
	}
	return c.http.Do(httpReq)
}

// TranslateStream is shared with Anthropic-on-Vertex so both retain cache usage
// and propagate truncated, malformed and provider-error events.
func TranslateStream(body io.ReadCloser, pw *io.PipeWriter) {
	defer body.Close()
	defer pw.Close()

	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64*1024), maxScannerTokenLen)

	var (
		id                string
		model             string
		finishReason      string
		usage             providers.Usage
		sentAssistantRole bool
	)

	writeData := func(v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(pw, "data: %s\n\n", b)
		return err
	}

	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var evt anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			_ = pw.CloseWithError(fmt.Errorf("decode anthropic stream event: %w", err))
			return
		}

		switch evt.Type {
		case "message_start":
			id = evt.Message.ID
			model = evt.Message.Model
			var err error
			usage, err = evt.Message.Usage.Canonical()
			if err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		case "content_block_delta":
			if evt.Delta.Type != "text_delta" || evt.Delta.Text == "" {
				continue
			}
			delta := map[string]any{"content": evt.Delta.Text}
			if !sentAssistantRole {
				delta["role"] = "assistant"
				sentAssistantRole = true
			}
			if err := writeData(openAIChunk(id, model, []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}}, nil)); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
		case "message_delta":
			if evt.Usage.OutputTokens != 0 {
				if err := usage.SetCompletionTokens(evt.Usage.OutputTokens); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
			if mapped := mapStopReason(evt.Delta.StopReason); mapped != "" {
				finishReason = mapped
				if err := writeData(openAIChunk(id, model, []map[string]any{{
					"index":         0,
					"delta":         map[string]any{},
					"finish_reason": finishReason,
				}}, nil)); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
		case "message_stop":
			if usage.TotalTokens == 0 && usage.PromptTokens > 0 {
				usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			}
			if err := writeData(openAIChunk(id, model, []map[string]any{}, &usage)); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			if _, err := io.WriteString(pw, "data: [DONE]\n\n"); err != nil {
				_ = pw.CloseWithError(err)
				return
			}
			return
		case "error":
			_ = pw.CloseWithError(fmt.Errorf("anthropic stream error"))
			return
		}
	}

	if err := sc.Err(); err != nil {
		_ = pw.CloseWithError(err)
	} else {
		_ = pw.CloseWithError(io.ErrUnexpectedEOF)
	}
}

func buildAnthropicRequest(req *providers.ChatRequest, stream bool) (anthropicRequest, error) {
	out := anthropicRequest{
		Model:     req.Model,
		MaxTokens: defaultMaxTokens,
		Stream:    stream,
		Messages:  make([]anthropicMessage, 0, len(req.Messages)),
	}
	if req.OutputTokenLimit() != nil && *req.OutputTokenLimit() > 0 {
		out.MaxTokens = *req.OutputTokenLimit()
	}
	if req.Temperature != nil {
		out.Temperature = req.Temperature
	}

	var systemParts []string
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			if strings.TrimSpace(msg.Content) != "" {
				systemParts = append(systemParts, msg.Content)
			}
		case "assistant":
			out.Messages = append(out.Messages, anthropicMessage{Role: "assistant", Content: msg.Content})
		default:
			out.Messages = append(out.Messages, anthropicMessage{Role: "user", Content: msg.Content})
		}
	}
	if len(systemParts) > 0 {
		out.System = strings.Join(systemParts, "\n\n")
	}
	return out, nil
}

func joinTextBlocks(blocks []anthropicContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "text" && block.Text != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "")
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	default:
		return ""
	}
}

func openAIChunk(id, model string, choices []map[string]any, usage *providers.Usage) map[string]any {
	out := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": 0,
		"model":   model,
		"choices": choices,
	}
	if usage != nil {
		out["usage"] = usage
	}
	return out
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	MaxTokens   int                `json:"max_tokens"`
	Messages    []anthropicMessage `json:"messages"`
	Temperature *float64           `json:"temperature,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID         string                  `json:"id"`
	Model      string                  `json:"model"`
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage = providers.AnthropicUsage

type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Message struct {
		ID    string         `json:"id"`
		Model string         `json:"model"`
		Usage anthropicUsage `json:"usage"`
	} `json:"message"`
	Delta struct {
		Type       string `json:"type"`
		Text       string `json:"text"`
		StopReason string `json:"stop_reason"`
	} `json:"delta"`
	Usage anthropicUsage `json:"usage"`
}
