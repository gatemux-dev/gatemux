// Package vertex implements Google Vertex AI as a GateMux provider.
// Vertex hosts both Gemini and Anthropic Claude models behind the same
// region+project endpoint — we route on the upstream_model prefix:
//
//	gemini-*  → Vertex Gemini predict API (rawPredict shape)
//	claude-*  → Anthropic Messages on Vertex
//
// Auth uses GCP application-default credentials (service account JSON
// or attached compute identity). The deployment row carries
// `region` and `provider_config.gcp_project_id`.
package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/anthropic"
)

const scope = "https://www.googleapis.com/auth/cloud-platform"

type Client struct {
	region    string
	projectID string
	http      *http.Client
}

// New constructs a Vertex client. The OAuth2 token source auto-refreshes
// so we don't have to manage credential lifetime in the request path.
func New(ctx context.Context, region, projectID string) (*Client, error) {
	if region == "" {
		region = "us-central1"
	}
	if projectID == "" {
		return nil, errors.New("vertex: project_id required (set via deployment metadata)")
	}
	creds, err := google.FindDefaultCredentials(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("vertex auth: %w", err)
	}
	// oauth2.Transport wraps DefaultTransport with a TokenSource that
	// auto-refreshes — exactly what we want for long-lived gateway
	// processes hitting Vertex on every request.
	httpClient := &http.Client{
		Transport: &oauth2.Transport{
			Source: creds.TokenSource,
			Base:   providers.SharedHTTPClient.Transport,
		},
		CheckRedirect: providers.SharedHTTPClient.CheckRedirect,
	}
	return &Client{region: region, projectID: projectID, http: httpClient}, nil
}

func (c *Client) Type() string { return "vertex" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}

func (c *Client) endpoint() string {
	return fmt.Sprintf("https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s",
		c.region, c.projectID, c.region)
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	if isAnthropic(req.Model) {
		return c.anthropicChat(ctx, req, false)
	}
	return c.geminiChat(ctx, req)
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	if isAnthropic(req.Model) {
		return c.anthropicStream(ctx, req)
	}
	return c.geminiStream(ctx, req)
}

func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	// Vertex embeddings have a separate per-model URL; for Phase 1 we
	// punt and let the openai_compatible adapter or direct Gemini handle
	// them. Returning a clean error keeps the router behavior predictable.
	return nil, &providers.UpstreamError{
		Provider:   "vertex",
		StatusCode: 501,
		Message:    "vertex embeddings not yet supported in this adapter",
	}
}

func isAnthropic(model string) bool {
	return strings.HasPrefix(model, "claude-")
}

// ---- Gemini-on-Vertex ----

type vertexGeminiRequest struct {
	Contents          []vertexGeminiContent `json:"contents"`
	SystemInstruction *vertexGeminiContent  `json:"systemInstruction,omitempty"`
	GenerationConfig  *vertexGeminiConfig   `json:"generationConfig,omitempty"`
}

type vertexGeminiContent struct {
	Role  string             `json:"role,omitempty"`
	Parts []vertexGeminiPart `json:"parts"`
}

type vertexGeminiPart struct {
	Text string `json:"text"`
}

type vertexGeminiConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	MaxOutputTokens *int     `json:"maxOutputTokens,omitempty"`
}

type vertexGeminiResponse struct {
	Candidates []struct {
		Content      vertexGeminiContent `json:"content"`
		FinishReason string              `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata providers.GeminiUsage `json:"usageMetadata"`
}

func (c *Client) geminiChat(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	body := buildVertexGeminiRequest(req)
	url := fmt.Sprintf("%s/publishers/google/models/%s:generateContent", c.endpoint(), req.Model)
	resp, err := c.post(ctx, url, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: "vertex", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	var vr vertexGeminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&vr); err != nil {
		return nil, fmt.Errorf("decode vertex response: %w", err)
	}
	out := &providers.ChatResponse{ID: "vertex-" + req.Model, Model: req.Model}
	for i, cand := range vr.Candidates {
		out.Choices = append(out.Choices, providers.ChatChoice{
			Index: i,
			Message: providers.ChatMessage{
				Role:    "assistant",
				Content: joinVertexParts(cand.Content.Parts),
			},
			FinishReason: cand.FinishReason,
		})
	}
	out.Usage, err = vr.UsageMetadata.Canonical()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) geminiStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	body := buildVertexGeminiRequest(req)
	url := fmt.Sprintf("%s/publishers/google/models/%s:streamGenerateContent?alt=sse", c.endpoint(), req.Model)
	resp, err := c.post(ctx, url, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{Provider: "vertex", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	pr, pw := io.Pipe()
	go providers.TranslateGeminiStream(resp.Body, pw, req.Model)
	return pr, nil
}

func buildVertexGeminiRequest(req *providers.ChatRequest) *vertexGeminiRequest {
	out := &vertexGeminiRequest{}
	for _, m := range req.Messages {
		if m.Role == "system" {
			if out.SystemInstruction == nil {
				out.SystemInstruction = &vertexGeminiContent{}
			}
			out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, vertexGeminiPart{Text: m.Content})
			continue
		}
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		}
		out.Contents = append(out.Contents, vertexGeminiContent{Role: role, Parts: []vertexGeminiPart{{Text: m.Content}}})
	}
	if req.Temperature != nil || req.OutputTokenLimit() != nil {
		gc := &vertexGeminiConfig{}
		if req.Temperature != nil {
			gc.Temperature = req.Temperature
		}
		if req.OutputTokenLimit() != nil {
			gc.MaxOutputTokens = req.OutputTokenLimit()
		}
		out.GenerationConfig = gc
	}
	return out
}

func joinVertexParts(parts []vertexGeminiPart) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// ---- Anthropic-on-Vertex ----

type anthropicVertexRequest struct {
	AnthropicVersion string                   `json:"anthropic_version"`
	Messages         []anthropicVertexMessage `json:"messages"`
	System           string                   `json:"system,omitempty"`
	MaxTokens        int                      `json:"max_tokens"`
	Temperature      *float64                 `json:"temperature,omitempty"`
	Stream           bool                     `json:"stream,omitempty"`
}

type anthropicVertexMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicVertexResponse struct {
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string                   `json:"stop_reason"`
	Usage      providers.AnthropicUsage `json:"usage"`
}

func (c *Client) anthropicChat(ctx context.Context, req *providers.ChatRequest, _ bool) (*providers.ChatResponse, error) {
	body := buildAnthropicVertexRequest(req, false)
	url := fmt.Sprintf("%s/publishers/anthropic/models/%s:rawPredict", c.endpoint(), req.Model)
	resp, err := c.post(ctx, url, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: "vertex", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	var ar anthropicVertexResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("decode anthropic-on-vertex response: %w", err)
	}
	out := &providers.ChatResponse{ID: ar.ID, Model: req.Model}
	var text strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	out.Choices = []providers.ChatChoice{
		{Index: 0, Message: providers.ChatMessage{Role: "assistant", Content: text.String()}, FinishReason: ar.StopReason},
	}
	out.Usage, err = ar.Usage.Canonical()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) anthropicStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	body := buildAnthropicVertexRequest(req, true)
	url := fmt.Sprintf("%s/publishers/anthropic/models/%s:streamRawPredict", c.endpoint(), req.Model)
	resp, err := c.post(ctx, url, body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		raw := providers.ReadErrorBody(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{Provider: "vertex", StatusCode: resp.StatusCode, Message: string(raw)}
	}
	pr, pw := io.Pipe()
	go anthropic.TranslateStream(resp.Body, pw)
	return pr, nil
}

func buildAnthropicVertexRequest(req *providers.ChatRequest, stream bool) *anthropicVertexRequest {
	maxTokens := 1024
	if req.OutputTokenLimit() != nil {
		maxTokens = *req.OutputTokenLimit()
	}
	out := &anthropicVertexRequest{
		AnthropicVersion: "vertex-2023-10-16",
		MaxTokens:        maxTokens,
		Stream:           stream,
		Temperature:      req.Temperature,
	}
	for _, m := range req.Messages {
		if m.Role == "system" {
			if out.System != "" {
				out.System += "\n\n"
			}
			out.System += m.Content
			continue
		}
		out.Messages = append(out.Messages, anthropicVertexMessage{Role: m.Role, Content: m.Content})
	}
	return out
}

// ---- HTTP helpers ----

func (c *Client) post(ctx context.Context, url string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode vertex request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}
