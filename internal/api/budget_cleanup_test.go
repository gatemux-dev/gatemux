package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type panickingResponseWriter struct{ *httptest.ResponseRecorder }

func (*panickingResponseWriter) Write([]byte) (int, error) { panic("test response write failure") }

func TestBudgetPanicReconciliationIsConservativeAndIdempotent(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(100, 200)
	team := f.setTeamBudget(f.createTeam("panic-budget"), 100000)
	key := f.issueKey(team, nil)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+f.Alias+`","messages":[{"role":"user","content":"hi"}]}`))
	id := "panic-budget-" + randHex(5)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("X-Request-Id", id)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected response panic")
			}
		}()
		f.Router.ServeHTTP(&panickingResponseWriter{httptest.NewRecorder()}, req)
	}()
	var status string
	var estimated, settled int64
	if err := f.Store.Pool.QueryRow(context.Background(), "SELECT status, estimated_cost_cents, settled_cost_cents FROM budget_reservations WHERE request_id=$1", id).Scan(&status, &estimated, &settled); err != nil {
		t.Fatal(err)
	}
	if status != "settled" || estimated <= 0 || settled != estimated {
		t.Fatalf("panic reservation: %s %d/%d", status, settled, estimated)
	}
	if _, err := f.Budget.SettleEstimated(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := f.Budget.Settle(context.Background(), id, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Store.Pool.QueryRow(context.Background(), "SELECT settled_cost_cents FROM budget_reservations WHERE request_id=$1", id).Scan(&settled); err != nil || settled != estimated {
		t.Fatalf("settlement changed after retry: %d %v", settled, err)
	}
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatal("panic leaked deployment")
	}
}
