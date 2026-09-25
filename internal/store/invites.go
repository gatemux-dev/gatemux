package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type Invite struct {
	ID          int64
	TokenPrefix string
	Email       string
	TeamID      *int64
	Role        string
	CreatedBy   *int64
	CreatedAt   time.Time
	ExpiresAt   time.Time
	AcceptedAt  *time.Time
	AcceptedBy  *int64
}

type InviteListRow struct {
	Invite
	TeamSlug *string
}

func (s *Store) CreateInvite(
	ctx context.Context,
	tokenHash []byte,
	tokenPrefix, email, role string,
	teamID *int64,
	expiresAt time.Time,
) (*Invite, error) {
	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO invites (token_hash, token_prefix, email, team_id, role, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, token_prefix, COALESCE(email, ''), team_id, role,
		          created_by, created_at, expires_at, accepted_at, accepted_by
	`, tokenHash, tokenPrefix, emailPtr, teamID, role, expiresAt)
	inv := &Invite{}
	if err := row.Scan(
		&inv.ID, &inv.TokenPrefix, &inv.Email, &inv.TeamID, &inv.Role,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy,
	); err != nil {
		return nil, fmt.Errorf("create invite: %w", err)
	}
	return inv, nil
}

// A nil teamSlug is administrator scope; a non-nil slug is filtered before
// pagination. An empty scoped slug intentionally matches no unassigned invites.
func (s *Store) ListInvites(ctx context.Context, limit, offset int, teamSlug *string) ([]*InviteListRow, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT i.id, i.token_prefix, COALESCE(i.email, ''), i.team_id, i.role,
		       i.created_by, i.created_at, i.expires_at, i.accepted_at, i.accepted_by,
		       t.slug,
		       COUNT(*) OVER () AS total
		FROM invites i
		LEFT JOIN teams t ON t.id = i.team_id
		WHERE ($3::text IS NULL OR (t.slug = $3 AND t.archived_at IS NULL))
		ORDER BY i.created_at DESC, i.id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset, teamSlug)
	if err != nil {
		return nil, 0, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()
	out := []*InviteListRow{}
	var total int64
	for rows.Next() {
		r := &InviteListRow{}
		if err := rows.Scan(
			&r.ID, &r.TokenPrefix, &r.Email, &r.TeamID, &r.Role,
			&r.CreatedBy, &r.CreatedAt, &r.ExpiresAt, &r.AcceptedAt, &r.AcceptedBy,
			&r.TeamSlug,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan invite: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		err = s.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM invites i LEFT JOIN teams t ON t.id=i.team_id WHERE ($1::text IS NULL OR (t.slug=$1 AND t.archived_at IS NULL))`, teamSlug).Scan(&total)
	}
	return out, total, err
}

func (s *Store) GetInviteByTokenHash(ctx context.Context, tokenHash []byte) (*Invite, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, token_prefix, COALESCE(email, ''), team_id, role,
		       created_by, created_at, expires_at, accepted_at, accepted_by
		FROM invites WHERE token_hash = $1
	`, tokenHash)
	inv := &Invite{}
	if err := row.Scan(
		&inv.ID, &inv.TokenPrefix, &inv.Email, &inv.TeamID, &inv.Role,
		&inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.AcceptedAt, &inv.AcceptedBy,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get invite: %w", err)
	}
	return inv, nil
}

// MarkInviteAccepted records the user that accepted an invite. It only
// matches non-accepted, non-expired invites; otherwise returns ErrNotFound.
func (s *Store) MarkInviteAccepted(ctx context.Context, tokenHash []byte, userID int64) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE invites
		SET accepted_at = NOW(), accepted_by = $2
		WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > NOW()
	`, tokenHash, userID)
	if err != nil {
		return fmt.Errorf("accept invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
