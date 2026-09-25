// Package fireworks wraps Fireworks AI's OpenAI-compatible API.
package fireworks

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
)

const defaultBaseURL = "https://api.fireworks.ai/inference/v1"

type Client struct {
	*openai.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{Client: openai.New(apiKey, baseURL)}
}

func (c *Client) Type() string { return "fireworks" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}
