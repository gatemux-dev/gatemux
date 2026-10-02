package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// settleKeySpend records already-settled spend in micro-cents on a key.
func settleKeySpend(t *testing.T, f *chatFixture, team *store.Team, key *store.VirtualKey, microcents int64) {
	t.Helper()
	if _, err := f.Store.Pool.Exec(context.Background(), `INSERT INTO budget_reservations(request_id,team_id,key_id,alias,settled_cost_cents,settled_cost_microcents,status)
	 VALUES($1,$2,$3,$4,$5,$6,'settled')`, "spent-"+randHex(8), team.ID, key.ID, f.Alias, store.CeilCents(microcents), microcents); err != nil {
		t.Fatal(err)
	}
}

func TestSubCentRemainder(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(10, 0) // 10 µ¢ per prompt token
	targets := []budget.Target{{ProviderType: "openai", UpstreamModel: f.UpModel}}
	admit := func(team *store.Team, key *store.VirtualKey, customer *store.Customer, prompt int) error {
		_, err := f.Budget.Admit(context.Background(), budget.AdmissionRequest{RequestID: "subcent-" + randHex(8), Team: team, Key: key,
			Customer: customer, Alias: f.Alias, PromptTokens: prompt, Targets: targets})
		return err
	}
	team := f.createTeam("subcent")
	_, key := issueBudgetKey(t, f.testEnv, team, 100)
	settleKeySpend(t, f, team, key, 99_999_000) // 99.9990 cents of 100

	t.Run("a: 500 µ¢ fits the remainder", func(t *testing.T) {
		if err := admit(team, key, nil, 50); err != nil {
			t.Fatalf("refused a fitting sub-cent estimate: %v", err)
		}
	})
	t.Run("b: 2000 µ¢ does not", func(t *testing.T) {
		err := admit(team, key, nil, 200)
		var exceeded *budget.ExceededError
		if !errors.As(err, &exceeded) || exceeded.Scope != "key" || exceeded.NeedMicrocents != 2000 ||
			exceeded.LimitMicrocents != 100_000_000 || exceeded.UsedMicrocents != 99_999_500 || exceeded.NeedCents != 1 || exceeded.UsedCents != 100 {
			t.Fatalf("over-admitted or wrong denial: %+v %v", exceeded, err)
		}
	})
	t.Run("c: contention never over-admits", func(t *testing.T) {
		team := f.createTeam("subcent-race")
		_, key := issueBudgetKey(t, f.testEnv, team, 1)
		settleKeySpend(t, f, team, key, 999_000) // 1000 µ¢ left
		var admitted atomic.Int32
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := admit(team, key, nil, 30) // 300 µ¢ each
				var exceeded *budget.ExceededError
				switch {
				case err == nil:
					admitted.Add(1)
				case !errors.As(err, &exceeded):
					t.Errorf("unexpected admission error: %v", err)
				}
			}()
		}
		wg.Wait()
		if admitted.Load() != 3 {
			t.Fatalf("admitted %d of 16 against a 1000 µ¢ remainder, want 3", admitted.Load())
		}
	})
	t.Run("d: a zero limit refuses 1 µ¢", func(t *testing.T) {
		team := f.createTeam("subcent-zero")
		zero := int64(0)
		customer, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "zero", UsdLimitCents: &zero})
		if err != nil {
			t.Fatal(err)
		}
		cheap := "cheap-" + randHex(4)
		if _, err := f.Store.UpsertPricing(context.Background(), "openai", cheap, 1, 0); err != nil {
			t.Fatal(err)
		}
		_, err = f.Budget.Admit(context.Background(), budget.AdmissionRequest{RequestID: "subcent-" + randHex(8), Team: team, Customer: customer,
			Alias: f.Alias, PromptTokens: 1, Targets: []budget.Target{{ProviderType: "openai", UpstreamModel: cheap}}})
		var exceeded *budget.ExceededError
		if !errors.As(err, &exceeded) || exceeded.Scope != "customer" || exceeded.NeedMicrocents != 1 {
			t.Fatalf("zero customer budget admitted 1 µ¢: %v", err)
		}
	})
	t.Run("e: a limit beyond int64 µ¢ saturates", func(t *testing.T) {
		// The key API maximum (2^53-1 cents) and a team at MaxInt64 cents both
		// exceed MaxInt64 µ¢, so their limits saturate instead of wrapping.
		team := f.setTeamBudget(f.createTeam("subcent-huge"), math.MaxInt64)
		_, key := issueBudgetKey(t, f.testEnv, team, int64(1<<53-1))
		settleKeySpend(t, f, team, key, 1)
		if err := admit(team, key, nil, 50); err != nil {
			t.Fatalf("saturated limit refused: %v", err)
		}
	})
	t.Run("f: spend exactly at the limit", func(t *testing.T) {
		team := f.createTeam("subcent-full")
		_, key := issueBudgetKey(t, f.testEnv, team, 1)
		settleKeySpend(t, f, team, key, 1_000_000)
		var exceeded *budget.ExceededError
		if err := admit(team, key, nil, 1); !errors.As(err, &exceeded) {
			t.Fatalf("positive estimate admitted at the limit: %v", err)
		}
		if err := admit(team, key, nil, 0); err != nil {
			t.Fatalf("zero estimate refused at the limit: %v", err)
		}
	})
}

func TestExactCostFailsClosed(t *testing.T) {
	newCountingFixture := func(t *testing.T, usage string) (*chatFixture, *atomic.Int32) {
		f := newChatFixture(t)
		var calls atomic.Int32
		mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"id":"id","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":`+usage+`}`)
		}))
		t.Cleanup(mock.Close)
		setStreamUpstream(t, f, mock.URL, "openai")
		return f, &calls
	}

	t.Run("estimate overflow refuses before the provider", func(t *testing.T) {
		f, calls := newCountingFixture(t, `{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}`)
		f.upsertPricing(math.MaxInt64, 0)
		team := f.setTeamBudget(f.createTeam("overflow-estimate"), 100)
		code, body := f.chatPOST(f.issueKey(team, nil))
		if code != 503 || !strings.Contains(string(body), "budget_unavailable") || calls.Load() != 0 {
			t.Fatalf("overflowing estimate: %d %s calls=%d", code, body, calls.Load())
		}
	})

	t.Run("budget read failure refuses before the provider", func(t *testing.T) {
		f, calls := newCountingFixture(t, `{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}`)
		f.upsertPricing(10, 0)
		team := f.createTeam("unreadable-budget")
		raw, key := issueBudgetKey(t, f.testEnv, team, 100)
		// Two valid daily rows whose sum exceeds int64: the read itself fails.
		if _, err := f.Store.Pool.Exec(context.Background(), `INSERT INTO budget_daily_totals(scope,subject_id,day,cost_microcents)
		 SELECT 'key',$1,d,9223372036854775807 FROM (VALUES (date_trunc('month',NOW() AT TIME ZONE 'UTC')::date),
		 ((date_trunc('month',NOW() AT TIME ZONE 'UTC')+interval '1 day')::date)) v(d)`, key.ID); err != nil {
			t.Fatal(err)
		}
		code, body := f.chatPOST(raw)
		if code != 503 || !strings.Contains(string(body), "budget_unavailable") || calls.Load() != 0 {
			t.Fatalf("unreadable budget: %d %s calls=%d", code, body, calls.Load())
		}
	})

	t.Run("actual cost overflow keeps the estimate", func(t *testing.T) {
		// The ~22-token estimate fits; 10^7 reported tokens × 10^12 µ¢ does not.
		f, calls := newCountingFixture(t, `{"prompt_tokens":10000000,"completion_tokens":0,"total_tokens":10000000}`)
		f.upsertPricing(1_000_000_000_000, 0)
		team := f.setTeamBudget(f.createTeam("overflow-actual"), 1_000_000_000)
		id := "overflow-actual-" + randHex(6)
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, f.Alias)))
		req.Header.Set("Authorization", "Bearer "+f.issueKey(team, nil))
		req.Header.Set("X-Request-Id", id)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, req)
		if w.Code != 200 || calls.Load() != 1 {
			t.Fatalf("chat: %d %s", w.Code, w.Body)
		}
		var state string
		var exact, estimate *int64
		deadline := time.Now().Add(3 * time.Second)
		for {
			err := f.Store.Pool.QueryRow(context.Background(), `SELECT u.accounting_state,u.cost_microcents,b.estimated_cost_microcents
			 FROM usage_log u JOIN budget_reservations b USING(request_id) WHERE u.request_id=$1`, id).Scan(&state, &exact, &estimate)
			if err == nil || time.Now().After(deadline) {
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if state != "estimated" || exact == nil || estimate == nil || *exact != *estimate || *exact <= 0 {
			t.Fatalf("overflowing actual cost: state=%s cost=%v estimate=%v", state, exact, estimate)
		}
	})
}
