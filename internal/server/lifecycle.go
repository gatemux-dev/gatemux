package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
)

func (s *Server) initLifecycle() {
	s.requestCtx, s.requestCancel = context.WithCancel(context.Background())
	s.requestsDone = make(chan struct{})
	close(s.requestsDone)
}

// Drain is irreversible for this process. Registration and drain use the same
// mutex so shutdown never races a WaitGroup Add against its final Wait.
func (s *Server) Drain() {
	s.requestsMu.Lock()
	s.draining.Store(true)
	s.tel.SetDraining()
	s.requestsMu.Unlock()
}

func (s *Server) handleDrain(w http.ResponseWriter, _ *http.Request) {
	s.Drain()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"draining": true})
}

func writeDraining(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"type": "server_draining", "code": "server_draining", "message": "gateway is draining; retry on another instance"}})
}

func (s *Server) inferenceLifecycle(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestsMu.Lock()
		if s.draining.Load() {
			s.requestsMu.Unlock()
			writeDraining(w)
			return
		}
		if s.activeRequests == 0 {
			s.requestsDone = make(chan struct{})
		}
		s.activeRequests++
		s.requestsMu.Unlock()
		ctx, cancel := context.WithCancel(r.Context())
		canceled := make(chan struct{})
		stop := context.AfterFunc(s.requestCtx, func() { cancel(); close(canceled) })
		defer func() {
			if !stop() {
				<-canceled
			}
			cancel()
			s.requestsMu.Lock()
			s.activeRequests--
			if s.activeRequests == 0 {
				close(s.requestsDone)
			}
			s.requestsMu.Unlock()
		}()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Recheck after admission queueing, before authentication/policy DB work.
func (s *Server) rejectDrained(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.draining.Load() {
			writeDraining(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) waitInference(ctx context.Context) error {
	s.requestsMu.Lock()
	done := s.requestsDone
	s.requestsMu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) shutdown(ctx context.Context) error {
	s.Drain()
	s.serveMu.Lock()
	serveDone, metricsDone := s.serveDone, s.metricsDone
	s.serveMu.Unlock()
	policy := config.ShutdownConfig{}.WithDefaults()
	if s.cfg != nil {
		policy = s.cfg.Server.Shutdown.WithDefaults()
	}
	grace, cancelGrace := context.WithTimeout(ctx, policy.GracePeriod)
	var failures []error
	if s.http != nil {
		if err := s.http.Shutdown(grace); err != nil {
			failures = append(failures, err)
		}
	}
	if err := s.waitInference(grace); err != nil {
		failures = append(failures, err)
	}
	cancelGrace()
	// Grace expiry cancels upstream HTTP/SSE/upload work and closes sockets. The
	// separate bounded cleanup phase permits independent durable finalization.
	if s.requestCancel != nil {
		s.requestCancel()
	}
	if s.http != nil {
		_ = s.http.Close()
	}
	cleanup, cancelCleanup := context.WithTimeout(context.Background(), policy.CleanupTimeout)
	defer cancelCleanup()
	if err := s.waitInference(cleanup); err != nil {
		failures = append(failures, err)
	}
	if s.workerCancel != nil {
		s.workerCancel()
	}
	for _, done := range s.workerDone {
		select {
		case <-done:
		case <-cleanup.Done():
			failures = append(failures, cleanup.Err())
		}
	}
	if s.usage != nil {
		if err := s.usage.CloseContext(cleanup); err != nil {
			failures = append(failures, err)
		}
	}
	s.jwtVerifier.Close()
	if s.metrics != nil {
		if err := s.metrics.Shutdown(cleanup); err != nil {
			failures = append(failures, err)
			_ = s.metrics.Close()
		}
	}
	for _, done := range []<-chan struct{}{serveDone, metricsDone} {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-cleanup.Done():
			failures = append(failures, cleanup.Err())
		}
	}
	if s.limit != nil {
		_ = s.limit.Close()
	}
	if s.concurrency != nil {
		_ = s.concurrency.Close()
	}
	_ = s.promptCache.Close()
	if s.breakerClient != nil {
		_ = s.breakerClient.Close()
	}
	if s.tel != nil {
		if err := s.tel.Shutdown(cleanup); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

const readinessTimeout = 2 * time.Second
