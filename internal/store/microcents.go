package store

import (
	"errors"
	"math"
)

// Exact costs are integer micro-cents: 1 µ¢ = 1/1,000,000 US cent. With rates
// in whole cents per million tokens, Σ tokens × rate is exactly this unit.
const MicrocentsPerCent int64 = 1_000_000

// CeilCents reports an exact amount in whole cents, rounded up, so a positive
// amount is never 0. It avoids (µ¢+999999)/1e6, which overflows near MaxInt64.
func CeilCents(microcents int64) int64 {
	if microcents < 0 {
		panic("store: negative microcents")
	}
	cents := microcents / MicrocentsPerCent
	if microcents%MicrocentsPerCent != 0 {
		cents++
	}
	return cents
}

// LimitMicrocents converts a whole-cent limit for exact comparison. Limits above
// MaxInt64 µ¢ (about $92.2B) saturate: no int64 amount can exceed them anyway.
func LimitMicrocents(limitCents int64) int64 {
	if limitCents < 0 {
		panic("store: negative limit")
	}
	if limitCents > math.MaxInt64/MicrocentsPerCent {
		return math.MaxInt64
	}
	return limitCents * MicrocentsPerCent
}

// EffectiveMicrocents is an amount's exact value, or its whole-cent history
// value × 1e6 when no exact value was recorded. It saturates instead of
// wrapping and never panics: it reads stored values, which are not validated.
func EffectiveMicrocents(cents int64, exact *int64) int64 {
	switch {
	case exact != nil:
		return *exact
	case cents < -math.MaxInt64/MicrocentsPerCent:
		return math.MinInt64
	case cents > math.MaxInt64/MicrocentsPerCent:
		return math.MaxInt64
	}
	return cents * MicrocentsPerCent
}

// AddMicrocents adds two non-negative amounts, failing instead of wrapping.
func AddMicrocents(a, b int64) (int64, error) {
	if a < 0 || b < 0 || a > math.MaxInt64-b {
		return 0, errors.New("micro-cent total exceeds int64")
	}
	return a + b, nil
}
