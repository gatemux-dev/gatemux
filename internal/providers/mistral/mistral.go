// Package mistral wraps Mistral's OpenAI-compatible API. The wire format
// matches OpenAI; we keep a distinct provider type so capabilities,
// pricing, and routing tags can be tracked per-vendor.
package mistral

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
)

const defaultBaseURL = "https://api.mistral.ai/v1"

type Client struct {
	*openai.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{Client: openai.New(apiKey, baseURL)}
}

func (c *Client) Type() string { return "mistral" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}
