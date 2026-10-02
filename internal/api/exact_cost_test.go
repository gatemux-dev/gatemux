package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// exactCostUpstream answers every chat completion with the given usage object.
func exactCostUpstream(t *testing.T, f *chatFixture, usage string) {
	t.Helper()
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"id","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":`+usage+`}`)
	}))
	t.Cleanup(mock.Close)
	setStreamUpstream(t, f, mock.URL, "openai")
}

// waitUsageRows polls until the async usage writer has recorded n rows for a team.
func waitUsageRows(t *testing.T, f *chatFixture, team int64, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var rows int
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM usage_log WHERE team_id=$1`, team).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		if rows == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("usage rows=%d, want %d", rows, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExactCostReferenceWorkload(t *testing.T) {
	t.Run("1000 small requests", func(t *testing.T) {
		f := newChatFixture(t)
		exactCostUpstream(t, f, `{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150}`)
		f.upsertPricing(10, 40)
		team := f.setTeamBudget(f.createTeam("exact-reference"), 100000)
		key := f.issueKey(team, nil)
		for i := range 1000 {
			if code, body := f.chatPOST(key); code != 200 {
				t.Fatalf("request %d: %d %s", i, code, body)
			}
		}
		waitUsageRows(t, f, team.ID, 1000)
		var wrong int
		var total, daily int64
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT
		 count(*) FILTER (WHERE cost_microcents IS DISTINCT FROM 3000 OR cost_cents <> 1),
		 COALESCE(SUM(cost_microcents),0),
		 (SELECT COALESCE(SUM(cost_microcents),0) FROM budget_daily_totals WHERE scope='team' AND subject_id=$1)
		 FROM usage_log WHERE team_id=$1`, team.ID).Scan(&wrong, &total, &daily); err != nil {
			t.Fatal(err)
		}
		// Before exact costs this workload recorded 2 cents per request: 2000 cents.
		if wrong != 0 || total != 3_000_000 || daily != 3_000_000 {
			t.Fatalf("rows off by exact cost=%d, usage total=%d µ¢, daily total=%d µ¢", wrong, total, daily)
		}
	})

	t.Run("tiered usage", func(t *testing.T) {
		f := newChatFixture(t)
		exactCostUpstream(t, f, `{"prompt_tokens":1000,"completion_tokens":500,"total_tokens":1500,"prompt_tokens_details":{"cached_tokens":300},"completion_tokens_details":{"reasoning_tokens":200}}`)
		read, reasoning := int64(3), int64(29)
		if _, err := f.Store.UpsertPricing(context.Background(), "openai", f.UpModel, 7, 13, store.PricingTiers{CacheReadPerMillionCents: &read, ReasoningPerMillionCents: &reasoning}); err != nil {
			t.Fatal(err)
		}
		team := f.setTeamBudget(f.createTeam("exact-tiers"), 100000)
		key := f.issueKey(team, nil)
		if code, body := f.chatPOST(key); code != 200 {
			t.Fatalf("chat: %d %s", code, body)
		}
		waitUsageRows(t, f, team.ID, 1)
		// 700×7 + 300×3 + 300×13 + 200×29 µ¢, no rounding anywhere.
		want := int64(700*7 + 300*3 + 300*13 + 200*29)
		var cents int64
		var exact *int64
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT cost_cents,cost_microcents FROM usage_log WHERE team_id=$1`, team.ID).Scan(&cents, &exact); err != nil {
			t.Fatal(err)
		}
		if exact == nil || *exact != want || cents != 1 {
			t.Fatalf("stored %d ¢ / %v µ¢, want 1 ¢ / %d µ¢", cents, exact, want)
		}
		var daily int64
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(cost_microcents),0) FROM budget_daily_totals WHERE scope='team' AND subject_id=$1`, team.ID).Scan(&daily); err != nil || daily != want {
			t.Fatalf("daily total %d µ¢, want %d: %v", daily, want, err)
		}
	})
}
