// Package cache implements the per-alias prompt cache (design doc 0001).
//
// Caching is keyed on (team_id, alias, sha256(canonical_request)). On a hit,
// the v1 handler replays the cached response and writes a usage_log row
// with cached=true and cost_cents=0. Misses fall through to the router.
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache is the cross-replica prompt cache. The nil value is a no-op cache —
// every Get returns ErrMiss and Set is silent — so callers don't need to
// branch on whether caching is configured.
type Cache struct {
	rc        *redis.Client
	keyPrefix string
}

// ErrMiss is returned by Get when no cached entry exists.
var ErrMiss = errors.New("cache miss")

func (c *Cache) Close() error {
	if c == nil || c.rc == nil {
		return nil
	}
	return c.rc.Close()
}

// New constructs a Redis-backed cache. addr="" returns a no-op cache.
func New(addr, password string, db int, keyPrefix string) *Cache {
	if addr == "" {
		return nil
	}
	if keyPrefix == "" {
		keyPrefix = "aiport" // Preserve the pre-rename storage namespace.
	}
	return &Cache{
		rc: redis.NewClient(&redis.Options{
			Addr: addr, Password: password, DB: db,
			DialTimeout: 250 * time.Millisecond, ReadTimeout: 250 * time.Millisecond,
			WriteTimeout: 250 * time.Millisecond, PoolTimeout: 250 * time.Millisecond,
			ContextTimeoutEnabled: true, MaxRetries: -1,
		}),
		keyPrefix: keyPrefix,
	}
}

// Entry is the wire shape stored in Redis. JSON over msgpack so the
// payload is greppable in dev (the perf cost is tiny next to the network
// hop savings).
type Entry struct {
	StatusCode       int               `json:"status_code"`
	Body             json.RawMessage   `json:"body,omitempty"`
	Chunks           []string          `json:"chunks,omitempty"`
	PromptTokens     int               `json:"prompt_tokens"`
	CompletionTokens int               `json:"completion_tokens"`
	UpstreamModel    string            `json:"upstream_model"`
	OriginRequestID  string            `json:"origin_request_id"`
	OriginDeployment string            `json:"origin_deployment"`
	CachedAt         time.Time         `json:"cached_at"`
	Headers          map[string]string `json:"headers,omitempty"`
}

// Key computes the canonical cache key for a given team/alias/request body.
// The body is normalized so cosmetic differences (whitespace, key order)
// don't fragment the cache.
func (c *Cache) Key(teamID int64, alias string, requestBody []byte) (string, error) {
	canonical, err := canonicalize(requestBody)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(canonical)
	prefix := c.keyPrefix
	if prefix == "" {
		prefix = "aiport" // Preserve the pre-rename storage namespace.
	}
	return fmt.Sprintf("%s:cache:%d:%s:%s", prefix, teamID, alias, hex.EncodeToString(h[:])), nil
}

// Get returns the cached entry or ErrMiss. A nil cache always misses.
func (c *Cache) Get(ctx context.Context, key string) (*Entry, error) {
	if c == nil || c.rc == nil {
		return nil, ErrMiss
	}
	raw, err := c.rc.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, ErrMiss
		}
		return nil, err
	}
	e := &Entry{}
	if err := json.Unmarshal(raw, e); err != nil {
		return nil, fmt.Errorf("decode cache entry: %w", err)
	}
	return e, nil
}

// Set stores the entry with the given TTL. ttl <= 0 is a no-op (caching
// disabled). A nil cache silently drops.
func (c *Cache) Set(ctx context.Context, key string, e *Entry, ttl time.Duration) error {
	if c == nil || c.rc == nil || ttl <= 0 {
		return nil
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode cache entry: %w", err)
	}
	return c.rc.Set(ctx, key, raw, ttl).Err()
}

// Delete removes a single key. Used by the admin "flush" endpoint.
func (c *Cache) Delete(ctx context.Context, key string) error {
	if c == nil || c.rc == nil {
		return nil
	}
	return c.rc.Del(ctx, key).Err()
}

// FlushAlias drops every cache entry under (team_id, alias). Uses SCAN
// rather than KEYS so we don't block Redis on a large keyspace.
func (c *Cache) FlushAlias(ctx context.Context, teamID int64, alias string) error {
	if c == nil || c.rc == nil {
		return nil
	}
	pattern := fmt.Sprintf("%s:cache:%d:%s:*", c.keyPrefix, teamID, alias)
	iter := c.rc.Scan(ctx, 0, pattern, 200).Iterator()
	for iter.Next(ctx) {
		if err := c.rc.Del(ctx, iter.Val()).Err(); err != nil {
			return err
		}
	}
	return iter.Err()
}

// canonicalize normalizes a JSON request body for hashing. Strips fields
// that shouldn't affect cache identity (stream, user, request_id, metadata)
// then re-encodes with sorted keys.
func canonicalize(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse request body: %w", err)
	}
	for _, k := range []string{"stream", "user", "request_id", "metadata", "stream_options"} {
		delete(raw, k)
	}
	if msgs, ok := raw["messages"].([]any); ok {
		for _, m := range msgs {
			if mm, ok := m.(map[string]any); ok {
				if s, ok := mm["content"].(string); ok {
					mm["content"] = normalizeWhitespace(s)
				}
			}
		}
	}
	if input, ok := raw["input"].(string); ok {
		raw["input"] = normalizeWhitespace(input)
	}
	return marshalSorted(raw)
}

var wsRun = regexp.MustCompile(`[ \t]+`)

func normalizeWhitespace(s string) string {
	s = strings.TrimSpace(s)
	s = wsRun.ReplaceAllString(s, " ")
	return s
}

// marshalSorted is json.Marshal with deterministic key order.
func marshalSorted(v any) ([]byte, error) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var buf strings.Builder
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			kb, _ := json.Marshal(k)
			buf.Write(kb)
			buf.WriteByte(':')
			vb, err := marshalSorted(t[k])
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte('}')
		return []byte(buf.String()), nil
	case []any:
		var buf strings.Builder
		buf.WriteByte('[')
		for i, item := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			vb, err := marshalSorted(item)
			if err != nil {
				return nil, err
			}
			buf.Write(vb)
		}
		buf.WriteByte(']')
		return []byte(buf.String()), nil
	default:
		return json.Marshal(v)
	}
}
