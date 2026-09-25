package bedrock

import (
	"fmt"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

func canonicalUsage(u *types.TokenUsage) (providers.Usage, error) {
	if u == nil {
		return providers.Usage{}, nil
	}
	native := providers.AnthropicUsage{InputTokens: int(aws.ToInt32(u.InputTokens)), OutputTokens: int(aws.ToInt32(u.OutputTokens)),
		CacheCreationInputTokens: int(aws.ToInt32(u.CacheWriteInputTokens)), CacheReadInputTokens: int(aws.ToInt32(u.CacheReadInputTokens))}
	if len(u.CacheDetails) > 0 {
		native.CacheCreation = &providers.CacheCreationDetails{}
		seen := make(map[types.CacheTTL]bool, 2)
		for _, detail := range u.CacheDetails {
			if seen[detail.Ttl] {
				return providers.Usage{}, fmt.Errorf("duplicate Bedrock cache TTL usage")
			}
			seen[detail.Ttl] = true
			switch detail.Ttl {
			case types.CacheTTLFiveMinutes:
				native.CacheCreation.Ephemeral5mInputTokens = int(aws.ToInt32(detail.InputTokens))
			case types.CacheTTLOneHour:
				native.CacheCreation.Ephemeral1hInputTokens = int(aws.ToInt32(detail.InputTokens))
			default:
				return providers.Usage{}, fmt.Errorf("unsupported Bedrock cache TTL usage")
			}
		}
	}
	return native.Canonical()
}
