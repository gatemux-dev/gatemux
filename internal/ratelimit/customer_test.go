package ratelimit

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestCustomerRateAtomicAcrossReplicasAndKeys(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires disposable Redis")
	}
	if time.Now().Second() > 55 {
		t.Skip("contention check must not span a fixed-window boundary")
	}
	prefix := fmt.Sprintf("customer-test-%d", time.Now().UnixNano())
	a, b := NewRedis(addr, "", 0, prefix), NewRedis(addr, "", 0, prefix)
	defer a.Close()
	defer b.Close()
	team := &store.Team{ID: 1, RPM: intPtr(50)}
	customer := &store.Customer{ID: 12, TeamID: 1, RPM: intPtr(10), TPM: intPtr(100)}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l := a
			if i%2 == 1 {
				l = b
			}
			result, err := l.Check(context.Background(), Check{Team: team, Key: &store.VirtualKey{ID: int64(i + 1)}, Customer: customer, Tokens: 10})
			if err != nil {
				t.Error(err)
				return
			}
			if result.Allowed {
				allowed.Add(1)
			} else if result.Scope != "customer" {
				t.Errorf("wrong denial: %+v", result)
			}
		}(i)
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("customer shared across keys/replicas admitted %d", allowed.Load())
	}
	// Denied customer calls must not consume any other scope's RPM allowance.
	for i := 0; i < 40; i++ {
		r, e := a.Check(context.Background(), Check{Team: team, Tokens: 1})
		if e != nil || !r.Allowed {
			t.Fatalf("partial increments: %+v %v", r, e)
		}
	}
	if r, e := a.Check(context.Background(), Check{Team: team, Tokens: 1}); e != nil || r.Allowed {
		t.Fatalf("team count mismatch: %+v %v", r, e)
	}
	if _, e := a.Check(context.Background(), Check{Team: &store.Team{ID: 2}, Customer: customer}); e == nil {
		t.Fatal("cross-team customer accepted")
	}
}

func TestCustomerTPMAndLocalCounterCapacity(t *testing.T) {
	l := New()
	c := &store.Customer{ID: 2, TeamID: 1, TPM: intPtr(5)}
	check := Check{Team: &store.Team{ID: 1}, Customer: c, Tokens: 6}
	if r, e := l.Check(context.Background(), check); e != nil || r.Allowed || r.Scope != "customer" || r.Metric != "tpm" {
		t.Fatalf("TPM: %+v %v", r, e)
	}
	check.Tokens = 5
	if r, e := l.Check(context.Background(), check); e != nil || !r.Allowed {
		t.Fatalf("denial consumed tokens: %+v %v", r, e)
	}
	l = New()
	now := time.Now()
	l.prunedAt = now
	for i := 0; i < 65536; i++ {
		l.memory[fmt.Sprint(i)] = memoryCounter{Used: 1, ExpiresAt: now.Add(time.Minute)}
	}
	spec := []limitSpec{{Key: "new", Scope: "customer", Metric: "rpm", Limit: 1, Increment: 1}}
	if _, e := l.checkMemory(spec, now, now.Add(time.Minute)); e == nil {
		t.Fatal("unbounded local counters")
	}
	if r, e := l.checkMemory(spec, now.Add(2*time.Minute), now.Add(3*time.Minute)); e != nil || !r.Allowed || len(l.memory) != 1 {
		t.Fatalf("expired counters not recovered: %+v %v", r, e)
	}
}
