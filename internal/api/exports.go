package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// ExportUsage streams the usage table as CSV. Honors the same filters as
// the Usage page (team filter only for now). Caps at 100k rows so a bad
// filter doesn't tie up a worker for hours.
func (h *AdminHandler) ExportUsage(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("team")
	maxRows := 100_000
	rows, _, err := h.Store.ListUsage(r.Context(), store.UsageFilter{TeamSlug: slug, Limit: maxRows})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="gatemux-usage-`+time.Now().Format("20060102")+`.csv"`)
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "ts", "team", "alias", "deployment", "model", "prompt_tokens", "completion_tokens", "total_tokens", "cost_cents", "latency_ms", "status_code", "error", "accounting_state"})
	for _, row := range rows {
		_ = cw.Write([]string{
			strconv.FormatInt(row.ID, 10),
			row.Ts.UTC().Format(time.RFC3339),
			row.TeamSlug,
			row.Alias,
			row.DeploymentName,
			row.ModelUsed,
			strconv.Itoa(row.PromptTokens),
			strconv.Itoa(row.CompletionTokens),
			strconv.Itoa(row.TotalTokens),
			strconv.FormatInt(row.CostCents, 10),
			strconv.Itoa(row.LatencyMs),
			strconv.Itoa(row.StatusCode),
			row.Error,
			row.Accounting,
		})
	}
}

// ExportAudit streams the audit log as CSV.
func (h *AdminHandler) ExportAudit(w http.ResponseWriter, r *http.Request) {
	maxRows := 100_000
	rows, _, err := h.Store.ListAuditEvents(r.Context(), maxRows, 0)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="gatemux-audit-`+time.Now().Format("20060102")+`.csv"`)
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"id", "ts", "actor_type", "actor_id", "action", "resource_type", "resource_id"})
	for _, e := range rows {
		_ = cw.Write([]string{
			strconv.FormatInt(e.ID, 10),
			e.CreatedAt.UTC().Format(time.RFC3339),
			e.ActorType,
			e.ActorID,
			e.Action,
			e.ResourceType,
			e.ResourceID,
		})
	}
}

// ProjectionResponse is the spend-projection tile. on_track is "above",
// "below", or "on" the linear pace.
type ProjectionResponse struct {
	ScopeType       string  `json:"scope_type"`
	ScopeID         int64   `json:"scope_id"`
	PeriodStart     string  `json:"period_start"`
	PeriodEnd       string  `json:"period_end"`
	SpendSoFarCents int64   `json:"spend_so_far_cents"`
	ProjectedCents  int64   `json:"projected_cents"`
	LimitCents      *int64  `json:"limit_cents,omitempty"`
	DaysToLimit     float64 `json:"days_to_limit,omitempty"`
	OnTrack         string  `json:"on_track"`
	NeedsMoreData   bool    `json:"needs_more_data"`
}

// GetProjection returns a forward-looking spend projection. Uses the
// same period semantics as budgets (day/week/month windows).
func (h *AdminHandler) GetProjection(w http.ResponseWriter, r *http.Request) {
	scopeType := r.URL.Query().Get("scope_type")
	if scopeType == "" {
		scopeType = "team"
	}
	scopeIDStr := r.URL.Query().Get("scope_id")
	scopeID, _ := strconv.ParseInt(scopeIDStr, 10, 64)

	// Phase 1 supports team-scoped projection only. Other scopes return
	// an empty projection rather than an error so the UI can render
	// uniformly.
	if scopeType != "team" || scopeID == 0 {
		writeJSON(w, http.StatusOK, ProjectionResponse{ScopeType: scopeType, ScopeID: scopeID, NeedsMoreData: true, OnTrack: "unknown"})
		return
	}
	team, err := h.Store.GetTeamByID(r.Context(), scopeID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	period := team.Period
	if period == "" {
		period = "month"
	}
	start, end := windowFor(period, time.Now().UTC())
	spend, err := h.Store.SumTeamSpendInWindow(r.Context(), team.ID, start, end)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	resp := ProjectionResponse{
		ScopeType:       "team",
		ScopeID:         team.ID,
		PeriodStart:     start.Format(time.RFC3339),
		PeriodEnd:       end.Format(time.RFC3339),
		SpendSoFarCents: spend,
		LimitCents:      team.UsdLimitCents,
		OnTrack:         "unknown",
	}
	now := time.Now().UTC()
	totalSec := end.Sub(start).Seconds()
	elapsedSec := now.Sub(start).Seconds()
	if totalSec <= 0 || elapsedSec <= 0 {
		resp.NeedsMoreData = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	frac := elapsedSec / totalSec
	if frac < 0.05 {
		resp.NeedsMoreData = true
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.ProjectedCents = int64(float64(spend) / frac)
	if team.UsdLimitCents != nil && *team.UsdLimitCents > 0 {
		dailyBurn := float64(spend) / (elapsedSec / 86400)
		if dailyBurn > 0 {
			resp.DaysToLimit = (float64(*team.UsdLimitCents - spend)) / dailyBurn
		}
		switch {
		case resp.ProjectedCents > *team.UsdLimitCents:
			resp.OnTrack = "above"
		case resp.ProjectedCents < int64(0.9*float64(*team.UsdLimitCents)):
			resp.OnTrack = "below"
		default:
			resp.OnTrack = "on"
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func windowFor(period string, now time.Time) (time.Time, time.Time) {
	switch period {
	case "day":
		start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		return start, start.Add(24 * time.Hour)
	case "week":
		offset := int(now.Weekday())
		start := time.Date(now.Year(), now.Month(), now.Day()-offset, 0, 0, 0, 0, time.UTC)
		return start, start.Add(7 * 24 * time.Hour)
	default: // "month"
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, 0)
		return start, end
	}
}

// _ keeps fmt imported when the projection logic is light.
var _ = fmt.Sprint
