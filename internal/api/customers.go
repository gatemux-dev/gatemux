package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// CustomerResponse mirrors store.Customer with cents → float dollars where
// useful. Spend is filled in by ListCustomers via a join, not on this row.
type CustomerResponse struct {
	ID                  int64          `json:"id"`
	ExternalID          string         `json:"external_id"`
	Name                string         `json:"name"`
	Metadata            map[string]any `json:"metadata"`
	UsdLimitCents       *int64         `json:"usd_limit_cents,omitempty"`
	Period              string         `json:"period"`
	RPM                 *int           `json:"rpm,omitempty"`
	TPM                 *int           `json:"tpm,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	CreatedAt           string         `json:"created_at"`
}

func customerToResponse(c *store.Customer) CustomerResponse {
	return CustomerResponse{
		ID: c.ID, ExternalID: c.ExternalID, Name: c.Name, Metadata: c.Metadata,
		UsdLimitCents: c.UsdLimitCents, Period: c.Period,
		MaxParallelRequests: c.MaxParallelRequests,
		RPM:                 c.RPM, TPM: c.TPM, CreatedAt: c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *AdminHandler) ListCustomers(w http.ResponseWriter, r *http.Request) {
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
	rows, total, err := h.Store.ListCustomers(r.Context(), team.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]CustomerResponse, 0, len(rows))
	for _, c := range rows {
		out = append(out, customerToResponse(c))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

type CreateCustomerRequest struct {
	ExternalID          string         `json:"external_id"`
	Name                string         `json:"name"`
	Metadata            map[string]any `json:"metadata"`
	UsdLimitCents       *int64         `json:"usd_limit_cents"`
	Period              string         `json:"period"`
	RPM                 *int           `json:"rpm"`
	TPM                 *int           `json:"tpm"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
}

func (h *AdminHandler) CreateCustomer(w http.ResponseWriter, r *http.Request) {
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
	var req CreateCustomerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := store.ValidateCustomerID(req.ExternalID); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := store.ValidateCustomerLimits(req.UsdLimitCents, req.Period, req.RPM, req.TPM); err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	maxParallelRequests, ok := positiveLimit(w, req.MaxParallelRequests, "max_parallel_requests")
	if !ok {
		return
	}
	c, err := h.Store.CreateCustomer(r.Context(), store.CreateCustomerParams{
		TeamID: team.ID, ExternalID: req.ExternalID, Name: req.Name,
		Metadata: req.Metadata, UsdLimitCents: req.UsdLimitCents,
		Period: req.Period, RPM: req.RPM, TPM: req.TPM,
		MaxParallelRequests: maxParallelRequests,
	})
	if err != nil {
		var conflict *pgconn.PgError
		if errors.As(err, &conflict) && conflict.Code == "23505" {
			writeJSONError(w, 409, "customer_exists", "customer ID is already registered in this team")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "customer.create", "customer", c.ExternalID, map[string]any{
		"team":                  team.Slug,
		"usd_limit_cents":       c.UsdLimitCents,
		"period":                c.Period,
		"rpm":                   c.RPM,
		"tpm":                   c.TPM,
		"max_parallel_requests": c.MaxParallelRequests,
	})
	h.refreshConcurrencyPolicies(r)
	writeJSON(w, http.StatusCreated, customerToResponse(c))
}

func (h *AdminHandler) SetCustomerConcurrency(w http.ResponseWriter, r *http.Request) {
	team, err := h.Store.GetTeamBySlug(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
		} else {
			writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team")
		}
		return
	}
	var req struct {
		MaxParallelRequests json.RawMessage `json:"max_parallel_requests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MaxParallelRequests) == 0 {
		writeJSONError(w, 400, "invalid_request", "max_parallel_requests is required; null clears the cap")
		return
	}
	var limit *int
	if err := json.Unmarshal(req.MaxParallelRequests, &limit); err != nil {
		writeJSONError(w, 400, "invalid_request", "max_parallel_requests must be an integer or null")
		return
	}
	limit, ok := positiveLimit(w, limit, "max_parallel_requests")
	if !ok {
		return
	}
	c, err := h.Store.SetCustomerConcurrency(r.Context(), team.ID, chi.URLParam(r, "externalID"), limit)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeJSONError(w, 500, "internal_error", "could not save customer concurrency")
		return
	}
	h.audit(r, "customer.concurrency_update", "customer", c.ExternalID, map[string]any{"team": team.Slug, "max_parallel_requests": limit})
	h.refreshConcurrencyPolicies(r)
	writeJSON(w, http.StatusOK, customerToResponse(c))
}

type UpdateCustomerRequest struct {
	Name          *string         `json:"name"`
	UsdLimitCents json.RawMessage `json:"usd_limit_cents"`
	Period        *string         `json:"period"`
	RPM           json.RawMessage `json:"rpm"`
	TPM           json.RawMessage `json:"tpm"`
}

func (h *AdminHandler) UpdateCustomer(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	externalID := chi.URLParam(r, "externalID")
	team, err := h.Store.GetTeamBySlug(r.Context(), slug)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var req UpdateCustomerRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	existing, err := h.Store.GetCustomerByExternalID(r.Context(), team.ID, externalID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "internal_error", "customer lookup failed")
		return
	}
	params := store.UpdateCustomerParams{Name: existing.Name, UsdLimitCents: existing.UsdLimitCents, Period: existing.Period, RPM: existing.RPM, TPM: existing.TPM}
	if req.Name != nil {
		params.Name = *req.Name
	}
	if req.Period != nil {
		params.Period = *req.Period
	}
	for _, field := range []struct {
		raw    json.RawMessage
		target any
	}{{req.UsdLimitCents, &params.UsdLimitCents}, {req.RPM, &params.RPM}, {req.TPM, &params.TPM}} {
		if len(field.raw) > 0 {
			if err := json.Unmarshal(field.raw, field.target); err != nil {
				writeJSONError(w, 400, "invalid_request", "budget and rates must be integers or null")
				return
			}
		}
	}
	if err := store.ValidateCustomerLimits(params.UsdLimitCents, params.Period, params.RPM, params.TPM); err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	c, err := h.Store.UpdateCustomer(r.Context(), team.ID, externalID, params)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "customer not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "customer.update", "customer", c.ExternalID, map[string]any{
		"team":            team.Slug,
		"usd_limit_cents": c.UsdLimitCents,
		"period":          c.Period,
		"rpm":             c.RPM,
		"tpm":             c.TPM,
	})
	writeJSON(w, http.StatusOK, customerToResponse(c))
}

func (h *AdminHandler) SetCustomerRegistration(w http.ResponseWriter, r *http.Request) {
	team, err := h.Store.GetTeamBySlug(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "team not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "internal_error", "team lookup failed")
		return
	}
	var req struct {
		Mode string `json:"customer_registration"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, 400, "invalid_request", "invalid JSON")
		return
	}
	if req.Mode != "optional" && req.Mode != "required" && req.Mode != "auto_create" {
		writeJSONError(w, 400, "invalid_request", "customer_registration must be optional, required or auto_create")
		return
	}
	if err := h.Store.SetCustomerRegistration(r.Context(), team.ID, req.Mode); err != nil {
		writeJSONError(w, 503, "internal_error", "could not save customer registration policy")
		return
	}
	team.CustomerRegistration = req.Mode
	h.audit(r, "customer.policy_update", "team", team.Slug, map[string]any{"customer_registration": req.Mode})
	writeJSON(w, 200, teamToResponse(team))
}

func (h *AdminHandler) GetCustomerBudget(w http.ResponseWriter, r *http.Request) {
	team, err := h.Store.GetTeamBySlug(r.Context(), chi.URLParam(r, "slug"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "team not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "internal_error", "team lookup failed")
		return
	}
	c, err := h.Store.GetCustomerByExternalID(r.Context(), team.ID, chi.URLParam(r, "externalID"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "customer not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "internal_error", "customer lookup failed")
		return
	}
	summary, err := budget.New(h.Store).CustomerSummary(r.Context(), c)
	if err != nil {
		writeJSONError(w, 503, "internal_error", "customer budget summary unavailable")
		return
	}
	writeJSON(w, 200, summary)
}
