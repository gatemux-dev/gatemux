package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type CreateVirtualKeyParams struct {
	TeamID              int64
	UserID              *int64
	ServiceAccountID    *int64
	KeyHash             []byte
	Prefix              string
	Name                string
	Metadata            map[string]any
	AllowedModels       []string
	ScopedRPM           *int
	ScopedTPM           *int
	MaxParallelRequests *int
	ScopedUsdLimitCents *int64
	ExpiresAt           *time.Time
}

func (s *Store) CreateVirtualKey(ctx context.Context, params CreateVirtualKeyParams) (*VirtualKey, error) {
	if err := ValidateKeyBudget(params.ScopedUsdLimitCents); err != nil {
		return nil, err
	}
	if params.Metadata == nil {
		params.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(params.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal key metadata: %w", err)
	}
	allowed, err := json.Marshal(normalizeAllowedModels(params.AllowedModels))
	if err != nil {
		return nil, fmt.Errorf("marshal key allowlist: %w", err)
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO virtual_keys (team_id, user_id, service_account_id, key_hash, key_prefix, name, metadata, allowed_models, scoped_rpm, scoped_tpm, max_parallel_requests, expires_at, scoped_usd_limit_cents)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9, $10, $11, $12, $13)
		RETURNING id, team_id, user_id, service_account_id, key_prefix, name, metadata, allowed_models, scoped_rpm, scoped_tpm, max_parallel_requests, expires_at, scoped_usd_limit_cents,
		          rotated_from_key_id, last_used_at, created_at, revoked_at, disabled_at
	`, params.TeamID, params.UserID, params.ServiceAccountID, params.KeyHash, params.Prefix, params.Name, metadata, allowed, params.ScopedRPM, params.ScopedTPM, params.MaxParallelRequests, params.ExpiresAt, params.ScopedUsdLimitCents)
	vk := &VirtualKey{}
	var metadataRaw, allowedRaw []byte
	if err := row.Scan(
		&vk.ID, &vk.TeamID, &vk.UserID, &vk.ServiceAccountID, &vk.KeyPrefix, &vk.Name, &metadataRaw, &allowedRaw, &vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ExpiresAt, &vk.ScopedUsdLimitCents,
		&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
	); err != nil {
		return nil, fmt.Errorf("create virtual key: %w", err)
	}
	vk.Metadata = parseMetadata(metadataRaw)
	vk.AllowedModels = parseAllowedModels(allowedRaw)
	return vk, nil
}

func (s *Store) LookupKey(ctx context.Context, keyHash []byte) (*VirtualKey, *Team, *User, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT vk.id, vk.team_id, vk.user_id, vk.service_account_id, vk.key_prefix, vk.name, vk.metadata, vk.allowed_models, vk.allowed_cidrs, vk.expires_at,
		       vk.scoped_rpm, vk.scoped_tpm, vk.max_parallel_requests, vk.scoped_usd_limit_cents, vk.rotated_from_key_id, vk.last_used_at, vk.created_at, vk.revoked_at, vk.disabled_at,
		       COALESCE(sa.name, '') AS sa_name,
		       CASE WHEN vk.user_id IS NOT NULL THEN u.max_parallel_requests ELSE sa.max_parallel_requests END AS owner_max_parallel_requests,
		       u.id, COALESCE(u.email, ''), COALESCE(u.name, ''), u.team_id, COALESCE(u.role, 'member'), u.usd_limit_cents, COALESCE(u.period, 'month'), u.last_login_at, u.created_at, u.archived_at,
		       t.id, t.slug, t.name, t.usd_limit_cents, t.period, t.allowed_models, t.rpm, t.tpm, t.max_parallel_requests, t.customer_registration, t.created_at, t.archived_at
		FROM virtual_keys vk
		LEFT JOIN users u ON u.id = vk.user_id
		LEFT JOIN service_accounts sa ON sa.id = vk.service_account_id
		JOIN teams t ON t.id = vk.team_id
		WHERE vk.key_hash = $1
	`, keyHash)
	vk := &VirtualKey{}
	team := &Team{}
	var owner *User
	var keyMetadataRaw, keyAllowedRaw, keyCIDRsRaw, allowedRaw []byte
	var ownerID *int64
	var ownerEmail, ownerName string
	var ownerTeamID *int64
	var ownerRole string
	var ownerUsdLimit *int64
	var ownerPeriod string
	var ownerLastLogin, ownerCreated, ownerArchived any
	err := row.Scan(
		&vk.ID, &vk.TeamID, &vk.UserID, &vk.ServiceAccountID, &vk.KeyPrefix, &vk.Name, &keyMetadataRaw, &keyAllowedRaw, &keyCIDRsRaw, &vk.ExpiresAt,
		&vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ScopedUsdLimitCents,
		&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
		&vk.ServiceAccountName,
		&vk.OwnerMaxParallelRequests,
		&ownerID, &ownerEmail, &ownerName, &ownerTeamID, &ownerRole, &ownerUsdLimit, &ownerPeriod, &ownerLastLogin, &ownerCreated, &ownerArchived,
		&team.ID, &team.Slug, &team.Name, &team.UsdLimitCents, &team.Period, &allowedRaw, &team.RPM, &team.TPM, &team.MaxParallelRequests, &team.CustomerRegistration, &team.CreatedAt, &team.ArchivedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, nil, ErrNotFound
		}
		return nil, nil, nil, fmt.Errorf("lookup key: %w", err)
	}
	vk.Metadata = parseMetadata(keyMetadataRaw)
	vk.AllowedModels = parseAllowedModels(keyAllowedRaw)
	vk.AllowedCIDRs = parseStringList(keyCIDRsRaw)
	team.AllowedModels = parseAllowedModels(allowedRaw)
	if ownerID != nil {
		owner = &User{
			ID:                  *ownerID,
			Email:               ownerEmail,
			Name:                ownerName,
			TeamID:              ownerTeamID,
			Role:                ownerRole,
			UsdLimitCents:       ownerUsdLimit,
			Period:              ownerPeriod,
			MaxParallelRequests: vk.OwnerMaxParallelRequests,
		}
		owner.finalize()
		if v, ok := ownerLastLogin.(time.Time); ok {
			owner.LastLoginAt = &v
		}
		if v, ok := ownerCreated.(time.Time); ok {
			owner.CreatedAt = v
		}
		if v, ok := ownerArchived.(time.Time); ok {
			owner.ArchivedAt = &v
		}
		vk.OwnerUserEmail = owner.Email
		vk.OwnerUserName = owner.Name
	}
	return vk, team, owner, nil
}

func (s *Store) GetVirtualKeyByID(ctx context.Context, id int64) (*VirtualKey, *Team, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT vk.id, vk.team_id, vk.user_id, vk.service_account_id, vk.key_prefix, vk.name, vk.metadata, vk.allowed_models, vk.expires_at,
		       vk.scoped_rpm, vk.scoped_tpm, vk.max_parallel_requests, vk.scoped_usd_limit_cents, vk.rotated_from_key_id, vk.last_used_at, vk.created_at, vk.revoked_at, vk.disabled_at,
		       COALESCE(u.email, ''), COALESCE(u.name, ''),
		       COALESCE(sa.name, ''),
		       t.id, t.slug, t.name, t.usd_limit_cents, t.period, t.allowed_models, t.rpm, t.tpm, t.max_parallel_requests, t.customer_registration, t.created_at, t.archived_at
		FROM virtual_keys vk
		LEFT JOIN users u ON u.id = vk.user_id
		LEFT JOIN service_accounts sa ON sa.id = vk.service_account_id
		JOIN teams t ON t.id = vk.team_id
		WHERE vk.id = $1
	`, id)
	vk := &VirtualKey{}
	team := &Team{}
	var keyMetadataRaw, keyAllowedRaw, allowedRaw []byte
	err := row.Scan(
		&vk.ID, &vk.TeamID, &vk.UserID, &vk.ServiceAccountID, &vk.KeyPrefix, &vk.Name, &keyMetadataRaw, &keyAllowedRaw, &vk.ExpiresAt,
		&vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ScopedUsdLimitCents,
		&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
		&vk.OwnerUserEmail, &vk.OwnerUserName,
		&vk.ServiceAccountName,
		&team.ID, &team.Slug, &team.Name, &team.UsdLimitCents, &team.Period, &allowedRaw, &team.RPM, &team.TPM, &team.MaxParallelRequests, &team.CustomerRegistration, &team.CreatedAt, &team.ArchivedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, fmt.Errorf("get key by id: %w", err)
	}
	vk.Metadata = parseMetadata(keyMetadataRaw)
	vk.AllowedModels = parseAllowedModels(keyAllowedRaw)
	team.AllowedModels = parseAllowedModels(allowedRaw)
	return vk, team, nil
}

func (s *Store) ListKeysForTeam(ctx context.Context, teamID int64, limit, offset int) ([]*VirtualKey, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT vk.id, vk.team_id, vk.user_id, vk.service_account_id, vk.key_prefix, vk.name, vk.metadata, vk.allowed_models, vk.expires_at,
		       vk.scoped_rpm, vk.scoped_tpm, vk.max_parallel_requests, vk.scoped_usd_limit_cents, vk.rotated_from_key_id, vk.last_used_at, vk.created_at, vk.revoked_at, vk.disabled_at,
		       COALESCE(u.email, ''), COALESCE(u.name, ''),
		       COALESCE(sa.name, ''),
		       COUNT(*) OVER () AS total
		FROM virtual_keys vk
		LEFT JOIN users u ON u.id = vk.user_id
		LEFT JOIN service_accounts sa ON sa.id = vk.service_account_id
		WHERE vk.team_id = $1
		ORDER BY vk.created_at DESC
		LIMIT $2 OFFSET $3
	`, teamID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list keys: %w", err)
	}
	defer rows.Close()
	out := []*VirtualKey{}
	var total int64
	for rows.Next() {
		vk := &VirtualKey{}
		var metadataRaw, allowedRaw []byte
		if err := rows.Scan(
			&vk.ID, &vk.TeamID, &vk.UserID, &vk.ServiceAccountID, &vk.KeyPrefix, &vk.Name, &metadataRaw, &allowedRaw, &vk.ExpiresAt,
			&vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ScopedUsdLimitCents,
			&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
			&vk.OwnerUserEmail, &vk.OwnerUserName,
			&vk.ServiceAccountName,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan key: %w", err)
		}
		vk.Metadata = parseMetadata(metadataRaw)
		vk.AllowedModels = parseAllowedModels(allowedRaw)
		out = append(out, vk)
	}
	return out, total, rows.Err()
}

// ListKeysForUser returns every virtual key owned by a specific user
// (i.e. virtual_keys.user_id = userID). Includes revoked keys; callers
// should filter by RevokedAt as needed.
func (s *Store) ListKeysForUser(ctx context.Context, userID int64, limit, offset int) ([]*VirtualKey, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT vk.id, vk.team_id, vk.user_id, vk.key_prefix, vk.name, vk.metadata, vk.allowed_models, vk.expires_at,
		       vk.scoped_rpm, vk.scoped_tpm, vk.max_parallel_requests, vk.rotated_from_key_id, vk.last_used_at, vk.created_at, vk.revoked_at, vk.disabled_at,
		       COALESCE(u.email, ''), COALESCE(u.name, ''),
		       COUNT(*) OVER () AS total
		FROM virtual_keys vk
		LEFT JOIN users u ON u.id = vk.user_id
		WHERE vk.user_id = $1
		ORDER BY vk.created_at DESC
		LIMIT $2 OFFSET $3
	`, userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list user keys: %w", err)
	}
	defer rows.Close()
	out := []*VirtualKey{}
	var total int64
	for rows.Next() {
		vk := &VirtualKey{}
		var metadataRaw, allowedRaw []byte
		if err := rows.Scan(
			&vk.ID, &vk.TeamID, &vk.UserID, &vk.KeyPrefix, &vk.Name, &metadataRaw, &allowedRaw, &vk.ExpiresAt,
			&vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests,
			&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
			&vk.OwnerUserEmail, &vk.OwnerUserName,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan key: %w", err)
		}
		vk.Metadata = parseMetadata(metadataRaw)
		vk.AllowedModels = parseAllowedModels(allowedRaw)
		out = append(out, vk)
	}
	return out, total, rows.Err()
}

// UpdateVirtualKeyParams is a full-replace of the editable fields on a key.
// The secret material (key_hash, key_prefix), team, and owner are not
// editable — rotate or revoke instead.
type UpdateVirtualKeyParams struct {
	Name                string
	Metadata            map[string]any
	AllowedModels       []string
	ScopedRPM           *int
	ScopedTPM           *int
	MaxParallelRequests *int
	ScopedUsdLimitCents *int64
	ExpiresAt           *time.Time
}

func (s *Store) UpdateVirtualKey(ctx context.Context, id int64, params UpdateVirtualKeyParams) (*VirtualKey, error) {
	if err := ValidateKeyBudget(params.ScopedUsdLimitCents); err != nil {
		return nil, err
	}
	if params.Metadata == nil {
		params.Metadata = map[string]any{}
	}
	metadata, err := json.Marshal(params.Metadata)
	if err != nil {
		return nil, fmt.Errorf("marshal key metadata: %w", err)
	}
	allowed, err := json.Marshal(normalizeAllowedModels(params.AllowedModels))
	if err != nil {
		return nil, fmt.Errorf("marshal key allowlist: %w", err)
	}
	row := s.Pool.QueryRow(ctx, `
		UPDATE virtual_keys
		SET name                   = $2,
		    metadata               = $3::jsonb,
		    allowed_models         = $4::jsonb,
		    scoped_rpm             = $5,
		    scoped_tpm             = $6,
		    max_parallel_requests  = $7,
		    expires_at             = $8,
		    scoped_usd_limit_cents = $9
		WHERE id = $1 AND revoked_at IS NULL
		RETURNING id, team_id, user_id, key_prefix, name, metadata, allowed_models, scoped_rpm, scoped_tpm, max_parallel_requests, scoped_usd_limit_cents, expires_at,
		          rotated_from_key_id, last_used_at, created_at, revoked_at, disabled_at
	`, id, params.Name, metadata, allowed, params.ScopedRPM, params.ScopedTPM, params.MaxParallelRequests, params.ExpiresAt, params.ScopedUsdLimitCents)
	vk := &VirtualKey{}
	var metadataRaw, allowedRaw []byte
	if err := row.Scan(
		&vk.ID, &vk.TeamID, &vk.UserID, &vk.KeyPrefix, &vk.Name, &metadataRaw, &allowedRaw,
		&vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ScopedUsdLimitCents, &vk.ExpiresAt,
		&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update virtual key: %w", err)
	}
	vk.Metadata = parseMetadata(metadataRaw)
	vk.AllowedModels = parseAllowedModels(allowedRaw)
	return vk, nil
}

// parseStringList decodes a JSONB string-array column. Empty / missing
// is treated as nil so callers can compare with len(...) > 0.
func parseStringList(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// SetKeyAllowedCIDRs replaces the IP allowlist on a key. nil or empty
// disables enforcement. Doc 0008 design.
func (s *Store) SetKeyAllowedCIDRs(ctx context.Context, id int64, cidrs []string) error {
	var raw []byte
	if len(cidrs) > 0 {
		var err error
		raw, err = json.Marshal(cidrs)
		if err != nil {
			return fmt.Errorf("marshal cidrs: %w", err)
		}
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE virtual_keys SET allowed_cidrs = $2 WHERE id = $1 AND revoked_at IS NULL`, id, raw)
	if err != nil {
		return fmt.Errorf("set allowed cidrs: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetKeyPaused pauses or resumes a live key. Revoked keys can't be changed.
func (s *Store) SetKeyPaused(ctx context.Context, id int64, paused bool) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE virtual_keys SET disabled_at = CASE WHEN $2 THEN COALESCE(disabled_at, NOW()) ELSE NULL END
		WHERE id = $1 AND revoked_at IS NULL`, id, paused)
	if err != nil {
		return fmt.Errorf("pause key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RotateVirtualKey issues a replacement key with the same settings. With no
// grace period the old key is revoked at once; otherwise it keeps working
// until the grace period ends (or its own earlier expiry), so clients can
// switch over without an outage.
func (s *Store) RotateVirtualKey(ctx context.Context, id int64, teamID int64, keyHash []byte, prefix string, grace ...time.Duration) (*VirtualKey, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userID              *int64
		serviceAccountID    *int64
		name                string
		metadataRaw         []byte
		allowedRaw          []byte
		allowedCIDRsRaw     []byte
		scopedRPM           *int
		scopedTPM           *int
		maxParallelRequests *int
		scopedUsdLimitCents *int64
		expiresAt           *time.Time
		revokedAt           *time.Time
		disabledAt          *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT user_id, service_account_id, name, metadata, allowed_models, allowed_cidrs,
		       scoped_rpm, scoped_tpm, max_parallel_requests, scoped_usd_limit_cents, expires_at, revoked_at, disabled_at
		FROM virtual_keys
		WHERE id = $1 AND team_id = $2
		FOR UPDATE
	`, id, teamID).Scan(&userID, &serviceAccountID, &name, &metadataRaw, &allowedRaw, &allowedCIDRsRaw, &scopedRPM, &scopedTPM, &maxParallelRequests, &scopedUsdLimitCents, &expiresAt, &revokedAt, &disabledAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load key for rotation: %w", err)
	}
	if revokedAt != nil {
		return nil, ErrNotFound
	}
	// The handler's pre-check can race with PauseKey. Recheck while holding
	// the same row lock used by pause, before issuing an active replacement.
	if disabledAt != nil {
		return nil, fmt.Errorf("key is paused; resume it before rotating")
	}
	if expiresAt != nil && expiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("cannot rotate expired key")
	}

	row := tx.QueryRow(ctx, `
		INSERT INTO virtual_keys (team_id, user_id, service_account_id, key_hash, key_prefix, name, metadata, allowed_models, allowed_cidrs, scoped_rpm, scoped_tpm, max_parallel_requests, scoped_usd_limit_cents, expires_at, rotated_from_key_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8::jsonb, $9::jsonb, $10, $11, $12, $13, $14, $15)
		RETURNING id, team_id, user_id, service_account_id, key_prefix, name, metadata, allowed_models, allowed_cidrs, scoped_rpm, scoped_tpm, max_parallel_requests, scoped_usd_limit_cents, expires_at,
		          rotated_from_key_id, last_used_at, created_at, revoked_at, disabled_at
	`, teamID, userID, serviceAccountID, keyHash, prefix, name, metadataRaw, allowedRaw, allowedCIDRsRaw, scopedRPM, scopedTPM, maxParallelRequests, scopedUsdLimitCents, expiresAt, id)
	vk := &VirtualKey{}
	var newMetadataRaw, newAllowedRaw, newAllowedCIDRsRaw []byte
	if err := row.Scan(
		&vk.ID, &vk.TeamID, &vk.UserID, &vk.ServiceAccountID, &vk.KeyPrefix, &vk.Name, &newMetadataRaw, &newAllowedRaw, &newAllowedCIDRsRaw, &vk.ScopedRPM, &vk.ScopedTPM, &vk.MaxParallelRequests, &vk.ScopedUsdLimitCents, &vk.ExpiresAt,
		&vk.RotatedFromKeyID, &vk.LastUsedAt, &vk.CreatedAt, &vk.RevokedAt, &vk.DisabledAt,
	); err != nil {
		return nil, fmt.Errorf("create rotated key: %w", err)
	}
	if len(grace) > 0 && grace[0] > 0 {
		if _, err := tx.Exec(ctx, `
			UPDATE virtual_keys SET expires_at = LEAST(COALESCE(expires_at, 'infinity'::timestamptz), NOW() + make_interval(secs => $2))
			WHERE id = $1 AND revoked_at IS NULL
		`, id, grace[0].Seconds()); err != nil {
			return nil, fmt.Errorf("schedule rotated key expiry: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE virtual_keys SET revoked_at = NOW()
		WHERE id = $1 AND revoked_at IS NULL
	`, id); err != nil {
		return nil, fmt.Errorf("revoke rotated key source: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	vk.Metadata = parseMetadata(newMetadataRaw)
	vk.AllowedModels = parseAllowedModels(newAllowedRaw)
	vk.AllowedCIDRs = parseStringList(newAllowedCIDRsRaw)
	return vk, nil
}

func (s *Store) RevokeKey(ctx context.Context, id int64) error {
	tag, err := s.Pool.Exec(ctx, `
		UPDATE virtual_keys SET revoked_at = NOW()
		WHERE id = $1 AND revoked_at IS NULL
	`, id)
	if err != nil {
		return fmt.Errorf("revoke key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
