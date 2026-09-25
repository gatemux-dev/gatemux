package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PasswordReset is the admin-issued reset row. Token bytes themselves
// are never stored — only the SHA-256 hash — so a DB dump alone can't
// be used to consume an outstanding reset.
type PasswordReset struct {
	ID        int64
	UserID    int64
	UserEmail string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// CreatePasswordReset inserts a fresh row. Caller hashes the raw token
// before passing it in. createdBy is nullable for system-issued resets
// (we don't have any today, but the schema allows it).
func (s *Store) CreatePasswordReset(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time, createdBy *int64) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO password_resets (user_id, token_hash, expires_at, created_by)
		VALUES ($1, $2, $3, $4)
	`, userID, tokenHash, expiresAt, createdBy)
	if err != nil {
		return fmt.Errorf("create password reset: %w", err)
	}
	return nil
}

// LookupPasswordReset finds an unexpired, unused reset by token hash and
// returns the owning user's email + id so the consume handler can show
// "set a new password for foo@bar.com" and run the update under the
// right user. Single-use: callers must MarkPasswordResetUsed inside the
// same transaction or accept a race; in practice we serialize via the
// new password write.
func (s *Store) LookupPasswordReset(ctx context.Context, tokenHash []byte) (*PasswordReset, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT pr.id, pr.user_id, u.email, pr.expires_at, pr.used_at, pr.created_at
		FROM password_resets pr
		JOIN users u ON u.id = pr.user_id
		WHERE pr.token_hash = $1
		  AND pr.expires_at > NOW()
		  AND pr.used_at IS NULL
		  AND u.archived_at IS NULL
	`, tokenHash)
	pr := &PasswordReset{}
	if err := row.Scan(&pr.ID, &pr.UserID, &pr.UserEmail, &pr.ExpiresAt, &pr.UsedAt, &pr.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lookup password reset: %w", err)
	}
	return pr, nil
}

// MarkPasswordResetUsed flips used_at and is intended to be called in
// the same flow as the password write. Idempotent — re-marking a
// already-used row is a no-op (the WHERE clause ignores it).
func (s *Store) MarkPasswordResetUsed(ctx context.Context, id int64) error {
	cmd, err := s.Pool.Exec(ctx, `
		UPDATE password_resets SET used_at = NOW()
		WHERE id = $1 AND used_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("mark password reset used: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSessionsForUser revokes every session belonging to the user.
// Used by the reset-consume handler so a stolen current session can't
// outlive a forced password reset.
func (s *Store) DeleteSessionsForUser(ctx context.Context, userID int64) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("delete sessions for user: %w", err)
	}
	return nil
}
