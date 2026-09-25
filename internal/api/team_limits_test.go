package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// Rates use the same null-or-zero-clears semantics as concurrency; negative
// values are rejected before any write.
func TestUpdateTeamRatesRoundTripAndValidation(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("rates")
	path := fmt.Sprintf("/admin/teams/%s/rates", team.Slug)

	code, body := e.PATCH(path, e.MasterKey, map[string]any{"rpm": 120, "tpm": 90000})
	if code != http.StatusOK {
		t.Fatalf("set rates: %d %s", code, body)
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["rpm"] != float64(120) || resp["tpm"] != float64(90000) {
		t.Fatalf("rates not persisted in response: %v", resp)
	}
	fresh, err := e.Store.GetTeamBySlug(t.Context(), team.Slug)
	if err != nil || fresh.RPM == nil || *fresh.RPM != 120 || fresh.TPM == nil || *fresh.TPM != 90000 {
		t.Fatalf("rates not persisted in store: %+v err=%v", fresh, err)
	}

	// Zero clears a cap; the other one can be set in the same call.
	code, body = e.PATCH(path, e.MasterKey, map[string]any{"rpm": 0, "tpm": 500})
	if code != http.StatusOK {
		t.Fatalf("clear rpm: %d %s", code, body)
	}
	fresh, _ = e.Store.GetTeamBySlug(t.Context(), team.Slug)
	if fresh.RPM != nil || fresh.TPM == nil || *fresh.TPM != 500 {
		t.Fatalf("expected cleared rpm and tpm=500: %+v", fresh)
	}

	if code, _ = e.PATCH(path, e.MasterKey, map[string]any{"rpm": -1}); code != http.StatusBadRequest {
		t.Fatalf("negative rpm accepted: %d", code)
	}
	if code, _ = e.PATCH("/admin/teams/absent-team/rates", e.MasterKey, map[string]any{"rpm": 1}); code != http.StatusNotFound {
		t.Fatalf("missing team: %d", code)
	}
}

// Managers may tune their own team's rates (same class as budget/concurrency)
// but never another team's.
func TestUpdateTeamRatesManagerScoping(t *testing.T) {
	e := newTestEnv(t)
	team, _, manager := makeManager(e, "rates-mgr")
	foreign := e.createTeam("rates-foreign")

	code, body := e.PATCH(fmt.Sprintf("/admin/teams/%s/rates", team.Slug), manager, map[string]any{"rpm": 30})
	if code != http.StatusOK {
		t.Fatalf("manager own-team rates: %d %s", code, body)
	}
	if code, _ = e.PATCH(fmt.Sprintf("/admin/teams/%s/rates", foreign.Slug), manager, map[string]any{"rpm": 30}); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("manager set foreign team rates: %d", code)
	}
}

// The allowlist replaces atomically, normalizes to ["*"] when the wildcard is
// present, rejects empty lists, and is admin-only.
func TestUpdateTeamAllowedModelsValidationAndScoping(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("models")
	_, _, manager := makeManager(e, "models-mgr")
	path := fmt.Sprintf("/admin/teams/%s/models", team.Slug)

	code, body := e.PUT(path, e.MasterKey, map[string]any{"allowed_models": []string{"gpt-4", "claude", "gpt-4"}})
	if code != http.StatusOK {
		t.Fatalf("set allowlist: %d %s", code, body)
	}
	fresh, err := e.Store.GetTeamBySlug(t.Context(), team.Slug)
	if err != nil || len(fresh.AllowedModels) != 2 {
		t.Fatalf("expected deduplicated 2-entry allowlist: %+v err=%v", fresh.AllowedModels, err)
	}
	if !fresh.AllowsModel("gpt-4") || fresh.AllowsModel("other") {
		t.Fatalf("allowlist not enforced: %v", fresh.AllowedModels)
	}

	if code, _ = e.PUT(path, e.MasterKey, map[string]any{"allowed_models": []string{"a", "*"}}); code != http.StatusOK {
		t.Fatalf("wildcard set: %d", code)
	}
	fresh, _ = e.Store.GetTeamBySlug(t.Context(), team.Slug)
	if len(fresh.AllowedModels) != 1 || fresh.AllowedModels[0] != "*" {
		t.Fatalf("wildcard should collapse to [*]: %v", fresh.AllowedModels)
	}

	if code, _ = e.PUT(path, e.MasterKey, map[string]any{"allowed_models": []string{}}); code != http.StatusBadRequest {
		t.Fatalf("empty allowlist accepted: %d", code)
	}
	if code, _ = e.PUT(path, manager, map[string]any{"allowed_models": []string{"x"}}); code != http.StatusForbidden {
		t.Fatalf("manager edited allowlist: %d", code)
	}
}

func TestListTeamsSearchFiltersSlugAndName(t *testing.T) {
	e := newTestEnv(t)
	needle := e.createTeam("searchneedle")
	e.createTeam("searchother")

	// The random slug suffix is unique per run, so matching on it proves both
	// the filter and the exclusion of non-matching teams.
	code, body := e.GET("/admin/teams?q="+needle.Slug[len(needle.Slug)-8:], e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("search: %d %s", code, body)
	}
	var teams []map[string]any
	if err := json.Unmarshal(body, &teams); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(teams) != 1 || teams[0]["slug"] != needle.Slug {
		t.Fatalf("search should return exactly the needle team: %s", body)
	}

	// LIKE metacharacters match literally instead of as wildcards.
	code, body = e.GET("/admin/teams?q=%25%25", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("literal search: %d", code)
	}
	if err := json.Unmarshal(body, &teams); err != nil || len(teams) != 0 {
		t.Fatalf("%%%% should match nothing: %s", body)
	}
}

// The assignments listing surfaces every scope with a policy so the console
// can show the whole enforcement surface at once.
func TestGuardrailAssignmentsListing(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("guard-list")
	_, memberToken := e.createUser("guard-list-member", false)

	policy := []map[string]any{{
		"name": "banned-terms", "type": "banned_terms", "mode": "block",
		"phase": "pre", "terms": []string{"secret-codename"},
	}}
	code, body := e.PUT(fmt.Sprintf("/admin/guardrails/team/%s", team.Slug), e.MasterKey,
		map[string]any{"expected": []any{}, "policies": policy})
	if code != http.StatusOK {
		t.Fatalf("assign guardrail: %d %s", code, body)
	}

	code, body = e.GET("/admin/guardrails/assignments", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("list assignments: %d %s", code, body)
	}
	var rows []store.GuardrailAssignment
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, row := range rows {
		if row.ScopeType == "team" && row.ScopeID == team.Slug {
			found = true
			var policies []map[string]any
			if err := json.Unmarshal(row.Policies, &policies); err != nil || len(policies) != 1 {
				t.Fatalf("assignment policies malformed: %s", row.Policies)
			}
		}
	}
	if !found {
		t.Fatalf("assigned team missing from listing: %s", body)
	}

	if code, _ = e.GET("/admin/guardrails/assignments", memberToken); code != http.StatusForbidden {
		t.Fatalf("member read assignments: %d", code)
	}
}
