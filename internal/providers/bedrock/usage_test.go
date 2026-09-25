package bedrock

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"testing"
)

func TestCanonicalCacheUsage(t *testing.T) {
	native := &types.TokenUsage{InputTokens: aws.Int32(10), OutputTokens: aws.Int32(5), CacheReadInputTokens: aws.Int32(20), CacheWriteInputTokens: aws.Int32(30),
		CacheDetails: []types.CacheDetail{{Ttl: types.CacheTTLFiveMinutes, InputTokens: aws.Int32(10)}, {Ttl: types.CacheTTLOneHour, InputTokens: aws.Int32(20)}}}
	u, err := canonicalUsage(native)
	if err != nil || u.PromptTokens != 60 || u.TotalTokens != 65 || u.CacheCreation.Ephemeral1hInputTokens != 20 {
		t.Fatalf("usage=%+v err=%v", u, err)
	}
	native.CacheDetails = append(native.CacheDetails, native.CacheDetails[0])
	if _, err := canonicalUsage(native); err == nil {
		t.Fatal("accepted duplicate TTL")
	}
}
