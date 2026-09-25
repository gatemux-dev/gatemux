package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/admission"
)

func TestAdmissionMiddlewareRejectsExcessLoad(t *testing.T) {
	gate := admission.New(admission.Config{MaxInFlight: 1})
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := admissionMiddleware(gate, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
		firstDone <- recorder
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter handler")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	if got, want := second.Code, http.StatusServiceUnavailable; got != want {
		t.Fatalf("status = %d, want %d", got, want)
	}
	if got := second.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if body.Error.Type != "server_overloaded" || body.Error.Code != "server_overloaded" {
		t.Fatalf("error = %#v", body.Error)
	}

	close(release)
	select {
	case first := <-firstDone:
		if first.Code != http.StatusNoContent {
			t.Fatalf("first status = %d, want 204", first.Code)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
	if got := gate.Stats().InFlight; got != 0 {
		t.Fatalf("in flight = %d after completion, want 0", got)
	}
}
