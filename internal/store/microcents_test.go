package store

import (
	"math"
	"testing"
)

func TestCeilCents(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{0, 0}, {1, 1}, {999999, 1}, {1000000, 1}, {1000001, 2},
		{math.MaxInt64, 9223372036855},
	} {
		if got := CeilCents(tc.in); got != tc.want {
			t.Fatalf("CeilCents(%d)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestLimitMicrocents(t *testing.T) {
	for _, tc := range []struct{ in, want int64 }{
		{0, 0}, {125, 125000000},
		{math.MaxInt64/1000000 + 1, math.MaxInt64}, {math.MaxInt64, math.MaxInt64},
	} {
		if got := LimitMicrocents(tc.in); got != tc.want {
			t.Fatalf("LimitMicrocents(%d)=%d want %d", tc.in, got, tc.want)
		}
	}
}

func TestMicrocentsRejectNegative(t *testing.T) {
	for name, fn := range map[string]func(int64) int64{"CeilCents": CeilCents, "LimitMicrocents": LimitMicrocents} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s accepted a negative amount", name)
				}
			}()
			fn(-1)
		}()
	}
}

func TestEffectiveMicrocents(t *testing.T) {
	exact := int64(3000)
	for _, tc := range []struct {
		cents int64
		exact *int64
		want  int64
	}{
		{1, &exact, 3000}, {2, nil, 2000000}, {0, nil, 0},
		{math.MaxInt64, nil, math.MaxInt64}, {-1, nil, -1000000}, {math.MinInt64, nil, math.MinInt64},
	} {
		if got := EffectiveMicrocents(tc.cents, tc.exact); got != tc.want {
			t.Fatalf("EffectiveMicrocents(%d,%v)=%d want %d", tc.cents, tc.exact, got, tc.want)
		}
	}
}
