// Package alerts implements the periodic alert-rule evaluator (doc 0009).
// The evaluator runs every 30 seconds, walks each enabled alert_rules
// row, computes the trigger condition, and routes through the callback
// bus on a fire. Cooldowns prevent alert storms.
package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

const (
	evalInterval = 30 * time.Second
)

type Evaluator struct {
	Store  *store.Store
	Logger *slog.Logger
}

func New(s *store.Store, log *slog.Logger) *Evaluator {
	return &Evaluator{Store: s, Logger: log}
}

// Start runs the evaluator until ctx is done.
func (e *Evaluator) Start(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(evalInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, cancel := context.WithTimeout(ctx, 10*time.Second)
				e.evaluate(work)
				cancel()
			}
		}
	}()
	return done
}

func (e *Evaluator) evaluate(ctx context.Context) {
	rules, err := e.Store.ListAlertRules(ctx)
	if err != nil {
		if e.Logger != nil {
			e.Logger.Warn("alert evaluator load failed", "err", err)
		}
		return
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			return
		}
		e.evaluateRule(ctx, rule)
	}
}

func (e *Evaluator) evaluateRule(ctx context.Context, rule *store.AlertRule) {
	last, err := e.Store.LastAlertEventTime(ctx, rule.ID)
	if err != nil {
		return
	}
	cooldown := time.Duration(rule.CooldownSeconds) * time.Second
	if !last.IsZero() && time.Since(last) < cooldown {
		return
	}
	switch rule.TriggerType {
	case "budget_threshold":
		e.evaluateBudgetThreshold(ctx, rule)
	case "budget_exceeded":
		// The same check pinned at 100%, so the rule reads as intended
		// in the console without a threshold field.
		exceeded := *rule
		exceeded.ThresholdOptions = map[string]any{"threshold_pct": 100.0}
		e.evaluateBudgetThreshold(ctx, &exceeded)
	case "error_rate":
		e.evaluateTraffic(ctx, rule, "error_rate")
	case "latency_p95":
		e.evaluateTraffic(ctx, rule, "latency_p95")
	case "provider_unavailable":
		e.evaluateProviders(ctx, rule)
	}
}

// Trigger and channel vocabularies, shared with the admin API so a rule
// the evaluator can't act on is rejected at creation instead of saving
// silently and never firing.
var (
	Triggers = map[string]bool{"budget_threshold": true, "budget_exceeded": true, "error_rate": true, "latency_p95": true, "provider_unavailable": true}
	Channels = map[string]bool{"slack": true, "webhook": true}
)

func option(rule *store.AlertRule, key string, fallback float64) float64 {
	switch v := rule.ThresholdOptions[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return fallback
}

// evaluateTraffic fires on the server-error rate (percent of requests with
// a 5xx) or the p95 latency over the last window_minutes, scoped to one
// team or the whole gateway. min_requests keeps quiet periods from firing
// on a handful of requests. threshold_options:
//
//	{ "threshold": 5, "window_minutes": 15, "min_requests": 20 }
func (e *Evaluator) evaluateTraffic(ctx context.Context, rule *store.AlertRule, metric string) {
	window := time.Duration(option(rule, "window_minutes", 15)) * time.Minute
	minRequests := int64(option(rule, "min_requests", 20))
	threshold := option(rule, "threshold", map[string]float64{"error_rate": 5, "latency_p95": 5000}[metric])
	var teamID *int64
	if rule.ScopeType == "team" {
		teamID = rule.ScopeID
	}
	w, err := e.Store.AlertTraffic(ctx, teamID, time.Now().UTC().Add(-window))
	if err != nil || w.Requests < minRequests {
		return
	}
	value := w.P95Ms
	if metric == "error_rate" {
		value = 100 * float64(w.ServerErrors) / float64(w.Requests)
	}
	if value < threshold {
		return
	}
	e.fire(ctx, rule, map[string]any{
		"metric": metric, "value": value, "threshold": threshold,
		"requests": w.Requests, "window_minutes": window.Minutes(), "scope": rule.ScopeType,
	})
}

// evaluateProviders fires when enabled deployments have been unavailable
// for every health sample in the last window_minutes (default 5).
func (e *Evaluator) evaluateProviders(ctx context.Context, rule *store.AlertRule) {
	window := time.Duration(option(rule, "window_minutes", 5)) * time.Minute
	names, err := e.Store.UnavailableDeployments(ctx, time.Now().UTC().Add(-window))
	if err != nil || len(names) == 0 {
		return
	}
	e.fire(ctx, rule, map[string]any{"deployments": names, "window_minutes": window.Minutes()})
}

func (e *Evaluator) fire(ctx context.Context, rule *store.AlertRule, payload map[string]any) {
	delivErr := e.deliver(ctx, rule, payload)
	status := "delivered"
	if delivErr != "" {
		status = "failed"
	}
	_ = e.Store.RecordAlertEvent(ctx, rule.ID, payload, status, delivErr)
}

// evaluateBudgetThreshold fires when the scope's spend crosses the
// configured threshold percentage of its budget. threshold_options:
// { "threshold_pct": 80 } — supports 50, 80, 100 (or any number).
func (e *Evaluator) evaluateBudgetThreshold(ctx context.Context, rule *store.AlertRule) {
	if rule.ScopeType != "team" || rule.ScopeID == nil {
		return
	}
	thresholdPct := 80.0
	if v, ok := rule.ThresholdOptions["threshold_pct"]; ok {
		switch t := v.(type) {
		case float64:
			thresholdPct = t
		case int:
			thresholdPct = float64(t)
		}
	}
	team, err := e.Store.GetTeamByID(ctx, *rule.ScopeID)
	if err != nil || team.UsdLimitCents == nil || *team.UsdLimitCents <= 0 {
		return
	}
	start, end := windowFor(team.Period, time.Now().UTC())
	spend, err := e.Store.SumTeamSpendInWindow(ctx, team.ID, start, end)
	if err != nil {
		return
	}
	pct := 100.0 * float64(spend) / float64(*team.UsdLimitCents)
	if pct < thresholdPct {
		return
	}
	payload := map[string]any{
		"team_slug":     team.Slug,
		"limit_cents":   *team.UsdLimitCents,
		"spend_cents":   spend,
		"threshold_pct": thresholdPct,
		"actual_pct":    pct,
		"period":        team.Period,
		"period_start":  start,
		"period_end":    end,
	}
	e.fire(ctx, rule, payload)
}

func (e *Evaluator) deliver(ctx context.Context, rule *store.AlertRule, payload map[string]any) string {
	switch rule.ChannelType {
	case "slack":
		return slackDeliver(ctx, rule.ChannelTarget, slackText(rule, payload))
	case "webhook":
		return webhookDeliver(ctx, rule.ChannelTarget, payload)
	}
	return "unsupported channel: " + rule.ChannelType
}

func slackText(rule *store.AlertRule, payload map[string]any) string {
	switch rule.TriggerType {
	case "budget_threshold":
		return fmt.Sprintf(
			":warning: Team *%v* has used %.1f%% of its %v budget (limit $%.2f, spent $%.2f)",
			payload["team_slug"], payload["actual_pct"], payload["period"],
			float64(payload["limit_cents"].(int64))/100,
			float64(payload["spend_cents"].(int64))/100,
		)
	case "budget_exceeded":
		return fmt.Sprintf(":rotating_light: Team *%v* has exceeded its %v budget", payload["team_slug"], payload["period"])
	case "error_rate":
		return fmt.Sprintf(":warning: *%s*: %.1f%% of %v requests failed with a server error in the last %v minutes",
			rule.Name, payload["value"], payload["requests"], payload["window_minutes"])
	case "latency_p95":
		return fmt.Sprintf(":snail: *%s*: p95 latency is %.0f ms over the last %v minutes (%v requests)",
			rule.Name, payload["value"], payload["window_minutes"], payload["requests"])
	case "provider_unavailable":
		return fmt.Sprintf(":red_circle: *%s*: unavailable for %v minutes: %v", rule.Name, payload["window_minutes"], payload["deployments"])
	}
	return fmt.Sprintf("Alert *%s* fired", rule.Name)
}

func slackDeliver(ctx context.Context, webhookURL string, text string) string {
	body, _ := json.Marshal(map[string]any{"text": text})
	req, err := http.NewRequestWithContext(ctx, "POST", webhookURL, bytes.NewReader(body))
	if err != nil {
		return err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("slack status %d", resp.StatusCode)
	}
	return ""
}

func webhookDeliver(ctx context.Context, url string, payload map[string]any) string {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
	if err != nil {
		return err.Error()
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Sprintf("webhook status %d", resp.StatusCode)
	}
	return ""
}

func windowFor(period string, now time.Time) (time.Time, time.Time) {
	switch period {
	case "day":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.Add(24 * time.Hour)
	case "week":
		offset := int(now.Weekday())
		start := time.Date(now.Year(), now.Month(), now.Day()-offset, 0, 0, 0, 0, time.UTC)
		return start, start.Add(7 * 24 * time.Hour)
	default:
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
}
