package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// testEnv wires a real Postgres-backed Store, the budget service, and a chi
// router to the API handlers under test. Tests that need a database call
// newTestEnv at the top of TestX. A disposable TEST_DATABASE_URL is required;
// never fall back to the persistent local preview database.
type testEnv struct {
	t         *testing.T
	Store     *store.Store
	Budget    *budget.Service
	Router    chi.Router
	MasterKey string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL must explicitly name a disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("test postgres unavailable: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("test postgres ping failed: %v", err)
	}

	s := &store.Store{Pool: pool}
	if err := store.Migrate(ctx, pool); err != nil {
		pool.Close()
		t.Fatalf("migrate test database: %v", err)
	}
	b := budget.New(s)
	masterKey := "test-master-" + randHex(8)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	meH := &MeHandler{Store: s, Budget: b}
	adminH := &AdminHandler{Store: s, Config: &config.Config{}, Version: "test", Logger: logger}

	r := chi.NewRouter()

	r.Route("/me", func(r chi.Router) {
		r.Use(auth.Session(masterKey, s))
		r.Get("/keys", meH.ListKeys)
		r.Get("/usage", meH.ListUsage)
		r.Get("/budget", meH.GetBudget)
	})

	// Mirror server.go's admin route layout so role boundary tests run
	// against the same access tiers production uses.
	r.Route("/admin", func(r chi.Router) {
		r.Use(auth.Session(masterKey, s))

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireAdmin)
			r.Get("/users", adminH.ListUsers)
			r.Post("/users/{id}/budget", adminH.UpdateUserBudget)
			r.Patch("/users/{id}/concurrency", adminH.UpdateUserConcurrency)
			r.Patch("/service-accounts/{id}/concurrency", adminH.UpdateServiceAccountConcurrency)
			r.Post("/service-accounts/{id}/keys", adminH.CreateServiceAccountKey)
			r.Post("/teams", adminH.CreateTeam)
			r.Get("/pricing", adminH.ListPricing)
			r.Get("/guardrails", adminH.ListGuardrails)
			r.Get("/guardrails/assignments", adminH.ListGuardrailAssignments)
			r.Post("/guardrails/test", adminH.TestGuardrails)
			r.Get("/audit", adminH.ListAudit)
			r.Get("/audit/facets", adminH.GetAuditFacets)
			r.Get("/guardrails/{scope}/{subject}", adminH.GetGuardrailScope)
			r.Put("/guardrails/{scope}/{subject}", adminH.SetGuardrailScope)
			r.Put("/teams/{slug}/models", adminH.UpdateTeamAllowedModels)
			r.Post("/pricing", adminH.UpsertPricing)
			r.Get("/deployments", adminH.ListDeployments)
			r.Get("/concurrency/routing", adminH.ListRoutingConcurrencyLimits)
			r.Put("/concurrency/routing", adminH.SetRoutingConcurrencyLimit)
			r.Post("/deployments", adminH.CreateDeployment)
			r.Patch("/deployments/{name}", adminH.UpdateDeployment)
			r.Post("/deployments/{name}/test", adminH.TestDeploymentConnection)
			r.Delete("/deployments/{name}", adminH.DeleteDeployment)
			r.Get("/aliases", adminH.ListAliases)
			r.Post("/aliases", adminH.UpsertAlias)
			r.Delete("/aliases/{alias}", adminH.DeleteAlias)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireTeamAccess("slug"))
			r.Get("/teams/{slug}", adminH.GetTeam)
			r.Get("/teams/{slug}/members", adminH.ListTeamMembers)
			r.Get("/teams/{slug}/models", adminH.ListTeamModels)
			r.Post("/teams/{slug}/budget", adminH.UpdateTeamBudget)
			r.Patch("/teams/{slug}/concurrency", adminH.UpdateTeamConcurrency)
			r.Patch("/teams/{slug}/rates", adminH.UpdateTeamRates)
			r.Get("/teams/{slug}/keys", adminH.ListKeys)
			r.Post("/teams/{slug}/keys", adminH.CreateKey)
			r.Post("/teams/{slug}/service-accounts", adminH.CreateServiceAccount)
			r.Get("/teams/{slug}/customers", adminH.ListCustomers)
			r.Patch("/teams/{slug}/customer-policy", adminH.SetCustomerRegistration)
			r.Post("/teams/{slug}/customers", adminH.CreateCustomer)
			r.Patch("/teams/{slug}/customers/{externalID}", adminH.UpdateCustomer)
			r.Get("/teams/{slug}/customers/{externalID}/budget", adminH.GetCustomerBudget)
			r.Patch("/teams/{slug}/customers/{externalID}/concurrency", adminH.SetCustomerConcurrency)
		})

		r.Group(func(r chi.Router) {
			r.Use(auth.RequireManagerOrAdmin)
			r.Get("/teams", adminH.ListTeams)
			r.Get("/usage", adminH.ListUsage)
			r.Get("/usage/facets", adminH.GetUsageFacets)
			r.Get("/usage/{id}", adminH.GetUsageRow)
			r.Get("/spend", adminH.GetSpendReport)
			r.Get("/invites", adminH.ListInvites)
			r.Post("/invites", adminH.CreateInvite)
			r.Patch("/keys/{id}", adminH.UpdateKey)
			r.Get("/keys/{id}/budget", adminH.GetKeyBudget)
			r.Patch("/keys/{id}/budget", adminH.SetKeyBudget)
			r.Post("/keys/{id}/rotate", adminH.RotateKey)
			r.Post("/keys/{id}/revoke", adminH.RevokeKey)
			r.Post("/keys/{id}/pause", adminH.PauseKey)
			r.Post("/keys/{id}/resume", adminH.ResumeKey)
		})
	})

	t.Cleanup(func() { pool.Close() })

	return &testEnv{
		t:         t,
		Store:     s,
		Budget:    b,
		Router:    r,
		MasterKey: masterKey,
	}
}

func (e *testEnv) GET(path, token string) (int, []byte) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	body, _ := io.ReadAll(rec.Body)
	return rec.Code, body
}

func (e *testEnv) POST(path, token string, body any) (int, []byte) {
	e.t.Helper()
	var reader io.Reader
	if body != nil {
		buf, _ := json.Marshal(body)
		reader = bytes.NewReader(buf)
	}
	req := httptest.NewRequest(http.MethodPost, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

func (e *testEnv) DELETE(path, token string) (int, []byte) {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

func (e *testEnv) PATCH(path, token string, body any) (int, []byte) {
	e.t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPatch, path, bytes.NewReader(buf))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

func (e *testEnv) PUT(path, token string, body any) (int, []byte) {
	e.t.Helper()
	buf, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(buf))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.Router.ServeHTTP(rec, req)
	respBody, _ := io.ReadAll(rec.Body)
	return rec.Code, respBody
}

func (e *testEnv) createUser(suffix string, isAdmin bool) (*store.User, string) {
	e.t.Helper()
	role := store.RoleMember
	if isAdmin {
		role = store.RoleAdmin
	}
	return e.createUserWithRole(suffix, role, nil)
}

func (e *testEnv) createUserWithRole(suffix, role string, teamID *int64) (*store.User, string) {
	e.t.Helper()
	email := fmt.Sprintf("test-%s-%s@example.com", suffix, randHex(4))
	pwHash, err := auth.HashPassword("hunter22!")
	if err != nil {
		e.t.Fatalf("hash password: %v", err)
	}
	user, err := e.Store.CreateUser(context.Background(), email, "Test "+suffix, pwHash, teamID, role)
	if err != nil {
		e.t.Fatalf("create user: %v", err)
	}
	rawToken, hash, err := auth.GenerateSessionToken()
	if err != nil {
		e.t.Fatalf("generate session: %v", err)
	}
	if err := e.Store.CreateSession(context.Background(), hash, user.ID, time.Now().Add(time.Hour)); err != nil {
		e.t.Fatalf("create session: %v", err)
	}
	return user, rawToken
}

func (e *testEnv) createTeam(suffix string) *store.Team {
	e.t.Helper()
	slug := fmt.Sprintf("test-%s-%s", suffix, randHex(4))
	team, err := e.Store.CreateTeam(context.Background(), slug, "Test "+suffix, nil, "month", nil, nil, nil)
	if err != nil {
		e.t.Fatalf("create team: %v", err)
	}
	return team
}

func (e *testEnv) createKeyForUser(team *store.Team, user *store.User) *store.VirtualKey {
	e.t.Helper()
	_, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		e.t.Fatalf("generate key: %v", err)
	}
	uid := user.ID
	vk, err := e.Store.CreateVirtualKey(context.Background(), store.CreateVirtualKeyParams{
		TeamID:  team.ID,
		UserID:  &uid,
		KeyHash: hash,
		Prefix:  prefix,
		Name:    "test-" + randHex(4),
	})
	if err != nil {
		e.t.Fatalf("create virtual key: %v", err)
	}
	return vk
}

// insertUsageRow writes a single row directly to usage_log so /me/usage tests
// don't need to round-trip through the v1 handler. It's a convenience for
// asserting visibility filtering, not behavior.
func (e *testEnv) insertUsageRow(team *store.Team, user *store.User, key *store.VirtualKey, alias string) {
	e.t.Helper()
	uid := user.ID
	kid := key.ID
	if err := e.Store.InsertUsage(context.Background(), store.UsageEntry{
		TeamID:           team.ID,
		UserID:           &uid,
		KeyID:            &kid,
		Alias:            alias,
		DeploymentName:   "test-dep",
		RequestID:        "req-" + randHex(8),
		ModelRequested:   alias,
		ModelUsed:        "test-upstream",
		PromptTokens:     10,
		CompletionTokens: 5,
		TotalTokens:      15,
		LatencyMs:        100,
		StatusCode:       200,
	}); err != nil {
		e.t.Fatalf("insert usage: %v", err)
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
