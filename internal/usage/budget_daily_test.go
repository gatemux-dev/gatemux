package usage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// Compare every scope/day against the previous admission formula, including
// zero rows, foreign-team request ID reuse, UTC boundaries and legacy evidence.
func assertDailyTotals(t *testing.T, s *store.Store) {
	t.Helper()
	var differences int
	err := s.Pool.QueryRow(context.Background(), `WITH charges AS (
	 SELECT team_id,user_id,service_account_id,key_id,customer_id,created_at AS ts,
	 CASE WHEN status='reserved' THEN estimated_cost_cents ELSE settled_cost_cents END AS cost
	 FROM budget_reservations WHERE status IN ('reserved','settled')
	 UNION ALL SELECT u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,u.cost_cents
	 FROM usage_log u WHERE NOT EXISTS (SELECT 1 FROM budget_reservations b WHERE b.request_id=u.request_id AND b.team_id=u.team_id)
	), expected AS (
	 SELECT v.scope,v.subject_id,(ts AT TIME ZONE 'UTC')::date AS day,SUM(cost) AS cost_cents
	 FROM charges CROSS JOIN LATERAL (VALUES ('team',team_id),('user',user_id),
	 ('service_account',service_account_id),('key',key_id),('customer',customer_id)) v(scope,subject_id)
	 WHERE v.subject_id IS NOT NULL GROUP BY v.scope,v.subject_id,(ts AT TIME ZONE 'UTC')::date
	) SELECT count(*) FROM expected e FULL JOIN budget_daily_totals a USING(scope,subject_id,day)
	 WHERE COALESCE(e.cost_cents,0)<>COALESCE(a.cost_cents,0)`).Scan(&differences)
	if err != nil || differences != 0 {
		t.Fatalf("daily totals disagree with source ledger: %d %v", differences, err)
	}
}

func rollbackDailyMigration(t *testing.T, s *store.Store) {
	t.Helper()
	down, err := os.ReadFile("../store/migrations/0039_budget_daily_totals.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), `DELETE FROM schema_migrations WHERE version='0039_budget_daily_totals'`); err != nil {
		t.Fatal(err)
	}
}

func TestBudgetDailyMigrationAndHistoryIndependentAdmission(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	rollbackDailyMigration(t, s) // Only this test's empty private schema.
	// A month's history is larger than the failed soak, installed without the
	// new triggers to exercise the real upgrade/backfill path, not 50k HTTP calls.
	_, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents,settled_cost_cents,status,created_at)
	 SELECT 'history-'||n,$1,'test-model',3,2,'settled',date_trunc('month',NOW())+((n%28)::text||' days')::interval
	 FROM generate_series(1,50000) n`, team)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Pool.Exec(ctx, `INSERT INTO usage_log(team_id,alias,model_requested,request_id,status_code,cost_cents,ts)
	 SELECT team_id,alias,alias,request_id,200,settled_cost_cents,created_at FROM budget_reservations
	 UNION ALL SELECT $1,'test-model','test-model','unreserved',200,7,date_trunc('month',NOW())`, team)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	assertDailyTotals(t, s)
	var rows, usageRows int
	var cents int64
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),sum(cost_cents),(SELECT count(*) FROM usage_log) FROM budget_daily_totals`).Scan(&rows, &cents, &usageRows); err != nil || rows != 28 || cents != 100007 || usageRows != 50001 {
		t.Fatalf("backfill rewrote/duplicated history: rows=%d cost=%d usage=%d err=%v", rows, cents, usageRows, err)
	}
	if _, err := s.UpsertPricing(ctx, "openai", "test-model", 1000000, 1000000); err != nil {
		t.Fatal(err)
	}
	limit := int64(100007)
	service := budget.New(s)
	req := budget.AdmissionRequest{RequestID: "after-backfill", Team: &store.Team{ID: team, Period: "month", UsdLimitCents: &limit}, Alias: "test-model", PromptTokens: 1, Targets: []budget.Target{{ProviderType: "openai", UpstreamModel: "test-model"}}}
	_, err = service.Admit(ctx, req)
	var exceeded *budget.ExceededError
	if !errors.As(err, &exceeded) || exceeded.UsedCents != 100007 {
		t.Fatalf("upgraded budget ignored history: %v", err)
	}
	limit++
	if _, err := service.Admit(ctx, req); err != nil {
		t.Fatal(err)
	}
	assertDailyTotals(t, s)
}

func TestBudgetDailyMutationRollbackAndUTCBoundaries(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	usr, err := s.CreateUser(ctx, "daily@example.test", "Daily", []byte("fixture"), &team, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	sa, err := s.CreateServiceAccount(ctx, store.CreateServiceAccountParams{TeamID: team, Name: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	vk, err := s.CreateVirtualKey(ctx, store.CreateVirtualKeyParams{TeamID: team, KeyHash: []byte("daily-fixture"), Prefix: "daily", Name: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	customer, err := s.GetOrCreateCustomer(ctx, team, "daily")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTeam(ctx, "other", "Other", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO usage_log(team_id,user_id,service_account_id,key_id,customer_id,alias,model_requested,request_id,cost_cents,ts)
		 VALUES($1,$2,$3,$4,$5,'daily','daily','same-id',7,'2026-08-31 23:59:59Z'),($1,$2,$3,$4,$5,'daily','daily','same-id',11,'2026-09-01 00:00:00Z')`, []any{team, usr.ID, sa.ID, vk.ID, customer.ID}},
		{`INSERT INTO usage_log(team_id,alias,model_requested,request_id,cost_cents,ts) VALUES($1,'daily','daily','same-id',23,'2026-09-01Z')`, []any{other.ID}},
		{`INSERT INTO budget_reservations(team_id,user_id,service_account_id,key_id,customer_id,alias,request_id,estimated_cost_cents,created_at)
		 VALUES($1,$2,$3,$4,$5,'daily','same-id',29,'2026-09-01 00:00:01Z')`, []any{team, usr.ID, sa.ID, vk.ID, customer.ID}},
		{`UPDATE budget_reservations SET status='settled',settled_cost_cents=13 WHERE request_id='same-id'`, nil},
		{`UPDATE usage_log SET cost_cents=17 WHERE team_id=$1`, []any{team}}, // Covered usage must not double-charge.
		{`UPDATE budget_reservations SET created_at='2026-10-01Z' WHERE request_id='same-id'`, nil},
		{`UPDATE budget_reservations SET request_id='renamed' WHERE request_id='same-id'`, nil},
		{`UPDATE budget_reservations SET request_id='same-id' WHERE request_id='renamed'`, nil},
		{`UPDATE usage_log SET key_id=NULL WHERE key_id=$1`, []any{vk.ID}}, // Historical usage FK intentionally restricts key deletion.
		{`DELETE FROM virtual_keys WHERE id=$1`, []any{vk.ID}},
		{`DELETE FROM service_accounts WHERE id=$1`, []any{sa.ID}},
		{`DELETE FROM users WHERE id=$1`, []any{usr.ID}},
		{`DELETE FROM customers WHERE id=$1`, []any{customer.ID}},
		{`DELETE FROM budget_reservations WHERE request_id='same-id'`, nil}, // Restore unreserved history.
		{`DELETE FROM usage_log WHERE team_id=$1`, []any{team}},
		{`DELETE FROM usage_log WHERE team_id=$1`, []any{other.ID}},
		{`DELETE FROM teams WHERE id=$1`, []any{other.ID}},
	}
	for i, step := range steps {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			tx, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, `SET LOCAL TIME ZONE 'Pacific/Kiritimati'`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, step.sql, step.args...); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			assertDailyTotals(t, s)
		})
		if t.Failed() {
			return
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents) VALUES('rollback',$1,'daily',99)`, team); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertDailyTotals(t, s)
	for _, table := range []string{"usage_log", "budget_reservations", "budget_daily_totals"} {
		if _, err := s.Pool.Exec(ctx, "TRUNCATE "+table+" CASCADE"); err == nil || !strings.Contains(err.Error(), "cannot be truncated") {
			t.Fatalf("unsafe truncate of %s not rejected: %v", table, err)
		}
	}
}

func TestBudgetDailyConcurrentReplicasRespectBudget(t *testing.T) {
	s, team, dsn := accountingStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	other, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := s.UpsertPricing(ctx, "openai", "daily", 1000000, 1000000); err != nil {
		t.Fatal(err)
	}
	limit := int64(10)
	services := []*budget.Service{budget.New(s), budget.New(other)}
	var accepted, denied atomic.Int64
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := budget.AdmissionRequest{RequestID: fmt.Sprintf("replica-%d", i), Team: &store.Team{ID: team, Period: "month", UsdLimitCents: &limit}, Alias: "daily", PromptTokens: 1, Targets: []budget.Target{{ProviderType: "openai", UpstreamModel: "daily"}}}
			_, err := services[i%2].Admit(ctx, req)
			var exceeded *budget.ExceededError
			if errors.As(err, &exceeded) {
				denied.Add(1)
				return
			}
			if err != nil {
				t.Error(err)
				return
			}
			accepted.Add(1)
		}()
	}
	wg.Wait()
	if accepted.Load() != 10 || denied.Load() != 22 {
		t.Fatalf("budget overspend: accepted=%d denied=%d", accepted.Load(), denied.Load())
	}
	assertDailyTotals(t, s)
}

func TestBudgetDailyConcurrentLegacyCorrelation(t *testing.T) {
	for _, reservationFirst := range []bool{false, true} {
		t.Run(fmt.Sprint(reservationFirst), func(t *testing.T) {
			s, team, _ := accountingStore(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			usageSQL := `INSERT INTO usage_log(team_id,alias,model_requested,request_id,cost_cents) VALUES($1,'daily','daily','race',7)`
			reservationSQL := `INSERT INTO budget_reservations(team_id,alias,request_id,estimated_cost_cents) VALUES($1,'daily','race',19)`
			first, second := usageSQL, reservationSQL
			if reservationFirst {
				first, second = second, first
			}
			tx, err := s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx, first, team); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, err := s.Pool.Exec(ctx, second, team); done <- err }()
			select {
			case err := <-done:
				t.Fatalf("correlation bypassed uncommitted peer: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			assertDailyTotals(t, s)
			var cents int64
			if err := s.Pool.QueryRow(ctx, `SELECT sum(cost_cents) FROM budget_daily_totals`).Scan(&cents); err != nil || cents != 19 {
				t.Fatalf("concurrent usage double counted: %d %v", cents, err)
			}
		})
	}
}

func TestBudgetDailyOverflowRollsBackAllScopes(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	customer, err := s.GetOrCreateCustomer(ctx, team, "overflow")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(team_id,alias,request_id,estimated_cost_cents)
	 VALUES($1,'daily','full',9223372036854775807)`, team); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(team_id,customer_id,alias,request_id,estimated_cost_cents)
	 VALUES($1,$2,'daily','overflow',1)`, team, customer.ID); err == nil {
		t.Fatal("overflow was accepted")
	}
	assertDailyTotals(t, s)
	var reservations, customerRows int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM budget_reservations),
	 (SELECT count(*) FROM budget_daily_totals WHERE scope='customer')`).Scan(&reservations, &customerRows); err != nil || reservations != 1 || customerRows != 0 {
		t.Fatalf("partial scope charge survived rollback: %d/%d %v", reservations, customerRows, err)
	}
}
