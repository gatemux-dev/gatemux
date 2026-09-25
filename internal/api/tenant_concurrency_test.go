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

func TestAdminTeamAndKeyConcurrencyRoundTripClearAndRotation(t *testing.T) {
	e := newTestEnv(t)
	slug := "concurrency-" + randHex(4)

	code, body := e.POST("/admin/teams", e.MasterKey, map[string]any{
		"slug": slug, "name": "Concurrency", "max_parallel_requests": 4,
	})
	if code != http.StatusCreated {
		t.Fatalf("create team got %d, want 201 (body=%s)", code, body)
	}
	var team TeamResponse
	if err := json.Unmarshal(body, &team); err != nil {
		t.Fatal(err)
	}
	if team.MaxParallelRequests == nil || *team.MaxParallelRequests != 4 {
		t.Fatalf("team max_parallel_requests = %v, want 4", team.MaxParallelRequests)
	}

	code, body = e.PATCH("/admin/teams/"+slug+"/concurrency", e.MasterKey, map[string]any{
		"max_parallel_requests": 0,
	})
	if code != http.StatusOK {
		t.Fatalf("clear team concurrency got %d, want 200 (body=%s)", code, body)
	}
	team = TeamResponse{} // omitted JSON fields must not retain the create response
	if err := json.Unmarshal(body, &team); err != nil {
		t.Fatal(err)
	}
	if team.MaxParallelRequests != nil {
		t.Fatalf("cleared team max_parallel_requests = %v, want nil", team.MaxParallelRequests)
	}
	code, body = e.PATCH("/admin/teams/"+slug+"/concurrency", e.MasterKey, map[string]any{
		"max_parallel_requests": -1,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("negative team concurrency got %d, want 400 (body=%s)", code, body)
	}

	code, body = e.POST("/admin/teams/"+slug+"/keys", e.MasterKey, map[string]any{
		"name": "limited", "max_parallel_requests": 2,
	})
	if code != http.StatusCreated {
		t.Fatalf("create key got %d, want 201 (body=%s)", code, body)
	}
	var issued CreateKeyResponse
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatal(err)
	}

	code, body = e.GET("/admin/teams/"+slug+"/keys", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("list keys got %d, want 200 (body=%s)", code, body)
	}
	var keys []KeyResponse
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	var key KeyResponse
	for _, candidate := range keys {
		if candidate.Prefix == issued.Prefix {
			key = candidate
			break
		}
	}
	if key.ID == 0 || key.MaxParallelRequests == nil || *key.MaxParallelRequests != 2 {
		t.Fatalf("created key concurrency missing: %+v", key)
	}

	code, body = e.PATCH("/admin/keys/"+strconv.FormatInt(key.ID, 10), e.MasterKey, map[string]any{
		"name": key.Name, "metadata": key.Metadata, "allowed_models": key.AllowedModels,
		"max_parallel_requests": 3,
	})
	if code != http.StatusOK {
		t.Fatalf("update key got %d, want 200 (body=%s)", code, body)
	}
	var updated KeyResponse
	if err := json.Unmarshal(body, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.MaxParallelRequests == nil || *updated.MaxParallelRequests != 3 {
		t.Fatalf("updated key max_parallel_requests = %v, want 3", updated.MaxParallelRequests)
	}

	code, body = e.POST("/admin/keys/"+strconv.FormatInt(key.ID, 10)+"/rotate", e.MasterKey, nil)
	if code != http.StatusCreated {
		t.Fatalf("rotate key got %d, want 201 (body=%s)", code, body)
	}
	var rotated CreateKeyResponse
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatal(err)
	}
	code, body = e.GET("/admin/teams/"+slug+"/keys", e.MasterKey)
	if code != http.StatusOK {
		t.Fatalf("list rotated keys got %d (body=%s)", code, body)
	}
	keys = nil
	if err := json.Unmarshal(body, &keys); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range keys {
		if candidate.Prefix == rotated.Prefix {
			if candidate.MaxParallelRequests == nil || *candidate.MaxParallelRequests != 3 {
				t.Fatalf("rotation did not preserve key concurrency: %+v", candidate)
			}
			return
		}
	}
	t.Fatal("rotated key not found")
}

func TestAdminPrincipalConcurrencyAndOwnerLookup(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("principal-concurrency")
	user, _ := e.createUserWithRole("principal-concurrency", store.RoleMember, &team.ID)

	code, body := e.PATCH("/admin/users/"+strconv.FormatInt(user.ID, 10)+"/concurrency", e.MasterKey, map[string]any{
		"max_parallel_requests": 2,
	})
	if code != http.StatusOK {
		t.Fatalf("update user concurrency got %d, want 200 (body=%s)", code, body)
	}
	var userResponse UserResponse
	if err := json.Unmarshal(body, &userResponse); err != nil {
		t.Fatal(err)
	}
	if userResponse.MaxParallelRequests == nil || *userResponse.MaxParallelRequests != 2 {
		t.Fatalf("user max_parallel_requests = %v, want 2", userResponse.MaxParallelRequests)
	}

	code, body = e.POST("/admin/teams/"+team.Slug+"/keys", e.MasterKey, map[string]any{
		"name": "user-owned", "user_id": user.ID,
	})
	if code != http.StatusCreated {
		t.Fatalf("create user key got %d, want 201 (body=%s)", code, body)
	}
	var userKey CreateKeyResponse
	if err := json.Unmarshal(body, &userKey); err != nil {
		t.Fatal(err)
	}
	vk, _, owner, err := e.Store.LookupKey(context.Background(), auth.HashKey(userKey.Key))
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.MaxParallelRequests == nil || *owner.MaxParallelRequests != 2 {
		t.Fatalf("user owner concurrency was not loaded: %+v", owner)
	}
	if vk.OwnerMaxParallelRequests == nil || *vk.OwnerMaxParallelRequests != 2 {
		t.Fatalf("key owner concurrency = %v, want 2", vk.OwnerMaxParallelRequests)
	}

	code, body = e.POST("/admin/teams/"+team.Slug+"/service-accounts", e.MasterKey, map[string]any{
		"name": "worker", "max_parallel_requests": 3,
	})
	if code != http.StatusCreated {
		t.Fatalf("create service account got %d, want 201 (body=%s)", code, body)
	}
	var serviceAccount ServiceAccountResponse
	if err := json.Unmarshal(body, &serviceAccount); err != nil {
		t.Fatal(err)
	}
	if serviceAccount.MaxParallelRequests == nil || *serviceAccount.MaxParallelRequests != 3 {
		t.Fatalf("service account max_parallel_requests = %v, want 3", serviceAccount.MaxParallelRequests)
	}

	code, body = e.PATCH("/admin/service-accounts/"+strconv.FormatInt(serviceAccount.ID, 10)+"/concurrency", e.MasterKey, map[string]any{
		"max_parallel_requests": 4,
	})
	if code != http.StatusOK {
		t.Fatalf("update service account concurrency got %d, want 200 (body=%s)", code, body)
	}
	if err := json.Unmarshal(body, &serviceAccount); err != nil {
		t.Fatal(err)
	}
	if serviceAccount.MaxParallelRequests == nil || *serviceAccount.MaxParallelRequests != 4 {
		t.Fatalf("updated service account max_parallel_requests = %v, want 4", serviceAccount.MaxParallelRequests)
	}

	code, body = e.POST("/admin/service-accounts/"+strconv.FormatInt(serviceAccount.ID, 10)+"/keys", e.MasterKey, map[string]any{
		"name": "service-owned",
	})
	if code != http.StatusCreated {
		t.Fatalf("create service account key got %d, want 201 (body=%s)", code, body)
	}
	var serviceKey CreateKeyResponse
	if err := json.Unmarshal(body, &serviceKey); err != nil {
		t.Fatal(err)
	}
	vk, _, owner, err = e.Store.LookupKey(context.Background(), auth.HashKey(serviceKey.Key))
	if err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		t.Fatalf("service account key unexpectedly has user owner: %+v", owner)
	}
	if vk.ServiceAccountID == nil || *vk.ServiceAccountID != serviceAccount.ID {
		t.Fatalf("service account id = %v, want %d", vk.ServiceAccountID, serviceAccount.ID)
	}
	if vk.OwnerMaxParallelRequests == nil || *vk.OwnerMaxParallelRequests != 4 {
		t.Fatalf("service owner concurrency = %v, want 4", vk.OwnerMaxParallelRequests)
	}
	if err := e.Store.SetKeyAllowedCIDRs(context.Background(), vk.ID, []string{"10.0.0.0/8"}); err != nil {
		t.Fatal(err)
	}
	code, body = e.PATCH("/admin/keys/"+strconv.FormatInt(vk.ID, 10), e.MasterKey, map[string]any{
		"name": "service-owned", "allowed_models": []string{"*"},
		"max_parallel_requests": 5, "usd_limit_cents": 123,
	})
	if code != http.StatusOK {
		t.Fatalf("update service key policy got %d, want 200 (body=%s)", code, body)
	}
	code, body = e.POST("/admin/keys/"+strconv.FormatInt(vk.ID, 10)+"/rotate", e.MasterKey, nil)
	if code != http.StatusCreated {
		t.Fatalf("rotate service key got %d, want 201 (body=%s)", code, body)
	}
	var rotatedServiceKey CreateKeyResponse
	if err := json.Unmarshal(body, &rotatedServiceKey); err != nil {
		t.Fatal(err)
	}
	rotatedVK, _, _, err := e.Store.LookupKey(context.Background(), auth.HashKey(rotatedServiceKey.Key))
	if err != nil {
		t.Fatal(err)
	}
	if rotatedVK.ServiceAccountID == nil || *rotatedVK.ServiceAccountID != serviceAccount.ID {
		t.Fatalf("rotation lost service owner: %+v", rotatedVK)
	}
	if rotatedVK.MaxParallelRequests == nil || *rotatedVK.MaxParallelRequests != 5 {
		t.Fatalf("rotation lost concurrency policy: %+v", rotatedVK)
	}
	if rotatedVK.ScopedUsdLimitCents == nil || *rotatedVK.ScopedUsdLimitCents != 123 {
		t.Fatalf("rotation lost budget policy: %+v", rotatedVK)
	}
	if len(rotatedVK.AllowedCIDRs) != 1 || rotatedVK.AllowedCIDRs[0] != "10.0.0.0/8" {
		t.Fatalf("rotation lost CIDR policy: %+v", rotatedVK.AllowedCIDRs)
	}
}
