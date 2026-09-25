package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5"
)

func alertStore(t *testing.T) (*store.Store, int64) {
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
	schema := fmt.Sprintf("alerts_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		root.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = root.Pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		root.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "alerts", "Alerts", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, team.ID
}

type sink struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (k *sink) server(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		k.mu.Lock()
		k.payloads = append(k.payloads, p)
		k.mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (k *sink) count() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.payloads)
}

func traffic(t *testing.T, s *store.Store, team int64, n, failures, latencyMs int) {
	t.Helper()
	for i := 0; i < n; i++ {
		status := 200
		if i < failures {
			status = 502
		}
		if _, err := s.Pool.Exec(context.Background(), `
			INSERT INTO usage_log (team_id, alias, request_id, model_requested, latency_ms, status_code)
			VALUES ($1, 'chat', $2, 'chat', $3, $4)`, team, fmt.Sprintf("r-%d-%d", time.Now().UnixNano(), i), latencyMs, status); err != nil {
			t.Fatal(err)
		}
	}
}

func rule(t *testing.T, s *store.Store, target, trigger string, team *int64, opts map[string]any) *store.AlertRule {
	t.Helper()
	scope := "global"
	if team != nil {
		scope = "team"
	}
	r, err := s.CreateAlertRule(context.Background(), store.CreateAlertRuleParams{
		Name: trigger, ScopeType: scope, ScopeID: team, TriggerType: trigger,
		ThresholdOptions: opts, ChannelType: "webhook", ChannelTarget: target,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTrafficTriggers(t *testing.T) {
	s, team := alertStore(t)
	ctx := context.Background()
	e := New(s, nil)
	var hits sink
	target := hits.server(t)

	// Too little traffic: min_requests keeps a quiet gateway from paging.
	traffic(t, s, team, 5, 5, 100)
	quiet := rule(t, s, target, "error_rate", nil, map[string]any{"threshold": 10.0, "min_requests": 20.0})
	e.evaluateRule(ctx, quiet)
	if hits.count() != 0 {
		t.Fatalf("fired below min_requests: %d", hits.count())
	}

	// 30 requests, 10 server errors: 33% crosses a 20% threshold.
	traffic(t, s, team, 25, 5, 100)
	errRule := rule(t, s, target, "error_rate", &team, map[string]any{"threshold": 20.0, "min_requests": 20.0})
	e.evaluateRule(ctx, errRule)
	if hits.count() != 1 {
		t.Fatalf("error_rate fired %d times, want 1", hits.count())
	}
	if got := hits.payloads[0]["metric"]; got != "error_rate" {
		t.Fatalf("payload metric = %v", got)
	}
	// The cooldown holds a second evaluation back.
	e.evaluateRule(ctx, errRule)
	if hits.count() != 1 {
		t.Fatalf("fired inside cooldown")
	}

	// p95 of 100ms stays under a 1s threshold.
	slow := rule(t, s, target, "latency_p95", nil, map[string]any{"threshold": 1000.0, "min_requests": 20.0})
	e.evaluateRule(ctx, slow)
	if hits.count() != 1 {
		t.Fatalf("latency_p95 fired below threshold")
	}
	traffic(t, s, team, 30, 0, 4000)
	e.evaluateRule(ctx, slow)
	if hits.count() != 2 {
		t.Fatalf("latency_p95 did not fire above threshold")
	}
}

func TestProviderUnavailable(t *testing.T) {
	s, _ := alertStore(t)
	ctx := context.Background()
	e := New(s, nil)
	var hits sink
	target := hits.server(t)

	for _, name := range []string{"down", "flaky"} {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO deployments (name, provider_type, upstream_model, credential_ref) VALUES ($1, 'openai', 'gpt', 'env:X')`, name); err != nil {
			t.Fatal(err)
		}
	}
	sample := func(name string, ready bool, circuit string) {
		if _, err := s.Pool.Exec(ctx, `INSERT INTO provider_health_samples (deployment_name, ready, circuit, consecutive_failures) VALUES ($1, $2, $3, 0)`, name, ready, circuit); err != nil {
			t.Fatal(err)
		}
	}
	sample("down", false, "open")
	sample("down", true, "open")
	sample("flaky", false, "closed")
	sample("flaky", true, "closed")

	r := rule(t, s, target, "provider_unavailable", nil, nil)
	e.evaluateRule(ctx, r)
	if hits.count() != 1 {
		t.Fatalf("provider_unavailable fired %d times, want 1", hits.count())
	}
	names, _ := hits.payloads[0]["deployments"].([]any)
	if len(names) != 1 || names[0] != "down" {
		t.Fatalf("unavailable = %v, want [down]", names)
	}
}
