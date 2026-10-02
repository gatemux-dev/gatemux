package telemetry

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestInferenceCostCountsFractionalCents(t *testing.T) {
	previous := prometheus.DefaultRegisterer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	defer func() { prometheus.DefaultRegisterer = previous }()
	c, err := New("cost-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(context.Background())
	// Two requests of 3000 µ¢ each: 0.006 cents, not 0 and not 2 whole cents.
	c.RecordInference("alias", "openai", 100, 50, 150, 3000)
	c.RecordInference("alias", "openai", 100, 50, 150, 3000)
	c.RecordInference("alias", "openai", 0, 0, 0, 0)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range families {
		if family.GetName() == "gatemux_cost_cents_total" {
			if len(family.Metric) != 1 {
				t.Fatalf("cost series: %d", len(family.Metric))
			}
			if got := family.Metric[0].GetCounter().GetValue(); got < 0.0059999 || got > 0.0060001 {
				t.Fatalf("cost cents = %v, want 0.006", got)
			}
			return
		}
	}
	t.Fatal("gatemux_cost_cents_total not exported")
}
