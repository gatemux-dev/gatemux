package anthropic

import (
	"io"
	"strings"
	"testing"
)

func TestStreamRetainsCacheUsage(t *testing.T) {
	start := `data: {"type":"message_start","message":{"id":"msg1","model":"test","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":20}}}}` + "\n\n"
	delta := `data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}` + "\n\n"
	for _, tc := range []struct {
		name, ending string
		valid        bool
	}{
		{"complete", `data: {"type":"message_stop"}` + "\n\n", true},
		{"truncated", "", false},
		{"provider error", `data: {"type":"error","error":{"type":"overloaded_error"}}` + "\n\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, w := io.Pipe()
			go TranslateStream(io.NopCloser(strings.NewReader(start+delta+tc.ending)), w)
			defer r.Close()
			body, err := io.ReadAll(r)
			if tc.valid {
				for _, part := range []string{`"prompt_tokens":60`, `"completion_tokens":5`, `"cache_read_input_tokens":20`, `"ephemeral_1h_input_tokens":20`, "[DONE]"} {
					if !strings.Contains(string(body), part) {
						t.Fatalf("missing %s: %s", part, body)
					}
				}
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || strings.Contains(string(body), "[DONE]") {
				t.Fatalf("false success: %s %v", body, err)
			}
		})
	}
}
