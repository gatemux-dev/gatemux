package budget

import (
	"fmt"
	"math"
	"math/big"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func tierRate(override *int64, fallback int64) int64 {
	if override == nil {
		return fallback
	}
	return *override
}

// CostUsage partitions authoritative input/output totals into disjoint tiers.
// OpenAI cached/reasoning detail counts are subsets, never extra tokens.
// Native adapters must normalize their protocol's totals before calling this.
// Round once per direction (not per tier), preserving legacy whole-cent billing.
func CostUsage(p *store.Pricing, u providers.Usage) (int64, error) {
	if p == nil {
		return 0, fmt.Errorf("pricing unavailable")
	}
	read, write, write1h, reasoning := u.CacheReadInputTokens, u.CacheCreationInputTokens, 0, 0
	if read < 0 || write < 0 {
		return 0, fmt.Errorf("negative cache usage")
	}
	if u.PromptTokensDetails != nil {
		cached := u.PromptTokensDetails.CachedTokens
		if cached < 0 || (read > 0 && cached > 0 && cached != read) {
			return 0, fmt.Errorf("inconsistent cached input usage")
		}
		read = max(read, cached)
	}
	if u.CacheCreation != nil {
		write1h = u.CacheCreation.Ephemeral1hInputTokens
		write5m := u.CacheCreation.Ephemeral5mInputTokens
		if write1h < 0 || write5m < 0 || write1h > math.MaxInt-write5m {
			return 0, fmt.Errorf("invalid cache write usage")
		}
		if write == 0 {
			write = write1h + write5m
		} else if write != write1h+write5m {
			return 0, fmt.Errorf("inconsistent cache write usage")
		}
	}
	if u.CompletionTokensDetails != nil {
		reasoning = u.CompletionTokensDetails.ReasoningTokens
	}
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || read < 0 || write < 0 || read > u.PromptTokens || write > u.PromptTokens-read || reasoning < 0 || reasoning > u.CompletionTokens {
		return 0, fmt.Errorf("token detail counts exceed authoritative totals")
	}
	input, err := weightedCents([]tokenTier{
		{u.PromptTokens - read - write, p.InputPerMillionCents},
		{read, tierRate(p.Tiers.CacheReadPerMillionCents, p.InputPerMillionCents)},
		{write - write1h, tierRate(p.Tiers.CacheWritePerMillionCents, p.InputPerMillionCents)},
		{write1h, tierRate(p.Tiers.CacheWrite1hPerMillionCents, tierRate(p.Tiers.CacheWritePerMillionCents, p.InputPerMillionCents))},
	})
	if err != nil {
		return 0, err
	}
	output, err := weightedCents([]tokenTier{{u.CompletionTokens - reasoning, p.OutputPerMillionCents}, {reasoning, tierRate(p.Tiers.ReasoningPerMillionCents, p.OutputPerMillionCents)}})
	if err != nil {
		return 0, err
	}
	if input > math.MaxInt64-output {
		return 0, fmt.Errorf("token cost exceeds int64 cents")
	}
	return input + output, nil
}

type tokenTier struct {
	count int
	rate  int64
}

func weightedCents(tiers []tokenTier) (int64, error) {
	var sum, product, count, rate big.Int
	for _, tier := range tiers {
		if tier.count < 0 || tier.rate < 0 {
			return 0, fmt.Errorf("negative token count or rate")
		}
		count.SetInt64(int64(tier.count))
		rate.SetInt64(tier.rate)
		product.Mul(&count, &rate)
		sum.Add(&sum, &product)
	}
	if sum.Sign() == 0 {
		return 0, nil
	}
	sum.Add(&sum, big.NewInt(999999))
	sum.Quo(&sum, big.NewInt(1000000))
	if !sum.IsInt64() {
		return 0, fmt.Errorf("token cost exceeds int64 cents")
	}
	return sum.Int64(), nil
}

// Admission cannot predict cache hits, cache writes, or hidden reasoning. Reserve
// at the maximum configured input and output tier over every eligible fallback.
func estimatedTierCost(p *store.Pricing, prompt, completion int) (int64, error) {
	copy := *p
	for _, rate := range []*int64{p.Tiers.CacheReadPerMillionCents, p.Tiers.CacheWritePerMillionCents, p.Tiers.CacheWrite1hPerMillionCents} {
		if rate != nil {
			copy.InputPerMillionCents = max(copy.InputPerMillionCents, *rate)
		}
	}
	copy.OutputPerMillionCents = max(copy.OutputPerMillionCents, tierRate(p.Tiers.ReasoningPerMillionCents, p.OutputPerMillionCents))
	return CostUsage(&copy, providers.Usage{PromptTokens: prompt, CompletionTokens: completion})
}
