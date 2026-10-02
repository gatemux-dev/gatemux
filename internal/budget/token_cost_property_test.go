package budget

import (
	"math"
	"math/big"
	"math/rand"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// An independent reference: every tier's tokens × its (inherited) rate, summed
// without bounds. CostUsage must equal it exactly, or fail exactly when it
// does not fit in int64 micro-cents.
func referenceMicrocents(p *store.Pricing, prompt, read, write5m, write1h, completion, reasoning int) *big.Int {
	or := func(rate *int64, fallback int64) int64 {
		if rate != nil {
			return *rate
		}
		return fallback
	}
	write := or(p.Tiers.CacheWritePerMillionCents, p.InputPerMillionCents)
	sum := new(big.Int)
	for _, tier := range []struct {
		tokens int
		rate   int64
	}{
		{prompt - read - write5m - write1h, p.InputPerMillionCents},
		{read, or(p.Tiers.CacheReadPerMillionCents, p.InputPerMillionCents)},
		{write5m, write},
		{write1h, or(p.Tiers.CacheWrite1hPerMillionCents, write)},
		{completion - reasoning, p.OutputPerMillionCents},
		{reasoning, or(p.Tiers.ReasoningPerMillionCents, p.OutputPerMillionCents)},
	} {
		sum.Add(sum, new(big.Int).Mul(big.NewInt(int64(tier.tokens)), big.NewInt(tier.rate)))
	}
	return sum
}

func TestCostUsageMatchesReferenceProperty(t *testing.T) {
	rng := rand.New(rand.NewSource(20260930))
	rate := func() int64 { return rng.Int63n(1_000_000_001) }
	optionalRate := func() *int64 {
		if rng.Intn(3) == 0 {
			return nil // inherit
		}
		r := rate()
		return &r
	}
	// Spread token counts over many magnitudes so some sums overflow int64.
	tokens := func() int { return int(rng.Int63n(int64(math.Pow10(rng.Intn(12))) + 1)) }
	part := func(n int) int { return int(rng.Int63n(int64(n) + 1)) }
	maxInt64 := big.NewInt(math.MaxInt64)
	var exact, overflow int
	for i := range 20_000 {
		p := &store.Pricing{InputPerMillionCents: rate(), OutputPerMillionCents: rate(), Tiers: store.PricingTiers{
			CacheReadPerMillionCents: optionalRate(), CacheWritePerMillionCents: optionalRate(),
			CacheWrite1hPerMillionCents: optionalRate(), ReasoningPerMillionCents: optionalRate(),
		}}
		prompt, completion := tokens(), tokens()
		read := part(prompt)
		write := part(prompt - read)
		write1h := part(write)
		reasoning := part(completion)
		u := providers.Usage{PromptTokens: prompt, CompletionTokens: completion, TotalTokens: prompt + completion,
			CacheReadInputTokens: read, CacheCreationInputTokens: write}
		if rng.Intn(2) == 0 {
			u.PromptTokensDetails = &providers.PromptTokensDetails{CachedTokens: read}
		}
		if write1h > 0 || rng.Intn(2) == 0 {
			u.CacheCreation = &providers.CacheCreationDetails{Ephemeral5mInputTokens: write - write1h, Ephemeral1hInputTokens: write1h}
		} else {
			write1h = 0
		}
		if rng.Intn(2) == 0 {
			u.CompletionTokensDetails = &providers.CompletionTokensDetails{ReasoningTokens: reasoning}
		} else {
			reasoning = 0
		}
		want := referenceMicrocents(p, prompt, read, write-write1h, write1h, completion, reasoning)
		got, err := CostUsage(p, u)
		if want.Cmp(maxInt64) > 0 {
			overflow++
			if err == nil {
				t.Fatalf("case %d: accepted %d for reference %s beyond int64", i, got, want)
			}
			continue
		}
		exact++
		if err != nil || got != want.Int64() {
			t.Fatalf("case %d: got %d err=%v, want %s (pricing %+v usage %+v)", i, got, err, want, p, u)
		}
	}
	if exact < 10_000 || overflow == 0 {
		t.Fatalf("generator lost coverage: exact=%d overflow=%d", exact, overflow)
	}
}
