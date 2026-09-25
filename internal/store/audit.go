package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type AuditEvent struct {
	ID           int64          `json:"id"`
	ActorType    string         `json:"actor_type"`
	ActorID      string         `json:"actor_id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
}

func (s *Store) InsertAuditEvent(ctx context.Context, event AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return fmt.Errorf("marshal audit metadata: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO audit_log (actor_type, actor_id, action, resource_type, resource_id, metadata)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb)
	`, event.ActorType, event.ActorID, event.Action, event.ResourceType, event.ResourceID, metadata)
	if err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

// LatestConfigChange returns the most recent audit row that represents a
// configuration mutation. Used by /admin/info → Settings → Runtime.
// Filters to mutating actions ("*.update", "*.create", "*.delete",
// "*.rotate", "*.revoke") so login/refresh noise doesn't show as the
// "latest change". Returns (nil, nil) when the audit log is empty.
func (s *Store) LatestConfigChange(ctx context.Context) (*AuditEvent, error) {
	row := s.Pool.QueryRow(ctx, `
		SELECT id, actor_type, actor_id, action, resource_type, resource_id, metadata, created_at
		FROM audit_log
		WHERE action ~ '\.(update|create|delete|rotate|revoke)$'
		ORDER BY created_at DESC
		LIMIT 1
	`)
	event := &AuditEvent{}
	var metadataRaw []byte
	if err := row.Scan(
		&event.ID, &event.ActorType, &event.ActorID, &event.Action,
		&event.ResourceType, &event.ResourceID, &metadataRaw, &event.CreatedAt,
	); err != nil {
		// Pool.QueryRow surfaces ErrNoRows on no match; bubble nil so the
		// caller can treat it as "no recent change yet".
		if err.Error() == "no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("latest config change: %w", err)
	}
	event.Metadata = parseMetadata(metadataRaw)
	return event, nil
}

// ListAuditEventsSearch is ListAuditEvents with an optional case-insensitive
// substring match across actor, action, and resource_id. Empty query
// behaves identically to ListAuditEvents.
func (s *Store) ListAuditEventsSearch(ctx context.Context, q string, limit, offset int) ([]*AuditEvent, int64, error) {
	return s.ListAuditEventsFiltered(ctx, AuditFilter{Query: q}, limit, offset)
}

// AuditFilter narrows the audit log. Query matches actor, action and
// resource text; the type fields match exactly.
type AuditFilter struct {
	Query        string
	ResourceType string
	ActorType    string
}

func (s *Store) ListAuditEventsFiltered(ctx context.Context, f AuditFilter, limit, offset int) ([]*AuditEvent, int64, error) {
	q := strings.TrimSpace(f.Query)
	if q == "" && f.ResourceType == "" && f.ActorType == "" {
		return s.ListAuditEvents(ctx, limit, offset)
	}
	limit, offset = NormalizePage(limit, offset)
	conds := []string{}
	args := []any{}
	if q != "" {
		args = append(args, "%"+strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q))+"%")
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(LOWER(actor_id) LIKE $%[1]d OR LOWER(action) LIKE $%[1]d OR LOWER(resource_id) LIKE $%[1]d OR LOWER(resource_type) LIKE $%[1]d)`, n))
	}
	if f.ResourceType != "" {
		args = append(args, f.ResourceType)
		conds = append(conds, fmt.Sprintf("resource_type = $%d", len(args)))
	}
	if f.ActorType != "" {
		args = append(args, f.ActorType)
		conds = append(conds, fmt.Sprintf("actor_type = $%d", len(args)))
	}
	args = append(args, limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT id, actor_type, actor_id, action, resource_type, resource_id, metadata, created_at,
		       COUNT(*) OVER () AS total
		FROM audit_log
		WHERE `+strings.Join(conds, " AND ")+`
		ORDER BY created_at DESC
		LIMIT $`+fmt.Sprint(len(args)-1)+` OFFSET $`+fmt.Sprint(len(args)), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search audit events: %w", err)
	}
	defer rows.Close()
	out := []*AuditEvent{}
	var total int64
	for rows.Next() {
		event := &AuditEvent{}
		var metadataRaw []byte
		if err := rows.Scan(
			&event.ID, &event.ActorType, &event.ActorID, &event.Action,
			&event.ResourceType, &event.ResourceID, &metadataRaw, &event.CreatedAt, &total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan audit search: %w", err)
		}
		event.Metadata = parseMetadata(metadataRaw)
		out = append(out, event)
	}
	return out, total, rows.Err()
}

// AuditFacets lists the resource and actor types present in the log so the
// console's filters offer only values that match something.
type AuditFacets struct {
	ResourceTypes []string `json:"resource_types"`
	ActorTypes    []string `json:"actor_types"`
}

func (s *Store) GetAuditFacets(ctx context.Context) (AuditFacets, error) {
	out := AuditFacets{ResourceTypes: []string{}, ActorTypes: []string{}}
	for _, col := range []struct {
		name string
		dst  *[]string
	}{{"resource_type", &out.ResourceTypes}, {"actor_type", &out.ActorTypes}} {
		rows, err := s.Pool.Query(ctx, `SELECT DISTINCT `+col.name+` FROM audit_log ORDER BY 1 LIMIT 200`)
		if err != nil {
			return out, fmt.Errorf("audit facets: %w", err)
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return out, err
			}
			*col.dst = append(*col.dst, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// ListAuditEvents returns a single page of the append-only audit log
// (ordered newest first) plus the total event count, for paginated UIs.
// COUNT(*) OVER () computes the unfiltered total alongside the page so
// we don't need a second query to render "X of N".
func (s *Store) ListAuditEvents(ctx context.Context, limit, offset int) ([]*AuditEvent, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx, `
		SELECT id, actor_type, actor_id, action, resource_type, resource_id, metadata, created_at,
		       COUNT(*) OVER () AS total
		FROM audit_log
		ORDER BY created_at DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit events: %w", err)
	}
	defer rows.Close()
	out := []*AuditEvent{}
	var total int64
	for rows.Next() {
		event := &AuditEvent{}
		var metadataRaw []byte
		if err := rows.Scan(
			&event.ID,
			&event.ActorType,
			&event.ActorID,
			&event.Action,
			&event.ResourceType,
			&event.ResourceID,
			&metadataRaw,
			&event.CreatedAt,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan audit event: %w", err)
		}
		event.Metadata = parseMetadata(metadataRaw)
		out = append(out, event)
	}
	return out, total, rows.Err()
}
