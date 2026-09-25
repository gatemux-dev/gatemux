package store

import (
	"context"
	"fmt"
	"strconv"
)

// CustomerConcurrencySubject is only a snapshot lookup key. Redis memberships
// use the customer's stable database ID, never client-supplied strings.
func CustomerConcurrencySubject(teamID int64, externalID string) string {
	return strconv.FormatInt(teamID, 10) + ":" + externalID
}

func (s *Store) ListCustomerConcurrencyLimits(ctx context.Context) ([]RoutingConcurrencyLimit, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id, team_id, external_id, max_parallel_requests FROM customers
		WHERE max_parallel_requests IS NOT NULL AND archived_at IS NULL`)
	if err != nil {
		return nil, fmt.Errorf("load customer concurrency: %w", err)
	}
	defer rows.Close()
	out := []RoutingConcurrencyLimit{}
	for rows.Next() {
		var row RoutingConcurrencyLimit
		var teamID int64
		var externalID string
		if err := rows.Scan(&row.ID, &teamID, &externalID, &row.MaxParallelRequests); err != nil {
			return nil, err
		}
		row.Scope, row.Subject = "customer", CustomerConcurrencySubject(teamID, externalID)
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) SetCustomerConcurrency(ctx context.Context, teamID int64, externalID string, limit *int) (*Customer, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE customers SET max_parallel_requests = $3
		WHERE team_id = $1 AND external_id = $2 AND archived_at IS NULL`, teamID, externalID, limit)
	if err != nil {
		return nil, fmt.Errorf("set customer concurrency: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetCustomerByExternalID(ctx, teamID, externalID)
}
