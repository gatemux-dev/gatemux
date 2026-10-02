package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// seedCostRows writes one exact row (3000 µ¢), one pre-0041 row (2 whole
// cents, NULL µ¢) and, optionally, one unknown row. It returns their IDs.
func seedCostRows(t *testing.T, e *testEnv, team *store.Team, withUnknown bool) (exact, legacy, unknown int64) {
	t.Helper()
	ctx := context.Background()
	three := int64(3000)
	var err error
	exact, err = e.Store.InsertUsageWithID(ctx, store.UsageEntry{TeamID: team.ID, Alias: "exact", ModelRequested: "exact",
		RequestID: "exact-" + randHex(6), StatusCode: 200, Accounting: "priced", CostMicrocents: &three})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Pool.QueryRow(ctx, `INSERT INTO usage_log(team_id,alias,model_requested,request_id,status_code,cost_cents,accounting_state)
	 VALUES($1,'legacy','legacy',$2,200,2,'priced') RETURNING id`, team.ID, "legacy-"+randHex(6)).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if withUnknown {
		zero := int64(0)
		unknown, err = e.Store.InsertUsageWithID(ctx, store.UsageEntry{TeamID: team.ID, Alias: "unknown", ModelRequested: "unknown",
			RequestID: "unknown-" + randHex(6), StatusCode: 503, Accounting: "unknown", CostMicrocents: &zero})
		if err != nil {
			t.Fatal(err)
		}
	}
	return exact, legacy, unknown
}

func TestUsageJSONCostFields(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("cost-fields")
	exact, legacy, unknown := seedCostRows(t, e, team, true)
	want := map[int64]struct {
		microcents, precision, state string
		cents                        float64
	}{
		exact:   {"3000", "exact", "priced", 1},
		legacy:  {"2000000", "whole_cent", "priced", 2},
		unknown: {"0", "exact", "unknown", 0},
	}
	check := func(row map[string]any) {
		t.Helper()
		id := int64(row["id"].(float64))
		w := want[id]
		if row["cost_microcents"] != w.microcents || row["cost_cents"] != w.cents || row["cost_precision"] != w.precision || row["accounting_state"] != w.state {
			t.Fatalf("row %d cost fields: microcents=%#v cents=%#v precision=%#v state=%#v", id, row["cost_microcents"], row["cost_cents"], row["cost_precision"], row["accounting_state"])
		}
	}
	code, body := e.GET("/admin/usage?team="+team.Slug, e.MasterKey)
	var rows []map[string]any
	if code != http.StatusOK || json.Unmarshal(body, &rows) != nil || len(rows) != 3 {
		t.Fatalf("list usage: %d %s", code, body)
	}
	for _, row := range rows {
		check(row)
	}
	for id := range want {
		code, body := e.GET(fmt.Sprintf("/admin/usage/%d", id), e.MasterKey)
		var row map[string]any
		if code != http.StatusOK || json.Unmarshal(body, &row) != nil {
			t.Fatalf("get usage %d: %d %s", id, code, body)
		}
		check(row)
	}
}

func TestUsageCSVColumns(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("cost-csv")
	exact, legacy, _ := seedCostRows(t, e, team, false)
	code, body := e.GET("/admin/export/usage.csv?team="+team.Slug, e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("export: %d %s", code, body)
	}
	records, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("csv: %d rows %v", len(records), err)
	}
	if got := strings.Join(records[0], ","); got != "id,ts,team,alias,deployment,model,prompt_tokens,completion_tokens,total_tokens,cost_cents,latency_ms,status_code,error,accounting_state,cost_microcents,cost_precision" {
		t.Fatalf("csv header: %s", got)
	}
	want := map[string][3]string{fmt.Sprint(exact): {"1", "3000", "exact"}, fmt.Sprint(legacy): {"2", "2000000", "whole_cent"}}
	for _, r := range records[1:] {
		if w := want[r[0]]; r[9] != w[0] || r[14] != w[1] || r[15] != w[2] {
			t.Fatalf("csv row %v, want cents/µ¢/precision %v", r, w)
		}
	}
}

func TestSpendIncludesHistory(t *testing.T) {
	e := newTestEnv(t)
	spend := func(team *store.Team) map[string]any {
		t.Helper()
		code, body := e.GET("/admin/spend?team="+team.Slug, e.MasterKey)
		var report struct {
			Total   map[string]any   `json:"total"`
			Aliases []map[string]any `json:"aliases"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &report) != nil {
			t.Fatalf("spend: %d %s", code, body)
		}
		for _, alias := range report.Aliases {
			if _, ok := alias["cost_microcents"].(string); !ok {
				t.Fatalf("alias row without exact cost: %v", alias)
			}
		}
		return report.Total
	}
	mixed := e.createTeam("spend-mixed")
	seedCostRows(t, e, mixed, true)
	total := spend(mixed)
	if total["cost_microcents"] != "2003000" || total["cost_cents"] != 3.0 || total["includes_whole_cent_history"] != true {
		t.Fatalf("mixed total: %v", total)
	}
	exactOnly := e.createTeam("spend-exact")
	three := int64(3000)
	if err := e.Store.InsertUsage(context.Background(), store.UsageEntry{TeamID: exactOnly.ID, Alias: "exact", ModelRequested: "exact",
		RequestID: "exact-" + randHex(6), StatusCode: 200, Accounting: "priced", CostMicrocents: &three}); err != nil {
		t.Fatal(err)
	}
	total = spend(exactOnly)
	if total["cost_microcents"] != "3000" || total["cost_cents"] != 1.0 || total["includes_whole_cent_history"] != false {
		t.Fatalf("exact-only total: %v", total)
	}
}
