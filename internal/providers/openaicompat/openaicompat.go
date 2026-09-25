package openaicompat

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
)

type Client struct {
	*openai.Client
}

func New(apiKey, baseURL string) *Client {
	return &Client{Client: openai.New(apiKey, baseURL)}
}

func (c *Client) Type() string { return "openai_compatible" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true, Responses: true}
}
