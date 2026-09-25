package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
)

type Customer struct {
	ID                  int64
	TeamID              int64
	ExternalID          string
	Name                string
	Metadata            map[string]any
	UsdLimitCents       *int64
	Period              string
	RPM                 *int
	TPM                 *int
	MaxParallelRequests *int
	ArchivedAt          *time.Time
	CreatedAt           time.Time
}

func ValidateCustomerID(value string) error {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return fmt.Errorf("customer ID must contain 1–256 bytes without surrounding whitespace")
	}
	for _, r := range value {
		if unicode.IsControl(r) || r == '/' || r == '\\' {
			return fmt.Errorf("customer ID must not contain control characters or path separators")
		}
	}
	return nil
}

func ValidateCustomerLimits(limit *int64, period string, rpm, tpm *int) error {
	if limit != nil && (*limit < 0 || *limit > 1000000000000) {
		return fmt.Errorf("customer budget must be 0–1000000000000 cents or null")
	}
	if period != "" && period != "day" && period != "month" {
		return fmt.Errorf("period must be day or month")
	}
	for _, rate := range []*int{rpm, tpm} {
		if rate != nil && (*rate < 0 || *rate > 1000000000) {
			return fmt.Errorf("customer RPM/TPM must be 0–1000000000 or null")
		}
	}
	return nil
}

func (s *Store) SetCustomerRegistration(ctx context.Context, teamID int64, mode string) error {
	if mode != "optional" && mode != "required" && mode != "auto_create" {
		return fmt.Errorf("customer_registration must be optional, required or auto_create")
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE teams SET customer_registration=$2 WHERE id=$1 AND archived_at IS NULL`, teamID, mode)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

var ErrCustomerCapacity = errors.New("automatic customer registration limit reached")

const MaxAutoCreatedCustomers = 10000

// GetOrCreateCustomer is the auto-provision path: returns the existing
// row or inserts a fresh one with no limits. Used by the v1 admission
// pipeline when the team is in `auto_create` mode.
func (s *Store) GetOrCreateCustomer(ctx context.Context, teamID int64, externalID string) (*Customer, error) {
	if err := ValidateCustomerID(externalID); err != nil {
		return nil, err
	}
	c, err := s.GetCustomerByExternalID(ctx, teamID, externalID)
	if err == nil {
		return c, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Only first registration is serialized, not existing-customer inference.
	// The row lock keeps concurrent auto-registration within the per-team cap.
	var id int64
	if err := tx.QueryRow(ctx, `SELECT id FROM teams WHERE id=$1 AND archived_at IS NULL FOR UPDATE`, teamID).Scan(&id); err != nil {
		return nil, err
	}
	var exists bool
	var count int
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM customers WHERE team_id=$1 AND external_id=$2), (SELECT COUNT(*) FROM customers WHERE team_id=$1)`, teamID, externalID).Scan(&exists, &count); err != nil {
		return nil, err
	}
	if !exists && count >= MaxAutoCreatedCustomers {
		return nil, ErrCustomerCapacity
	}
	row := tx.QueryRow(ctx, `
		INSERT INTO customers (team_id, external_id) VALUES ($1, $2)
		ON CONFLICT (team_id, external_id) DO UPDATE SET external_id = EXCLUDED.external_id
		RETURNING id, team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests, archived_at, created_at
	`, teamID, externalID)
	out := &Customer{}
	var meta []byte
	if err := row.Scan(&out.ID, &out.TeamID, &out.ExternalID, &out.Name, &meta, &out.UsdLimitCents, &out.Period, &out.RPM, &out.TPM, &out.MaxParallelRequests, &out.ArchivedAt, &out.CreatedAt); err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	out.Metadata = parseJSONMap(meta)
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) GetCustomerByExternalID(ctx context.Context, teamID int64, externalID string) (*Customer, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests, archived_at, created_at
		FROM customers WHERE team_id = $1 AND external_id = $2
	`, teamID, externalID)
	out := &Customer{}
	var meta []byte
	if err := row.Scan(&out.ID, &out.TeamID, &out.ExternalID, &out.Name, &meta, &out.UsdLimitCents, &out.Period, &out.RPM, &out.TPM, &out.MaxParallelRequests, &out.ArchivedAt, &out.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get customer: %w", err)
	}
	out.Metadata = parseJSONMap(meta)
	return out, nil
}

func (s *Store) ListCustomers(ctx context.Context, teamID int64, limit, offset int) ([]*Customer, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT id, team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests, archived_at, created_at,
		       COUNT(*) OVER () AS total
		FROM customers
		WHERE team_id = $1 AND archived_at IS NULL
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, teamID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list customers: %w", err)
	}
	defer rows.Close()
	out := []*Customer{}
	var total int64
	for rows.Next() {
		c := &Customer{}
		var meta []byte
		if err := rows.Scan(&c.ID, &c.TeamID, &c.ExternalID, &c.Name, &meta, &c.UsdLimitCents, &c.Period, &c.RPM, &c.TPM, &c.MaxParallelRequests, &c.ArchivedAt, &c.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan customer: %w", err)
		}
		c.Metadata = parseJSONMap(meta)
		out = append(out, c)
	}
	return out, total, rows.Err()
}

type CreateCustomerParams struct {
	TeamID              int64
	ExternalID          string
	Name                string
	Metadata            map[string]any
	UsdLimitCents       *int64
	Period              string
	RPM                 *int
	TPM                 *int
	MaxParallelRequests *int
}

func (s *Store) CreateCustomer(ctx context.Context, p CreateCustomerParams) (*Customer, error) {
	if err := ValidateCustomerID(p.ExternalID); err != nil {
		return nil, err
	}
	if err := ValidateCustomerLimits(p.UsdLimitCents, p.Period, p.RPM, p.TPM); err != nil {
		return nil, err
	}
	if p.Period == "" {
		p.Period = "month"
	}
	if p.Metadata == nil {
		p.Metadata = map[string]any{}
	}
	meta, _ := json.Marshal(p.Metadata)
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO customers (team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8, $9)
		RETURNING id, team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests, archived_at, created_at
	`, p.TeamID, p.ExternalID, p.Name, meta, p.UsdLimitCents, p.Period, p.RPM, p.TPM, p.MaxParallelRequests)
	out := &Customer{}
	var rawMeta []byte
	if err := row.Scan(&out.ID, &out.TeamID, &out.ExternalID, &out.Name, &rawMeta, &out.UsdLimitCents, &out.Period, &out.RPM, &out.TPM, &out.MaxParallelRequests, &out.ArchivedAt, &out.CreatedAt); err != nil {
		return nil, fmt.Errorf("create customer: %w", err)
	}
	out.Metadata = parseJSONMap(rawMeta)
	return out, nil
}

type UpdateCustomerParams struct {
	UsdLimitCents       *int64
	Period              string
	RPM                 *int
	TPM                 *int
	MaxParallelRequests *int
	Name                string
}

func (s *Store) UpdateCustomer(ctx context.Context, teamID int64, externalID string, p UpdateCustomerParams) (*Customer, error) {
	if err := ValidateCustomerLimits(p.UsdLimitCents, p.Period, p.RPM, p.TPM); err != nil {
		return nil, err
	}
	if p.Period == "" {
		p.Period = "month"
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE customers
		SET name = $3, usd_limit_cents = $4, period = $5, rpm = $6, tpm = $7
		WHERE team_id = $1 AND external_id = $2 AND archived_at IS NULL
		RETURNING id, team_id, external_id, name, metadata, usd_limit_cents, period, rpm, tpm, max_parallel_requests, archived_at, created_at
	`, teamID, externalID, p.Name, p.UsdLimitCents, p.Period, p.RPM, p.TPM)
	out := &Customer{}
	var meta []byte
	if err := row.Scan(&out.ID, &out.TeamID, &out.ExternalID, &out.Name, &meta, &out.UsdLimitCents, &out.Period, &out.RPM, &out.TPM, &out.MaxParallelRequests, &out.ArchivedAt, &out.CreatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update customer: %w", err)
	}
	out.Metadata = parseJSONMap(meta)
	return out, nil
}

func parseJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
