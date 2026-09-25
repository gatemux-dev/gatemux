package loadtest

import (
	"strings"
	"testing"
)

func TestValidationRejectsFalseHTTPSuccess(t *testing.T) {
	for _, tc := range []struct {
		mode, body string
		valid      bool
	}{
		{"chat", `{"id":"c","object":"chat.completion","choices":[{}]}`, true},
		{"chat", `{"error":{"message":"bad"}}`, false},
		{"chat", `{"id":"c","object":"chat.completion","choices":[{}]}garbage`, false},
		{"sse", "data: {\"choices\":[]}\r\n\r\ndata: [DONE]\r\n\r\n", true},
		{"sse", "data: {\"choices\":[]}\n\n", false},
		{"sse", "data: {\"error\":{}}\n\ndata: [DONE]\n\n", false},
		{"sse", "data: [DONE]\n\n", false},
		{"sse", "data: {}\n\ndata: [DONE]\n", false},
	} {
		if err := validateResponse(strings.NewReader(tc.body), tc.mode); (err == nil) != tc.valid {
			t.Fatalf("%s %q: %v", tc.mode, tc.body, err)
		}
	}
}
