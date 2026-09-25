package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/admission"
	"github.com/gatemux-dev/gatemux/internal/config"
)

func lifecycleServer(grace time.Duration) *Server {
	s := &Server{cfg: &config.Config{Server: config.ServerConfig{Shutdown: config.ShutdownConfig{GracePeriod: grace, CleanupTimeout: 2 * time.Second}}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s.initLifecycle()
	return s
}

func awaitLifecycle(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("lifecycle operation did not finish")
	}
}

func serveLifecycle(t *testing.T, s *Server, h http.Handler) string {
	t.Helper()
	s.http = &http.Server{Handler: h}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = s.http.Serve(listener) }()
	t.Cleanup(func() { _ = s.Shutdown(context.Background()); awaitLifecycle(t, done) })
	return "http://" + listener.Addr().String()
}

func TestDrainLetsExistingSSECompleteAndRejectsNewWork(t *testing.T) {
	s := lifecycleServer(time.Second)
	finish := make(chan struct{})
	var canceled atomic.Bool
	base := serveLifecycle(t, s, s.inferenceLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: started\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-finish:
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		case <-r.Context().Done():
			canceled.Store(true)
		}
	})))
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: started\n" {
		t.Fatalf("no first event: %q %v", line, err)
	}
	s.Drain()
	rejected, err := client.Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer rejected.Body.Close()
	if rejected.StatusCode != 503 || rejected.Header.Get("Retry-After") != "1" {
		t.Fatalf("drain response: %d", rejected.StatusCode)
	}
	ready := httptest.NewRecorder()
	s.handleReady(ready, httptest.NewRequest("GET", "/readyz", nil))
	if ready.Code != 503 || !strings.Contains(ready.Body.String(), "draining") {
		t.Fatal("drain did not fail readiness without dependency access")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("shutdown did not wait for active stream: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(finish)
	rest, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(rest), "[DONE]") {
		t.Fatalf("graceful stream incomplete: %s %v", rest, err)
	}
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if canceled.Load() {
		t.Fatal("graceful stream was canceled")
	}
}

func TestShutdownCancelsStalledSSEAfterGraceAndJoinsFinalizer(t *testing.T) {
	s := lifecycleServer(30 * time.Millisecond)
	finalized := make(chan struct{})
	base := serveLifecycle(t, s, s.inferenceLifecycle(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: started\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		// Independent durable finalization is permitted after request cancel.
		time.Sleep(20 * time.Millisecond)
		close(finalized)
	})))
	resp, err := (&http.Client{Timeout: time.Second}).Get(base)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	started := time.Now()
	err = s.Shutdown(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("missing forced-drain error: %v", err)
	}
	if elapsed := time.Since(started); elapsed < 30*time.Millisecond || elapsed > time.Second {
		t.Fatalf("unbounded shutdown: %s", elapsed)
	}
	select {
	case <-finalized:
	default:
		t.Fatal("shutdown returned before request cleanup")
	}
	if s.activeRequests != 0 {
		t.Fatalf("active requests leaked: %d", s.activeRequests)
	}
	// Every caller observes the same completed shutdown result.
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if !errors.Is(s.Shutdown(context.Background()), context.DeadlineExceeded) {
				t.Error("shutdown result changed")
			}
		}()
	}
	wg.Wait()
}

func TestDrainRechecksPreviouslyQueuedWork(t *testing.T) {
	s := lifecycleServer(time.Second)
	gate := admission.New(admission.Config{MaxInFlight: 1, MaxQueued: 1, QueueTimeout: time.Second})
	permit, _, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()
	var calls atomic.Int32
	h := s.inferenceLifecycle(admissionMiddleware(gate, nil)(s.rejectDrained(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))))
	w := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", nil)) }()
	deadline := time.Now().Add(time.Second)
	for gate.Stats().Queued != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if gate.Stats().Queued != 1 {
		t.Fatal("request not queued")
	}
	s.Drain()
	permit.Release()
	awaitLifecycle(t, done)
	if w.Code != 503 || calls.Load() != 0 {
		t.Fatalf("queued request reached policy/upstream work: %d/%d", w.Code, calls.Load())
	}
	if stats := gate.Stats(); stats.Queued != 0 || stats.InFlight != 0 {
		t.Fatalf("permits leaked: %+v", stats)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownRacesRequestRegistration(t *testing.T) {
	s := lifecycleServer(time.Second)
	h := s.inferenceLifecycle(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil))
			}
		}()
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if s.activeRequests != 0 {
		t.Fatalf("request population leaked: %d", s.activeRequests)
	}
}

func TestStartFailureAndConcurrentShutdownOwnListeners(t *testing.T) {
	for _, metricsConflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "inference-bind", true: "metrics-bind"}[metricsConflict], func(t *testing.T) {
			occupied, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			s := lifecycleServer(time.Second)
			s.http = &http.Server{Addr: occupied.Addr().String()}
			if metricsConflict {
				s.http.Addr = "127.0.0.1:0"
				s.metrics = &http.Server{Addr: occupied.Addr().String()}
			}
			if err := s.Start(); err == nil {
				t.Fatal("listener bind failure was hidden")
			}
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			awaitLifecycle(t, s.serveDone)
		})
	}
	for range 20 {
		s := lifecycleServer(time.Second)
		s.http = &http.Server{Addr: "127.0.0.1:0"}
		s.metrics = &http.Server{Addr: "127.0.0.1:0"}
		done := make(chan struct{})
		go func() { defer close(done); _ = s.Start() }()
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		awaitLifecycle(t, done)
		if err := s.Start(); err == nil {
			t.Fatal("server restarted after shutdown")
		}
	}
}
