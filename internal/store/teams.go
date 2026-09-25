package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// JWTTeamConfig is the per-team JWT verification config used by the
// bearer middleware on /v1. Populated only for teams that have set
// jwt_jwks_url. Doc 0008.
type JWTTeamConfig struct {
	TeamID    int64
	TeamSlug  string
	JWKSURL   string
	TeamClaim string
	Audience  string
	Mode      string // "jwt_only" | "jwt_or_key"
}

// ListJWTTeams returns every team that has a JWKS URL configured. The
// JWT verifier loops over these to find the team a token was issued for
// (the team is identified by a claim, e.g. team_slug).
func (s *Store) ListJWTTeams(ctx context.Context) ([]JWTTeamConfig, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, slug,
		       COALESCE(jwt_jwks_url, ''),
		       COALESCE(jwt_team_claim, 'team_slug'),
		       COALESCE(jwt_audience, ''),
		       COALESCE(jwt_mode, 'jwt_or_key')
		FROM teams
		WHERE jwt_jwks_url IS NOT NULL AND jwt_jwks_url <> ''
		  AND archived_at IS NULL
	`)
	if err != nil {
		return nil, fmt.Errorf("list jwt teams: %w", err)
	}
	defer rows.Close()
	out := []JWTTeamConfig{}
	for rows.Next() {
		var c JWTTeamConfig
		if err := rows.Scan(&c.TeamID, &c.TeamSlug, &c.JWKSURL, &c.TeamClaim, &c.Audience, &c.Mode); err != nil {
			return nil, fmt.Errorf("scan jwt team: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetTeamBySlugForJWT loads the team + JWT config in one shot, used by
// the verifier after it identifies which team the JWT belongs to.
func (s *Store) GetTeamBySlugForJWT(ctx context.Context, slug string) (*Team, *JWTTeamConfig, error) {
	team, err := s.GetTeamBySlug(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	cfg := &JWTTeamConfig{}
	err = s.Pool.QueryRow(ctx, `
		SELECT COALESCE(jwt_jwks_url, ''),
		       COALESCE(jwt_team_claim, 'team_slug'),
		       COALESCE(jwt_audience, ''),
		       COALESCE(jwt_mode, 'jwt_or_key')
		FROM teams WHERE id = $1
	`, team.ID).Scan(&cfg.JWKSURL, &cfg.TeamClaim, &cfg.Audience, &cfg.Mode)
	if err != nil {
		return team, nil, fmt.Errorf("get team jwt cfg: %w", err)
	}
	cfg.TeamID = team.ID
	cfg.TeamSlug = team.Slug
	return team, cfg, nil
}

// SumTeamSpendInWindow totals cost_cents on usage_log for the given team
// over [start, end). Used by the projection tile (doc 0009).
func (s *Store) SumTeamSpendInWindow(ctx context.Context, teamID int64, start, end time.Time) (int64, error) {
	var total *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT SUM(cost_cents) FROM usage_log
		WHERE team_id = $1 AND ts >= $2 AND ts < $3
	`, teamID, start, end).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum team spend: %w", err)
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

// SumUserSpendInWindow totals cost_cents on usage_log for keys owned by
// the given user (or service account when applicable) over [start, end).
func (s *Store) SumUserSpendInWindow(ctx context.Context, userID int64, start, end time.Time) (int64, error) {
	var total *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT SUM(cost_cents) FROM usage_log
		WHERE user_id = $1 AND ts >= $2 AND ts < $3
	`, userID, start, end).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum user spend: %w", err)
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

// SumKeySpendInWindow totals cost_cents on usage_log for the given key
// over [start, end). Used by the effective-policy endpoint to show
// per-key budget burn alongside team/user windows.
func (s *Store) SumKeySpendInWindow(ctx context.Context, keyID int64, start, end time.Time) (int64, error) {
	var total *int64
	err := s.Pool.QueryRow(ctx, `
		SELECT SUM(cost_cents) FROM usage_log
		WHERE key_id = $1 AND ts >= $2 AND ts < $3
	`, keyID, start, end).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("sum key spend: %w", err)
	}
	if total == nil {
		return 0, nil
	}
	return *total, nil
}

func (s *Store) CreateTeam(ctx context.Context, slug, name string, usdLimitCents *int64, period string, rpm, tpm, maxParallelRequests *int) (*Team, error) {
	if period == "" {
		period = "month"
	}
	allowed, _ := json.Marshal([]string{"*"})
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO teams (slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8)
		RETURNING id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
	`, slug, name, usdLimitCents, period, allowed, rpm, tpm, maxParallelRequests)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		return nil, fmt.Errorf("create team: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

func (s *Store) GetTeamByID(ctx context.Context, id int64) (*Team, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
		FROM teams WHERE id = $1
	`, id)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get team by id: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

func (s *Store) GetTeamBySlug(ctx context.Context, slug string) (*Team, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
		FROM teams WHERE slug = $1
	`, slug)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get team: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

func (s *Store) ListTeams(ctx context.Context, limit, offset int, query string) ([]*Team, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	// Escape LIKE metacharacters so a search for "100%" matches literally.
	pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(query))
	rows, err := s.Pool.Query(ctx, `
		SELECT id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at,
		       COUNT(*) OVER () AS total
		FROM teams
		WHERE archived_at IS NULL
		  AND ($3 = '' OR slug ILIKE '%' || $3 || '%' OR name ILIKE '%' || $3 || '%')
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset, pattern)
	if err != nil {
		return nil, 0, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()
	out := []*Team{}
	var total int64
	for rows.Next() {
		t := &Team{}
		var allowedRaw []byte
		if err := rows.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan team: %w", err)
		}
		t.AllowedModels = parseAllowedModels(allowedRaw)
		out = append(out, t)
	}
	return out, total, rows.Err()
}

func (s *Store) UpdateTeamBudget(ctx context.Context, slug string, usdLimitCents *int64, period string) (*Team, error) {
	period = normalizeBudgetPeriod(period)
	row := s.Pool.QueryRow(ctx, `
		UPDATE teams
		SET usd_limit_cents = $2, period = $3
		WHERE slug = $1 AND archived_at IS NULL
		RETURNING id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
	`, slug, usdLimitCents, period)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update team budget: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

func (s *Store) UpdateTeamConcurrency(ctx context.Context, slug string, maxParallelRequests *int) (*Team, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE teams
		SET max_parallel_requests = $2
		WHERE slug = $1 AND archived_at IS NULL
		RETURNING id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
	`, slug, maxParallelRequests)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update team concurrency: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

// UpdateTeamRates changes the team-wide request/token per-minute caps. A null
// or zero value clears that cap; the new values apply to each subsequent
// admission decision.
func (s *Store) UpdateTeamRates(ctx context.Context, slug string, rpm, tpm *int) (*Team, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE teams
		SET rpm = $2, tpm = $3
		WHERE slug = $1 AND archived_at IS NULL
		RETURNING id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
	`, slug, rpm, tpm)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update team rates: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

// UpdateTeamAllowedModels replaces the team's model allowlist. ["*"] allows
// every alias. Key-level restrictions can only narrow this list further.
func (s *Store) UpdateTeamAllowedModels(ctx context.Context, slug string, models []string) (*Team, error) {
	normalized := normalizeAllowedModels(models)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("encode allowed models: %w", err)
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE teams
		SET allowed_models = $2::jsonb
		WHERE slug = $1 AND archived_at IS NULL
		RETURNING id, slug, name, usd_limit_cents, period, allowed_models, rpm, tpm, max_parallel_requests, capture_payloads, customer_registration, created_at, archived_at
	`, slug, encoded)
	t := &Team{}
	var allowedRaw []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.UsdLimitCents, &t.Period, &allowedRaw, &t.RPM, &t.TPM, &t.MaxParallelRequests, &t.CapturePayloads, &t.CustomerRegistration, &t.CreatedAt, &t.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update team allowed models: %w", err)
	}
	t.AllowedModels = parseAllowedModels(allowedRaw)
	return t, nil
}

// TeamListStats is the at-a-glance state the teams directory shows next to
// each team: live keys, active members, and spend in the current budget
// period (UTC calendar day or month, matching budget enforcement).
type TeamListStats struct {
	ActiveKeys       int64
	Members          int64
	PeriodSpendCents int64
}

func (s *Store) TeamListStats(ctx context.Context, teamIDs []int64) (map[int64]TeamListStats, error) {
	out := make(map[int64]TeamListStats, len(teamIDs))
	if len(teamIDs) == 0 {
		return out, nil
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id,
		       (SELECT COUNT(*) FROM virtual_keys k
		         WHERE k.team_id = t.id AND k.revoked_at IS NULL AND (k.expires_at IS NULL OR k.expires_at > NOW())),
		       (SELECT COUNT(*) FROM users u WHERE u.team_id = t.id AND u.archived_at IS NULL),
		       (SELECT COALESCE(SUM(l.cost_cents), 0) FROM usage_log l
		         WHERE l.team_id = t.id
		           AND l.ts >= date_trunc(CASE WHEN t.period = 'day' THEN 'day' ELSE 'month' END, NOW() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')
		FROM teams t
		WHERE t.id = ANY($1)`, teamIDs)
	if err != nil {
		return nil, fmt.Errorf("team list stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st TeamListStats
		if err := rows.Scan(&id, &st.ActiveKeys, &st.Members, &st.PeriodSpendCents); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}
