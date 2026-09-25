package concurrency

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

type scriptCall struct {
	keys []string
	args []any
}

type scriptedRedis struct {
	mu      sync.Mutex
	results []any
	calls   []scriptCall
}

func (s *scriptedRedis) next(keys []string, args ...any) *redis.Cmd {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, scriptCall{keys: append([]string(nil), keys...), args: append([]any(nil), args...)})
	if len(s.results) == 0 {
		return redis.NewCmdResult(nil, fmt.Errorf("unexpected script call"))
	}
	result := s.results[0]
	s.results = s.results[1:]
	if err, ok := result.(error); ok {
		return redis.NewCmdResult(nil, err)
	}
	return redis.NewCmdResult(result, nil)
}

func (s *scriptedRedis) Eval(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(nil, fmt.Errorf("unexpected EVAL"))
}
func (s *scriptedRedis) EvalSha(_ context.Context, _ string, keys []string, args ...any) *redis.Cmd {
	return s.next(keys, args...)
}
func (s *scriptedRedis) EvalRO(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(nil, fmt.Errorf("unexpected EVAL_RO"))
}
func (s *scriptedRedis) EvalShaRO(context.Context, string, []string, ...any) *redis.Cmd {
	return redis.NewCmdResult(nil, fmt.Errorf("unexpected EVALSHA_RO"))
}
func (s *scriptedRedis) ScriptExists(context.Context, ...string) *redis.BoolSliceCmd {
	return redis.NewBoolSliceResult(nil, fmt.Errorf("unexpected SCRIPT EXISTS"))
}
func (s *scriptedRedis) ScriptLoad(context.Context, string) *redis.StringCmd {
	return redis.NewStringResult("", fmt.Errorf("unexpected SCRIPT LOAD"))
}

func TestAcquireBuildsBoundedScopeKeysAndReleaseIsIdempotent(t *testing.T) {
	backend := &scriptedRedis{results: []any{
		[]any{int64(1), int64(0), int64(0)},
		int64(2),
	}}
	limiter := &Limiter{redis: backend, keyPrefix: "test", ioTimeout: time.Second}
	lease, result, err := limiter.Acquire(context.Background(), Request{
		ID:        "req-1",
		Partition: 7,
		TTL:       2 * time.Minute,
		Scopes: []Scope{
			{Kind: "team", ID: 7, Limit: 5},
			{Kind: "key", ID: 11, Limit: 2},
		},
	})
	if err != nil || !result.Allowed || lease == nil {
		t.Fatalf("Acquire() = lease %v, result %+v, err %v", lease, result, err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.calls) != 2 {
		t.Fatalf("script calls = %d, want acquire + one release", len(backend.calls))
	}
	wantKeys := []string{"test:concurrency:{gateway}:team:7", "test:concurrency:{gateway}:key:11"}
	for i, want := range wantKeys {
		if got := backend.calls[0].keys[i]; got != want {
			t.Errorf("acquire key %d = %q, want %q", i, got, want)
		}
	}
	if got := backend.calls[0].args[1]; got != "req-1" {
		t.Errorf("request member = %v, want req-1", got)
	}
	if got := backend.calls[0].args[2]; got != 2 {
		t.Errorf("scope count = %v, want 2", got)
	}
}

func TestAcquireReturnsDeniedScopeAndRetry(t *testing.T) {
	backend := &scriptedRedis{results: []any{
		[]any{int64(0), int64(2), int64(1750)},
	}}
	limiter := &Limiter{redis: backend, keyPrefix: "test", ioTimeout: time.Second}
	lease, result, err := limiter.Acquire(context.Background(), Request{
		ID: "req-2", Partition: 1, TTL: time.Minute,
		Scopes: []Scope{{Kind: "team", ID: 1, Limit: 10}, {Kind: "key", ID: 2, Limit: 1}},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if lease != nil || result.Allowed || result.Scope != "key" || result.RetryAfter != 1750*time.Millisecond {
		t.Fatalf("Acquire() = lease %v, result %+v", lease, result)
	}
}

func TestAcquireRejectsInvalidOrUnboundedInput(t *testing.T) {
	limiter := &Limiter{}
	if _, result, err := limiter.Acquire(context.Background(), Request{}); err != nil || !result.Allowed {
		t.Fatalf("empty scopes should bypass: result %+v, err %v", result, err)
	}
	backend := &scriptedRedis{}
	limiter = &Limiter{redis: backend, keyPrefix: "test", ioTimeout: time.Second}
	cases := []Request{
		{ID: "", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}}},
		{ID: "x", Partition: 0, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}}},
		{ID: "x", Partition: 1, TTL: 0, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}}},
		{ID: "x", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "bogus", ID: 1, Limit: 1}}},
		{ID: "x", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 0, Limit: 1}}},
		{ID: "x", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 0}}},
	}
	for _, req := range cases {
		if _, _, err := limiter.Acquire(context.Background(), req); err == nil {
			t.Errorf("Acquire(%+v) unexpectedly succeeded", req)
		}
	}
}

func TestRedisAtomicLimitsIdempotencyReleaseAndExpiry(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GATEMUX_TEST_REDIS_ADDR to run Redis integration coverage")
	}
	prefix := fmt.Sprintf("gatemux-test-%d", time.Now().UnixNano())
	limiter := NewRedis(addr, "", 0, prefix)
	defer func() { _ = limiter.Close() }()
	ctx := context.Background()

	first, result, err := limiter.Acquire(ctx, Request{
		ID: "first", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}},
	})
	if err != nil || !result.Allowed {
		t.Fatalf("first acquire: result %+v, err %v", result, err)
	}
	retry, result, err := limiter.Acquire(ctx, Request{
		ID: "first", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}},
	})
	if err != nil || !result.Allowed {
		t.Fatalf("idempotent acquire: result %+v, err %v", result, err)
	}
	if _, result, err := limiter.Acquire(ctx, Request{
		ID: "second", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}},
	}); err != nil || result.Allowed || result.Scope != "team" {
		t.Fatalf("saturated acquire: result %+v, err %v", result, err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	if err := retry.Release(); err != nil {
		t.Fatal(err)
	}

	// Fill only the key scope. A compound admission rejected by that key
	// must not leave a partial membership in its otherwise-empty team scope.
	keyLease, _, err := limiter.Acquire(ctx, Request{
		ID: "key-holder", Partition: 2, TTL: time.Second, Scopes: []Scope{{Kind: "key", ID: 9, Limit: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, result, err := limiter.Acquire(ctx, Request{
		ID: "compound", Partition: 2, TTL: time.Second,
		Scopes: []Scope{{Kind: "team", ID: 2, Limit: 1}, {Kind: "key", ID: 9, Limit: 1}},
	}); err != nil || result.Allowed || result.Scope != "key" {
		t.Fatalf("compound denial: result %+v, err %v", result, err)
	}
	teamLease, result, err := limiter.Acquire(ctx, Request{
		ID: "team-only", Partition: 2, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 2, Limit: 1}},
	})
	if err != nil || !result.Allowed {
		t.Fatalf("partial team lease leaked: result %+v, err %v", result, err)
	}
	_ = keyLease.Release()
	_ = teamLease.Release()

	if _, result, err := limiter.Acquire(ctx, Request{
		ID: "crashed", Partition: 3, TTL: 80 * time.Millisecond, Scopes: []Scope{{Kind: "team", ID: 3, Limit: 1}},
	}); err != nil || !result.Allowed {
		t.Fatalf("expiry setup: result %+v, err %v", result, err)
	}
	time.Sleep(120 * time.Millisecond)
	expiredLease, result, err := limiter.Acquire(ctx, Request{
		ID: "after-crash", Partition: 3, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 3, Limit: 1}},
	})
	if err != nil || !result.Allowed {
		t.Fatalf("expired lease was not recovered: result %+v, err %v", result, err)
	}
	_ = expiredLease.Release()
}

func TestRedisUserCapIsSharedAcrossTeamsAndReplicas(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GATEMUX_TEST_REDIS_ADDR for Redis integration")
	}
	prefix := fmt.Sprintf("shared-user-%d", time.Now().UnixNano())
	first, second := NewRedis(addr, "", 0, prefix), NewRedis(addr, "", 0, prefix)
	defer first.Close()
	defer second.Close()
	lease, result, err := first.Acquire(context.Background(), Request{ID: "team-one", Partition: 1, TTL: time.Second, Scopes: []Scope{{Kind: "user", ID: 9, Limit: 1}}})
	if err != nil || !result.Allowed {
		t.Fatalf("first request: %+v %v", result, err)
	}
	defer lease.Release()
	_, result, err = second.Acquire(context.Background(), Request{ID: "team-two", Partition: 2, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 2, Limit: 1}, {Kind: "user", ID: 9, Limit: 1}}})
	if err != nil || result.Allowed || result.Scope != "user" {
		t.Fatalf("cross-team user cap bypass: %+v %v", result, err)
	}
	// The denial must not partially reserve the second team.
	team, result, err := second.Acquire(context.Background(), Request{ID: "other-user", Partition: 2, TTL: time.Second, Scopes: []Scope{{Kind: "team", ID: 2, Limit: 1}}})
	if err != nil || !result.Allowed {
		t.Fatalf("partial membership leaked: %+v %v", result, err)
	}
	defer team.Release()
}

func TestRedisConcurrentReplicasNeverExceedCap(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set GATEMUX_TEST_REDIS_ADDR for Redis integration")
	}
	prefix := fmt.Sprintf("contended-%d", time.Now().UnixNano())
	clients := []*Limiter{NewRedis(addr, "", 0, prefix), NewRedis(addr, "", 0, prefix)}
	defer clients[0].Close()
	defer clients[1].Close()
	const workers, capacity = 32, 7
	var wg sync.WaitGroup
	results := make(chan *Lease, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, result, err := clients[i%2].Acquire(context.Background(), Request{ID: fmt.Sprint(i), Partition: int64(i + 1), TTL: 5 * time.Second, Scopes: []Scope{{Kind: "model", ID: 1, Limit: capacity}, {Kind: "provider", ID: 2, Limit: capacity}}})
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			if result.Allowed {
				results <- lease
			}
		}(i)
	}
	wg.Wait()
	close(results)
	admitted := 0
	for lease := range results {
		admitted++
		if err := lease.Release(); err != nil {
			t.Error(err)
		}
	}
	if admitted != capacity {
		t.Fatalf("admitted %d, want %d", admitted, capacity)
	}
}

func TestRedisBlackholeRespectsIOBound(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	defer close(done)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		<-done // accept TCP but never complete Redis handshake
	}()
	l := NewRedis(listener.Addr().String(), "", 0, "blackhole")
	defer l.Close()
	started := time.Now()
	_, _, err = l.Acquire(context.Background(), Request{ID: "one", Partition: 1, TTL: time.Minute, Scopes: []Scope{{Kind: "team", ID: 1, Limit: 1}}})
	if err == nil {
		t.Fatal("blackholed Redis unexpectedly accepted lease")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Redis IO exceeded bound: %s", elapsed)
	}
}
