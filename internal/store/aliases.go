package store

import (
	"strings"
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type AliasWithDeployments struct {
	ID              int64
	Alias           string
	CreatedAt       time.Time
	Deployments     []string
	Targets         []AliasTarget
	CacheEnabled    bool
	CacheTTLSeconds int
	Strategy        string
	StrategyOptions map[string]any
}

type AliasTarget struct {
	Deployment string
	Priority   int
	Weight     int
}

func (s *Store) ListAliasesWithDeployments(ctx context.Context) ([]*AliasWithDeployments, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT a.id, a.alias, a.created_at, a.cache_enabled, a.cache_ttl_seconds,
		       a.strategy, a.strategy_options,
		       d.name, ad.priority, ad.weight
		FROM model_aliases a
		LEFT JOIN alias_deployments ad ON ad.alias_id = a.id
		LEFT JOIN deployments d ON d.id = ad.deployment_id
		ORDER BY a.alias, ad.priority, d.name
	`)
	if err != nil {
		return nil, fmt.Errorf("list aliases: %w", err)
	}
	defer rows.Close()
	out := []*AliasWithDeployments{}
	byAlias := map[string]*AliasWithDeployments{}
	for rows.Next() {
		var (
			id          int64
			alias       string
			createdAt   time.Time
			cacheOn     bool
			cacheTTL    int
			strategy    string
			stratOpts   []byte
			depName     *string
			priority    *int
			weight      *int
		)
		if err := rows.Scan(&id, &alias, &createdAt, &cacheOn, &cacheTTL, &strategy, &stratOpts, &depName, &priority, &weight); err != nil {
			return nil, fmt.Errorf("scan alias: %w", err)
		}
		r, ok := byAlias[alias]
		if !ok {
			r = &AliasWithDeployments{
				ID:              id,
				Alias:           alias,
				CreatedAt:       createdAt,
				CacheEnabled:    cacheOn,
				CacheTTLSeconds: cacheTTL,
				Strategy:        strategy,
				StrategyOptions: parseJSONMap(stratOpts),
			}
			byAlias[alias] = r
			out = append(out, r)
		}
		if depName != nil {
			r.Deployments = append(r.Deployments, *depName)
			target := AliasTarget{Deployment: *depName}
			if priority != nil {
				target.Priority = *priority
			}
			if weight != nil {
				target.Weight = *weight
			}
			if target.Weight <= 0 {
				target.Weight = 1
			}
			r.Targets = append(r.Targets, target)
		}
	}
	return out, rows.Err()
}

// ListAliasesWithDeploymentsPage paginates aliases. Routing-critical
// callers (router refresh, /admin/info) still use the unpaginated variant;
// this is for the UI table only.
// ListAliasesWithDeploymentsPage pages aliases with their deployments.
// query matches the alias name; unroutedOnly keeps aliases with no
// deployment attached (every request to them fails).
func (s *Store) ListAliasesWithDeploymentsPage(ctx context.Context, limit, offset int, query string, unroutedOnly bool) ([]*AliasWithDeployments, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(query))
	// Two-step: first page the alias rows so LIMIT counts aliases not
	// alias×deployment fanout rows; then expand deployments for that page.
	rows, err := s.Pool.Query(ctx, `
		WITH page AS (
			SELECT id, alias, created_at, cache_enabled, cache_ttl_seconds, strategy, strategy_options, COUNT(*) OVER () AS total
			FROM model_aliases ma
			WHERE ($3 = '' OR ma.alias ILIKE '%' || $3 || '%')
			  AND (NOT $4 OR NOT EXISTS (SELECT 1 FROM alias_deployments x WHERE x.alias_id = ma.id))
			ORDER BY alias
			LIMIT $1 OFFSET $2
		)
		SELECT p.id, p.alias, p.created_at, p.cache_enabled, p.cache_ttl_seconds, p.strategy, p.strategy_options, p.total,
		       d.name, ad.priority, ad.weight
		FROM page p
		LEFT JOIN alias_deployments ad ON ad.alias_id = p.id
		LEFT JOIN deployments d ON d.id = ad.deployment_id
		ORDER BY p.alias, ad.priority, d.name
	`, limit, offset, pattern, unroutedOnly)
	if err != nil {
		return nil, 0, fmt.Errorf("list aliases page: %w", err)
	}
	defer rows.Close()
	out := []*AliasWithDeployments{}
	byAlias := map[string]*AliasWithDeployments{}
	var total int64
	for rows.Next() {
		var (
			id          int64
			alias       string
			createdAt   time.Time
			cacheOn     bool
			cacheTTL    int
			strategy    string
			stratOpts   []byte
			rowTotal    int64
			depName     *string
			priority    *int
			weight      *int
		)
		if err := rows.Scan(&id, &alias, &createdAt, &cacheOn, &cacheTTL, &strategy, &stratOpts, &rowTotal, &depName, &priority, &weight); err != nil {
			return nil, 0, fmt.Errorf("scan alias: %w", err)
		}
		total = rowTotal
		r, ok := byAlias[alias]
		if !ok {
			r = &AliasWithDeployments{
				ID:              id,
				Alias:           alias,
				CreatedAt:       createdAt,
				CacheEnabled:    cacheOn,
				CacheTTLSeconds: cacheTTL,
				Strategy:        strategy,
				StrategyOptions: parseJSONMap(stratOpts),
			}
			byAlias[alias] = r
			out = append(out, r)
		}
		if depName != nil {
			r.Deployments = append(r.Deployments, *depName)
			target := AliasTarget{Deployment: *depName}
			if priority != nil {
				target.Priority = *priority
			}
			if weight != nil {
				target.Weight = *weight
			}
			if target.Weight <= 0 {
				target.Weight = 1
			}
			r.Targets = append(r.Targets, target)
		}
	}
	return out, total, rows.Err()
}

func (s *Store) UpsertAlias(ctx context.Context, alias string, deploymentNames []string) (*AliasWithDeployments, error) {
	if len(deploymentNames) == 0 {
		return nil, fmt.Errorf("alias %q: at least one deployment is required", alias)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, name := range deploymentNames {
		exists, err := s.deploymentExists(ctx, tx, name)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, fmt.Errorf("deployment %q does not exist", name)
		}
	}

	var aliasID int64
	var createdAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO model_aliases (alias) VALUES ($1)
		ON CONFLICT (alias) DO UPDATE SET alias = EXCLUDED.alias
		RETURNING id, created_at
	`, alias).Scan(&aliasID, &createdAt); err != nil {
		return nil, fmt.Errorf("upsert alias: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM alias_deployments WHERE alias_id = $1`, aliasID); err != nil {
		return nil, fmt.Errorf("clear alias deployments: %w", err)
	}
	for i, name := range deploymentNames {
		if _, err := tx.Exec(ctx, `
			INSERT INTO alias_deployments (alias_id, deployment_id, priority)
			SELECT $1, id, $2 FROM deployments WHERE name = $3
		`, aliasID, i, name); err != nil {
			return nil, fmt.Errorf("link alias→deployment %q: %w", name, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &AliasWithDeployments{
		ID:          aliasID,
		Alias:       alias,
		CreatedAt:   createdAt,
		Deployments: deploymentNames,
	}, nil
}

// UpdateAliasStrategy sets the routing strategy and options on an alias.
// Empty strategy resets to the default ("priority"). Doc 0007.
func (s *Store) UpdateAliasStrategy(ctx context.Context, alias string, strategy string, options map[string]any) error {
	if strategy == "" {
		strategy = "priority"
	}
	if options == nil {
		options = map[string]any{}
	}
	raw, err := json.Marshal(options)
	if err != nil {
		return fmt.Errorf("marshal strategy options: %w", err)
	}
	tag, err := s.Pool.Exec(ctx, `
		UPDATE model_aliases
		SET strategy = $2, strategy_options = $3::jsonb
		WHERE alias = $1
	`, alias, strategy, raw)
	if err != nil {
		return fmt.Errorf("update alias strategy: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateAliasCacheSettings toggles cache_enabled and cache_ttl_seconds on
// an existing alias. Returns ErrNotFound if the alias doesn't exist.
func (s *Store) UpdateAliasCacheSettings(ctx context.Context, alias string, enabled bool, ttlSeconds int) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE model_aliases
		SET cache_enabled = $2, cache_ttl_seconds = $3
		WHERE alias = $1
	`, alias, enabled, ttlSeconds)
	if err != nil {
		return fmt.Errorf("update alias cache: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteAlias(ctx context.Context, alias string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM model_aliases WHERE alias = $1`, alias)
	if err != nil {
		return fmt.Errorf("delete alias: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
