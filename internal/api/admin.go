package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/callbacks"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
)

type AdminHandler struct {
	Store     *store.Store
	Config    *config.Config
	Version   string
	StartedAt time.Time
	Inflight  *atomic.Int64
	Registry  *router.Registry
	Logger    *slog.Logger
	Callbacks *callbacks.Bus
	V1        *V1Handler
}

// InflightMiddleware tracks the number of requests currently in flight so
// the Settings → Runtime card can render it. Counter survives panics
// because Recoverer runs above it.
func (h *AdminHandler) InflightMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.Inflight != nil {
			h.Inflight.Add(1)
			defer h.Inflight.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}

// ListCallbackStats returns the current health of every configured
// callback. Used by the Settings → Integrations panel.
func (h *AdminHandler) ListCallbackStats(w http.ResponseWriter, r *http.Request) {
	if h.Callbacks == nil {
		writeJSON(w, http.StatusOK, []callbacks.Stat{})
		return
	}
	writeJSON(w, http.StatusOK, h.Callbacks.Stats())
}

func (h *AdminHandler) refreshRegistry(r *http.Request) {
	if h.Registry == nil {
		return
	}
	_ = h.Registry.Refresh(r.Context())
}

func (h *AdminHandler) audit(r *http.Request, action, resourceType, resourceID string, metadata map[string]any) {
	if h.Store == nil {
		return
	}
	actorType := "system"
	actorID := "unknown"
	if p := auth.PrincipalFromContext(r.Context()); p != nil {
		switch {
		case p.IsMasterKey:
			actorType = "master_key"
			actorID = "master"
		case p.User != nil:
			actorType = "user"
			actorID = strconv.FormatInt(p.User.ID, 10)
		}
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	if err := h.Store.InsertAuditEvent(r.Context(), store.AuditEvent{
		ActorType:    actorType,
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Metadata:     metadata,
	}); err != nil && h.Logger != nil {
		h.Logger.Error("audit log write failed", "action", action, "resource_type", resourceType, "resource_id", resourceID, "err", err)
	}
}

type CreateTeamRequest struct {
	Slug                string `json:"slug"`
	Name                string `json:"name"`
	UsdLimit            *int64 `json:"usd_limit"`
	Period              string `json:"period"`
	RPM                 *int   `json:"rpm"`
	TPM                 *int   `json:"tpm"`
	MaxParallelRequests *int   `json:"max_parallel_requests"`
}

type TeamResponse struct {
	ID                   int64              `json:"id"`
	Slug                 string             `json:"slug"`
	Name                 string             `json:"name"`
	UsdLimitCents        *int64             `json:"usd_limit_cents,omitempty"`
	Period               string             `json:"period"`
	RPM                  *int               `json:"rpm,omitempty"`
	TPM                  *int               `json:"tpm,omitempty"`
	MaxParallelRequests  *int               `json:"max_parallel_requests,omitempty"`
	AllowedModels        []string           `json:"allowed_models,omitempty"`
	CapturePayloads      bool               `json:"capture_payloads"`
	CustomerRegistration string             `json:"customer_registration"`
	Stats                *TeamStatsResponse `json:"stats,omitempty"`
}

// TeamStatsResponse is included when the directory asks for ?stats=1.
type TeamStatsResponse struct {
	ActiveKeys       int64 `json:"active_keys"`
	Members          int64 `json:"members"`
	PeriodSpendCents int64 `json:"period_spend_cents"`
}

func teamToResponse(t *store.Team) TeamResponse {
	return TeamResponse{
		ID: t.ID, Slug: t.Slug, Name: t.Name,
		UsdLimitCents:        t.UsdLimitCents,
		Period:               t.Period,
		RPM:                  t.RPM,
		TPM:                  t.TPM,
		AllowedModels:        t.AllowedModels,
		MaxParallelRequests:  t.MaxParallelRequests,
		CapturePayloads:      t.CapturePayloads,
		CustomerRegistration: t.CustomerRegistration,
	}
}

func (h *AdminHandler) ListTeams(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	limit, offset = store.NormalizePage(limit, offset)
	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		// Scope before pagination: unrelated newer teams must not hide the
		// manager's team or influence the reported total.
		out := []TeamResponse{}
		var total int64
		if p.IsManager() && p.TeamSlug() != "" {
			t, err := h.Store.GetTeamBySlug(r.Context(), p.TeamSlug())
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team")
				return
			}
			if err == nil && t.ArchivedAt == nil {
				total = 1
				if offset == 0 {
					out = append(out, teamToResponse(t))
				}
			}
		}
		setTotalCount(w, total)
		writeJSON(w, http.StatusOK, out)
		return
	}
	teams, total, err := h.Store.ListTeams(r.Context(), limit, offset, r.URL.Query().Get("q"))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]TeamResponse, 0, len(teams))
	for _, t := range teams {
		out = append(out, teamToResponse(t))
	}
	if r.URL.Query().Get("stats") == "1" && len(teams) > 0 {
		ids := make([]int64, len(teams))
		for i, t := range teams {
			ids[i] = t.ID
		}
		stats, err := h.Store.TeamListStats(r.Context(), ids)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team stats")
			return
		}
		for i := range out {
			st := stats[out[i].ID]
			out[i].Stats = &TeamStatsResponse{ActiveKeys: st.ActiveKeys, Members: st.Members, PeriodSpendCents: st.PeriodSpendCents}
		}
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) GetTeam(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	t, err := h.Store.GetTeamBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, teamToResponse(t))
}

func (h *AdminHandler) CreateTeam(w http.ResponseWriter, r *http.Request) {
	var req CreateTeamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Slug == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "slug is required")
		return
	}
	if req.Name == "" {
		req.Name = req.Slug
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	var usdCents *int64
	if req.UsdLimit != nil {
		c := *req.UsdLimit * 100
		usdCents = &c
	}
	team, err := h.Store.CreateTeam(r.Context(), req.Slug, req.Name, usdCents, req.Period, req.RPM, req.TPM, maxParallelRequests)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.create", "team", team.Slug, map[string]any{
		"name":                  team.Name,
		"usd_limit_cents":       team.UsdLimitCents,
		"period":                team.Period,
		"rpm":                   team.RPM,
		"tpm":                   team.TPM,
		"max_parallel_requests": team.MaxParallelRequests,
	})
	writeJSON(w, http.StatusCreated, teamToResponse(team))
}

type UpdateTeamConcurrencyRequest struct {
	MaxParallelRequests *int `json:"max_parallel_requests"`
}

// UpdateTeamConcurrency changes the distributed team-wide in-flight cap. A
// null or zero value clears the cap. Existing leases are left to complete; the
// new value applies atomically to each subsequent admission decision.
func (h *AdminHandler) UpdateTeamConcurrency(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req UpdateTeamConcurrencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	team, err := h.Store.UpdateTeamConcurrency(r.Context(), slug, maxParallelRequests)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.concurrency.update", "team", team.Slug, map[string]any{
		"max_parallel_requests": team.MaxParallelRequests,
	})
	writeJSON(w, http.StatusOK, teamToResponse(team))
}

type UpdateTeamRatesRequest struct {
	RPM *int `json:"rpm"`
	TPM *int `json:"tpm"`
}

// UpdateTeamRates changes the team-wide RPM/TPM caps. A null or zero value
// clears that cap. The new values apply to each subsequent admission decision.
func (h *AdminHandler) UpdateTeamRates(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req UpdateTeamRatesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	rpm, ok := positiveLimit(w, req.RPM, "rpm")
	if !ok {
		return
	}
	tpm, ok := positiveLimit(w, req.TPM, "tpm")
	if !ok {
		return
	}
	team, err := h.Store.UpdateTeamRates(r.Context(), slug, rpm, tpm)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.rates.update", "team", team.Slug, map[string]any{
		"rpm": team.RPM,
		"tpm": team.TPM,
	})
	writeJSON(w, http.StatusOK, teamToResponse(team))
}

type UpdateTeamAllowedModelsRequest struct {
	AllowedModels []string `json:"allowed_models"`
}

// UpdateTeamAllowedModels replaces the team's model allowlist. ["*"] allows
// every alias; key-level restrictions can only narrow this list further.
func (h *AdminHandler) UpdateTeamAllowedModels(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req UpdateTeamAllowedModelsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.AllowedModels) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "allowed_models must not be empty; use [\"*\"] to allow every model")
		return
	}
	if len(req.AllowedModels) > 200 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "allowed_models is limited to 200 entries")
		return
	}
	team, err := h.Store.UpdateTeamAllowedModels(r.Context(), slug, req.AllowedModels)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.models.update", "team", team.Slug, map[string]any{
		"allowed_models": team.AllowedModels,
	})
	writeJSON(w, http.StatusOK, teamToResponse(team))
}

func positiveLimit(w http.ResponseWriter, value *int, field string) (*int, bool) {
	if value == nil || *value == 0 {
		return nil, true
	}
	if *value < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", field+" must be zero or greater")
		return nil, false
	}
	return value, true
}

type CreateKeyRequest struct {
	Name                string         `json:"name"`
	UserID              *int64         `json:"user_id"`
	Metadata            map[string]any `json:"metadata"`
	AllowedModels       []string       `json:"allowed_models"`
	RPM                 *int           `json:"rpm"`
	TPM                 *int           `json:"tpm"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
	UsdLimitCents       *int64         `json:"usd_limit_cents"`
	ExpiresAt           *time.Time     `json:"expires_at"`
}

type CreateKeyResponse struct {
	Key      string `json:"key"`
	Prefix   string `json:"prefix"`
	Name     string `json:"name"`
	TeamSlug string `json:"team_slug"`
}

type KeyResponse struct {
	ID                  int64          `json:"id"`
	Prefix              string         `json:"prefix"`
	Name                string         `json:"name"`
	UserID              *int64         `json:"user_id,omitempty"`
	ServiceAccountID    *int64         `json:"service_account_id,omitempty"`
	ServiceAccountName  string         `json:"service_account_name,omitempty"`
	OwnerUserEmail      string         `json:"owner_user_email,omitempty"`
	OwnerUserName       string         `json:"owner_user_name,omitempty"`
	Metadata            map[string]any `json:"metadata"`
	AllowedModels       []string       `json:"allowed_models"`
	RPM                 *int           `json:"rpm,omitempty"`
	TPM                 *int           `json:"tpm,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	UsdLimitCents       *int64         `json:"usd_limit_cents,omitempty"`
	ExpiresAt           *time.Time     `json:"expires_at,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	RevokedAt           *time.Time     `json:"revoked_at,omitempty"`
	PausedAt            *time.Time     `json:"paused_at,omitempty"`
	LastUsedAt          *time.Time     `json:"last_used_at,omitempty"`
	RotatedFromKeyID    *int64         `json:"rotated_from_key_id,omitempty"`
}

func keyToResponse(k *store.VirtualKey) KeyResponse {
	return KeyResponse{
		ID:                  k.ID,
		Prefix:              k.KeyPrefix,
		Name:                k.Name,
		UserID:              k.UserID,
		ServiceAccountID:    k.ServiceAccountID,
		ServiceAccountName:  k.ServiceAccountName,
		OwnerUserEmail:      k.OwnerUserEmail,
		OwnerUserName:       k.OwnerUserName,
		Metadata:            k.Metadata,
		AllowedModels:       k.AllowedModels,
		RPM:                 k.ScopedRPM,
		TPM:                 k.ScopedTPM,
		MaxParallelRequests: k.MaxParallelRequests,
		UsdLimitCents:       k.ScopedUsdLimitCents,
		ExpiresAt:           k.ExpiresAt,
		CreatedAt:           k.CreatedAt,
		RevokedAt:           k.RevokedAt,
		PausedAt:            k.DisabledAt,
		LastUsedAt:          k.LastUsedAt,
		RotatedFromKeyID:    k.RotatedFromKeyID,
	}
}

func (h *AdminHandler) ListKeys(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	team, err := h.Store.GetTeamBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	limit, offset := parsePagination(r)
	keys, total, err := h.Store.ListKeysForTeam(r.Context(), team.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]KeyResponse, 0, len(keys))
	for _, k := range keys {
		out = append(out, keyToResponse(k))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) CreateKey(w http.ResponseWriter, r *http.Request) {
	teamSlug := chi.URLParam(r, "slug")
	team, err := h.Store.GetTeamBySlug(r.Context(), teamSlug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := store.ValidateKeyBudget(req.UsdLimitCents); err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	if req.RPM != nil && *req.RPM < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "rpm must be zero or greater")
		return
	}
	if req.TPM != nil && *req.TPM < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "tpm must be zero or greater")
		return
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now()) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "expires_at must be in the future")
		return
	}
	if req.UserID != nil {
		owner, err := h.Store.GetUserByID(r.Context(), *req.UserID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if owner.TeamID == nil || *owner.TeamID != team.ID {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id must belong to the selected team")
			return
		}
	}

	rawKey, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if _, err := h.Store.CreateVirtualKey(r.Context(), store.CreateVirtualKeyParams{
		TeamID:              team.ID,
		UserID:              req.UserID,
		KeyHash:             hash,
		Prefix:              prefix,
		Name:                req.Name,
		Metadata:            req.Metadata,
		AllowedModels:       req.AllowedModels,
		ScopedRPM:           req.RPM,
		ScopedTPM:           req.TPM,
		MaxParallelRequests: maxParallelRequests,
		ScopedUsdLimitCents: req.UsdLimitCents,
		ExpiresAt:           req.ExpiresAt,
	}); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "key.create", "virtual_key", prefix, map[string]any{
		"usd_limit_cents":       req.UsdLimitCents,
		"team_slug":             team.Slug,
		"user_id":               req.UserID,
		"name":                  req.Name,
		"allowed_models":        req.AllowedModels,
		"metadata":              req.Metadata,
		"rpm":                   req.RPM,
		"tpm":                   req.TPM,
		"max_parallel_requests": maxParallelRequests,
		"expires_at":            req.ExpiresAt,
	})
	writeJSON(w, http.StatusCreated, CreateKeyResponse{
		Key: rawKey, Prefix: prefix, Name: req.Name, TeamSlug: team.Slug,
	})
}

type UpdateKeyRequest struct {
	Name                string         `json:"name"`
	Metadata            map[string]any `json:"metadata"`
	AllowedModels       []string       `json:"allowed_models"`
	RPM                 *int           `json:"rpm"`
	TPM                 *int           `json:"tpm"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
	UsdLimitCents       *int64         `json:"usd_limit_cents"`
	ExpiresAt           *time.Time     `json:"expires_at"`
}

// UpdateKey replaces the editable fields on a virtual key. The secret
// material, team, and owner remain pinned — rotate or revoke for those.
func (h *AdminHandler) UpdateKey(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key id must be an integer")
		return
	}
	current, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(team.Slug) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "no access to team "+team.Slug)
		return
	}
	if current.RevokedAt != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "cannot edit a revoked key")
		return
	}

	var req UpdateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := store.ValidateKeyBudget(req.UsdLimitCents); err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	if req.RPM != nil && *req.RPM < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "rpm must be zero or greater")
		return
	}
	if req.TPM != nil && *req.TPM < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "tpm must be zero or greater")
		return
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	if req.ExpiresAt != nil && req.ExpiresAt.Before(time.Now()) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "expires_at must be in the future")
		return
	}

	updated, err := h.Store.UpdateVirtualKey(r.Context(), id, store.UpdateVirtualKeyParams{
		Name:                req.Name,
		Metadata:            req.Metadata,
		AllowedModels:       req.AllowedModels,
		ScopedRPM:           req.RPM,
		ScopedTPM:           req.TPM,
		MaxParallelRequests: maxParallelRequests,
		ScopedUsdLimitCents: req.UsdLimitCents,
		ExpiresAt:           req.ExpiresAt,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found or revoked")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	updated.OwnerUserEmail = current.OwnerUserEmail
	updated.OwnerUserName = current.OwnerUserName
	updated.ServiceAccountID = current.ServiceAccountID
	updated.ServiceAccountName = current.ServiceAccountName
	h.audit(r, "key.update", "virtual_key", strconv.FormatInt(id, 10), map[string]any{
		"usd_limit_cents":       req.UsdLimitCents,
		"team_slug":             team.Slug,
		"name":                  req.Name,
		"allowed_models":        req.AllowedModels,
		"metadata":              req.Metadata,
		"rpm":                   req.RPM,
		"tpm":                   req.TPM,
		"max_parallel_requests": maxParallelRequests,
		"expires_at":            req.ExpiresAt,
	})
	writeJSON(w, http.StatusOK, keyToResponse(updated))
}

func (h *AdminHandler) RotateKey(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key id must be an integer")
		return
	}

	current, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(team.Slug) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "no access to team "+team.Slug)
		return
	}
	if current.RevokedAt != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key already revoked")
		return
	}
	if current.DisabledAt != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key is paused; resume it before rotating")
		return
	}
	if current.ExpiresAt != nil && current.ExpiresAt.Before(time.Now()) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "cannot rotate an expired key")
		return
	}

	// Optional body {"grace_seconds": N}: keep the old key working for up
	// to 7 days while clients move to the new one.
	var opts struct {
		GraceSeconds int64 `json:"grace_seconds"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&opts); err != nil && !errors.Is(err, io.EOF) {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "body must be {\"grace_seconds\": N}")
			return
		}
	}
	if opts.GraceSeconds < 0 || opts.GraceSeconds > 7*24*3600 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "grace_seconds must be between 0 and 604800 (7 days)")
		return
	}
	grace := time.Duration(opts.GraceSeconds) * time.Second

	rawKey, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	rotated, err := h.Store.RotateVirtualKey(r.Context(), id, team.ID, hash, prefix, grace)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.audit(r, "key.rotate", "virtual_key", strconv.FormatInt(id, 10), map[string]any{
		"team_slug":     team.Slug,
		"new_prefix":    rotated.KeyPrefix,
		"grace_seconds": opts.GraceSeconds,
	})
	writeJSON(w, http.StatusCreated, CreateKeyResponse{
		Key: rawKey, Prefix: prefix, Name: rotated.Name, TeamSlug: team.Slug,
	})
}

// PauseKey and ResumeKey stop and restart a key without revoking it: its
// identity, limits and history stay, and clients get 401 while paused.
func (h *AdminHandler) PauseKey(w http.ResponseWriter, r *http.Request)  { h.setKeyPaused(w, r, true) }
func (h *AdminHandler) ResumeKey(w http.ResponseWriter, r *http.Request) { h.setKeyPaused(w, r, false) }

func (h *AdminHandler) setKeyPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key id must be an integer")
		return
	}
	_, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(team.Slug) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "no access to team "+team.Slug)
		return
	}
	if err := h.Store.SetKeyPaused(r.Context(), id, paused); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found or revoked")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	action := "key.resume"
	if paused {
		action = "key.pause"
	}
	h.audit(r, action, "virtual_key", strconv.FormatInt(id, 10), map[string]any{"team_slug": team.Slug})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) RevokeKey(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "key id must be an integer")
		return
	}
	// Look up the key's team first so managers can only revoke keys in
	// their own team. GetVirtualKeyByID returns ErrNotFound for already-
	// deleted rows; in that case we return 404 the same as the post-revoke
	// check below.
	_, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(team.Slug) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "no access to team "+team.Slug)
		return
	}
	if err := h.Store.RevokeKey(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found or already revoked")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "key.revoke", "virtual_key", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

type UsageRowResponse struct {
	Accounting         string          `json:"accounting_state"`
	ID                 int64           `json:"id"`
	TeamSlug           string          `json:"team_slug"`
	TeamName           string          `json:"team_name,omitempty"`
	Alias              string          `json:"alias"`
	DeploymentName     string          `json:"deployment_name"`
	ProviderType       string          `json:"provider_type,omitempty"`
	Strategy           string          `json:"strategy,omitempty"`
	ModelUsed          string          `json:"model_used"`
	PromptTokens       int             `json:"prompt_tokens"`
	CompletionTokens   int             `json:"completion_tokens"`
	TotalTokens        int             `json:"total_tokens"`
	CostCents          int64           `json:"cost_cents"`
	LatencyMs          int             `json:"latency_ms"`
	QueueMs            *int            `json:"queue_ms,omitempty"`
	UpstreamMs         *int            `json:"upstream_ms,omitempty"`
	TTFBMs             *int            `json:"ttfb_ms,omitempty"`
	PostprocessMs      *int            `json:"postprocess_ms,omitempty"`
	StatusCode         int             `json:"status_code"`
	Error              string          `json:"error,omitempty"`
	Ts                 time.Time       `json:"ts"`
	RequestID          string          `json:"request_id,omitempty"`
	UserID             *int64          `json:"user_id,omitempty"`
	UserEmail          string          `json:"user_email,omitempty"`
	KeyID              *int64          `json:"key_id,omitempty"`
	KeyPrefix          string          `json:"key_prefix,omitempty"`
	KeyName            string          `json:"key_name,omitempty"`
	CustomerExternalID string          `json:"customer_external_id,omitempty"`
	RequestTags        []string        `json:"request_tags,omitempty"`
	Cached             bool            `json:"cached"`
	ClientIP           string          `json:"client_ip,omitempty"`
	HasPayload         bool            `json:"has_payload"`
	TokenDetails       json.RawMessage `json:"token_details,omitempty"`
	// Fallback is true when a deployment other than the alias's primary
	// served the request. GuardrailDecision is the strongest decision
	// recorded for it (block, error, unsupported, redact, flag) or empty.
	Fallback          bool   `json:"fallback"`
	GuardrailDecision string `json:"guardrail_decision,omitempty"`
}

func usageRowToResponse(r *store.UsageRow) UsageRowResponse {
	return UsageRowResponse{
		Accounting: r.Accounting,
		ID:         r.ID, TeamSlug: r.TeamSlug, TeamName: r.TeamName,
		Alias:          r.Alias,
		DeploymentName: r.DeploymentName,
		ProviderType:   r.ProviderType,
		Strategy:       r.Strategy,
		ModelUsed:      r.ModelUsed,
		PromptTokens:   r.PromptTokens, CompletionTokens: r.CompletionTokens,
		TotalTokens: r.TotalTokens, CostCents: r.CostCents,
		LatencyMs: r.LatencyMs,
		QueueMs:   r.QueueMs, UpstreamMs: r.UpstreamMs,
		TTFBMs: r.TTFBMs, PostprocessMs: r.PostprocessMs,
		StatusCode: r.StatusCode, Error: r.Error, Ts: r.Ts,
		RequestID: r.RequestID,
		UserID:    r.UserID, UserEmail: r.UserEmail,
		KeyID: r.KeyID, KeyPrefix: r.KeyPrefix, KeyName: r.KeyName,
		CustomerExternalID: r.CustomerExternalID,
		RequestTags:        r.RequestTags,
		Cached:             r.Cached,
		ClientIP:           r.ClientIP,
		HasPayload:         r.HasPayload,
		TokenDetails:       r.TokenDetails,
		Fallback:           r.PrimaryDeployment != "" && r.DeploymentName != "" && r.DeploymentName != r.PrimaryDeployment,
		GuardrailDecision:  r.GuardrailDecision,
	}
}

type DeploymentInfo struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	UpstreamModel string `json:"upstream_model"`
	BaseURL       string `json:"base_url,omitempty"`
}

type AliasInfo struct {
	Alias       string   `json:"alias"`
	Deployments []string `json:"deployments"`
}

type CountsResponse struct {
	Teams          int `json:"teams"`
	Users          int `json:"users"`
	ActiveKeys     int `json:"active_keys"`
	PendingInvites int `json:"pending_invites"`
}

type InfoResponse struct {
	Version          string           `json:"version"`
	Deployments      []DeploymentInfo `json:"deployments"`
	Aliases          []AliasInfo      `json:"aliases"`
	Counts           CountsResponse   `json:"counts"`
	StartedAt        time.Time        `json:"started_at"`
	UptimeSeconds    int64            `json:"uptime_seconds"`
	Inflight         int64            `json:"inflight"`
	ListenAddr       string           `json:"listen_addr,omitempty"`
	DBOK             bool             `json:"db_ok"`
	RedisOK          bool             `json:"redis_ok"`
	MasterKeySuffix  string           `json:"master_key_suffix,omitempty"`
	LastConfigChange *AuditEventBrief `json:"last_config_change,omitempty"`
}

type AuditEventBrief struct {
	When         time.Time `json:"when"`
	ActorID      string    `json:"actor_id"`
	ActorType    string    `json:"actor_type"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
}

func (h *AdminHandler) GetInfo(w http.ResponseWriter, r *http.Request) {
	storeDeps, err := h.Store.ListDeployments(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	deps := make([]DeploymentInfo, 0, len(storeDeps))
	for _, d := range storeDeps {
		var baseURL string
		if d.BaseURL != nil {
			baseURL = *d.BaseURL
		}
		deps = append(deps, DeploymentInfo{
			Name: d.Name, Type: d.ProviderType, UpstreamModel: d.UpstreamModel, BaseURL: baseURL,
		})
	}
	storeAliases, err := h.Store.ListAliasesWithDeployments(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	aliases := make([]AliasInfo, 0, len(storeAliases))
	for _, a := range storeAliases {
		aliases = append(aliases, AliasInfo{Alias: a.Alias, Deployments: a.Deployments})
	}
	counts, err := h.Store.GetCounts(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	resp := InfoResponse{
		Version:     h.Version,
		Deployments: deps,
		Aliases:     aliases,
		Counts: CountsResponse{
			Teams: counts.Teams, Users: counts.Users,
			ActiveKeys: counts.ActiveKeys, PendingInvites: counts.PendingInvites,
		},
	}
	if !h.StartedAt.IsZero() {
		resp.StartedAt = h.StartedAt
		resp.UptimeSeconds = int64(time.Since(h.StartedAt).Seconds())
	}
	if h.Inflight != nil {
		resp.Inflight = h.Inflight.Load()
	}
	if h.Config != nil {
		resp.ListenAddr = h.Config.Server.Addr
		if mk := config.Env(h.Config.Admin.MasterKeyEnv); mk != "" {
			resp.MasterKeySuffix = lastNRunes(mk, 4)
		}
	}
	resp.DBOK = h.Store != nil && h.Store.Pool != nil && h.Store.Pool.Ping(r.Context()) == nil
	resp.RedisOK = h.Callbacks != nil // proxy: callbacks bus comes online with redis; refine when we expose redis ping
	if last, err := h.Store.LatestConfigChange(r.Context()); err == nil && last != nil {
		resp.LastConfigChange = &AuditEventBrief{
			When:         last.CreatedAt,
			ActorID:      last.ActorID,
			ActorType:    last.ActorType,
			Action:       last.Action,
			ResourceType: last.ResourceType,
			ResourceID:   last.ResourceID,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func lastNRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

type UserResponse struct {
	ID                  int64      `json:"id"`
	Email               string     `json:"email"`
	Name                string     `json:"name"`
	TeamID              *int64     `json:"team_id,omitempty"`
	TeamSlug            string     `json:"team_slug,omitempty"`
	Role                string     `json:"role"`
	IsAdmin             bool       `json:"is_admin"`
	UsdLimitCents       *int64     `json:"usd_limit_cents,omitempty"`
	MaxParallelRequests *int       `json:"max_parallel_requests,omitempty"`
	Period              string     `json:"period"`
	LastLoginAt         *time.Time `json:"last_login_at,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	DisabledAt          *time.Time `json:"disabled_at,omitempty"`
	OIDCSub             string     `json:"oidc_sub,omitempty"`
	RoleManagedByOIDC   bool       `json:"role_managed_by_oidc"`
}

func (h *AdminHandler) ListUsers(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	role := r.URL.Query().Get("role")
	if role != "" && role != store.RoleAdmin && role != store.RoleManager && role != store.RoleMember {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role must be admin, manager or member")
		return
	}
	rows, total, err := h.Store.ListUsers(r.Context(), limit, offset, r.URL.Query().Get("q"), role)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]UserResponse, 0, len(rows))
	for _, u := range rows {
		ur := UserResponse{
			ID:                  u.ID,
			Email:               u.Email,
			Name:                u.Name,
			TeamID:              u.TeamID,
			Role:                u.Role,
			IsAdmin:             u.IsAdmin,
			UsdLimitCents:       u.UsdLimitCents,
			MaxParallelRequests: u.MaxParallelRequests,
			Period:              u.Period,
			LastLoginAt:         u.LastLoginAt,
			CreatedAt:           u.CreatedAt,
			DisabledAt:          u.DisabledAt,
			OIDCSub:             u.OIDCSub,
			RoleManagedByOIDC:   u.RoleManagedByOIDC,
		}
		if u.TeamSlug != "" {
			ur.TeamSlug = u.TeamSlug
		}
		out = append(out, ur)
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type UpdateBudgetRequest struct {
	LimitCents *int64 `json:"limit_cents"`
	Period     string `json:"period"`
}

func normalizedBudgetLimit(limit *int64) (*int64, error) {
	if limit == nil {
		return nil, nil
	}
	if *limit < 0 {
		return nil, errors.New("limit_cents must be zero or greater")
	}
	if *limit == 0 {
		return nil, nil
	}
	return limit, nil
}

func (h *AdminHandler) UpdateTeamBudget(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req UpdateBudgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	limit, err := normalizedBudgetLimit(req.LimitCents)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	team, err := h.Store.UpdateTeamBudget(r.Context(), slug, limit, req.Period)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.budget.update", "team", team.Slug, map[string]any{
		"limit_cents": team.UsdLimitCents,
		"period":      team.Period,
	})
	writeJSON(w, http.StatusOK, teamToResponse(team))
}

func (h *AdminHandler) UpdateUserBudget(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user id must be an integer")
		return
	}
	var req UpdateBudgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	limit, err := normalizedBudgetLimit(req.LimitCents)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	user, err := h.Store.UpdateUserBudget(r.Context(), id, limit, req.Period)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "user.budget.update", "user", strconv.FormatInt(user.ID, 10), map[string]any{
		"limit_cents": user.UsdLimitCents,
		"period":      user.Period,
	})
	writeJSON(w, http.StatusOK, UserResponse{
		ID:                  user.ID,
		Email:               user.Email,
		Name:                user.Name,
		TeamID:              user.TeamID,
		Role:                user.Role,
		IsAdmin:             user.IsAdmin,
		UsdLimitCents:       user.UsdLimitCents,
		MaxParallelRequests: user.MaxParallelRequests,
		Period:              user.Period,
		LastLoginAt:         user.LastLoginAt,
		CreatedAt:           user.CreatedAt,
		DisabledAt:          user.DisabledAt,
		OIDCSub:             user.OIDCSub,
		RoleManagedByOIDC:   user.RoleManagedByOIDC,
	})
}

func (h *AdminHandler) UpdateUserConcurrency(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "user id must be an integer")
		return
	}
	var req UpdateTeamConcurrencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	user, err := h.Store.UpdateUserConcurrency(r.Context(), id, maxParallelRequests)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "user.concurrency.update", "user", strconv.FormatInt(user.ID, 10), map[string]any{
		"max_parallel_requests": user.MaxParallelRequests,
	})
	writeJSON(w, http.StatusOK, UserResponse{
		ID: user.ID, Email: user.Email, Name: user.Name, TeamID: user.TeamID,
		TeamSlug: user.TeamSlug, Role: user.Role, IsAdmin: user.IsAdmin,
		UsdLimitCents: user.UsdLimitCents, MaxParallelRequests: user.MaxParallelRequests,
		Period: user.Period, LastLoginAt: user.LastLoginAt, CreatedAt: user.CreatedAt,
		DisabledAt: user.DisabledAt, OIDCSub: user.OIDCSub, RoleManagedByOIDC: user.RoleManagedByOIDC,
	})
}

type InviteResponse struct {
	ID         int64      `json:"id"`
	Prefix     string     `json:"prefix"`
	Email      string     `json:"email,omitempty"`
	TeamID     *int64     `json:"team_id,omitempty"`
	TeamSlug   string     `json:"team_slug,omitempty"`
	Role       string     `json:"role"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

type CreateInviteRequest struct {
	Email          string `json:"email"`
	TeamSlug       string `json:"team_slug"`
	Role           string `json:"role"`
	ExpiresInHours int    `json:"expires_in_hours"`
}

type CreateInviteResponseBody struct {
	InviteResponse
	Token string `json:"token"`
	URL   string `json:"url"`
}

func inviteToResponse(r *store.InviteListRow) InviteResponse {
	out := InviteResponse{
		ID: r.ID, Prefix: r.TokenPrefix, Email: r.Email,
		TeamID: r.TeamID, Role: r.Role, ExpiresAt: r.ExpiresAt,
		AcceptedAt: r.AcceptedAt, CreatedAt: r.CreatedAt,
	}
	if r.TeamSlug != nil {
		out.TeamSlug = *r.TeamSlug
	}
	return out
}

func (h *AdminHandler) ListInvites(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	p := auth.PrincipalFromContext(r.Context())
	var teamSlug *string
	if !p.IsAdmin() {
		slug := p.TeamSlug()
		teamSlug = &slug
	}
	rows, total, err := h.Store.ListInvites(r.Context(), limit, offset, teamSlug)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]InviteResponse, 0, len(rows))
	for _, inv := range rows {
		out = append(out, inviteToResponse(inv))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) CreateInvite(w http.ResponseWriter, r *http.Request) {
	var req CreateInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	// Accept the new role names ("admin"/"manager"/"member") as well as the
	// legacy "user" alias for backward compatibility with older callers.
	if req.Role == "" {
		req.Role = "member"
	}
	switch req.Role {
	case "admin", "manager", "member":
	case "user":
		req.Role = "member"
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", `role must be "admin", "manager", or "member"`)
		return
	}

	// Manager privilege guard: managers can only mint member-role invites,
	// and only into their own team. They cannot create admin invites or
	// invite into other teams. Admins are unrestricted.
	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		if req.Role != "member" {
			writeJSONError(w, http.StatusForbidden, "forbidden",
				"managers can only invite members; ask an admin for elevated invites")
			return
		}
		ownTeam := p.TeamSlug()
		if ownTeam == "" {
			writeJSONError(w, http.StatusForbidden, "forbidden", "manager has no team to invite into")
			return
		}
		if req.TeamSlug == "" {
			req.TeamSlug = ownTeam
		}
		if req.TeamSlug != ownTeam {
			writeJSONError(w, http.StatusForbidden, "forbidden",
				"managers can only invite into their own team")
			return
		}
	}
	hours := req.ExpiresInHours
	if hours <= 0 {
		hours = 168
	}
	if hours > 24*30 {
		hours = 24 * 30
	}
	expiresAt := time.Now().Add(time.Duration(hours) * time.Hour)

	var teamID *int64
	var teamSlug string
	if req.TeamSlug != "" {
		t, err := h.Store.GetTeamBySlug(r.Context(), req.TeamSlug)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeJSONError(w, http.StatusBadRequest, "invalid_request", "team_slug not found")
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		teamID = &t.ID
		teamSlug = t.Slug
	}

	rawToken, hash, prefix, err := auth.GenerateInviteToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	inv, err := h.Store.CreateInvite(r.Context(), hash, prefix, req.Email, req.Role, teamID, expiresAt)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	resp := CreateInviteResponseBody{
		InviteResponse: InviteResponse{
			ID: inv.ID, Prefix: inv.TokenPrefix, Email: inv.Email,
			TeamID: inv.TeamID, TeamSlug: teamSlug, Role: inv.Role,
			ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt,
		},
		Token: rawToken,
		URL:   "/signup/" + rawToken,
	}
	h.audit(r, "invite.create", "invite", prefix, map[string]any{
		"email":      req.Email,
		"team_slug":  req.TeamSlug,
		"role":       req.Role,
		"expires_at": expiresAt,
	})
	writeJSON(w, http.StatusCreated, resp)
}

type DeploymentResponse struct {
	Name                string                        `json:"name"`
	ProviderType        string                        `json:"provider_type"`
	UpstreamModel       string                        `json:"upstream_model"`
	CredentialRef       string                        `json:"credential_ref"`
	BaseURL             *string                       `json:"base_url,omitempty"`
	Region              *string                       `json:"region,omitempty"`
	Enabled             bool                          `json:"enabled"`
	HasCredential       bool                          `json:"has_credential"`
	MaxParallelRequests *int                          `json:"max_parallel_requests,omitempty"`
	Capabilities        *store.DeploymentCapabilities `json:"capabilities,omitempty"`
	Streaming           *config.StreamingConfig       `json:"streaming"`
	// ManagedBy is "config" when the gateway config file defines this
	// entry; it is re-applied at every start, replacing console edits.
	ManagedBy string `json:"managed_by,omitempty"`
}

var supportedProviderTypes = []string{
	"openai",
	"azure_openai",
	"anthropic",
	"openai_compatible",
	"ollama",
	"vllm",
	"mistral",
	"groq",
	"together",
	"fireworks",
	"openrouter",
	"cohere",
	"bedrock",
	"vertex",
	"gemini",
}

func providerNeedsCredential(providerType string) bool {
	switch providerType {
	case "openai_compatible", "ollama", "vllm":
		return false
	default:
		return true
	}
}

func (h *AdminHandler) withDeploymentSource(d DeploymentResponse) DeploymentResponse {
	if h.Config != nil {
		for _, c := range h.Config.Deployments {
			if c.Name == d.Name {
				d.ManagedBy = "config"
				break
			}
		}
	}
	return d
}

func (h *AdminHandler) withAliasSource(a AliasResponse) AliasResponse {
	if h.Config != nil {
		for _, c := range h.Config.Aliases {
			if c.Alias == a.Alias {
				a.ManagedBy = "config"
				break
			}
		}
	}
	return a
}

func deploymentToResponse(d *store.Deployment) DeploymentResponse {
	hasCredential := true
	if d.CredentialRef != "" {
		hasCredential = os.Getenv(d.CredentialRef) != ""
	}
	return DeploymentResponse{
		Name: d.Name, ProviderType: d.ProviderType,
		UpstreamModel: d.UpstreamModel, CredentialRef: d.CredentialRef,
		BaseURL: d.BaseURL, Region: d.Region, Enabled: d.Enabled,
		HasCredential:       hasCredential,
		MaxParallelRequests: d.MaxParallelRequests,
		Capabilities:        d.Capabilities,
		Streaming:           d.Streaming,
	}
}

func (h *AdminHandler) ListDeployments(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	deps, total, err := h.Store.ListDeploymentsPage(r.Context(), limit, offset, r.URL.Query().Get("q"))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]DeploymentResponse, 0, len(deps))
	for _, d := range deps {
		out = append(out, h.withDeploymentSource(deploymentToResponse(d)))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type CreateDeploymentRequest struct {
	SupportsResponses   *bool                   `json:"supports_responses"`
	Name                string                  `json:"name"`
	ProviderType        string                  `json:"provider_type"`
	UpstreamModel       string                  `json:"upstream_model"`
	CredentialRef       string                  `json:"credential_ref"`
	BaseURL             string                  `json:"base_url"`
	Region              string                  `json:"region"`
	MaxParallelRequests *int                    `json:"max_parallel_requests"`
	SupportsChat        *bool                   `json:"supports_chat"`
	SupportsStreamChat  *bool                   `json:"supports_stream_chat"`
	SupportsEmbeddings  *bool                   `json:"supports_embeddings"`
	Streaming           *config.StreamingConfig `json:"streaming"`
}

func boolPtrOrFalse(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

func (h *AdminHandler) CreateDeployment(w http.ResponseWriter, r *http.Request) {
	var req CreateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Name == "" || req.ProviderType == "" || req.UpstreamModel == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"name, provider_type, and upstream_model are required")
		return
	}
	if !slices.Contains(supportedProviderTypes, req.ProviderType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			`provider_type must be one of openai, azure_openai, anthropic, openai_compatible, ollama, vllm, mistral, groq, together, fireworks, openrouter, cohere`)
		return
	}
	if providerNeedsCredential(req.ProviderType) && req.CredentialRef == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"credential_ref is required for this provider_type")
		return
	}
	if req.MaxParallelRequests != nil && *req.MaxParallelRequests < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "max_parallel_requests must be non-negative")
		return
	}
	var baseURL, region *string
	if req.BaseURL != "" {
		baseURL = &req.BaseURL
	}
	if req.Region != "" {
		region = &req.Region
	}

	// Capabilities are explicit only when the caller sent at least one flag.
	// Otherwise leave the column NULL so the router falls back to provider
	// defaults (= the historical behavior).
	var caps *store.DeploymentCapabilities
	if req.SupportsChat != nil || req.SupportsStreamChat != nil || req.SupportsEmbeddings != nil || req.SupportsResponses != nil {
		caps = &store.DeploymentCapabilities{
			Responses:  req.SupportsResponses,
			Chat:       boolPtrOrFalse(req.SupportsChat),
			StreamChat: boolPtrOrFalse(req.SupportsStreamChat),
			Embeddings: boolPtrOrFalse(req.SupportsEmbeddings),
		}
	}

	maxParallelRequests := positiveIntPtr(req.MaxParallelRequests)
	d, err := h.Store.UpsertDeployment(r.Context(), req.Name, req.ProviderType, req.UpstreamModel, req.CredentialRef, baseURL, region, caps, maxParallelRequests, req.Streaming)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "deployment.upsert", "deployment", d.Name, map[string]any{
		"streaming":             d.Streaming,
		"provider_type":         d.ProviderType,
		"upstream_model":        d.UpstreamModel,
		"credential_ref":        d.CredentialRef,
		"base_url":              d.BaseURL,
		"region":                d.Region,
		"max_parallel_requests": d.MaxParallelRequests,
		"capabilities":          d.Capabilities,
	})
	writeJSON(w, http.StatusCreated, h.withDeploymentSource(deploymentToResponse(d)))
}

type UpdateDeploymentRequest struct {
	SupportsResponses   *bool           `json:"supports_responses"`
	ProviderType        string          `json:"provider_type"`
	UpstreamModel       string          `json:"upstream_model"`
	CredentialRef       string          `json:"credential_ref"`
	BaseURL             *string         `json:"base_url"`
	Region              *string         `json:"region"`
	MaxParallelRequests *int            `json:"max_parallel_requests"`
	SupportsChat        *bool           `json:"supports_chat"`
	SupportsStreamChat  *bool           `json:"supports_stream_chat"`
	SupportsEmbeddings  *bool           `json:"supports_embeddings"`
	Streaming           json.RawMessage `json:"streaming"`
}

// UpdateDeployment edits an existing deployment. The deployment name is
// pinned (since aliases reference it); use delete+recreate to rename.
// Backed by the same UpsertDeployment store method as CreateDeployment.
func (h *AdminHandler) UpdateDeployment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	existing, err := h.Store.GetDeploymentByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "deployment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req UpdateDeploymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.ProviderType == "" {
		req.ProviderType = existing.ProviderType
	}
	if req.UpstreamModel == "" {
		req.UpstreamModel = existing.UpstreamModel
	}
	if req.CredentialRef == "" {
		req.CredentialRef = existing.CredentialRef
	}
	if !slices.Contains(supportedProviderTypes, req.ProviderType) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			`provider_type must be one of openai, azure_openai, anthropic, openai_compatible, ollama, vllm, mistral, groq, together, fireworks, openrouter, cohere`)
		return
	}
	if providerNeedsCredential(req.ProviderType) && req.CredentialRef == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"credential_ref is required for this provider_type")
		return
	}
	if req.MaxParallelRequests != nil && *req.MaxParallelRequests < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "max_parallel_requests must be non-negative")
		return
	}
	baseURL := existing.BaseURL
	if req.BaseURL != nil {
		baseURL = nonEmptyStringPtr(*req.BaseURL)
	}
	region := existing.Region
	if req.Region != nil {
		region = nonEmptyStringPtr(*req.Region)
	}
	caps := existing.Capabilities
	if req.SupportsChat != nil || req.SupportsStreamChat != nil || req.SupportsEmbeddings != nil {
		caps = &store.DeploymentCapabilities{
			Chat:       boolPtrOrFalse(req.SupportsChat),
			StreamChat: boolPtrOrFalse(req.SupportsStreamChat),
			Embeddings: boolPtrOrFalse(req.SupportsEmbeddings),
		}
		if existing.Capabilities != nil {
			caps.Responses = existing.Capabilities.Responses
		}
	}
	if req.SupportsResponses != nil {
		if caps == nil {
			defaults := router.DefaultCapabilities(req.ProviderType)
			caps = &store.DeploymentCapabilities{Chat: defaults.Chat, StreamChat: defaults.StreamChat, Embeddings: defaults.Embeddings}
		} else {
			copy := *caps
			caps = &copy
		}
		caps.Responses = req.SupportsResponses
	}
	maxParallelRequests := existing.MaxParallelRequests
	if req.MaxParallelRequests != nil {
		maxParallelRequests = positiveIntPtr(req.MaxParallelRequests)
	}
	streaming := existing.Streaming
	if len(req.Streaming) != 0 {
		if err := json.Unmarshal(req.Streaming, &streaming); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
	}
	d, err := h.Store.UpsertDeployment(r.Context(), name, req.ProviderType, req.UpstreamModel, req.CredentialRef, baseURL, region, caps, maxParallelRequests, streaming)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "deployment.update", "deployment", d.Name, map[string]any{
		"streaming":             d.Streaming,
		"provider_type":         d.ProviderType,
		"upstream_model":        d.UpstreamModel,
		"credential_ref":        d.CredentialRef,
		"base_url":              d.BaseURL,
		"region":                d.Region,
		"max_parallel_requests": d.MaxParallelRequests,
		"capabilities":          d.Capabilities,
	})
	writeJSON(w, http.StatusOK, h.withDeploymentSource(deploymentToResponse(d)))
}

func nonEmptyStringPtr(value string) *string {
	if value == "" {
		return nil
	}
	v := value
	return &v
}

// positiveIntPtr normalizes the public API's zero value to NULL/unlimited.
// Callers validate negative values before reaching this helper.
func positiveIntPtr(value *int) *int {
	if value == nil || *value == 0 {
		return nil
	}
	v := *value
	return &v
}

// ProviderHealthRow is the registry's DeploymentHealth augmented with the
// 5-minute traffic stats from usage_log. Keeping the merge on the wire
// instead of doing it client-side means /admin/provider-health stays the
// single source of truth for the Settings → Providers cards.
type ProviderHealthRow struct {
	router.DeploymentHealth
	Stats *store.DeploymentStats5m `json:"stats,omitempty"`
}

// GetProviderHealthHistory returns the recent samples for a single
// deployment. The Settings sparkline calls this once per card.
func (h *AdminHandler) GetProviderHealthHistory(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "deployment name is required")
		return
	}
	limit := 60
	if v, _ := strconv.Atoi(r.URL.Query().Get("limit")); v > 0 {
		limit = v
	}
	samples, err := h.Store.ListProviderHealthSamples(r.Context(), name, limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, samples)
}

func (h *AdminHandler) GetProviderHealth(w http.ResponseWriter, r *http.Request) {
	if h.Registry == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "unavailable", "provider registry unavailable")
		return
	}
	health := h.Registry.Health()
	statsByName := map[string]store.DeploymentStats5m{}
	if stats, err := h.Store.GetDeploymentStats5m(r.Context()); err == nil {
		for _, s := range stats {
			statsByName[s.Deployment] = s
		}
	}
	out := make([]ProviderHealthRow, 0, len(health))
	for _, hh := range health {
		row := ProviderHealthRow{DeploymentHealth: hh}
		if s, ok := statsByName[hh.Name]; ok {
			s := s
			row.Stats = &s
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) DeleteDeployment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.Store.DeleteDeployment(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "deployment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "deployment.delete", "deployment", name, nil)
	w.WriteHeader(http.StatusNoContent)
}

type AliasResponse struct {
	Alias           string         `json:"alias"`
	Deployments     []string       `json:"deployments"`
	CacheEnabled    bool           `json:"cache_enabled"`
	CacheTTLSeconds int            `json:"cache_ttl_seconds"`
	Strategy        string         `json:"strategy"`
	StrategyOptions map[string]any `json:"strategy_options"`
	// ManagedBy is "config" when the gateway config file defines this
	// entry; it is re-applied at every start, replacing console edits.
	ManagedBy string `json:"managed_by,omitempty"`
}

func aliasToResponse(a *store.AliasWithDeployments) AliasResponse {
	opts := a.StrategyOptions
	if opts == nil {
		opts = map[string]any{}
	}
	strat := a.Strategy
	if strat == "" {
		strat = "priority"
	}
	// A nil slice marshals as JSON null, which crashed the console's Models
	// page for aliases with no deployments; always serialize an array.
	deps := a.Deployments
	if deps == nil {
		deps = []string{}
	}
	return AliasResponse{
		Alias:           a.Alias,
		Deployments:     deps,
		CacheEnabled:    a.CacheEnabled,
		CacheTTLSeconds: a.CacheTTLSeconds,
		Strategy:        strat,
		StrategyOptions: opts,
	}
}

func (h *AdminHandler) ListAliases(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListAliasesWithDeploymentsPage(r.Context(), limit, offset, r.URL.Query().Get("q"), r.URL.Query().Get("unrouted") == "true")
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]AliasResponse, 0, len(rows))
	for _, a := range rows {
		out = append(out, h.withAliasSource(aliasToResponse(a)))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type SetUserDisabledRequest struct {
	Disabled bool `json:"disabled"`
}

// SetUserDisabled flips the disabled_at flag, also revoking every live
// session for the user (disable should be immediate, not "after their
// cookie expires"). Re-enable does not restore sessions.
func (h *AdminHandler) SetUserDisabled(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	var req SetUserDisabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.Store.SetUserDisabled(r.Context(), id, req.Disabled); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if req.Disabled {
		_ = h.Store.DeleteSessionsForUser(r.Context(), id)
	}
	h.audit(r, "user.disable_set", "user", strconv.FormatInt(id, 10), map[string]any{"disabled": req.Disabled})
	w.WriteHeader(http.StatusNoContent)
}

// SetUserRoleRequest carries an explicit admin override of role/team.
// Setting either field flips role_managed_by_oidc=false on the row so
// claim mapping won't undo this on the user's next OIDC sign-in.
type SetUserRoleRequest struct {
	Role     *string `json:"role,omitempty"`
	TeamSlug *string `json:"team_slug,omitempty"`
}

func (h *AdminHandler) SetUserRole(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	var req SetUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Role == nil && req.TeamSlug == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "role or team_slug is required")
		return
	}
	if req.Role != nil {
		switch *req.Role {
		case store.RoleAdmin, store.RoleManager, store.RoleMember:
		default:
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "role must be admin, manager, or member")
			return
		}
		if _, err := h.Store.Pool.Exec(r.Context(),
			`UPDATE users SET role = $2, is_admin = $3 WHERE id = $1`,
			id, *req.Role, *req.Role == store.RoleAdmin); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	if req.TeamSlug != nil {
		// Pre-validate the slug. The previous form used a subquery in the
		// UPDATE, which silently set team_id = NULL on a typo and
		// orphaned the user without an error path.
		var teamID int64
		err := h.Store.Pool.QueryRow(r.Context(),
			`SELECT id FROM teams WHERE slug = $1 AND archived_at IS NULL`,
			*req.TeamSlug).Scan(&teamID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeJSONError(w, http.StatusNotFound, "not_found", "team not found: "+*req.TeamSlug)
				return
			}
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		if _, err := h.Store.Pool.Exec(r.Context(),
			`UPDATE users SET team_id = $2 WHERE id = $1`,
			id, teamID); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
	}
	if err := h.Store.MarkRoleManuallyEdited(r.Context(), id); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "user.role_set", "user", strconv.FormatInt(id, 10), map[string]any{
		"role":      req.Role,
		"team_slug": req.TeamSlug,
	})
	w.WriteHeader(http.StatusNoContent)
}

type UpdateAliasStrategyRequest struct {
	Strategy        string         `json:"strategy"`
	StrategyOptions map[string]any `json:"strategy_options"`
}

func (h *AdminHandler) UpdateAliasStrategy(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	var req UpdateAliasStrategyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	switch req.Strategy {
	case "", "priority", "tagged", "region", "cost", "latency":
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			`strategy must be one of: priority, tagged, region, cost, latency`)
		return
	}
	if err := h.Store.UpdateAliasStrategy(r.Context(), alias, req.Strategy, req.StrategyOptions); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "alias not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "alias.strategy_update", "model_alias", alias, map[string]any{
		"strategy":         req.Strategy,
		"strategy_options": req.StrategyOptions,
	})
	writeJSON(w, http.StatusOK, req)
}

type UpdateDeploymentTagsRequest struct {
	Tags []string `json:"tags"`
}

func (h *AdminHandler) UpdateDeploymentTagsHandler(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req UpdateDeploymentTagsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.Store.UpdateDeploymentTags(r.Context(), name, req.Tags); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "deployment not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "deployment.tags_update", "deployment", name, map[string]any{"tags": req.Tags})
	writeJSON(w, http.StatusOK, req)
}

type UpdateAliasCacheRequest struct {
	CacheEnabled    bool `json:"cache_enabled"`
	CacheTTLSeconds int  `json:"cache_ttl_seconds"`
}

// UpdateAliasCache toggles the prompt cache on an alias and sets the TTL.
// Doc 0001 keeps cache config out of the existing UpsertAlias path so the
// cache is editable independently of deployment routing.
func (h *AdminHandler) UpdateAliasCache(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	var req UpdateAliasCacheRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.CacheEnabled && req.CacheTTLSeconds <= 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "cache_ttl_seconds must be > 0 when cache_enabled is true")
		return
	}
	if err := h.Store.UpdateAliasCacheSettings(r.Context(), alias, req.CacheEnabled, req.CacheTTLSeconds); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "alias not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "alias.cache_update", "model_alias", alias, map[string]any{
		"cache_enabled":     req.CacheEnabled,
		"cache_ttl_seconds": req.CacheTTLSeconds,
	})
	writeJSON(w, http.StatusOK, req)
}

type UpsertAliasRequest struct {
	Alias       string   `json:"alias"`
	Deployments []string `json:"deployments"`
}

func (h *AdminHandler) UpsertAlias(w http.ResponseWriter, r *http.Request) {
	var req UpsertAliasRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Alias == "" || len(req.Deployments) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"alias and at least one deployment are required")
		return
	}
	a, err := h.Store.UpsertAlias(r.Context(), req.Alias, req.Deployments)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "alias.upsert", "model_alias", a.Alias, map[string]any{
		"deployments": a.Deployments,
	})
	writeJSON(w, http.StatusCreated, h.withAliasSource(aliasToResponse(a)))
}

func (h *AdminHandler) DeleteAlias(w http.ResponseWriter, r *http.Request) {
	alias := chi.URLParam(r, "alias")
	if err := h.Store.DeleteAlias(r.Context(), alias); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "alias not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.refreshRegistry(r)
	h.audit(r, "alias.delete", "model_alias", alias, nil)
	w.WriteHeader(http.StatusNoContent)
}

type PricingResponse struct {
	store.PricingTiers
	ProviderType          string    `json:"provider_type"`
	UpstreamModel         string    `json:"upstream_model"`
	InputPerMillionCents  int64     `json:"input_per_million_cents"`
	OutputPerMillionCents int64     `json:"output_per_million_cents"`
	EffectiveAt           time.Time `json:"effective_at"`
}

type UpsertPricingRequest struct {
	store.PricingTiers
	ProviderType          string `json:"provider_type"`
	UpstreamModel         string `json:"upstream_model"`
	InputPerMillionCents  int64  `json:"input_per_million_cents"`
	OutputPerMillionCents int64  `json:"output_per_million_cents"`
}

func pricingToResponse(p *store.Pricing) PricingResponse {
	return PricingResponse{
		PricingTiers:          p.Tiers,
		ProviderType:          p.ProviderType,
		UpstreamModel:         p.UpstreamModel,
		InputPerMillionCents:  p.InputPerMillionCents,
		OutputPerMillionCents: p.OutputPerMillionCents,
		EffectiveAt:           p.EffectiveAt,
	}
}

func (h *AdminHandler) ListPricing(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListCurrentPricingPage(r.Context(), limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]PricingResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, pricingToResponse(row))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

func (h *AdminHandler) UpsertPricing(w http.ResponseWriter, r *http.Request) {
	var req UpsertPricingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.ProviderType == "" || req.UpstreamModel == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "provider_type and upstream_model are required")
		return
	}
	if req.InputPerMillionCents < 0 || req.OutputPerMillionCents < 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "pricing values must be zero or greater")
		return
	}
	if err := req.PricingTiers.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	row, err := h.Store.UpsertPricing(r.Context(), req.ProviderType, req.UpstreamModel, req.InputPerMillionCents, req.OutputPerMillionCents, req.PricingTiers)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "pricing.upsert", "pricing", req.ProviderType+"/"+req.UpstreamModel, map[string]any{
		"tiers":                    req.PricingTiers,
		"input_per_million_cents":  req.InputPerMillionCents,
		"output_per_million_cents": req.OutputPerMillionCents,
	})
	writeJSON(w, http.StatusCreated, pricingToResponse(row))
}

func (h *AdminHandler) GetSpendReport(w http.ResponseWriter, r *http.Request) {
	teamSlug := r.URL.Query().Get("team")
	// Managers are pinned to their own team's spend.
	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		ownTeam := p.TeamSlug()
		if ownTeam == "" {
			writeJSON(w, http.StatusOK, store.SpendReport{Aliases: []store.SpendByAlias{}})
			return
		}
		if teamSlug == "" {
			teamSlug = ownTeam
		} else if teamSlug != ownTeam {
			writeJSONError(w, http.StatusForbidden, "forbidden", "managers can only view their own team's spend")
			return
		}
	}
	var userID *int64
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id must be an integer")
			return
		}
		userID = &parsed
	}
	var from, to time.Time
	var err error
	if raw := r.URL.Query().Get("from"); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return
		}
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		to, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return
		}
	}
	customer := r.URL.Query().Get("customer")
	if customer != "" {
		if teamSlug == "" {
			writeJSONError(w, 400, "invalid_request", "team is required with a customer filter")
			return
		}
		if err := store.ValidateCustomerID(customer); err != nil {
			writeJSONError(w, 400, "invalid_request", err.Error())
			return
		}
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy != "" && groupBy != "alias" && !store.ValidSpendGroupBy(groupBy) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "group_by must be alias, team, key, user or customer")
		return
	}
	report, err := h.Store.GetSpendReport(r.Context(), store.SpendFilter{
		TeamSlug:           teamSlug,
		CustomerExternalID: customer,
		UserID:             userID,
		From:               from,
		To:                 to,
		GroupBy:            groupBy,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// GetUsageAggregate returns per-bucket request/error/token/latency stats
// for the dashboard chart. Managers are pinned to their own team like
// the row-level /admin/usage endpoint.
func (h *AdminHandler) GetUsageAggregate(w http.ResponseWriter, r *http.Request) {
	teamSlug := r.URL.Query().Get("team")
	alias := r.URL.Query().Get("alias")
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "hour"
	}

	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		ownTeam := p.TeamSlug()
		if ownTeam == "" {
			writeJSON(w, http.StatusOK, []store.UsageBucket{})
			return
		}
		if teamSlug == "" {
			teamSlug = ownTeam
		} else if teamSlug != ownTeam {
			writeJSONError(w, http.StatusForbidden, "forbidden", "managers can only view their own team's usage")
			return
		}
	}

	from, to, ok := parseTimeRange(w, r)
	if !ok {
		return
	}

	rows, err := h.Store.GetUsageAggregate(r.Context(), teamSlug, alias, from, to, bucket)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// GetSpendTimeseries returns per-bucket cost breakdown by alias for the
// /spend dashboard chart. Reuses the same role-aware filtering as
// GetSpendReport.
func (h *AdminHandler) GetSpendTimeseries(w http.ResponseWriter, r *http.Request) {
	teamSlug := r.URL.Query().Get("team")
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}

	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		ownTeam := p.TeamSlug()
		if ownTeam == "" {
			writeJSON(w, http.StatusOK, store.SpendTimeseries{Series: []store.SpendBucket{}, Aliases: []string{}})
			return
		}
		if teamSlug == "" {
			teamSlug = ownTeam
		} else if teamSlug != ownTeam {
			writeJSONError(w, http.StatusForbidden, "forbidden", "managers can only view their own team's spend")
			return
		}
	}

	var userID *int64
	if raw := r.URL.Query().Get("user_id"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "user_id must be an integer")
			return
		}
		userID = &parsed
	}

	from, to, ok := parseTimeRange(w, r)
	if !ok {
		return
	}

	report, err := h.Store.GetSpendTimeseries(r.Context(), teamSlug, userID, from, to, bucket)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// parseTimeRange reads ?from= and ?to= as RFC3339 strings, returning
// zero values when absent (the store layer applies sane defaults).
func parseTimeRange(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	if raw := r.URL.Query().Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be RFC3339")
			return time.Time{}, time.Time{}, false
		}
		from = parsed
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "to must be RFC3339")
			return time.Time{}, time.Time{}, false
		}
		to = parsed
	}
	return from, to, true
}

// GetAuditFacets returns the resource and actor types present in the log.
func (h *AdminHandler) GetAuditFacets(w http.ResponseWriter, r *http.Request) {
	facets, err := h.Store.GetAuditFacets(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load audit facets")
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

func (h *AdminHandler) ListAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	q := r.URL.Query()
	rows, total, err := h.Store.ListAuditEventsFiltered(r.Context(), store.AuditFilter{
		Query: q.Get("q"), ResourceType: q.Get("resource_type"), ActorType: q.Get("actor_type"),
	}, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, rows)
}

// usageFilterFromRequest scopes and parses the logs query shared by the
// list and facet endpoints. ok=false means a response was already written;
// empty=true means a manager without a team, who sees nothing.
func usageFilterFromRequest(w http.ResponseWriter, r *http.Request) (f store.UsageFilter, empty, ok bool) {
	q := r.URL.Query()
	slug := q.Get("team")
	// Managers are pinned to their own team regardless of any ?team= they
	// pass. If they try a different team explicitly, refuse rather than
	// silently rewriting (avoids confusion when results don't match the
	// query).
	p := auth.PrincipalFromContext(r.Context())
	if !p.IsAdmin() {
		ownTeam := p.TeamSlug()
		if ownTeam == "" {
			return f, true, true
		}
		if slug == "" {
			slug = ownTeam
		} else if slug != ownTeam {
			writeJSONError(w, http.StatusForbidden, "forbidden", "managers can only view their own team's usage")
			return f, false, false
		}
	}
	f.TeamSlug = slug
	// An optional RFC3339 lower bound keeps the list on the same window as
	// the aggregate tiles; an invalid value is rejected rather than ignored
	// so the console never shows a wider window than it claims.
	if raw := q.Get("from"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "from must be an RFC3339 timestamp")
			return f, false, false
		}
		f.Since = parsed
	}
	f.Alias = q.Get("alias")
	f.KeyPrefix = q.Get("key")
	f.Query = q.Get("q")
	switch status := q.Get("status"); status {
	case "", "all":
	case "2xx", "4xx", "5xx", "error":
		f.StatusClass = status
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "status must be 2xx, 4xx, 5xx or error")
		return f, false, false
	}
	switch latency := q.Get("latency"); latency {
	case "":
	case "fast", "med", "slow":
		f.LatencyBand = latency
	default:
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "latency must be fast, med or slow")
		return f, false, false
	}
	return f, false, true
}

func (h *AdminHandler) ListUsage(w http.ResponseWriter, r *http.Request) {
	f, empty, ok := usageFilterFromRequest(w, r)
	if !ok {
		return
	}
	if empty {
		setTotalCount(w, 0)
		writeJSON(w, http.StatusOK, []UsageRowResponse{})
		return
	}
	f.Limit, f.Offset = parsePagination(r)
	rows, total, err := h.Store.ListUsage(r.Context(), f)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]UsageRowResponse, 0, len(rows))
	for _, row := range rows {
		out = append(out, usageRowToResponse(row))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

// GetUsageFacets returns status-class counts for the same filters as
// ListUsage (status itself ignored), so the console's 2xx/4xx/5xx chips
// describe the whole window rather than the loaded page.
func (h *AdminHandler) GetUsageFacets(w http.ResponseWriter, r *http.Request) {
	f, empty, ok := usageFilterFromRequest(w, r)
	if !ok {
		return
	}
	if empty {
		writeJSON(w, http.StatusOK, store.UsageFacets{})
		return
	}
	facets, err := h.Store.UsageFacetCounts(r.Context(), f)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not count requests")
		return
	}
	writeJSON(w, http.StatusOK, facets)
}

// GetUsageRow serves one request for its permalink. Managers can only open
// requests from their own team; others read as not found.
func (h *AdminHandler) GetUsageRow(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	scope := ""
	if p := auth.PrincipalFromContext(r.Context()); !p.IsAdmin() {
		scope = p.TeamSlug()
		if scope == "" {
			writeJSONError(w, http.StatusNotFound, "not_found", "request not found")
			return
		}
	}
	row, err := h.Store.GetUsageRow(r.Context(), id, scope)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound, "not_found", "request not found")
		return
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load request")
		return
	}
	writeJSON(w, http.StatusOK, usageRowToResponse(row))
}
