// Package concurrency enforces distributed in-flight request limits.
package concurrency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultKeyPrefix = "aiport" // Preserve the pre-rename storage namespace.
	defaultIOTimeout = 100 * time.Millisecond
	maxScopes        = 8
)

// acquireScript atomically removes expired leases, verifies every requested
// scope, and then adds the request to all scopes. Redis TIME keeps expiry
// decisions independent of gateway-host clock skew. An existing member is an
// idempotent retry and therefore does not consume a second slot.
var acquireScript = redis.NewScript(`
local redis_time = redis.call('TIME')
local now_ms = (tonumber(redis_time[1]) * 1000) + math.floor(tonumber(redis_time[2]) / 1000)
local ttl_ms = tonumber(ARGV[1])
local member = ARGV[2]
local count = tonumber(ARGV[3])
local expires_ms = now_ms + ttl_ms

for i = 1, count do
  redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', now_ms)
  local existing = redis.call('ZSCORE', KEYS[i], member)
  local current = redis.call('ZCARD', KEYS[i])
  local limit = tonumber(ARGV[3 + i])
  if (not existing) and current >= limit then
    local oldest = redis.call('ZRANGE', KEYS[i], 0, 0, 'WITHSCORES')
    local retry_ms = 1000
    if oldest[2] then
      retry_ms = math.max(1, tonumber(oldest[2]) - now_ms)
    end
    return {0, i, retry_ms}
  end
end

for i = 1, count do
  redis.call('ZADD', KEYS[i], expires_ms, member)
  redis.call('PEXPIRE', KEYS[i], ttl_ms + 60000)
end
return {1, 0, 0}
`)

var releaseScript = redis.NewScript(`
local member = ARGV[1]
local removed = 0
for i = 1, #KEYS do
  removed = removed + redis.call('ZREM', KEYS[i], member)
end
return removed
`)

type Scope struct {
	Kind  string
	ID    int64
	Limit int
}

type Request struct {
	ID        string
	Partition int64
	TTL       time.Duration
	Scopes    []Scope
}

type Result struct {
	Allowed    bool
	Scope      string
	RetryAfter time.Duration
}

// Limiter is safe for concurrent use. It deliberately has no local fallback:
// allowing independently in each process would violate a configured
// distributed cap. Callers skip it entirely when no scope has a limit.
type Limiter struct {
	redis     redis.Scripter
	closer    interface{ Close() error }
	keyPrefix string
	ioTimeout time.Duration
}

func NewRedis(addr, password string, db int, keyPrefix string) *Limiter {
	client := redis.NewClient(&redis.Options{
		Addr:                  addr,
		Password:              password,
		DB:                    db,
		DialTimeout:           defaultIOTimeout,
		ReadTimeout:           defaultIOTimeout,
		WriteTimeout:          defaultIOTimeout,
		PoolTimeout:           defaultIOTimeout,
		ContextTimeoutEnabled: true,
		MaxRetries:            -1,
	})
	return NewWithClient(client, keyPrefix)
}

func NewWithClient(client redis.UniversalClient, keyPrefix string) *Limiter {
	if keyPrefix == "" {
		keyPrefix = defaultKeyPrefix
	}
	return &Limiter{
		redis:     client,
		closer:    client,
		keyPrefix: keyPrefix,
		ioTimeout: defaultIOTimeout,
	}
}

func (l *Limiter) Acquire(ctx context.Context, req Request) (*Lease, Result, error) {
	if len(req.Scopes) == 0 {
		return nil, Result{Allowed: true}, nil
	}
	if l == nil || l.redis == nil {
		return nil, Result{}, fmt.Errorf("distributed concurrency store is not configured")
	}
	if req.ID == "" {
		return nil, Result{}, fmt.Errorf("request id is required")
	}
	if req.Partition <= 0 {
		return nil, Result{}, fmt.Errorf("positive concurrency partition is required")
	}
	if req.TTL <= 0 {
		return nil, Result{}, fmt.Errorf("lease ttl must be positive")
	}
	if len(req.Scopes) > maxScopes {
		return nil, Result{}, fmt.Errorf("too many concurrency scopes: %d", len(req.Scopes))
	}

	keys := make([]string, 0, len(req.Scopes))
	args := make([]any, 0, 3+len(req.Scopes))
	args = append(args, maxInt64(req.TTL.Milliseconds(), 1), req.ID, len(req.Scopes))
	for _, scope := range req.Scopes {
		if !validScopeKind(scope.Kind) || scope.ID <= 0 || scope.Limit <= 0 {
			return nil, Result{}, fmt.Errorf("invalid concurrency scope: kind=%q id=%d limit=%d", scope.Kind, scope.ID, scope.Limit)
		}
		// IDs are globally unique within each scope kind. A common hash tag
		// permits atomic cross-team user and shared provider/model checks in
		// Redis Cluster. Partition identifies the calling team, not the owner
		// of a counter: partitioning users by team would multiply their cap.
		keys = append(keys, fmt.Sprintf("%s:concurrency:{gateway}:%s:%d", l.keyPrefix, scope.Kind, scope.ID))
		args = append(args, scope.Limit)
	}

	callCtx, cancel := boundedContext(ctx, l.ioTimeout)
	defer cancel()
	raw, err := acquireScript.Run(callCtx, l.redis, keys, args...).Result()
	if err != nil {
		return nil, Result{}, fmt.Errorf("acquire distributed concurrency lease: %w", err)
	}
	values, ok := raw.([]any)
	if !ok || len(values) != 3 {
		return nil, Result{}, fmt.Errorf("unexpected concurrency result: %T", raw)
	}
	allowed, err := asInt64(values[0])
	if err != nil {
		return nil, Result{}, err
	}
	if allowed == 1 {
		return &Lease{limiter: l, member: req.ID, keys: keys}, Result{Allowed: true}, nil
	}
	index, err := asInt64(values[1])
	if err != nil {
		return nil, Result{}, err
	}
	retryMs, err := asInt64(values[2])
	if err != nil {
		return nil, Result{}, err
	}
	scopeIndex := int(index) - 1
	if scopeIndex < 0 || scopeIndex >= len(req.Scopes) {
		scopeIndex = 0
	}
	return nil, Result{
		Allowed:    false,
		Scope:      req.Scopes[scopeIndex].Kind,
		RetryAfter: time.Duration(maxInt64(retryMs, 1)) * time.Millisecond,
	}, nil
}

// NewRequestID creates an internal lease identity, independent of client headers.
func NewRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Sprintf("generate concurrency lease identity: %v", err))
	}
	return hex.EncodeToString(value[:])
}

func (l *Limiter) Ready(ctx context.Context) error {
	if l == nil || l.redis == nil {
		return nil
	}
	client, ok := l.redis.(interface {
		Ping(context.Context) *redis.StatusCmd
	})
	if !ok {
		return nil
	}
	callCtx, cancel := boundedContext(ctx, l.ioTimeout)
	defer cancel()
	return client.Ping(callCtx).Err()
}

func (l *Limiter) Close() error {
	if l == nil || l.closer == nil {
		return nil
	}
	return l.closer.Close()
}

// Lease is an idempotently releasable set of distributed scope memberships.
type Lease struct {
	once    sync.Once
	limiter *Limiter
	member  string
	keys    []string
	err     error
}

// Release uses an independent short context because the request context is
// commonly canceled at the exact moment the handler returns. A failed release
// is still crash-safe: the Redis score expires at the configured lease TTL.
func (l *Lease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.limiter == nil || l.limiter.redis == nil || len(l.keys) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), l.limiter.ioTimeout)
		defer cancel()
		if err := releaseScript.Run(ctx, l.limiter.redis, l.keys, l.member).Err(); err != nil {
			l.err = fmt.Errorf("release distributed concurrency lease: %w", err)
		}
	})
	return l.err
}

func validScopeKind(kind string) bool {
	switch strings.ToLower(kind) {
	case "team", "key", "user", "service_account", "customer", "provider", "model":
		return kind == strings.ToLower(kind)
	default:
		return false
	}
}

func boundedContext(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= limit {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, limit)
}

func asInt64(value any) (int64, error) {
	switch number := value.(type) {
	case int64:
		return number, nil
	case int:
		return int64(number), nil
	case string:
		var parsed int64
		if _, err := fmt.Sscan(number, &parsed); err != nil {
			return 0, fmt.Errorf("unexpected numeric value %q", number)
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("unexpected numeric type %T", value)
	}
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
