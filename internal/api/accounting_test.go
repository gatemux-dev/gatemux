package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/gatemux-dev/gatemux/internal/usage"
)

func TestAccountingHTTPUsageEvidence(t *testing.T) {
	for _, surface := range []string{"chat", "chat-stream", "embeddings", "responses", "responses-stream"} {
		for _, evidence := range []string{"missing", "partial", "zero"} {
			t.Run(surface+"/"+evidence, func(t *testing.T) {
				f := newChatFixture(t)
				f.upsertPricing(100, 200)
				team := f.setTeamBudget(f.createTeam("accounting-evidence"), 100000)
				key := f.issueKey(team, nil)
				mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					var body map[string]any
					if strings.HasPrefix(surface, "responses") {
						body = nativeResponse("resp_evidence", f.UpModel)
					} else if surface == "embeddings" {
						body = map[string]any{"object": "list", "model": f.UpModel, "data": []any{}}
					} else {
						body = map[string]any{"id": "chat-evidence", "object": "chat.completion", "model": f.UpModel, "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"}, "delta": map[string]any{"content": "ok"}, "finish_reason": "stop"}}}
					}
					delete(body, "usage")
					if evidence == "partial" {
						body["usage"] = map[string]any{"total_tokens": 0}
					} else if evidence == "zero" {
						if strings.HasPrefix(surface, "responses") {
							body["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
						} else {
							body["usage"] = map[string]any{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0}
						}
					}
					if strings.HasSuffix(surface, "-stream") {
						w.Header().Set("Content-Type", "text/event-stream")
						if surface == "responses-stream" {
							body = map[string]any{"type": "response.completed", "sequence_number": 0, "response": body}
						}
						raw, _ := json.Marshal(body)
						fmt.Fprintf(w, "data: %s\n\n", raw)
						if surface == "chat-stream" {
							fmt.Fprint(w, "data: [DONE]\n\n")
						}
					} else {
						_ = json.NewEncoder(w).Encode(body)
					}
				}))
				defer mock.Close()
				setStreamUpstream(t, f, mock.URL, "openai")
				path := "/v1/chat/completions"
				body := map[string]any{"model": f.Alias, "messages": []any{map[string]string{"role": "user", "content": "hello"}}, "stream": strings.HasSuffix(surface, "-stream")}
				if surface == "embeddings" {
					path, body = "/v1/embeddings", map[string]any{"model": f.Alias, "input": "hello"}
				} else if strings.HasPrefix(surface, "responses") {
					path, body = "/v1/responses", map[string]any{"model": f.Alias, "input": "hello", "store": false, "stream": surface == "responses-stream"}
				}
				raw, _ := json.Marshal(body)
				req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
				id := "accounting-evidence-" + randHex(8)
				req.Header.Set("X-Request-Id", id)
				req.Header.Set("Authorization", "Bearer "+key)
				w := httptest.NewRecorder()
				f.Router.ServeHTTP(w, req)
				if w.Code != 200 {
					t.Fatalf("request %d: %s", w.Code, w.Body)
				}
				var state string
				var cost, estimate, settled int64
				err := f.Store.Pool.QueryRow(context.Background(), `SELECT u.accounting_state,u.cost_cents,b.estimated_cost_cents,b.settled_cost_cents FROM usage_log u JOIN budget_reservations b ON b.request_id=u.accounting_id WHERE u.accounting_id=$1 AND b.status='settled'`, id).Scan(&state, &cost, &estimate, &settled)
				if err != nil {
					t.Fatal(err)
				}
				if evidence == "zero" {
					if state != "priced" || cost != 0 || settled != 0 {
						t.Fatalf("explicit zero evidence lost: %s/%d/%d", state, cost, settled)
					}
				} else if state != "estimated" || cost != estimate || cost <= 0 || settled != cost {
					t.Fatalf("unknown work silently refunded: %s cost=%d estimate=%d settled=%d", state, cost, estimate, settled)
				}
			})
		}
	}
}

func TestAccountingRequestIDReuseBeforeUpstream(t *testing.T) {
	f := newChatFixture(t)
	f.upsertPricing(100, 200)
	team := f.createTeam("accounting-reuse")
	key := f.issueKey(team, nil)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		f.Mock.Config.Handler.ServeHTTP(w, r)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	id := "accounting-reuse-" + randHex(8)
	for i, want := range []int{200, 409} {
		req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, f.Alias)))
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Request-Id", id)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, req)
		if w.Code != want || i == 1 && readErrorType(w.Body.Bytes()) != "request_id_reused" {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate request reached upstream")
	}
	var count int
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT COUNT(*) FROM usage_log WHERE request_id=$1`, id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("reused ID changed usage: %d %v", count, err)
	}
	// Failed completion freezes subsequent admitted work before provider IO.
	failedID := "accounting-fail-" + randHex(8)
	e := store.UsageEntry{TeamID: team.ID, Alias: f.Alias, RequestID: failedID, AccountingID: failedID}
	if err := f.Store.BeginAccounting(context.Background(), e, time.Minute); err != nil {
		t.Fatal(err)
	}
	e.TokenDetails = json.RawMessage(`{"broken":`)
	if _, err := f.UsageLog.Record(e); err == nil {
		t.Fatal("failure injection did not fail")
	}
	if status, body := f.chatPOST(key); status != 503 || readErrorType(body) != "accounting_unavailable" || calls.Load() != 1 {
		t.Fatalf("accounting pause bypassed: %d %s", status, body)
	}
	e.TokenDetails = nil
	if _, err := f.Store.FinalizeUsage(context.Background(), e); err != nil {
		t.Fatal(err)
	}
}

func TestAccountingStoredResponsesDoNotRebill(t *testing.T) {
	f, _, _ := newResponsesFixture(t)
	team := f.createTeam("accounting-resources")
	key := f.issueKey(team, nil)
	w := responseHTTP(f, key, "POST", "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hello"}`, f.Alias))
	var response struct {
		ID string `json:"id"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ID == "" {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	for _, operation := range []struct{ method, suffix string }{{"GET", ""}, {"GET", "/input_items"}, {"DELETE", ""}} {
		id := "resource-operation-" + randHex(8)
		req := httptest.NewRequest(operation.method, "/v1/responses/"+response.ID+operation.suffix, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("X-Request-Id", id)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("resource %s: %d %s", operation.method, w.Code, w.Body)
		}
		var state string
		var cost, status int
		if err := f.Store.Pool.QueryRow(context.Background(), `SELECT accounting_state,cost_cents,status_code FROM usage_log WHERE accounting_id=$1`, id).Scan(&state, &cost, &status); err != nil || state != "not_billable" || cost != 0 || status != 200 {
			t.Fatalf("resource rebilled/misreported: %s/%d/%d %v", state, cost, status, err)
		}
	}
}

func TestAccountingAdmissionAndStartFailClosed(t *testing.T) {
	f := newChatFixture(t)
	team := f.createTeam("accounting-start")
	st, err := store.Open(context.Background(), os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	l := usage.NewLogger(st, nil)
	l.Close()
	st.Close()
	h := &V1Handler{Usage: l}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r = r.WithContext(auth.WithTeam(r.Context(), team))
	w := httptest.NewRecorder()
	if _, admitted := h.beginAccounting(w, r, f.Alias); admitted || w.Code != 503 || readErrorType(w.Body.Bytes()) != "accounting_unavailable" {
		t.Fatalf("missing durable admission accepted: %d %s", w.Code, w.Body)
	}
	w = httptest.NewRecorder()
	finish, admitted := f.Handler.beginAccounting(w, r, f.Alias)
	if !admitted {
		t.Fatalf("begin: %d %s", w.Code, w.Body)
	}
	defer finish()
	run := requestAccounting(r.Context())
	if run == nil {
		t.Fatal("journal owner not in context")
	}
	if _, err := f.Store.Pool.Exec(context.Background(), `UPDATE inference_journal SET recover_after=NOW()-interval '1 second' WHERE request_id=$1`, run.entry.RequestID); err != nil {
		t.Fatal(err)
	}
	target, err := f.Registry.Resolve(f.Alias)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.Handler.tryAcquireUpstream(r.Context(), target)
	var unavailable *accountingError
	if !errors.As(err, &unavailable) {
		t.Fatalf("expired journal allowed upstream IO: %v", err)
	}
	if state := f.Registry.DeploymentConcurrency(f.DepName); state.InFlight != 0 {
		t.Fatalf("failed start leaked permit: %+v", state)
	}
	finish()
	var state string
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT accounting_state FROM usage_log WHERE accounting_id=$1`, run.entry.RequestID).Scan(&state); err != nil || state != "not_billable" {
		t.Fatalf("pre-upstream failure charged: %s %v", state, err)
	}
}
