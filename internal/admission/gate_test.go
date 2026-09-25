package admission

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGateAdmitsAndReleases(t *testing.T) {
	g := New(Config{MaxInFlight: 2, MaxQueued: 1, QueueTimeout: time.Second})
	p1, d1, err := g.Acquire(context.Background())
	if err != nil || d1.Outcome != OutcomeAdmitted {
		t.Fatalf("first acquire = (%v, %v), want admitted", d1, err)
	}
	p2, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if got := g.Stats().InFlight; got != 2 {
		t.Fatalf("in flight = %d, want 2", got)
	}
	p1.Release()
	p1.Release() // release is deliberately idempotent
	p2.Release()
	if got := g.Stats().InFlight; got != 0 {
		t.Fatalf("in flight after release = %d, want 0", got)
	}
}

func TestGateQueuesThenAdmits(t *testing.T) {
	g := New(Config{MaxInFlight: 1, MaxQueued: 1, QueueTimeout: time.Second})
	first, _, err := g.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		permit   *Permit
		decision Decision
		err      error
	}
	done := make(chan result, 1)
	go func() {
		p, d, acquireErr := g.Acquire(context.Background())
		done <- result{permit: p, decision: d, err: acquireErr}
	}()
	waitFor(t, time.Second, func() bool { return g.Stats().Queued == 1 })
	first.Release()

	got := <-done
	if got.err != nil || got.decision.Outcome != OutcomeAdmitted {
		t.Fatalf("queued acquire = (%v, %v), want admitted", got.decision, got.err)
	}
	if got.decision.Waited <= 0 {
		t.Fatal("queued acquire reported no wait")
	}
	got.permit.Release()
}

func TestGateRejectsWhenQueueIsFull(t *testing.T) {
	g := New(Config{MaxInFlight: 1, MaxQueued: 1, QueueTimeout: time.Second})
	first, _, _ := g.Acquire(context.Background())
	defer first.Release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queuedDone := make(chan error, 1)
	go func() {
		p, _, err := g.Acquire(ctx)
		if p != nil {
			p.Release()
		}
		queuedDone <- err
	}()
	waitFor(t, time.Second, func() bool { return g.Stats().Queued == 1 })

	_, decision, err := g.Acquire(context.Background())
	if !errors.Is(err, ErrQueueFull) || decision.Outcome != OutcomeQueueFull {
		t.Fatalf("third acquire = (%v, %v), want queue full", decision, err)
	}
	cancel()
	<-queuedDone
}

func TestGateQueueTimeout(t *testing.T) {
	g := New(Config{MaxInFlight: 1, MaxQueued: 1, QueueTimeout: 20 * time.Millisecond})
	first, _, _ := g.Acquire(context.Background())
	defer first.Release()

	_, decision, err := g.Acquire(context.Background())
	if !errors.Is(err, ErrQueueTimeout) || decision.Outcome != OutcomeQueueTimeout {
		t.Fatalf("queued acquire = (%v, %v), want queue timeout", decision, err)
	}
}

func TestGateCancellationRemovesWaiter(t *testing.T) {
	g := New(Config{MaxInFlight: 1, MaxQueued: 1, QueueTimeout: time.Second})
	first, _, _ := g.Acquire(context.Background())
	defer first.Release()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct {
		decision Decision
		err      error
	}, 1)
	go func() {
		_, decision, err := g.Acquire(ctx)
		done <- struct {
			decision Decision
			err      error
		}{decision: decision, err: err}
	}()
	waitFor(t, time.Second, func() bool { return g.Stats().Queued == 1 })
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || got.decision.Outcome != OutcomeCanceled {
		t.Fatalf("canceled acquire = (%v, %v), want canceled", got.decision, got.err)
	}
	if stats := g.Stats(); stats.Queued != 0 || stats.InFlight != 1 {
		t.Fatalf("stats after cancellation = %#v, want one active and no waiters", stats)
	}
}

func TestGateNeverExceedsCapacity(t *testing.T) {
	const capacity = 4
	g := New(Config{MaxInFlight: capacity, MaxQueued: 64, QueueTimeout: 2 * time.Second})
	var active atomic.Int64
	var peak atomic.Int64
	var wg sync.WaitGroup

	for range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, _, err := g.Acquire(context.Background())
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			time.Sleep(time.Millisecond)
			active.Add(-1)
			p.Release()
		}()
	}
	wg.Wait()
	if got := peak.Load(); got > capacity {
		t.Fatalf("peak active = %d, capacity = %d", got, capacity)
	}
}

func TestCounterDynamicLimitPreservesInflight(t *testing.T) {
	c := NewCounter(2)
	p1, ok := c.TryAcquire()
	if !ok {
		t.Fatal("first acquire denied")
	}
	p2, ok := c.TryAcquire()
	if !ok {
		t.Fatal("second acquire denied")
	}
	if _, ok := c.TryAcquire(); ok {
		t.Fatal("third acquire succeeded above limit")
	}

	// Lowering below the active count admits nothing until enough work drains.
	c.SetLimit(1)
	p1.Release()
	if _, ok := c.TryAcquire(); ok {
		t.Fatal("acquire succeeded while active count still equals lowered limit")
	}
	p2.Release()
	p3, ok := c.TryAcquire()
	if !ok {
		t.Fatal("acquire denied after active work drained")
	}
	p3.Release()
	if got := c.Stats().InFlight; got != 0 {
		t.Fatalf("in flight = %d, want 0", got)
	}
}

func TestCounterUnlimitedStillTracksInflight(t *testing.T) {
	c := NewCounter(0)
	permits := make([]*CounterPermit, 100)
	for i := range permits {
		var ok bool
		permits[i], ok = c.TryAcquire()
		if !ok {
			t.Fatalf("unlimited acquire %d denied", i)
		}
	}
	if got := c.Stats(); got.InFlight != 100 || got.Limit != 0 {
		t.Fatalf("stats = %#v, want 100 active and unlimited", got)
	}
	for _, permit := range permits {
		permit.Release()
		permit.Release()
	}
	if got := c.Stats().InFlight; got != 0 {
		t.Fatalf("in flight after release = %d, want 0", got)
	}
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
