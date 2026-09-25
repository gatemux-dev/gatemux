package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5"
)

// Every test owns a private disposable schema; neither recovery nor failure
// injection may affect another test, let alone the running preview database.
func accountingStore(t *testing.T) (*store.Store, int64, string) {
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
	schema := fmt.Sprintf("accounting_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = root.Pool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		root.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := root.Pool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
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
	u.RawQuery = query.Encode()
	privateDSN := u.String()
	s, err := store.Open(ctx, privateDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "accounting", "Accounting", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s, team.ID, privateDSN
}

func intent(t *testing.T, s *store.Store, team int64, id string, started, reserve bool) store.UsageEntry {
	t.Helper()
	e := store.UsageEntry{TeamID: team, RequestID: id, AccountingID: id, Alias: "test-model"}
	if err := s.BeginAccounting(context.Background(), e, time.Minute); err != nil {
		t.Fatal(err)
	}
	if reserve {
		if _, err := s.Pool.Exec(context.Background(), `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents) VALUES($1,$2,'test-model',123)`, id, team); err != nil {
			t.Fatal(err)
		}
	}
	if started {
		if err := s.MarkAccountingStarted(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func expireIntent(t *testing.T, s *store.Store, id string) {
	t.Helper()
	if _, err := s.Pool.Exec(context.Background(), `UPDATE inference_journal SET recover_after=NOW()-interval '1 second' WHERE request_id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func TestAccountingAtomicIdempotentCompletion(t *testing.T) {
	s, team, _ := accountingStore(t)
	t.Cleanup(func() { assertDailyTotals(t, s) })
	e := intent(t, s, team, "concurrent", true, true)
	e.Accounting, e.StatusCode, e.CostCents = "priced", 200, 21
	var wg sync.WaitGroup
	receipts := make(chan store.UsageReceipt, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.FinalizeUsage(context.Background(), e)
			if err != nil {
				t.Error(err)
				return
			}
			receipts <- r
		}()
	}
	wg.Wait()
	close(receipts)
	var first store.UsageReceipt
	for r := range receipts {
		if first.ID == 0 {
			first = r
		}
		if r != first || r.CostCents != 21 || r.Accounting != "priced" {
			t.Fatalf("non-idempotent receipt: %+v / %+v", first, r)
		}
	}
	e.CostCents = 999
	r, err := s.FinalizeUsage(context.Background(), e)
	if err != nil || r != first {
		t.Fatalf("duplicate rewrote original: %+v, %v", r, err)
	}
	var rows, settled int64
	if err := s.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM usage_log`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("usage rows=%d: %v", rows, err)
	}
	if err := s.Pool.QueryRow(context.Background(), `SELECT settled_cost_cents FROM budget_reservations WHERE request_id=$1 AND status='settled'`, e.RequestID).Scan(&settled); err != nil || settled != 21 {
		t.Fatalf("settlement=%d: %v", settled, err)
	}
	if err := s.BeginAccounting(context.Background(), e, time.Minute); !errors.Is(err, store.ErrAccountingConflict) {
		t.Fatalf("reused ID accepted: %v", err)
	}
}

func TestAccountingRollbackAndHealthRecovery(t *testing.T) {
	s, team, _ := accountingStore(t)
	l := NewLogger(s, nil)
	l.Close() // Drive the same single-worker method deterministically below.
	e := intent(t, s, team, "rollback", true, true)
	e.Accounting, e.CostCents, e.TokenDetails = "priced", 9, json.RawMessage(`{"broken":`)
	if _, err := l.Record(e); err == nil || l.Ready() == nil {
		t.Fatal("failed completion did not pause admission")
	}
	var state string
	var cost, rows int64
	if err := s.Pool.QueryRow(context.Background(), `SELECT status,settled_cost_cents FROM budget_reservations WHERE request_id=$1`, e.RequestID).Scan(&state, &cost); err != nil || state != "reserved" || cost != 0 {
		t.Fatalf("failed usage insert partially settled: %s/%d %v", state, cost, err)
	}
	if err := s.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM usage_log`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("partial usage rows=%d: %v", rows, err)
	}
	if n, err := l.reconcile(context.Background()); err != nil || n != 0 || l.Ready() == nil {
		t.Fatalf("live intent recovered too early: %d %v", n, err)
	}
	// Later healthy-replica work must not move the fixed failure checkpoint.
	intent(t, s, team, "later-replica", true, false)
	expireIntent(t, s, e.RequestID)
	if n, err := l.reconcile(context.Background()); err != nil || n != 1 || l.Ready() != nil {
		t.Fatalf("recovery did not resume admission: %d %v %v", n, err, l.Ready())
	}
	if err := s.Pool.QueryRow(context.Background(), `SELECT accounting_state,cost_cents FROM usage_log WHERE accounting_id=$1`, e.RequestID).Scan(&state, &cost); err != nil || state != "estimated" || cost != 123 {
		t.Fatalf("lost conservative charge: %s/%d %v", state, cost, err)
	}
	// Failed completion followed by a successful request-local retry also heals,
	// even though the worker itself has no rows left to recover.
	e = intent(t, s, team, "retry", true, true)
	e.TokenDetails = json.RawMessage(`{"broken":`)
	_, _ = l.Record(e)
	e.TokenDetails, e.Accounting, e.CostCents = nil, "priced", 5
	if _, err := l.Record(e); err != nil {
		t.Fatal(err)
	}
	// Settle the unrelated previously live operation before this new checkpoint.
	if _, err := s.FinalizeUsage(context.Background(), store.UsageEntry{TeamID: team, RequestID: "later-replica", AccountingID: "later-replica", Accounting: "unknown"}); err != nil {
		t.Fatal(err)
	}
	if n, err := l.reconcile(context.Background()); err != nil || n != 0 || l.Ready() != nil {
		t.Fatalf("successful retry left permanent pause: %d %v %v", n, err, l.Ready())
	}
	// A transient recovery failure with no pending intents heals as well.
	l.failures.Add(1)
	if _, err := l.reconcile(context.Background()); err != nil || l.Ready() != nil {
		t.Fatalf("idle recovery could not heal: %v", err)
	}
}

func TestAccountingCorrelationIDCannotCompleteAnotherIntent(t *testing.T) {
	s, team, _ := accountingStore(t)
	e := intent(t, s, team, "active", true, true)
	denial := e
	denial.AccountingID, denial.StatusCode = "", 403
	if _, err := s.FinalizeUsage(context.Background(), denial); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := s.Pool.QueryRow(context.Background(), `SELECT state FROM inference_journal WHERE request_id='active'`).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("unadmitted request completed someone else's journal: %s %v", state, err)
	}
	foreign := e
	foreign.TeamID++
	if _, err := s.FinalizeUsage(context.Background(), foreign); err == nil {
		t.Fatal("foreign principal completed journal")
	}
	missing := e
	missing.AccountingID = "missing"
	if _, err := s.FinalizeUsage(context.Background(), missing); err == nil {
		t.Fatal("missing intent silently became standalone usage")
	}
}

func TestAccountingRecoveryStatesAndBounds(t *testing.T) {
	s, team, _ := accountingStore(t)
	for _, tc := range []struct {
		name             string
		started, reserve bool
		want             string
		cost             int64
	}{
		{"pre-call", false, true, "not_billable", 0},
		{"no-reservation", true, false, "unknown", 0},
		{"reserved", true, true, "estimated", 123},
	} {
		intent(t, s, team, tc.name, tc.started, tc.reserve)
		expireIntent(t, s, tc.name)
		if n, err := s.ReconcileAccounting(context.Background()); err != nil || n != 1 {
			t.Fatalf("reconcile %s: %d %v", tc.name, n, err)
		}
		var state string
		var cost int64
		if err := s.Pool.QueryRow(context.Background(), `SELECT accounting_state,cost_cents FROM usage_log WHERE accounting_id=$1`, tc.name).Scan(&state, &cost); err != nil || state != tc.want || cost != tc.cost {
			t.Fatalf("%s: %s/%d %v", tc.name, state, cost, err)
		}
	}
	for i := range 130 {
		id := fmt.Sprintf("batch-%03d", i)
		intent(t, s, team, id, true, false)
		expireIntent(t, s, id)
	}
	// A live finalizer's locked intent cannot block another replica's recovery.
	tx, err := s.Pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `SELECT 1 FROM inference_journal WHERE request_id='batch-000' FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ReconcileAccounting(context.Background()); err != nil || n != 64 {
		t.Fatalf("batch bound/skip-locked failed: %d %v", n, err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if n, err := s.ReconcileAccounting(context.Background()); err != nil || n > 64 {
				t.Errorf("replica reconciliation: %d %v", n, err)
			}
		}()
	}
	wg.Wait()
	_ = tx.Rollback(context.Background())
	if n, err := s.ReconcileAccounting(context.Background()); err != nil || n != 1 {
		t.Fatalf("locked intent not recovered after release: %d %v", n, err)
	}
	if n, err := s.ReconcileAccounting(context.Background()); err != nil || n != 0 {
		t.Fatalf("recovery not idempotent: %d %v", n, err)
	}
	for _, e := range []store.UsageEntry{{TeamID: team}, {TeamID: team, RequestID: strings.Repeat("x", 257)}, {TeamID: team, RequestID: "oversized", Alias: strings.Repeat("x", 25<<10)}} {
		if err := s.BeginAccounting(context.Background(), e, time.Minute); err == nil {
			t.Fatal("unbounded intent accepted")
		}
	}
}

func TestAccountingAbruptProcessExit(t *testing.T) {
	s, team, dsn := accountingStore(t)
	t.Cleanup(func() { assertDailyTotals(t, s) })
	entry, _ := json.Marshal(store.UsageEntry{TeamID: team, RequestID: "killed-process", AccountingID: "killed-process", Alias: "test-model"})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAccountingCrashChild$")
	cmd.Env = append(os.Environ(), "GATEMUX_ACCOUNTING_CHILD_DSN="+dsn, "GATEMUX_ACCOUNTING_CHILD_ENTRY="+string(entry))
	output, err := cmd.CombinedOutput()
	var exited *exec.ExitError
	if !errors.As(err, &exited) || exited.ExitCode() != 23 {
		t.Fatalf("child did not exit at interruption point: %v %s", err, output)
	}
	// Advance only this test's durable deadline; do not spend 90s waiting for it.
	expireIntent(t, s, "killed-process")
	l := NewLogger(s, nil) // New process-equivalent worker, no prior in-memory entry.
	defer l.Close()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var state string
		var cost int64
		err := s.Pool.QueryRow(context.Background(), `SELECT accounting_state,cost_cents FROM usage_log WHERE accounting_id='killed-process'`).Scan(&state, &cost)
		if err == nil {
			if state != "estimated" || cost != 123 {
				t.Fatalf("crash silently refunded work: %s/%d", state, cost)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement worker did not recover: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n, err := s.ReconcileAccounting(context.Background()); err != nil || n != 0 {
		t.Fatalf("replacement worker duplicated completion: %d %v", n, err)
	}
}

func TestAccountingCrashChild(t *testing.T) {
	dsn := os.Getenv("GATEMUX_ACCOUNTING_CHILD_DSN")
	if dsn == "" {
		t.Skip("only run by the abrupt-exit test")
	}
	s, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	var e store.UsageEntry
	if err := json.Unmarshal([]byte(os.Getenv("GATEMUX_ACCOUNTING_CHILD_ENTRY")), &e); err != nil {
		t.Fatal(err)
	}
	intent(t, s, e.TeamID, e.RequestID, true, true)
	os.Exit(23) // Intentionally no defers, logger flush, HTTP cleanup or pool close.
}

func TestAccountingBudgetIncludesUnreservedHistory(t *testing.T) {
	for _, scope := range []string{"team", "user", "service_account", "key", "customer"} {
		t.Run(scope, func(t *testing.T) {
			s, teamID, _ := accountingStore(t)
			ctx := context.Background()
			user, err := s.CreateUser(ctx, "budget@example.test", "Budget", []byte("fixture-hash"), &teamID, store.RoleMember)
			if err != nil {
				t.Fatal(err)
			}
			sa, err := s.CreateServiceAccount(ctx, store.CreateServiceAccountParams{TeamID: teamID, Name: "automation"})
			if err != nil {
				t.Fatal(err)
			}
			key, err := s.CreateVirtualKey(ctx, store.CreateVirtualKeyParams{TeamID: teamID, KeyHash: []byte("fixture-hash"), Prefix: "fixture", Name: "key"})
			if err != nil {
				t.Fatal(err)
			}
			customer, err := s.GetOrCreateCustomer(ctx, teamID, "customer")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.UpsertPricing(ctx, "openai", "test-model", 1_000_000, 1_000_000); err != nil {
				t.Fatal(err)
			}
			e := store.UsageEntry{TeamID: teamID, RequestID: "previous-uncapped", Accounting: "priced", CostCents: 37, StatusCode: 200, UserID: &user.ID, ServiceAccountID: &sa.ID, KeyID: &key.ID, CustomerID: &customer.ID}
			if err := s.InsertUsage(ctx, e); err != nil {
				t.Fatal(err)
			}
			limit := int64(37)
			team := &store.Team{ID: teamID, Period: "month"}
			switch scope {
			case "team":
				team.UsdLimitCents = &limit
			case "user":
				user.UsdLimitCents = &limit
			case "service_account":
				sa.UsdLimitCents = &limit
			case "key":
				key.ScopedUsdLimitCents = &limit
			case "customer":
				customer.UsdLimitCents = &limit
			}
			service := budget.New(s)
			req := budget.AdmissionRequest{RequestID: "new-budget", Team: team, User: user, ServiceAccount: sa, Key: key, Customer: customer, Alias: "test-model", PromptTokens: 1, Targets: []budget.Target{{ProviderType: "openai", UpstreamModel: "test-model"}}}
			_, err = service.Admit(ctx, req)
			var exceeded *budget.ExceededError
			if !errors.As(err, &exceeded) || exceeded.Scope != scope || exceeded.UsedCents != 37 {
				t.Fatalf("unreserved history ignored: %v", err)
			}
			limit = 100
			if _, err := service.Admit(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := service.Settle(ctx, req.RequestID, 1); err != nil {
				t.Fatal(err)
			}
			e.RequestID, e.CostCents = req.RequestID, 1
			if err := s.InsertUsage(ctx, e); err != nil {
				t.Fatal(err)
			}
			limit, req.RequestID = 38, "next-budget"
			_, err = service.Admit(ctx, req)
			if !errors.As(err, &exceeded) || exceeded.UsedCents != 38 {
				t.Fatalf("usage/reservation counted twice: %v", err)
			}
			if scope == "user" {
				summary, err := service.UserSummary(ctx, user)
				if err != nil || summary.UsedCents != 38 {
					t.Fatalf("summary disagrees with admission: %+v %v", summary, err)
				}
			}
		})
	}
}

func TestAccountingRecoveryDeletedIdentityAndLegacyReservation(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	key, err := s.CreateVirtualKey(ctx, store.CreateVirtualKeyParams{TeamID: team, KeyHash: []byte("fixture-hash"), Prefix: "fixture", Name: "key"})
	if err != nil {
		t.Fatal(err)
	}
	e := store.UsageEntry{TeamID: team, RequestID: "deleted-key", AccountingID: "deleted-key", KeyID: &key.ID}
	if err := s.BeginAccounting(ctx, e, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAccountingStarted(ctx, e.RequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM virtual_keys WHERE id=$1`, key.ID); err != nil {
		t.Fatal(err)
	}
	expireIntent(t, s, e.RequestID)
	if n, err := s.ReconcileAccounting(ctx); err != nil || n != 1 {
		t.Fatalf("deleted key blocked recovery: %d %v", n, err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents) VALUES('pre-migration',$1,'test',123)`, team); err != nil {
		t.Fatal(err)
	}
	e.RequestID = "pre-migration"
	if err := s.BeginAccounting(ctx, e, time.Minute); !errors.Is(err, store.ErrAccountingConflict) {
		t.Fatalf("old reservation could be reused: %v", err)
	}
}

func TestAccountingMigrationPreservesLegacyEvidence(t *testing.T) {
	s, team, _ := accountingStore(t)
	ctx := context.Background()
	rollbackDailyMigration(t, s)
	down, err := os.ReadFile("../store/migrations/0038_durable_accounting.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Roll back only the empty schema created by this test to build a v37 fixture.
	if _, err := s.Pool.Exec(ctx, string(down)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version='0038_durable_accounting'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO usage_log(team_id,alias,model_requested,request_id,status_code,cost_cents) VALUES($1,'legacy','legacy','legacy-usage',200,17)`, team); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO budget_reservations(request_id,team_id,alias,estimated_cost_cents) VALUES('legacy-pending',$1,'legacy',19)`, team); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	var state string
	var cost int64
	if err := s.Pool.QueryRow(ctx, `SELECT accounting_state,cost_cents FROM usage_log WHERE request_id='legacy-usage'`).Scan(&state, &cost); err != nil || state != "legacy" || cost != 17 {
		t.Fatalf("history rewritten: %s/%d %v", state, cost, err)
	}
	if n, err := s.ReconcileAccounting(ctx); err != nil || n != 0 {
		t.Fatalf("invented old usage evidence: %d %v", n, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT status,estimated_cost_cents FROM budget_reservations WHERE request_id='legacy-pending'`).Scan(&state, &cost); err != nil || state != "reserved" || cost != 19 {
		t.Fatalf("historical reservation refunded: %s/%d %v", state, cost, err)
	}
}
