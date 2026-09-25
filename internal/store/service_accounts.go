package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ServiceAccount is a team-scoped non-human principal. Mirrors the
// budget/limits surface of users so admission logic stays uniform —
// whichever principal owns a key, the same caps apply.
type ServiceAccount struct {
	ID                  int64
	TeamID              int64
	TeamSlug            string
	Name                string
	Description         string
	UsdLimitCents       *int64
	Period              string
	RPM                 *int
	TPM                 *int
	MaxParallelRequests *int
	CreatedBy           *int64
	CreatedAt           time.Time
	ArchivedAt          *time.Time
}

func scanServiceAccount(row pgx.Row) (*ServiceAccount, error) {
	sa := &ServiceAccount{}
	var desc *string
	if err := row.Scan(&sa.ID, &sa.TeamID, &sa.TeamSlug, &sa.Name, &desc,
		&sa.UsdLimitCents, &sa.Period, &sa.RPM, &sa.TPM, &sa.MaxParallelRequests,
		&sa.CreatedBy, &sa.CreatedAt, &sa.ArchivedAt); err != nil {
		return nil, err
	}
	if desc != nil {
		sa.Description = *desc
	}
	return sa, nil
}

const saSelect = `
	SELECT sa.id, sa.team_id, t.slug, sa.name, sa.description,
	       sa.usd_limit_cents, sa.period, sa.rpm, sa.tpm, sa.max_parallel_requests,
	       sa.created_by, sa.created_at, sa.archived_at
	FROM service_accounts sa
	JOIN teams t ON t.id = sa.team_id
`

// ListServiceAccountsForTeam returns active SAs in a team, newest first.
func (s *Store) ListServiceAccountsForTeam(ctx context.Context, teamSlug string, limit, offset int) ([]*ServiceAccount, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, saSelect+`
		WHERE t.slug = $1 AND sa.archived_at IS NULL
		ORDER BY sa.created_at DESC
		LIMIT $2 OFFSET $3
	`, teamSlug, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list service accounts: %w", err)
	}
	defer rows.Close()
	out := []*ServiceAccount{}
	for rows.Next() {
		sa, err := scanServiceAccount(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan service account: %w", err)
		}
		out = append(out, sa)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var total int64
	if err := s.Pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM service_accounts sa
		JOIN teams t ON t.id = sa.team_id
		WHERE t.slug = $1 AND sa.archived_at IS NULL
	`, teamSlug).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count service accounts: %w", err)
	}
	return out, total, nil
}

// GetServiceAccountByID is used by the budget service when a key
// references an SA — it needs the limits to gate admission.
func (s *Store) GetServiceAccountByID(ctx context.Context, id int64) (*ServiceAccount, error) {
	row := s.Pool.QueryRow(ctx, saSelect+`WHERE sa.id = $1 AND sa.archived_at IS NULL`, id)
	sa, err := scanServiceAccount(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get service account: %w", err)
	}
	return sa, nil
}

// CreateServiceAccountParams keeps the field list explicit so caller
// intent is obvious at call sites (admin handler, tests).
type CreateServiceAccountParams struct {
	TeamID              int64
	Name                string
	Description         string
	UsdLimitCents       *int64
	Period              string
	RPM                 *int
	TPM                 *int
	MaxParallelRequests *int
	CreatedBy           *int64
}

// CreateServiceAccount inserts and returns the new row. (team_id, name)
// is unique so duplicate names within a team return ErrConflict.
func (s *Store) CreateServiceAccount(ctx context.Context, p CreateServiceAccountParams) (*ServiceAccount, error) {
	if p.Period == "" {
		p.Period = "month"
	}
	var desc *string
	if p.Description != "" {
		desc = &p.Description
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO service_accounts (team_id, name, description, usd_limit_cents, period, rpm, tpm, max_parallel_requests, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, team_id,
		          (SELECT slug FROM teams WHERE id = team_id),
		          name, description, usd_limit_cents, period, rpm, tpm, max_parallel_requests,
		          created_by, created_at, archived_at
	`, p.TeamID, p.Name, desc, p.UsdLimitCents, p.Period, p.RPM, p.TPM, p.MaxParallelRequests, p.CreatedBy)
	sa, err := scanServiceAccount(row)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create service account: %w", err)
	}
	return sa, nil
}

func (s *Store) UpdateServiceAccountConcurrency(ctx context.Context, id int64, maxParallelRequests *int) (*ServiceAccount, error) {
	row := s.Pool.QueryRow(ctx, `
		UPDATE service_accounts
		SET max_parallel_requests = $2
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, team_id,
		          (SELECT slug FROM teams WHERE id = team_id),
		          name, description, usd_limit_cents, period, rpm, tpm, max_parallel_requests,
		          created_by, created_at, archived_at
	`, id, maxParallelRequests)
	sa, err := scanServiceAccount(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update service account concurrency: %w", err)
	}
	return sa, nil
}

// UpdateServiceAccountParams covers the fields the admin UI lets you
// change after creation. Pointers distinguish "leave it" from "set
// to NULL". Name and description are always sent.
type UpdateServiceAccountParams struct {
	Name          string
	Description   *string
	UsdLimitCents *int64
	UsdLimitClear bool
	Period        string
	RPM           *int
	RPMClear      bool
	TPM           *int
	TPMClear      bool
}

// UpdateServiceAccount mutates an SA. The *Clear bool flags exist so
// callers can null a column without colliding with the "don't touch"
// nil-pointer convention. Period defaults to "month" when sent empty
// to mirror UpdateTeamBudget's behavior.
func (s *Store) UpdateServiceAccount(ctx context.Context, id int64, p UpdateServiceAccountParams) (*ServiceAccount, error) {
	if p.Period == "" {
		p.Period = "month"
	}
	var usd any
	if p.UsdLimitClear {
		usd = nil
	} else if p.UsdLimitCents != nil {
		usd = *p.UsdLimitCents
	} else {
		// Leave existing value alone via COALESCE on the existing column.
		// Pgx + dynamic UPDATE would be cleaner, but a single static
		// UPDATE w/ COALESCE keeps the SQL trivially auditable.
		row := s.Pool.QueryRow(ctx, `SELECT usd_limit_cents FROM service_accounts WHERE id = $1`, id)
		var v *int64
		if err := row.Scan(&v); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("read existing usd_limit_cents: %w", err)
		}
		usd = v
	}
	rpm := pickIntPointer(p.RPM, p.RPMClear, "rpm", id, s)
	tpm := pickIntPointer(p.TPM, p.TPMClear, "tpm", id, s)
	desc := p.Description
	row := s.Pool.QueryRow(ctx, `
		UPDATE service_accounts
		SET name = $2,
		    description = COALESCE($3, description),
		    usd_limit_cents = $4,
		    period = $5,
		    rpm = $6,
		    tpm = $7
		WHERE id = $1 AND archived_at IS NULL
		RETURNING id, team_id,
		          (SELECT slug FROM teams WHERE id = team_id),
		          name, description, usd_limit_cents, period, rpm, tpm, max_parallel_requests,
		          created_by, created_at, archived_at
	`, id, p.Name, desc, usd, p.Period, rpm, tpm)
	sa, err := scanServiceAccount(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("update service account: %w", err)
	}
	return sa, nil
}

// pickIntPointer is the shared "leave alone vs clear vs set" helper for
// the nullable int columns on UpdateServiceAccount. Reads existing on
// "leave alone".
func pickIntPointer(set *int, clear bool, col string, id int64, s *Store) any {
	if clear {
		return nil
	}
	if set != nil {
		return *set
	}
	row := s.Pool.QueryRow(context.Background(), `SELECT `+col+` FROM service_accounts WHERE id = $1`, id)
	var v *int
	_ = row.Scan(&v)
	if v == nil {
		return nil
	}
	return *v
}

// ArchiveServiceAccount soft-deletes by setting archived_at. The keys
// remain attached for audit; the FK cascade only fires on hard delete.
// The index of active rows excludes archived ones via the WHERE clause
// in callers.
func (s *Store) ArchiveServiceAccount(ctx context.Context, id int64) error {
	cmd, err := s.Pool.Exec(ctx, `
		UPDATE service_accounts SET archived_at = NOW()
		WHERE id = $1 AND archived_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("archive service account: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
