// Package openrouter wraps OpenRouter's OpenAI-compatible API.
// OpenRouter namespaces model IDs as "<vendor>/<model>"; gatemux keeps that
// convention in the deployment's upstream_model field.
package openrouter

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

type Client struct {
	*openai.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{Client: openai.New(apiKey, baseURL)}
}

func (c *Client) Type() string { return "openrouter" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: false}
}
