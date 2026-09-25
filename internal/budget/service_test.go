package budget

import (
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestEstimateCostCents(t *testing.T) {
	pricing := &store.Pricing{
		InputPerMillionCents:  150,
		OutputPerMillionCents: 600,
	}
	got := estimateCostCents(pricing, 1_000, 500)
	if got != 2 {
		t.Fatalf("estimateCostCents() = %d, want 2", got)
	}

	got = estimateCostCents(pricing, 10_000, 10_000)
	if got <= 0 {
		t.Fatalf("estimateCostCents() = %d, want positive cost", got)
	}
}

func TestBudgetWindow(t *testing.T) {
	now := mustTime(t, "2026-04-26T15:04:05Z")
	dayStart, dayEnd := budgetWindow(now, "day")
	if dayStart.Format("2006-01-02T15:04:05Z07:00") != "2026-04-26T00:00:00Z" {
		t.Fatalf("day start = %s", dayStart)
	}
	if dayEnd.Sub(dayStart).Hours() != 24 {
		t.Fatalf("day window = %s", dayEnd.Sub(dayStart))
	}
	monthStart, monthEnd := budgetWindow(now, "month")
	if monthStart.Format("2006-01-02T15:04:05Z07:00") != "2026-04-01T00:00:00Z" {
		t.Fatalf("month start = %s", monthStart)
	}
	if monthEnd.Format("2006-01-02T15:04:05Z07:00") != "2026-05-01T00:00:00Z" {
		t.Fatalf("month end = %s", monthEnd)
	}
}

func mustTime(t *testing.T, value string) (out time.Time) {
	t.Helper()
	out, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("time.Parse: %v", err)
	}
	return out
}
