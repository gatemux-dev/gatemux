package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Pricing struct {
	Tiers                 PricingTiers
	ProviderType          string
	UpstreamModel         string
	InputPerMillionCents  int64
	OutputPerMillionCents int64
	EffectiveAt           time.Time
}

type ModelBudget struct {
	Alias      string
	LimitCents int64
	Period     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type SpendFilter struct {
	TeamSlug           string
	CustomerExternalID string
	UserID             *int64
	From               time.Time
	To                 time.Time
	// GroupBy adds a Breakdown grouped by "team", "key", "user" or
	// "customer" alongside the always-present per-alias rows.
	GroupBy string
}

// SpendGroup is one row of a SpendReport breakdown. Key is the stable
// identifier (slug, key prefix, user email, customer id); Label is the
// human name when one exists.
type SpendGroup struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	SpendTotals
}

type SpendTotals struct {
	Requests         int64 `json:"requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	CostCents        int64 `json:"cost_cents"`
}

type SpendByAlias struct {
	Alias string `json:"alias"`
	SpendTotals
}

type SpendReport struct {
	TeamSlug           string         `json:"team_slug,omitempty"`
	CustomerExternalID string         `json:"customer_external_id,omitempty"`
	UserID             *int64         `json:"user_id,omitempty"`
	From               time.Time      `json:"from"`
	To                 time.Time      `json:"to"`
	Total              SpendTotals    `json:"total"`
	Aliases            []SpendByAlias `json:"aliases"`
	GroupBy            string         `json:"group_by,omitempty"`
	Breakdown          []SpendGroup   `json:"breakdown,omitempty"`
}

// spendGroupColumns maps a GroupBy dimension to its key and label columns
// and the joins they need. Unknown dimensions are rejected by the caller.
var spendGroupColumns = map[string]struct{ key, label, join string }{
	"team":     {"t.slug", "t.name", ""},
	"key":      {"COALESCE(vk.key_prefix, '(no key)')", "COALESCE(vk.name, '')", "LEFT JOIN virtual_keys vk ON vk.id = u.key_id"},
	"user":     {"COALESCE(usr.email, '(no user)')", "COALESCE(usr.name, '')", "LEFT JOIN users usr ON usr.id = u.user_id"},
	"customer": {"COALESCE(u.customer_external_id, '(no customer)')", "''", ""},
}

// ValidSpendGroupBy reports whether dim is a supported breakdown.
func ValidSpendGroupBy(dim string) bool {
	_, ok := spendGroupColumns[dim]
	return ok
}

func normalizeBudgetPeriod(period string) string {
	switch period {
	case "day", "month":
		return period
	default:
		return "month"
	}
}

func (s *Store) UpsertPricing(ctx context.Context, providerType, upstreamModel string, inputPerMillion, outputPerMillion int64, tierOptions ...PricingTiers) (*Pricing, error) {
	var tiers PricingTiers
	if len(tierOptions) > 0 {
		tiers = tierOptions[0]
	}
	if err := tiers.Validate(); err != nil {
		return nil, err
	}
	if inputPerMillion < 0 || outputPerMillion < 0 {
		return nil, fmt.Errorf("pricing values must be non-negative")
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO pricing (provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers, effective_at
	`, providerType, upstreamModel, inputPerMillion, outputPerMillion, tiers)
	p := &Pricing{}
	if err := row.Scan(&p.ProviderType, &p.UpstreamModel, &p.InputPerMillionCents, &p.OutputPerMillionCents, &p.Tiers, &p.EffectiveAt); err != nil {
		return nil, fmt.Errorf("upsert pricing: %w", err)
	}
	return p, nil
}

func (s *Store) GetCurrentPricing(ctx context.Context, providerType, upstreamModel string) (*Pricing, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers, effective_at
		FROM pricing
		WHERE provider_type = $1 AND upstream_model = $2
		ORDER BY effective_at DESC
		LIMIT 1
	`, providerType, upstreamModel)
	p := &Pricing{}
	if err := row.Scan(&p.ProviderType, &p.UpstreamModel, &p.InputPerMillionCents, &p.OutputPerMillionCents, &p.Tiers, &p.EffectiveAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get pricing: %w", err)
	}
	return p, nil
}

func (s *Store) ListCurrentPricing(ctx context.Context) ([]*Pricing, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (provider_type, upstream_model)
		       provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers, effective_at
		FROM pricing
		ORDER BY provider_type, upstream_model, effective_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list pricing: %w", err)
	}
	defer rows.Close()
	out := []*Pricing{}
	for rows.Next() {
		p := &Pricing{}
		if err := rows.Scan(&p.ProviderType, &p.UpstreamModel, &p.InputPerMillionCents, &p.OutputPerMillionCents, &p.Tiers, &p.EffectiveAt); err != nil {
			return nil, fmt.Errorf("scan pricing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListCurrentPricingPage paginates the current pricing rows. The cost
// engine still uses the unpaginated ListCurrentPricing on every request, so
// this is a UI-only path.
func (s *Store) ListCurrentPricingPage(ctx context.Context, limit, offset int) ([]*Pricing, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		WITH current AS (
			SELECT DISTINCT ON (provider_type, upstream_model)
			       provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers, effective_at
			FROM pricing
			ORDER BY provider_type, upstream_model, effective_at DESC
		)
		SELECT provider_type, upstream_model, input_per_million_cents, output_per_million_cents, tiers, effective_at,
		       COUNT(*) OVER () AS total
		FROM current
		ORDER BY provider_type, upstream_model
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list pricing page: %w", err)
	}
	defer rows.Close()
	out := []*Pricing{}
	var total int64
	for rows.Next() {
		p := &Pricing{}
		if err := rows.Scan(&p.ProviderType, &p.UpstreamModel, &p.InputPerMillionCents, &p.OutputPerMillionCents, &p.Tiers, &p.EffectiveAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan pricing: %w", err)
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) UpsertModelBudget(ctx context.Context, alias string, limitCents int64, period string) (*ModelBudget, error) {
	period = normalizeBudgetPeriod(period)
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO model_budgets (alias, limit_cents, period)
		VALUES ($1, $2, $3)
		ON CONFLICT (alias) DO UPDATE SET
			limit_cents = EXCLUDED.limit_cents,
			period = EXCLUDED.period,
			updated_at = NOW()
		RETURNING alias, limit_cents, period, created_at, updated_at
	`, alias, limitCents, period)
	b := &ModelBudget{}
	if err := row.Scan(&b.Alias, &b.LimitCents, &b.Period, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, fmt.Errorf("upsert model budget: %w", err)
	}
	return b, nil
}

func (s *Store) GetModelBudget(ctx context.Context, alias string) (*ModelBudget, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT alias, limit_cents, period, created_at, updated_at
		FROM model_budgets
		WHERE alias = $1
	`, alias)
	b := &ModelBudget{}
	if err := row.Scan(&b.Alias, &b.LimitCents, &b.Period, &b.CreatedAt, &b.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get model budget: %w", err)
	}
	return b, nil
}

func (s *Store) ListModelBudgets(ctx context.Context) ([]*ModelBudget, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT alias, limit_cents, period, created_at, updated_at
		FROM model_budgets
		ORDER BY alias
	`)
	if err != nil {
		return nil, fmt.Errorf("list model budgets: %w", err)
	}
	defer rows.Close()
	out := []*ModelBudget{}
	for rows.Next() {
		b := &ModelBudget{}
		if err := rows.Scan(&b.Alias, &b.LimitCents, &b.Period, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan model budget: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) GetSpendReport(ctx context.Context, f SpendFilter) (*SpendReport, error) {
	if f.From.IsZero() {
		f.From = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	if f.To.IsZero() {
		f.To = time.Now().UTC()
	}
	report := &SpendReport{
		TeamSlug:           f.TeamSlug,
		CustomerExternalID: f.CustomerExternalID,
		UserID:             f.UserID,
		From:               f.From,
		To:                 f.To,
		Aliases:            []SpendByAlias{},
	}

	baseArgs := []any{f.From, f.To}
	where := `u.ts >= $1 AND u.ts < $2`
	nextArg := 3
	if f.TeamSlug != "" {
		where += fmt.Sprintf(` AND t.slug = $%d`, nextArg)
		baseArgs = append(baseArgs, f.TeamSlug)
		nextArg++
	}
	if f.UserID != nil {
		where += fmt.Sprintf(` AND u.user_id = $%d`, nextArg)
		baseArgs = append(baseArgs, *f.UserID)
		nextArg++
	}
	if f.CustomerExternalID != "" {
		if f.TeamSlug == "" {
			return nil, fmt.Errorf("customer spend requires a team filter")
		}
		where += fmt.Sprintf(` AND u.customer_external_id = $%d`, nextArg)
		baseArgs = append(baseArgs, f.CustomerExternalID)
	}

	totalQuery := `
		SELECT COUNT(*), COALESCE(SUM(u.prompt_tokens), 0), COALESCE(SUM(u.completion_tokens), 0),
		       COALESCE(SUM(u.total_tokens), 0), COALESCE(SUM(u.cost_cents), 0)
		FROM usage_log u
		JOIN teams t ON t.id = u.team_id
		WHERE ` + where
	if err := s.Pool.QueryRow(ctx, totalQuery, baseArgs...).Scan(
		&report.Total.Requests,
		&report.Total.PromptTokens,
		&report.Total.CompletionTokens,
		&report.Total.TotalTokens,
		&report.Total.CostCents,
	); err != nil {
		return nil, fmt.Errorf("get spend totals: %w", err)
	}

	aliasQuery := `
		SELECT u.alias, COUNT(*), COALESCE(SUM(u.prompt_tokens), 0), COALESCE(SUM(u.completion_tokens), 0),
		       COALESCE(SUM(u.total_tokens), 0), COALESCE(SUM(u.cost_cents), 0)
		FROM usage_log u
		JOIN teams t ON t.id = u.team_id
		WHERE ` + where + `
		GROUP BY u.alias
		ORDER BY COALESCE(SUM(u.cost_cents), 0) DESC, u.alias`
	rows, err := s.Pool.Query(ctx, aliasQuery, baseArgs...)
	if err != nil {
		return nil, fmt.Errorf("get spend by alias: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		row := SpendByAlias{}
		if err := rows.Scan(
			&row.Alias,
			&row.Requests,
			&row.PromptTokens,
			&row.CompletionTokens,
			&row.TotalTokens,
			&row.CostCents,
		); err != nil {
			return nil, fmt.Errorf("scan spend row: %w", err)
		}
		report.Aliases = append(report.Aliases, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	if cols, ok := spendGroupColumns[f.GroupBy]; ok {
		report.GroupBy = f.GroupBy
		report.Breakdown = []SpendGroup{}
		groupQuery := `
			SELECT ` + cols.key + `, ` + cols.label + `, COUNT(*), COALESCE(SUM(u.prompt_tokens), 0),
			       COALESCE(SUM(u.completion_tokens), 0), COALESCE(SUM(u.total_tokens), 0), COALESCE(SUM(u.cost_cents), 0)
			FROM usage_log u
			JOIN teams t ON t.id = u.team_id
			` + cols.join + `
			WHERE ` + where + `
			GROUP BY 1, 2
			ORDER BY 7 DESC, 1
			LIMIT 500`
		grows, err := s.Pool.Query(ctx, groupQuery, baseArgs...)
		if err != nil {
			return nil, fmt.Errorf("get spend breakdown: %w", err)
		}
		defer grows.Close()
		for grows.Next() {
			g := SpendGroup{}
			if err := grows.Scan(&g.Key, &g.Label, &g.Requests, &g.PromptTokens, &g.CompletionTokens, &g.TotalTokens, &g.CostCents); err != nil {
				return nil, fmt.Errorf("scan spend breakdown: %w", err)
			}
			report.Breakdown = append(report.Breakdown, g)
		}
		return report, grows.Err()
	}
	return report, nil
}
