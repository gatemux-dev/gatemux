package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Role constants. Mirrored on the wire as plain strings; CHECK constraint
// in migration 0008 enforces the same set in the database.
const (
	RoleAdmin   = "admin"
	RoleManager = "manager"
	RoleMember  = "member"
)

type User struct {
	ID                  int64
	Email               string
	Name                string
	TeamID              *int64
	TeamSlug            string // empty when not in a team; populated from teams.slug join
	Role                string
	IsAdmin             bool // derived from Role == RoleAdmin; kept on the struct for callers that already read it
	UsdLimitCents       *int64
	Period              string
	MaxParallelRequests *int
	OIDCSub             string
	RoleManagedByOIDC   bool
	DisabledAt          *time.Time
	CreatedAt           time.Time
	LastLoginAt         *time.Time
	ArchivedAt          *time.Time
}

// finalize sets derived fields (currently IsAdmin from Role) so callers
// that still check IsAdmin keep working without any per-call boilerplate.
func (u *User) finalize() {
	u.IsAdmin = u.Role == RoleAdmin
}

type UserListRow struct {
	User
}

// ListUsers pages active users. query matches email or name; role narrows
// to one role. Both run in SQL so totals cover every user.
func (s *Store) ListUsers(ctx context.Context, limit, offset int, query, role string) ([]*UserListRow, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(query))
	rows, err := s.Pool.Query(ctx, `
		SELECT u.id, u.email, u.name, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       u.disabled_at, COALESCE(u.oidc_sub, ''), u.role_managed_by_oidc,
		       t.slug,
		       COUNT(*) OVER () AS total
		FROM users u
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE u.archived_at IS NULL
		  AND ($3 = '' OR u.email ILIKE '%' || $3 || '%' OR u.name ILIKE '%' || $3 || '%')
		  AND ($4 = '' OR u.role = $4)
		ORDER BY u.created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset, pattern, role)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	out := []*UserListRow{}
	var total int64
	for rows.Next() {
		r := &UserListRow{}
		var teamSlug *string
		if err := rows.Scan(
			&r.ID, &r.Email, &r.Name, &r.TeamID, &r.Role,
			&r.UsdLimitCents, &r.Period, &r.MaxParallelRequests,
			&r.LastLoginAt, &r.CreatedAt, &r.ArchivedAt,
			&r.DisabledAt, &r.OIDCSub, &r.RoleManagedByOIDC,
			&teamSlug,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan user: %w", err)
		}
		if teamSlug != nil {
			r.User.TeamSlug = *teamSlug
		}
		r.User.finalize()
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// CreateUser writes a new user with the given role. is_admin is stored as a
// denormalized mirror of role == 'admin' for one release of back-compat;
// callers should not read it directly.
func (s *Store) CreateUser(ctx context.Context, email, name string, passwordHash []byte, teamID *int64, role string) (*User, error) {
	if role == "" {
		role = RoleMember
	}
	if role != RoleAdmin && role != RoleManager && role != RoleMember {
		return nil, fmt.Errorf("invalid role %q", role)
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO users (email, name, password_hash, team_id, is_admin, role)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, email, name, team_id, role,
		          usd_limit_cents, period, max_parallel_requests,
		          last_login_at, created_at, archived_at
	`, email, name, passwordHash, teamID, role == RoleAdmin, role)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests,
		&u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	u.finalize()
	if teamID != nil {
		if t, err := s.GetTeamByID(ctx, *teamID); err == nil {
			u.TeamSlug = t.Slug
		}
	}
	return u, nil
}

// GetUserByEmail returns the user with their password hash for verification.
// The hash is intentionally separate from the User struct so it never leaks
// out of an admin response by accident.
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*User, []byte, error) {
	var passwordHash []byte
	row := s.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.password_hash, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       u.disabled_at,
		       t.slug
		FROM users u
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE LOWER(u.email) = LOWER($1) AND u.archived_at IS NULL
	`, email)
	u := &User{}
	var teamSlug *string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &passwordHash, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests,
		&u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt,
		&u.DisabledAt,
		&teamSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("get user: %w", err)
	}
	if teamSlug != nil {
		u.TeamSlug = *teamSlug
	}
	u.finalize()
	return u, passwordHash, nil
}

func (s *Store) UpdateLastLogin(ctx context.Context, userID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET last_login_at = NOW() WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("update last login: %w", err)
	}
	return nil
}

// LookupUserByOIDCSub finds a user previously bound to this IdP subject.
// Returns ErrNotFound when no row matches; OIDC sign-in upserts on this
// boundary so an admin's manual edits survive across re-authentications.
func (s *Store) LookupUserByOIDCSub(ctx context.Context, sub string) (*User, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       u.oidc_sub, u.role_managed_by_oidc, u.disabled_at,
		       t.slug
		FROM users u
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE u.oidc_sub = $1 AND u.archived_at IS NULL
	`, sub)
	return scanOIDCUser(row)
}

// LookupUserByEmail returns a user by email. Used by OIDC sign-in to
// claim an existing password-only account on first SSO sign-in (binding
// it to the IdP subject so future sign-ins go through the sub path).
func (s *Store) LookupUserByEmail(ctx context.Context, email string) (*User, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       u.oidc_sub, u.role_managed_by_oidc, u.disabled_at,
		       t.slug
		FROM users u
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE LOWER(u.email) = LOWER($1) AND u.archived_at IS NULL
	`, email)
	return scanOIDCUser(row)
}

func scanOIDCUser(row pgx.Row) (*User, error) {
	u := &User{}
	var teamSlug *string
	var oidcSub *string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests,
		&u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt,
		&oidcSub, &u.RoleManagedByOIDC, &u.DisabledAt,
		&teamSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan user: %w", err)
	}
	if oidcSub != nil {
		u.OIDCSub = *oidcSub
	}
	if teamSlug != nil {
		u.TeamSlug = *teamSlug
	}
	u.finalize()
	return u, nil
}

// CreateOIDCUserParams covers the auto-provision path. role and team
// come from claim mapping; an empty team slug means "no team yet, admin
// will assign". Email collision returns ErrConflict so the caller can
// fall back to LookupUserByEmail and bind the existing row.
type CreateOIDCUserParams struct {
	OIDCSub  string
	Email    string
	Name     string
	Role     string
	TeamSlug string
}

func (s *Store) CreateOIDCUser(ctx context.Context, p CreateOIDCUserParams) (*User, error) {
	if p.Role == "" {
		p.Role = RoleMember
	}
	var teamID *int64
	if p.TeamSlug != "" {
		var id int64
		err := s.Pool.QueryRow(ctx, `SELECT id FROM teams WHERE slug = $1 AND archived_at IS NULL`, p.TeamSlug).Scan(&id)
		if err == nil {
			teamID = &id
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("resolve team for oidc user: %w", err)
		}
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO users (email, name, oidc_sub, team_id, is_admin, role, role_managed_by_oidc)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE)
		RETURNING id, email, name, team_id, role,
		          usd_limit_cents, period, max_parallel_requests,
		          last_login_at, created_at, archived_at,
		          oidc_sub, role_managed_by_oidc, disabled_at,
		          (SELECT slug FROM teams WHERE id = team_id)
	`, p.Email, p.Name, p.OIDCSub, teamID, p.Role == RoleAdmin, p.Role)
	u, err := scanOIDCUser(row)
	if err != nil {
		// Conflict on email or oidc_sub
		if pgErrCode(err) == "23505" {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create oidc user: %w", err)
	}
	return u, nil
}

// BindOIDCSub attaches an IdP subject to an existing user (typically
// when an OIDC sign-in lands on an email that already has a password
// account). Idempotent.
func (s *Store) BindOIDCSub(ctx context.Context, userID int64, sub string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET oidc_sub = $2 WHERE id = $1`, userID, sub)
	if err != nil {
		return fmt.Errorf("bind oidc sub: %w", err)
	}
	return nil
}

// ApplyOIDCMapping updates role and team to match what the IdP claims
// asserted on this sign-in. No-op when the user has been hand-edited
// (role_managed_by_oidc=false). Empty newTeamSlug leaves the team
// alone — useful when claim mapping doesn't have a team rule for this
// user but a role rule still applied.
func (s *Store) ApplyOIDCMapping(ctx context.Context, userID int64, newRole, newTeamSlug string) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var managed bool
	if err := tx.QueryRow(ctx, `SELECT role_managed_by_oidc FROM users WHERE id = $1`, userID).Scan(&managed); err != nil {
		return fmt.Errorf("read mapping flag: %w", err)
	}
	if !managed {
		return tx.Commit(ctx)
	}
	args := []any{userID, newRole, newRole == RoleAdmin}
	q := `UPDATE users SET role = $2, is_admin = $3`
	if newTeamSlug != "" {
		q += `, team_id = (SELECT id FROM teams WHERE slug = $4 AND archived_at IS NULL)`
		args = append(args, newTeamSlug)
	}
	q += ` WHERE id = $1`
	if _, err := tx.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("apply oidc mapping: %w", err)
	}
	return tx.Commit(ctx)
}

// MarkRoleManuallyEdited flips role_managed_by_oidc=false so subsequent
// OIDC sign-ins don't undo whatever an admin just changed. Called from
// admin role/team edits.
func (s *Store) MarkRoleManuallyEdited(ctx context.Context, userID int64) error {
	_, err := s.Pool.Exec(ctx, `UPDATE users SET role_managed_by_oidc = FALSE WHERE id = $1`, userID)
	if err != nil {
		return fmt.Errorf("mark role manual: %w", err)
	}
	return nil
}

// SetUserDisabled flips disabled_at so the auth.Session middleware can
// reject sign-ins regardless of the IdP's view. Pass disabled=true to
// disable, false to re-enable.
func (s *Store) SetUserDisabled(ctx context.Context, userID int64, disabled bool) error {
	var q string
	if disabled {
		q = `UPDATE users SET disabled_at = NOW() WHERE id = $1 AND disabled_at IS NULL`
	} else {
		q = `UPDATE users SET disabled_at = NULL WHERE id = $1`
	}
	_, err := s.Pool.Exec(ctx, q, userID)
	if err != nil {
		return fmt.Errorf("set disabled: %w", err)
	}
	return nil
}

func pgErrCode(err error) string {
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState()
	}
	return ""
}

// UpdatePasswordHash rewrites the user's bcrypt hash. Used by /me/password
// after the caller has verified the current password.
func (s *Store) UpdatePasswordHash(ctx context.Context, userID int64, newHash []byte) error {
	cmd, err := s.Pool.Exec(ctx, `
		UPDATE users SET password_hash = $2 WHERE id = $1 AND archived_at IS NULL
	`, userID, newHash)
	if err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetUserPasswordHash returns just the bcrypt hash for the current
// password — used by /me/password to verify the caller knows it before
// rotating to a new one.
func (s *Store) GetUserPasswordHash(ctx context.Context, userID int64) ([]byte, error) {
	var hash []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT password_hash FROM users WHERE id = $1 AND archived_at IS NULL
	`, userID).Scan(&hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get password hash: %w", err)
	}
	return hash, nil
}

func (s *Store) GetUserByID(ctx context.Context, id int64) (*User, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.team_id, u.role,
		       u.usd_limit_cents, u.period, u.max_parallel_requests,
		       u.last_login_at, u.created_at, u.archived_at,
		       t.slug
		FROM users u
		LEFT JOIN teams t ON t.id = u.team_id
		WHERE u.id = $1 AND u.archived_at IS NULL
	`, id)
	u := &User{}
	var teamSlug *string
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests,
		&u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt,
		&teamSlug); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	if teamSlug != nil {
		u.TeamSlug = *teamSlug
	}
	u.finalize()
	return u, nil
}

func (s *Store) UpdateUserBudget(ctx context.Context, id int64, usdLimitCents *int64, period string) (*User, error) {
	period = normalizeBudgetPeriod(period)
	row := s.Pool.QueryRow(ctx, `
		UPDATE users
		SET usd_limit_cents = $2, period = $3
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, email, name, team_id, role, usd_limit_cents, period, max_parallel_requests, last_login_at, created_at, archived_at
	`, id, usdLimitCents, period)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests, &u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update user budget: %w", err)
	}
	u.finalize()
	if u.TeamID != nil {
		if t, err := s.GetTeamByID(ctx, *u.TeamID); err == nil {
			u.TeamSlug = t.Slug
		}
	}
	return u, nil
}

func (s *Store) UpdateUserConcurrency(ctx context.Context, id int64, maxParallelRequests *int) (*User, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE users
		SET max_parallel_requests = $2
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, email, name, team_id, role, usd_limit_cents, period, max_parallel_requests, last_login_at, created_at, archived_at
	`, id, maxParallelRequests)
	u := &User{}
	if err := row.Scan(&u.ID, &u.Email, &u.Name, &u.TeamID, &u.Role,
		&u.UsdLimitCents, &u.Period, &u.MaxParallelRequests, &u.LastLoginAt, &u.CreatedAt, &u.ArchivedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update user concurrency: %w", err)
	}
	u.finalize()
	if u.TeamID != nil {
		if t, err := s.GetTeamByID(ctx, *u.TeamID); err == nil {
			u.TeamSlug = t.Slug
		}
	}
	return u, nil
}
