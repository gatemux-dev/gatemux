package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestRoutingConcurrencyAdminValidationAuthorizationAndStableIdentity(t *testing.T) {
	e := newTestEnv(t)
	subject := "routing-limit-" + randHex(4)
	put := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/admin/concurrency/routing", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		out := httptest.NewRecorder()
		e.Router.ServeHTTP(out, req)
		return out
	}
	for _, body := range []string{
		`{"scope":"key","subject":"x","max_parallel_requests":1}`,
		`{"scope":"provider","subject":"invalid","max_parallel_requests":1}`,
		`{"scope":"model","subject":"x"}`,
		`{"scope":"model","subject":"x","max_parallel_requests":-1}`,
		`{"scope":"model","subject":"x","max_parallel_requests":1.5}`,
	} {
		if got := put(e.MasterKey, body); got.Code != 400 {
			t.Fatalf("invalid policy: %d %s", got.Code, got.Body)
		}
	}
	_, managerToken := e.createUserWithRole("routing-limit-manager", store.RoleManager, nil)
	valid := `{"scope":"model","subject":"` + subject + `","max_parallel_requests":2}`
	if got := put(managerToken, valid); got.Code != 403 {
		t.Fatalf("manager mutation: %d", got.Code)
	}
	if code, _ := e.GET("/admin/concurrency/routing", managerToken); code != 403 {
		t.Fatalf("manager read: %d", code)
	}
	var identity int64
	for _, value := range []string{"2", "null", "3", "0"} {
		got := put(e.MasterKey, `{"scope":"model","subject":"`+subject+`","max_parallel_requests":`+value+`}`)
		if got.Code != 200 {
			t.Fatalf("save: %d %s", got.Code, got.Body)
		}
		var row store.RoutingConcurrencyLimit
		if err := json.Unmarshal(got.Body.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if identity == 0 {
			identity = row.ID
		} else if identity != row.ID {
			t.Fatal("clearing or reconfiguring changed the policy ID")
		}
		if (value == "null" || value == "0") && row.MaxParallelRequests != nil {
			t.Fatal("clear left a configured cap")
		}
	}
	code, body := e.GET("/admin/concurrency/routing", e.MasterKey)
	if code != 200 || !bytes.Contains(body, []byte(subject)) {
		t.Fatalf("list: %d %s", code, body)
	}
}

func routingLimiter(t *testing.T) *concurrency.Limiter {
	t.Helper()
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GATEMUX_TEST_REDIS_ADDR for Redis integration")
	}
	l := concurrency.NewRedis(addr, "", 0, "routing-test-"+randHex(6))
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func setRoutingLimit(t *testing.T, f *chatFixture, kind, subject string) {
	t.Helper()
	limit := 1
	if _, err := f.Store.SetRoutingConcurrencyLimit(context.Background(), kind, subject, &limit); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.Store.SetRoutingConcurrencyLimit(context.Background(), kind, subject, nil); err != nil {
			t.Error(err)
		}
	})
	if err := f.Registry.RefreshConcurrencyPolicies(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestModelConcurrencyBlocksChatAndEmbeddingsAndReleases(t *testing.T) {
	limiter := routingLimiter(t)
	f := newChatFixture(t)
	f.Handler.Concurrency = limiter
	setRoutingLimit(t, f, "model", f.Alias)
	team := f.createTeam("model-limit")
	key := f.issueKey(team, nil)
	held, err := f.Handler.acquireRoutingScope(context.Background(), "model", f.Alias)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Handler.releaseRoutingLease(held)
	for _, call := range []func(string) (int, []byte){f.chatPOST, f.embeddingsPOST} {
		code, body := call(key)
		if code != 429 || readErrorType(body) != "concurrency_limit_exceeded" {
			t.Fatalf("model denial: %d %s", code, body)
		}
	}
	f.Handler.releaseRoutingLease(held)
	if code, body := f.chatPOST(key); code != 200 {
		t.Fatalf("after release: %d %s", code, body)
	}
	next, err := f.Handler.acquireRoutingScope(context.Background(), "model", f.Alias)
	if err != nil {
		t.Fatalf("handler leaked model lease: %v", err)
	}
	f.Handler.releaseRoutingLease(next)
}

func TestProviderConcurrencyFallbackAndFailedAttemptRelease(t *testing.T) {
	limiter := routingLimiter(t)
	f := newChatFixture(t)
	f.Handler.Concurrency = limiter
	setRoutingLimit(t, f, "provider", "openai")
	fallbackName, fallbackURL := "routing-fallback-"+randHex(4), f.BaseURL
	if _, err := f.Store.UpsertDeployment(context.Background(), fallbackName, "openai_compatible", "fallback", "", &fallbackURL, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Store.UpsertAlias(context.Background(), f.Alias, []string{f.DepName, fallbackName}); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	held, err := f.Handler.acquireRoutingScope(context.Background(), "provider", "openai")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Handler.releaseRoutingLease(held)
	target, _, err := f.Handler.runChatWithFallback(context.Background(), f.Alias, &providers.ChatRequest{Messages: []providers.ChatMessage{{Role: "user", Content: "hi"}}}, router.ResolveContext{})
	if err != nil || target.DeploymentName != fallbackName {
		t.Fatalf("provider fallback: target %+v, err %v", target, err)
	}
	if n := f.Registry.DeploymentConcurrency(f.DepName).InFlight; n != 0 {
		t.Fatalf("denied attempt leaked local permit: %d", n)
	}
	f.Handler.releaseRoutingLease(held)
	// Fail before stream headers; the provider permit must be returned even
	// when the error is permanent and no fallback is permitted.
	_, _, _, err = f.Handler.openStreamWithFallback(context.Background(), f.Alias, router.ResolveContext{}, func(context.Context, *router.Resolved) (io.ReadCloser, error) {
		return nil, errors.New("permanent failure")
	})
	if err == nil {
		t.Fatal("expected stream-open failure")
	}
	next, err := f.Handler.acquireRoutingScope(context.Background(), "provider", "openai")
	if err != nil {
		t.Fatalf("failed attempt leaked lease: %v", err)
	}
	f.Handler.releaseRoutingLease(next)
}

func TestRoutingConcurrencyFailsClosedAndReleasesLocalPermit(t *testing.T) {
	f := newChatFixture(t)
	setRoutingLimit(t, f, "provider", "openai")
	team := f.createTeam("provider-no-redis")
	key := f.issueKey(team, nil)
	code, body := f.chatPOST(key)
	if code != 503 || readErrorType(body) != "concurrency_limit_unavailable" {
		t.Fatalf("missing redis: %d %s", code, body)
	}
	if n := f.Registry.DeploymentConcurrency(f.DepName).InFlight; n != 0 {
		t.Fatalf("failed admission leaked local permit: %d", n)
	}
}

func TestRoutingStreamHoldsBothCapsAndDisconnectReleases(t *testing.T) {
	limiter := routingLimiter(t)
	f := newChatFixture(t)
	f.Handler.Concurrency = limiter
	setRoutingLimit(t, f, "model", f.Alias)
	setRoutingLimit(t, f, "provider", "openai")
	started, upstreamDone := make(chan struct{}), make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(upstreamDone)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer mock.Close()
	baseURL := mock.URL + "/v1"
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", f.UpModel, f.CredEnv, &baseURL, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	team := f.createTeam("stream-routing-limit")
	key := f.issueKey(team, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": f.Alias, "messages": []map[string]string{{"role": "user", "content": "hi"}}, "stream": true})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+key)
	done := make(chan struct{})
	go func() { defer close(done); f.Router.ServeHTTP(httptest.NewRecorder(), req) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not start")
	}
	for _, scope := range []struct{ kind, subject string }{{"model", f.Alias}, {"provider", "openai"}} {
		lease, err := f.Handler.acquireRoutingScope(context.Background(), scope.kind, scope.subject)
		if err == nil {
			f.Handler.releaseRoutingLease(lease)
			t.Fatalf("%s cap released at headers", scope.kind)
		}
		if !canFallbackConcurrency(err) {
			t.Fatalf("wanted saturation, got %v", err)
		}
	}
	cancel()
	select {
	case <-upstreamDone:
	case <-time.After(3 * time.Second):
		t.Fatal("disconnect did not cancel upstream")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("handler did not stop")
	}
	for _, scope := range []struct{ kind, subject string }{{"model", f.Alias}, {"provider", "openai"}} {
		lease, err := f.Handler.acquireRoutingScope(context.Background(), scope.kind, scope.subject)
		if err != nil {
			t.Fatalf("%s cap leaked after cancellation: %v", scope.kind, err)
		}
		f.Handler.releaseRoutingLease(lease)
	}
}

func TestModelConcurrencyProtectsCapabilityEndpoints(t *testing.T) {
	f := newChatFixture(t)
	setRoutingLimit(t, f, "model", f.Alias)
	// Missing Redis must reject before any routed upstream work on each JSON
	// capability path. No second parsing/buffering middleware is required.
	for _, handler := range []http.HandlerFunc{f.Handler.Moderations, f.Handler.Rerank, f.Handler.ImagesGenerations, f.Handler.Messages, f.Handler.AudioSpeech} {
		req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(`{"model":"`+f.Alias+`"}`))
		req = req.WithContext(auth.WithTeam(req.Context(), &store.Team{ID: 1, AllowedModels: []string{"*"}}))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 503 || readErrorType(rec.Body.Bytes()) != "concurrency_limit_unavailable" {
			t.Fatalf("capability admission: %d %s", rec.Code, rec.Body)
		}
	}
}
