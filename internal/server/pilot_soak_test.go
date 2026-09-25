package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/loadtest"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/redis/go-redis/v9"
)

// Opt-in, strictly local configured-policy stability evidence. This is not a
// gateway-only performance benchmark: gateway, mock and bounded clients share
// this Go process. It never contacts paid providers or the preview database.
// Short diagnostic runs exercise the same checks but cannot qualify the gate.
type soakSample struct {
	Elapsed        float64        `json:"elapsed_seconds"`
	HeapInUse      uint64         `json:"process_heap_in_use_bytes"`
	RuntimeSys     uint64         `json:"process_runtime_sys_bytes"`
	Goroutines     int            `json:"process_goroutines"`
	GoroutineKinds map[string]int `json:"process_goroutine_kinds"`
	InFlight       int64          `json:"gateway_in_flight"`
	Queued         int64          `json:"gateway_queued"`
	Upstream       int64          `json:"upstream_in_flight"`
	CallbackQueued int            `json:"callback_queued"`
	Ready          bool           `json:"ready"`
}

// Fixed-size stack-PC capture only: no request arguments, local values or raw
// stack text enter the report. Classify a closed vocabulary to diagnose growth.
func soakGoroutineKinds() map[string]int {
	records := make([]runtime.StackRecord, 1025)
	n, ok := runtime.GoroutineProfile(records)
	if !ok {
		return map[string]int{"profile_overflow": n}
	}
	out := map[string]int{}
	for _, record := range records[:n] {
		kind := "other"
		frames := runtime.CallersFrames(record.Stack())
		for {
			frame, more := frames.Next()
			switch {
			case strings.Contains(frame.Function, "net/http.(*persistConn).readLoop"):
				kind = "http_client_read"
			case strings.Contains(frame.Function, "net/http.(*persistConn).writeLoop"):
				kind = "http_client_write"
			case strings.Contains(frame.Function, "net/http.(*conn).serve"):
				kind = "http_server"
			case strings.Contains(frame.Function, "github.com/redis/"):
				kind = "redis"
			case strings.Contains(frame.Function, "github.com/jackc/"):
				kind = "postgres"
			case strings.Contains(frame.Function, "/internal/loadtest."):
				kind = "load_client"
			}
			if !more {
				break
			}
		}
		out[kind]++
	}
	return out
}

type soakAudit struct {
	UpstreamCalls      int64 `json:"upstream_calls"`
	Intents            int64 `json:"intents"`
	PendingIntents     int64 `json:"pending_intents"`
	Usage              int64 `json:"usage_rows"`
	JournaledUsage     int64 `json:"journaled_usage"`
	GuardrailDenials   int64 `json:"guardrail_denial_usage"`
	MissingPreAudits   int64 `json:"missing_pre_guardrail_audits"`
	MissingPostAudits  int64 `json:"missing_post_guardrail_audits"`
	Reservations       int64 `json:"reservations"`
	Unsettled          int64 `json:"unsettled_reservations"`
	UsageCost          int64 `json:"usage_cost_cents"`
	SettledCost        int64 `json:"settled_cost_cents"`
	RemainingLeases    int64 `json:"remaining_distributed_leases"`
	GlobalInFlight     int64 `json:"remaining_global_in_flight"`
	GlobalQueued       int64 `json:"remaining_global_queued"`
	DeploymentInFlight int64 `json:"remaining_deployment_in_flight"`
	BudgetDrift        int64 `json:"budget_daily_mismatches"`
}

func soakDelay(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func soakPolicies(t *testing.T, st *store.Store) []string {
	t.Helper()
	ctx := context.Background()
	budget, limit, rpm, tpm := int64(100000000), 12, 12000, 1000000
	var keys []string
	for i := range 2 {
		team, err := st.CreateTeam(ctx, fmt.Sprintf("soak-team-%d", i), "Soak fixture", &budget, "month", &rpm, &tpm, &limit)
		if err != nil {
			t.Fatal(err)
		}
		raw, hash, prefix, err := auth.GenerateKey(team.Slug)
		if err != nil {
			t.Fatal(err)
		}
		vk, err := st.CreateVirtualKey(ctx, store.CreateVirtualKeyParams{TeamID: team.ID, KeyHash: hash, Prefix: prefix, Name: "soak", AllowedModels: []string{"soak"}, ScopedRPM: &rpm, ScopedTPM: &tpm, MaxParallelRequests: &limit})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool.Exec(ctx, `UPDATE virtual_keys SET scoped_usd_limit_cents=$1 WHERE id=$2`, budget, vk.ID); err != nil {
			t.Fatal(err)
		}
		if err := st.SetCustomerRegistration(ctx, team.ID, "required"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.CreateCustomer(ctx, store.CreateCustomerParams{TeamID: team.ID, ExternalID: "soak-customer", Name: "Soak", UsdLimitCents: &budget, Period: "month", RPM: &rpm, TPM: &tpm, MaxParallelRequests: &limit}); err != nil {
			t.Fatal(err)
		}
		policy := json.RawMessage(`[{"name":"soak","type":"banned_terms","mode":"block","phase":"both","terms":["forbidden-marker"]}]`)
		if err := st.ReplaceGuardrailScope(ctx, "team", team.Slug, json.RawMessage(`[]`), policy, "soak-fixture"); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, raw)
	}
	if _, err := st.UpsertPricing(ctx, "openai", "soak-model", 1000000, 1000000); err != nil {
		t.Fatal(err)
	}
	shared := 24
	for scope, subject := range map[string]string{"model": "soak", "provider": "openai"} {
		if _, err := st.SetRoutingConcurrencyLimit(ctx, scope, subject, &shared); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func soakBody(stream bool, content string) []byte {
	body, _ := json.Marshal(map[string]any{"model": "soak", "messages": []map[string]string{{"role": "user", "content": content}}, "stream": stream, "max_tokens": 16, "user": "soak-customer"})
	return body
}

func collectSoakAudit(t *testing.T, s *Server, st *store.Store, calls int64) soakAudit {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	a := soakAudit{UpstreamCalls: calls}
	err := st.Pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM inference_journal),
		(SELECT count(*) FROM inference_journal WHERE state <> 'complete'),
		(SELECT count(*) FROM usage_log),
		(SELECT count(*) FROM usage_log WHERE accounting_id IS NOT NULL),
		(SELECT count(*) FROM usage_log WHERE error='guardrail_blocked'),
		(SELECT count(*) FROM usage_log u WHERE NOT EXISTS(SELECT 1 FROM guardrail_decisions d WHERE d.request_id=u.request_id AND d.phase='pre')),
		(SELECT count(*) FROM usage_log u WHERE u.status_code=200 AND NOT EXISTS(SELECT 1 FROM guardrail_decisions d WHERE d.request_id=u.request_id AND d.phase='post')),
		(SELECT count(*) FROM budget_reservations),
		(SELECT count(*) FROM budget_reservations WHERE status <> 'settled'),
		(SELECT COALESCE(sum(cost_cents),0) FROM usage_log),
		(SELECT COALESCE(sum(settled_cost_cents),0) FROM budget_reservations)`).Scan(&a.Intents, &a.PendingIntents, &a.Usage, &a.JournaledUsage, &a.GuardrailDenials, &a.MissingPreAudits, &a.MissingPostAudits, &a.Reservations, &a.Unsettled, &a.UsageCost, &a.SettledCost)
	if err != nil {
		t.Fatal(err)
	}
	// Independent source-ledger oracle: never let faster admission hide drift.
	err = st.Pool.QueryRow(ctx, `WITH charges AS (
	 SELECT team_id,user_id,service_account_id,key_id,customer_id,created_at AS ts,
	 CASE WHEN status='reserved' THEN estimated_cost_cents ELSE settled_cost_cents END AS cost
	 FROM budget_reservations WHERE status IN ('reserved','settled')
	 UNION ALL SELECT u.team_id,u.user_id,u.service_account_id,u.key_id,u.customer_id,u.ts,u.cost_cents
	 FROM usage_log u WHERE NOT EXISTS (SELECT 1 FROM budget_reservations b WHERE b.request_id=u.request_id AND b.team_id=u.team_id)
	), expected AS (
	 SELECT v.scope,v.subject_id,(ts AT TIME ZONE 'UTC')::date AS day,SUM(cost) AS cost_cents
	 FROM charges CROSS JOIN LATERAL (VALUES ('team',team_id),('user',user_id),
	 ('service_account',service_account_id),('key',key_id),('customer',customer_id)) v(scope,subject_id)
	 WHERE v.subject_id IS NOT NULL GROUP BY v.scope,v.subject_id,(ts AT TIME ZONE 'UTC')::date
	) SELECT count(*) FROM expected e FULL JOIN budget_daily_totals a USING(scope,subject_id,day)
	 WHERE COALESCE(e.cost_cents,0)<>COALESCE(a.cost_cents,0)`).Scan(&a.BudgetDrift)
	if err != nil {
		t.Fatal(err)
	}
	rc := redis.NewClient(&redis.Options{Addr: s.cfg.Redis.Addr, ReadTimeout: time.Second, ContextTimeoutEnabled: true, MaxRetries: -1})
	defer rc.Close()
	// SCAN is confined to this uniquely owned fixture prefix; never FLUSHDB.
	iter := rc.Scan(ctx, 0, s.cfg.Redis.KeyPrefix+":concurrency:*", 100).Iterator()
	for iter.Next(ctx) {
		n, err := rc.ZCard(ctx, iter.Val()).Result()
		if err != nil {
			t.Fatal(err)
		}
		a.RemainingLeases += n
	}
	if err := iter.Err(); err != nil {
		t.Fatal(err)
	}
	stats := s.admission.Stats()
	a.GlobalInFlight, a.GlobalQueued = stats.InFlight, stats.Queued
	for _, h := range s.policyRegistry.Health() {
		a.DeploymentInFlight += int64(h.InFlight)
	}
	return a
}

func TestPilotConfiguredPolicySoak(t *testing.T) {
	if os.Getenv("GATEMUX_PILOT_SOAK") != "1" {
		t.Skip("set GATEMUX_PILOT_SOAK=1 for local configured-policy soak")
	}
	if os.Getenv("GATEMUX_TEST_REDIS_ADDR") == "" {
		t.Fatal("explicit disposable Redis address required")
	}
	duration := 30 * time.Minute
	if value := os.Getenv("GATEMUX_SOAK_DURATION"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < time.Second || parsed > 2*time.Hour {
			t.Fatal("soak duration must be 1s–2h")
		}
		duration = parsed
	}
	reportPath := os.Getenv("GATEMUX_SOAK_REPORT")
	if !filepath.IsAbs(reportPath) {
		t.Fatal("GATEMUX_SOAK_REPORT must be an explicit absolute output path")
	}
	// Exclusive creation prevents accidentally replacing another run's evidence.
	file, err := os.OpenFile(reportPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"schema_version": 1, "qualified": false, "measurement_scope": "co-located gateway, mock and load generator Go process; local Postgres/Redis; not comparative performance", "duration_seconds": duration.Seconds(), "profile": "two teams; each 24 RPS chat + 8 RPS SSE + 1 RPS expected guardrail block", "bounds": map[string]any{"global_active": 32, "global_queued": 16, "deployment": 24, "model": 24, "provider": 24, "team_key_customer": 12, "rpm_each_scope": 12000, "tpm_each_scope": 1000000, "budget_cents_each_scope": 100000000, "client_active_per_profile": 32, "sample_period_seconds": 10, "max_heap_bytes": 512 << 20, "max_goroutines": 1024, "max_heap_window_growth_bytes": 64 << 20, "max_goroutine_window_growth": 32}}
	t.Cleanup(func() {
		defer file.Close()
		report["test_failed"] = t.Failed()
		if t.Failed() {
			report["qualified"] = false
		}
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(report); err != nil {
			t.Errorf("write soak report: %v", err)
		}
	})
	revision, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	report["source_commit"] = strings.TrimSpace(string(revision))
	binaryPath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Open(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	_, err = io.Copy(digest, binary)
	_ = binary.Close()
	if err != nil {
		t.Fatal(err)
	}
	report["test_binary_sha256"] = hex.EncodeToString(digest.Sum(nil))
	report["tracked_worktree_clean"] = exec.Command("git", "diff", "--quiet", "HEAD", "--").Run() == nil
	untracked, err := exec.Command("git", "ls-files", "--others", "--exclude-standard", "--", ":(top)cmd", ":(top)internal", ":(top)web/src").Output()
	if err != nil {
		t.Fatal(err)
	}
	if len(untracked) != 0 {
		report["tracked_worktree_clean"] = false
	}
	report["environment"] = map[string]any{"goos": runtime.GOOS, "goarch": runtime.GOARCH, "go_version": runtime.Version(), "gomaxprocs": runtime.GOMAXPROCS(0), "logical_cpus": runtime.NumCPU()}
	st, prefix := recoveryStore(t)
	cfg := recoveryConfig(t, prefix)
	cfg.Server.Admission = config.AdmissionConfig{MaxInFlight: 32, MaxQueued: 16, QueueTimeout: 100 * time.Millisecond}
	cfg.Server.Shutdown.GracePeriod = 100 * time.Millisecond
	var calls, active, peak atomic.Int64
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&body) != nil {
			w.WriteHeader(400)
			return
		}
		if !soakDelay(r.Context(), 50*time.Millisecond) {
			return
		}
		if !body.Stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":"soak-%d","object":"chat.completion","model":"soak-model","choices":[{"index":0,"message":{"role":"assistant","content":"hello from local mock"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`, id)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ {
			fmt.Fprintf(w, "data: {\"id\":\"soak-%d\",\"object\":\"chat.completion.chunk\",\"model\":\"soak-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello from local mock\"}}]}\n\n", id)
			w.(http.Flusher).Flush()
			if i == 0 && len(body.Messages) > 0 && body.Messages[0].Content == "stall-until-cancel" {
				<-r.Context().Done()
				return
			}
			if !soakDelay(r.Context(), 40*time.Millisecond) {
				return
			}
		}
		fmt.Fprintf(w, "data: {\"id\":\"soak-%d\",\"model\":\"soak-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\ndata: [DONE]\n\n", id)
	}))
	t.Cleanup(mock.Close)
	cfg.Deployments = []config.DeploymentConfig{{Name: "soak", Type: "openai", UpstreamModel: "soak-model", APIKeyEnv: "GATEMUX_RECOVERY_PROVIDER", BaseURL: mock.URL, MaxParallelRequests: 24}}
	cfg.Aliases = []config.AliasConfig{{Alias: "soak", Deployments: []string{"soak"}}}
	keys := soakPolicies(t, st)
	diagnostics := &soakLogHandler{}
	s, err := New(cfg, st, slog.New(diagnostics))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()); report["diagnostic_logs"] = diagnostics.snapshot() })
	base := serveLifecycle(t, s, s.http.Handler)
	ctx, cancel := context.WithTimeout(context.Background(), duration+30*time.Second)
	defer cancel()
	started := time.Now()
	report["started_at"] = started.UTC()
	samples := make([]soakSample, 0, int(duration/(10*time.Second))+2)
	sample := func() {
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		stats := s.admission.Stats()
		w := httptest.NewRecorder()
		s.handleReady(w, httptest.NewRequest("GET", "/readyz", nil))
		queued := 0
		for _, stat := range s.callbacks.Stats() {
			queued += stat.QueueLen
		}
		samples = append(samples, soakSample{Elapsed: time.Since(started).Seconds(), HeapInUse: mem.HeapInuse, RuntimeSys: mem.Sys, Goroutines: runtime.NumGoroutine(), InFlight: stats.InFlight, Queued: stats.Queued, Upstream: active.Load(), CallbackQueued: queued, Ready: w.Code == 200})
		samples[len(samples)-1].GoroutineKinds = soakGoroutineKinds()
	}
	sample()
	type result struct {
		value loadtest.Result
		err   error
	}
	results := make(chan result, 6)
	var clients sync.WaitGroup
	for team, key := range keys {
		for _, profile := range []struct {
			name       string
			rate       float64
			stream     bool
			content    string
			status     int
			validation string
		}{{"chat", 24, false, "hello", 200, "chat"}, {"stream", 8, true, "hello", 200, "sse"}, {"guardrail-block", 1, false, "forbidden-marker", 403, "none"}} {
			clients.Add(1)
			go func(team int, key string, p struct {
				name       string
				rate       float64
				stream     bool
				content    string
				status     int
				validation string
			}) {
				defer clients.Done()
				value, err := loadtest.Run(ctx, loadtest.Config{Label: fmt.Sprintf("team-%d-%s", team, p.name), URL: base + "/v1/chat/completions", Token: key, Body: soakBody(p.stream, p.content), Rate: p.rate, Duration: duration, RequestTimeout: 5 * time.Second, MaxInFlight: 32, ExpectedStatus: p.status, Validation: p.validation})
				value.Config.URL = "/v1/chat/completions"
				results <- result{value, err}
			}(team, key, profile)
		}
	}
	completed := make(chan struct{})
	go func() { clients.Wait(); close(completed) }()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	running := true
	for running {
		select {
		case <-ticker.C:
			sample()
			current := samples[len(samples)-1]
			t.Logf("soak %.0fs heap=%.1fMiB goroutines=%d active=%d queue=%d ready=%t", current.Elapsed, float64(current.HeapInUse)/(1<<20), current.Goroutines, current.InFlight, current.Queued, current.Ready)
		case <-completed:
			running = false
		}
	}
	elapsed := time.Since(started)
	report["elapsed_monotonic_seconds"] = elapsed.Seconds()
	report["finished_at"] = time.Now().UTC()
	var measured []loadtest.Result
	var passed, denied uint64
	for range 6 {
		r := <-results
		if r.err != nil {
			t.Errorf("load profile failed: %v", r.err)
		}
		measured = append(measured, r.value)
		if r.value.Counts.HTTPError+r.value.Counts.TransportError+r.value.Counts.ClientDropped != 0 {
			t.Errorf("unexpected load errors/drops: %s %+v", r.value.Config.Label, r.value.Counts)
		}
		if r.value.Config.ExpectedStatus == 200 {
			passed += r.value.Counts.Succeeded
		} else {
			denied += r.value.Counts.Succeeded
		}
		delta := math.Abs(r.value.FinishedAt.Sub(r.value.StartedAt).Seconds() - r.value.MeasurementWallSeconds)
		if delta > math.Max(2, r.value.MeasurementWallSeconds*.02) {
			t.Error("clock discontinuity invalidates soak")
		}
	}
	sort.Slice(measured, func(i, j int) bool { return measured[i].Config.Label < measured[j].Config.Label })
	report["profiles"] = measured
	report["measurement_diagnostic_logs"] = diagnostics.snapshot()
	if err := s.waitInference(ctx); err != nil {
		t.Fatal(err)
	}
	sample()
	report["samples"] = samples
	before := collectSoakAudit(t, s, st, calls.Load())
	report["post_measurement_audit"] = before
	if before.UpstreamCalls != int64(passed) || before.Intents != int64(passed+denied) || before.JournaledUsage != int64(passed+denied) || before.Usage != int64(passed+denied) || before.GuardrailDenials != int64(denied) {
		t.Errorf("load audit mismatch: %+v successful=%d denied=%d", before, passed, denied)
	}
	assertSoakRecovered(t, before)
	for _, v := range samples {
		if v.HeapInUse > 512<<20 || v.Goroutines > 1024 || v.InFlight > 32 || v.Queued > 16 || v.Upstream > 24 || v.CallbackQueued > 10000 || !v.Ready {
			t.Errorf("sample outside declared bounds: %+v", v)
		}
	}
	if len(samples) >= 14 {
		// Compare warmed six-sample windows, excluding idle endpoints. Fixed
		// tolerances are declared before the run, not fitted to its result.
		first, last := samples[1:7], samples[len(samples)-7:len(samples)-1]
		var earlyHeap, lateHeap uint64
		var earlyG, lateG int
		for i := range first {
			earlyHeap += first[i].HeapInUse
			lateHeap += last[i].HeapInUse
			earlyG += first[i].Goroutines
			lateG += last[i].Goroutines
		}
		if lateHeap > earlyHeap+6*(64<<20) || lateG > earlyG+6*32 {
			t.Error("memory/goroutine windows did not plateau within declared tolerance")
		}
	}
	// Two client disconnects and one forced grace expiry after measurement.
	client := &http.Client{Timeout: 5 * time.Second}
	probe := func(id string) *http.Response {
		r, _ := http.NewRequest("POST", base+"/v1/chat/completions", strings.NewReader(string(soakBody(true, "stall-until-cancel"))))
		r.Header.Set("Authorization", "Bearer "+keys[0])
		r.Header.Set("X-Request-ID", id)
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			t.Fatalf("probe rejected: %d", resp.StatusCode)
		}
		return resp
	}
	for i := range 2 {
		resp := probe(fmt.Sprintf("soak-disconnect-%d", i))
		_ = resp.Body.Close()
		if err := s.waitInference(ctx); err != nil {
			t.Fatal(err)
		}
	}
	resp := probe("soak-forced-drain")
	if err := s.Shutdown(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("forced drain did not report grace expiry: %v", err)
	}
	_ = resp.Body.Close()
	assertWorkersJoined(t, s)
	report["callback_final_stats"] = s.callbacks.Stats()
	for _, stat := range s.callbacks.Stats() {
		if stat.Failed != 0 || stat.QueueLen != 0 {
			t.Errorf("callback did not recover: %+v", stat)
		}
	}
	// New instance, same private database/prefix: no global metric collisions
	// or leftover distributed capacity, and existing policies still apply.
	restarted := newRecoveryServer(t, cfg, st)
	restartedBase := serveLifecycle(t, restarted, restarted.http.Handler)
	for _, key := range keys {
		r, _ := http.NewRequest("POST", restartedBase+"/v1/chat/completions", strings.NewReader(string(soakBody(false, "hello"))))
		r.Header.Set("Authorization", "Bearer "+key)
		response, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("restart did not recover: %d", response.StatusCode)
		}
	}
	if err := restarted.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertWorkersJoined(t, restarted)
	after := collectSoakAudit(t, restarted, st, calls.Load())
	report["post_disconnect_drain_restart_audit"] = after
	assertSoakRecovered(t, after)
	if after.Intents != before.Intents+5 || after.UpstreamCalls != before.UpstreamCalls+5 || after.JournaledUsage != after.Intents {
		t.Errorf("probe/restart audit incomplete: %+v", after)
	}
	var uncertain int
	if err := st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM usage_log WHERE request_id IN ('soak-disconnect-0','soak-disconnect-1','soak-forced-drain') AND accounting_state='estimated' AND cost_cents>0`).Scan(&uncertain); err != nil {
		t.Fatal(err)
	}
	if uncertain != 3 {
		t.Errorf("interrupted work lost its conservative estimate: %d", uncertain)
	}
	report["disconnect_probes"] = 2
	report["forced_drain_probes"] = 1
	report["restart_requests"] = 2
	report["peak_upstream_in_flight"] = peak.Load()
	if peak.Load() > 24 {
		t.Error("upstream exceeded configured concurrency cap")
	}
	report["qualified"] = !t.Failed() && duration >= 30*time.Minute && report["tracked_worktree_clean"] == true
	t.Logf("soak completed: qualified=%v requests=%d expected_blocks=%d audit_rows=%d", report["qualified"], passed, denied, after.Usage)
}

func assertSoakRecovered(t *testing.T, a soakAudit) {
	t.Helper()
	if a.BudgetDrift+a.PendingIntents+a.Unsettled+a.MissingPreAudits+a.MissingPostAudits+a.RemainingLeases+a.DeploymentInFlight != 0 || a.GlobalInFlight+a.GlobalQueued != 0 || a.Reservations != a.UpstreamCalls || a.JournaledUsage != a.Intents || a.UsageCost != a.SettledCost || a.UsageCost <= 0 {
		t.Errorf("incomplete recovery/accounting: %+v", a)
	}
}
