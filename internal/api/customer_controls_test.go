package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestCustomerPricingDimensionsFailClosedBeforeIO(t *testing.T) {
	f := newChatFixture(t)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	team := f.createTeam("customer-unpriced")
	key := f.issueKey(team, nil)
	limit := int64(100)
	if _, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "priced", UsdLimitCents: &limit}); err != nil {
		t.Fatal(err)
	}
	for _, tail := range []string{`"audio":{}`, `"modalities":["text","audio"]`, `"web_search_options":{}`, `"prediction":{}`, `"service_tier":"priority"`, `"service_tier":"auto"`} {
		w := responseHTTP(f, key, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"user":"priced","messages":[{"role":"user","content":"hi"}],%s}`, f.Alias, tail))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "customer_accounting_unsupported") || calls.Load() != 0 {
			t.Fatalf("unsupported charge escaped: %d %s", w.Code, w.Body)
		}
	}
	for _, path := range []string{"/v1/images/generations", "/v1/messages"} {
		w := responseHTTP(f, key, "POST", path, fmt.Sprintf(`{"model":%q,"user":"priced","prompt":"hi"}`, f.Alias))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "customer_accounting_unsupported") || calls.Load() != 0 {
			t.Fatalf("unpriced endpoint escaped: %d %s", w.Code, w.Body)
		}
	}
}

func TestCustomerStreamWithoutUsageRetainsEstimate(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			f := newChatFixture(t)
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
				if terminal {
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}
			}))
			defer mock.Close()
			setStreamUpstream(t, f, mock.URL, "openai")
			f.upsertPricing(100, 200)
			team := f.createTeam("customer-stream-estimate")
			key := f.issueKey(team, nil)
			limit := int64(100)
			if _, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "priced", UsdLimitCents: &limit}); err != nil {
				t.Fatal(err)
			}
			id := "customer-stream-" + randHex(8)
			r := streamRequest(f, key)
			r.Header.Set("X-Request-Id", id)
			r.Header.Set("X-Customer-Id", "priced")
			w := httptest.NewRecorder()
			f.Router.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("stream: %d %s", w.Code, w.Body)
			}
			var estimated, settled int64
			var status string
			err := f.Store.Pool.QueryRow(context.Background(), `SELECT estimated_cost_cents,settled_cost_cents,status FROM budget_reservations WHERE request_id=$1`, id).Scan(&estimated, &settled, &status)
			if err != nil || estimated <= 0 || settled != estimated || status != "settled" {
				t.Fatalf("unknown usage refunded: %d %d %s %v", estimated, settled, status, err)
			}
		})
	}
}

func TestCustomerNativeAuditAndSharedRPM(t *testing.T) {
	f := newChatFixture(t)
	f.setDeploymentCapabilities(&store.DeploymentCapabilities{Images: true, Chat: true})
	team := f.createTeam("customer-native-rpm")
	key1, key2 := f.issueKey(team, nil), f.issueKey(team, nil)
	rpm := 1
	c, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "native", RPM: &rpm})
	if err != nil {
		t.Fatal(err)
	}
	id := "native-audit-" + randHex(8)
	r := httptest.NewRequest("POST", "/v1/images/generations", strings.NewReader(fmt.Sprintf(`{"model":%q,"user":"native","prompt":"hi"}`, f.Alias)))
	r.Header.Set("Authorization", "Bearer "+key1)
	r.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("native: %d %s", w.Code, w.Body)
	}
	var customerID int64
	var accounting string
	deadline := time.Now().Add(3 * time.Second)
	for {
		err = f.Store.Pool.QueryRow(context.Background(), `SELECT customer_id,token_details->>'accounting' FROM usage_log WHERE request_id=$1`, id).Scan(&customerID, &accounting)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || customerID != c.ID || accounting != "unpriced_native" {
		t.Fatalf("unpriced attribution lost: %d %q %v", customerID, accounting, err)
	}
	w = responseHTTP(f, key2, "POST", "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"user":"native","messages":[{"role":"user","content":"hi"}]}`, f.Alias))
	if w.Code != 429 {
		t.Fatalf("second key bypassed shared RPM: %d %s", w.Code, w.Body)
	}
}

func TestCustomerPolicyAdminValidationPartialEditsAndIsolation(t *testing.T) {
	e := newTestEnv(t)
	team, _, manager := makeManager(e, "customer-policy")
	other := e.createTeam("customer-policy-other")
	base := "/admin/teams/" + team.Slug
	for _, mode := range []string{"optional", "required", "auto_create"} {
		code, body := e.PATCH(base+"/customer-policy", manager, map[string]any{"customer_registration": mode})
		if code != 200 || !strings.Contains(string(body), mode) {
			t.Fatalf("mode %d %s", code, body)
		}
	}
	if code, _ := e.PATCH(base+"/customer-policy", manager, map[string]any{"customer_registration": "fail_open"}); code != 400 {
		t.Fatal(code)
	}
	if code, _ := e.PATCH("/admin/teams/"+other.Slug+"/customer-policy", manager, map[string]any{"customer_registration": "optional"}); code != 403 {
		t.Fatal(code)
	}
	for _, policy := range []map[string]any{{"usd_limit_cents": -1}, {"rpm": -1}, {"tpm": 1000000001}, {"period": "century"}, {"external_id": "a/b"}} {
		if _, exists := policy["external_id"]; !exists {
			policy["external_id"] = "invalid"
		}
		if code, body := e.POST(base+"/customers", manager, policy); code != 400 {
			t.Fatalf("invalid: %d %s", code, body)
		}
	}
	policy := map[string]any{"external_id": "customer-a", "name": "before", "usd_limit_cents": 100, "period": "day", "rpm": 3, "tpm": 9000}
	if code, body := e.POST(base+"/customers", manager, policy); code != 201 {
		t.Fatalf("create %d %s", code, body)
	}
	if code, _ := e.POST(base+"/customers", manager, policy); code != 409 {
		t.Fatalf("duplicate %d", code)
	}
	code, body := e.PATCH(base+"/customers/customer-a", manager, map[string]any{"name": "after"})
	var customer CustomerResponse
	_ = json.Unmarshal(body, &customer)
	if code != 200 || customer.UsdLimitCents == nil || *customer.UsdLimitCents != 100 || customer.RPM == nil || *customer.RPM != 3 || customer.Period != "day" {
		t.Fatalf("partial edit lost fields: %d %s", code, body)
	}
	code, body = e.PATCH(base+"/customers/customer-a", manager, map[string]any{"usd_limit_cents": 0, "rpm": nil})
	if code != 200 {
		t.Fatalf("clear: %d %s", code, body)
	}
	c, err := e.Store.GetCustomerByExternalID(context.Background(), team.ID, "customer-a")
	if err != nil || c.UsdLimitCents == nil || *c.UsdLimitCents != 0 || c.RPM != nil || c.TPM == nil {
		t.Fatalf("zero/clear failed: %+v %v", c, err)
	}
	if code, body := e.GET(base+"/customers/customer-a/budget", manager); code != 200 {
		t.Fatalf("budget %d %s", code, body)
	}
	if code, _ := e.GET("/admin/teams/"+other.Slug+"/customers/customer-a/budget", manager); code != 403 {
		t.Fatal(code)
	}
}

func TestCustomerRegistrationModesAndDeniedStateNeverReachUpstream(t *testing.T) {
	f, calls, _ := newResponsesFixture(t)
	team := f.createTeam("customer-required")
	key := f.issueKey(team, nil)
	request := func(user string) *httptest.ResponseRecorder {
		return responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","user":%q,"store":false}`, f.Alias, user))
	}
	if err := f.Store.SetCustomerRegistration(context.Background(), team.ID, "required"); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"", "missing"} {
		if w := request(user); w.Code != 403 || calls.Load() != 0 {
			t.Fatalf("required %d %s", w.Code, w.Body)
		}
	}
	if err := f.Store.SetCustomerRegistration(context.Background(), team.ID, "optional"); err != nil {
		t.Fatal(err)
	}
	if w := request("unregistered"); w.Code != 200 {
		t.Fatalf("optional %d %s", w.Code, w.Body)
	}
	if _, err := f.Store.GetCustomerByExternalID(context.Background(), team.ID, "unregistered"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("optional mode unexpectedly registered a customer")
	}
	if err := f.Store.SetCustomerRegistration(context.Background(), team.ID, "auto_create"); err != nil {
		t.Fatal(err)
	}
	if w := request("automatic"); w.Code != 200 {
		t.Fatalf("auto %d %s", w.Code, w.Body)
	}
	c, err := f.Store.GetCustomerByExternalID(context.Background(), team.ID, "automatic")
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if _, err := f.Store.Pool.Exec(context.Background(), `UPDATE customers SET archived_at=NOW() WHERE id=$1`, c.ID); err != nil {
		t.Fatal(err)
	}
	if w := request("automatic"); w.Code != 403 || calls.Load() != before {
		t.Fatalf("archived %d %s", w.Code, w.Body)
	}
	if w := request(""); w.Code != 403 || calls.Load() != before {
		t.Fatal("auto mode permitted missing identity")
	}
	if w := request("bad/id"); w.Code != 400 || calls.Load() != before {
		t.Fatal("invalid ID reached upstream")
	}
}

func TestCustomerAtomicBudgetAndSettlementAcrossKeys(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(1000000, 1000000)
	team := f.createTeam("customer-budget-atomic")
	limit := int64(3000)
	c, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "shared", UsdLimitCents: &limit})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	ids := []string{}
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "cust-atomic-" + randHex(8)
			_, err := f.Budget.Admit(context.Background(), budget.AdmissionRequest{RequestID: id, Team: team, Customer: c, Alias: f.Alias, PromptTokens: 1000, Targets: []budget.Target{{ProviderType: "openai", UpstreamModel: f.UpModel}}})
			if err == nil {
				mu.Lock()
				ids = append(ids, id)
				mu.Unlock()
				return
			}
			var exceeded *budget.ExceededError
			if !errors.As(err, &exceeded) || exceeded.Scope != "customer" {
				t.Errorf("unexpected admission error: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(ids) != 3 {
		t.Fatalf("oversold customer budget: admitted %d", len(ids))
	}
	for _, id := range ids {
		if err := f.Budget.Settle(context.Background(), id, 10); err != nil {
			t.Fatal(err)
		}
		if err := f.Budget.Settle(context.Background(), id, 99); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := f.Budget.CustomerSummary(context.Background(), c)
	if err != nil || summary.UsedCents != 30 {
		t.Fatalf("settlement double counted: %+v %v", summary, err)
	}
}

func TestCustomerUsageAttributionBudgetDenialAndSpend(t *testing.T) {
	f, calls, _ := newResponsesFixture(t)
	team := f.createTeam("customer-attribution")
	key := f.issueKey(team, nil)
	limit := int64(100)
	c, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "customer-a", UsdLimitCents: &limit})
	if err != nil {
		t.Fatal(err)
	}
	id := "customer-usage-" + randHex(8)
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":"hi","user":"ignored","store":false}`, f.Alias)))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("X-Gatemux-Customer-Id", c.ExternalID)
	r.Header.Set("X-Request-Id", id)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("generation: %d %s", w.Code, w.Body)
	}
	var cid *int64
	var external string
	var cost int64
	deadline := time.Now().Add(3 * time.Second)
	for {
		err = f.Store.Pool.QueryRow(context.Background(), `SELECT customer_id,customer_external_id,cost_cents FROM usage_log WHERE request_id=$1`, id).Scan(&cid, &external, &cost)
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil || cid == nil || *cid != c.ID || external != c.ExternalID || cost != 2 {
		t.Fatalf("attribution %v %q %d %v", cid, external, cost, err)
	}
	report, err := f.Store.GetSpendReport(context.Background(), store.SpendFilter{TeamSlug: team.Slug, CustomerExternalID: c.ExternalID})
	if err != nil || report.Total.CostCents != 2 || report.Total.Requests != 1 {
		t.Fatalf("customer spend %+v %v", report, err)
	}
	summary, err := f.Budget.CustomerSummary(context.Background(), c)
	if err != nil || summary.UsedCents != 2 {
		t.Fatalf("ledger and usage double counted: %+v %v", summary, err)
	}
	zero := int64(0)
	if _, err := f.Store.UpdateCustomer(context.Background(), team.ID, c.ExternalID, store.UpdateCustomerParams{UsdLimitCents: &zero, Period: "month"}); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	w = responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi","user":%q}`, f.Alias, c.ExternalID))
	if w.Code != 403 || calls.Load() != before || !strings.Contains(w.Body.String(), "customer budget exceeded") {
		t.Fatalf("zero budget bypass: %d %s", w.Code, w.Body)
	}
}

func TestCustomerAutoRegistrationIsUniqueAndBounded(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("customer-auto-bound")
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Store.GetOrCreateCustomer(context.Background(), team.ID, "same"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	var count int
	if err := e.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM customers WHERE team_id=$1`, team.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate auto registration: %d %v", count, err)
	}
	if _, err := e.Store.Pool.Exec(context.Background(), `INSERT INTO customers(team_id,external_id) SELECT $1,'capacity-'||i FROM generate_series(1,$2::int) i`, team.ID, store.MaxAutoCreatedCustomers-1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Store.GetOrCreateCustomer(context.Background(), team.ID, "excess"); !errors.Is(err, store.ErrCustomerCapacity) {
		t.Fatalf("auto limit not enforced: %v", err)
	}
	if _, err := e.Store.GetOrCreateCustomer(context.Background(), team.ID, "same"); err != nil {
		t.Fatalf("cap rejected existing customer: %v", err)
	}
}
