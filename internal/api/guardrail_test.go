package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func testGuardPolicy(mode, phase string, terms ...string) guardrails.Policy {
	return guardrails.Policy{Name: "test", Type: "banned_terms", Mode: mode, Phase: phase, Terms: terms}
}
func setGuardScope(t *testing.T, s *store.Store, scope, id string, policies []guardrails.Policy) {
	t.Helper()
	old, err := s.GetGuardrailScope(context.Background(), scope, id)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(policies)
	if err = s.ReplaceGuardrailScope(context.Background(), scope, id, old, raw, "test"); err != nil {
		t.Fatal(err)
	}
}
func guardPUT(e *testEnv, path, token string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest("PUT", path, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	e.Router.ServeHTTP(w, r)
	return w.Code, w.Body.Bytes()
}

func TestGuardrailAdminCASAndRoles(t *testing.T) {
	e := newTestEnv(t)
	team := e.createTeam("guard-admin")
	path := "/admin/guardrails/team/" + team.Slug
	policies := []guardrails.Policy{testGuardPolicy("block", "both", "secret")}
	body := map[string]any{"expected": []any{}, "policies": policies}
	_, manager := e.createUserWithRole("guard-manager", store.RoleManager, &team.ID)
	_, member := e.createUserWithRole("guard-member", store.RoleMember, &team.ID)
	for _, token := range []string{manager, member} {
		if status, _ := guardPUT(e, path, token, body); status != 403 {
			t.Fatalf("role write %d", status)
		}
		if status, _ := e.GET(path, token); status != 403 {
			t.Fatalf("role read %d", status)
		}
	}
	if status, b := guardPUT(e, path, e.MasterKey, body); status != 200 {
		t.Fatalf("save %d %s", status, b)
	}
	if status, _ := guardPUT(e, path, e.MasterKey, body); status != 409 {
		t.Fatalf("lost edit accepted %d", status)
	}
	if status, b := e.GET("/admin/guardrails", e.MasterKey); status != 200 || !bytes.Contains(b, []byte(team.Slug)) {
		t.Fatalf("catalog %d %s", status, b)
	}
	if status, b := guardPUT(e, path, e.MasterKey, map[string]any{"expected": policies, "policies": []any{}}); status != 200 {
		t.Fatalf("delete %d %s", status, b)
	}
	bad := []guardrails.Policy{testGuardPolicy("redact", "both", "")}
	if status, _ := guardPUT(e, path, e.MasterKey, map[string]any{"expected": []any{}, "policies": bad}); status != 400 {
		t.Fatal("invalid spec accepted")
	}
}

func TestGuardrailPrePostAndIsolation(t *testing.T) {
	f := newChatFixture(t)
	team := f.createTeam("guard-runtime")
	key := f.issueKey(team, nil)
	other := f.createTeam("guard-other")
	otherKey := f.issueKey(other, nil)
	var calls atomic.Int64
	var lastInput string
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		lastInput = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"test","model":"upstream","choices":[{"index":0,"message":{"role":"assistant","content":"secret and SECRET"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)
	}))
	defer mock.Close()
	setStreamUpstream(t, f, mock.URL, "openai")
	f.upsertPricing(100, 200)
	setGuardScope(t, f.Store, "team", team.Slug, []guardrails.Policy{testGuardPolicy("block", "pre", "hello")})
	if status, b := f.chatPOST(key); status != 403 || readErrorType(b) != "guardrail_blocked" || calls.Load() != 0 {
		t.Fatalf("pre block %d %s calls=%d", status, b, calls.Load())
	}
	if status, _ := f.chatPOST(otherKey); status != 200 || calls.Load() != 1 {
		t.Fatal("foreign team affected")
	}
	setGuardScope(t, f.Store, "team", team.Slug, []guardrails.Policy{testGuardPolicy("redact", "both", "hello", "secret")})
	if status, b := f.chatPOST(key); status != 200 || bytes.Contains(bytes.ToLower(b), []byte("secret")) || strings.Contains(lastInput, "hello") || !strings.Contains(lastInput, "[REDACTED]") {
		t.Fatalf("redaction %d %s upstream=%s", status, b, lastInput)
	}
	// Alias policy cannot weaken the same-named team policy.
	setGuardScope(t, f.Store, "alias", f.Alias, []guardrails.Policy{testGuardPolicy("block", "post", "secret")})
	if status, b := f.chatPOST(key); status != 403 {
		t.Fatalf("additive policy %d %s", status, b)
	}
	setGuardScope(t, f.Store, "alias", f.Alias, []guardrails.Policy{})
	if status, b := f.embeddingsPOST(key); status != 400 || readErrorType(b) != "guardrail_unsupported" {
		t.Fatalf("endpoint bypass %d %s", status, b)
	}
	// Invalid legacy/unknown policy must not disappear from enforcement.
	if _, err := f.Store.Pool.Exec(context.Background(), `UPDATE teams SET guardrails='[{"name":"unknown","mode":"redact"}]' WHERE id=$1`, team.ID); err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	if status, _ := f.chatPOST(key); status != 503 || calls.Load() != before {
		t.Fatal("invalid policy failed open")
	}
	var decisions int
	if err := f.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM guardrail_decisions WHERE team_id=$1 AND decision='redact'`, team.ID).Scan(&decisions); err != nil || decisions < 2 {
		t.Fatalf("missing decisions %d %v", decisions, err)
	}
}

func TestGuardrailStreamBoundaryAuditAndRecovery(t *testing.T) {
	for _, mode := range []string{"redact", "block", "flag"} {
		t.Run(mode, func(t *testing.T) {
			f := newChatFixture(t)
			team := f.setTeamBudget(f.createTeam("guard-stream"), 100000)
			key := f.issueKey(team, nil)
			f.upsertPricing(100, 200)
			setGuardScope(t, f.Store, "team", team.Slug, []guardrails.Policy{testGuardPolicy(mode, "post", "secret")})
			closed := make(chan struct{})
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(closed)
				io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "text/event-stream")
				for _, part := range []string{"safe se", "cr", "et safe"} {
					b, _ := json.Marshal(map[string]any{"id": "test", "model": "upstream", "choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": part}, "finish_reason": nil}}})
					fmt.Fprintf(w, "data: %s\n\n", b)
					w.(http.Flusher).Flush()
				}
				io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\ndata: [DONE]\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer mock.Close()
			setStreamUpstream(t, f, mock.URL, "openai")
			r := streamRequest(f, key)
			rid := "guard-" + randHex(6)
			r.Header.Set("X-Request-Id", rid)
			w := httptest.NewRecorder()
			f.Router.ServeHTTP(w, r)
			if mode == "block" {
				if strings.Contains(w.Body.String(), "[DONE]") || !strings.Contains(w.Body.String(), "guardrail_blocked") {
					t.Fatalf("block %s", w.Body)
				}
			} else if !strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatalf("missing terminal %s", w.Body)
			}
			var output strings.Builder
			for _, line := range strings.Split(w.Body.String(), "\n") {
				if !strings.HasPrefix(line, "data: {") {
					continue
				}
				var c struct {
					Choices []struct{ Delta struct{ Content string } }
				}
				json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &c)
				for _, v := range c.Choices {
					output.WriteString(v.Delta.Content)
				}
			}
			if mode == "redact" && output.String() != "safe [REDACTED] safe" {
				t.Fatalf("stream leaked/mutated %q", output.String())
			}
			if mode == "block" && strings.Contains(output.String(), "secret") {
				t.Fatal("blocked term escaped")
			}
			if mode == "flag" && output.String() != "safe secret safe" {
				t.Fatal("flag changed content")
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("upstream not canceled")
			}
			assertStreamPermitsReleased(t, f, "openai")
			var count int
			if err := f.Store.Pool.QueryRow(context.Background(), `SELECT count(*) FROM guardrail_decisions WHERE request_id=$1 AND guardrail_name='team/test' AND phase='post' AND decision=$2`, rid, mode).Scan(&count); err != nil || count != 1 {
				t.Fatalf("stream audit %d %v", count, err)
			}
			var status string
			if err := f.Store.Pool.QueryRow(context.Background(), `SELECT status FROM budget_reservations WHERE request_id=$1`, rid).Scan(&status); err != nil || status != "settled" {
				t.Fatalf("reservation %q %v", status, err)
			}
		})
	}
}
