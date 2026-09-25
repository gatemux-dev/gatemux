package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestMeRejectsMissingToken(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/me/keys", "/me/usage", "/me/budget"} {
		code, body := env.GET(path, "")
		if code != http.StatusUnauthorized {
			t.Errorf("%s without token: got %d, want 401 (body=%s)", path, code, body)
		}
	}
}

func TestMeRejectsInvalidToken(t *testing.T) {
	env := newTestEnv(t)
	code, body := env.GET("/me/keys", "sess-not-a-real-token-"+randHex(16))
	if code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401 (body=%s)", code, body)
	}
}

func TestMeRejectsMasterKey(t *testing.T) {
	// /me requires a real user record — the synthetic master-key principal
	// has no user_id to filter by, so the handler should reject it instead
	// of returning all rows.
	env := newTestEnv(t)
	for _, path := range []string{"/me/keys", "/me/usage", "/me/budget"} {
		code, body := env.GET(path, env.MasterKey)
		if code != http.StatusBadRequest {
			t.Errorf("%s with master key: got %d, want 400 (body=%s)", path, code, body)
		}
		var resp struct {
			Error struct {
				Type string `json:"type"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &resp)
		if resp.Error.Type != "no_user_session" {
			t.Errorf("%s error.type = %q, want %q", path, resp.Error.Type, "no_user_session")
		}
	}
}

func TestMeKeysOnlyShowsOwnKeys(t *testing.T) {
	env := newTestEnv(t)
	team := env.createTeam("keys-isolation")
	alice, aliceToken := env.createUser("alice", false)
	bob, _ := env.createUser("bob", false)

	aliceKey := env.createKeyForUser(team, alice)
	bobKey := env.createKeyForUser(team, bob)

	code, body := env.GET("/me/keys", aliceToken)
	if code != http.StatusOK {
		t.Fatalf("got %d (body=%s)", code, body)
	}
	var keys []MyKeyResponse
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatalf("unmarshal keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("alice should see exactly her key, got %d (body=%s)", len(keys), body)
	}
	if keys[0].ID != aliceKey.ID {
		t.Errorf("got key id %d, want %d", keys[0].ID, aliceKey.ID)
	}
	for _, k := range keys {
		if k.ID == bobKey.ID {
			t.Errorf("alice can see bob's key id %d", k.ID)
		}
	}
}

func TestMeUsageOnlyShowsOwnUsage(t *testing.T) {
	env := newTestEnv(t)
	team := env.createTeam("usage-isolation")
	alice, aliceToken := env.createUser("alice", false)
	bob, _ := env.createUser("bob", false)

	aliceKey := env.createKeyForUser(team, alice)
	bobKey := env.createKeyForUser(team, bob)

	env.insertUsageRow(team, alice, aliceKey, "test-alpha")
	env.insertUsageRow(team, alice, aliceKey, "test-alpha")
	env.insertUsageRow(team, bob, bobKey, "test-beta")

	code, body := env.GET("/me/usage", aliceToken)
	if code != http.StatusOK {
		t.Fatalf("got %d (body=%s)", code, body)
	}
	var rows []MyUsageRow
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("unmarshal usage: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("alice should see her 2 rows, got %d", len(rows))
	}
	for _, r := range rows {
		if r.Alias == "test-beta" {
			t.Errorf("alice can see bob's usage row (alias=%s)", r.Alias)
		}
	}
}

func TestMeBudgetReturnsConfiguredLimit(t *testing.T) {
	env := newTestEnv(t)
	user, token := env.createUser("budget", false)

	limit := int64(1234) // $12.34
	if _, err := env.Store.UpdateUserBudget(context.Background(), user.ID, &limit, "month"); err != nil {
		t.Fatalf("update user budget: %v", err)
	}

	code, body := env.GET("/me/budget", token)
	if code != http.StatusOK {
		t.Fatalf("got %d (body=%s)", code, body)
	}
	var resp MyBudgetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.LimitCents == nil || *resp.LimitCents != limit {
		got := int64(0)
		if resp.LimitCents != nil {
			got = *resp.LimitCents
		}
		t.Errorf("limit_cents = %d, want %d", got, limit)
	}
	if resp.Period != "month" {
		t.Errorf("period = %q, want %q", resp.Period, "month")
	}
	if resp.WindowEnd.Before(resp.WindowStart) {
		t.Errorf("window_end (%s) before window_start (%s)", resp.WindowEnd, resp.WindowStart)
	}
}

func TestMeBudgetReturnsNoLimitWhenUnconfigured(t *testing.T) {
	env := newTestEnv(t)
	_, token := env.createUser("nobudget", false)

	code, body := env.GET("/me/budget", token)
	if code != http.StatusOK {
		t.Fatalf("got %d (body=%s)", code, body)
	}
	var resp MyBudgetResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.LimitCents != nil {
		t.Errorf("limit_cents = %d, want nil", *resp.LimitCents)
	}
	if resp.UsedCents != 0 {
		t.Errorf("used_cents = %d, want 0", resp.UsedCents)
	}
}
