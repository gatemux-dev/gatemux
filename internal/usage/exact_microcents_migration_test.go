package usage

import (
	"context"
	"os"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func rollbackMigration0041(t *testing.T, s *store.Store) {
	t.Helper()
	down, err := os.ReadFile("../store/migrations/0041_exact_microcents.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), `DELETE FROM schema_migrations WHERE version='0041_exact_microcents'`); err != nil {
		t.Fatal(err)
	}
}

func TestMigration0041(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	rollbackMigration0041(t, s) // Only this test's private schema.
	// Whole-cent history written by the pre-0041 triggers.
	for _, q := range []string{
		`INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents,status) VALUES('legacy-reserved',$1,'m',5,'reserved')`,
		`INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents,settled_cost_cents,status) VALUES('legacy-settled',$1,'m',9,4,'settled')`,
		`INSERT INTO usage_log(team_id,alias,model_requested,request_id,status_code,cost_cents) VALUES($1,'m','m','legacy-settled',200,4),($1,'m','m','legacy-unreserved',200,7)`,
	} {
		if _, err := s.Pool.Exec(ctx, q, team); err != nil {
			t.Fatal(err)
		}
	}
	var before int64
	if err := s.Pool.QueryRow(ctx, `SELECT sum(cost_cents) FROM budget_daily_totals WHERE scope='team'`).Scan(&before); err != nil || before != 16 {
		t.Fatalf("seeded totals: %d %v", before, err)
	}
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}

	var exactUsage, exactReservations int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_log WHERE cost_microcents IS NOT NULL),
	 (SELECT count(*) FROM budget_reservations WHERE estimated_cost_microcents IS NOT NULL OR settled_cost_microcents IS NOT NULL)`).Scan(&exactUsage, &exactReservations); err != nil || exactUsage != 0 || exactReservations != 0 {
		t.Fatalf("history gained exact amounts: usage=%d reservations=%d err=%v", exactUsage, exactReservations, err)
	}
	var legacyCents int64
	if err := s.Pool.QueryRow(ctx, `SELECT sum(cost_cents) FROM usage_log`).Scan(&legacyCents); err != nil || legacyCents != 11 {
		t.Fatalf("history cents changed: %d %v", legacyCents, err)
	}
	var after int64
	if err := s.Pool.QueryRow(ctx, `SELECT sum(cost_microcents) FROM budget_daily_totals WHERE scope='team'`).Scan(&after); err != nil || after != before*1000000 {
		t.Fatalf("daily totals not backfilled in microcents: %d %v", after, err)
	}
	var centsColumn bool
	if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns
	 WHERE table_schema=current_schema() AND table_name='budget_daily_totals' AND column_name='cost_cents')`).Scan(&centsColumn); err != nil || centsColumn {
		t.Fatalf("budget_daily_totals.cost_cents still exists: %v %v", centsColumn, err)
	}

	insert := `INSERT INTO usage_log(team_id,alias,model_requested,request_id,status_code,cost_cents,cost_microcents) VALUES($1,'m','m',$2,200,$3,$4)`
	if _, err := s.Pool.Exec(ctx, insert, team, "inconsistent", 0, 3000); err == nil {
		t.Fatal("accepted cents below the exact amount")
	}
	if _, err := s.Pool.Exec(ctx, insert, team, "negative", 0, -1); err == nil {
		t.Fatal("accepted a negative exact amount")
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents,estimated_cost_microcents) VALUES('bad-estimate',$1,'m',0,3000)`, team); err == nil {
		t.Fatal("accepted inconsistent reservation estimate")
	}
	if _, err := s.Pool.Exec(ctx, insert, team, "consistent", 1, 3000); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT sum(cost_microcents) FROM budget_daily_totals WHERE scope='team'`).Scan(&after); err != nil || after != before*1000000+3000 {
		t.Fatalf("exact usage not charged in microcents: %d %v", after, err)
	}
	assertDailyTotals(t, s)
}
