package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

type GuardrailDecision struct {
	ID            int64
	RequestID     string
	TeamID        *int64
	Alias         string
	GuardrailName string
	Phase         string
	Decision      string
	Severity      string
	Reason        string
	Tags          []string
	LatencyMs     int
	CreatedAt     time.Time
}

func (s *Store) InsertGuardrailDecision(ctx context.Context, d GuardrailDecision) error {
	tagsJSON, _ := json.Marshal(d.Tags)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO guardrail_decisions (
			request_id, team_id, alias, guardrail_name, phase, decision,
			severity, reason, tags, latency_ms
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10
		)
	`,
		d.RequestID, d.TeamID, d.Alias, d.GuardrailName, d.Phase, d.Decision,
		d.Severity, d.Reason, tagsJSON, d.LatencyMs,
	)
	if err != nil {
		return fmt.Errorf("insert guardrail decision: %w", err)
	}
	return nil
}
