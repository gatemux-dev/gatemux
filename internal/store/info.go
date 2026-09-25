package store

import (
	"context"
	"fmt"
)

type Counts struct {
	Teams          int
	Users          int
	ActiveKeys     int
	PendingInvites int
}

func (s *Store) GetCounts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.Pool.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM teams WHERE archived_at IS NULL),
			(SELECT COUNT(*) FROM users WHERE archived_at IS NULL),
			(SELECT COUNT(*) FROM virtual_keys WHERE revoked_at IS NULL),
			(SELECT COUNT(*) FROM invites WHERE accepted_at IS NULL AND expires_at > NOW())
	`).Scan(&c.Teams, &c.Users, &c.ActiveKeys, &c.PendingInvites)
	if err != nil {
		return c, fmt.Errorf("get counts: %w", err)
	}
	return c, nil
}
