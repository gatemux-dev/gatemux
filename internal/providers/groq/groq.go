// Package groq wraps Groq's OpenAI-compatible chat API. Embeddings are
// not currently exposed by Groq, so the capability set advertises chat
// only — the router will skip groq deployments for embedding requests.
package groq

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/openai"
)

const defaultBaseURL = "https://api.groq.com/openai/v1"

type Client struct {
	*openai.Client
}

func New(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{Client: openai.New(apiKey, baseURL)}
}

func (c *Client) Type() string { return "groq" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: false}
}
