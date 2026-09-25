package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResetDoesNotReuseProviderResponseIDs(t *testing.T) {
	s := &mockServer{streamChunks: 1}
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		w := httptest.NewRecorder()
		s.chat(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"test"}`)))
		var response struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.ID == "" || seen[response.ID] {
			t.Fatalf("reused response ID %q", response.ID)
		}
		seen[response.ID] = true
		if s.requests.Load() != 1 {
			t.Fatal("per-run counter did not reset")
		}
		w = httptest.NewRecorder()
		s.reset(w, httptest.NewRequest("POST", "/reset", nil))
		if w.Code != 204 || s.requests.Load() != 0 || s.inFlight.Load() != 0 {
			t.Fatal("reset failed")
		}
	}
}
