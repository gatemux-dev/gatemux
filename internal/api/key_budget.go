package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5"
)

func hasKeyBudget(r *http.Request) bool {
	key := auth.VirtualKeyFromContext(r.Context())
	return key != nil && key.ScopedUsdLimitCents != nil && *key.ScopedUsdLimitCents > 0
}

func (h *AdminHandler) keyBudgetAccess(w http.ResponseWriter, r *http.Request) (*store.VirtualKey, *store.Team) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeJSONError(w, 400, "invalid_request", "key id must be a positive integer")
		return nil, nil
	}
	key, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "key not found")
		return nil, nil
	}
	if err != nil {
		writeJSONError(w, 503, "budget_unavailable", "key policy unavailable")
		return nil, nil
	}
	if !auth.PrincipalFromContext(r.Context()).CanAccessTeam(team.Slug) {
		writeJSONError(w, 403, "forbidden", "no access to key budget")
		return nil, nil
	}
	return key, team
}

func (h *AdminHandler) GetKeyBudget(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	key, team := h.keyBudgetAccess(w, r)
	if key == nil {
		return
	}
	summary, err := budget.New(h.Store).KeySummary(ctx, key, team)
	if err != nil {
		writeJSONError(w, 503, "budget_unavailable", "key budget usage unavailable")
		return
	}
	writeJSON(w, 200, summary)
}

func (h *AdminHandler) SetKeyBudget(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	key, team := h.keyBudgetAccess(w, r)
	if key == nil {
		return
	}
	var body struct {
		Limit json.RawMessage `json:"usd_limit_cents"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || len(body.Limit) == 0 {
		writeJSONError(w, 400, "invalid_request", "usd_limit_cents is required; null clears the key cap")
		return
	}
	var limit *int64
	if json.Unmarshal(body.Limit, &limit) != nil || store.ValidateKeyBudget(limit) != nil {
		writeJSONError(w, 400, "invalid_request", "usd_limit_cents must be null or an integer between 0 and 9007199254740991; zero means no key cap")
		return
	}
	if err := h.Store.SetVirtualKeyBudget(ctx, key.ID, team.ID, limit); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, 404, "not_found", "key not found or revoked")
		} else {
			writeJSONError(w, 503, "budget_unavailable", "could not update key budget")
		}
		return
	}
	h.audit(r, "key.budget", "virtual_key", strconv.FormatInt(key.ID, 10), map[string]any{"team_slug": team.Slug, "usd_limit_cents": limit})
	w.WriteHeader(http.StatusNoContent)
}
