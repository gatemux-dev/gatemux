package callbacks

import (
	"encoding/json"
	"testing"
)

func TestCallbackCostMicrocents(t *testing.T) {
	b, err := json.Marshal(Event{ID: "e", Type: EventRequestCompleted, CostCents: 1, CostMicrocents: 3000})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["cost_cents"] != 1.0 || got["cost_microcents"] != "3000" {
		t.Fatalf("event cost fields: %s", b)
	}
	// Events without a cost (guardrail, lifecycle) keep omitting both fields.
	b, _ = json.Marshal(Event{ID: "e", Type: EventGuardrailBlocked})
	got = nil
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["cost_microcents"]; ok {
		t.Fatalf("zero cost serialized: %s", b)
	}
}
