package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// PassthroughResponse is the wire shape for /admin/passthroughs.
type PassthroughResponse struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	TargetURL       string `json:"target_url"`
	AuthHeader      string `json:"auth_header,omitempty"`
	AuthValueEnv    string `json:"auth_value_env,omitempty"`
	AuthValuePrefix string `json:"auth_value_prefix,omitempty"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       string `json:"created_at"`
}

func passthroughResponse(p *store.Passthrough) PassthroughResponse {
	return PassthroughResponse{
		ID:              p.ID,
		Name:            p.Name,
		TargetURL:       p.TargetURL,
		AuthHeader:      p.AuthHeader,
		AuthValueEnv:    p.AuthValueEnv,
		AuthValuePrefix: p.AuthValuePrefix,
		Enabled:         p.Enabled,
		CreatedAt:       p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
}

// ListPassthroughs returns every active passthrough configured. Admin-only
// because the response leaks the env-var name holding the upstream secret.
func (h *AdminHandler) ListPassthroughs(w http.ResponseWriter, r *http.Request) {
	limit, offset := parsePagination(r)
	items, total, err := h.Store.ListPassthroughs(r.Context(), limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]PassthroughResponse, 0, len(items))
	for _, p := range items {
		out = append(out, passthroughResponse(p))
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, out)
}

// CreatePassthroughRequest is the wire shape posted to /admin/passthroughs.
type CreatePassthroughRequest struct {
	Name            string `json:"name"`
	TargetURL       string `json:"target_url"`
	AuthHeader      string `json:"auth_header,omitempty"`
	AuthValueEnv    string `json:"auth_value_env,omitempty"`
	AuthValuePrefix string `json:"auth_value_prefix,omitempty"`
	Enabled         *bool  `json:"enabled,omitempty"`
}

func (h *AdminHandler) CreatePassthrough(w http.ResponseWriter, r *http.Request) {
	var req CreatePassthroughRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Name == "" || req.TargetURL == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "name and target_url are required")
		return
	}
	if !strings.HasPrefix(req.TargetURL, "http://") && !strings.HasPrefix(req.TargetURL, "https://") {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "target_url must be http:// or https://")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	p, err := h.Store.CreatePassthrough(r.Context(), store.CreatePassthroughParams{
		Name:            req.Name,
		TargetURL:       req.TargetURL,
		AuthHeader:      req.AuthHeader,
		AuthValueEnv:    req.AuthValueEnv,
		AuthValuePrefix: req.AuthValuePrefix,
		Enabled:         enabled,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "passthrough.create", "passthrough", p.Name, map[string]any{
		"target_url": p.TargetURL, "enabled": p.Enabled,
	})
	writeJSON(w, http.StatusCreated, passthroughResponse(p))
}

// UpdatePassthroughRequest patches one or more fields. nil pointer leaves
// the field untouched (the COALESCE in the UPDATE handles that).
type UpdatePassthroughRequest struct {
	TargetURL       *string `json:"target_url,omitempty"`
	AuthHeader      *string `json:"auth_header,omitempty"`
	AuthValueEnv    *string `json:"auth_value_env,omitempty"`
	AuthValuePrefix *string `json:"auth_value_prefix,omitempty"`
	Enabled         *bool   `json:"enabled,omitempty"`
}

func (h *AdminHandler) UpdatePassthrough(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req UpdatePassthroughRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.TargetURL != nil && !strings.HasPrefix(*req.TargetURL, "http://") && !strings.HasPrefix(*req.TargetURL, "https://") {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "target_url must be http:// or https://")
		return
	}
	p, err := h.Store.UpdatePassthrough(r.Context(), name, store.UpdatePassthroughParams{
		TargetURL:       req.TargetURL,
		AuthHeader:      req.AuthHeader,
		AuthValueEnv:    req.AuthValueEnv,
		AuthValuePrefix: req.AuthValuePrefix,
		Enabled:         req.Enabled,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "passthrough not found: "+name)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "passthrough.update", "passthrough", name, nil)
	writeJSON(w, http.StatusOK, passthroughResponse(p))
}

func (h *AdminHandler) DeletePassthrough(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if err := h.Store.DeletePassthrough(r.Context(), name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "passthrough not found: "+name)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "passthrough.delete", "passthrough", name, nil)
	w.WriteHeader(http.StatusNoContent)
}
