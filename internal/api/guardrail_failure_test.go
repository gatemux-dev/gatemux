package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGuardrailAuditFailureWithholdsResponse(t *testing.T) {
	// A closed, never-connected pool deterministically simulates a failed audit
	// destination without disrupting the shared disposable Postgres service.
	pool, err := pgxpool.New(context.Background(), "postgres://invalid@127.0.0.1:1/invalid?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	g := &guardrailRun{store: &store.Store{Pool: pool}, post: guardrails.New([]guardrails.Policy{testGuardPolicy("redact", "post", "secret")}, "post")}
	_, err = g.response([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"safe"},"finish_reason":"stop"}]}`), false)
	var failure *guardrailError
	if !errors.As(err, &failure) || failure.status != 503 {
		t.Fatalf("audit failed open: %v", err)
	}
}

func TestGuardrailUnsupportedRequestsNoUpstream(t *testing.T) {
	f := newChatFixture(t)
	team := f.createTeam("guard-negative")
	key := f.issueKey(team, nil)
	setGuardScope(t, f.Store, "team", team.Slug, []guardrails.Policy{testGuardPolicy("block", "both", "secret")})
	for _, extra := range []string{`,"tools":[]`, `,"n":2`, `,"future_extension":"secret"`} {
		r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"`+f.Alias+`","messages":[{"role":"user","content":"safe"}]`+extra+`}`))
		r.Header.Set("Authorization", "Bearer "+key)
		w := httptest.NewRecorder()
		f.Router.ServeHTTP(w, r)
		if w.Code != 400 || readErrorType(w.Body.Bytes()) != "guardrail_unsupported" {
			t.Fatalf("bypass %d %s", w.Code, w.Body)
		}
	}
	// Canceled policy reads fail closed and return promptly, without changing a
	// live DB or relying on a successful query under an expired request context.
	r := streamRequest(f, key)
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	r = r.WithContext(ctx)
	started := time.Now()
	w := httptest.NewRecorder()
	f.Router.ServeHTTP(w, r)
	if w.Code == 200 || time.Since(started) > 3*time.Second {
		t.Fatalf("canceled request %d", w.Code)
	}
	assertStreamPermitsReleased(t, f, "openai")
}
