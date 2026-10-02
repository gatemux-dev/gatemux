package budget

import (
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
	"math"
	"testing"
)

func pricePointer(value int64) *int64 { return &value }

func TestCostUsagePartitionsAndInherits(t *testing.T) {
	u := providers.Usage{PromptTokens: 1000000, CompletionTokens: 1000000,
		PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 300000}, CacheReadInputTokens: 300000,
		CacheCreationInputTokens: 200000, CacheCreation: &providers.CacheCreationDetails{Ephemeral5mInputTokens: 100000, Ephemeral1hInputTokens: 100000},
		CompletionTokensDetails: &providers.CompletionTokensDetails{ReasoningTokens: 250000}}
	p := &store.Pricing{InputPerMillionCents: 100, OutputPerMillionCents: 200}
	for _, tc := range []struct {
		name  string
		tiers store.PricingTiers
		want  int64
	}{
		{"inherit", store.PricingTiers{}, 300000000},
		// 500k×100 + 300k×10 + 100k×125 + 100k×200 + 750k×200 + 250k×400 µ¢.
		{"distinct", store.PricingTiers{CacheReadPerMillionCents: pricePointer(10), CacheWritePerMillionCents: pricePointer(125), CacheWrite1hPerMillionCents: pricePointer(200), ReasoningPerMillionCents: pricePointer(400)}, 335500000},
		{"explicit free", store.PricingTiers{CacheReadPerMillionCents: pricePointer(0), CacheWritePerMillionCents: pricePointer(0), ReasoningPerMillionCents: pricePointer(0)}, 200000000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p.Tiers = tc.tiers
			got, err := CostUsage(p, u)
			if err != nil || got != tc.want {
				t.Fatalf("cost=%d err=%v want=%d", got, err, tc.want)
			}
		})
	}
	// Fractions of a cent in two input tiers are never rounded.
	p.InputPerMillionCents = 1
	p.Tiers = store.PricingTiers{}
	got, err := CostUsage(p, providers.Usage{PromptTokens: 2, PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 1}})
	if err != nil || got != 2 {
		t.Fatalf("tier exactness: %d %v", got, err)
	}
}

func TestCostUsageExactMicrocents(t *testing.T) {
	p := &store.Pricing{InputPerMillionCents: 10, OutputPerMillionCents: 40}
	got, err := CostUsage(p, providers.Usage{PromptTokens: 100, CompletionTokens: 50})
	if err != nil || got != 3000 {
		t.Fatalf("small request: %d %v", got, err)
	}
	got, err = CostUsage(p, providers.Usage{})
	if err != nil || got != 0 {
		t.Fatalf("zero tokens: %d %v", got, err)
	}
}

func TestCostUsageRejectsInvalidAndOverflow(t *testing.T) {
	p := &store.Pricing{InputPerMillionCents: 100, OutputPerMillionCents: 200}
	for _, u := range []providers.Usage{
		{PromptTokens: -1}, {CompletionTokens: -1},
		{PromptTokens: 1, CacheReadInputTokens: 2},
		{PromptTokens: 3, CacheReadInputTokens: 2, CacheCreationInputTokens: 2},
		{PromptTokens: 3, CacheReadInputTokens: 2, PromptTokensDetails: &providers.PromptTokensDetails{CachedTokens: 1}},
		{PromptTokens: 3, CacheCreationInputTokens: 2, CacheCreation: &providers.CacheCreationDetails{Ephemeral1hInputTokens: 1}},
		{CompletionTokens: 1, CompletionTokensDetails: &providers.CompletionTokensDetails{ReasoningTokens: 2}},
		{PromptTokens: 3, CacheCreation: &providers.CacheCreationDetails{Ephemeral1hInputTokens: math.MaxInt, Ephemeral5mInputTokens: 1}},
	} {
		if _, err := CostUsage(p, u); err == nil {
			t.Fatalf("accepted invalid usage: %+v", u)
		}
	}
	p.InputPerMillionCents = math.MaxInt64
	if _, err := CostUsage(p, providers.Usage{PromptTokens: math.MaxInt}); err == nil {
		t.Fatal("accepted overflowing cost")
	}
	got, err := CostUsage(p, providers.Usage{PromptTokens: 1})
	if err != nil || got != math.MaxInt64 {
		t.Fatalf("exact large cost: %d %v", got, err)
	}
	// Two tiers that each fit but whose sum does not.
	p.Tiers = store.PricingTiers{CacheReadPerMillionCents: pricePointer(math.MaxInt64/2 + 1)}
	p.InputPerMillionCents = math.MaxInt64/2 + 1
	if _, err := CostUsage(p, providers.Usage{PromptTokens: 2, CacheReadInputTokens: 1}); err == nil {
		t.Fatal("accepted overflowing tier sum")
	}
	// Input and output that each fit but whose sum does not.
	p.Tiers = store.PricingTiers{}
	p.InputPerMillionCents, p.OutputPerMillionCents = math.MaxInt64, 1
	if _, err := CostUsage(p, providers.Usage{PromptTokens: 1, CompletionTokens: 1}); err == nil {
		t.Fatal("accepted overflowing input+output sum")
	}
}

func TestEstimateReservesMostExpensiveTier(t *testing.T) {
	p := &store.Pricing{InputPerMillionCents: 100, OutputPerMillionCents: 200, Tiers: store.PricingTiers{CacheWrite1hPerMillionCents: pricePointer(400), ReasoningPerMillionCents: pricePointer(600)}}
	got, err := estimatedTierCost(p, 1000000, 1000000)
	if err != nil || got != 1000000000 {
		t.Fatalf("reservation: %d %v", got, err)
	}
	if p.InputPerMillionCents != 100 || p.OutputPerMillionCents != 200 {
		t.Fatal("estimate mutated pricing")
	}
}
