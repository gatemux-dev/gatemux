package store

import (
	"context"
	"fmt"
	"time"
)

// RoutingConcurrencyLimit is global across teams and gateway replicas.
// A model subject names a client-facing alias; a provider subject names a
// deployment's provider_type. NULL clears a cap without changing its identity.
type RoutingConcurrencyLimit struct {
	ID                  int64     `json:"id"`
	Scope               string    `json:"scope"`
	Subject             string    `json:"subject"`
	MaxParallelRequests *int      `json:"max_parallel_requests"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func (s *Store) ListRoutingConcurrencyLimits(ctx context.Context) ([]RoutingConcurrencyLimit, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, scope, subject, max_parallel_requests, updated_at
		FROM routing_concurrency_limits ORDER BY scope, subject`)
	if err != nil {
		return nil, fmt.Errorf("list routing concurrency: %w", err)
	}
	defer rows.Close()
	out := []RoutingConcurrencyLimit{}
	for rows.Next() {
		var p RoutingConcurrencyLimit
		if err := rows.Scan(&p.ID, &p.Scope, &p.Subject, &p.MaxParallelRequests, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SetRoutingConcurrencyLimit(ctx context.Context, scope, subject string, limit *int) (*RoutingConcurrencyLimit, error) {
	var p RoutingConcurrencyLimit
	err := s.Pool.QueryRow(ctx, `INSERT INTO routing_concurrency_limits (scope, subject, max_parallel_requests)
		VALUES ($1, $2, $3) ON CONFLICT (scope, subject) DO UPDATE
		SET max_parallel_requests = EXCLUDED.max_parallel_requests, updated_at = NOW()
		RETURNING id, scope, subject, max_parallel_requests, updated_at`, scope, subject, limit).
		Scan(&p.ID, &p.Scope, &p.Subject, &p.MaxParallelRequests, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("set routing concurrency: %w", err)
	}
	return &p, nil
}
