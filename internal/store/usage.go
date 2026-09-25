package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type UsageEntry struct {
	AccountingID         string
	Accounting           string
	TokenDetails         json.RawMessage `json:",omitempty"`
	TeamID               int64
	UserID               *int64
	KeyID                *int64
	CustomerID           *int64
	CustomerExternalID   string
	Alias                string
	DeploymentName       string
	RequestID            string
	ModelRequested       string
	ModelUsed            string
	PromptTokens         int
	CompletionTokens     int
	TotalTokens          int
	CostCents            int64
	LatencyMs            int
	QueueMs              int // pre-call work (auth, budget check, registry resolve)
	UpstreamMs           int // upstream HTTP call duration (request → response complete)
	TTFBMs               int // upstream time-to-first-byte (streaming only; same as UpstreamMs otherwise)
	PostprocessMs        int // post-call work (logging, callbacks)
	StatusCode           int
	Error                string
	Cached               bool
	CacheOriginRequestID string
	RequestTags          []string
	ClientIP             string
	ServiceAccountID     *int64
	Ts                   time.Time
}

// UsageRow is the joined view returned to the admin UI. The pointer
// fields are nullable in the DB and bubble through as undefined in JSON.
type UsageRow struct {
	Accounting         string
	TokenDetails       json.RawMessage
	ID                 int64
	TeamSlug           string
	TeamName           string
	Alias              string
	DeploymentName     string
	ProviderType       string
	Strategy           string
	ModelUsed          string
	PromptTokens       int
	CompletionTokens   int
	TotalTokens        int
	CostCents          int64
	LatencyMs          int
	QueueMs            *int
	UpstreamMs         *int
	TTFBMs             *int
	PostprocessMs      *int
	StatusCode         int
	Error              string
	Ts                 time.Time
	RequestID          string
	UserID             *int64
	UserEmail          string
	KeyID              *int64
	KeyPrefix          string
	KeyName            string
	CustomerExternalID string
	RequestTags        []string
	Cached             bool
	ClientIP           string
	HasPayload         bool
	// Filled for list pages only (see enrichUsageRows): the alias's primary
	// deployment, so a row served elsewhere reads as a fallback, and the
	// strongest guardrail decision recorded for the request.
	PrimaryDeployment string
	GuardrailDecision string
}

type UsageFilter struct {
	TeamSlug string
	// Since bounds rows to ts >= Since when non-zero, so the console's list
	// can cover exactly the same window as its aggregate tiles.
	Since time.Time
	// Optional narrowing; every filter runs in SQL so results and counts
	// cover the whole window, not just the loaded page.
	Alias       string
	KeyPrefix   string
	StatusClass string // "2xx", "4xx", "5xx", or "error" (any status >= 400)
	LatencyBand string // "fast" (<200ms), "med" (200-1000ms), "slow" (>1000ms)
	Query       string // substring over request id, alias, team, deployment, key prefix, user email
	Limit       int
	Offset      int
}

// UsageFacets are status-class counts for a filter, computed with the
// status filter itself ignored so the console can show all three options.
type UsageFacets struct {
	OK          int64 `json:"2xx"`
	ClientError int64 `json:"4xx"`
	ServerError int64 `json:"5xx"`
}

func (s *Store) InsertUsage(ctx context.Context, e UsageEntry) error {
	_, err := s.InsertUsageWithID(ctx, e)
	return err
}

// InsertUsageWithID is InsertUsage but returns the inserted row's id so
// the caller can attach a payload row in usage_log_payloads.
func (s *Store) InsertUsageWithID(ctx context.Context, e UsageEntry) (int64, error) {
	return insertUsage(ctx, s.Pool, e)
}

type usageQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func insertUsage(ctx context.Context, q usageQuerier, e UsageEntry) (int64, error) {
	if e.Accounting == "" {
		e.Accounting = "legacy"
	}
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	var errPtr *string
	if e.Error != "" {
		errPtr = &e.Error
	}
	var cacheOrigin *string
	if e.CacheOriginRequestID != "" {
		cacheOrigin = &e.CacheOriginRequestID
	}
	var custExt *string
	if e.CustomerExternalID != "" {
		custExt = &e.CustomerExternalID
	}
	tags := e.RequestTags
	if tags == nil {
		tags = []string{}
	}
	queueMs := nullableInt(e.QueueMs)
	upstreamMs := nullableInt(e.UpstreamMs)
	ttfbMs := nullableInt(e.TTFBMs)
	postMs := nullableInt(e.PostprocessMs)
	tokenDetails := e.TokenDetails
	if strings.TrimSpace(string(tokenDetails)) == "null" {
		tokenDetails = nil
	}
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO usage_log (
			team_id, user_id, key_id, alias, deployment_name,
			request_id, model_requested, model_used,
			prompt_tokens, completion_tokens, total_tokens,
			cost_cents, latency_ms, status_code, error, ts,
			cached, cache_origin_request_id,
			customer_id, customer_external_id,
			request_tags,
			queue_ms, upstream_ms, ttfb_ms, postprocess_ms,
			client_ip,
			service_account_id, token_details, accounting_id, accounting_state
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8,
			$9, $10, $11,
			$12, $13, $14, $15, $16,
			$17, $18,
			$19, $20,
			$21,
			$22, $23, $24, $25,
			$26,
			$27, $28, NULLIF($29,''), $30
		)
		ON CONFLICT (accounting_id) DO UPDATE SET accounting_id=EXCLUDED.accounting_id
		RETURNING id
	`,
		e.TeamID, e.UserID, e.KeyID, e.Alias, e.DeploymentName,
		e.RequestID, e.ModelRequested, e.ModelUsed,
		e.PromptTokens, e.CompletionTokens, e.TotalTokens,
		e.CostCents, e.LatencyMs, e.StatusCode, errPtr, e.Ts,
		e.Cached, cacheOrigin,
		e.CustomerID, custExt,
		tags,
		queueMs, upstreamMs, ttfbMs, postMs,
		nullableInet(e.ClientIP),
		e.ServiceAccountID, tokenDetails, e.AccountingID, e.Accounting,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert usage: %w", err)
	}
	return id, nil
}

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullableInet returns nil when the IP string is empty so the column
// stays NULL; otherwise the string is passed through (Postgres parses
// IPv4 / IPv6 directly into INET).
func nullableInet(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// usageSelect is the canonical SELECT used by both ListUsage and
// ListUsageForUser. JOINs users + virtual_keys for the caller card,
// deployments + model_aliases for routing context, and LEFT JOINs
// usage_log_payloads to surface a "has body captured" flag without
// pulling the body itself (that's fetched on detail-row open).
const usageSelect = `
	SELECT u.id, t.slug, t.name,
	       u.alias, COALESCE(u.deployment_name, ''),
	       COALESCE(d.provider_type, ''), COALESCE(a.strategy, 'priority'),
	       COALESCE(u.model_used, ''),
	       u.prompt_tokens, u.completion_tokens, u.total_tokens,
	       u.cost_cents, u.latency_ms,
	       u.queue_ms, u.upstream_ms, u.ttfb_ms, u.postprocess_ms,
	       u.status_code, COALESCE(u.error, ''), u.ts,
	       COALESCE(u.request_id, ''),
	       u.user_id, COALESCE(usr.email, ''),
	       u.key_id, COALESCE(vk.key_prefix, ''), COALESCE(vk.name, ''),
	       COALESCE(u.customer_external_id, ''),
	       COALESCE(u.request_tags, '{}'::text[]),
	       u.cached,
	       COALESCE(host(u.client_ip), ''),
	       (p.usage_id IS NOT NULL) AS has_payload, u.token_details, u.accounting_state,
	       COUNT(*) OVER () AS total
	FROM usage_log u
	JOIN teams t ON t.id = u.team_id
	LEFT JOIN deployments d ON d.name = u.deployment_name
	LEFT JOIN model_aliases a ON a.alias = u.alias
	LEFT JOIN users usr ON usr.id = u.user_id
	LEFT JOIN virtual_keys vk ON vk.id = u.key_id
	LEFT JOIN usage_log_payloads p ON p.usage_id = u.id
`

func scanUsageRow(rows interface {
	Scan(...any) error
}) (*UsageRow, int64, error) {
	r := &UsageRow{}
	var total int64
	var tags []string
	if err := rows.Scan(
		&r.ID, &r.TeamSlug, &r.TeamName,
		&r.Alias, &r.DeploymentName,
		&r.ProviderType, &r.Strategy,
		&r.ModelUsed,
		&r.PromptTokens, &r.CompletionTokens, &r.TotalTokens,
		&r.CostCents, &r.LatencyMs,
		&r.QueueMs, &r.UpstreamMs, &r.TTFBMs, &r.PostprocessMs,
		&r.StatusCode, &r.Error, &r.Ts,
		&r.RequestID,
		&r.UserID, &r.UserEmail,
		&r.KeyID, &r.KeyPrefix, &r.KeyName,
		&r.CustomerExternalID,
		&tags,
		&r.Cached,
		&r.ClientIP,
		&r.HasPayload, &r.TokenDetails, &r.Accounting,
		&total,
	); err != nil {
		return nil, 0, err
	}
	r.RequestTags = tags
	return r, total, nil
}

// ListUsageForUser returns the most recent usage rows for which user_id
// matches the provided id. Used by /me/usage so a non-admin user only ever
// sees their own keys' traffic.
func (s *Store) ListUsageForUser(ctx context.Context, userID int64, limit, offset int) ([]*UsageRow, int64, error) {
	limit, offset = NormalizePage(limit, offset)
	rows, err := s.Pool.Query(ctx,
		usageSelect+`WHERE u.user_id = $1 ORDER BY u.ts DESC LIMIT $2 OFFSET $3`,
		userID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list user usage: %w", err)
	}
	defer rows.Close()
	out := []*UsageRow{}
	var total int64
	for rows.Next() {
		r, t, err := scanUsageRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan usage: %w", err)
		}
		total = t
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// usageConditions renders f as SQL predicates over usageSelect's aliases.
// withStatus=false omits the status-class predicate (for facet counts).
func usageConditions(f UsageFilter, withStatus bool) ([]string, []any) {
	conds := []string{}
	args := []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		conds = append(conds, fmt.Sprintf(expr, len(args)))
	}
	if f.TeamSlug != "" {
		add("t.slug = $%d", f.TeamSlug)
	}
	if !f.Since.IsZero() {
		add("u.ts >= $%d", f.Since)
	}
	if f.Alias != "" {
		add("u.alias = $%d", f.Alias)
	}
	if f.KeyPrefix != "" {
		add("vk.key_prefix = $%d", f.KeyPrefix)
	}
	switch f.LatencyBand {
	case "fast":
		conds = append(conds, "u.latency_ms < 200")
	case "med":
		conds = append(conds, "u.latency_ms BETWEEN 200 AND 1000")
	case "slow":
		conds = append(conds, "u.latency_ms > 1000")
	}
	if withStatus {
		switch f.StatusClass {
		case "2xx":
			conds = append(conds, "u.status_code < 300")
		case "4xx":
			conds = append(conds, "u.status_code BETWEEN 400 AND 499")
		case "5xx":
			conds = append(conds, "u.status_code >= 500")
		case "error":
			conds = append(conds, "u.status_code >= 400")
		}
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		args = append(args, pattern)
		n := len(args)
		conds = append(conds, fmt.Sprintf(`(u.request_id ILIKE $%[1]d OR u.alias ILIKE $%[1]d OR t.slug ILIKE $%[1]d
			OR u.deployment_name ILIKE $%[1]d OR vk.key_prefix ILIKE $%[1]d OR usr.email ILIKE $%[1]d
			OR u.id::text = $%[2]d)`, n, n+1))
		args = append(args, q)
	}
	return conds, args
}

func (s *Store) ListUsage(ctx context.Context, f UsageFilter) ([]*UsageRow, int64, error) {
	f.Limit, f.Offset = NormalizePage(f.Limit, f.Offset)
	conds, args := usageConditions(f, true)
	query := usageSelect
	if len(conds) > 0 {
		query += "WHERE " + strings.Join(conds, " AND ") + " "
	}
	args = append(args, f.Limit, f.Offset)
	query += fmt.Sprintf(`ORDER BY u.ts DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args))
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list usage: %w", err)
	}
	defer rows.Close()
	out := []*UsageRow{}
	var total int64
	for rows.Next() {
		r, t, err := scanUsageRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan usage: %w", err)
		}
		total = t
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.enrichUsageRows(ctx, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// UsageFacetCounts counts rows per status class under f (status ignored).
func (s *Store) UsageFacetCounts(ctx context.Context, f UsageFilter) (UsageFacets, error) {
	conds, args := usageConditions(f, false)
	query := `SELECT
		COUNT(*) FILTER (WHERE u.status_code < 300),
		COUNT(*) FILTER (WHERE u.status_code BETWEEN 400 AND 499),
		COUNT(*) FILTER (WHERE u.status_code >= 500)
	FROM usage_log u
	JOIN teams t ON t.id = u.team_id
	LEFT JOIN users usr ON usr.id = u.user_id
	LEFT JOIN virtual_keys vk ON vk.id = u.key_id `
	if len(conds) > 0 {
		query += "WHERE " + strings.Join(conds, " AND ")
	}
	var out UsageFacets
	err := s.Pool.QueryRow(ctx, query, args...).Scan(&out.OK, &out.ClientError, &out.ServerError)
	if err != nil {
		return out, fmt.Errorf("usage facets: %w", err)
	}
	return out, nil
}

// GetUsageRow loads one request for its permalink, scoped to a team when
// teamSlug is non-empty so managers cannot open other teams' requests.
func (s *Store) GetUsageRow(ctx context.Context, id int64, teamSlug string) (*UsageRow, error) {
	query := usageSelect + "WHERE u.id = $1"
	args := []any{id}
	if teamSlug != "" {
		query += " AND t.slug = $2"
		args = append(args, teamSlug)
	}
	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("get usage row: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	r, _, err := scanUsageRow(rows)
	if err != nil {
		return nil, fmt.Errorf("scan usage: %w", err)
	}
	rows.Close()
	if err := s.enrichUsageRows(ctx, []*UsageRow{r}); err != nil {
		return nil, err
	}
	return r, nil
}

// guardrailRank orders decisions by how strongly they changed the request.
var guardrailRank = map[string]int{"block": 5, "error": 4, "unsupported": 3, "redact": 2, "flag": 1}

// enrichUsageRows adds the primary deployment and strongest guardrail
// decision for one page of rows in two keyed queries, rather than per-row
// subqueries that would run across the whole window count.
func (s *Store) enrichUsageRows(ctx context.Context, rows []*UsageRow) error {
	if len(rows) == 0 {
		return nil
	}
	aliases := map[string]bool{}
	requestIDs := []string{}
	for _, r := range rows {
		aliases[r.Alias] = true
		if r.RequestID != "" {
			requestIDs = append(requestIDs, r.RequestID)
		}
	}
	aliasList := make([]string, 0, len(aliases))
	for a := range aliases {
		aliasList = append(aliasList, a)
	}
	primary := map[string]string{}
	prows, err := s.Pool.Query(ctx, `
		SELECT DISTINCT ON (a.alias) a.alias, d.name
		FROM model_aliases a
		JOIN alias_deployments ad ON ad.alias_id = a.id
		JOIN deployments d ON d.id = ad.deployment_id
		WHERE a.alias = ANY($1)
		ORDER BY a.alias, ad.priority, d.name`, aliasList)
	if err != nil {
		return fmt.Errorf("primary deployments: %w", err)
	}
	for prows.Next() {
		var alias, name string
		if err := prows.Scan(&alias, &name); err != nil {
			prows.Close()
			return err
		}
		primary[alias] = name
	}
	prows.Close()
	decisions := map[string]string{}
	if len(requestIDs) > 0 {
		grows, err := s.Pool.Query(ctx, `SELECT request_id, decision FROM guardrail_decisions WHERE request_id = ANY($1)`, requestIDs)
		if err != nil {
			return fmt.Errorf("guardrail decisions: %w", err)
		}
		for grows.Next() {
			var id, decision string
			if err := grows.Scan(&id, &decision); err != nil {
				grows.Close()
				return err
			}
			if guardrailRank[decision] > guardrailRank[decisions[id]] {
				decisions[id] = decision
			}
		}
		grows.Close()
	}
	for _, r := range rows {
		r.PrimaryDeployment = primary[r.Alias]
		r.GuardrailDecision = decisions[r.RequestID]
	}
	return nil
}

// GetUsageByID returns the team_id, key_id, user_id, and alias for a
// single usage_log row. Used by the replay endpoint to reconstruct the
// auth context the original request ran under.
func (s *Store) GetUsageByID(ctx context.Context, id int64) (teamID int64, keyID *int64, userID *int64, alias string, err error) {
	err = s.Pool.QueryRow(ctx, `
		SELECT team_id, key_id, user_id, alias
		FROM usage_log
		WHERE id = $1
	`, id).Scan(&teamID, &keyID, &userID, &alias)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, nil, nil, "", ErrNotFound
		}
		return 0, nil, nil, "", fmt.Errorf("get usage row: %w", err)
	}
	return teamID, keyID, userID, alias, nil
}

// GetUsagePayload returns the raw request/response bodies captured for
// the given usage_log row, if any. Returns ErrNotFound when no payload
// row exists (capture was off, retention rolled it off, or never inserted).
func (s *Store) GetUsagePayload(ctx context.Context, usageID int64) ([]byte, []byte, time.Time, error) {
	var req, resp []byte
	var capturedAt time.Time
	err := s.Pool.QueryRow(ctx, `
		SELECT request_body, response_body, captured_at
		FROM usage_log_payloads
		WHERE usage_id = $1
	`, usageID).Scan(&req, &resp, &capturedAt)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return nil, nil, time.Time{}, ErrNotFound
		}
		return nil, nil, time.Time{}, fmt.Errorf("get payload: %w", err)
	}
	return req, resp, capturedAt, nil
}

// InsertUsagePayload writes captured request + response bodies for a
// usage row. Caller must already have inserted the usage_log row.
func (s *Store) InsertUsagePayload(ctx context.Context, usageID int64, requestBody, responseBody []byte) error {
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO usage_log_payloads (usage_id, request_body, response_body)
		VALUES ($1, $2::jsonb, $3::jsonb)
		ON CONFLICT (usage_id) DO UPDATE
		    SET request_body = EXCLUDED.request_body,
		        response_body = EXCLUDED.response_body,
		        captured_at = NOW()
	`, usageID, requestBody, responseBody)
	if err != nil {
		return fmt.Errorf("insert payload: %w", err)
	}
	return nil
}

// TeamCapturesPayloads is a fast lookup used in the v1 hot path so we
// only do the body read+insert when a team has explicitly opted in.
func (s *Store) TeamCapturesPayloads(ctx context.Context, teamID int64) (bool, error) {
	var on bool
	err := s.Pool.QueryRow(ctx, `SELECT capture_payloads FROM teams WHERE id = $1`, teamID).Scan(&on)
	if err != nil {
		return false, fmt.Errorf("team capture flag: %w", err)
	}
	return on, nil
}

// PrunePayloadsOlderThan deletes captured request/response bodies older
// than `cutoff`. Returns the number of rows removed so the caller can
// log it. Used by the retention goroutine started in server.New.
func (s *Store) PrunePayloadsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	cmd, err := s.Pool.Exec(ctx, `
		DELETE FROM usage_log_payloads
		WHERE captured_at < $1
	`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune payloads: %w", err)
	}
	return cmd.RowsAffected(), nil
}

// SetTeamCapturePayloads toggles per-team payload capture from the admin UI.
func (s *Store) SetTeamCapturePayloads(ctx context.Context, slug string, on bool) error {
	cmd, err := s.Pool.Exec(ctx, `UPDATE teams SET capture_payloads = $2 WHERE slug = $1`, slug, on)
	if err != nil {
		return fmt.Errorf("set team capture flag: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
