package store

import "testing"

func TestValidateKeyBudget(t *testing.T) {
	for _, value := range []int64{-1, MaxKeyBudgetCents + 1} {
		if ValidateKeyBudget(&value) == nil {
			t.Fatalf("accepted invalid limit %d", value)
		}
	}
	for _, value := range []int64{0, 1, MaxKeyBudgetCents} {
		if err := ValidateKeyBudget(&value); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateKeyBudget(nil); err != nil {
		t.Fatal(err)
	}
}
