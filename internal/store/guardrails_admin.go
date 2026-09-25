package store

import (
	"context"
	"time"
)

type GuardrailCatalogRow struct {
	ScopeType    string     `json:"scope_type"`
	ScopeID      string     `json:"scope_id"`
	ScopeLabel   string     `json:"scope_label"`
	Name         string     `json:"name"`
	Mode         string     `json:"mode"`
	Hits24h      int64      `json:"hits_24h"`
	Blocks24h    int64      `json:"blocks_24h"`
	LastDecision *time.Time `json:"last_decision,omitempty"`
}

// Bounded catalog projection. Invalid legacy rows remain visible for repair.
func (s *Store) ListGuardrailCatalog(ctx context.Context) ([]GuardrailCatalogRow, error) {
	rows, err := s.Pool.Query(ctx, `WITH scopes AS (
		SELECT 'team' AS kind,slug AS subject,name AS label,id AS team_id,guardrails AS specs FROM teams WHERE archived_at IS NULL
		UNION ALL SELECT 'alias',alias,alias,NULL,guardrails FROM model_aliases
	), policies AS (
		SELECT kind,subject,label,team_id,p FROM scopes CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(specs)='array' THEN specs ELSE '[{"name":"invalid-configuration","mode":"invalid"}]'::jsonb END) p
		ORDER BY kind,subject,p->>'name' LIMIT 500
	) SELECT kind,subject,label,COALESCE(p->>'name','invalid-configuration'),COALESCE(p->>'mode','invalid'),
		stats.hits,stats.blocks,stats.last_at FROM policies LEFT JOIN LATERAL (
		SELECT count(*) FILTER(WHERE decision<>'allow') AS hits,count(*) FILTER(WHERE decision IN ('block','error','unsupported')) AS blocks,max(created_at) AS last_at
		FROM guardrail_decisions d WHERE d.created_at>=NOW()-interval '24 hours'
		AND d.guardrail_name=kind||'/'||(p->>'name') AND ((kind='team' AND d.team_id=policies.team_id) OR (kind='alias' AND d.alias=subject))
	) stats ON true`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuardrailCatalogRow{}
	for rows.Next() {
		var row GuardrailCatalogRow
		if err = rows.Scan(&row.ScopeType, &row.ScopeID, &row.ScopeLabel, &row.Name, &row.Mode, &row.Hits24h, &row.Blocks24h, &row.LastDecision); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
