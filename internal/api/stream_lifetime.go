package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
)

var (
	errStreamFirstEventTimeout = fmt.Errorf("upstream first stream event timeout: %w", context.DeadlineExceeded)
	errStreamIdleTimeout       = fmt.Errorf("upstream stream idle timeout: %w", context.DeadlineExceeded)
)

// chatStream owns one upstream attempt, including its connection/header wait.
// One timer and one cancellation callback per active attempt; no read goroutines
// or event queues. Closing cancels translated providers as well as HTTP bodies.
type chatStream struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	config config.StreamingConfig
	reader *sseReader
	first  *sseEvent
	// Native/opaque protocols may define progress and terminal events differently.
	progress func(*sseEvent) bool
	allowEOF bool

	mu        sync.Mutex
	timer     *time.Timer
	deadline  time.Time
	cause     error
	closed    bool
	paused    bool
	remaining time.Duration

	body      io.ReadCloser
	closeOnce sync.Once
	closeErr  error
	stopClose func() bool
}

func newChatStream(ctx context.Context, cfg config.StreamingConfig) *chatStream {
	cfg = cfg.WithDefaults()
	ctx, cancel := context.WithCancelCause(ctx)
	s := &chatStream{ctx: ctx, cancel: cancel, config: cfg,
		deadline: time.Now().Add(cfg.FirstEventTimeout), cause: errStreamFirstEventTimeout}
	s.timer = time.AfterFunc(cfg.FirstEventTimeout, s.expire)
	return s
}

func (s *chatStream) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.paused || s.ctx.Err() != nil {
		return
	}
	// A callback already scheduled before an event reset must not expire the
	// new idle window. Serialize the absolute deadline, not just Timer.Reset.
	if remaining := time.Until(s.deadline); remaining > 0 {
		s.timer.Reset(remaining)
		return
	}
	s.cancel(s.cause)
}

// Pause upstream idle accounting while backpressure prevents us from reading.
// The independent downstream-write and total-request deadlines still apply.
func (s *chatStream) pauseIdle() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused {
		return context.Cause(s.ctx)
	}
	s.paused = true
	s.remaining = time.Until(s.deadline)
	if s.remaining <= 0 {
		s.cancel(s.cause)
	}
	s.timer.Stop()
	return context.Cause(s.ctx)
}

func (s *chatStream) resumeIdle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paused = false
	if !s.closed && s.ctx.Err() == nil {
		s.deadline = time.Now().Add(s.remaining)
		s.timer.Reset(s.remaining)
	}
}

func (s *chatStream) attach(body io.ReadCloser) {
	s.body = body
	s.reader = newSSEReader(body)
	s.stopClose = context.AfterFunc(s.ctx, func() { _ = s.closeBody() })
}

func (s *chatStream) closeBody() error {
	s.closeOnce.Do(func() {
		if s.body != nil {
			s.closeErr = s.body.Close()
		}
	})
	return s.closeErr
}

func (s *chatStream) Close() error {
	s.mu.Lock()
	s.closed = true
	s.timer.Stop()
	s.mu.Unlock()
	s.cancel(context.Canceled)
	if s.stopClose != nil {
		s.stopClose()
	}
	return s.closeBody()
}

func (s *chatStream) readEvent() (*sseEvent, error) {
	if err := context.Cause(s.ctx); err != nil {
		return nil, err
	}
	event, err := s.reader.next()
	s.mu.Lock()
	defer s.mu.Unlock()
	if cause := context.Cause(s.ctx); cause != nil {
		return nil, cause
	}
	// Enforce the deadline even if timer scheduling was delayed under load.
	if !time.Now().Before(s.deadline) {
		s.cancel(s.cause)
		return nil, s.cause
	}
	if err != nil {
		if errors.Is(err, io.EOF) && !s.allowEOF {
			return nil, io.ErrUnexpectedEOF // a missing [DONE] is not success
		}
		return nil, err
	}
	if s.meaningful(event) {
		s.deadline = time.Now().Add(s.config.IdleTimeout)
		s.cause = errStreamIdleTimeout
		s.timer.Reset(s.config.IdleTimeout)
	}
	return event, nil
}

func (s *chatStream) prefetch() error {
	for {
		event, err := s.readEvent()
		if err != nil {
			return err
		}
		// Do not commit HTTP success (or grow a prelude buffer) for pings alone.
		if s.meaningful(event) {
			s.first = event
			return nil
		}
	}
}

func (s *chatStream) meaningful(event *sseEvent) bool {
	if s.progress != nil {
		return s.progress(event)
	}
	return event.hasData && event.data != ""
}

// readBytes shares connection/first-byte/idle limits with SSE while retaining
// fixed-size buffers for binary/opaque bodies. Unlike SSE, EOF is a valid end.
func (s *chatStream) readBytes(buf []byte) (int, error) {
	if err := context.Cause(s.ctx); err != nil {
		return 0, err
	}
	n, err := s.body.Read(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	if cause := context.Cause(s.ctx); cause != nil {
		return 0, cause
	}
	if !time.Now().Before(s.deadline) {
		s.cancel(s.cause)
		return 0, s.cause
	}
	if n > 0 {
		s.deadline = time.Now().Add(s.config.IdleTimeout)
		s.cause = errStreamIdleTimeout
		s.timer.Reset(s.config.IdleTimeout)
	}
	return n, err
}

func (s *chatStream) next() (*sseEvent, error) {
	if s.first != nil {
		event := s.first
		s.first = nil
		if err := context.Cause(s.ctx); err != nil {
			return nil, err
		}
		return event, nil
	}
	return s.readEvent()
}
