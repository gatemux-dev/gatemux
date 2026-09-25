package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gatemux-dev/gatemux/internal/store"
)

const windowSize = time.Minute

var allowScript = redis.NewScript(`
local default_ttl_ms = tonumber(ARGV[1])
local count = tonumber(ARGV[2])
for i = 1, count do
  local base = 2 + ((i - 1) * 2)
  local limit = tonumber(ARGV[base + 1])
  local inc = tonumber(ARGV[base + 2])
  local current = tonumber(redis.call('GET', KEYS[i]) or '0')
  if current + inc > limit then
    local ttl = redis.call('PTTL', KEYS[i])
    if ttl < 0 then
      ttl = default_ttl_ms
    end
    return {0, i, ttl}
  end
end
for i = 1, count do
  local base = 2 + ((i - 1) * 2)
  local inc = tonumber(ARGV[base + 2])
  local current = redis.call('INCRBY', KEYS[i], inc)
  if current == inc then
    redis.call('PEXPIRE', KEYS[i], default_ttl_ms)
  end
end
return {1, 0, 0}
`)

type Check struct {
	Team     *store.Team
	Key      *store.VirtualKey
	Customer *store.Customer
	Tokens   int
}

type Result struct {
	Allowed    bool
	RetryAfter time.Duration
	Scope      string
	Metric     string
}

type Limiter struct {
	mu        sync.Mutex
	memory    map[string]memoryCounter
	redis     *redis.Client
	keyPrefix string
	prunedAt  time.Time
}

type memoryCounter struct {
	Used      int
	ExpiresAt time.Time
}

type limitSpec struct {
	Key       string
	Scope     string
	Metric    string
	Limit     int
	Increment int
}

func New() *Limiter {
	return &Limiter{
		memory:    make(map[string]memoryCounter),
		keyPrefix: "aiport", // Preserve the pre-rename storage namespace.
	}
}

func NewRedis(addr, password string, db int, keyPrefix string) *Limiter {
	if keyPrefix == "" {
		keyPrefix = "aiport" // Preserve the pre-rename storage namespace.
	}
	return &Limiter{
		memory: make(map[string]memoryCounter),
		redis: redis.NewClient(&redis.Options{
			Addr: addr, Password: password, DB: db,
			DialTimeout: 100 * time.Millisecond, ReadTimeout: 100 * time.Millisecond,
			WriteTimeout: 100 * time.Millisecond, PoolTimeout: 100 * time.Millisecond,
			ContextTimeoutEnabled: true, MaxRetries: -1,
		}),
		keyPrefix: keyPrefix,
	}
}

func (l *Limiter) Close() error {
	if l == nil || l.redis == nil {
		return nil
	}
	return l.redis.Close()
}

func (l *Limiter) Ready(ctx context.Context) error {
	if l == nil || l.redis == nil {
		return nil
	}
	return l.redis.Ping(ctx).Err()
}

func (l *Limiter) Check(ctx context.Context, check Check) (Result, error) {
	if l == nil {
		return Result{Allowed: true}, nil
	}
	if check.Tokens < 0 {
		check.Tokens = 0
	}
	if check.Customer != nil && (check.Team == nil || check.Customer.TeamID != check.Team.ID) {
		return Result{}, fmt.Errorf("customer rate policy team mismatch")
	}
	now := time.Now().UTC()
	specs, windowEnd := l.buildSpecs(check, now)
	if len(specs) == 0 {
		return Result{Allowed: true}, nil
	}
	if l.redis != nil {
		return l.checkRedis(ctx, specs, now, windowEnd)
	}
	return l.checkMemory(specs, now, windowEnd)
}

func (l *Limiter) buildSpecs(check Check, now time.Time) ([]limitSpec, time.Time) {
	windowStart := now.Truncate(windowSize)
	windowEnd := windowStart.Add(windowSize)
	windowBucket := windowStart.Unix()

	specs := make([]limitSpec, 0, 6)
	if check.Team != nil {
		if check.Team.RPM != nil && *check.Team.RPM > 0 {
			specs = append(specs, limitSpec{
				Key:       fmt.Sprintf("%s:ratelimit:team:%d:rpm:%d", l.keyPrefix, check.Team.ID, windowBucket),
				Scope:     "team",
				Metric:    "rpm",
				Limit:     *check.Team.RPM,
				Increment: 1,
			})
		}
		if check.Team.TPM != nil && *check.Team.TPM > 0 && check.Tokens > 0 {
			specs = append(specs, limitSpec{
				Key:       fmt.Sprintf("%s:ratelimit:team:%d:tpm:%d", l.keyPrefix, check.Team.ID, windowBucket),
				Scope:     "team",
				Metric:    "tpm",
				Limit:     *check.Team.TPM,
				Increment: check.Tokens,
			})
		}
	}
	if check.Key != nil {
		if check.Key.ScopedRPM != nil && *check.Key.ScopedRPM > 0 {
			specs = append(specs, limitSpec{
				Key:       fmt.Sprintf("%s:ratelimit:key:%d:rpm:%d", l.keyPrefix, check.Key.ID, windowBucket),
				Scope:     "key",
				Metric:    "rpm",
				Limit:     *check.Key.ScopedRPM,
				Increment: 1,
			})
		}
		if check.Key.ScopedTPM != nil && *check.Key.ScopedTPM > 0 && check.Tokens > 0 {
			specs = append(specs, limitSpec{
				Key:       fmt.Sprintf("%s:ratelimit:key:%d:tpm:%d", l.keyPrefix, check.Key.ID, windowBucket),
				Scope:     "key",
				Metric:    "tpm",
				Limit:     *check.Key.ScopedTPM,
				Increment: check.Tokens,
			})
		}
	}
	if c := check.Customer; c != nil && c.ID > 0 {
		if c.RPM != nil && *c.RPM > 0 {
			specs = append(specs, limitSpec{Key: fmt.Sprintf("%s:ratelimit:customer:%d:rpm:%d", l.keyPrefix, c.ID, windowBucket), Scope: "customer", Metric: "rpm", Limit: *c.RPM, Increment: 1})
		}
		if c.TPM != nil && *c.TPM > 0 && check.Tokens > 0 {
			specs = append(specs, limitSpec{Key: fmt.Sprintf("%s:ratelimit:customer:%d:tpm:%d", l.keyPrefix, c.ID, windowBucket), Scope: "customer", Metric: "tpm", Limit: *c.TPM, Increment: check.Tokens})
		}
	}
	return specs, windowEnd
}

func (l *Limiter) checkMemory(specs []limitSpec, now, windowEnd time.Time) (Result, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Development-only backend still has bounded cardinality and expiry. Never
	// evict live counters to admit traffic, which would reset a caller's limit.
	if l.prunedAt.IsZero() || now.Sub(l.prunedAt) >= windowSize {
		for key, value := range l.memory {
			if !value.ExpiresAt.After(now) {
				delete(l.memory, key)
			}
		}
		l.prunedAt = now
	}
	missing := 0
	for _, spec := range specs {
		if _, exists := l.memory[spec.Key]; !exists {
			missing++
		}
	}
	if len(l.memory)+missing > 65536 {
		return Result{}, fmt.Errorf("local rate counter capacity exhausted")
	}

	for _, spec := range specs {
		counter, ok := l.memory[spec.Key]
		if ok && !counter.ExpiresAt.After(now) {
			delete(l.memory, spec.Key)
			ok = false
		}
		used := 0
		if ok {
			used = counter.Used
		}
		if used > spec.Limit || spec.Increment > spec.Limit-used {
			return Result{
				Allowed:    false,
				RetryAfter: maxDuration(windowEnd.Sub(now), time.Second),
				Scope:      spec.Scope,
				Metric:     spec.Metric,
			}, nil
		}
	}

	for _, spec := range specs {
		counter := l.memory[spec.Key]
		counter.Used += spec.Increment
		counter.ExpiresAt = windowEnd
		l.memory[spec.Key] = counter
	}

	return Result{Allowed: true}, nil
}

func (l *Limiter) checkRedis(ctx context.Context, specs []limitSpec, now, windowEnd time.Time) (Result, error) {
	keys := make([]string, 0, len(specs))
	args := make([]any, 0, 2+(len(specs)*2))
	retryAfter := windowEnd.Sub(now)
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	args = append(args, retryAfter.Milliseconds(), len(specs))
	for _, spec := range specs {
		keys = append(keys, spec.Key)
		args = append(args, spec.Limit, spec.Increment)
	}

	raw, err := allowScript.Run(ctx, l.redis, keys, args...).Result()
	if err != nil {
		return Result{}, err
	}
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return Result{}, fmt.Errorf("unexpected rate limit result: %T", raw)
	}
	allowed, err := asInt64(values[0])
	if err != nil {
		return Result{}, err
	}
	if allowed == 1 {
		return Result{Allowed: true}, nil
	}
	index, err := asInt64(values[1])
	if err != nil {
		return Result{}, err
	}
	ttlMs, err := asInt64(values[2])
	if err != nil {
		return Result{}, err
	}
	specIdx := int(index) - 1
	if specIdx < 0 || specIdx >= len(specs) {
		specIdx = 0
	}
	return Result{
		Allowed:    false,
		RetryAfter: maxDuration(time.Duration(ttlMs)*time.Millisecond, time.Second),
		Scope:      specs[specIdx].Scope,
		Metric:     specs[specIdx].Metric,
	}, nil
}

func asInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	default:
		return 0, fmt.Errorf("unexpected numeric type %T", v)
	}
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
