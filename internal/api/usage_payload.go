package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// PayloadResponse is the JSON shape returned by /admin/usage/{id}/payload.
// Both bodies are surfaced as raw JSON so the UI can pretty-print without
// double-decoding.
type PayloadResponse struct {
	UsageID      int64           `json:"usage_id"`
	RequestBody  json.RawMessage `json:"request_body,omitempty"`
	ResponseBody json.RawMessage `json:"response_body,omitempty"`
	CapturedAt   time.Time       `json:"captured_at"`
}

// GetUsagePayload fetches the captured request/response body for a single
// usage_log row. Returns 404 if capture was off for that team or no row
// was inserted (legacy data). Admin-only.
func (h *AdminHandler) GetUsagePayload(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	req, resp, capturedAt, err := h.Store.GetUsagePayload(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no payload captured for this usage row")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, PayloadResponse{
		UsageID:      id,
		RequestBody:  req,
		ResponseBody: resp,
		CapturedAt:   capturedAt,
	})
}

// UpdateTeamCapturePayloadsRequest carries the on/off toggle.
type UpdateTeamCapturePayloadsRequest struct {
	CapturePayloads bool `json:"capture_payloads"`
}

// UpdateTeamCapturePayloads turns body capture on or off for one team.
// Admin-only — has clear privacy implications, so we don't let team
// managers flip it on themselves.
func (h *AdminHandler) UpdateTeamCapturePayloads(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	var req UpdateTeamCapturePayloadsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.Store.SetTeamCapturePayloads(r.Context(), slug, req.CapturePayloads); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "team not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "team.capture_payloads", "team", slug, map[string]any{"enabled": req.CapturePayloads})
	writeJSON(w, http.StatusOK, req)
}
