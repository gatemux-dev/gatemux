package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5"
)

func (h *AdminHandler) directoryTeam(w http.ResponseWriter, r *http.Request) *store.Team {
	slug := chi.URLParam(r, "slug")
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(slug) {
		writeJSONError(w, http.StatusForbidden, "permission_denied", "team access denied")
		return nil
	}
	team, err := h.Store.GetTeamBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) || (err == nil && team.ArchivedAt != nil) {
		writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
		return nil
	}
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team")
		return nil
	}
	return team
}

func (h *AdminHandler) ListTeamMembers(w http.ResponseWriter, r *http.Request) {
	team := h.directoryTeam(w, r)
	if team == nil {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 256 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "directory search must be at most 256 bytes")
		return
	}
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListTeamMembers(r.Context(), team.ID, query, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team directory")
		return
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, rows)
}

func (h *AdminHandler) ListTeamModels(w http.ResponseWriter, r *http.Request) {
	team := h.directoryTeam(w, r)
	if team == nil {
		return
	}
	limit, offset := parsePagination(r)
	rows, total, err := h.Store.ListTeamModels(r.Context(), team.ID, limit, offset)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load team models")
		return
	}
	setTotalCount(w, total)
	writeJSON(w, http.StatusOK, rows)
}
