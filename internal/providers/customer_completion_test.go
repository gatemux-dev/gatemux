package providers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCustomerCompletionBoundAndModernFieldFidelity(t *testing.T) {
	for _, tc := range []struct {
		body   string
		tokens int
		field  string
	}{
		{`{"model":"x","messages":[]}`, 1024, `"max_completion_tokens":1024`},
		{`{"max_tokens":9,"n":2}`, 18, `"max_tokens":9`},
		{`{"max_completion_tokens":7,"n":3}`, 21, `"max_completion_tokens":7`},
	} {
		var r ChatRequest
		if err := json.Unmarshal([]byte(tc.body), &r); err != nil {
			t.Fatal(err)
		}
		n, err := r.BoundCustomerCompletion()
		if err != nil || n != tc.tokens {
			t.Fatalf("bound %d %v", n, err)
		}
		body, _ := json.Marshal(r)
		if !strings.Contains(string(body), tc.field) {
			t.Fatalf("lost wire field: %s", body)
		}
		if r.MaxCompletionTokens != nil && strings.Contains(string(body), `"max_tokens"`) {
			t.Fatalf("modern request acquired incompatible legacy field: %s", body)
		}
	}
	for _, body := range []string{`{"max_tokens":0}`, `{"max_tokens":1000001}`, `{"max_tokens":1,"max_completion_tokens":2}`, `{"n":0}`, `{"n":129}`, `{"n":1.5}`} {
		var r ChatRequest
		_ = json.Unmarshal([]byte(body), &r)
		if _, err := r.BoundCustomerCompletion(); err == nil {
			t.Fatalf("invalid bound accepted: %s", body)
		}
	}
}
