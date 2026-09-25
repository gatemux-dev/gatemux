package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"

	"github.com/jackc/pgx/v5"
)

// DeploymentCapabilities is the wire shape of the capabilities JSONB
// column. nil pointers on Deployment.Capabilities mean "fall back to the
// provider type's defaults" — the router resolves precedence.
type DeploymentCapabilities struct {
	Responses           *bool `json:"responses,omitempty"`
	Chat                bool  `json:"chat"`
	StreamChat          bool  `json:"stream_chat"`
	Embeddings          bool  `json:"embeddings"`
	Moderation          bool  `json:"moderation,omitempty"`
	Rerank              bool  `json:"rerank,omitempty"`
	Images              bool  `json:"images,omitempty"`
	AudioTranscribe     bool  `json:"audio_transcribe,omitempty"`
	AudioSpeech         bool  `json:"audio_speech,omitempty"`
	MessagesPassthrough bool  `json:"messages_passthrough,omitempty"`
}

type Deployment struct {
	ID                  int64
	Name                string
	ProviderType        string
	UpstreamModel       string
	CredentialRef       string
	BaseURL             *string
	Region              *string
	Enabled             bool
	Weight              int
	MaxParallelRequests *int
	Tags                []string
	Capabilities        *DeploymentCapabilities
	Streaming           *config.StreamingConfig
	CreatedAt           time.Time
}

// RoutingIdentity excludes secret values but pins stored upstream resources to
// a deployment identity, endpoint, credential reference and model configuration.
func (d *Deployment) RoutingIdentity() string {
	body, _ := json.Marshal([]any{d.ID, d.ProviderType, d.UpstreamModel, d.BaseURL, d.CredentialRef, d.Region})
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

// ListDeploymentsPage is the paginated variant for the admin UI. The
// existing ListDeployments stays unpaginated because the router and the
// /admin/info endpoint both need the full set on every refresh.
// ListDeploymentsPage pages deployments; query matches the name, upstream
// model or provider type.
func (s *Store) ListDeploymentsPage(ctx context.Context, limit, offset int, query string) ([]*Deployment, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	pattern := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.TrimSpace(query))
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, provider_type, upstream_model, credential_ref,
		       base_url, region, enabled, weight, max_parallel_requests, capabilities, streaming, tags, created_at,
		       COUNT(*) OVER () AS total
		FROM deployments
		WHERE ($3 = '' OR name ILIKE '%' || $3 || '%' OR upstream_model ILIKE '%' || $3 || '%' OR provider_type ILIKE '%' || $3 || '%')
		ORDER BY name
		LIMIT $1 OFFSET $2
	`, limit, offset, pattern)
	if err != nil {
		return nil, 0, fmt.Errorf("list deployments page: %w", err)
	}
	defer rows.Close()
	out := []*Deployment{}
	var total int64
	for rows.Next() {
		d := &Deployment{}
		var capsRaw []byte
		if err := rows.Scan(&d.ID, &d.Name, &d.ProviderType, &d.UpstreamModel, &d.CredentialRef,
			&d.BaseURL, &d.Region, &d.Enabled, &d.Weight, &d.MaxParallelRequests, &capsRaw, &d.Streaming, &d.Tags, &d.CreatedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan deployment: %w", err)
		}
		d.Capabilities = parseCapabilities(capsRaw)
		out = append(out, d)
	}
	return out, total, rows.Err()
}

func (s *Store) ListDeployments(ctx context.Context) ([]*Deployment, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT id, name, provider_type, upstream_model, credential_ref,
		       base_url, region, enabled, weight, max_parallel_requests, capabilities, streaming, tags, created_at
		FROM deployments
		ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()
	out := []*Deployment{}
	for rows.Next() {
		d := &Deployment{}
		var capsRaw []byte
		if err := rows.Scan(&d.ID, &d.Name, &d.ProviderType, &d.UpstreamModel, &d.CredentialRef,
			&d.BaseURL, &d.Region, &d.Enabled, &d.Weight, &d.MaxParallelRequests, &capsRaw, &d.Streaming, &d.Tags, &d.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan deployment: %w", err)
		}
		d.Capabilities = parseCapabilities(capsRaw)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDeploymentByName(ctx context.Context, name string) (*Deployment, error) {
	d := &Deployment{}
	var capsRaw []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT id, name, provider_type, upstream_model, credential_ref,
		       base_url, region, enabled, weight, max_parallel_requests, capabilities, streaming, tags, created_at
		FROM deployments WHERE name = $1
	`, name).Scan(&d.ID, &d.Name, &d.ProviderType, &d.UpstreamModel, &d.CredentialRef,
		&d.BaseURL, &d.Region, &d.Enabled, &d.Weight, &d.MaxParallelRequests, &capsRaw, &d.Streaming, &d.Tags, &d.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get deployment by name: %w", err)
	}
	d.Capabilities = parseCapabilities(capsRaw)
	return d, nil
}

// UpsertDeployment writes or updates a deployment row. capabilities=nil
// preserves existing behavior (NULL in DB → provider defaults at runtime);
// pass a populated *DeploymentCapabilities to lock down which capabilities
// the deployment advertises.
func (s *Store) UpsertDeployment(
	ctx context.Context,
	name, providerType, upstreamModel, credentialRef string,
	baseURL, region *string,
	capabilities *DeploymentCapabilities,
	maxParallelRequests *int,
	streaming ...*config.StreamingConfig,
) (*Deployment, error) {
	var streamConfig *config.StreamingConfig
	if len(streaming) > 0 {
		streamConfig = streaming[0]
	}
	if streamConfig != nil {
		if err := streamConfig.Validate(); err != nil {
			return nil, err
		}
	}
	var capsBytes []byte
	if capabilities != nil {
		var err error
		capsBytes, err = json.Marshal(capabilities)
		if err != nil {
			return nil, fmt.Errorf("marshal capabilities: %w", err)
		}
	}
	row := s.Pool.QueryRow(ctx, `
		INSERT INTO deployments (name, provider_type, upstream_model, credential_ref, base_url, region, capabilities, max_parallel_requests, streaming)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (name) DO UPDATE SET
			provider_type  = EXCLUDED.provider_type,
			upstream_model = EXCLUDED.upstream_model,
			credential_ref = EXCLUDED.credential_ref,
			base_url       = EXCLUDED.base_url,
			region         = EXCLUDED.region,
			capabilities   = EXCLUDED.capabilities,
			max_parallel_requests = EXCLUDED.max_parallel_requests,
			streaming = CASE WHEN $10 THEN EXCLUDED.streaming ELSE deployments.streaming END
		RETURNING id, name, provider_type, upstream_model, credential_ref,
		          base_url, region, enabled, weight, max_parallel_requests, capabilities, streaming, tags, created_at
	`, name, providerType, upstreamModel, credentialRef, baseURL, region, capsBytes, maxParallelRequests, streamConfig, len(streaming) > 0)
	d := &Deployment{}
	var capsRaw []byte
	if err := row.Scan(&d.ID, &d.Name, &d.ProviderType, &d.UpstreamModel, &d.CredentialRef,
		&d.BaseURL, &d.Region, &d.Enabled, &d.Weight, &d.MaxParallelRequests, &capsRaw, &d.Streaming, &d.Tags, &d.CreatedAt); err != nil {
		return nil, fmt.Errorf("upsert deployment: %w", err)
	}
	d.Capabilities = parseCapabilities(capsRaw)
	return d, nil
}

// UpdateDeploymentTags replaces the routing tags array on a deployment.
// Tags are free-form strings; the routing layer (doc 0007) uses them to
// filter candidates on tagged-strategy aliases.
func (s *Store) UpdateDeploymentTags(ctx context.Context, name string, tags []string) error {
	if tags == nil {
		tags = []string{}
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE deployments SET tags = $2 WHERE name = $1`, name, tags)
	if err != nil {
		return fmt.Errorf("update deployment tags: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteDeployment(ctx context.Context, name string) error {
	tag, err := s.Pool.Exec(ctx, `DELETE FROM deployments WHERE name = $1`, name)
	if err != nil {
		return fmt.Errorf("delete deployment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) deploymentExists(ctx context.Context, tx pgx.Tx, name string) (bool, error) {
	var dummy int
	err := tx.QueryRow(ctx, `SELECT 1 FROM deployments WHERE name = $1`, name).Scan(&dummy)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func parseCapabilities(raw []byte) *DeploymentCapabilities {
	if len(raw) == 0 {
		return nil
	}
	c := &DeploymentCapabilities{}
	if err := json.Unmarshal(raw, c); err != nil {
		return nil
	}
	return c
}
