package store

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// UsageBucket is a single point on the operational dashboard line/area
// charts: request volume, error count, token totals, and latency
// percentiles inside a single bucket window.
type UsageBucket struct {
	Bucket           time.Time `json:"bucket"`
	Requests         int64     `json:"requests"`
	Errors           int64     `json:"errors"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	CostCents        int64     `json:"cost_cents"`
	LatencyP50Ms     float64   `json:"latency_p50_ms"`
	LatencyP95Ms     float64   `json:"latency_p95_ms"`
	CacheHits        int64     `json:"cache_hits"`
}

// SpendBucket is a single point on the spend-over-time chart, with the
// per-alias breakdown that lets us render a stacked bar.
type SpendBucket struct {
	Bucket    time.Time        `json:"bucket"`
	Total     SpendTotals      `json:"total"`
	Aliases   map[string]int64 `json:"aliases"`
}

// SpendTimeseries wraps the bucket series with a stable alias list (ordered
// by total cost descending) so the frontend doesn't have to compute it.
type SpendTimeseries struct {
	From    time.Time     `json:"from"`
	To      time.Time     `json:"to"`
	Bucket  string        `json:"bucket"`
	Series  []SpendBucket `json:"series"`
	Aliases []string      `json:"aliases"`
}

// validateBucket whitelists the date_trunc argument since it's interpolated
// into the SQL string. Postgres accepts more values; we only allow the ones
// the dashboard actually uses.
func validateBucket(bucket string) (string, error) {
	switch bucket {
	case "hour", "day", "week":
		return bucket, nil
	case "":
		return "day", nil
	default:
		return "", fmt.Errorf("unsupported bucket %q (allowed: hour, day, week)", bucket)
	}
}

// defaultRange fills in a 30-day default range when the caller passes
// zero values. Saves callers a few lines and keeps the SQL parameters
// non-null.
func defaultRange(from, to time.Time, days int) (time.Time, time.Time) {
	if to.IsZero() {
		to = time.Now().UTC()
	}
	if from.IsZero() {
		from = to.Add(-time.Duration(days) * 24 * time.Hour)
	}
	return from, to
}

// DeploymentStats5m is per-deployment short-window stats used by the
// Settings → Providers cards. Window length is fixed at 5 minutes to
// match the design spec ("Errors (5m)").
type DeploymentStats5m struct {
	Deployment string  `json:"deployment"`
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	ErrorPct   float64 `json:"error_pct"`
	RPM        float64 `json:"rpm"` // requests per minute, averaged over the window
	P50Ms      float64 `json:"p50_ms"`
	P95Ms      float64 `json:"p95_ms"`
}

// GetDeploymentStats5m returns short-window per-deployment counts so the
// Settings → Providers cards can show RPM and error %. We use the
// deployment_name column on usage_log (present since the registry
// resolves before logging).
func (s *Store) GetDeploymentStats5m(ctx context.Context) ([]DeploymentStats5m, error) {
	rows, err := s.Pool.Query(ctx, `
		SELECT deployment_name,
		       COUNT(*) AS requests,
		       COUNT(*) FILTER (WHERE status_code >= 400) AS errors,
		       COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY latency_ms), 0) AS p50,
		       COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY latency_ms), 0) AS p95
		FROM usage_log
		WHERE ts >= NOW() - INTERVAL '5 minutes'
		  AND deployment_name IS NOT NULL
		GROUP BY deployment_name
	`)
	if err != nil {
		return nil, fmt.Errorf("deployment stats: %w", err)
	}
	defer rows.Close()
	out := []DeploymentStats5m{}
	for rows.Next() {
		s := DeploymentStats5m{}
		if err := rows.Scan(&s.Deployment, &s.Requests, &s.Errors, &s.P50Ms, &s.P95Ms); err != nil {
			return nil, fmt.Errorf("scan deployment stats: %w", err)
		}
		if s.Requests > 0 {
			s.ErrorPct = (float64(s.Errors) / float64(s.Requests)) * 100
		}
		s.RPM = float64(s.Requests) / 5.0
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetUsageAggregate returns a per-bucket breakdown of usage_log rows.
// Latency percentiles use Postgres's ordered-set aggregates — fine at
// MVP volumes; if we ever cross a few million rows per query we'd add a
// pre-aggregated rollup table.
func (s *Store) GetUsageAggregate(ctx context.Context, teamSlug, alias string, from, to time.Time, bucket string) ([]UsageBucket, error) {
	bucketTrunc, err := validateBucket(bucket)
	if err != nil {
		return nil, err
	}
	from, to = defaultRange(from, to, 7)

	args := []any{from, to}
	where := "u.ts >= $1 AND u.ts < $2"
	if teamSlug != "" {
		args = append(args, teamSlug)
		where += fmt.Sprintf(" AND t.slug = $%d", len(args))
	}
	if alias != "" {
		args = append(args, alias)
		where += fmt.Sprintf(" AND u.alias = $%d", len(args))
	}

	query := fmt.Sprintf(`
		SELECT date_trunc('%s', u.ts) AS bucket,
		       COUNT(*) AS requests,
		       COUNT(*) FILTER (WHERE u.status_code >= 400) AS errors,
		       COALESCE(SUM(u.prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(u.completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(u.total_tokens), 0) AS total_tokens,
		       COALESCE(SUM(u.cost_cents), 0) AS cost_cents,
		       COALESCE(PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY u.latency_ms), 0) AS p50,
		       COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY u.latency_ms), 0) AS p95,
		       COUNT(*) FILTER (WHERE u.cached) AS cache_hits
		FROM usage_log u
		JOIN teams t ON t.id = u.team_id
		WHERE %s
		GROUP BY bucket
		ORDER BY bucket
	`, bucketTrunc, where)

	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("usage aggregate: %w", err)
	}
	defer rows.Close()

	out := []UsageBucket{}
	for rows.Next() {
		b := UsageBucket{}
		if err := rows.Scan(
			&b.Bucket, &b.Requests, &b.Errors,
			&b.PromptTokens, &b.CompletionTokens, &b.TotalTokens,
			&b.CostCents, &b.LatencyP50Ms, &b.LatencyP95Ms, &b.CacheHits,
		); err != nil {
			return nil, fmt.Errorf("scan usage bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetSpendTimeseries pivots usage_log into (bucket, alias) → cost so the
// frontend can render a stacked bar by alias. Aliases are sorted by total
// spend descending across the window so the legend has a stable order.
func (s *Store) GetSpendTimeseries(ctx context.Context, teamSlug string, userID *int64, from, to time.Time, bucket string) (*SpendTimeseries, error) {
	bucketTrunc, err := validateBucket(bucket)
	if err != nil {
		return nil, err
	}
	from, to = defaultRange(from, to, 30)

	args := []any{from, to}
	where := "u.ts >= $1 AND u.ts < $2"
	if teamSlug != "" {
		args = append(args, teamSlug)
		where += fmt.Sprintf(" AND t.slug = $%d", len(args))
	}
	if userID != nil {
		args = append(args, *userID)
		where += fmt.Sprintf(" AND u.user_id = $%d", len(args))
	}

	query := fmt.Sprintf(`
		SELECT date_trunc('%s', u.ts) AS bucket,
		       u.alias,
		       COUNT(*) AS requests,
		       COALESCE(SUM(u.prompt_tokens), 0) AS prompt_tokens,
		       COALESCE(SUM(u.completion_tokens), 0) AS completion_tokens,
		       COALESCE(SUM(u.total_tokens), 0) AS total_tokens,
		       COALESCE(SUM(u.cost_cents), 0) AS cost_cents
		FROM usage_log u
		JOIN teams t ON t.id = u.team_id
		WHERE %s
		GROUP BY bucket, u.alias
		ORDER BY bucket, u.alias
	`, bucketTrunc, where)

	rows, err := s.Pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("spend timeseries: %w", err)
	}
	defer rows.Close()

	type aliasRow struct {
		bucket    time.Time
		alias     string
		requests  int64
		prompt    int64
		complete  int64
		total     int64
		costCents int64
	}

	rawRows := []aliasRow{}
	for rows.Next() {
		r := aliasRow{}
		if err := rows.Scan(&r.bucket, &r.alias, &r.requests, &r.prompt, &r.complete, &r.total, &r.costCents); err != nil {
			return nil, fmt.Errorf("scan spend row: %w", err)
		}
		rawRows = append(rawRows, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Pivot: bucket -> {total, aliases{name -> cost}}
	bucketMap := map[time.Time]*SpendBucket{}
	bucketOrder := []time.Time{}
	aliasTotals := map[string]int64{}
	for _, r := range rawRows {
		entry, ok := bucketMap[r.bucket]
		if !ok {
			entry = &SpendBucket{Bucket: r.bucket, Aliases: map[string]int64{}}
			bucketMap[r.bucket] = entry
			bucketOrder = append(bucketOrder, r.bucket)
		}
		entry.Aliases[r.alias] = r.costCents
		entry.Total.Requests += r.requests
		entry.Total.PromptTokens += r.prompt
		entry.Total.CompletionTokens += r.complete
		entry.Total.TotalTokens += r.total
		entry.Total.CostCents += r.costCents
		aliasTotals[r.alias] += r.costCents
	}

	series := make([]SpendBucket, 0, len(bucketOrder))
	for _, b := range bucketOrder {
		series = append(series, *bucketMap[b])
	}

	aliases := make([]string, 0, len(aliasTotals))
	for a := range aliasTotals {
		aliases = append(aliases, a)
	}
	sort.SliceStable(aliases, func(i, j int) bool {
		// Highest total first; tie-break alphabetically so the legend is
		// deterministic across reloads.
		if aliasTotals[aliases[i]] != aliasTotals[aliases[j]] {
			return aliasTotals[aliases[i]] > aliasTotals[aliases[j]]
		}
		return aliases[i] < aliases[j]
	})

	return &SpendTimeseries{
		From:    from,
		To:      to,
		Bucket:  bucketTrunc,
		Series:  series,
		Aliases: aliases,
	}, nil
}
