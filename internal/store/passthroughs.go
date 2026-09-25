package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Passthrough is a named generic-proxy route. The handler resolves the
// inbound /passthrough/{name}/* request to a single Passthrough row and
// forwards to TargetURL + remainder, replacing the client's bearer token
// with AuthValuePrefix+os.Getenv(AuthValueEnv) under AuthHeader.
type Passthrough struct {
	ID              int64
	Name            string
	TargetURL       string
	AuthHeader      string
	AuthValueEnv    string
	AuthValuePrefix string
	Enabled         bool
	CreatedAt       time.Time
	ArchivedAt      *time.Time
}

func (s *Store) ListPassthroughs(ctx context.Context, limit, offset int) ([]*Passthrough, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, target_url, auth_header, auth_value_env, auth_value_prefix,
		       enabled, created_at, archived_at,
		       COUNT(*) OVER () AS total
		FROM passthroughs
		WHERE archived_at IS NULL
		ORDER BY name
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list passthroughs: %w", err)
	}
	defer rows.Close()
	out := []*Passthrough{}
	var total int64
	for rows.Next() {
		p := &Passthrough{}
		if err := rows.Scan(&p.ID, &p.Name, &p.TargetURL, &p.AuthHeader, &p.AuthValueEnv, &p.AuthValuePrefix,
			&p.Enabled, &p.CreatedAt, &p.ArchivedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan passthrough: %w", err)
		}
		out = append(out, p)
	}
	return out, total, rows.Err()
}

func (s *Store) GetPassthroughByName(ctx context.Context, name string) (*Passthrough, error) {
	p := &Passthrough{}
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, target_url, auth_header, auth_value_env, auth_value_prefix,
		       enabled, created_at, archived_at
		FROM passthroughs
		WHERE name = $1 AND archived_at IS NULL
	`, name).Scan(&p.ID, &p.Name, &p.TargetURL, &p.AuthHeader, &p.AuthValueEnv, &p.AuthValuePrefix,
		&p.Enabled, &p.CreatedAt, &p.ArchivedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get passthrough: %w", err)
	}
	return p, nil
}

type CreatePassthroughParams struct {
	Name            string
	TargetURL       string
	AuthHeader      string
	AuthValueEnv    string
	AuthValuePrefix string
	Enabled         bool
}

func (s *Store) CreatePassthrough(ctx context.Context, p CreatePassthroughParams) (*Passthrough, error) {
	out := &Passthrough{}
	err := s.Pool.QueryRow(ctx, `
		INSERT INTO passthroughs (name, target_url, auth_header, auth_value_env, auth_value_prefix, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, target_url, auth_header, auth_value_env, auth_value_prefix,
		          enabled, created_at, archived_at
	`, p.Name, p.TargetURL, p.AuthHeader, p.AuthValueEnv, p.AuthValuePrefix, p.Enabled).Scan(
		&out.ID, &out.Name, &out.TargetURL, &out.AuthHeader, &out.AuthValueEnv, &out.AuthValuePrefix,
		&out.Enabled, &out.CreatedAt, &out.ArchivedAt)
	if err != nil {
		return nil, fmt.Errorf("create passthrough: %w", err)
	}
	return out, nil
}

type UpdatePassthroughParams struct {
	TargetURL       *string
	AuthHeader      *string
	AuthValueEnv    *string
	AuthValuePrefix *string
	Enabled         *bool
}

func (s *Store) UpdatePassthrough(ctx context.Context, name string, p UpdatePassthroughParams) (*Passthrough, error) {
	out := &Passthrough{}
	err := s.Pool.QueryRow(ctx, `
		UPDATE passthroughs SET
			target_url        = COALESCE($2, target_url),
			auth_header       = COALESCE($3, auth_header),
			auth_value_env    = COALESCE($4, auth_value_env),
			auth_value_prefix = COALESCE($5, auth_value_prefix),
			enabled           = COALESCE($6, enabled)
		WHERE name = $1 AND archived_at IS NULL
		RETURNING id, name, target_url, auth_header, auth_value_env, auth_value_prefix,
		          enabled, created_at, archived_at
	`, name, p.TargetURL, p.AuthHeader, p.AuthValueEnv, p.AuthValuePrefix, p.Enabled).Scan(
		&out.ID, &out.Name, &out.TargetURL, &out.AuthHeader, &out.AuthValueEnv, &out.AuthValuePrefix,
		&out.Enabled, &out.CreatedAt, &out.ArchivedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update passthrough: %w", err)
	}
	return out, nil
}

func (s *Store) DeletePassthrough(ctx context.Context, name string) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE passthroughs SET archived_at = NOW()
		WHERE name = $1 AND archived_at IS NULL
	`, name)
	if err != nil {
		return fmt.Errorf("delete passthrough: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
