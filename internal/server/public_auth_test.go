package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestLoginThrottleBoundedAtomic(t *testing.T) {
	throttle := newLoginThrottle(nil)
	now := time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC)
	throttle.now = func() time.Time { return now }
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, retry := throttle.allow(context.Background(), "login", fmt.Sprint(i), "account@example.test")
			if status == 0 {
				accepted.Add(1)
			} else if status != 429 {
				t.Errorf("status %d", status)
			}
			if retry <= 0 || retry > 15*time.Minute {
				t.Error("unbounded retry")
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("accepted %d", accepted.Load())
	}
	// Failed admission does not consume a new IP counter.
	if len(throttle.counts) != 6 {
		t.Fatalf("counter count %d", len(throttle.counts))
	}
	now = now.Add(15 * time.Minute)
	if status, _ := throttle.allow(context.Background(), "login", "new", "account@example.test"); status != 0 {
		t.Fatal(status)
	}
	if len(throttle.counts) != 2 {
		t.Fatal("old window retained")
	}
	for len(throttle.counts) < loginCounterCapacity {
		throttle.counts[fmt.Sprint(len(throttle.counts))] = 1
	}
	if status, _ := throttle.allow(context.Background(), "login", "new-ip", "new-account"); status != 503 {
		t.Fatal("capacity must fail closed")
	}
	if len(throttle.counts) != loginCounterCapacity {
		t.Fatal("unbounded map")
	}
}

func TestLoginThrottleHTTP(t *testing.T) {
	throttle := newLoginThrottle(nil)
	calls := 0
	handler := throttle.middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "email") {
			t.Error("body was lost")
		}
		calls++
		w.WriteHeader(200)
	}))
	for i := 0; i < 6; i++ {
		email := "User@example.test"
		if i%2 == 1 {
			email = "  user@EXAMPLE.test  "
		}
		req := httptest.NewRequest("POST", "/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q}`, email)))
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if i < 5 && w.Code != 200 {
			t.Fatal(w.Code)
		}
		if i == 5 && (w.Code != 429 || w.Header().Get("Retry-After") == "" || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("missing throttle contract")
		}
	}
	if calls != 5 {
		t.Fatal("successful requests reset the shared counter")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", strings.NewReader(strings.Repeat("x", (1<<20)+1))))
	if w.Code != 413 || calls != 5 {
		t.Fatal("body limit not enforced")
	}
}

func TestLoginThrottleIPAndEndpointIsolation(t *testing.T) {
	throttle := newLoginThrottle(nil)
	throttle.now = func() time.Time { return time.Date(2026, 9, 23, 12, 1, 0, 0, time.UTC) }
	for i := 0; i < 21; i++ {
		status, _ := throttle.allow(context.Background(), "login", "192.0.2.1", fmt.Sprintf("user%d@example.test", i))
		want := 0
		if i == 20 {
			want = 429
		}
		if status != want {
			t.Fatalf("attempt %d: %d", i, status)
		}
	}
	if status, _ := throttle.allow(context.Background(), "reset", "192.0.2.1", ""); status != 0 {
		t.Fatal("login and reset share an IP quota")
	}
}

func TestLoginThrottleSharedRedis(t *testing.T) {
	addr := os.Getenv("GATEMUX_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("GATEMUX_TEST_REDIS_ADDR required")
	}
	rc := redis.NewClient(&redis.Options{Addr: addr, DialTimeout: 50 * time.Millisecond, ReadTimeout: 50 * time.Millisecond, WriteTimeout: 50 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true})
	t.Cleanup(func() { _ = rc.Close() })
	prefix := fmt.Sprintf("login-test-%d", time.Now().UnixNano())
	now := time.Now().UTC().Truncate(15 * time.Minute).Add(time.Minute)
	key := fmt.Sprintf("%s:{login-attempts}:%d", prefix, now.Truncate(15*time.Minute).Unix())
	t.Cleanup(func() { _ = rc.Del(context.Background(), key).Err() })
	workers := []*loginThrottle{newLoginThrottle(nil), newLoginThrottle(nil)}
	for _, w := range workers {
		w.redis, w.prefix, w.now = rc, prefix, func() time.Time { return now }
	}
	var accepted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			status, _ := workers[i%2].allow(context.Background(), "login", fmt.Sprint(i), "account@example.test")
			if status == 0 {
				accepted.Add(1)
			} else if status != 429 {
				t.Errorf("status %d", status)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 5 {
		t.Fatalf("cross-worker accepted %d", accepted.Load())
	}
	fields, err := rc.HKeys(context.Background(), key).Result()
	if err != nil || len(fields) != 6 {
		t.Fatalf("counter count=%d err=%v", len(fields), err)
	}
	for _, field := range fields {
		if strings.Contains(field, "example") {
			t.Fatal("identity in Redis key")
		}
	}
	if ttl := rc.TTL(context.Background(), key).Val(); ttl <= 0 || ttl > 15*time.Minute {
		t.Fatal("unbounded retention", ttl)
	}
	// Exact owned hash only; never flush shared Redis.
	values := make(map[string]any)
	for i := 0; i < loginCounterCapacity-len(fields); i++ {
		values[fmt.Sprintf("capacity-%d", i)] = 1
	}
	if err := rc.HSet(context.Background(), key, values).Err(); err != nil {
		t.Fatal(err)
	}
	if status, _ := workers[0].allow(context.Background(), "login", "new", "new"); status != 503 {
		t.Fatal("Redis capacity bypass")
	}
	if err := rc.Del(context.Background(), key).Err(); err != nil {
		t.Fatal(err)
	}
	_ = rc.Close()
	if status, _ := workers[0].allow(context.Background(), "login", "new", "new"); status != 503 {
		t.Fatal("Redis failure bypass")
	}
}

func TestPublicAuthCookieAndMasterDisable(t *testing.T) {
	st, prefix := recoveryStore(t)
	cfg := recoveryConfig(t, prefix)
	cfg.Admin.DisableMasterKey = true
	cfg.Admin.SecureCookies = true
	password := "fixture-password-for-cookie-test"
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateUser(context.Background(), "admin@example.test", "Admin", hash, nil, store.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	srv := newRecoveryServer(t, cfg, st)
	for _, path := range []string{"/admin/info", "/auth/me", "/me/sessions", "/health/drain"} {
		method := "GET"
		if path == "/health/drain" {
			method = "POST"
		}
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer fixture-master")
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatalf("disabled master accepted at %s: %d", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("POST", "/auth/login", strings.NewReader(fmt.Sprintf(`{"email":"admin@example.test","password":%q}`, password))))
	if w.Code != 200 {
		t.Fatalf("login status %d", w.Code)
	}
	if strings.Contains(w.Body.String(), `"token"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("session token exposed/cached")
	}
	cookies := w.Result().Cookies()
	var session *http.Cookie
	var clearedLegacy bool
	for _, c := range cookies {
		if c.Name == auth.SessionCookieName {
			session = c
		}
		if c.Name == auth.LegacySessionCookieName && c.MaxAge == -1 {
			clearedLegacy = true
		}
	}
	if session == nil || !clearedLegacy || !session.HttpOnly || !session.Secure || session.SameSite != http.SameSiteStrictMode {
		t.Fatal("cookie protections")
	}
	if strings.Contains(w.Body.String(), session.Value) {
		t.Fatal("cookie copied into JSON")
	}
	req := httptest.NewRequest("GET", "/auth/me", nil)
	req.AddCookie(session)
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("account access disabled with master key")
	}
	w = httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", "/auth/login-modes", nil))
	if !strings.Contains(w.Body.String(), `"master_key_enabled":false`) {
		t.Fatal("UI disagrees with policy")
	}
}

func TestRenameLegacySessionRBACAndLogout(t *testing.T) {
	st, prefix := recoveryStore(t)
	cfg := recoveryConfig(t, prefix)
	srv := newRecoveryServer(t, cfg, st)
	user, err := st.CreateUser(context.Background(), "rename@example.test", "Member", nil, nil, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"rename-legacy-session", "rename-new-session"} {
		if err := st.CreateSession(context.Background(), auth.HashKey(token), user.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path string, both bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", "Bearer fixture-master")
		r.AddCookie(&http.Cookie{Name: auth.LegacySessionCookieName, Value: "rename-legacy-session"})
		if both {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: "rename-new-session"})
		}
		w := httptest.NewRecorder()
		srv.http.Handler.ServeHTTP(w, r)
		return w
	}
	if w := request("GET", "/auth/me", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"role":"member"`) {
		t.Fatal("legacy session lost", w.Code)
	}
	if w := request("GET", "/admin/info", false); w.Code != 403 {
		t.Fatal("legacy member escalated by master bearer", w.Code)
	}
	w := request("POST", "/auth/logout", true)
	if w.Code != 204 {
		t.Fatal("logout failed", w.Code)
	}
	cleared := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.MaxAge == -1 {
			cleared[c.Name] = true
		}
	}
	if !cleared[auth.SessionCookieName] || !cleared[auth.LegacySessionCookieName] {
		t.Fatal("both cookies must be cleared")
	}
	for _, token := range []string{"rename-legacy-session", "rename-new-session"} {
		if _, err := st.LookupSession(context.Background(), auth.HashKey(token)); err == nil {
			t.Fatal("logout left an active session")
		}
	}
	if w := request("GET", "/auth/me", false); w.Code != 401 {
		t.Fatal("revoked legacy cookie fell through to master bearer", w.Code)
	}
}
