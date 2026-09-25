package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// makeManager creates a team and a manager bound to it. Returns the team,
// the manager user, and the manager's session token.
func makeManager(e *testEnv, suffix string) (*store.Team, *store.User, string) {
	team := e.createTeam("mgr-" + suffix)
	user, token := e.createUserWithRole("mgr-"+suffix, store.RoleManager, &team.ID)
	return team, user, token
}

func TestRole_MemberCannotHitAdminRoutes(t *testing.T) {
	e := newTestEnv(t)
	_, memberToken := e.createUser("member", false)

	cases := []struct{ method, path string }{
		{"GET", "/admin/teams"},
		{"POST", "/admin/teams"},
		{"GET", "/admin/users"},
		{"GET", "/admin/usage"},
		{"GET", "/admin/spend"},
		{"GET", "/admin/invites"},
		{"GET", "/admin/deployments"},
		{"GET", "/admin/pricing"},
		{"GET", "/admin/aliases"},
	}
	for _, tc := range cases {
		var code int
		switch tc.method {
		case "GET":
			code, _ = e.GET(tc.path, memberToken)
		case "POST":
			code, _ = e.POST(tc.path, memberToken, map[string]string{"slug": "x"})
		}
		if code != http.StatusForbidden {
			t.Errorf("%s %s as member: got %d, want 403", tc.method, tc.path, code)
		}
	}
}

func TestRole_ManagerCannotCreateTeams(t *testing.T) {
	e := newTestEnv(t)
	_, _, mgrToken := makeManager(e, "no-create")
	code, body := e.POST("/admin/teams", mgrToken, map[string]any{"slug": "should-fail-" + randHex(4)})
	if code != http.StatusForbidden {
		t.Errorf("got %d, want 403 (body=%s)", code, body)
	}
}

func TestRole_ManagerCannotManageDeployments(t *testing.T) {
	e := newTestEnv(t)
	_, _, mgrToken := makeManager(e, "no-deploy")
	code, body := e.POST("/admin/deployments", mgrToken, map[string]any{
		"name": "x", "provider_type": "openai", "upstream_model": "gpt", "credential_ref": "X",
	})
	if code != http.StatusForbidden {
		t.Errorf("POST /admin/deployments: got %d, want 403 (body=%s)", code, body)
	}
	code, _ = e.DELETE("/admin/deployments/anything", mgrToken)
	if code != http.StatusForbidden {
		t.Errorf("DELETE /admin/deployments: got %d, want 403", code)
	}
}

func TestRole_ManagerCannotManagePricing(t *testing.T) {
	e := newTestEnv(t)
	_, _, mgrToken := makeManager(e, "no-pricing")
	code, _ := e.POST("/admin/pricing", mgrToken, map[string]any{
		"provider_type":             "openai",
		"upstream_model":            "x",
		"input_per_million_cents":   1,
		"output_per_million_cents":  1,
	})
	if code != http.StatusForbidden {
		t.Errorf("got %d, want 403", code)
	}
}

func TestRole_ManagerCanAccessOwnTeamButNotOthers(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A")
	teamB := e.createTeam("teamB")

	// Own team — allow
	if code, _ := e.GET("/admin/teams/"+teamA.Slug, mgrA); code != http.StatusOK {
		t.Errorf("manager A reading own team: got %d, want 200", code)
	}
	// Other team — deny
	if code, _ := e.GET("/admin/teams/"+teamB.Slug, mgrA); code != http.StatusForbidden {
		t.Errorf("manager A reading team B: got %d, want 403", code)
	}
}

func TestRole_ManagerCanIssueOwnTeamKeysButNotOthers(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A-keys")
	teamB := e.createTeam("teamB-keys")

	// Own team — issue
	code, body := e.POST("/admin/teams/"+teamA.Slug+"/keys", mgrA, map[string]string{"name": "by-mgr"})
	if code != http.StatusCreated {
		t.Fatalf("manager A issuing on own team: got %d, want 201 (body=%s)", code, body)
	}
	// Other team — denied
	code, _ = e.POST("/admin/teams/"+teamB.Slug+"/keys", mgrA, map[string]string{"name": "should-fail"})
	if code != http.StatusForbidden {
		t.Errorf("manager A issuing on team B: got %d, want 403", code)
	}
}

func TestRole_ManagerRevokeOnlyForOwnTeam(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A-revoke")
	teamB := e.createTeam("teamB-revoke")

	// Issue a key on team B (admin-only path — so use a master-key-like
	// flow: create directly via store, since that's what the running
	// system would do for an admin).
	bobUser, _ := e.createUser("bob-team-b", false)
	keyOnB := e.createKeyForUser(teamB, bobUser)
	keyOnA := e.createKeyForUser(teamA, bobUser) // bob isn't really in A but for the key it's fine

	// Manager A revoking their team's key — allowed
	code, body := e.POST("/admin/keys/"+strconv.FormatInt(keyOnA.ID, 10)+"/revoke", mgrA, nil)
	if code != http.StatusNoContent {
		t.Errorf("revoke own team's key: got %d, want 204 (body=%s)", code, body)
	}
	// Manager A revoking team B's key — forbidden
	code, _ = e.POST("/admin/keys/"+strconv.FormatInt(keyOnB.ID, 10)+"/revoke", mgrA, nil)
	if code != http.StatusForbidden {
		t.Errorf("revoke other team's key: got %d, want 403", code)
	}
}

func TestRole_ManagerCannotInviteAdminOrIntoOtherTeam(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A-invite")
	teamB := e.createTeam("teamB-invite")

	// Inviting role=admin → forbidden
	code, body := e.POST("/admin/invites", mgrA, map[string]any{
		"role": "admin", "team_slug": teamA.Slug,
	})
	if code != http.StatusForbidden {
		t.Errorf("manager inviting admin: got %d, want 403 (body=%s)", code, body)
	}
	// Inviting role=manager → forbidden
	code, _ = e.POST("/admin/invites", mgrA, map[string]any{
		"role": "manager", "team_slug": teamA.Slug,
	})
	if code != http.StatusForbidden {
		t.Errorf("manager inviting manager: got %d, want 403", code)
	}
	// Inviting member into other team → forbidden
	code, _ = e.POST("/admin/invites", mgrA, map[string]any{
		"role": "member", "team_slug": teamB.Slug,
	})
	if code != http.StatusForbidden {
		t.Errorf("manager inviting into other team: got %d, want 403", code)
	}
	// Inviting member into own team → allowed
	code, _ = e.POST("/admin/invites", mgrA, map[string]any{
		"role": "member", "team_slug": teamA.Slug,
	})
	if code != http.StatusCreated {
		t.Errorf("manager inviting member into own team: got %d, want 201", code)
	}
}

func TestRole_ManagerListTeamsSeesOnlyOwn(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A-list")
	_ = e.createTeam("teamB-list")

	code, body := e.GET("/admin/teams", mgrA)
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", code, body)
	}
	var teams []TeamResponse
	if err := json.Unmarshal(body, &teams); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(teams) != 1 {
		t.Fatalf("manager should see 1 team, got %d (body=%s)", len(teams), body)
	}
	if teams[0].Slug != teamA.Slug {
		t.Errorf("got team %q, want %q", teams[0].Slug, teamA.Slug)
	}
}

func TestRole_ManagerListUsageBlocksOtherTeam(t *testing.T) {
	e := newTestEnv(t)
	teamA, _, mgrA := makeManager(e, "A-usage")

	// Default (no team filter) — allowed, scoped to own team
	if code, _ := e.GET("/admin/usage", mgrA); code != http.StatusOK {
		t.Errorf("manager listing own usage: got %d, want 200", code)
	}
	// Explicit own team — allowed
	if code, _ := e.GET("/admin/usage?team="+teamA.Slug, mgrA); code != http.StatusOK {
		t.Errorf("manager listing own team explicitly: got %d, want 200", code)
	}
	// Explicit other team — forbidden
	if code, _ := e.GET("/admin/usage?team=some-other-team", mgrA); code != http.StatusForbidden {
		t.Errorf("manager listing other team usage: got %d, want 403", code)
	}
}

func TestRole_AdminUnrestricted(t *testing.T) {
	// As a regression check that admin access didn't get tightened by the
	// role rollout: an admin user should still be able to do everything.
	e := newTestEnv(t)
	_, adminToken := e.createUserWithRole("admin", store.RoleAdmin, nil)

	if code, _ := e.GET("/admin/teams", adminToken); code != http.StatusOK {
		t.Errorf("admin /admin/teams: got %d, want 200", code)
	}
	if code, _ := e.GET("/admin/users", adminToken); code != http.StatusOK {
		t.Errorf("admin /admin/users: got %d, want 200", code)
	}
	if code, _ := e.GET("/admin/usage?team=anything", adminToken); code != http.StatusOK {
		t.Errorf("admin /admin/usage with arbitrary team: got %d, want 200", code)
	}
}

func TestRole_PrincipalHelpers(t *testing.T) {
	// Pure unit-style guard: makes sure the Principal helpers behave as
	// the middleware expects, with no accidental privilege leak.
	cases := []struct {
		name        string
		p           *auth.Principal
		isAdmin     bool
		isManager   bool
		canAccessA  bool
		teamSlug    string
	}{
		{
			name:    "nil",
			p:       nil,
			isAdmin: false, isManager: false, canAccessA: false, teamSlug: "",
		},
		{
			name:    "master_key",
			p:       &auth.Principal{IsMasterKey: true},
			isAdmin: true, isManager: false, canAccessA: true, teamSlug: "",
		},
		{
			name:    "admin_user",
			p:       &auth.Principal{User: &store.User{Role: store.RoleAdmin}},
			isAdmin: true, isManager: false, canAccessA: true, teamSlug: "",
		},
		{
			name:    "manager_of_A",
			p:       &auth.Principal{User: &store.User{Role: store.RoleManager, TeamSlug: "team-A"}},
			isAdmin: false, isManager: true, canAccessA: true, teamSlug: "team-A",
		},
		{
			name:    "manager_of_other",
			p:       &auth.Principal{User: &store.User{Role: store.RoleManager, TeamSlug: "team-other"}},
			isAdmin: false, isManager: true, canAccessA: false, teamSlug: "team-other",
		},
		{
			name:    "member",
			p:       &auth.Principal{User: &store.User{Role: store.RoleMember, TeamSlug: "team-A"}},
			isAdmin: false, isManager: false, canAccessA: false, teamSlug: "team-A",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.p.IsAdmin() != tc.isAdmin {
				t.Errorf("IsAdmin = %v, want %v", tc.p.IsAdmin(), tc.isAdmin)
			}
			if tc.p.IsManager() != tc.isManager {
				t.Errorf("IsManager = %v, want %v", tc.p.IsManager(), tc.isManager)
			}
			if got := tc.p.CanAccessTeam("team-A"); got != tc.canAccessA {
				t.Errorf("CanAccessTeam(team-A) = %v, want %v", got, tc.canAccessA)
			}
			if got := tc.p.TeamSlug(); got != tc.teamSlug {
				t.Errorf("TeamSlug() = %q, want %q", got, tc.teamSlug)
			}
		})
	}
}

// guard against a future regression where the listing fields drift from
// the principal check: a user who's lost their team (team_id NULL) and is
// still a manager should not get access to anything.
func TestRole_OrphanManagerHasNoAccess(t *testing.T) {
	e := newTestEnv(t)
	user, token := e.createUserWithRole("orphan", store.RoleManager, nil)
	if user.TeamID != nil {
		t.Fatalf("expected nil team, got %v", user.TeamID)
	}
	if code, _ := e.GET("/admin/teams", token); code != http.StatusOK {
		t.Errorf("orphan manager listing teams: got %d, want 200 (empty)", code)
	}
	// Should NOT be allowed to mint invites — no team to scope into.
	code, _ := e.POST("/admin/invites", token, map[string]any{
		"role": "member",
	})
	if code != http.StatusForbidden {
		t.Errorf("orphan manager creating invite: got %d, want 403", code)
	}
}

// helper not visible outside this file — sanity that the test setup runs
// the same migrations the live server runs (so role columns exist).
func TestRole_StoreHasRoleColumn(t *testing.T) {
	e := newTestEnv(t)
	user, _ := e.createUserWithRole("col-check", store.RoleAdmin, nil)
	if user.Role != store.RoleAdmin {
		t.Errorf("expected role=admin, got %q", user.Role)
	}
	// re-fetch to confirm round-trip through the DB
	got, err := e.Store.GetUserByID(context.Background(), user.ID)
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if got.Role != store.RoleAdmin {
		t.Errorf("after round-trip: role=%q, want %q", got.Role, store.RoleAdmin)
	}
	if !got.IsAdmin {
		t.Errorf("IsAdmin should be derived true from role=admin")
	}
}
