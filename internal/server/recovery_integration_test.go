package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

// Recovery/fault tests own private schemas and Redis prefixes. Never pause or
// flush a shared dependency; a blackhole socket or exhausted private pool is
// sufficient to inject failures without changing another test or preview.
func recoveryStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL must explicitly name a disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("recovery_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		root.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := root.Pool.Exec(ctx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		root.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	query.Set("pool_max_conns", "8")
	u.RawQuery = query.Encode()
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := store.Migrate(ctx, st.Pool); err != nil {
		t.Fatal(err)
	}
	return st, schema
}

func recoveryConfig(t *testing.T, prefix string) *config.Config {
	t.Helper()
	t.Setenv("GATEMUX_RECOVERY_MASTER", "fixture-master")
	t.Setenv("GATEMUX_RECOVERY_PROVIDER", "fixture-provider")
	return &config.Config{
		Admin: config.AdminConfig{MasterKeyEnv: "GATEMUX_RECOVERY_MASTER"},
		Server: config.ServerConfig{Addr: "127.0.0.1:0", MetricsAddr: "127.0.0.1:0", V1Deadline: 5 * time.Second,
			Admission: config.AdmissionConfig{MaxInFlight: 2, MaxQueued: 2, QueueTimeout: 100 * time.Millisecond},
			Shutdown:  config.ShutdownConfig{GracePeriod: time.Second, CleanupTimeout: 3 * time.Second}},
		Redis:     config.RedisConfig{Addr: os.Getenv("GATEMUX_TEST_REDIS_ADDR"), KeyPrefix: prefix},
		Callbacks: []config.CallbackConfig{{Name: "archive", Type: "s3", LocalDir: t.TempDir()}},
	}
}

func newRecoveryServer(t *testing.T, cfg *config.Config, st *store.Store) *Server {
	t.Helper()
	s, err := New(cfg, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s
}

func assertWorkersJoined(t *testing.T, s *Server) {
	t.Helper()
	for i, done := range s.workerDone {
		select {
		case <-done:
		default:
			t.Fatalf("worker %d not joined", i)
		}
	}
	if err := s.usage.CloseContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.breakerClient != nil && !errors.Is(s.breakerClient.Ping(context.Background()).Err(), redis.ErrClosed) {
		t.Fatal("breaker client not closed")
	}
	if s.cfg.Redis.Addr != "" && !errors.Is(s.limit.Ready(context.Background()), redis.ErrClosed) {
		t.Fatal("rate limiter client not closed")
	}
	if s.promptCache != nil {
		if _, err := s.promptCache.Get(context.Background(), "closed-check"); !errors.Is(err, redis.ErrClosed) {
			t.Fatalf("cache client not closed: %v", err)
		}
	}
}

func TestServerWorkersAndMetricsHaveInstanceOwnership(t *testing.T) {
	st, prefix := recoveryStore(t)
	cfg := recoveryConfig(t, prefix)
	for range 3 {
		a := newRecoveryServer(t, cfg, st)
		b := newRecoveryServer(t, cfg, st)
		a.tel.RecordAccounting("completion", "failed", 0, 0)
		for i, s := range []*Server{a, b} {
			w := httptest.NewRecorder()
			s.tel.MetricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
			hasSample := strings.Contains(w.Body.String(), `gatemux_accounting_operations_total{operation="completion",outcome="failed"} 1`)
			if hasSample != (i == 0) {
				t.Fatal("metrics leaked between server instances")
			}
		}
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := a.Shutdown(context.Background()); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		assertWorkersJoined(t, a)
		if err := b.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertWorkersJoined(t, b)
		if err := st.Pool.Ping(context.Background()); err != nil {
			t.Fatal("server closed caller-owned DB pool")
		}
	}
}

func TestDrainEndpointRequiresAdminAndIsIrreversible(t *testing.T) {
	st, prefix := recoveryStore(t)
	s := newRecoveryServer(t, recoveryConfig(t, prefix), st)
	for _, key := range []string{"", "wrong"} {
		r := httptest.NewRequest("POST", "/health/drain", nil)
		if key != "" {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		if w.Code != 401 || s.draining.Load() {
			t.Fatalf("unauthorized drain: %d", w.Code)
		}
	}
	team, err := st.CreateTeam(context.Background(), "manager-team", "Manager", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(context.Background(), "manager@example.test", "Manager", []byte("unused"), &team.ID, store.RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	// Use the production session issuer to prove an authenticated manager is
	// forbidden, rather than only testing anonymous callers.
	raw, hash, _, err := auth.GenerateKey("session")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSession(context.Background(), hash, user.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/health/drain", nil)
	r.AddCookie(&http.Cookie{Name: "gatemux_session", Value: raw})
	w := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(w, r)
	if w.Code != 403 || s.draining.Load() {
		t.Fatalf("manager can drain: %d", w.Code)
	}
	for range 2 {
		r := httptest.NewRequest("POST", "/health/drain", nil)
		r.Header.Set("Authorization", "Bearer fixture-master")
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, r)
		if w.Code != 200 || !s.draining.Load() {
			t.Fatalf("admin drain failed: %d", w.Code)
		}
	}
	for path, status := range map[string]int{"/readyz": 503, "/healthz": 200, "/v1/models": 503, "/passthrough/test/foo": 503} {
		w := httptest.NewRecorder()
		s.http.Handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != status {
			t.Fatalf("draining %s = %d, want %d", path, w.Code, status)
		}
	}
	w = httptest.NewRecorder()
	s.tel.MetricsHandler().ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(w.Body.String(), "gatemux_draining 1") {
		t.Fatal("missing observable drain state")
	}
}

func TestRecoveryFullStackDrainSettlesAndReleases(t *testing.T) {
	if os.Getenv("GATEMUX_TEST_REDIS_ADDR") == "" {
		t.Skip("GATEMUX_TEST_REDIS_ADDR required")
	}
	for _, forced := range []bool{false, true} {
		t.Run(fmt.Sprint("forced=", forced), func(t *testing.T) {
			st, prefix := recoveryStore(t)
			cfg := recoveryConfig(t, prefix)
			if forced {
				cfg.Server.Shutdown.GracePeriod = 40 * time.Millisecond
			}
			finish, upstreamDone := make(chan struct{}), make(chan struct{})
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"drain\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-finish:
				}
				_, _ = io.WriteString(w, "data: {\"id\":\"drain\",\"model\":\"mock-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n")
			}))
			t.Cleanup(mock.Close)
			cfg.Deployments = []config.DeploymentConfig{{Name: "mock", Type: "openai", UpstreamModel: "mock-model", APIKeyEnv: "GATEMUX_RECOVERY_PROVIDER", BaseURL: mock.URL, MaxParallelRequests: 2}}
			cfg.Aliases = []config.AliasConfig{{Alias: "pilot", Deployments: []string{"mock"}}}
			cap := 2
			budget := int64(1000000)
			team, err := st.CreateTeam(context.Background(), "pilot", "Pilot", &budget, "month", nil, nil, &cap)
			if err != nil {
				t.Fatal(err)
			}
			raw, hash, keyPrefix, err := auth.GenerateKey(team.Slug)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateVirtualKey(context.Background(), store.CreateVirtualKeyParams{TeamID: team.ID, KeyHash: hash, Prefix: keyPrefix, Name: "fixture", MaxParallelRequests: &cap}); err != nil {
				t.Fatal(err)
			}
			if _, err := st.UpsertPricing(context.Background(), "openai", "mock-model", 1000000, 1000000); err != nil {
				t.Fatal(err)
			}
			for scope, subject := range map[string]string{"model": "pilot", "provider": "openai"} {
				if _, err := st.SetRoutingConcurrencyLimit(context.Background(), scope, subject, &cap); err != nil {
					t.Fatal(err)
				}
			}
			policies := json.RawMessage(`[{"name":"pilot","type":"banned_terms","mode":"block","phase":"both","terms":["blocked-term"]}]`)
			if err := st.ReplaceGuardrailScope(context.Background(), "team", team.Slug, json.RawMessage(`[]`), policies, "fixture"); err != nil {
				t.Fatal(err)
			}
			s := newRecoveryServer(t, cfg, st)
			base := serveLifecycle(t, s, s.http.Handler)
			r, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(`{"model":"pilot","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":10}`))
			r.Header.Set("Authorization", "Bearer "+raw)
			r.Header.Set("X-Request-ID", "full-stack-drain")
			resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != 200 {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("stream rejected: %d %s", resp.StatusCode, body)
			}
			rc := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr})
			defer rc.Close()
			keys, err := rc.Keys(context.Background(), prefix+":concurrency:*").Result()
			if err != nil {
				t.Fatal(err)
			}
			if len(keys) < 4 {
				t.Fatalf("missing configured concurrency scope evidence: %d", len(keys))
			}
			for _, key := range keys {
				if n, err := rc.ZCard(context.Background(), key).Result(); err != nil || n != 1 {
					t.Fatalf("missing active lease: %d %v", n, err)
				}
			}
			s.Drain()
			if !forced {
				close(finish)
			}
			err = s.Shutdown(context.Background())
			if forced && !errors.Is(err, context.DeadlineExceeded) || !forced && err != nil {
				t.Fatalf("drain error: %v", err)
			}
			if !forced {
				data, err := io.ReadAll(resp.Body)
				if err != nil || !strings.Contains(string(data), "[DONE]") || strings.Contains(string(data), `"error"`) {
					t.Fatalf("guarded stream did not finish cleanly: %v", err)
				}
			}
			awaitLifecycle(t, upstreamDone)
			assertWorkersJoined(t, s)
			var intents, usages, reservations, pending int
			var charged, settled int64
			if err := st.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM inference_journal), (SELECT count(*) FROM usage_log WHERE accounting_id IS NOT NULL), (SELECT count(*) FROM budget_reservations), (SELECT count(*) FROM inference_journal WHERE state='pending') + (SELECT count(*) FROM budget_reservations WHERE status='reserved'), (SELECT COALESCE(sum(cost_cents),0) FROM usage_log), (SELECT COALESCE(sum(settled_cost_cents),0) FROM budget_reservations)`).Scan(&intents, &usages, &reservations, &pending, &charged, &settled); err != nil {
				t.Fatal(err)
			}
			if intents != 1 || usages != 1 || reservations != 1 || pending != 0 || charged <= 0 || charged != settled {
				t.Fatalf("incomplete accounting: %d/%d/%d pending=%d charged=%d settled=%d", intents, usages, reservations, pending, charged, settled)
			}
			if stats := s.admission.Stats(); stats.InFlight != 0 || stats.Queued != 0 {
				t.Fatalf("global permits leaked: %+v", stats)
			}
			for _, key := range keys {
				if n, err := rc.ZCard(context.Background(), key).Result(); err != nil || n != 0 {
					t.Fatalf("lease leaked: %d %v", n, err)
				}
			}
			for _, health := range s.policyRegistry.Health() {
				if health.InFlight != 0 {
					t.Fatalf("deployment permit leaked: %+v", health)
				}
			}
		})
	}
}

func TestReadinessPostgresPoolFailureIsBoundedAndRecovers(t *testing.T) {
	st, prefix := recoveryStore(t)
	s := newRecoveryServer(t, recoveryConfig(t, prefix), st)
	var held []*pgxpool.Conn
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		for _, c := range held {
			c.Release()
		}
	}()
	for range st.Pool.Config().MaxConns {
		c, err := st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	start := time.Now()
	w := httptest.NewRecorder()
	s.handleReady(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("unbounded DB readiness failure: %d %s", w.Code, time.Since(start))
	}
	for _, c := range held {
		c.Release()
	}
	held = nil
	w = httptest.NewRecorder()
	s.handleReady(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 200 {
		t.Fatalf("readiness did not recover: %d", w.Code)
	}
}

func blackholeSocket(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = l.Close()
		awaitLifecycle(t, done)
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return l.Addr().String()
}

func TestReadinessRedisBlackholeIsBounded(t *testing.T) {
	st, prefix := recoveryStore(t)
	cfg := recoveryConfig(t, prefix)
	cfg.Redis.Addr = blackholeSocket(t)
	s := newRecoveryServer(t, cfg, st)
	start := time.Now()
	w := httptest.NewRecorder()
	s.handleReady(w, httptest.NewRequest("GET", "/readyz", nil))
	if w.Code != 503 || time.Since(start) > 2500*time.Millisecond {
		t.Fatalf("unbounded Redis readiness failure: %d %s", w.Code, time.Since(start))
	}
	start = time.Now()
	if _, err := s.promptCache.Get(context.Background(), "missing"); err == nil || time.Since(start) > time.Second {
		t.Fatal("cache Redis IO not bounded")
	}
	start = time.Now()
	if err := s.breakerClient.Ping(context.Background()).Err(); err == nil || time.Since(start) > time.Second {
		t.Fatal("breaker Redis IO not bounded")
	}
}

func TestShutdownCancelsWorkersWaitingForPostgres(t *testing.T) {
	st, prefix := recoveryStore(t)
	s := newRecoveryServer(t, recoveryConfig(t, prefix), st)
	var held []*pgxpool.Conn
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	defer func() {
		for _, c := range held {
			c.Release()
		}
	}()
	for range st.Pool.Config().MaxConns {
		c, err := st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	start := time.Now()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("shutdown waited on unavailable DB instead of canceling workers")
	}
	assertWorkersJoined(t, s)
}
