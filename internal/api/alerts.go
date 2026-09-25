package api

import (
	"encoding/json"
	"errors"
	"github.com/gatemux-dev/gatemux/internal/alerts"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/store"
)

type AlertRuleResponse struct {
	ID               int64          `json:"id"`
	Name             string         `json:"name"`
	Enabled          bool           `json:"enabled"`
	ScopeType        string         `json:"scope_type"`
	ScopeID          *int64         `json:"scope_id,omitempty"`
	TriggerType      string         `json:"trigger_type"`
	ThresholdOptions map[string]any `json:"threshold_options"`
	ChannelType      string         `json:"channel_type"`
	ChannelTarget    string         `json:"channel_target"`
	CooldownSeconds  int            `json:"cooldown_seconds"`
	CreatedAt        string         `json:"created_at"`
}

func alertRuleToResponse(r *store.AlertRule) AlertRuleResponse {
	return AlertRuleResponse{
		ID: r.ID, Name: r.Name, Enabled: r.Enabled,
		ScopeType: r.ScopeType, ScopeID: r.ScopeID,
		TriggerType: r.TriggerType, ThresholdOptions: r.ThresholdOptions,
		ChannelType: r.ChannelType, ChannelTarget: r.ChannelTarget,
		CooldownSeconds: r.CooldownSeconds,
		CreatedAt:       r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *AdminHandler) ListAlertRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.Store.ListAllAlertRules(r.Context())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]AlertRuleResponse, 0, len(rules))
	for _, rule := range rules {
		out = append(out, alertRuleToResponse(rule))
	}
	writeJSON(w, http.StatusOK, out)
}

type UpdateAlertRuleRequest struct {
	Enabled *bool `json:"enabled,omitempty"`
}

// UpdateAlertRule currently only supports toggling enabled. The Settings
// UI's primary mutation is the on/off switch; full editing routes through
// delete + recreate to keep the schema simple.
func (h *AdminHandler) UpdateAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	var req UpdateAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Enabled == nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	if err := h.Store.SetAlertRuleEnabled(r.Context(), id, *req.Enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "alert rule not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "alert.update", "alert_rule", strconv.FormatInt(id, 10), map[string]any{"enabled": *req.Enabled})
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) DeleteAlertRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	if err := h.Store.DeleteAlertRule(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "alert rule not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "alert.delete", "alert_rule", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *AdminHandler) ListAlertEvents(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v, _ := strconv.Atoi(r.URL.Query().Get("limit")); v > 0 {
		limit = v
	}
	rows, err := h.Store.ListAlertEvents(r.Context(), limit)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

type CreateAlertRuleRequest struct {
	Name             string         `json:"name"`
	ScopeType        string         `json:"scope_type"`
	ScopeID          *int64         `json:"scope_id"`
	TriggerType      string         `json:"trigger_type"`
	ThresholdOptions map[string]any `json:"threshold_options"`
	ChannelType      string         `json:"channel_type"`
	ChannelTarget    string         `json:"channel_target"`
	CooldownSeconds  int            `json:"cooldown_seconds"`
}

func (h *AdminHandler) CreateAlertRule(w http.ResponseWriter, r *http.Request) {
	var req CreateAlertRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.TriggerType != "" && !alerts.Triggers[req.TriggerType] {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "trigger_type must be budget_threshold, budget_exceeded, error_rate, latency_p95 or provider_unavailable")
		return
	}
	if req.ChannelType != "" && !alerts.Channels[req.ChannelType] {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "channel_type must be slack or webhook")
		return
	}
	if req.Name == "" || req.ScopeType == "" || req.TriggerType == "" || req.ChannelType == "" || req.ChannelTarget == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"name, scope_type, trigger_type, channel_type, and channel_target are required")
		return
	}
	rule, err := h.Store.CreateAlertRule(r.Context(), store.CreateAlertRuleParams{
		Name: req.Name, ScopeType: req.ScopeType, ScopeID: req.ScopeID,
		TriggerType: req.TriggerType, ThresholdOptions: req.ThresholdOptions,
		ChannelType: req.ChannelType, ChannelTarget: req.ChannelTarget,
		CooldownSeconds: req.CooldownSeconds,
	})
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "alert.create", "alert_rule", rule.Name, map[string]any{
		"trigger_type": rule.TriggerType,
		"channel":      rule.ChannelType,
	})
	writeJSON(w, http.StatusCreated, alertRuleToResponse(rule))
}
