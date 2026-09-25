package loadtest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunProducesConsistentCounts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(2 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	result, err := Run(context.Background(), Config{
		URL: server.URL, Body: []byte(`{}`), Rate: 100,
		Duration: 100 * time.Millisecond, RequestTimeout: time.Second,
		MaxInFlight: 8, ExpectedStatus: http.StatusOK,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Counts.Scheduled != 10 {
		t.Fatalf("scheduled = %d, want 10", result.Counts.Scheduled)
	}
	if result.Counts.Started+result.Counts.ClientDropped != result.Counts.Scheduled {
		t.Fatalf("started + dropped != scheduled: %+v", result.Counts)
	}
	if result.Counts.Completed != result.Counts.Started || result.Counts.Succeeded != result.Counts.Completed {
		t.Fatalf("completion counts inconsistent: %+v", result.Counts)
	}
	if result.Latency.Count != result.Counts.Completed || result.Latency.P95MS <= 0 {
		t.Fatalf("latency summary inconsistent: %+v", result.Latency)
	}
}

func TestRunDropsAtClientBoundInsteadOfQueueingUnbounded(t *testing.T) {
	var active atomic.Int64
	var peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	result, err := Run(context.Background(), Config{
		URL: server.URL, Body: []byte(`{}`), Rate: 1000,
		Duration: 50 * time.Millisecond, RequestTimeout: time.Second,
		MaxInFlight: 2, ExpectedStatus: http.StatusOK,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Counts.ClientDropped == 0 {
		t.Fatalf("expected client-side drops at bound: %+v", result.Counts)
	}
	if got := peak.Load(); got > 2 {
		t.Fatalf("server peak = %d, want <= 2", got)
	}
}

func TestRunClassifiesHTTPAndTransportErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"server_overloaded","message":"do not export this text"}}`))
	}))

	result, err := Run(context.Background(), Config{
		URL: server.URL, Body: []byte(`{}`), Rate: 20,
		Duration: 50 * time.Millisecond, RequestTimeout: time.Second,
		MaxInFlight: 1, ExpectedStatus: http.StatusOK,
	})
	server.Close()
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Counts.HTTPError != 1 || result.Counts.StatusCodes["503"] != 1 {
		t.Fatalf("HTTP error classification = %+v", result.Counts)
	}
	if result.Counts.HTTPErrorCodes["server_overloaded"] != 1 {
		t.Fatalf("missing bounded denial diagnostic: %+v", result.Counts.HTTPErrorCodes)
	}

	result, err = Run(context.Background(), Config{
		URL: server.URL, Body: []byte(`{}`), Rate: 20,
		Duration: 50 * time.Millisecond, RequestTimeout: 100 * time.Millisecond,
		MaxInFlight: 1, ExpectedStatus: http.StatusOK,
	})
	if err != nil {
		t.Fatalf("Run closed target: %v", err)
	}
	if result.Counts.TransportError != 1 {
		t.Fatalf("transport error classification = %+v", result.Counts)
	}
	if result.Counts.TransportErrors["connection_refused"] != 1 {
		t.Fatalf("transport error categories = %+v", result.Counts.TransportErrors)
	}
}

func TestHTTPErrorCodesNeverExportArbitraryProviderText(t *testing.T) {
	if got := classifyHTTPError([]byte(`{"error":{"type":"guardrail_unavailable"}}`)); got != "guardrail_unavailable" {
		t.Fatalf("type-only gateway error missed: %s", got)
	}
	for _, input := range []string{`{"error":{"code":"secret-provider-value","message":"private"}}`, `{"error":{"code":123}}`, "private body", ""} {
		if got := classifyHTTPError([]byte(input)); got != "other" {
			t.Fatalf("unbounded diagnostic category: %q", got)
		}
	}
}
