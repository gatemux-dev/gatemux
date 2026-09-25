package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, userID int64, expiresAt time.Time) error {
	return s.CreateSessionWithMeta(ctx, tokenHash, userID, expiresAt, "", "")
}

// CreateSessionWithMeta is the metadata-aware variant used by /auth/login
// and /invite/accept. ip/userAgent are nullable; empty strings store as
// NULL so we don't pollute the row with placeholder data when they're
// not available.
func (s *Store) CreateSessionWithMeta(ctx context.Context, tokenHash []byte, userID int64, expiresAt time.Time, ip, userAgent string) error {
	var ipArg any
	if ip != "" {
		ipArg = ip
	}
	var uaArg any
	if userAgent != "" {
		uaArg = userAgent
	}
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO sessions (token_hash, user_id, expires_at, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5)
	`, tokenHash, userID, expiresAt, ipArg, uaArg)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// SessionRow is the human-facing view of a session for the Account →
// Sessions list. token_hash itself is exposed as a hex prefix so the UI
// can identify "this is my current session" without leaking enough to
// hijack it.
type SessionRow struct {
	TokenHashPrefix string     `json:"token_hash_prefix"`
	CreatedAt       time.Time  `json:"created_at"`
	ExpiresAt       time.Time  `json:"expires_at"`
	LastSeenAt      *time.Time `json:"last_seen_at,omitempty"`
	IP              string     `json:"ip,omitempty"`
	UserAgent       string     `json:"user_agent,omitempty"`
	Current         bool       `json:"current"`
}

// ListSessionsForUser returns the user's active (non-expired) sessions,
// newest first. The current session is marked by matching the supplied
// currentHash, so the UI can render "this device" without an extra
// round-trip.
func (s *Store) ListSessionsForUser(ctx context.Context, userID int64, currentHash []byte) ([]SessionRow, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT token_hash, created_at, expires_at, last_seen_at,
		       COALESCE(host(ip), ''), COALESCE(user_agent, '')
		FROM sessions
		WHERE user_id = $1 AND expires_at > NOW()
		ORDER BY created_at DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	out := []SessionRow{}
	for rows.Next() {
		var hash []byte
		row := SessionRow{}
		if err := rows.Scan(&hash, &row.CreatedAt, &row.ExpiresAt, &row.LastSeenAt, &row.IP, &row.UserAgent); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		// Hex prefix is stable enough to identify a session without
		// leaking the full hash.
		row.TokenHashPrefix = hexPrefix(hash, 8)
		row.Current = len(currentHash) > 0 && bytesEqual(hash, currentHash)
		out = append(out, row)
	}
	return out, rows.Err()
}

// DeleteUserSessionByPrefix revokes a single session belonging to the
// user, identified by the hex prefix shown in the UI. Scoping by user
// id makes prefix collisions safe — even an attacker who guessed
// someone else's prefix can only revoke their own.
func (s *Store) DeleteUserSessionByPrefix(ctx context.Context, userID int64, prefix string) error {
	if prefix == "" {
		return ErrNotFound
	}
	cmd, err := s.Pool.Exec(ctx, `
		DELETE FROM sessions
		WHERE user_id = $1
		  AND encode(token_hash, 'hex') LIKE $2 || '%'
	`, userID, prefix)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOtherSessionsForUser revokes every session belonging to the user
// except the one matching keepHash. Used by "sign out other devices".
func (s *Store) DeleteOtherSessionsForUser(ctx context.Context, userID int64, keepHash []byte) (int64, error) {
	cmd, err := s.Pool.Exec(ctx, `
		DELETE FROM sessions
		WHERE user_id = $1 AND token_hash <> $2
	`, userID, keepHash)
	if err != nil {
		return 0, fmt.Errorf("delete other sessions: %w", err)
	}
	return cmd.RowsAffected(), nil
}

// TouchSession bumps last_seen_at on every authenticated request. Cheap
// in steady state because the WHERE matches by primary key.
func (s *Store) TouchSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE sessions SET last_seen_at = NOW() WHERE token_hash = $1
	`, tokenHash)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

func hexPrefix(b []byte, n int) string {
	const hex = "0123456789abcdef"
	if n > len(b) {
		n = len(b)
	}
	out := make([]byte, n*2)
	for i := 0; i < n; i++ {
		out[i*2] = hex[b[i]>>4]
		out[i*2+1] = hex[b[i]&0x0f]
	}
	return string(out)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// LookupSession returns the user owning a non-expired session matching
// tokenHash, or ErrNotFound. The user's password_hash is never returned.
// usd_limit_cents and period are loaded so /me budget views match the
// numbers admission would actually enforce.
func (s *Store) LookupSession(ctx context.Context, tokenHash []byte) (*User, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       u.disabled_at,
		       t.slug
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE s.token_hash = $1
		  AND s.expires_at > NOW()
		  AND u.archived_at IS NULL
	`, tokenHash)
	u := &User{}
	var teamSlug *string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests,
		&u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt,
		&u.DisabledAt,
		&teamSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("lookup session: %w", err)
	}
	if teamSlug != nil {
		u.TeamSlug = *teamSlug
	}
	u.finalize()
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}
