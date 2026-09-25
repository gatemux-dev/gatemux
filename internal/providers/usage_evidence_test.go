package providers

import (
	"encoding/json"
	"testing"
)

func TestReportedUsageEvidence(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`null`, false}, {`{}`, false},
		{`{"prompt_tokens":1}`, false},
		{`{"prompt_tokens":null,"completion_tokens":0}`, false},
		{`{"prompt_tokens":-1,"completion_tokens":0}`, false},
		{`{"prompt_tokens":1.5,"completion_tokens":0}`, false},
		{`{"prompt_tokens":0,"completion_tokens":0}`, true},
		{`{"prompt_tokens":2,"completion_tokens":3}`, true},
	} {
		if got := ReportedUsage(json.RawMessage(tc.raw), true); got != tc.want {
			t.Errorf("%s reported=%v", tc.raw, got)
		}
	}
	for _, raw := range []string{`{}`, `{"input_tokens":3}`, `{"input_tokens":null,"output_tokens":0}`, `{"input_tokens":-1,"output_tokens":0}`} {
		var u ResponseUsage
		if err := json.Unmarshal([]byte(raw), &u); err != nil || u.UsageReported() {
			t.Errorf("incomplete Responses usage accepted: %s %v", raw, err)
		}
	}
	var u ResponseUsage
	if err := json.Unmarshal([]byte(`{"input_tokens":0,"output_tokens":0}`), &u); err != nil || !u.UsageReported() {
		t.Fatalf("explicit zero Responses usage lost: %v", err)
	}
	var chat ChatResponse
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":0,"completion_tokens":0}}`), &chat); err != nil || !chat.UsageReported() {
		t.Fatalf("explicit zero chat usage lost: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"choices":[]}`), &chat); err != nil || chat.UsageReported() {
		t.Fatalf("missing chat usage treated as zero: %v", err)
	}
	var embed EmbeddingResponse
	if err := json.Unmarshal([]byte(`{"usage":{"prompt_tokens":0}}`), &embed); err != nil || !embed.UsageReported() {
		t.Fatalf("explicit zero embedding usage lost: %v", err)
	}
}
