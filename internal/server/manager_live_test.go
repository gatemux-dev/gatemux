package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This opt-in browser test uses production routes, embedded assets, real
// password logins and HttpOnly cookies. Its private schema has no providers,
// callbacks or external authentication configured. No intercepted responses.
func TestManagerLiveBrowser(t *testing.T) {
	if os.Getenv("GATEMUX_MANAGER_BROWSER_TEST") != "1" {
		t.Skip("set GATEMUX_MANAGER_BROWSER_TEST=1 after building web assets")
	}
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL must explicitly name a disposable database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	root, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	suffix := hex.EncodeToString(entropy[:])
	schema := "manager_live_" + suffix
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := root.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		// Only the unique schema created by this test is removed.
		if _, err := root.Exec(cleanupCtx, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("remove browser fixture schema: %v", err)
		}
	}()
	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = schema
	poolCfg.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	st := &store.Store{Pool: pool}
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	team, err := st.CreateTeam(ctx, "pilot-team", "Pilot team", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := st.CreateTeam(ctx, "foreign-team", "Foreign team", nil, "month", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	password := "Fixture-" + suffix
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := st.CreateUser(ctx, "manager@example.test", "Pilot manager", hash, &team.ID, store.RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	member, err := st.CreateUser(ctx, "member@example.test", "Pilot member", hash, &team.ID, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := st.CreateUser(ctx, "foreign@example.test", "Foreign member", hash, &foreign.ID, store.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	// More than one member page, without generating costly login hashes.
	for i := 0; i < 26; i++ {
		if _, err := pool.Exec(ctx, `INSERT INTO users(email, name, password_hash, team_id, role) VALUES ($1, 'Directory fixture', $2, $3, 'member')`, "z-"+string(rune('a'+i))+"@example.test", hash, team.ID); err != nil {
			t.Fatal(err)
		}
	}
	for _, alias := range []string{"allowed-model", "hidden-model"} {
		if _, err := pool.Exec(ctx, `INSERT INTO model_aliases(alias) VALUES ($1)`, alias); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE teams SET allowed_models='["allowed-model"]'::jsonb WHERE id=$1`, team.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateServiceAccount(ctx, store.CreateServiceAccountParams{TeamID: team.ID, Name: "pilot-ci"}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertUsage(ctx, store.UsageEntry{TeamID: team.ID, UserID: &member.ID, Alias: "allowed-model", RequestID: "pilot-request", PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12, CostCents: 7, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO usage_log_payloads(usage_id, request_body) SELECT id, '{"messages":[{"role":"user","content":"fixture-only private payload"}]}'::jsonb FROM usage_log WHERE request_id='pilot-request'`); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertUsage(ctx, store.UsageEntry{TeamID: foreign.ID, UserID: &otherUser.ID, Alias: "hidden-model", RequestID: "foreign-request", CostCents: 99, StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEMUX_MANAGER_FIXTURE_MASTER", "fixture-master-"+suffix)
	cfg := &config.Config{Admin: config.AdminConfig{MasterKeyEnv: "GATEMUX_MANAGER_FIXTURE_MASTER"}}
	srv, err := New(cfg, st, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Shutdown(context.Background()) }()
	httpServer := httptest.NewServer(srv.http.Handler)
	defer httpServer.Close()
	fixture, _ := json.Marshal(map[string]any{
		"base": httpServer.URL, "managerEmail": manager.Email, "memberEmail": member.Email,
		"foreignEmail": otherUser.Email, "password": password, "memberID": member.ID,
		"team": team.Slug, "foreignTeam": foreign.Slug,
		"masterKey": os.Getenv("GATEMUX_MANAGER_FIXTURE_MASTER"),
	})
	cmd := exec.CommandContext(ctx, "node", "../../web/tests/manager.live.cjs")
	cmd.Stdin = bytes.NewReader(fixture)
	// Never print credentials, request bodies or whole HTML on failure.
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live manager browser: %v\n%s", err, strings.ReplaceAll(string(output), password, "[redacted]"))
	}
	t.Log(strings.TrimSpace(string(output)))
	if os.Getenv("GATEMUX_ACCOUNTING_BROWSER_TEST") == "1" {
		for _, state := range []string{"priced", "estimated", "unknown", "unpriced", "not_billable"} {
			cost := int64(0)
			if state == "estimated" {
				cost = 123
			}
			if err := st.InsertUsage(ctx, store.UsageEntry{TeamID: team.ID, Alias: "evidence-" + state, RequestID: "evidence-" + state, Accounting: state, CostCents: cost, StatusCode: 200}); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.CommandContext(ctx, "node", "../../web/tests/accounting.live.cjs")
		cmd.Stdin = bytes.NewReader(fixture)
		output, err := cmd.CombinedOutput()
		if err != nil {
			safe := strings.ReplaceAll(string(output), os.Getenv("GATEMUX_MANAGER_FIXTURE_MASTER"), "[redacted]")
			t.Fatalf("accounting browser: %v\n%s", err, safe)
		}
		t.Log(strings.TrimSpace(string(output)))
	}
	if os.Getenv("GATEMUX_GUARDRAIL_BROWSER_TEST") == "1" {
		cmd := exec.CommandContext(ctx, "node", "../../web/tests/guardrails.live.cjs")
		cmd.Stdin = bytes.NewReader(fixture)
		output, err := cmd.CombinedOutput()
		if err != nil {
			safe := strings.ReplaceAll(string(output), os.Getenv("GATEMUX_MANAGER_FIXTURE_MASTER"), "[redacted]")
			t.Fatalf("guardrail browser: %v\n%s", err, safe)
		}
		t.Log(strings.TrimSpace(string(output)))
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action='guardrails.update'`).Scan(&count); err != nil || count < 4 {
			t.Fatalf("guardrail writes not persisted: %d %v", count, err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM virtual_keys WHERE team_id=$1 AND user_id=$2 AND name='pilot-browser-key' AND revoked_at IS NOT NULL`, team.ID, member.ID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("real key issue/revoke did not persist: count=%d err=%v", count, err)
	}
	customer, err := st.GetCustomerByExternalID(ctx, team.ID, "pilot-customer")
	if err != nil || customer.UsdLimitCents == nil || *customer.UsdLimitCents != 500 || customer.RPM == nil || *customer.RPM != 20 {
		t.Fatalf("real customer edits did not persist: %v", err)
	}
}
