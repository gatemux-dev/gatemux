package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

var microcentsPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,18})$`)

// assertCostPair checks one contracts/http-fields.md pair: the exact field is
// a canonical base-10 string, the cents field a number, and cents round up.
func assertCostPair(t *testing.T, where string, obj map[string]any, cents, microcents string, want int64) {
	t.Helper()
	exact, ok := obj[microcents].(string)
	if !ok || !microcentsPattern.MatchString(exact) {
		t.Fatalf("%s: %s = %#v, want a base-10 string", where, microcents, obj[microcents])
	}
	whole, ok := obj[cents].(float64)
	if !ok {
		t.Fatalf("%s: %s = %#v, want a JSON number", where, cents, obj[cents])
	}
	m, err := strconv.ParseInt(exact, 10, 64)
	if err != nil {
		t.Fatalf("%s: %s: %v", where, microcents, err)
	}
	if whole*1e6 < float64(m) || m > 0 && whole < 1 {
		t.Fatalf("%s: %s=%v does not cover %s=%d", where, cents, whole, microcents, m)
	}
	if want >= 0 && m != want {
		t.Fatalf("%s: %s=%d, want %d", where, microcents, m, want)
	}
}

func TestCostFieldContract(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	team := e.createTeam("cost-contract")
	user, token := e.createUser("cost-contract", false)
	key := e.createKeyForUser(team, user)
	customer, err := e.Store.CreateCustomer(ctx, store.CreateCustomerParams{TeamID: team.ID, ExternalID: "contract"})
	if err != nil {
		t.Fatal(err)
	}
	three := int64(3000)
	if err := e.Store.InsertUsage(ctx, store.UsageEntry{TeamID: team.ID, UserID: &user.ID, KeyID: &key.ID, CustomerID: &customer.ID,
		CustomerExternalID: customer.ExternalID, Alias: "contract", ModelRequested: "contract", RequestID: "contract-" + randHex(6),
		StatusCode: 200, Accounting: "priced", CostMicrocents: &three}); err != nil {
		t.Fatal(err)
	}
	get := func(path, auth string, out any) {
		t.Helper()
		code, body := e.GET(path, auth)
		if code != http.StatusOK || json.Unmarshal(body, out) != nil {
			t.Fatalf("GET %s: %d %s", path, code, body)
		}
	}

	var buckets []map[string]any
	get("/admin/usage/aggregate?team="+team.Slug, e.MasterKey, &buckets)
	if len(buckets) != 1 || buckets[0]["includes_whole_cent_history"] != false {
		t.Fatalf("aggregate: %v", buckets)
	}
	assertCostPair(t, "usage/aggregate", buckets[0], "cost_cents", "cost_microcents", 3000)

	var series struct {
		Series []struct {
			Total             map[string]any `json:"total"`
			Aliases           map[string]any `json:"aliases"`
			AliasesMicrocents map[string]any `json:"aliases_microcents"`
		} `json:"series"`
	}
	get("/admin/spend/timeseries?team="+team.Slug, e.MasterKey, &series)
	if len(series.Series) != 1 || series.Series[0].Total["includes_whole_cent_history"] != false {
		t.Fatalf("timeseries: %+v", series)
	}
	point := series.Series[0]
	assertCostPair(t, "spend/timeseries total", point.Total, "cost_cents", "cost_microcents", 3000)
	assertCostPair(t, "spend/timeseries alias", map[string]any{"c": point.Aliases["contract"], "m": point.AliasesMicrocents["contract"]}, "c", "m", 3000)

	var projection map[string]any
	get(fmt.Sprintf("/admin/projections?scope_type=team&scope_id=%d", team.ID), e.MasterKey, &projection)
	assertCostPair(t, "projections spend", projection, "spend_so_far_cents", "spend_so_far_microcents", 3000)
	assertCostPair(t, "projections projected", projection, "projected_cents", "projected_microcents", -1)

	var policy struct{ Team, Key map[string]any }
	get(fmt.Sprintf("/admin/keys/%d/effective-policy", key.ID), e.MasterKey, &policy)
	assertCostPair(t, "effective-policy team", policy.Team, "spend_so_far_cents", "spend_so_far_microcents", 3000)
	assertCostPair(t, "effective-policy key", policy.Key, "spend_so_far_cents", "spend_so_far_microcents", 3000)

	for _, c := range []struct{ path, auth string }{
		{fmt.Sprintf("/admin/keys/%d/budget", key.ID), e.MasterKey},
		{"/admin/teams/" + team.Slug + "/customers/contract/budget", e.MasterKey},
		{"/me/budget", token},
	} {
		var summary map[string]any
		get(c.path, c.auth, &summary)
		assertCostPair(t, c.path, summary, "used_cents", "used_microcents", 3000)
	}

	var teams []struct {
		Slug  string         `json:"slug"`
		Stats map[string]any `json:"stats"`
	}
	get("/admin/teams?stats=1", e.MasterKey, &teams)
	found := false
	for _, listed := range teams {
		if listed.Slug == team.Slug {
			found = true
			assertCostPair(t, "teams?stats=1", listed.Stats, "period_spend_cents", "period_spend_microcents", 3000)
		}
	}
	if !found {
		t.Fatalf("team %s missing from directory", team.Slug)
	}
}
