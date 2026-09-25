// Package bedrock implements an AWS Bedrock provider adapter using the
// unified Converse / ConverseStream APIs. Converse normalizes request +
// response shape across model families (Anthropic Claude, Meta Llama,
// Amazon Titan, Cohere Command, Mistral) so we don't have to ship a
// per-family request transform like the legacy InvokeModel path does.
//
// SigV4 auth, region routing, and the AWS event-stream binary protocol
// for streaming responses are handled by aws-sdk-go-v2; we only deal in
// the high-level Converse type.
package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

type Client struct {
	br     *bedrockruntime.Client
	region string
}

// New constructs a Bedrock client. Credentials are resolved through the
// default AWS provider chain (env, profile, IAM role) so this works in
// EC2 / ECS / Lambda without explicit keys. Region is required and
// passed via the deployment row.
func New(ctx context.Context, region string) (*Client, error) {
	if region == "" {
		region = "us-east-1"
	}
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithHTTPClient(providers.NewSDKHTTPClient()))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &Client{
		br:     bedrockruntime.NewFromConfig(cfg),
		region: region,
	}, nil
}

func (c *Client) Type() string { return "bedrock" }

func (c *Client) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}

func (c *Client) ChatCompletion(ctx context.Context, req *providers.ChatRequest) (*providers.ChatResponse, error) {
	in := buildConverseInput(req)
	out, err := c.br.Converse(ctx, in)
	if err != nil {
		return nil, wrapError(err)
	}
	return convertConverseOutput(req, out)
}

func (c *Client) ChatCompletionStream(ctx context.Context, req *providers.ChatRequest) (io.ReadCloser, error) {
	in := buildConverseStreamInput(req)
	stream, err := c.br.ConverseStream(ctx, in)
	if err != nil {
		return nil, wrapError(err)
	}
	pr, pw := io.Pipe()
	go translateStream(stream, pw, req.Model)
	return pr, nil
}

// Embeddings is a stub today — Bedrock embeddings (Titan, Cohere) need
// the legacy InvokeModel path with per-family request shapes. Return
// "not supported" so the router falls through to a different deployment
// rather than half-failing.
func (c *Client) Embeddings(ctx context.Context, req *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return nil, &providers.UpstreamError{
		Provider:   "bedrock",
		StatusCode: 501,
		Message:    "bedrock embeddings not yet supported in this adapter",
	}
}

func buildConverseInput(req *providers.ChatRequest) *bedrockruntime.ConverseInput {
	system, messages := splitSystem(req.Messages)
	in := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(req.Model),
		Messages: messages,
	}
	if len(system) > 0 {
		in.System = system
	}
	cfg := inferenceConfig(req)
	if cfg != nil {
		in.InferenceConfig = cfg
	}
	return in
}

func buildConverseStreamInput(req *providers.ChatRequest) *bedrockruntime.ConverseStreamInput {
	system, messages := splitSystem(req.Messages)
	in := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(req.Model),
		Messages: messages,
	}
	if len(system) > 0 {
		in.System = system
	}
	cfg := inferenceConfig(req)
	if cfg != nil {
		in.InferenceConfig = cfg
	}
	return in
}

func inferenceConfig(req *providers.ChatRequest) *types.InferenceConfiguration {
	if req.Temperature == nil && req.OutputTokenLimit() == nil {
		return nil
	}
	cfg := &types.InferenceConfiguration{}
	if req.Temperature != nil {
		t := float32(*req.Temperature)
		cfg.Temperature = &t
	}
	if req.OutputTokenLimit() != nil {
		cfg.MaxTokens = aws.Int32(int32(*req.OutputTokenLimit()))
	}
	return cfg
}

// splitSystem moves system-role messages out of the main message list
// (Bedrock Converse takes them as a separate `System` field) and maps
// the remaining messages to Converse's role+content shape.
func splitSystem(msgs []providers.ChatMessage) ([]types.SystemContentBlock, []types.Message) {
	var system []types.SystemContentBlock
	out := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			system = append(system, &types.SystemContentBlockMemberText{Value: m.Content})
			continue
		}
		role := types.ConversationRoleUser
		if m.Role == "assistant" {
			role = types.ConversationRoleAssistant
		}
		out = append(out, types.Message{
			Role:    role,
			Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: m.Content}},
		})
	}
	return system, out
}

func convertConverseOutput(req *providers.ChatRequest, out *bedrockruntime.ConverseOutput) (*providers.ChatResponse, error) {
	resp := &providers.ChatResponse{
		ID:    "bedrock-" + req.Model,
		Model: req.Model,
	}
	if out.Output != nil {
		if msg, ok := out.Output.(*types.ConverseOutputMemberMessage); ok {
			text := joinText(msg.Value.Content)
			resp.Choices = []providers.ChatChoice{
				{
					Index:        0,
					Message:      providers.ChatMessage{Role: "assistant", Content: text},
					FinishReason: string(out.StopReason),
				},
			}
		}
	}
	if out.Usage != nil {
		var err error
		resp.Usage, err = canonicalUsage(out.Usage)
		if err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func joinText(blocks []types.ContentBlock) string {
	var b strings.Builder
	for _, blk := range blocks {
		if t, ok := blk.(*types.ContentBlockMemberText); ok {
			b.WriteString(t.Value)
		}
	}
	return b.String()
}

func wrapError(err error) error {
	if err == nil {
		return nil
	}
	// AWS errors implement smithy.APIError; map status from common types.
	msg := err.Error()
	status := 500
	if strings.Contains(msg, "ThrottlingException") {
		status = 429
	} else if strings.Contains(msg, "ValidationException") {
		status = 400
	} else if strings.Contains(msg, "AccessDeniedException") {
		status = 403
	} else if strings.Contains(msg, "ResourceNotFoundException") {
		status = 404
	}
	return &providers.UpstreamError{Provider: "bedrock", StatusCode: status, Message: msg}
}

// suppress unused-import for errors when no error-wrapping path uses it.
var _ = errors.New
