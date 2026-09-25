package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	distconcurrency "github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/store"
	"os"
)

func TestTenantConcurrencyWithoutConfiguredLimitsBypassesRedis(t *testing.T) {
	handler := tenantConcurrencyMiddleware(nil, time.Minute, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.WithVirtualKey(auth.WithTeam(req.Context(), &store.Team{ID: 1}), &store.VirtualKey{ID: 2, TeamID: 1}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
}

func TestTenantConcurrencyHoldsLeaseForWholeHandler(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GATEMUX_TEST_REDIS_ADDR to run Redis integration coverage")
	}
	limiter := distconcurrency.NewRedis(addr, "", 0, "gatemux-server-test")
	defer func() { _ = limiter.Close() }()
	limit := 1
	team := &store.Team{ID: time.Now().UnixNano(), MaxParallelRequests: &limit}
	entered := make(chan struct{})
	release := make(chan struct{})
	handler := tenantConcurrencyMiddleware(limiter, time.Minute, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	request := func() *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		return req.WithContext(auth.WithTeam(req.Context(), team))
	}
	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request())
		firstDone <- recorder
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first request did not enter handler")
	}

	second := httptest.NewRecorder()
	handler.ServeHTTP(second, request())
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("second status = %d, want 429 (body=%s)", second.Code, second.Body.Bytes())
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("second response is missing Retry-After")
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

	thirdHandler := tenantConcurrencyMiddleware(limiter, time.Minute, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	third := httptest.NewRecorder()
	thirdHandler.ServeHTTP(third, request())
	if third.Code != http.StatusNoContent {
		t.Fatalf("third status after release = %d, want 204 (body=%s)", third.Code, third.Body.Bytes())
	}
}

func TestTenantConcurrencyFailsClosedWhenRedisIsMissing(t *testing.T) {
	limit := 1
	handler := tenantConcurrencyMiddleware(nil, time.Minute, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("limited request reached handler without Redis")
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req = req.WithContext(auth.WithTeam(req.Context(), &store.Team{ID: 1, MaxParallelRequests: &limit}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", recorder.Code)
	}
	if recorder.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", recorder.Header().Get("Retry-After"))
	}
	var body struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.Bytes())
	}
	if body.Error.Type != "concurrency_limit_unavailable" || body.Error.Code != "concurrency_limit_unavailable" {
		t.Fatalf("error = %+v", body.Error)
	}
}

func TestTenantConcurrencyDoesNotChargeModelListing(t *testing.T) {
	limit := 1
	handler := tenantConcurrencyMiddleware(nil, time.Minute, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req = req.WithContext(auth.WithTeam(req.Context(), &store.Team{ID: 1, MaxParallelRequests: &limit}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", recorder.Code)
	}
}

func TestConcurrencyLeaseTTLAndRetryHeader(t *testing.T) {
	if got := concurrencyLeaseTTL(90 * time.Second); got != 2*time.Minute {
		t.Fatalf("default lease ttl = %s, want 2m", got)
	}
	if got := concurrencyLeaseTTL(5 * time.Minute); got != 5*time.Minute+30*time.Second {
		t.Fatalf("long lease ttl = %s, want 5m30s", got)
	}
	if got := retryAfterSeconds(1100 * time.Millisecond); got != "2" {
		t.Fatalf("retry header = %q, want 2", got)
	}
}

func TestTenantConcurrencyScopesIncludeConfiguredOwner(t *testing.T) {
	teamLimit, keyLimit, ownerLimit := 8, 4, 2
	userID := int64(17)
	partition, scopes := tenantConcurrencyScopes(
		&store.Team{ID: 5, MaxParallelRequests: &teamLimit},
		&store.VirtualKey{ID: 9, TeamID: 5, UserID: &userID, MaxParallelRequests: &keyLimit, OwnerMaxParallelRequests: &ownerLimit},
	)
	if partition != 5 {
		t.Fatalf("partition = %d, want 5", partition)
	}
	if len(scopes) != 3 || scopes[0].Kind != "team" || scopes[1].Kind != "key" || scopes[2].Kind != "user" {
		t.Fatalf("scopes = %+v, want team/key/user", scopes)
	}

	serviceAccountID := int64(21)
	partition, scopes = tenantConcurrencyScopes(nil, &store.VirtualKey{
		ID: 10, TeamID: 6, ServiceAccountID: &serviceAccountID, OwnerMaxParallelRequests: &ownerLimit,
	})
	if partition != 6 || len(scopes) != 1 || scopes[0].Kind != "service_account" || scopes[0].ID != serviceAccountID {
		t.Fatalf("service account scopes = partition %d, %+v", partition, scopes)
	}
}
