// Package gemini implements the direct Google Gemini API (not Vertex).
// Auth is API-key based, endpoint is generativelanguage.googleapis.com.
// The shape is Google's own (not OpenAI), so we transform request and
// response bidirectionally and translate streaming SSE chunks into
// OpenAI-shaped delta chunks for downstream consumers.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

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

func (c *Client) Type() string { return "gemini" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}

// Gemini uses `contents` (a list of role+parts entries) and a separate
// `systemInstruction` for system messages. We map OpenAI's flat message
// list onto that shape.
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiSystem struct {
	Parts []geminiPart `json:"parts"`
}

type geminiGenerationConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type geminiRequest struct {
	Contents          []geminiContent         `json:"contents"`
	SystemInstruction *geminiSystem           `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig `json:"generationConfig,omitempty"`
}

type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata providers.GeminiUsage `json:"usageMetadata"`
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	body, err := buildGeminiRequest(req)
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, fmt.Sprintf("/models/%s:generateContent", req.Model), body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: "gemini", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	var gr geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, fmt.Errorf("decode gemini response: %w", err)
	}
	out := &providers.ChatResponse{ID: "gemini-" + req.Model, Model: req.Model}
	for i, cand := range gr.Candidates {
		out.Choices = append(out.Choices, providers.ChatChoice{
			Index: i,
			Message: providers.ChatMessage{
				Role:    "assistant",
				Content: joinParts(cand.Content.Parts),
			},
			FinishReason: cand.FinishReason,
		})
	}
	out.Usage, err = gr.UsageMetadata.Canonical()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	body, err := buildGeminiRequest(req)
	if err != nil {
		return nil, err
	}
	// streamGenerateContent returns SSE; we translate Gemini chunks into
	// OpenAI-shaped delta chunks downstream consumers expect.
	resp, err := c.do(ctx, fmt.Sprintf("/models/%s:streamGenerateContent?alt=sse", req.Model), body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{Provider: "gemini", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	pr, pw := io.Pipe()
	go providers.TranslateGeminiStream(resp.Body, pw, req.Model)
	return pr, nil
}

type geminiEmbedRequest struct {
	Content geminiContent `json:"content"`
}

type geminiEmbedResponse struct {
	Embedding struct {
		Values []float32 `json:"values"`
	} `json:"embedding"`
}

func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	if req.Input.IsTokenized() {
		return nil, providers.UnsupportedTokenInput(c.Type())
	}
	out := &providers.EmbeddingResponse{Object: "list", Model: req.Model}
	for i, input := range req.Input.Texts {
		body := geminiEmbedRequest{Content: geminiContent{Parts: []geminiPart{{Text: input}}}}
		resp, err := c.do(ctx, fmt.Sprintf("/models/%s:embedContent", req.Model), body)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			raw := providers.ReadErrorBody(resp.Body)
			resp.Body.Close()
			return nil, &providers.UpstreamError{Provider: "gemini", StatusCode: resp.StatusCode, Message: string(raw)}
		}
		var er geminiEmbedResponse
		if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("decode gemini embed: %w", err)
		}
		resp.Body.Close()
		out.Data = append(out.Data, providers.EmbeddingVec{Index: i, Embedding: er.Embedding.Values, Object: "embedding"})
	}
	return out, nil
}

func buildGeminiRequest(req *providers.ChatRequest) (*geminiRequest, error) {
	gr := &geminiRequest{}
	for _, m := range req.Messages {
		if m.Role == "system" {
			if gr.SystemInstruction == nil {
				gr.SystemInstruction = &geminiSystem{}
			}
			gr.SystemInstruction.Parts = append(gr.SystemInstruction.Parts, geminiPart{Text: m.Content})
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		gr.Contents = append(gr.Contents, geminiContent{Role: role, Parts: []geminiPart{{Text: m.Content}}})
	}
	if req.Temperature != nil || req.OutputTokenLimit() != nil {
		gc := &geminiGenerationConfig{}
		if req.Temperature != nil {
			gc.Temperature = req.Temperature
		}
		if req.OutputTokenLimit() != nil {
			gc.MaxOutputTokens = req.OutputTokenLimit()
		}
		gr.GenerationConfig = gc
	}
	return gr, nil
}

func (c *Client) do(ctx context.Context, path string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}
	url := c.baseURL + path
	if strings.Contains(url, "?") {
		url += "&key=" + c.apiKey
	} else {
		url += "?key=" + c.apiKey
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}

func joinParts(parts []geminiPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}
