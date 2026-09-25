package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// AlertRule mirrors the alert_rules table (doc 0009). Phase 1 supports
// budget-threshold and budget-exceeded triggers; the evaluator can grow
// to handle circuit and key-revocation events too.
type AlertRule struct {
	ID               int64
	Name             string
	Enabled          bool
	ScopeType        string
	ScopeID          *int64
	TriggerType      string
	ThresholdOptions map[string]any
	ChannelType      string
	ChannelTarget    string
	CooldownSeconds  int
	CreatedAt        time.Time
}

// ListAllAlertRules returns every rule (enabled or disabled). The
// evaluator's separate ListAlertRules path filters to enabled=true; the
// admin UI needs disabled rules visible too so users can re-enable them.
func (s *Store) ListAllAlertRules(ctx context.Context) ([]*AlertRule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, enabled, scope_type, scope_id, trigger_type, threshold_options,
		       channel_type, channel_target, cooldown_seconds, created_at
		FROM alert_rules
		ORDER BY id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list all alert rules: %w", err)
	}
	defer rows.Close()
	out := []*AlertRule{}
	for rows.Next() {
		r := &AlertRule{}
		var thresholdRaw []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.ScopeType, &r.ScopeID, &r.TriggerType,
			&thresholdRaw, &r.ChannelType, &r.ChannelTarget, &r.CooldownSeconds, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		if len(thresholdRaw) > 0 {
			r.ThresholdOptions = map[string]any{}
			_ = json.Unmarshal(thresholdRaw, &r.ThresholdOptions)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetAlertRuleEnabled flips the enabled flag. Returns ErrNotFound when
// the rule doesn't exist so the handler can surface 404.
func (s *Store) SetAlertRuleEnabled(ctx context.Context, id int64, enabled bool) error {
	cmd, err := s.Pool.Exec(ctx, `UPDATE alert_rules SET enabled = $2 WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("update alert rule: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAlertRule removes a rule and its events (FK cascade).
func (s *Store) DeleteAlertRule(ctx context.Context, id int64) error {
	cmd, err := s.Pool.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AlertEventRow is one row from the alert_events feed, augmented with
// the rule name so the UI can render it without a join.
type AlertEventRow struct {
	ID             int64          `json:"id"`
	RuleID         int64          `json:"rule_id"`
	RuleName       string         `json:"rule_name"`
	FiredAt        time.Time      `json:"fired_at"`
	Payload        map[string]any `json:"payload"`
	DeliveryStatus string         `json:"delivery_status"`
	DeliveryError  string         `json:"delivery_error,omitempty"`
}

// ListAlertEvents returns the most recent alert firings, joined with
// rule names. Bounded by limit so a long history doesn't bloat the page.
func (s *Store) ListAlertEvents(ctx context.Context, limit int) ([]AlertEventRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT e.id, e.rule_id, r.name, e.fired_at, e.payload, e.delivery_status, COALESCE(e.delivery_error, '')
		FROM alert_events e
		JOIN alert_rules r ON r.id = e.rule_id
		ORDER BY e.fired_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list alert events: %w", err)
	}
	defer rows.Close()
	out := []AlertEventRow{}
	for rows.Next() {
		row := AlertEventRow{}
		var raw []byte
		if err := rows.Scan(&row.ID, &row.RuleID, &row.RuleName, &row.FiredAt, &raw, &row.DeliveryStatus, &row.DeliveryError); err != nil {
			return nil, fmt.Errorf("scan alert event: %w", err)
		}
		if len(raw) > 0 {
			row.Payload = map[string]any{}
			_ = json.Unmarshal(raw, &row.Payload)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) ListAlertRules(ctx context.Context) ([]*AlertRule, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, enabled, scope_type, scope_id, trigger_type, threshold_options,
		       channel_type, channel_target, cooldown_seconds, created_at
		FROM alert_rules
		WHERE enabled = true
		ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()
	out := []*AlertRule{}
	for rows.Next() {
		r := &AlertRule{}
		var thresholdRaw []byte
		if err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.ScopeType, &r.ScopeID, &r.TriggerType,
			&thresholdRaw, &r.ChannelType, &r.ChannelTarget, &r.CooldownSeconds, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		if len(thresholdRaw) > 0 {
			r.ThresholdOptions = map[string]any{}
			_ = json.Unmarshal(thresholdRaw, &r.ThresholdOptions)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastAlertEventTime returns the last fired_at for a rule, used by the
// evaluator's cooldown check. Returns zero time on no prior firing.
func (s *Store) LastAlertEventTime(ctx context.Context, ruleID int64) (time.Time, error) {
	var t *time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT MAX(fired_at) FROM alert_events WHERE rule_id = $1
	`, ruleID).Scan(&t)
	if err != nil {
		return time.Time{}, fmt.Errorf("last alert event: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}

func (s *Store) RecordAlertEvent(ctx context.Context, ruleID int64, payload map[string]any, deliveryStatus string, deliveryErr string) error {
	raw, _ := json.Marshal(payload)
	var deliveryErrPtr *string
	if deliveryErr != "" {
		deliveryErrPtr = &deliveryErr
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO alert_events (rule_id, payload, delivery_status, delivery_error)
		VALUES ($1, $2::jsonb, $3, $4)
	`, ruleID, raw, deliveryStatus, deliveryErrPtr)
	if err != nil {
		return fmt.Errorf("insert alert event: %w", err)
	}
	return nil
}

type CreateAlertRuleParams struct {
	Name             string
	ScopeType        string
	ScopeID          *int64
	TriggerType      string
	ThresholdOptions map[string]any
	ChannelType      string
	ChannelTarget    string
	CooldownSeconds  int
}

func (s *Store) CreateAlertRule(ctx context.Context, p CreateAlertRuleParams) (*AlertRule, error) {
	if p.CooldownSeconds <= 0 {
		p.CooldownSeconds = 300
	}
	if p.ThresholdOptions == nil {
		p.ThresholdOptions = map[string]any{}
	}
	thresh, _ := json.Marshal(p.ThresholdOptions)
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO alert_rules (name, scope_type, scope_id, trigger_type, threshold_options, channel_type, channel_target, cooldown_seconds)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		RETURNING id, name, enabled, scope_type, scope_id, trigger_type, threshold_options, channel_type, channel_target, cooldown_seconds, created_at
	`, p.Name, p.ScopeType, p.ScopeID, p.TriggerType, thresh, p.ChannelType, p.ChannelTarget, p.CooldownSeconds)
	r := &AlertRule{}
	var thresholdRaw []byte
	if err := row.Scan(&r.ID, &r.Name, &r.Enabled, &r.ScopeType, &r.ScopeID, &r.TriggerType,
		&thresholdRaw, &r.ChannelType, &r.ChannelTarget, &r.CooldownSeconds, &r.CreatedAt); err != nil {
		return nil, fmt.Errorf("create alert rule: %w", err)
	}
	if len(thresholdRaw) > 0 {
		r.ThresholdOptions = map[string]any{}
		_ = json.Unmarshal(thresholdRaw, &r.ThresholdOptions)
	}
	return r, nil
}

// AlertTrafficWindow summarises /v1 traffic over [since, now) for alert
// evaluation, optionally scoped to one team. P95Ms is 0 without traffic.
type AlertTrafficWindow struct {
	Requests     int64
	ServerErrors int64
	P95Ms        float64
}

func (s *Store) AlertTraffic(ctx context.Context, teamID *int64, since time.Time) (AlertTrafficWindow, error) {
	var w AlertTrafficWindow
	err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE status_code >= 500),
		       COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY latency_ms), 0)
		FROM usage_log
		WHERE ts >= $1 AND ($2::bigint IS NULL OR team_id = $2)`, since, teamID).Scan(&w.Requests, &w.ServerErrors, &w.P95Ms)
	if err != nil {
		return w, fmt.Errorf("alert traffic: %w", err)
	}
	return w, nil
}

// UnavailableDeployments lists enabled deployments whose every health
// sample since `since` was not ready or had an open circuit, so a single
// blip doesn't page anyone.
func (s *Store) UnavailableDeployments(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT h.deployment_name
		FROM provider_health_samples h
		JOIN deployments d ON d.name = h.deployment_name AND d.enabled
		WHERE h.sampled_at >= $1
		GROUP BY h.deployment_name
		HAVING bool_and(NOT h.ready OR h.circuit = 'open')
		ORDER BY h.deployment_name`, since)
	if err != nil {
		return nil, fmt.Errorf("unavailable deployments: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
