package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

const loginCounterCapacity = 10000

// Reserve attempts before password verification, including concurrent requests.
// One fixed-window hash bounds Redis cardinality. Its TTL bounds retention;
// fields contain only SHA-256 identity digests. Workers share one hash slot.
var loginAttemptScript = redis.NewScript(`
local missing = 0
for i = 1, #ARGV - 2, 2 do
  if tonumber(redis.call('HGET', KEYS[1], ARGV[i]) or '0') >= tonumber(ARGV[i+1]) then
    return 0
  end
  if redis.call('HEXISTS', KEYS[1], ARGV[i]) == 0 then missing = missing + 1 end
end
if redis.call('HLEN', KEYS[1]) + missing > tonumber(ARGV[#ARGV-1]) then return -1 end
for i = 1, #ARGV - 2, 2 do redis.call('HINCRBY', KEYS[1], ARGV[i], 1) end
redis.call('PEXPIRE', KEYS[1], ARGV[#ARGV])
return 1
`)

// Limits count all attempts: 20 per source IP and 5 per normalized account,
// per endpoint and 15-minute UTC window. Success does not reset counters.
// Redis failure/capacity exhaustion fails closed with 503. Without Redis,
// the same bounded policy is process-local, not distributed.
type loginThrottle struct {
	mu     sync.Mutex
	counts map[string]int
	bucket int64
	st     *store.Store
	redis  *redis.Client // borrowed; owned and closed by Server
	prefix string
	now    func() time.Time
}

func newLoginThrottle(st *store.Store) *loginThrottle {
	return &loginThrottle{st: st, counts: make(map[string]int), now: time.Now}
}

func loginIdentity(kind, value string) string {
	digest := sha256.Sum256([]byte(value))
	return kind + ":" + hex.EncodeToString(digest[:])
}

func (t *loginThrottle) allow(ctx context.Context, endpoint, ip, email string) (int, time.Duration) {
	const window = 15 * time.Minute
	now := t.now().UTC()
	start := now.Truncate(window)
	retry := start.Add(window).Sub(now)
	fields := []string{loginIdentity("ip", endpoint+"\x00"+ip)}
	limits := []int{20}
	if email != "" {
		fields = append(fields, loginIdentity("account", endpoint+"\x00"+email))
		limits = append(limits, 5)
	}
	if t.redis != nil {
		args := make([]any, 0, 6)
		for i, field := range fields {
			args = append(args, field, limits[i])
		}
		args = append(args, loginCounterCapacity, retry.Milliseconds()+1)
		ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		key := fmt.Sprintf("%s:{login-attempts}:%d", t.prefix, start.Unix())
		result, err := loginAttemptScript.Run(ctx, t.redis, []string{key}, args...).Int()
		if err != nil || result < 0 {
			return http.StatusServiceUnavailable, retry
		}
		if result == 0 {
			return http.StatusTooManyRequests, retry
		}
		return 0, retry
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.bucket != start.Unix() {
		t.bucket, t.counts = start.Unix(), make(map[string]int)
	}
	missing := 0
	for i, field := range fields {
		if t.counts[field] >= limits[i] {
			return http.StatusTooManyRequests, retry
		}
		if _, ok := t.counts[field]; !ok {
			missing++
		}
	}
	if len(t.counts)+missing > loginCounterCapacity {
		return http.StatusServiceUnavailable, retry
	}
	for _, field := range fields {
		t.counts[field]++
	}
	return 0, retry
}

func (t *loginThrottle) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, email, endpoint := auth.ClientIP(r), "", "login"
		if strings.HasPrefix(r.URL.Path, "/reset/") {
			endpoint = "reset"
		}
		if r.Body != nil {
			// Independently bounded even when mounted without limitBody.
			body, err := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
			_ = r.Body.Close()
			if err != nil || len(body) > 1<<20 {
				http.Error(w, "invalid authentication body", http.StatusRequestEntityTooLarge)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			// A reset token, not a caller-supplied email, authenticates reset.
			if endpoint == "login" {
				var envelope struct {
					Email string `json:"email"`
				}
				_ = json.Unmarshal(body, &envelope)
				email = strings.ToLower(strings.TrimSpace(envelope.Email))
			}
		}
		status, retry := t.allow(r.Context(), endpoint, ip, email)
		if status != 0 {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			if status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", strconvSeconds(retry))
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"type":"rate_limited","message":"too many authentication attempts; try again later"}}`))
			} else {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"type":"authentication_unavailable","message":"authentication temporarily unavailable"}}`))
			}
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if ww.Status() == http.StatusUnauthorized && t.st != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 500*time.Millisecond)
			defer cancel()
			_ = t.st.InsertAuditEvent(ctx, store.AuditEvent{
				ActorType: "anonymous", ActorID: ip, Action: "auth.login_failed",
				ResourceType: "session", ResourceID: email,
				Metadata: map[string]any{"ip": ip, "email": email},
			})
		}
	})
}

func strconvSeconds(d time.Duration) string {
	seconds := (d + time.Second - 1) / time.Second
	if seconds < 1 {
		seconds = 1
	}
	return strconv.FormatInt(int64(seconds), 10)
}
