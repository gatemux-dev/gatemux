// Package admission provides bounded process-level concurrency admission.
//
// The active and waiting populations are capped independently. This matters for
// an LLM gateway because upstream calls can remain open for minutes: a plain
// semaphore limits work sent upstream but still permits an unbounded number of
// goroutines to accumulate behind it during a traffic spike.
package admission

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrQueueFull    = errors.New("admission queue full")
	ErrQueueTimeout = errors.New("admission queue timeout")
)

type Config struct {
	MaxInFlight  int
	MaxQueued    int
	QueueTimeout time.Duration
}

type Outcome string

const (
	OutcomeAdmitted     Outcome = "admitted"
	OutcomeQueueFull    Outcome = "queue_full"
	OutcomeQueueTimeout Outcome = "queue_timeout"
	OutcomeCanceled     Outcome = "canceled"
)

type Decision struct {
	Outcome Outcome
	Waited  time.Duration
}

type Stats struct {
	InFlight int64
	Queued   int64
	Capacity int
	QueueCap int
}

// Gate bounds both executing requests and requests waiting for a slot. A nil
// Gate means admission control is disabled.
type Gate struct {
	slots        chan struct{}
	queue        chan struct{}
	queueTimeout time.Duration
	inFlight     atomic.Int64
	queued       atomic.Int64
}

func New(cfg Config) *Gate {
	if cfg.MaxInFlight <= 0 {
		return nil
	}
	if cfg.MaxQueued < 0 {
		cfg.MaxQueued = 0
	}
	return &Gate{
		slots:        make(chan struct{}, cfg.MaxInFlight),
		queue:        make(chan struct{}, cfg.MaxQueued),
		queueTimeout: cfg.QueueTimeout,
	}
}

// Acquire returns a permit which must be released exactly once. Admission is
// immediate while capacity exists. Once full, at most MaxQueued callers wait;
// everyone else is rejected without allocating a long-lived waiter.
func (g *Gate) Acquire(ctx context.Context) (*Permit, Decision, error) {
	started := time.Now()
	if g == nil {
		return &Permit{}, Decision{Outcome: OutcomeAdmitted}, nil
	}

	select {
	case g.slots <- struct{}{}:
		g.inFlight.Add(1)
		return &Permit{gate: g}, Decision{Outcome: OutcomeAdmitted, Waited: time.Since(started)}, nil
	default:
	}

	// A non-positive timeout makes a saturated gate fail fast, even when a
	// queue capacity was configured.
	if cap(g.queue) == 0 || g.queueTimeout <= 0 {
		return nil, Decision{Outcome: OutcomeQueueFull, Waited: time.Since(started)}, ErrQueueFull
	}
	select {
	case g.queue <- struct{}{}:
		g.queued.Add(1)
	default:
		return nil, Decision{Outcome: OutcomeQueueFull, Waited: time.Since(started)}, ErrQueueFull
	}
	defer func() {
		<-g.queue
		g.queued.Add(-1)
	}()

	timer := time.NewTimer(g.queueTimeout)
	defer timer.Stop()
	select {
	case g.slots <- struct{}{}:
		g.inFlight.Add(1)
		return &Permit{gate: g}, Decision{Outcome: OutcomeAdmitted, Waited: time.Since(started)}, nil
	case <-timer.C:
		return nil, Decision{Outcome: OutcomeQueueTimeout, Waited: time.Since(started)}, ErrQueueTimeout
	case <-ctx.Done():
		return nil, Decision{Outcome: OutcomeCanceled, Waited: time.Since(started)}, ctx.Err()
	}
}

func (g *Gate) Stats() Stats {
	if g == nil {
		return Stats{}
	}
	return Stats{
		InFlight: g.inFlight.Load(),
		Queued:   g.queued.Load(),
		Capacity: cap(g.slots),
		QueueCap: cap(g.queue),
	}
}

type Permit struct {
	gate *Gate
	once sync.Once
}

// Counter is a dynamically resizable, non-queueing concurrency limiter. It is
// used for deployment headroom: the router should immediately try another
// deployment when one is full, rather than building a queue per upstream.
// A non-positive limit means unlimited, but in-flight work is still tracked so
// enabling or lowering a limit at runtime does not reset the active count.
type Counter struct {
	limit    atomic.Int64
	inFlight atomic.Int64
}

type CounterStats struct {
	InFlight int64
	Limit    int64
}

func NewCounter(limit int64) *Counter {
	c := &Counter{}
	c.SetLimit(limit)
	return c
}

func (c *Counter) SetLimit(limit int64) {
	if c == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	c.limit.Store(limit)
}

func (c *Counter) TryAcquire() (*CounterPermit, bool) {
	if c == nil {
		return &CounterPermit{}, true
	}
	for {
		current := c.inFlight.Load()
		limit := c.limit.Load()
		if limit > 0 && current >= limit {
			return nil, false
		}
		if c.inFlight.CompareAndSwap(current, current+1) {
			return &CounterPermit{counter: c}, true
		}
	}
}

func (c *Counter) Stats() CounterStats {
	if c == nil {
		return CounterStats{}
	}
	return CounterStats{InFlight: c.inFlight.Load(), Limit: c.limit.Load()}
}

type CounterPermit struct {
	counter *Counter
	once    sync.Once
}

func (p *CounterPermit) Release() {
	if p == nil || p.counter == nil {
		return
	}
	p.once.Do(func() { p.counter.inFlight.Add(-1) })
}

func (p *Permit) Release() {
	if p == nil || p.gate == nil {
		return
	}
	p.once.Do(func() {
		<-p.gate.slots
		p.gate.inFlight.Add(-1)
	})
}
