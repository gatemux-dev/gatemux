package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestAccountingMetricsBoundLabels(t *testing.T) {
	previous := prometheus.DefaultRegisterer
	registry := prometheus.NewRegistry()
	prometheus.DefaultRegisterer = registry
	defer func() { prometheus.DefaultRegisterer = previous }()
	c, err := New("accounting-test")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Shutdown(context.Background())
	c.RecordAccounting("completion", "committed", 0, time.Millisecond)
	c.RecordAccounting("completion", "failed", 0, time.Second)
	c.RecordAccounting("recovery", "committed", 64, time.Millisecond)
	c.RecordAccounting("request-id-must-not-be-a-label", "committed", 100, 0)
	c.RecordAccounting("completion", "provider-secret-must-not-be-a-label", 100, 0)
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	var samples int
	var recovered float64
	for _, family := range families {
		switch family.GetName() {
		case "gatemux_accounting_operations_total":
			samples = len(family.Metric)
		case "gatemux_accounting_recovered_total":
			recovered = family.Metric[0].GetCounter().GetValue()
		}
	}
	if samples != 3 || recovered != 64 {
		t.Fatalf("metric cardinality/count: %d/%v", samples, recovered)
	}
}
