package router

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// BreakerStore is the optional cross-replica view of the per-deployment
// breaker. The router still maintains an in-memory cache for the hot
// path; the store is consulted on every IsOpen check (with a tight
// timeout) so a peer replica's breaker decisions are honored within
// the round-trip of a single Redis GET.
//
// Implementations:
//   - nil — single-replica mode, in-memory state is the only truth
//   - RedisBreakerStore — multi-replica, writes through, reads with timeout
type BreakerStore interface {
	// IsOpen returns the deployment's open-until time if currently
	// open, or zero time when closed. Implementations should bound
	// their I/O — slow reads cost every /v1 request.
	IsOpen(ctx context.Context, deployment string) (time.Time, error)
	// RecordFailure increments the per-deployment failure counter and
	// returns the new value plus the open-until (zero if not yet open).
	RecordFailure(ctx context.Context, deployment string, threshold int, cooldown time.Duration) (failures int, openUntil time.Time, err error)
	// RecordSuccess clears any in-flight failure counter for the
	// deployment. Idempotent.
	RecordSuccess(ctx context.Context, deployment string) error
}

// RedisBreakerStore is the multi-replica implementation. Three keys
// per deployment:
//   <prefix>:breaker:<name>:failures     — counter, TTL = cooldown
//   <prefix>:breaker:<name>:open_until   — RFC3339 timestamp, TTL = cooldown
type RedisBreakerStore struct {
	client *redis.Client
	prefix string
	// IOTimeout caps every individual Redis call. Beyond this we fall
	// back to whatever the local cache says — slow Redis can't take
	// the gateway down with it.
	IOTimeout time.Duration
}

func NewRedisBreakerStore(client *redis.Client, keyPrefix string) *RedisBreakerStore {
	if keyPrefix == "" {
		keyPrefix = "aiport" // Preserve the pre-rename storage namespace.
	}
	return &RedisBreakerStore{client: client, prefix: keyPrefix, IOTimeout: 50 * time.Millisecond}
}

func (s *RedisBreakerStore) failuresKey(name string) string {
	return s.prefix + ":breaker:" + name + ":failures"
}
func (s *RedisBreakerStore) openKey(name string) string {
	return s.prefix + ":breaker:" + name + ":open_until"
}

func (s *RedisBreakerStore) IsOpen(ctx context.Context, deployment string) (time.Time, error) {
	if s == nil || s.client == nil {
		return time.Time{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.IOTimeout)
	defer cancel()
	v, err := s.client.Get(ctx, s.openKey(deployment)).Result()
	if err == redis.Nil {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("breaker is_open: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse open_until: %w", err)
	}
	if t.Before(time.Now()) {
		return time.Time{}, nil
	}
	return t, nil
}

func (s *RedisBreakerStore) RecordFailure(ctx context.Context, deployment string, threshold int, cooldown time.Duration) (int, time.Time, error) {
	if s == nil || s.client == nil {
		return 0, time.Time{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.IOTimeout)
	defer cancel()
	pipe := s.client.Pipeline()
	failuresIncr := pipe.Incr(ctx, s.failuresKey(deployment))
	pipe.PExpire(ctx, s.failuresKey(deployment), cooldown)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, time.Time{}, fmt.Errorf("breaker incr: %w", err)
	}
	failures := int(failuresIncr.Val())
	if failures < threshold {
		return failures, time.Time{}, nil
	}
	openUntil := time.Now().Add(cooldown)
	if err := s.client.Set(ctx, s.openKey(deployment), openUntil.Format(time.RFC3339Nano), cooldown).Err(); err != nil {
		return failures, time.Time{}, fmt.Errorf("breaker set open: %w", err)
	}
	return failures, openUntil, nil
}

func (s *RedisBreakerStore) RecordSuccess(ctx context.Context, deployment string) error {
	if s == nil || s.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, s.IOTimeout)
	defer cancel()
	pipe := s.client.Pipeline()
	pipe.Del(ctx, s.failuresKey(deployment))
	pipe.Del(ctx, s.openKey(deployment))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("breaker reset: %w", err)
	}
	return nil
}

// breakerKeysFor is a debug helper used by integration tests; not on
// any hot path.
func (s *RedisBreakerStore) breakerKeysFor(deployment string) []string {
	return []string{s.failuresKey(deployment), s.openKey(deployment)}
}

// strconvForBreaker is a lint-shim — keeps strconv imported for callers
// that emit numeric breaker keys in audit metadata. Cheap to keep.
var _ = strconv.Itoa
