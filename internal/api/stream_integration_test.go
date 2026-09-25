package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/router"
)

func setStreamUpstream(t *testing.T, f *chatFixture, upstreamURL, provider string) {
	t.Helper()
	baseURL, limit := upstreamURL+"/v1", 1
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, provider, f.UpModel, f.CredEnv, &baseURL, nil, nil, &limit); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func streamRequest(f *chatFixture, key string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+f.Alias+`","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	req.Header.Set("Authorization", "Bearer "+key)
	return req
}

func assertStreamPermitsReleased(t *testing.T, f *chatFixture, provider string) {
	t.Helper()
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatalf("local permit leaked: %+v", state)
	}
	for _, scope := range []struct{ kind, subject string }{{"model", f.Alias}, {"provider", provider}} {
		lease, err := f.Handler.acquireRoutingScope(context.Background(), scope.kind, scope.subject)
		if err != nil {
			t.Fatalf("%s permit leaked: %v", scope.kind, err)
		}
		f.Handler.releaseRoutingLease(lease)
	}
}

func TestStreamTimeoutHTTPAndDistributedPermitRelease(t *testing.T) {
	for _, mode := range []string{"headers", "first_event", "idle", "truncated", "done"} {
		t.Run(mode, func(t *testing.T) {
			limiter := routingLimiter(t)
			f := newChatFixture(t)
			f.Handler.Concurrency = limiter
			f.Handler.Streaming = config.StreamingConfig{FirstEventTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond}
			setRoutingLimit(t, f, "model", f.Alias)
			setRoutingLimit(t, f, "provider", "openai")
			upstreamDone := make(chan struct{})
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				_, _ = io.Copy(io.Discard, r.Body)
				if mode != "headers" {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode != "first_event" {
						_, _ = io.WriteString(w, "data: {\"model\":\"private\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
					}
					if mode == "done" {
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
					}
					w.(http.Flusher).Flush()
				}
				if mode == "truncated" {
					return
				}
				<-r.Context().Done()
			}))
			defer mock.Close()
			setStreamUpstream(t, f, mock.URL, "openai")
			team := f.setTeamBudget(f.createTeam("stream-timeout"), 100000)
			f.upsertPricing(100, 200)
			key := f.issueKey(team, nil)
			w := httptest.NewRecorder()
			req := streamRequest(f, key)
			requestID := "stream-outcome-" + randHex(6)
			req.Header.Set("X-Request-Id", requestID)
			f.Router.ServeHTTP(w, req)
			if mode == "headers" || mode == "first_event" {
				if w.Code != 504 || readErrorType(w.Body.Bytes()) != "upstream_timeout" || strings.Contains(w.Header().Get("Content-Type"), "event-stream") {
					t.Fatalf("first event failure: status %d body %s", w.Code, w.Body)
				}
			} else {
				if w.Code != 200 {
					t.Fatalf("committed stream status = %d", w.Code)
				}
				if mode == "done" {
					if !strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), `"error"`) {
						t.Fatalf("completed stream: %s", w.Body)
					}
				} else if strings.Contains(w.Body.String(), "[DONE]") || !strings.Contains(w.Body.String(), `"error"`) {
					t.Fatalf("interruption disguised as success: %s", w.Body)
				}
			}
			select {
			case <-upstreamDone:
			case <-time.After(time.Second):
				t.Fatal("upstream was not closed")
			}
			assertStreamPermitsReleased(t, f, "openai")
			var reservationState string
			if err := f.Store.Pool.QueryRow(context.Background(), "SELECT status FROM budget_reservations WHERE request_id=$1", requestID).Scan(&reservationState); err != nil || reservationState != "settled" {
				t.Fatalf("stream reservation not released: %q %v", reservationState, err)
			}
			wantStatus := 504
			if mode == "done" {
				wantStatus = 200
			} else if mode == "truncated" {
				wantStatus = 502
			}
			deadline := time.Now().Add(time.Second)
			for {
				var actualStatus int
				err := f.Store.Pool.QueryRow(context.Background(), "SELECT status_code FROM usage_log WHERE request_id=$1", requestID).Scan(&actualStatus)
				if err == nil {
					if actualStatus != wantStatus {
						t.Fatalf("usage status %d, want %d", actualStatus, wantStatus)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("usage was not recorded: %v", err)
				}
				<-time.After(5 * time.Millisecond)
			}
		})
	}
}

func TestStreamFirstEventFallbackButNoRetryAfterCommit(t *testing.T) {
	for _, began := range []bool{false, true} {
		f := newChatFixture(t)
		f.Handler.Streaming = config.StreamingConfig{FirstEventTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond}
		primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			if began {
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"primary\"}}]}\n\n")
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		t.Cleanup(primary.Close)
		setStreamUpstream(t, f, primary.URL, "openai")
		var calls atomic.Int64
		fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"fallback\"}}]}\n\ndata: [DONE]\n\n")
		}))
		t.Cleanup(fallback.Close)
		name, baseURL := "stream-fallback-"+randHex(4), fallback.URL+"/v1"
		if _, err := f.Store.UpsertDeployment(context.Background(), name, "openai", "fallback", f.CredEnv, &baseURL, nil, nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Store.UpsertAlias(context.Background(), f.Alias, []string{f.DepName, name}); err != nil {
			t.Fatal(err)
		}
		if err := f.Registry.Refresh(context.Background()); err != nil {
			t.Fatal(err)
		}
		key := f.issueKey(f.createTeam("stream-fallback"), nil)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, streamRequest(f, key))
		if w.Code != 200 {
			t.Fatalf("status %d body %s", w.Code, w.Body)
		}
		if began {
			if calls.Load() != 0 || !strings.Contains(w.Body.String(), "primary") || strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatalf("retried a committed stream: %s, fallback calls %d", w.Body, calls.Load())
			}
		} else if calls.Load() != 1 || !strings.Contains(w.Body.String(), "fallback") || !strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatalf("first event fallback failed: %s, calls %d", w.Body, calls.Load())
		}
		assertStreamPermitsReleased(t, f, "openai")
	}
}

type panicStreamBody struct{ closed atomic.Bool }

func (*panicStreamBody) Read([]byte) (int, error) { panic("test read panic") }
func (p *panicStreamBody) Close() error           { p.closed.Store(true); return nil }

func TestStreamOpenAndPrefetchPanicReleasePermits(t *testing.T) {
	for _, duringRead := range []bool{false, true} {
		limiter := routingLimiter(t)
		f := newChatFixture(t)
		f.Handler.Concurrency = limiter
		setRoutingLimit(t, f, "provider", "openai")
		setStreamUpstream(t, f, f.Mock.URL, "openai")
		body := &panicStreamBody{}
		var attemptCtx context.Context
		func() {
			defer func() {
				if recover() == nil {
					t.Error("expected panic")
				}
			}()
			_, _, _, _ = f.Handler.openStreamWithFallback(context.Background(), f.Alias, router.ResolveContext{}, func(ctx context.Context, _ *router.Resolved) (io.ReadCloser, error) {
				attemptCtx = ctx
				if duringRead {
					return body, nil
				}
				panic("test open panic")
			})
		}()
		if attemptCtx.Err() == nil || (duringRead && !body.closed.Load()) {
			t.Fatal("panic left upstream alive")
		}
		assertStreamPermitsReleased(t, f, "openai")
	}
}

func TestTranslatedAnthropicStreamDeadlineCancelsHTTP(t *testing.T) {
	for _, started := range []bool{false, true} {
		limiter := routingLimiter(t)
		f := newChatFixture(t)
		f.Handler.Concurrency = limiter
		f.Handler.Streaming = config.StreamingConfig{FirstEventTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond}
		setRoutingLimit(t, f, "model", f.Alias)
		setRoutingLimit(t, f, "provider", "anthropic")
		done := make(chan struct{})
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer close(done)
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"model\":\"claude\",\"usage\":{\"input_tokens\":1}}}\n\n")
			if started {
				_, _ = io.WriteString(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n")
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		t.Cleanup(mock.Close)
		setStreamUpstream(t, f, mock.URL, "anthropic")
		key := f.issueKey(f.createTeam("anthropic-timeout"), nil)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, streamRequest(f, key))
		if (!started && w.Code != 504) || (started && (w.Code != 200 || !strings.Contains(w.Body.String(), "upstream_timeout"))) {
			t.Fatalf("translated timeout: %d %s", w.Code, w.Body)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("translated pipe cancellation left HTTP upstream running")
		}
		assertStreamPermitsReleased(t, f, "anthropic")
	}
}

func TestStreamCanceledParentNeverOpensAttempt(t *testing.T) {
	h := &V1Handler{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := h.openStreamWithFallback(ctx, "unused", router.ResolveContext{}, func(context.Context, *router.Resolved) (io.ReadCloser, error) {
		t.Fatal("attempt opened for canceled request")
		return nil, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
