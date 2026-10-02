package alerts

import (
	"context"
	"strings"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestBudgetThresholdExact(t *testing.T) {
	s, team := alertStore(t)
	ctx := context.Background()
	e := New(s, nil)
	limit := int64(1)
	slug := ""
	if err := s.Pool.QueryRow(ctx, `SELECT slug FROM teams WHERE id=$1`, team).Scan(&slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTeamBudget(ctx, slug, &limit, "month"); err != nil {
		t.Fatal(err)
	}
	spend := func(id string, microcents int64) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, `INSERT INTO usage_log(team_id,alias,request_id,model_requested,status_code,cost_cents,cost_microcents)
		 VALUES($1,'chat',$2,'chat',200,$3,$4)`, team, id, store.CeilCents(microcents), microcents); err != nil {
			t.Fatal(err)
		}
	}
	var hooks, slack sink
	webhook := rule(t, s, hooks.server(t), "budget_threshold", &team, map[string]any{"threshold_pct": 50.0})
	chat, err := s.CreateAlertRule(ctx, store.CreateAlertRuleParams{Name: "slack", ScopeType: "team", ScopeID: &team, TriggerType: "budget_threshold",
		ThresholdOptions: map[string]any{"threshold_pct": 50.0}, ChannelType: "slack", ChannelTarget: slack.server(t)})
	if err != nil {
		t.Fatal(err)
	}

	// 0.499999 of a 1-cent limit: whole-cent rounding would report 1 cent (100%).
	spend("under", 499_999)
	e.evaluateRule(ctx, webhook)
	e.evaluateRule(ctx, chat)
	if hooks.count() != 0 || slack.count() != 0 {
		t.Fatalf("fired below the exact threshold: %v %v", hooks.payloads, slack.payloads)
	}
	spend("at", 1)
	e.evaluateRule(ctx, webhook)
	e.evaluateRule(ctx, chat)
	if hooks.count() != 1 || slack.count() != 1 {
		t.Fatalf("did not fire at exactly 50%%: %d %d", hooks.count(), slack.count())
	}
	p := hooks.payloads[0]
	if p["spend_microcents"] != "500000" || p["limit_microcents"] != "1000000" || p["spend_cents"] != 1.0 || p["limit_cents"] != 1.0 {
		t.Fatalf("payload amounts: %v", p)
	}
	if text, _ := slack.payloads[0]["text"].(string); !strings.Contains(text, "limit $0.01, spent $0.005)") {
		t.Fatalf("slack text: %q", text)
	}
}

// Same table as specs/001-exact-subcent-costs/contracts/console-money.md.
func TestFormatMicrocentsUSD(t *testing.T) {
	for in, want := range map[int64]string{
		0: "$0.00", 1: "$0.00000001", 3000: "$0.00003", 1000000: "$0.01",
		150000000: "$1.50", 1234560000: "$12.3456", 9223372036854775807: "$92,233,720,368.54775807",
	} {
		if got := formatMicrocentsUSD(in); got != want {
			t.Fatalf("formatMicrocentsUSD(%d)=%q want %q", in, got, want)
		}
	}
}
