package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

type ServiceAccountResponse struct {
	ID                  int64      `json:"id"`
	TeamSlug            string     `json:"team_slug"`
	Name                string     `json:"name"`
	Description         string     `json:"description,omitempty"`
	UsdLimitCents       *int64     `json:"usd_limit_cents,omitempty"`
	Period              string     `json:"period"`
	RPM                 *int       `json:"rpm,omitempty"`
	TPM                 *int       `json:"tpm,omitempty"`
	MaxParallelRequests *int       `json:"max_parallel_requests,omitempty"`
	CreatedAt           time.Time  `json:"created_at"`
	ArchivedAt          *time.Time `json:"archived_at,omitempty"`
}

func saToResponse(sa *store.ServiceAccount) ServiceAccountResponse {
	return ServiceAccountResponse{
		ID:                  sa.ID,
		TeamSlug:            sa.TeamSlug,
		Name:                sa.Name,
		Description:         sa.Description,
		UsdLimitCents:       sa.UsdLimitCents,
		Period:              sa.Period,
		RPM:                 sa.RPM,
		TPM:                 sa.TPM,
		MaxParallelRequests: sa.MaxParallelRequests,
		CreatedAt:           sa.CreatedAt,
		ArchivedAt:          sa.ArchivedAt,
	}
}

func (h *AdminHandler) ListServiceAccounts(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListServiceAccountsForTeam(r.Context(), slug, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]ServiceAccountResponse, 0, len(rows))
	for _, sa := range rows {
		out = append(out, saToResponse(sa))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type CreateServiceAccountRequest struct {
	Name                string `json:"name"`
	Description         string `json:"description"`
	UsdLimitCents       *int64 `json:"usd_limit_cents"`
	Period              string `json:"period"`
	RPM                 *int   `json:"rpm"`
	TPM                 *int   `json:"tpm"`
	MaxParallelRequests *int   `json:"max_parallel_requests"`
}

func (h *AdminHandler) CreateServiceAccount(w http.ResponseWriter, r *http.Request) {
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
	var req CreateServiceAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	var createdBy *int64
	if p := auth.PrincipalFromContext(r.Context()); p != nil && p.User != nil {
		uid := p.User.ID
		createdBy = &uid
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	sa, err := h.Store.CreateServiceAccount(r.Context(), store.CreateServiceAccountParams{
		TeamID:              team.ID,
		Name:                req.Name,
		Description:         req.Description,
		UsdLimitCents:       req.UsdLimitCents,
		Period:              req.Period,
		RPM:                 req.RPM,
		TPM:                 req.TPM,
		MaxParallelRequests: maxParallelRequests,
		CreatedBy:           createdBy,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeJSONError(w, http.StatusConflict, "conflict", "service account with that name already exists in this team")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "service_account.create", "service_account", strconv.FormatInt(sa.ID, 10), map[string]any{
		"team_slug": team.Slug,
		"name":      sa.Name,
	})
	writeJSON(w, http.StatusCreated, saToResponse(sa))
}

func (h *AdminHandler) UpdateServiceAccountConcurrency(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
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
	sa, err := h.Store.UpdateServiceAccountConcurrency(r.Context(), id, maxParallelRequests)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "service account not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "service_account.concurrency.update", "service_account", strconv.FormatInt(id, 10), map[string]any{
		"max_parallel_requests": sa.MaxParallelRequests,
	})
	writeJSON(w, http.StatusOK, saToResponse(sa))
}

type UpdateServiceAccountRequest struct {
	Name          string  `json:"name"`
	Description   *string `json:"description,omitempty"`
	UsdLimitCents *int64  `json:"usd_limit_cents,omitempty"`
	UsdLimitClear bool    `json:"usd_limit_clear,omitempty"`
	Period        string  `json:"period"`
	RPM           *int    `json:"rpm,omitempty"`
	RPMClear      bool    `json:"rpm_clear,omitempty"`
	TPM           *int    `json:"tpm,omitempty"`
	TPMClear      bool    `json:"tpm_clear,omitempty"`
}

func (h *AdminHandler) UpdateServiceAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	var req UpdateServiceAccountRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	sa, err := h.Store.UpdateServiceAccount(r.Context(), id, store.UpdateServiceAccountParams{
		Name:          req.Name,
		Description:   req.Description,
		UsdLimitCents: req.UsdLimitCents,
		UsdLimitClear: req.UsdLimitClear,
		Period:        req.Period,
		RPM:           req.RPM,
		RPMClear:      req.RPMClear,
		TPM:           req.TPM,
		TPMClear:      req.TPMClear,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "service account not found")
			return
		}
		if errors.Is(err, store.ErrConflict) {
			writeJSONError(w, http.StatusConflict, "conflict", "name already in use")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "service_account.update", "service_account", strconv.FormatInt(sa.ID, 10), nil)
	writeJSON(w, http.StatusOK, saToResponse(sa))
}

func (h *AdminHandler) ArchiveServiceAccount(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	if err := h.Store.ArchiveServiceAccount(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "service account not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "service_account.archive", "service_account", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

type CreateServiceAccountKeyRequest struct {
	Name                string         `json:"name"`
	Metadata            map[string]any `json:"metadata"`
	AllowedModels       []string       `json:"allowed_models"`
	RPM                 *int           `json:"rpm,omitempty"`
	TPM                 *int           `json:"tpm,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	UsdLimitCents       *int64         `json:"usd_limit_cents"`
	ExpiresAt           *time.Time     `json:"expires_at,omitempty"`
}

// CreateServiceAccountKey issues a virtual key tied to a service account.
// Mirrors CreateKey but stamps service_account_id instead of user_id.
func (h *AdminHandler) CreateServiceAccountKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	sa, err := h.Store.GetServiceAccountByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "service account not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	team, err := h.Store.GetTeamByID(r.Context(), sa.TeamID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var req CreateServiceAccountKeyRequest
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

	rawKey, hash, prefix, err := auth.GenerateKey(team.Slug)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if _, err := h.Store.CreateVirtualKey(r.Context(), store.CreateVirtualKeyParams{
		TeamID:              team.ID,
		ServiceAccountID:    &sa.ID,
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
	h.audit(r, "service_account.key_create", "virtual_key", prefix, map[string]any{
		"usd_limit_cents":       req.UsdLimitCents,
		"team_slug":             team.Slug,
		"service_account_id":    sa.ID,
		"name":                  req.Name,
		"max_parallel_requests": maxParallelRequests,
	})
	writeJSON(w, http.StatusCreated, CreateKeyResponse{
		Key: rawKey, Prefix: prefix, Name: req.Name, TeamSlug: team.Slug,
	})
}
