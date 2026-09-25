package store

import "fmt"

// Nil means inherit the base input/output rate; explicit zero is a free tier.
// Rates are operator-managed integer cents per million tokens, not model prices
// fetched from a third-party catalog. Versioned with each pricing row.
type PricingTiers struct {
	CacheReadPerMillionCents    *int64 `json:"cache_read_per_million_cents,omitempty"`
	CacheWritePerMillionCents   *int64 `json:"cache_write_per_million_cents,omitempty"`
	CacheWrite1hPerMillionCents *int64 `json:"cache_write_1h_per_million_cents,omitempty"`
	ReasoningPerMillionCents    *int64 `json:"reasoning_per_million_cents,omitempty"`
}

func (p PricingTiers) Validate() error {
	for _, rate := range []*int64{p.CacheReadPerMillionCents, p.CacheWritePerMillionCents, p.CacheWrite1hPerMillionCents, p.ReasoningPerMillionCents} {
		if rate != nil && *rate < 0 {
			return fmt.Errorf("pricing tier values must be non-negative")
		}
	}
	return nil
}
