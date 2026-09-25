package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/jackc/pgx/v5"
)

// GuardrailPolicies reads fresh policy before upstream IO. No cache can extend a
// removed protection. Legacy malformed/unsupported specs fail closed.
func (s *Store) GuardrailPolicies(ctx context.Context, teamID int64, alias string) ([]guardrails.Policy, error) {
	var teamRaw, aliasRaw []byte
	err := s.Pool.QueryRow(ctx, `SELECT CASE WHEN octet_length(t.guardrails::text)<=65536 THEN t.guardrails ELSE 'null'::jsonb END,
		CASE WHEN octet_length(COALESCE(a.guardrails,'[]'::jsonb)::text)<=65536 THEN COALESCE(a.guardrails,'[]'::jsonb) ELSE 'null'::jsonb END
		FROM teams t LEFT JOIN model_aliases a ON a.alias=$2 WHERE t.id=$1`, teamID, alias).Scan(&teamRaw, &aliasRaw)
	if err != nil {
		return nil, err
	}
	team, err := guardrails.Decode(teamRaw)
	if err != nil {
		return nil, err
	}
	model, err := guardrails.Decode(aliasRaw)
	if err != nil {
		return nil, err
	}
	// Opaque passthrough has no reliable model identity. Reject it if any model
	// policy exists, even without a team policy; otherwise it could bypass one.
	if alias == "" {
		var exists bool
		if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_aliases WHERE guardrails <> '[]'::jsonb)`).Scan(&exists); err != nil {
			return nil, err
		}
		if exists {
			return nil, guardrails.ErrUnsupported
		}
	}
	for i := range team {
		team[i].Name = "team/" + team[i].Name
	}
	for i := range model {
		model[i].Name = "alias/" + model[i].Name
	}
	return append(team, model...), nil
}

func guardrailScope(scope string) (string, error) {
	switch scope {
	case "team":
		return `SELECT guardrails FROM teams WHERE slug=$1 AND archived_at IS NULL FOR UPDATE`, nil
	case "alias":
		return `SELECT guardrails FROM model_aliases WHERE alias=$1 FOR UPDATE`, nil
	default:
		return "", errors.New("scope_type must be team or alias")
	}
}

// ReplaceGuardrailScope is a bounded atomic replacement of one scope's policy
// assignment. Serializes concurrent editors and records the mutation without
// logging term content. The caller supplies the expected old value (CAS).
func (s *Store) ReplaceGuardrailScope(ctx context.Context, scope, id string, expected, policies json.RawMessage, actor string) error {
	query, err := guardrailScope(scope)
	if err != nil {
		return err
	}
	if _, err = guardrails.Decode(policies); err != nil {
		return err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var old json.RawMessage
	if err = tx.QueryRow(ctx, query, id).Scan(&old); errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	var equal bool
	if err = tx.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, old, expected).Scan(&equal); err != nil {
		return err
	}
	if !equal {
		return ErrGuardrailConflict
	}
	update := `UPDATE teams SET guardrails=$2 WHERE slug=$1`
	if scope == "alias" {
		update = `UPDATE model_aliases SET guardrails=$2 WHERE alias=$1`
	}
	if _, err = tx.Exec(ctx, update, id, policies); err != nil {
		return err
	}
	// Existing audit schema is shared with other control-plane operations.
	meta, _ := json.Marshal(map[string]string{"scope_type": scope, "scope_id": id})
	if _, err = tx.Exec(ctx, `INSERT INTO audit_log(actor_type,actor_id,action,resource_type,resource_id,metadata) VALUES('admin',$1,'guardrails.update',$2,$3,$4)`, actor, scope, id, meta); err != nil {
		return fmt.Errorf("audit guardrail update: %w", err)
	}
	return tx.Commit(ctx)
}

var ErrGuardrailConflict = errors.New("guardrail policy changed; refresh before saving")

func (s *Store) GetGuardrailScope(ctx context.Context, scope, id string) (json.RawMessage, error) {
	query := `SELECT guardrails FROM teams WHERE slug=$1 AND archived_at IS NULL`
	if scope == "alias" {
		query = `SELECT guardrails FROM model_aliases WHERE alias=$1`
	} else if scope != "team" {
		return nil, ErrNotFound
	}
	var raw json.RawMessage
	err := s.Pool.QueryRow(ctx, query, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return raw, err
}

// GuardrailAssignment is one scope (team or model alias) that has a non-empty
// guardrail policy array assigned. Policies over the size bound are reported
// as null so a single oversized row cannot break the whole listing.
type GuardrailAssignment struct {
	ScopeType string          `json:"scope_type"`
	ScopeID   string          `json:"scope_id"`
	Policies  json.RawMessage `json:"policies"`
}

// ListGuardrailAssignments returns every team and alias scope that currently
// has guardrails assigned, so administrators can see the whole enforcement
// surface without loading scopes one by one.
func (s *Store) ListGuardrailAssignments(ctx context.Context) ([]GuardrailAssignment, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT 'team' AS scope_type, slug AS scope_id,
		       CASE WHEN octet_length(guardrails::text) <= 65536 THEN guardrails ELSE 'null'::jsonb END
		FROM teams WHERE archived_at IS NULL AND guardrails <> '[]'::jsonb
		UNION ALL
		SELECT 'alias', alias,
		       CASE WHEN octet_length(guardrails::text) <= 65536 THEN guardrails ELSE 'null'::jsonb END
		FROM model_aliases WHERE guardrails IS NOT NULL AND guardrails <> '[]'::jsonb
		ORDER BY 1, 2
		LIMIT 500
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuardrailAssignment{}
	for rows.Next() {
		var a GuardrailAssignment
		if err := rows.Scan(&a.ScopeType, &a.ScopeID, &a.Policies); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
