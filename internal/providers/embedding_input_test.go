package providers_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/providers/cohere"
	"github.com/gatemux-dev/gatemux/internal/providers/gemini"
	"github.com/gatemux-dev/gatemux/internal/ratelimit"
)

func TestEmbeddingInputsRoundTrip(t *testing.T) {
	for _, input := range []string{`"hello"`, `["hello","world"]`, `[0,1,200000]`, `[[0,1],[200000]]`} {
		t.Run(input, func(t *testing.T) {
			var req providers.EmbeddingRequest
			if err := json.Unmarshal([]byte(`{"model":"alias","input":`+input+`,"dimensions":42,"encoding_format":"base64","extension":9007199254740993}`), &req); err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(req.Input)
			if err != nil || string(encoded) != input {
				t.Fatalf("round trip = %s, %v", encoded, err)
			}
			req.Model = "upstream"
			encoded, err = json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &body); err != nil {
				t.Fatal(err)
			}
			if string(body["input"]) != input || string(body["model"]) != `"upstream"` || string(body["extension"]) != "9007199254740993" || string(body["dimensions"]) != "42" {
				t.Fatalf("wire fidelity: %s", encoded)
			}
			if req.Input.IsTokenized() && ratelimit.EstimateEmbeddingTokens(&req) != 3 {
				t.Fatalf("token IDs must count exactly once")
			}
		})
	}
}

func TestEmbeddingInputsRejectMalformed(t *testing.T) {
	for _, input := range []string{`null`, `[]`, `""`, `[""]`, `[[]]`, `[1,"2"]`, `[[1],2]`, `[null]`, `[[null]]`, `[-1]`, `[1.5]`, `[1e3]`, `[999999999999999999999999]`, `{}`, `true`} {
		var value providers.EmbeddingInput
		if err := json.Unmarshal([]byte(input), &value); err == nil {
			t.Errorf("accepted %s", input)
		}
	}
}

func TestNativeEmbeddingAdaptersRejectTokensBeforeIO(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	for _, client := range []providers.Provider{gemini.New("test", upstream.URL), cohere.New("test", upstream.URL)} {
		_, err := client.Embeddings(context.Background(), &providers.EmbeddingRequest{Model: "embed", Input: providers.EmbeddingInput{Tokens: [][]int{{1, 2}}}})
		var upstreamErr *providers.UpstreamError
		if !errors.As(err, &upstreamErr) || upstreamErr.StatusCode != 400 {
			t.Fatalf("%s error = %v", client.Type(), err)
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("token IDs reached translating adapter upstream")
	}
}
