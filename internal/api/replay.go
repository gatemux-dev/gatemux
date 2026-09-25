package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// ReplayResponse is what the UI gets back from a replay run. Mirrors the
// shape an /v1 client would have seen, plus the trace/usage hooks the
// debug view needs.
type ReplayResponse struct {
	OriginalUsageID int64           `json:"original_usage_id"`
	NewUsageID      int64           `json:"new_usage_id,omitempty"`
	StatusCode      int             `json:"status_code"`
	LatencyMs       int             `json:"latency_ms"`
	RequestID       string          `json:"request_id,omitempty"`
	ResponseBody    json.RawMessage `json:"response_body,omitempty"`
	EndpointKind    string          `json:"endpoint_kind"` // "chat" or "embeddings"
	Replayed        bool            `json:"replayed"`
	Reason          string          `json:"reason,omitempty"` // populated when Replayed=false
}

// ReplayUsage re-runs a captured /v1 request using the same team/key/user
// context the original ran under. Admin-only. The request body must
// already exist in usage_log_payloads (i.e. the team had capture enabled
// and retention hasn't pruned it yet).
//
// Replay goes through the normal router/budget/rate-limit pipeline so
// operators see what *would* happen now — not what happened then. We
// don't bypass admission; the goal is to answer "is this still failing?"
func (h *AdminHandler) ReplayUsage(w http.ResponseWriter, r *http.Request) {
	if h.V1 == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "internal_error", "replay handler not wired")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}

	teamID, keyID, userID, _, err := h.Store.GetUsageByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "usage row not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	reqBody, _, _, err := h.Store.GetUsagePayload(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, ReplayResponse{
				OriginalUsageID: id,
				Replayed:        false,
				Reason:          "no captured request body — enable capture_payloads on the team and try a new request",
			})
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	team, err := h.Store.GetTeamByID(r.Context(), teamID)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "team lookup failed: "+err.Error())
		return
	}

	// Detect endpoint shape from the captured body. Chat completions has
	// a top-level `messages` array; embeddings has a top-level `input`.
	kind, target := classifyReplayPayload(reqBody)
	if kind == "" {
		writeJSON(w, http.StatusOK, ReplayResponse{
			OriginalUsageID: id,
			Replayed:        false,
			Reason:          "unsupported endpoint shape — replay supports chat completions and embeddings",
		})
		return
	}

	// Resolve the original key (or fall back to the most recent active
	// key on the team) so rate limit / budget signals match production.
	var vk *store.VirtualKey
	if keyID != nil {
		v, _, kerr := h.Store.GetVirtualKeyByID(r.Context(), *keyID)
		if kerr == nil && v.RevokedAt == nil {
			vk = v
		}
	}
	if vk == nil {
		// Fall back: pick the most recent non-revoked key on the team.
		keys, _, lerr := h.Store.ListKeysForTeam(r.Context(), team.ID, 50, 0)
		if lerr == nil {
			for _, k := range keys {
				if k.RevokedAt == nil {
					vk = k
					break
				}
			}
		}
	}
	if vk == nil {
		writeJSON(w, http.StatusOK, ReplayResponse{
			OriginalUsageID: id,
			EndpointKind:    kind,
			Replayed:        false,
			Reason:          "no usable virtual key on team for replay",
		})
		return
	}

	var owner *store.User
	if userID != nil {
		if u, uerr := h.Store.GetUserByID(r.Context(), *userID); uerr == nil {
			owner = u
		}
	}

	rec := httptest.NewRecorder()
	inner, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, target, io.NopCloser(bytes.NewReader(reqBody)))
	inner.Header.Set("Content-Type", "application/json")
	// Mark as a replay so downstream callbacks / sinks can filter if they want.
	inner.Header.Set("X-Gatemux-Replay", "1")

	ctx := inner.Context()
	ctx = auth.WithTeam(ctx, team)
	ctx = auth.WithVirtualKey(ctx, vk)
	if owner != nil {
		ctx = auth.WithOwnerUser(ctx, owner)
	}
	// Stamp a chi RequestID so the V1 handlers' logs/usage rows line up.
	ctx = context.WithValue(ctx, chimw.RequestIDKey, "replay-"+strconv.FormatInt(id, 10)+"-"+strconv.FormatInt(time.Now().UnixNano(), 10))
	inner = inner.WithContext(ctx)

	started := time.Now()
	switch kind {
	case "chat":
		h.V1.ChatCompletions(rec, inner)
	case "embeddings":
		h.V1.Embeddings(rec, inner)
	}
	elapsed := time.Since(started).Milliseconds()

	resp := ReplayResponse{
		OriginalUsageID: id,
		StatusCode:      rec.Code,
		LatencyMs:       int(elapsed),
		RequestID:       rec.Header().Get("X-Request-Id"),
		ResponseBody:    json.RawMessage(rec.Body.Bytes()),
		EndpointKind:    kind,
		Replayed:        true,
	}
	writeJSON(w, http.StatusOK, resp)
}

// classifyReplayPayload returns ("chat", "/v1/chat/completions"),
// ("embeddings", "/v1/embeddings"), or ("", "") for unsupported shapes.
func classifyReplayPayload(body []byte) (string, string) {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(body, &probe); err != nil {
		return "", ""
	}
	if _, ok := probe["messages"]; ok {
		return "chat", "/v1/chat/completions"
	}
	if _, ok := probe["input"]; ok {
		return "embeddings", "/v1/embeddings"
	}
	return "", ""
}
