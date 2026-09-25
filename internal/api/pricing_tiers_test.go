package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestPricingTierAdminRoundTripAndValidation(t *testing.T) {
	e := newTestEnv(t)
	model := "tier-" + randHex(5)
	for _, tc := range []struct {
		tier   string
		status int
	}{
		{`"cache_read_per_million_cents":0,"cache_write_per_million_cents":125,"cache_write_1h_per_million_cents":200,"reasoning_per_million_cents":400`, 201},
		{`"cache_read_per_million_cents":-1`, 400},
		{`"cache_read_per_million_cents":1.5`, 400},
		{`"cache_read_per_million_cents":9223372036854775808`, 400},
	} {
		req := httptest.NewRequest("POST", "/admin/pricing", strings.NewReader(`{"provider_type":"openai","upstream_model":"`+model+`","input_per_million_cents":100,"output_per_million_cents":200,`+tc.tier+`}`))
		req.Header.Set("Authorization", "Bearer "+e.MasterKey)
		w := httptest.NewRecorder()
		e.Router.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Fatalf("status %d want %d: %s", w.Code, tc.status, w.Body)
		}
	}
	p, err := e.Store.GetCurrentPricing(context.Background(), "openai", model)
	if err != nil || p.Tiers.CacheReadPerMillionCents == nil || *p.Tiers.CacheReadPerMillionCents != 0 || *p.Tiers.CacheWrite1hPerMillionCents != 200 {
		t.Fatalf("stored price: %+v %v", p, err)
	}
	// A new full-row price with null/omitted tiers deliberately restores inheritance.
	req := httptest.NewRequest("POST", "/admin/pricing", strings.NewReader(`{"provider_type":"openai","upstream_model":"`+model+`","input_per_million_cents":100,"output_per_million_cents":200,"cache_read_per_million_cents":null}`))
	req.Header.Set("Authorization", "Bearer "+e.MasterKey)
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, req)
	if w.Code != 201 {
		t.Fatalf("reset: %s", w.Body)
	}
	p, err = e.Store.GetCurrentPricing(context.Background(), "openai", model)
	if err != nil || p.Tiers.CacheReadPerMillionCents != nil || p.Tiers.CacheWrite1hPerMillionCents != nil {
		t.Fatalf("reset inheritance: %+v %v", p, err)
	}
}

func TestTieredUsageSettlesAndPersistsForChatAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			f := newChatFixture(t)
			usage := `{"prompt_tokens":1000000,"completion_tokens":1000000,"total_tokens":2000000,"prompt_tokens_details":{"cached_tokens":300000},"completion_tokens_details":{"reasoning_tokens":250000},"cache_creation_input_tokens":200000,"cache_creation":{"ephemeral_5m_input_tokens":100000,"ephemeral_1h_input_tokens":100000}}`
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"id\":\"id\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":%s}\n\ndata: [DONE]\n\n", usage)
				} else {
					io.WriteString(w, `{"id":"id","object":"chat.completion","model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":`+usage+`}`)
				}
			}))
			defer mock.Close()
			setStreamUpstream(t, f, mock.URL, "openai")
			read, write, hour, reason := int64(10), int64(125), int64(200), int64(400)
			_, err := f.Store.UpsertPricing(context.Background(), "openai", f.UpModel, 100, 200, store.PricingTiers{CacheReadPerMillionCents: &read, CacheWritePerMillionCents: &write, CacheWrite1hPerMillionCents: &hour, ReasoningPerMillionCents: &reason})
			if err != nil {
				t.Fatal(err)
			}
			team := f.setTeamBudget(f.createTeam("tier"), 100000)
			key := f.issueKey(team, nil)
			id := "tier-" + randHex(6)
			req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%t}`, f.Alias, stream)))
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("X-Request-Id", id)
			w := httptest.NewRecorder()
			f.Router.ServeHTTP(w, req)
			if w.Code != 200 {
				t.Fatalf("chat: %d %s", w.Code, w.Body)
			}
			var settled int64
			if err := f.Store.Pool.QueryRow(context.Background(), "SELECT settled_cost_cents FROM budget_reservations WHERE request_id=$1", id).Scan(&settled); err != nil || settled != 336 {
				t.Fatalf("settled=%d err=%v", settled, err)
			}
			var details json.RawMessage
			var logged int64
			deadline := time.Now().Add(3 * time.Second)
			for {
				err = f.Store.Pool.QueryRow(context.Background(), "SELECT cost_cents,token_details FROM usage_log WHERE request_id=$1", id).Scan(&logged, &details)
				if err == nil || time.Now().After(deadline) {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil || logged != 336 || !strings.Contains(string(details), `"reasoning_tokens": 250000`) {
				t.Fatalf("audit: %d %s %v", logged, details, err)
			}
		})
	}
}

func TestEmbeddingTokenArrayHTTPFidelityAndBudgetAdmission(t *testing.T) {
	f := newChatFixture(t)
	var received json.RawMessage
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		received = body["input"]
		if string(body["model"]) != fmt.Sprintf("%q", f.UpModel) || string(body["dimensions"]) != "256" {
			t.Errorf("request fidelity: %v", body)
		}
		io.WriteString(w, `{"object":"list","model":"test","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	f.upsertPricing(1000000, 0)
	team := f.setTeamBudget(f.createTeam("token-embed"), 3)
	key := f.issueKey(team, nil)
	req := httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":[[0,100000],[200000]],"dimensions":256}`, f.Alias)))
	req.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if w.Code != 200 || string(received) != `[[0,100000],[200000]]` {
		t.Fatalf("embedding=%d %s input=%s", w.Code, w.Body, received)
	}
	// Exactly three token IDs consumed the entire three-cent budget.
	req = httptest.NewRequest("POST", "/v1/embeddings", strings.NewReader(fmt.Sprintf(`{"model":%q,"input":[1],"dimensions":256}`, f.Alias)))
	req.Header.Set("Authorization", "Bearer "+key)
	w = httptest.NewRecorder()
	f.Router.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("budget did not deny next token: %d %s", w.Code, w.Body)
	}
}
