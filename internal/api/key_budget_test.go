package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5"
)

func issueBudgetKey(t *testing.T, e *testEnv, team *store.Team, cap any) (string, *store.VirtualKey) {
	t.Helper()
	code, body := e.POST("/admin/teams/"+team.Slug+"/keys", e.MasterKey, map[string]any{
		"name": "budget-key", "usd_limit_cents": cap, "rpm": 12000, "tpm": 1000000,
		"max_parallel_requests": 20, "metadata": map[string]string{"label": "preserve"},
		"allowed_models": []string{"*"}, "expires_at": time.Now().UTC().Add(time.Hour),
	})
	if code != 201 {
		t.Fatalf("issue budget key: %d %s", code, body)
	}
	var issued CreateKeyResponse
	if json.Unmarshal(body, &issued) != nil {
		t.Fatal("invalid issue response")
	}
	key, _, _, err := e.Store.LookupKey(context.Background(), auth.HashKey(issued.Key))
	if err != nil {
		t.Fatal(err)
	}
	return issued.Key, key
}

func TestKeyBudgetAPIValidationIsolationAndPreservation(t *testing.T) {
	e := newTestEnv(t)
	team, foreign := e.createTeam("key-budget"), e.createTeam("foreign-budget")
	_, manager := e.createUserWithRole("key-budget-manager", store.RoleManager, &team.ID)
	_, other := e.createUserWithRole("foreign-budget-manager", store.RoleManager, &foreign.ID)
	_, member := e.createUserWithRole("key-budget-member", store.RoleMember, &team.ID)
	raw, key := issueBudgetKey(t, e, team, 500)
	if key.ScopedUsdLimitCents == nil || *key.ScopedUsdLimitCents != 500 {
		t.Fatal("create did not persist budget")
	}
	path := fmt.Sprintf("/admin/keys/%d/budget", key.ID)
	for _, token := range []string{other, member, raw} {
		for _, method := range []string{"GET", "PATCH"} {
			var code int
			if method == "GET" {
				code, _ = e.GET(path, token)
			} else {
				code, _ = e.PATCH(path, token, map[string]any{"usd_limit_cents": nil})
			}
			if code != 403 && code != 401 {
				t.Fatalf("unauthorized %s: %d", method, code)
			}
		}
	}
	for _, invalid := range []any{-1, 0.1, "123", true, json.RawMessage("9007199254740992"), json.RawMessage("9223372036854775808")} {
		for _, target := range []string{"create", "edit", "budget"} {
			body := map[string]any{"usd_limit_cents": invalid}
			var code int
			switch target {
			case "create":
				code, _ = e.POST("/admin/teams/"+team.Slug+"/keys", manager, body)
			case "edit":
				code, _ = e.PATCH(fmt.Sprintf("/admin/keys/%d", key.ID), manager, body)
			case "budget":
				code, _ = e.PATCH(path, manager, body)
			}
			if code != 400 {
				t.Fatalf("%s accepted invalid cap: %d", target, code)
			}
		}
	}
	for _, body := range []string{`{}`, `null`, `{"usd_limit_cents":12,"name":"replace"}`, `{"usd_limit_cents":12} {}`, strings.Repeat(" ", 4097) + `{"usd_limit_cents":12}`} {
		r := httptest.NewRequest("PATCH", path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+manager)
		w := httptest.NewRecorder()
		e.Router.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("malformed budget body accepted: %d", w.Code)
		}
	}
	for _, limit := range []any{250, 0, nil, store.MaxKeyBudgetCents, 100} {
		if code, b := e.PATCH(path, manager, map[string]any{"usd_limit_cents": limit}); code != 204 {
			t.Fatalf("budget patch: %d %s", code, b)
		}
		updated, _, err := e.Store.GetVirtualKeyByID(context.Background(), key.ID)
		if err != nil {
			t.Fatal(err)
		}
		key.ScopedUsdLimitCents = updated.ScopedUsdLimitCents
		if updated.Name != key.Name || !reflect.DeepEqual(updated.AllowedModels, key.AllowedModels) || !reflect.DeepEqual(updated.Metadata, key.Metadata) || *updated.ScopedRPM != *key.ScopedRPM || *updated.ScopedTPM != *key.ScopedTPM || *updated.MaxParallelRequests != *key.MaxParallelRequests || !updated.ExpiresAt.Equal(*key.ExpiresAt) {
			t.Fatal("budget-only edit replaced unrelated settings")
		}
		code, b := e.GET(path, manager)
		var summary budget.UserSummary
		if code != 200 || json.Unmarshal(b, &summary) != nil || summary.Period != "month" || summary.UsedCents != 0 || !reflect.DeepEqual(summary.LimitCents, updated.ScopedUsdLimitCents) {
			t.Fatalf("summary: %d %s", code, b)
		}
		if strings.Contains(string(b), raw) {
			t.Fatal("summary leaked secret")
		}
	}
	if code, _ := e.POST(fmt.Sprintf("/admin/keys/%d/revoke", key.ID), manager, nil); code != 204 {
		t.Fatalf("revoke: %d", code)
	}
	if code, _ := e.PATCH(path, manager, map[string]any{"usd_limit_cents": nil}); code != 404 {
		t.Fatal("revoked key budget edited")
	}
	if code, _ := e.GET(path, manager); code != 200 {
		t.Fatal("revoked budget history hidden")
	}
	var audits int
	if err := e.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log WHERE action='key.budget' AND resource_id=$1`, fmt.Sprint(key.ID)).Scan(&audits); err != nil || audits != 5 {
		t.Fatalf("budget audits: %d %v", audits, err)
	}
}

func TestKeyBudgetExhaustionFreshEditsAndUnpricedDenialBeforeUpstream(t *testing.T) {
	f := newChatFixture(t)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"budget","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	team := f.createTeam("key-budget-chat")
	raw, key := issueBudgetKey(t, f.testEnv, team, 1)
	path := fmt.Sprintf("/admin/keys/%d/budget", key.ID)
	request := func(token string) *httptest.ResponseRecorder {
		return responseHTTP(f, token, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, f.Alias))
	}
	if w := request(raw); w.Code != 503 || calls.Load() != 0 {
		t.Fatalf("unpriced key budget reached upstream: %d %s", w.Code, w.Body)
	}
	f.upsertPricing(100, 0) // input rounds to one cent; output is explicitly free
	if w := request(raw); w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("first call: %d %s", w.Code, w.Body)
	}
	if w := request(raw); w.Code != 403 || !strings.Contains(w.Body.String(), "insufficient_quota") || calls.Load() != 1 {
		t.Fatalf("exhaustion escaped: %d %s", w.Code, w.Body)
	}
	if code, _ := f.testEnv.PATCH(path, f.MasterKey, map[string]any{"usd_limit_cents": 2}); code != 204 {
		t.Fatal("raise cap failed")
	}
	if w := request(raw); w.Code != 200 || calls.Load() != 2 {
		t.Fatalf("fresh raised cap: %d %s", w.Code, w.Body)
	}
	code, b := f.testEnv.GET(path, f.MasterKey)
	var summary budget.UserSummary
	if code != 200 || json.Unmarshal(b, &summary) != nil || summary.UsedCents != 2 {
		t.Fatalf("settlement summary: %d %s", code, b)
	}
	if code, _ := f.testEnv.PATCH(path, f.MasterKey, map[string]any{"usd_limit_cents": 1}); code != 204 {
		t.Fatal("lower cap")
	}
	if w := request(raw); w.Code != 403 || calls.Load() != 2 {
		t.Fatal("lowered cap reset spend")
	}
	otherRaw, _ := issueBudgetKey(t, f.testEnv, team, 1)
	if w := request(otherRaw); w.Code != 200 || calls.Load() != 3 {
		t.Fatal("one key consumed another key's cap")
	}
	// Clearing a key cap cannot remove an exhausted parent budget.
	f.setTeamBudget(team, 1)
	if code, _ := f.testEnv.PATCH(path, f.MasterKey, map[string]any{"usd_limit_cents": nil}); code != 204 {
		t.Fatal("clear cap")
	}
	if w := request(raw); w.Code != 403 || calls.Load() != 3 {
		t.Fatal("clearing key budget bypassed team budget")
	}
}

func TestKeyBudgetConcurrentReservationsVisibleAndBounded(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(100, 0)
	team := f.createTeam("key-budget-race")
	raw, key := issueBudgetKey(t, f.testEnv, team, 1)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"budget","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer mock.Close()
	var once sync.Once
	defer once.Do(func() { close(release) })
	setStreamUpstream(t, f, mock.URL, "openai")
	request := func() *httptest.ResponseRecorder {
		return responseHTTP(f, raw, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"max_tokens":16}`, f.Alias))
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request() }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("first call never reached provider")
	}
	code, body := f.testEnv.GET(fmt.Sprintf("/admin/keys/%d/budget", key.ID), f.MasterKey)
	var summary budget.UserSummary
	if code != 200 || json.Unmarshal(body, &summary) != nil || summary.UsedCents != 1 {
		t.Fatalf("pending reservation invisible: %d %s", code, body)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := request(); w.Code != 403 || !strings.Contains(w.Body.String(), "insufficient_quota") {
				t.Errorf("concurrent admission escaped: %d", w.Code)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("overspent key budget")
	}
	once.Do(func() { close(release) })
	if w := <-done; w.Code != 200 {
		t.Fatalf("first admission failed: %d", w.Code)
	}
}

func TestKeyBudgetRejectsUnpricedRoutesAndBoundsOutput(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(100, 0)
	team := f.createTeam("key-budget-unsupported")
	raw, _ := issueBudgetKey(t, f.testEnv, team, 100)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["max_completion_tokens"] != float64(1024) {
			t.Error("missing bounded output forwarded to upstream")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"budget","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	for _, extra := range []string{`"audio":{}`, `"modalities":["text","audio"]`, `"web_search_options":{}`, `"prediction":{}`, `"service_tier":"priority"`} {
		w := responseHTTP(f, raw, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],%s}`, f.Alias, extra))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "key_accounting_unsupported") || calls.Load() != 0 {
			t.Fatalf("unpriced dimension escaped: %d %s", w.Code, w.Body)
		}
	}
	for _, path := range []string{"/v1/images/generations", "/v1/messages"} {
		w := responseHTTP(f, raw, "POST", path, fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"prompt":"hi"}`, f.Alias))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "key_accounting_unsupported") || calls.Load() != 0 {
			t.Fatalf("unpriced endpoint escaped: %d %s", w.Code, w.Body)
		}
	}
	// Generic passthrough must reject capped keys before loading/forwarding a route.
	opaque := chi.NewRouter()
	opaque.Use(auth.Bearer(f.Store, nil))
	opaque.HandleFunc("/passthrough/{name}/*", f.Handler.GenericPassthrough)
	r := httptest.NewRequest("POST", "/passthrough/not-configured/anything", strings.NewReader(`{}`))
	r.Header.Set("Authorization", "Bearer "+raw)
	w := httptest.NewRecorder()
	opaque.ServeHTTP(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "key_accounting_unsupported") || calls.Load() != 0 {
		t.Fatalf("opaque endpoint escaped budget: %d %s", w.Code, w.Body)
	}
	if code, b := f.chatPOST(raw); code != 200 || calls.Load() != 1 {
		t.Fatalf("default bounded output: %d %s", code, b)
	}
}

func TestServiceAccountKeyBudgetCreationAndValidation(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("sa-key-budget")
	sa, err := e.Store.CreateServiceAccount(context.Background(), store.CreateServiceAccountParams{TeamID: team.ID, Name: "budget-ci"})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/service-accounts/%d/keys", sa.ID)
	if code, _ := e.POST(path, e.MasterKey, map[string]any{"usd_limit_cents": -1}); code != 400 {
		t.Fatal("negative service-account key budget accepted")
	}
	code, body := e.POST(path, e.MasterKey, map[string]any{"usd_limit_cents": 125})
	var result CreateKeyResponse
	if code != 201 || json.Unmarshal(body, &result) != nil {
		t.Fatalf("create service-account key: %d %s", code, body)
	}
	key, _, _, err := e.Store.LookupKey(context.Background(), auth.HashKey(result.Key))
	if err != nil || key.ScopedUsdLimitCents == nil || *key.ScopedUsdLimitCents != 125 || key.ServiceAccountID == nil || *key.ServiceAccountID != sa.ID {
		t.Fatal("service-account key lost cap or identity")
	}
}

func TestKeyBudgetIncludesUsageBeforeCapAndFailsClosedOnReadFailure(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(100, 0)
	team := f.createTeam("key-budget-history")
	raw, key := issueBudgetKey(t, f.testEnv, team, nil)
	if err := f.Store.InsertUsage(context.Background(), store.UsageEntry{TeamID: team.ID, KeyID: &key.ID, RequestID: "pre-cap-" + randHex(8), Alias: f.Alias, CostCents: 25, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/admin/keys/%d/budget", key.ID)
	for _, cap := range []any{25, nil, 25} {
		if code, _ := f.testEnv.PATCH(path, f.MasterKey, map[string]any{"usd_limit_cents": cap}); code != 204 {
			t.Fatal("edit cap")
		}
		code, b := f.testEnv.GET(path, f.MasterKey)
		var summary budget.UserSummary
		if code != 200 || json.Unmarshal(b, &summary) != nil || summary.UsedCents != 25 {
			t.Fatal("historical usage lost or cleared")
		}
	}
	if code, b := f.chatPOST(raw); code != 403 || !strings.Contains(string(b), "insufficient_quota") {
		t.Fatalf("earlier unreserved usage bypassed new cap: %d %s", code, b)
	}
	// An unavailable policy/usage read must not masquerade as a zero-used summary.
	f.Store.Pool.Close()
	if code, b := f.testEnv.GET(path, f.MasterKey); code != 503 || strings.Contains(string(b), "used_cents") {
		t.Fatalf("unavailable summary: %d %s", code, b)
	}
}
