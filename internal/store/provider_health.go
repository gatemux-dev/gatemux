package store

import (
	"context"
	"fmt"
	"time"
)

// ProviderHealthSample is one point on the per-deployment health
// timeline. Captured by the sampler in server.startProviderHealthSampler;
// read by the Settings → Providers sparkline.
type ProviderHealthSample struct {
	DeploymentName      string    `json:"deployment_name"`
	SampledAt           time.Time `json:"sampled_at"`
	Ready               bool      `json:"ready"`
	Circuit             string    `json:"circuit"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	RPM5m               *float64  `json:"rpm_5m,omitempty"`
	ErrorPct5m          *float64  `json:"error_pct_5m,omitempty"`
	P50Ms               *float64  `json:"p50_ms,omitempty"`
	P95Ms               *float64  `json:"p95_ms,omitempty"`
}

func (s *Store) InsertProviderHealthSample(ctx context.Context, sample ProviderHealthSample) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO provider_health_samples (
			deployment_name, sampled_at, ready, circuit, consecutive_failures,
			rpm_5m, error_pct_5m, p50_ms, p95_ms
		) VALUES ($1, COALESCE($2, NOW()), $3, $4, $5, $6, $7, $8, $9)
	`, sample.DeploymentName, nullableTime(sample.SampledAt), sample.Ready, sample.Circuit, sample.ConsecutiveFailures,
		sample.RPM5m, sample.ErrorPct5m, sample.P50Ms, sample.P95Ms)
	if err != nil {
		return fmt.Errorf("insert provider health sample: %w", err)
	}
	return nil
}

// ListProviderHealthSamples returns the most recent samples for a
// deployment, oldest-first so the UI can stream them straight into a
// sparkline without flipping the order.
func (s *Store) ListProviderHealthSamples(ctx context.Context, deployment string, limit int) ([]ProviderHealthSample, error) {
	if limit <= 0 || limit > 500 {
		limit = 60 // default: last 30 minutes at 30s sampling
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT deployment_name, sampled_at, ready, circuit, consecutive_failures,
		       rpm_5m, error_pct_5m, p50_ms, p95_ms
		FROM provider_health_samples
		WHERE deployment_name = $1
		ORDER BY sampled_at DESC
		LIMIT $2
	`, deployment, limit)
	if err != nil {
		return nil, fmt.Errorf("list provider health: %w", err)
	}
	defer rows.Close()
	out := []ProviderHealthSample{}
	for rows.Next() {
		var s ProviderHealthSample
		if err := rows.Scan(&s.DeploymentName, &s.SampledAt, &s.Ready, &s.Circuit, &s.ConsecutiveFailures,
			&s.RPM5m, &s.ErrorPct5m, &s.P50Ms, &s.P95Ms); err != nil {
			return nil, fmt.Errorf("scan provider health: %w", err)
		}
		out = append(out, s)
	}
	// Reverse so caller gets oldest→newest.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}

// PruneProviderHealthSamplesOlderThan keeps the table bounded. Called
// by the sampler goroutine on a coarse interval.
func (s *Store) PruneProviderHealthSamplesOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	cmd, err := s.Pool.Exec(ctx, `DELETE FROM provider_health_samples WHERE sampled_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune provider health: %w", err)
	}
	return cmd.RowsAffected(), nil
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
