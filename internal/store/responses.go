package store

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

type ResponseBinding struct {
	ID                                                                                              string
	TeamID                                                                                          int64
	Owner, CustomerExternalID, Alias, Deployment, TargetFingerprint, UpstreamID, PreviousID, Status string
	TotalTokens                                                                                     int64
}

func (s *Store) CreateResponseBinding(ctx context.Context, b ResponseBinding) error {
	_, err := s.Pool.Exec(ctx, `INSERT INTO response_bindings (id,team_id,owner,customer_external_id,alias,deployment,target_fingerprint,previous_id,upstream_id,status,total_tokens) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, b.ID, b.TeamID, b.Owner, b.CustomerExternalID, b.Alias, b.Deployment, b.TargetFingerprint, b.PreviousID, b.UpstreamID, b.Status, b.TotalTokens)
	return err
}

func (s *Store) GetResponseBinding(ctx context.Context, teamID int64, owner, id string) (*ResponseBinding, error) {
	b := &ResponseBinding{}
	err := s.Pool.QueryRow(ctx, `SELECT id,team_id,owner,customer_external_id,alias,deployment,target_fingerprint,upstream_id,previous_id,total_tokens,status FROM response_bindings WHERE id=$1 AND team_id=$2 AND owner=$3 AND expires_at>NOW()`, id, teamID, owner).Scan(&b.ID, &b.TeamID, &b.Owner, &b.CustomerExternalID, &b.Alias, &b.Deployment, &b.TargetFingerprint, &b.UpstreamID, &b.PreviousID, &b.TotalTokens, &b.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get response binding: %w", err)
	}
	return b, nil
}

func (s *Store) UpdateResponseBinding(ctx context.Context, b ResponseBinding) error {
	result, err := s.Pool.Exec(ctx, `UPDATE response_bindings SET upstream_id=$4,total_tokens=$5,status=$6 WHERE id=$1 AND team_id=$2 AND owner=$3 AND (upstream_id='' OR upstream_id=$4)`, b.ID, b.TeamID, b.Owner, b.UpstreamID, b.TotalTokens, b.Status)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteResponseBinding(ctx context.Context, teamID int64, owner, id string) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM response_bindings WHERE id=$1 AND team_id=$2 AND owner=$3`, id, teamID, owner)
	return err
}

// PruneResponseBindings bounds each retention pass; caller schedules repetition.
func (s *Store) PruneResponseBindings(ctx context.Context) error {
	_, err := s.Pool.Exec(ctx, `DELETE FROM response_bindings WHERE id IN (SELECT id FROM response_bindings WHERE expires_at<NOW() ORDER BY expires_at LIMIT 1000)`)
	return err
}
