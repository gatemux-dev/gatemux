package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// streamWriter has a single writer (the handler). The cancellation callback
// only sets the socket deadline; it is joined before the handler returns.
type streamWriter struct {
	ctx        context.Context
	w          http.ResponseWriter
	controller *http.ResponseController
	timeout    time.Duration
	mu         sync.Mutex
	stop       func() bool
	done       chan struct{}
}

type streamWriteError struct{ error }

func (e *streamWriteError) Unwrap() error { return e.error }

func newStreamWriter(ctx context.Context, w http.ResponseWriter, timeout time.Duration) *streamWriter {
	s := &streamWriter{ctx: ctx, w: w, controller: http.NewResponseController(w), timeout: timeout, done: make(chan struct{})}
	s.stop = context.AfterFunc(ctx, func() {
		defer close(s.done)
		s.mu.Lock()
		defer s.mu.Unlock()
		_ = s.controller.SetWriteDeadline(time.Now())
	})
	return s
}

func (s *streamWriter) close() {
	if !s.stop() {
		<-s.done
	}
	_ = s.controller.SetWriteDeadline(time.Time{})
}

func (s *streamWriter) arm() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(s.timeout)
	if total, ok := s.ctx.Deadline(); ok && total.Before(deadline) {
		deadline = total
	}
	err := s.controller.SetWriteDeadline(deadline)
	// In-memory recorders have no socket/deadline. Production net/http writers
	// and the chi middleware's Unwrap chain support ResponseController.
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}

func (s *streamWriter) write(event string) (writeErr error) {
	defer func() {
		if writeErr != nil {
			writeErr = &streamWriteError{writeErr}
		}
	}()
	if err := s.arm(); err != nil {
		return err
	}
	if _, err := io.WriteString(s.w, event); err != nil {
		return err
	}
	err := s.controller.Flush()
	if errors.Is(err, http.ErrNotSupported) {
		return nil
	}
	return err
}
