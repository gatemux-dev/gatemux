package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestCustomerConcurrencyAdminRoundTripAndTeamIsolation(t *testing.T) {
	e := newTestEnv(t)
	team, _, manager := makeManager(e, "customers")
	other := e.createTeam("customer-other")
	path := "/admin/teams/" + team.Slug + "/customers"
	code, body := e.POST(path, manager, map[string]any{"external_id": "customer-a", "name": "Customer A", "max_parallel_requests": 2})
	if code != 201 {
		t.Fatalf("create: %d %s", code, body)
	}
	var row CustomerResponse
	if err := json.Unmarshal(body, &row); err != nil {
		t.Fatal(err)
	}
	if row.MaxParallelRequests == nil || *row.MaxParallelRequests != 2 {
		t.Fatalf("create cap: %+v", row)
	}
	for _, value := range []any{3, nil, 0} {
		code, body = e.PATCH(path+"/customer-a/concurrency", manager, map[string]any{"max_parallel_requests": value})
		if code != 200 {
			t.Fatalf("update: %d %s", code, body)
		}
		stored, err := e.Store.GetCustomerByExternalID(context.Background(), team.ID, "customer-a")
		if err != nil {
			t.Fatal(err)
		}
		if stored.Name != "Customer A" {
			t.Fatal("cap edit changed customer name")
		}
		if (value == nil || value == 0) && stored.MaxParallelRequests != nil {
			t.Fatal("customer cap was not cleared")
		}
	}
	code, body = e.PATCH(path+"/customer-a/concurrency", manager, map[string]any{"max_parallel_requests": -1})
	if code != 400 {
		t.Fatalf("negative cap: %d %s", code, body)
	}
	code, body = e.PATCH("/admin/teams/"+other.Slug+"/customers/customer-a/concurrency", manager, map[string]any{"max_parallel_requests": 1})
	if code != 403 {
		t.Fatalf("cross-team mutation: %d %s", code, body)
	}
	if code, body = e.GET(path, manager); code != 200 || !bytes.Contains(body, []byte("customer-a")) {
		t.Fatalf("list: %d %s", code, body)
	}
}

func TestCustomerConcurrencyIdentityPrecedenceAndCompoundAdmission(t *testing.T) {
	limiter := routingLimiter(t)
	f := newChatFixture(t)
	f.Handler.Concurrency = limiter
	team := f.createTeam("customer-scope")
	other := f.createTeam("customer-scope-other")
	limit := 1
	if _, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "limited", MaxParallelRequests: &limit}); err != nil {
		t.Fatal(err)
	}
	setRoutingLimit(t, f, "model", f.Alias)
	held, err := f.Handler.acquireRoutingScope(context.Background(), "customer", store.CustomerConcurrencySubject(team.ID, "limited"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Handler.releaseRoutingLease(held)
	for _, tc := range []struct {
		name, header, alt, user string
		team                    *store.Team
		denied                  bool
	}{
		{"body", "", "", "limited", team, true},
		{"alternate", "", "limited", "unlimited", team, true},
		{"primary", "limited", "unlimited", "unlimited", team, true},
		{"primary overrides", "unlimited", "limited", "limited", team, false},
		{"alternate overrides", "", "unlimited", "limited", team, false},
		{"another team", "limited", "", "", other, false},
		{"no identity", "", "", "", team, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Header.Set("X-Gatemux-Customer-Id", tc.header)
			r.Header.Set("X-Customer-Id", tc.alt)
			r = r.WithContext(auth.WithTeam(r.Context(), tc.team))
			out := httptest.NewRecorder()
			release, allowed := f.Handler.admitRequestConcurrency(out, r, f.Alias, tc.user)
			if allowed {
				release()
			}
			if allowed == tc.denied {
				t.Fatalf("allowed=%v, want denied=%v: %s", allowed, tc.denied, out.Body)
			}
			if tc.denied && (out.Code != 429 || out.Header().Get("Retry-After") == "" || !strings.Contains(out.Body.String(), "customer")) {
				t.Fatalf("customer denial: %d %s", out.Code, out.Body)
			}
			// A rejected customer must not leave a model membership behind.
			modelLease, err := f.Handler.acquireRoutingScope(context.Background(), "model", f.Alias)
			if err != nil {
				t.Fatalf("model membership leaked: %v", err)
			}
			f.Handler.releaseRoutingLease(modelLease)
		})
	}
}

type unreadBody struct{}

func (unreadBody) Read([]byte) (int, error) { panic("admission read an opaque passthrough body") }
func (unreadBody) Close() error             { return nil }

func TestCustomerConcurrencyAppliedAtEveryEndpointParser(t *testing.T) {
	f := newChatFixture(t)
	team := f.createTeam("customer-endpoints")
	limit := 1
	if _, err := f.Store.CreateCustomer(context.Background(), store.CreateCustomerParams{TeamID: team.ID, ExternalID: "limited", MaxParallelRequests: &limit}); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.RefreshConcurrencyPolicies(context.Background()); err != nil {
		t.Fatal(err)
	}
	// With Redis absent, every configured customer cap fails closed.
	for _, tc := range []struct {
		name, body string
		handler    http.HandlerFunc
	}{
		{"chat", `"messages":[{"role":"user","content":"hi"}]`, f.Handler.ChatCompletions},
		{"embeddings", `"input":"hi"`, f.Handler.Embeddings},
		{"messages", `"messages":[]`, f.Handler.Messages},
		{"moderation", `"input":"hi"`, f.Handler.Moderations},
		{"rerank", `"query":"hi"`, f.Handler.Rerank},
		{"images", `"prompt":"hi"`, f.Handler.ImagesGenerations},
		{"speech", `"input":"hi"`, f.Handler.AudioSpeech},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/test", strings.NewReader(`{"model":"`+f.Alias+`","user":"limited",`+tc.body+`}`))
			req = req.WithContext(auth.WithTeam(req.Context(), team))
			rec := httptest.NewRecorder()
			tc.handler(rec, req)
			if rec.Code != 503 || readErrorType(rec.Body.Bytes()) != "concurrency_limit_unavailable" {
				t.Fatalf("customer admission: %d %s", rec.Code, rec.Body)
			}
		})
	}
	for _, handler := range []http.HandlerFunc{f.Handler.AudioTranscriptions, f.Handler.AudioTranslations} {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("model", f.Alias)
		_ = form.WriteField("user", "limited")
		_ = form.Close()
		req := httptest.NewRequest(http.MethodPost, "/v1/audio/test", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		req = req.WithContext(auth.WithTeam(req.Context(), team))
		rec := httptest.NewRecorder()
		handler(rec, req)
		if rec.Code != 503 || readErrorType(rec.Body.Bytes()) != "concurrency_limit_unavailable" {
			t.Fatalf("multipart admission: %d %s", rec.Code, rec.Body)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/passthrough/test/upload", io.NopCloser(strings.NewReader("")))
	req.Body = unreadBody{}
	req.Header.Set("X-Gatemux-Customer-Id", "limited")
	req = req.WithContext(auth.WithTeam(req.Context(), team))
	rec := httptest.NewRecorder()
	f.Handler.GenericPassthrough(rec, req)
	if rec.Code != 503 || readErrorType(rec.Body.Bytes()) != "concurrency_limit_unavailable" {
		t.Fatalf("opaque passthrough admission: %d %s", rec.Code, rec.Body)
	}
}
