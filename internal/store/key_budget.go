package store

import (
	"context"
	"errors"
)

// MaxKeyBudgetCents is the largest exactly representable JSON integer in the UI.
const MaxKeyBudgetCents int64 = 9_007_199_254_740_991

// Zero retains the existing key-budget meaning: no key-specific cap. It does
// not disable team/owner/customer budgets. Null is preferred for clearing a cap.
func ValidateKeyBudget(limit *int64) error {
	if limit != nil && (*limit < 0 || *limit > MaxKeyBudgetCents) {
		return errors.New("usd_limit_cents must be null or an integer between 0 and 9007199254740991; zero means no key cap")
	}
	return nil
}

// SetVirtualKeyBudget changes only the cap; it never resets spend or replaces
// an allowlist, owner, metadata, expiry or rate/concurrency policy.
func (s *Store) SetVirtualKeyBudget(ctx context.Context, id, teamID int64, limit *int64) error {
	if err := ValidateKeyBudget(limit); err != nil {
		return err
	}
	result, err := s.Pool.Exec(ctx, `UPDATE virtual_keys SET scoped_usd_limit_cents=$3 WHERE id=$1 AND team_id=$2 AND revoked_at IS NULL`, id, teamID, limit)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}
