package store

import (
	"context"
	"strings"
	"time"
)

// TeamMember deliberately omits global budgets, identity-provider metadata and
// credentials. A manager's directory is not the global administrator user list.
type TeamMember struct {
	ID          int64      `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
	DisabledAt  *time.Time `json:"disabled_at,omitempty"`
}

func (s *Store) ListTeamMembers(ctx context.Context, teamID int64, query string, limit, offset int) ([]TeamMember, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	pattern := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query) + "%"
	const predicate = ` FROM users WHERE team_id = $1 AND archived_at IS NULL AND (email ILIKE $2 OR name ILIKE $2)`
	rows, err := s.Pool.Query(ctx, `SELECT id, email, name, role, last_login_at, disabled_at, COUNT(*) OVER ()`+predicate+` ORDER BY email, id LIMIT $3 OFFSET $4`, teamID, pattern, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TeamMember{}
	var total int64
	for rows.Next() {
		var u TeamMember
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.LastLoginAt, &u.DisabledAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Preserve the scoped count on an empty/out-of-range page as well.
	if len(out) == 0 {
		err = s.Pool.QueryRow(ctx, `SELECT COUNT(*)`+predicate, teamID, pattern).Scan(&total)
	}
	return out, total, err
}

type TeamModel struct {
	Alias string `json:"alias"`
}

// Apply policy before pagination. No deployment names, URLs, credentials or
// routing configuration are exposed through this model-name catalog.
func (s *Store) ListTeamModels(ctx context.Context, teamID int64, limit, offset int) ([]TeamModel, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	const predicate = ` FROM model_aliases a JOIN teams t ON t.id = $1
		WHERE t.archived_at IS NULL AND (t.allowed_models = '[]'::jsonb OR t.allowed_models ? '*' OR t.allowed_models ? a.alias)`
	rows, err := s.Pool.Query(ctx, `SELECT a.alias, COUNT(*) OVER ()`+predicate+` ORDER BY a.alias LIMIT $2 OFFSET $3`, teamID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []TeamModel{}
	var total int64
	for rows.Next() {
		var m TeamModel
		if err := rows.Scan(&m.Alias, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) == 0 {
		err = s.Pool.QueryRow(ctx, `SELECT COUNT(*)`+predicate, teamID).Scan(&total)
	}
	return out, total, err
}
