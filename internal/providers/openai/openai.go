package openai

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

const defaultBaseURL = "https://api.openai.com/v1"

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

func (c *Client) Type() string { return "openai" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true, Responses: true}
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	resp, err := c.postChat(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: c.Type(), StatusCode: resp.StatusCode, Message: string(buf)}
	}
	var out providers.ChatResponse
	if limit := providers.ResponseLimit(ctx); limit > 0 {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > limit {
			return nil, fmt.Errorf("protected response exceeds byte limit")
		}
		if err = json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("decode protected response: %w", err)
		}
		return &out, nil
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	resp, err := c.postChat(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		buf := providers.ReadErrorBody(resp.Body)
		resp.Body.Close()
		return nil, &providers.UpstreamError{Provider: c.Type(), StatusCode: resp.StatusCode, Message: string(buf)}
	}
	return resp.Body, nil
}

func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		buf := providers.ReadErrorBody(resp.Body)
		return nil, &providers.UpstreamError{Provider: c.Type(), StatusCode: resp.StatusCode, Message: string(buf)}
	}

	var out providers.EmbeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &out, nil
}

func (c *Client) postChat(ctx context.Context, req *providers.ChatRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	return c.http.Do(httpReq)
}
