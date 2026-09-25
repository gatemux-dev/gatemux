// Package callbacks implements the async event bus and built-in sinks
// described in design doc 0003. The bus runs one goroutine per registered
// callback, each with its own bounded queue, so a slow Slack webhook
// never backs up an S3 archival or vice-versa.
package callbacks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// EventType identifies the kind of event. New types should be additive —
// callers should ignore unknown types so older callbacks keep working.
type EventType string

const (
	EventRequestCompleted EventType = "request.completed"
	EventRequestFailed    EventType = "request.failed"
	EventBudgetThreshold  EventType = "budget.threshold"
	EventBudgetExceeded   EventType = "budget.exceeded"
	EventCircuitOpened    EventType = "circuit.opened"
	EventCircuitClosed    EventType = "circuit.closed"
	EventKeyCreated       EventType = "key.created"
	EventKeyRevoked       EventType = "key.revoked"
	EventKeyRotated       EventType = "key.rotated"
	EventGuardrailBlocked EventType = "guardrail.blocked"
)

// Event is the shape every callback sees. Fields not relevant to a given
// type are zero-valued.
type Event struct {
	ID         string         `json:"id"`
	Type       EventType      `json:"event"`
	RequestID  string         `json:"request_id,omitempty"`
	Ts         time.Time      `json:"ts"`
	TeamSlug   string         `json:"team_slug,omitempty"`
	UserID     int64          `json:"user_id,omitempty"`
	KeyPrefix  string         `json:"key_prefix,omitempty"`
	Alias      string         `json:"alias,omitempty"`
	Deployment string         `json:"deployment,omitempty"`
	ModelUsed  string         `json:"model_used,omitempty"`
	Tokens     TokenUsage     `json:"tokens,omitempty"`
	CostCents  int64          `json:"cost_cents,omitempty"`
	LatencyMs  int            `json:"latency_ms,omitempty"`
	StatusCode int            `json:"status_code,omitempty"`
	Cached     bool           `json:"cached,omitempty"`
	Error      string         `json:"error,omitempty"`
	Payload    map[string]any `json:"payload,omitempty"`
}

type TokenUsage struct {
	Prompt     int `json:"prompt,omitempty"`
	Completion int `json:"completion,omitempty"`
	Total      int `json:"total,omitempty"`
}

// Callback is implemented by sinks. Send is called with a per-callback
// context that respects shutdown and per-callback timeout. EventTypes
// returns the subset of events the sink wants; nil means all.
type Callback interface {
	Name() string
	EventTypes() []EventType
	Send(ctx context.Context, e Event) error
}

// Bus dispatches events to every registered callback. Each callback gets
// its own bounded buffer; on overflow the oldest is dropped and a counter
// bumped (visible via Stats).
type Bus struct {
	log     *slog.Logger
	workers []*worker
	mu      sync.RWMutex
	started sync.Once
	done    chan struct{}
	stopped bool // protected by mu; queue writes stop before final accounting
	sealed  bool // registration ends before the worker snapshot
}

type worker struct {
	cb         Callback
	subscribed map[EventType]bool
	subAll     bool
	queue      chan Event
	delivered  atomic.Int64
	failed     atomic.Int64
	dropped    atomic.Int64
	circuit    atomic.Int32 // consecutive failures; opened above 10
	openUntil  atomic.Int64 // unix nanos
	timeout    time.Duration
}

// NewBus creates an empty bus. Register callbacks then call Start.
func NewBus(log *slog.Logger) *Bus {
	return &Bus{log: log, done: make(chan struct{})}
}

// Register adds a callback with default queue depth and timeout. Must be
// called before Start. A bus has one worker generation and cannot be restarted.
func (b *Bus) Register(cb Callback) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sealed {
		return fmt.Errorf("callback registration is closed")
	}
	if len(b.workers) >= 32 {
		return fmt.Errorf("at most 32 callbacks may be registered")
	}
	w := &worker{
		cb:         cb,
		subscribed: map[EventType]bool{},
		queue:      make(chan Event, 10000),
		timeout:    10 * time.Second,
	}
	if types := cb.EventTypes(); len(types) == 0 {
		w.subAll = true
	} else {
		for _, t := range types {
			w.subscribed[t] = true
		}
	}
	b.workers = append(b.workers, w)
	return nil
}

// Start spawns one goroutine per registered callback. Stop the bus by
// cancelling the context; in-flight events are abandoned, queued events
// are dropped.
func (b *Bus) Start(ctx context.Context) <-chan struct{} {
	b.started.Do(func() {
		b.mu.Lock()
		b.sealed = true
		workers := append([]*worker(nil), b.workers...)
		b.mu.Unlock()
		var running sync.WaitGroup
		for _, w := range workers {
			running.Add(1)
			go func(w *worker) { defer running.Done(); w.run(ctx, b.log) }(w)
		}
		go func() {
			<-ctx.Done()
			b.mu.Lock()
			b.stopped = true
			b.mu.Unlock()
			running.Wait()
			for _, w := range workers {
				for len(w.queue) > 0 {
					<-w.queue
					w.dropped.Add(1)
				}
			}
			close(b.done)
		}()
	})
	return b.done
}

// Emit fans the event out to every subscriber whose filter matches. Drops
// silently when the bus is nil — callers don't need to branch on whether
// callbacks are configured.
func (b *Bus) Emit(e Event) {
	if b == nil {
		return
	}
	if e.ID == "" {
		e.ID = fmt.Sprintf("evt_%d", time.Now().UnixNano())
	}
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, w := range b.workers {
		if !w.subAll && !w.subscribed[e.Type] {
			continue
		}
		if b.stopped {
			w.dropped.Add(1)
			continue
		}
		select {
		case w.queue <- e:
		default:
			// Queue full — drop oldest, push new. Other concurrent Emit
			// calls and the worker can consume/refill it between attempts.
			select {
			case <-w.queue:
				w.dropped.Add(1)
			default:
			}
			select {
			case w.queue <- e:
			default:
				w.dropped.Add(1)
			}
		}
	}
}

func (w *worker) run(ctx context.Context, log *slog.Logger) {
	defer func() {
		if sink, ok := w.cb.(interface{ Flush(context.Context) error }); ok {
			flushCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := sink.Flush(flushCtx); err != nil && log != nil {
				log.Warn("callback flush failed", "callback", w.cb.Name(), "err", err)
			}
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case e := <-w.queue:
			if openUntil := w.openUntil.Load(); openUntil > 0 && time.Now().UnixNano() < openUntil {
				w.dropped.Add(1)
				continue
			}
			send, cancel := context.WithTimeout(ctx, w.timeout)
			err := w.cb.Send(send, e)
			cancel()
			if err != nil {
				w.failed.Add(1)
				if log != nil {
					log.Warn("callback send failed", "callback", w.cb.Name(), "event_id", e.ID, "err", err)
				}
				if w.circuit.Add(1) > 10 {
					w.openUntil.Store(time.Now().Add(60 * time.Second).UnixNano())
					w.circuit.Store(0)
				}
				continue
			}
			w.delivered.Add(1)
			w.circuit.Store(0)
		}
	}
}

// Stat is a snapshot of one callback's queue health.
type Stat struct {
	Name      string `json:"name"`
	Delivered int64  `json:"delivered"`
	Failed    int64  `json:"failed"`
	Dropped   int64  `json:"dropped"`
	QueueLen  int    `json:"queue_len"`
	Circuit   string `json:"circuit"`
}

// Stats returns one entry per registered callback. Used by the Settings
// integrations panel.
func (b *Bus) Stats() []Stat {
	if b == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Stat, 0, len(b.workers))
	for _, w := range b.workers {
		circuit := "closed"
		if openUntil := w.openUntil.Load(); openUntil > 0 && time.Now().UnixNano() < openUntil {
			circuit = "open"
		}
		out = append(out, Stat{
			Name:      w.cb.Name(),
			Delivered: w.delivered.Load(),
			Failed:    w.failed.Load(),
			Dropped:   w.dropped.Load(),
			QueueLen:  len(w.queue),
			Circuit:   circuit,
		})
	}
	return out
}
