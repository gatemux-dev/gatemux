package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// EffectivePolicyResponse explains every limit that fires for a given
// virtual key — team layer, owner (user or service account) layer, and
// key layer — plus current spend and the resolved allowlist intersection.
//
// The point is to answer "why was this request denied?" without forcing
// the operator to chase three separate pages.
type EffectivePolicyResponse struct {
	KeyID            int64        `json:"key_id"`
	KeyPrefix        string       `json:"key_prefix"`
	KeyName          string       `json:"key_name"`
	OwnerKind        string       `json:"owner_kind"` // "user", "service_account", or ""
	PeriodStart      string       `json:"period_start"`
	PeriodEnd        string       `json:"period_end"`
	Team             PolicyLayer  `json:"team"`
	Owner            *PolicyLayer `json:"owner,omitempty"`
	Key              PolicyLayer  `json:"key"`
	EffectiveAllowed []string     `json:"effective_allowed_models"`
	Notes            []string     `json:"notes,omitempty"`
}

// PolicyLayer is the per-layer view: limits as configured plus current
// usage so the UI can render "X of Y used". A nil limit means uncapped.
type PolicyLayer struct {
	Label               string   `json:"label"`
	UsdLimitCents       *int64   `json:"usd_limit_cents,omitempty"`
	SpendSoFarCents     int64    `json:"spend_so_far_cents"`
	Period              string   `json:"period,omitempty"`
	RPM                 *int     `json:"rpm,omitempty"`
	TPM                 *int     `json:"tpm,omitempty"`
	MaxParallelRequests *int     `json:"max_parallel_requests,omitempty"`
	AllowedModels       []string `json:"allowed_models,omitempty"`
}

// GetEffectivePolicy returns the layered policy view for one key.
// Admin-only.
func (h *AdminHandler) GetEffectivePolicy(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	vk, team, err := h.Store.GetVirtualKeyByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	period := team.Period
	if period == "" {
		period = "month"
	}
	start, end := windowFor(period, time.Now().UTC())

	teamSpend, _ := h.Store.SumTeamSpendInWindow(r.Context(), team.ID, start, end)

	resp := EffectivePolicyResponse{
		KeyID:       vk.ID,
		KeyPrefix:   vk.KeyPrefix,
		KeyName:     vk.Name,
		PeriodStart: start.Format(time.RFC3339),
		PeriodEnd:   end.Format(time.RFC3339),
		Team: PolicyLayer{
			Label:               "Team " + team.Slug,
			UsdLimitCents:       team.UsdLimitCents,
			SpendSoFarCents:     teamSpend,
			Period:              team.Period,
			RPM:                 team.RPM,
			TPM:                 team.TPM,
			MaxParallelRequests: team.MaxParallelRequests,
			AllowedModels:       team.AllowedModels,
		},
		Key: PolicyLayer{
			Label:               "Key " + vk.KeyPrefix,
			UsdLimitCents:       vk.ScopedUsdLimitCents,
			SpendSoFarCents:     0, // filled below
			RPM:                 vk.ScopedRPM,
			TPM:                 vk.ScopedTPM,
			MaxParallelRequests: vk.MaxParallelRequests,
			AllowedModels:       vk.AllowedModels,
		},
	}
	keySpend, _ := h.Store.SumKeySpendInWindow(r.Context(), vk.ID, start, end)
	resp.Key.SpendSoFarCents = keySpend

	switch {
	case vk.UserID != nil:
		resp.OwnerKind = "user"
		if u, err := h.Store.GetUserByID(r.Context(), *vk.UserID); err == nil {
			userSpend, _ := h.Store.SumUserSpendInWindow(r.Context(), *vk.UserID, start, end)
			label := u.Email
			if u.Name != "" {
				label = u.Name + " (" + u.Email + ")"
			}
			resp.Owner = &PolicyLayer{
				Label:               "User " + label,
				UsdLimitCents:       u.UsdLimitCents,
				SpendSoFarCents:     userSpend,
				Period:              u.Period,
				MaxParallelRequests: u.MaxParallelRequests,
			}
		}
	case vk.ServiceAccountID != nil:
		resp.OwnerKind = "service_account"
		if sa, err := h.Store.GetServiceAccountByID(r.Context(), *vk.ServiceAccountID); err == nil {
			resp.Owner = &PolicyLayer{
				Label:               "Service account " + sa.Name,
				UsdLimitCents:       sa.UsdLimitCents,
				SpendSoFarCents:     0, // SA spend not separately summed today
				Period:              sa.Period,
				RPM:                 sa.RPM,
				TPM:                 sa.TPM,
				MaxParallelRequests: sa.MaxParallelRequests,
			}
		}
	}

	resp.EffectiveAllowed = intersectAllowed(team.AllowedModels, vk.AllowedModels)

	if vk.RevokedAt != nil {
		resp.Notes = append(resp.Notes, "Key is revoked — every request will be denied at admission.")
	}
	if vk.ExpiresAt != nil && vk.ExpiresAt.Before(time.Now().UTC()) {
		resp.Notes = append(resp.Notes, "Key has expired.")
	}

	writeJSON(w, http.StatusOK, resp)
}

// intersectAllowed returns the effective allowlist a request must pass.
// "*" on either side means no narrowing at that layer; the result is the
// most-restrictive non-wildcard set (or ["*"] when both are wildcard).
func intersectAllowed(team, key []string) []string {
	teamWild := isWildcard(team)
	keyWild := isWildcard(key)
	switch {
	case teamWild && keyWild:
		return []string{"*"}
	case teamWild:
		return append([]string(nil), key...)
	case keyWild:
		return append([]string(nil), team...)
	}
	keyset := map[string]bool{}
	for _, k := range key {
		keyset[k] = true
	}
	out := []string{}
	for _, t := range team {
		if keyset[t] {
			out = append(out, t)
		}
	}
	return out
}

func isWildcard(allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == "*" {
			return true
		}
	}
	return false
}
