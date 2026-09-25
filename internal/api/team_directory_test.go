package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func directoryPage(t *testing.T, e *testEnv, path, token, total string, wantLen int) []map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, w.Code)
	}
	if got := w.Header().Get("X-Total-Count"); got != total {
		t.Fatalf("GET %s total %q, want %q", path, got, total)
	}
	var rows []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &rows); err != nil || rows == nil || len(rows) != wantLen {
		t.Fatalf("GET %s: invalid page (%v), rows=%d, want %d", path, err, len(rows), wantLen)
	}
	return rows
}

func TestTeamDirectoryIsolationPaginationAndProjection(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	team, _, manager := makeManager(e, "directory")
	other := e.createTeam("directory-foreign") // newer than manager's team
	member, memberToken := e.createUserWithRole("directory-member", store.RoleMember, &team.ID)
	foreign, _ := e.createUserWithRole("directory-foreign", store.RoleMember, &other.ID)
	_, orphan := e.createUserWithRole("directory-orphan", store.RoleManager, nil)
	path := "/admin/teams/" + team.Slug
	rows := directoryPage(t, e, "/admin/teams?limit=1", manager, "1", 1)
	if rows[0]["slug"] != team.Slug {
		t.Fatal("team scoping applied after pagination")
	}
	directoryPage(t, e, "/admin/teams?limit=1&offset=1", manager, "1", 0)
	directoryPage(t, e, "/admin/teams?offset=-1", manager, "1", 1)
	directoryPage(t, e, "/admin/teams", orphan, "0", 0)
	first := directoryPage(t, e, path+"/members?limit=1", manager, "2", 1)
	second := directoryPage(t, e, path+"/members?limit=1&offset=1", manager, "2", 1)
	if first[0]["id"] == second[0]["id"] {
		t.Fatal("member pagination repeated first row")
	}
	directoryPage(t, e, path+"/members?offset=999999", manager, "2", 0)
	for _, row := range append(first, second...) {
		for k := range row {
			switch k {
			case "id", "name", "email", "role", "last_login_at", "disabled_at":
			default:
				t.Fatalf("sensitive/unexpected directory field: %s", k)
			}
		}
		if row["email"] == foreign.Email {
			t.Fatal("foreign user leaked")
		}
	}
	directoryPage(t, e, path+"/members?q="+url.QueryEscape(member.Email), manager, "1", 1)
	directoryPage(t, e, path+"/members?q="+url.QueryEscape(foreign.Email), manager, "0", 0)
	directoryPage(t, e, path+"/members?q=%25", manager, "0", 0) // literal %, not a wildcard
	if code, _ := e.GET(path+"/members?q="+strings.Repeat("x", 257), manager); code != http.StatusBadRequest {
		t.Fatalf("oversize search: %d", code)
	}
	for _, resource := range []string{"members", "models"} {
		for _, token := range []string{manager, e.MasterKey} {
			if code, _ := e.GET(path+"/"+resource, token); code != http.StatusOK {
				t.Fatalf("authorized %s: %d", resource, code)
			}
		}
		for _, token := range []string{memberToken, orphan} {
			if code, _ := e.GET(path+"/"+resource, token); code != http.StatusForbidden {
				t.Fatalf("forbidden %s: %d", resource, code)
			}
		}
		if code, _ := e.GET("/admin/teams/"+other.Slug+"/"+resource, manager); code != http.StatusForbidden {
			t.Fatalf("foreign %s: %d", resource, code)
		}
		if code, _ := e.GET(path+"/"+resource, ""); code != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", resource, code)
		}
	}
	if code, _ := e.POST(path+"/keys", manager, map[string]any{"user_id": foreign.ID}); code != http.StatusBadRequest {
		t.Fatalf("foreign owner key creation: %d", code)
	}
	// Disabled accounts remain visible as disabled, archived accounts do not.
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE users SET disabled_at = now() WHERE id=$1`, member.ID); err != nil {
		t.Fatal(err)
	}
	rows = directoryPage(t, e, path+"/members?q="+url.QueryEscape(member.Email), manager, "1", 1)
	if rows[0]["disabled_at"] == nil {
		t.Fatal("disabled state omitted")
	}
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE users SET archived_at = now() WHERE id=$1`, member.ID); err != nil {
		t.Fatal(err)
	}
	directoryPage(t, e, path+"/members", manager, "1", 1)
}

func TestTeamModelCatalogAppliesPolicyBeforePagination(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	team, _, token := makeManager(e, "models")
	prefix := randHex(8)
	allowed := []string{prefix + "-z-allowed", prefix + "-zz-allowed"}
	for _, alias := range append([]string{prefix + "-a-hidden"}, allowed...) {
		if _, err := e.Store.Pool.Exec(ctx, `INSERT INTO model_aliases(alias) VALUES ($1)`, alias); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(allowed)
	if _, err := e.Store.Pool.Exec(ctx, `UPDATE teams SET allowed_models=$2::jsonb WHERE id=$1`, team.ID, raw); err != nil {
		t.Fatal(err)
	}
	path := "/admin/teams/" + team.Slug + "/models"
	for offset, alias := range allowed {
		page := path + "?limit=1"
		if offset == 1 {
			page += "&offset=1"
		}
		rows := directoryPage(t, e, page, token, "2", 1)
		if len(rows[0]) != 1 || rows[0]["alias"] != alias {
			t.Fatal("model projection/policy mismatch")
		}
	}
	directoryPage(t, e, path+"?offset=999999", token, "2", 0)
	for _, policy := range []string{`[]`, `["*"]`} {
		if _, err := e.Store.Pool.Exec(ctx, `UPDATE teams SET allowed_models=$2::jsonb WHERE id=$1`, team.ID, policy); err != nil {
			t.Fatal(err)
		}
		_, total, err := e.Store.ListTeamModels(ctx, team.ID, 1, 0)
		if err != nil || total < 3 {
			t.Fatalf("wildcard/empty policy total=%d: %v", total, err)
		}
	}
}

func TestManagerInvitesScopedBeforePagination(t *testing.T) {
	e := newTestEnv(t)
	team, _, manager := makeManager(e, "invite-page")
	other := e.createTeam("invite-page-foreign")
	_, orphan := e.createUserWithRole("invite-page-orphan", store.RoleManager, nil)
	for _, slug := range []string{team.Slug, team.Slug, other.Slug, ""} {
		if code, _ := e.POST("/admin/invites", e.MasterKey, map[string]any{"team_slug": slug, "role": "member"}); code != http.StatusCreated {
			t.Fatalf("create fixture invite: %d", code)
		}
	}
	first := directoryPage(t, e, "/admin/invites?limit=1", manager, "2", 1)
	second := directoryPage(t, e, "/admin/invites?limit=1&offset=1", manager, "2", 1)
	if first[0]["id"] == second[0]["id"] || first[0]["team_slug"] != team.Slug || second[0]["team_slug"] != team.Slug {
		t.Fatal("invite pagination/scope mismatch")
	}
	directoryPage(t, e, "/admin/invites?limit=1&offset=2", manager, "2", 0)
	directoryPage(t, e, "/admin/invites", orphan, "0", 0)
}
