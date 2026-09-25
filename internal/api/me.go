package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// MeHandler serves the user-facing /me/* endpoints. Every handler runs
// behind auth.Session, but does not require RequireAdmin: any authenticated
// user can see their own keys, usage, and budget.
//
// Master-key callers don't map to a real user row, so they're rejected
// with 400 — they should use /admin endpoints to inspect global state.
type MeHandler struct {
	Store  *store.Store
	Budget *budget.Service
	Logger *slog.Logger
}

func (h *MeHandler) requireUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil || p.User == nil {
		writeJSONError(w, http.StatusBadRequest, "no_user_session",
			"/me endpoints require a user session; master-key callers should use /admin")
		return nil, false
	}
	return p.User, true
}

type MyKeyResponse struct {
	ID            int64          `json:"id"`
	Prefix        string         `json:"prefix"`
	Name          string         `json:"name"`
	Metadata      map[string]any `json:"metadata"`
	AllowedModels []string       `json:"allowed_models"`
	RPM           *int           `json:"rpm,omitempty"`
	TPM           *int           `json:"tpm,omitempty"`
	ExpiresAt     *time.Time     `json:"expires_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	RevokedAt     *time.Time     `json:"revoked_at,omitempty"`
	PausedAt      *time.Time     `json:"paused_at,omitempty"`
	LastUsedAt    *time.Time     `json:"last_used_at,omitempty"`
}

func (h *MeHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	limit, offset := parsePagination(r)
	keys, total, err := h.Store.ListKeysForUser(r.Context(), user.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]MyKeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, MyKeyResponse{
			ID: k.ID, Prefix: k.KeyPrefix, Name: k.Name,
			Metadata: k.Metadata, AllowedModels: k.AllowedModels,
			RPM: k.ScopedRPM, TPM: k.ScopedTPM,
			ExpiresAt: k.ExpiresAt, CreatedAt: k.CreatedAt,
			RevokedAt: k.RevokedAt, PausedAt: k.DisabledAt, LastUsedAt: k.LastUsedAt,
		})
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type MyUsageRow struct {
	ID               int64     `json:"id"`
	TeamSlug         string    `json:"team_slug"`
	Alias            string    `json:"alias"`
	DeploymentName   string    `json:"deployment_name"`
	ModelUsed        string    `json:"model_used"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostCents        int64     `json:"cost_cents"`
	LatencyMs        int       `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	Error            string    `json:"error,omitempty"`
	Ts               time.Time `json:"ts"`
}

func (h *MeHandler) ListUsage(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListUsageForUser(r.Context(), user.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]MyUsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, MyUsageRow{
			ID: row.ID, TeamSlug: row.TeamSlug, Alias: row.Alias,
			DeploymentName: row.DeploymentName, ModelUsed: row.ModelUsed,
			PromptTokens: row.PromptTokens, CompletionTokens: row.CompletionTokens,
			TotalTokens: row.TotalTokens, CostCents: row.CostCents,
			LatencyMs: row.LatencyMs, StatusCode: row.StatusCode, Error: row.Error,
			Ts: row.Ts,
		})
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type MyBudgetResponse struct {
	LimitCents  *int64    `json:"limit_cents,omitempty"`
	Period      string    `json:"period"`
	WindowStart time.Time `json:"window_start,omitempty"`
	WindowEnd   time.Time `json:"window_end,omitempty"`
	UsedCents   int64     `json:"used_cents"`
}

func (h *MeHandler) GetBudget(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	period := user.Period
	if period == "" {
		period = "month"
	}
	if h.Budget == nil {
		writeJSON(w, http.StatusOK, MyBudgetResponse{Period: period})
		return
	}
	summary, err := h.Budget.UserSummary(r.Context(), user)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if summary == nil {
		writeJSON(w, http.StatusOK, MyBudgetResponse{Period: period})
		return
	}
	writeJSON(w, http.StatusOK, MyBudgetResponse{
		LimitCents:  summary.LimitCents,
		Period:      summary.Period,
		WindowStart: summary.WindowStart,
		WindowEnd:   summary.WindowEnd,
		UsedCents:   summary.UsedCents,
	})
}

// ─── Sessions ─────────────────────────────────────────────────────────

func (h *MeHandler) ListSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	currentHash := auth.SessionHashFromContext(r.Context())
	rows, err := h.Store.ListSessionsForUser(r.Context(), user.ID, currentHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// RevokeSession kills a single session by hex prefix. Server-side scope
// is the calling user; even a guessed prefix can only delete the
// caller's own row.
func (h *MeHandler) RevokeSession(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	prefix := chi.URLParam(r, "prefix")
	if err := h.Store.DeleteUserSessionByPrefix(r.Context(), user.ID, prefix); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "session not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RevokeOtherSessions logs the user out of every device except this one.
// Returns the count so the UI can show "signed out 3 sessions".
func (h *MeHandler) RevokeOtherSessions(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	currentHash := auth.SessionHashFromContext(r.Context())
	if len(currentHash) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "no current session to keep")
		return
	}
	n, err := h.Store.DeleteOtherSessionsForUser(r.Context(), user.ID, currentHash)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// ─── Password change ─────────────────────────────────────────────────

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// ChangePassword verifies the caller's current password and rotates it.
// Other sessions are kept alive by default; the UI calls
// RevokeOtherSessions explicitly when the user wants that.
func (h *MeHandler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	user, ok := h.requireUser(w, r)
	if !ok {
		return
	}
	var req ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"current_password and new_password are required")
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"new password must be at least 8 characters")
		return
	}
	hash, err := h.Store.GetUserPasswordHash(r.Context(), user.ID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.CheckPassword(hash, req.CurrentPassword) {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "current password is incorrect")
		return
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.Store.UpdatePasswordHash(r.Context(), user.ID, newHash); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	_ = h.Store.InsertAuditEvent(r.Context(), store.AuditEvent{
		ActorType:    "user",
		ActorID:      itoa(user.ID),
		Action:       "auth.password_change",
		ResourceType: "user",
		ResourceID:   itoa(user.ID),
	})
	w.WriteHeader(http.StatusNoContent)
}

func itoa(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
