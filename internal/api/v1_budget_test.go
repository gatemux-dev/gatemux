package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/ratelimit"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/gatemux-dev/gatemux/internal/usage"
)

// chatFixture wires the V1 chat handler against a Postgres-backed Store and
// a fake OpenAI HTTP server, so tests for the budget admission path can
// drive ChatCompletions end-to-end without real upstream calls.
type chatFixture struct {
	*testEnv
	Mock     *httptest.Server
	DepName  string
	Alias    string
	UpModel  string
	CredEnv  string
	BaseURL  string
	Registry *router.Registry
	Router   chi.Router
	UsageLog *usage.Logger
	Handler  *V1Handler
}

func newChatFixture(t *testing.T) *chatFixture {
	base := newTestEnv(t)

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Path-based dispatch so the same mock can answer both chat
		// completions and embeddings — capability filter tests need both
		// endpoints reachable so we can prove the router blocks the
		// wrong-shape request before it ever lands here.
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"model":  "test-upstream",
				"data": []map[string]any{{
					"object":    "embedding",
					"index":     0,
					"embedding": []float32{0.1, 0.2, 0.3},
				}},
				"usage": map[string]any{"prompt_tokens": 4, "total_tokens": 4},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":     "cmpl-mock",
			"object": "chat.completion",
			"model":  "test-upstream",
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "ok"},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{
				"prompt_tokens":     5,
				"completion_tokens": 3,
				"total_tokens":      8,
			},
		})
	}))
	t.Cleanup(mock.Close)

	suffix := randHex(4)
	depName := "v1-dep-" + suffix
	alias := "v1-alias-" + suffix
	upModel := "v1-upstream-" + suffix
	credEnv := "TEST_KEY_" + strings.ToUpper(suffix)
	t.Setenv(credEnv, "fake-test-key")

	baseURL := mock.URL + "/v1"
	if _, err := base.Store.UpsertDeployment(context.Background(), depName, "openai", upModel, credEnv, &baseURL, nil, nil, nil); err != nil {
		t.Fatalf("upsert deployment: %v", err)
	}
	if _, err := base.Store.UpsertAlias(context.Background(), alias, []string{depName}); err != nil {
		t.Fatalf("upsert alias: %v", err)
	}

	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reg, err := router.New(base.Store, logger)
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	usageLogger := usage.NewLogger(base.Store, logger)
	t.Cleanup(usageLogger.Close)

	v1H := &V1Handler{
		Logger:    logger,
		Router:    reg,
		Usage:     usageLogger,
		RateLimit: ratelimit.New(),
		Budget:    base.Budget,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID) // budget admission keys reservations off this id
	r.Route("/v1", func(r chi.Router) {
		r.Use(auth.Bearer(base.Store, nil))
		r.Get("/models", v1H.Models)
		r.Post("/chat/completions", v1H.ChatCompletions)
		r.Post("/embeddings", v1H.Embeddings)
		r.Post("/images/generations", v1H.ImagesGenerations)
		r.Post("/messages", v1H.Messages)
		r.Post("/responses", v1H.Responses)
		r.Get("/responses/{responseID}", v1H.GetResponse)
		r.Delete("/responses/{responseID}", v1H.DeleteResponse)
		r.Get("/responses/{responseID}/input_items", v1H.ResponseInputItems)
	})

	return &chatFixture{
		testEnv:  base,
		Mock:     mock,
		DepName:  depName,
		Alias:    alias,
		UpModel:  upModel,
		CredEnv:  credEnv,
		BaseURL:  baseURL,
		Registry: reg,
		Router:   r,
		UsageLog: usageLogger,
		Handler:  v1H,
	}
}

// setDeploymentCapabilities updates the fixture deployment's capability
// flags and refreshes the registry so the new caps take effect immediately.
func (f *chatFixture) setDeploymentCapabilities(caps *store.DeploymentCapabilities) {
	f.t.Helper()
	baseURL := f.BaseURL
	if _, err := f.Store.UpsertDeployment(context.Background(),
		f.DepName, "openai", f.UpModel, f.CredEnv, &baseURL, nil, caps, nil); err != nil {
		f.t.Fatalf("update capabilities: %v", err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		f.t.Fatalf("refresh registry: %v", err)
	}
}

// embeddingsPOST is the embeddings-shape version of chatPOST: same alias,
// same auth, but the embeddings request body. Used by capability tests.
func (f *chatFixture) embeddingsPOST(rawKey string) (int, []byte) {
	f.t.Helper()
	body := map[string]any{
		"model": f.Alias,
		"input": "hello world",
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	f.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

// chatPOST sends a minimal chat request as the given virtual key. The
// alias is the public name; upstream model substitution happens inside the
// handler.
func (f *chatFixture) chatPOST(rawKey string) (int, []byte) {
	f.t.Helper()
	body := map[string]any{
		"model": f.Alias,
		"messages": []map[string]string{
			{"role": "user", "content": "hello"},
		},
	}
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(buf))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	f.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

// issueKey wires a virtual key for a team, optionally bound to a user.
// Returns the raw key string the client uses as Bearer.
func (f *chatFixture) issueKey(team *store.Team, user *store.User) string {
	f.t.Helper()
	rawKey, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		f.t.Fatalf("generate key: %v", err)
	}
	params := store.CreateVirtualKeyParams{
		TeamID:  team.ID,
		KeyHash: hash,
		Prefix:  prefix,
		Name:    "test-" + randHex(4),
	}
	if user != nil {
		uid := user.ID
		params.UserID = &uid
	}
	if _, err := f.Store.CreateVirtualKey(context.Background(), params); err != nil {
		f.t.Fatalf("create virtual key: %v", err)
	}
	return rawKey
}

// upsertPricing seeds pricing for the fixture's deployment so admission
// can compute estimated cost.
func (f *chatFixture) upsertPricing(inputPerMillionCents, outputPerMillionCents int64) {
	f.t.Helper()
	if _, err := f.Store.UpsertPricing(context.Background(), "openai", f.UpModel, inputPerMillionCents, outputPerMillionCents); err != nil {
		f.t.Fatalf("upsert pricing: %v", err)
	}
}

// setTeamBudget tightens or removes a team's USD limit.
func (f *chatFixture) setTeamBudget(team *store.Team, limitCents int64) *store.Team {
	f.t.Helper()
	updated, err := f.Store.UpdateTeamBudget(context.Background(), team.Slug, &limitCents, "month")
	if err != nil {
		f.t.Fatalf("update team budget: %v", err)
	}
	return updated
}

func (f *chatFixture) setUserBudget(user *store.User, limitCents int64) {
	f.t.Helper()
	if _, err := f.Store.UpdateUserBudget(context.Background(), user.ID, &limitCents, "month"); err != nil {
		f.t.Fatalf("update user budget: %v", err)
	}
}

// readErrorType extracts {"error":{"type":...}} from a JSON error body.
func readErrorType(body []byte) string {
	var resp struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &resp)
	return resp.Error.Type
}

// --- tests below ---

func TestChat_NoBudget_AllowsAndCallsUpstream(t *testing.T) {
	// Sanity check: with neither team nor user budget set, admission should
	// short-circuit and the request should proxy through to the mock OpenAI.
	if os.Getenv("TEST_DATABASE_URL") == "" {
		// keep the rest of the suite usable when only the dev compose is up
	}
	f := newChatFixture(t)
	team := f.createTeam("nobudget")
	rawKey := f.issueKey(team, nil)

	code, body := f.chatPOST(rawKey)
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", code, body)
	}
}

func TestChat_TeamBudgetWithoutPricing_FailsClosed(t *testing.T) {
	// Team has a budget but pricing is missing for the upstream model.
	// Admission must deny rather than letting unpriced spend through.
	f := newChatFixture(t)
	team := f.createTeam("nopricing")
	team = f.setTeamBudget(team, 5000) // $50/month
	rawKey := f.issueKey(team, nil)

	code, body := f.chatPOST(rawKey)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("got %d, want 503 fail-closed (body=%s)", code, body)
	}
	if got := readErrorType(body); got != "budget_unavailable" {
		t.Errorf("error.type = %q, want %q", got, "budget_unavailable")
	}
}

func TestChat_TeamBudgetExhausted_DeniesWith403(t *testing.T) {
	f := newChatFixture(t)
	// Use absurdly high pricing so the smallest request is clearly over
	// budget. 100M cents per 1M tokens = $1 per token; even a few-token
	// prompt blows past a 1-cent cap.
	f.upsertPricing(100_000_000, 100_000_000)
	team := f.createTeam("teamexhausted")
	team = f.setTeamBudget(team, 1)
	rawKey := f.issueKey(team, nil)

	code, body := f.chatPOST(rawKey)
	if code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%s)", code, body)
	}
	if got := readErrorType(body); got != "insufficient_quota" {
		t.Errorf("error.type = %q, want %q", got, "insufficient_quota")
	}
	if !strings.Contains(string(body), "team") {
		t.Errorf("error message should mention which scope was exceeded; body=%s", body)
	}
}

func TestChat_UserBudgetExhausted_DeniesWith403(t *testing.T) {
	// Team has plenty of headroom; only the owner-user budget is tight.
	f := newChatFixture(t)
	f.upsertPricing(100_000_000, 100_000_000)
	team := f.createTeam("userexhausted")
	_ = f.setTeamBudget(team, 1_000_000_000) // huge team headroom
	user, _ := f.createUser("tightbudget", false)
	f.setUserBudget(user, 1) // 1-cent personal cap

	rawKey := f.issueKey(team, user)

	code, body := f.chatPOST(rawKey)
	if code != http.StatusForbidden {
		t.Fatalf("got %d, want 403 (body=%s)", code, body)
	}
	if got := readErrorType(body); got != "insufficient_quota" {
		t.Errorf("error.type = %q, want %q", got, "insufficient_quota")
	}
	if !strings.Contains(string(body), "user") {
		t.Errorf("error should attribute denial to user scope; body=%s", body)
	}
}

func TestChat_OwnerKeyWithTeamHeadroomNoUserBudget_Allows(t *testing.T) {
	// Owner-bound key, team has budget+pricing, user has no personal limit.
	// Should pass admission and proxy upstream.
	f := newChatFixture(t)
	f.upsertPricing(100, 200) // realistic-ish pricing, easy to fit
	team := f.createTeam("ownerOK")
	_ = f.setTeamBudget(team, 100_000)
	user, _ := f.createUser("noUserBudget", false)
	rawKey := f.issueKey(team, user)

	code, body := f.chatPOST(rawKey)
	if code != http.StatusOK {
		t.Fatalf("got %d, want 200 (body=%s)", code, body)
	}
}
