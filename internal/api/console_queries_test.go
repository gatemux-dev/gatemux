package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// insertUsage writes one usage row with explicit status/latency so filter
// tests can target specific classes. Returns the request id.
func (e *testEnv) insertUsage(team *store.Team, user *store.User, key *store.VirtualKey, alias string, status, latency int) string {
	e.t.Helper()
	uid, kid := user.ID, key.ID
	reqID := "req-" + randHex(8)
	if err := e.Store.InsertUsage(context.Background(), store.UsageEntry{
		TeamID: team.ID, UserID: &uid, KeyID: &kid, Alias: alias,
		DeploymentName: "test-dep", RequestID: reqID, ModelRequested: alias, ModelUsed: "m",
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
		LatencyMs: latency, StatusCode: status,
	}); err != nil {
		e.t.Fatalf("insert usage: %v", err)
	}
	return reqID
}

func decodeRows(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return rows
}

// Every logs filter runs in SQL so totals and pages cover the whole window,
// not the 50 rows the console happens to have loaded.
func TestUsageFiltersRunServerSide(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("logfilter")
	user, _ := e.createUser("logfilter-user", false)
	key := e.createKeyForUser(team, user)
	alias := "alias-" + randHex(4)
	e.insertUsage(team, user, key, alias, 200, 50)
	e.insertUsage(team, user, key, alias, 503, 1500)
	e.insertUsage(team, user, key, "other-"+randHex(4), 429, 300)

	get := func(params url.Values) []map[string]any {
		t.Helper()
		params.Set("team", team.Slug)
		code, body := e.GET("/admin/usage?"+params.Encode(), e.MasterKey)
		if code != http.StatusOK {
			t.Fatalf("list %v: %d %s", params, code, body)
		}
		return decodeRows(t, body)
	}
	if rows := get(url.Values{"alias": {alias}}); len(rows) != 2 {
		t.Fatalf("alias filter: want 2 rows, got %d", len(rows))
	}
	if rows := get(url.Values{"status": {"5xx"}}); len(rows) != 1 || rows[0]["status_code"] != float64(503) {
		t.Fatalf("status filter: %v", rows)
	}
	if rows := get(url.Values{"status": {"error"}}); len(rows) != 2 {
		t.Fatalf("error filter: want 4xx and 5xx, got %d", len(rows))
	}
	if rows := get(url.Values{"latency": {"slow"}}); len(rows) != 1 {
		t.Fatalf("latency filter: want 1, got %d", len(rows))
	}
	if rows := get(url.Values{"q": {alias[len(alias)-4:]}}); len(rows) != 2 {
		t.Fatalf("search: want 2, got %d", len(rows))
	}
	if rows := get(url.Values{"key": {key.KeyPrefix}, "status": {"4xx"}}); len(rows) != 1 {
		t.Fatalf("key+status: want 1, got %d", len(rows))
	}
	if code, _ := e.GET("/admin/usage?status=3xx", e.MasterKey); code != http.StatusBadRequest {
		t.Fatalf("invalid status accepted: %d", code)
	}

	// Facets ignore the status filter but honour everything else.
	code, body := e.GET("/admin/usage/facets?team="+team.Slug+"&status=2xx", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("facets: %d %s", code, body)
	}
	var facets map[string]int64
	if err := json.Unmarshal(body, &facets); err != nil {
		t.Fatal(err)
	}
	if facets["2xx"] != 1 || facets["4xx"] != 1 || facets["5xx"] != 1 {
		t.Fatalf("facets: %v", facets)
	}
}

// The permalink endpoint returns one row and scopes managers to their team.
func TestUsageRowPermalinkScoping(t *testing.T) {
	e := newTestEnv(t)
	team, managerUser, managerToken := makeManager(e, "permalink")
	foreign := e.createTeam("permalink-foreign")
	fUser, _ := e.createUser("permalink-foreign-user", false)
	ownKey := e.createKeyForUser(team, managerUser)
	foreignKey := e.createKeyForUser(foreign, fUser)
	e.insertUsage(team, managerUser, ownKey, "own-alias", 200, 10)
	e.insertUsage(foreign, fUser, foreignKey, "foreign-alias", 200, 10)

	idFor := func(slug string) int64 {
		rows, _, err := e.Store.ListUsage(context.Background(), store.UsageFilter{TeamSlug: slug, Limit: 1})
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed lookup %s: %v", slug, err)
		}
		return rows[0].ID
	}
	own, other := idFor(team.Slug), idFor(foreign.Slug)
	if code, body := e.GET(fmt.Sprintf("/admin/usage/%d", own), managerToken); code != http.StatusOK {
		t.Fatalf("manager own row: %d %s", code, body)
	}
	if code, _ := e.GET(fmt.Sprintf("/admin/usage/%d", other), managerToken); code != http.StatusNotFound {
		t.Fatalf("manager opened foreign row: %d", code)
	}
	if code, _ := e.GET(fmt.Sprintf("/admin/usage/%d", other), e.MasterKey); code != http.StatusOK {
		t.Fatalf("admin foreign row: %d", code)
	}
}

func TestDirectorySearchRunsServerSide(t *testing.T) {
	e := newTestEnv(t)
	marker := randHex(5)
	e.createUserWithRole("dirsearch-"+marker, store.RoleManager, nil)
	e.createUserWithRole("dirsearch-other-"+randHex(5), store.RoleMember, nil)

	code, body := e.GET("/admin/users?q="+marker, e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("users search: %d %s", code, body)
	}
	if rows := decodeRows(t, body); len(rows) != 1 {
		t.Fatalf("users search: want 1, got %d", len(rows))
	}
	code, body = e.GET("/admin/users?q="+marker+"&role=member", e.MasterKey)
	if code != http.StatusOK || len(decodeRows(t, body)) != 0 {
		t.Fatalf("role filter should exclude the manager: %d %s", code, body)
	}
	if code, _ := e.GET("/admin/users?role=owner", e.MasterKey); code != http.StatusBadRequest {
		t.Fatalf("invalid role accepted: %d", code)
	}

	alias := "dirsearch-alias-" + marker
	// The API refuses aliases without deployments, but they exist in real
	// databases (a deployment deleted out from under an alias), so seed one.
	if _, err := e.Store.Pool.Exec(context.Background(), `INSERT INTO model_aliases (alias) VALUES ($1)`, alias); err != nil {
		t.Fatalf("seed unrouted alias: %v", err)
	}
	code, body = e.GET("/admin/aliases?q="+marker+"&unrouted=true", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("alias search: %d %s", code, body)
	}
	rows := decodeRows(t, body)
	if len(rows) != 1 || rows[0]["alias"] != alias {
		t.Fatalf("unrouted alias search: %v", rows)
	}
}

func TestSpendBreakdownGroups(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("spendgroup")
	user, _ := e.createUser("spendgroup-user", false)
	key := e.createKeyForUser(team, user)
	e.insertUsage(team, user, key, "a", 200, 10)
	e.insertUsage(team, user, key, "b", 200, 10)

	for _, dim := range []string{"team", "key", "user", "customer"} {
		code, body := e.GET("/admin/spend?team="+team.Slug+"&group_by="+dim, e.MasterKey)
		if code != http.StatusOK {
			t.Fatalf("group_by=%s: %d %s", dim, code, body)
		}
		var report struct {
			GroupBy   string `json:"group_by"`
			Breakdown []struct {
				Key      string `json:"key"`
				Requests int64  `json:"requests"`
			} `json:"breakdown"`
		}
		if err := json.Unmarshal(body, &report); err != nil {
			t.Fatal(err)
		}
		if report.GroupBy != dim || len(report.Breakdown) != 1 || report.Breakdown[0].Requests != 2 {
			t.Fatalf("group_by=%s: %+v", dim, report)
		}
	}
	if code, _ := e.GET("/admin/spend?group_by=planet", e.MasterKey); code != http.StatusBadRequest {
		t.Fatalf("invalid group_by accepted: %d", code)
	}
}

func TestGuardrailTestReportsEveryRule(t *testing.T) {
	e := newTestEnv(t)
	policies := []map[string]any{
		{"name": "no-codename", "type": "banned_terms", "mode": "block", "phase": "pre", "terms": []string{"project-x"}},
		{"name": "mask-email", "type": "banned_terms", "mode": "redact", "phase": "both", "terms": []string{"alice@example.com"}},
		{"name": "outbound-only", "type": "banned_terms", "mode": "flag", "phase": "post", "terms": []string{"hello"}},
	}
	code, body := e.POST("/admin/guardrails/test", e.MasterKey, map[string]any{
		"text": "hello alice@example.com", "phase": "pre", "policies": policies,
	})
	if code != http.StatusOK {
		t.Fatalf("test: %d %s", code, body)
	}
	var resp struct {
		Decision string `json:"decision"`
		Output   string `json:"output"`
		Results  []struct {
			Name, Decision string
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Decision != "redact" || resp.Output != "hello [REDACTED]" {
		t.Fatalf("redaction outcome: %+v", resp)
	}
	want := map[string]string{"no-codename": "allow", "mask-email": "redact", "outbound-only": "skipped"}
	for _, r := range resp.Results {
		if want[r.Name] != r.Decision {
			t.Fatalf("rule %s: want %s, got %s", r.Name, want[r.Name], r.Decision)
		}
	}

	code, body = e.POST("/admin/guardrails/test", e.MasterKey, map[string]any{
		"text": "about project-x and alice@example.com", "policies": policies,
	})
	if err := json.Unmarshal(body, &resp); err != nil || code != http.StatusOK {
		t.Fatalf("block test: %d %s", code, body)
	}
	if resp.Decision != "block" || resp.Output != "" {
		t.Fatalf("block outcome: %+v", resp)
	}
	_, memberToken := e.createUser("guardtest-member", false)
	if code, _ := e.POST("/admin/guardrails/test", memberToken, map[string]any{"text": "x", "policies": policies}); code != http.StatusForbidden {
		t.Fatalf("member ran guardrail test: %d", code)
	}
}

func TestDeploymentConnectionTestUnknownDeployment(t *testing.T) {
	e := newTestEnv(t)
	if code, _ := e.POST("/admin/deployments/does-not-exist/test", e.MasterKey, map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("unknown deployment: %d", code)
	}
}

// The teams directory can show each team's live keys, members and spend in
// the current budget period without one request per team.
func TestTeamDirectoryStats(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("stats-" + randHex(4))
	user, _ := e.createUser("stats-user-"+randHex(4), false)
	e.createKeyForUser(team, user)
	revoked := e.createKeyForUser(team, user)
	ctx := context.Background()
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE virtual_keys SET revoked_at = NOW() WHERE id = $1`, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE users SET team_id = $1 WHERE id = $2`, team.ID, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.Pool.Exec(ctx, `
		INSERT INTO usage_log (team_id, alias, request_id, model_requested, cost_cents, status_code, ts)
		VALUES ($1, 'a', 'now-1', 'a', 250, 200, NOW()), ($1, 'a', 'old-1', 'a', 9999, 200, NOW() - INTERVAL '400 days')`, team.ID); err != nil {
		t.Fatal(err)
	}

	code, body := e.GET("/admin/teams?stats=1&q="+url.QueryEscape(team.Slug), e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("list teams: %d %s", code, body)
	}
	rows := decodeRows(t, body)
	if len(rows) != 1 {
		t.Fatalf("teams = %d, want 1", len(rows))
	}
	stats, _ := rows[0]["stats"].(map[string]any)
	if stats["active_keys"] != float64(1) || stats["members"] != float64(1) || stats["period_spend_cents"] != float64(250) {
		t.Fatalf("stats = %v, want 1 key, 1 member, 250 cents", stats)
	}

	_, plain := e.GET("/admin/teams?q="+url.QueryEscape(team.Slug), e.MasterKey)
	if _, ok := decodeRows(t, plain)[0]["stats"]; ok {
		t.Fatal("stats must be opt-in")
	}
}

// Audit filters combine in SQL and the facets list only types that exist.
func TestAuditFilters(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	marker := "audit-" + randHex(6)
	for _, ev := range []store.AuditEvent{
		{ActorType: "master_key", ActorID: "master", Action: "team.create", ResourceType: "team", ResourceID: marker},
		{ActorType: "user", ActorID: "u-" + marker, Action: "key.revoke", ResourceType: "virtual_key", ResourceID: marker},
		{ActorType: "user", ActorID: "u-" + marker, Action: "team.update", ResourceType: "team", ResourceID: marker},
	} {
		if err := e.Store.InsertAuditEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	count := func(params string) int {
		t.Helper()
		code, body := e.GET("/admin/audit?q="+marker+params, e.MasterKey)
		if code != http.StatusOK {
			t.Fatalf("audit %s: %d %s", params, code, body)
		}
		return len(decodeRows(t, body))
	}
	if got := count(""); got != 3 {
		t.Fatalf("search = %d, want 3", got)
	}
	if got := count("&resource_type=team"); got != 2 {
		t.Fatalf("resource filter = %d, want 2", got)
	}
	if got := count("&resource_type=team&actor_type=user"); got != 1 {
		t.Fatalf("combined filter = %d, want 1", got)
	}
	code, body := e.GET("/admin/audit/facets", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("facets: %d %s", code, body)
	}
	var facets store.AuditFacets
	if err := json.Unmarshal(body, &facets); err != nil {
		t.Fatal(err)
	}
	has := func(list []string, v string) bool {
		for _, x := range list {
			if x == v {
				return true
			}
		}
		return false
	}
	if !has(facets.ResourceTypes, "virtual_key") || !has(facets.ActorTypes, "master_key") {
		t.Fatalf("facets = %+v", facets)
	}
}
