package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func (h *V1Handler) GetResponse(w http.ResponseWriter, r *http.Request) {
	h.responseResource(w, r, false)
}
func (h *V1Handler) DeleteResponse(w http.ResponseWriter, r *http.Request) {
	h.responseResource(w, r, false)
}
func (h *V1Handler) ResponseInputItems(w http.ResponseWriter, r *http.Request) {
	h.responseResource(w, r, true)
}

func (h *V1Handler) responseResource(w http.ResponseWriter, r *http.Request, inputItems bool) {
	binding, err := h.loadResponseBinding(r, chi.URLParam(r, "responseID"))
	if err != nil {
		h.responseStateError(w, err)
		return
	}
	team, key := auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context())
	if !h.modelAllowed(team, key, binding.Alias) {
		writeJSONError(w, 403, "model_not_allowed", "model not allowed")
		return
	}
	// A key is the access-control boundary; customer attribution remains sticky.
	if supplied := extractCustomerID(r, ""); supplied != "" && supplied != binding.CustomerExternalID {
		writeJSONError(w, 404, "not_found", "response not found")
		return
	}
	if !upstreamResponseIDPattern.MatchString(binding.UpstreamID) {
		writeJSONError(w, 409, "response_not_ready", "response metadata is not ready")
		return
	}
	if r.URL.Query().Get("stream") == "true" {
		writeJSONError(w, 400, "unsupported_operation", "resuming stored streams is not supported; retrieve JSON or create a new streaming response")
		return
	}
	*r = *r.WithContext(context.WithValue(r.Context(), accountingNonBillableKey{}, true))
	release, admitted := h.admitRequestConcurrency(w, r, binding.Alias, binding.CustomerExternalID)
	if !admitted {
		return
	}
	defer release()
	auditWriter := &accountingResponseWriter{ResponseWriter: w}
	w = auditWriter
	defer func() {
		if run := requestAccounting(r.Context()); run != nil && !run.complete {
			entry := run.entry
			entry.StatusCode = auditWriter.status
			if entry.StatusCode == 0 {
				entry.StatusCode = 503
			}
			if entry.StatusCode >= 400 {
				entry.Error = "stored_response_operation_failed"
			}
			_, _ = h.persistUsage(r.Context(), entry)
		}
	}()
	if !h.enforceRateLimit(w, r, team, key, binding.Alias, middleware.GetReqID(r.Context()), 0) {
		return
	}
	target, err := h.responseBoundTarget(binding)
	if err != nil {
		writeJSONError(w, 503, "response_deployment_unavailable", err.Error())
		return
	}
	provider, ok := target.Provider.(providers.ResponsesProvider)
	if !ok {
		writeJSONError(w, 503, "response_deployment_unavailable", "deployment does not support Responses")
		return
	}
	permit, admitted, err := h.tryAcquireUpstream(r.Context(), target)
	if err != nil || !admitted {
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, 503, "server_overloaded", "response deployment capacity unavailable")
		return
	}
	defer h.releaseUpstreamPermit(permit)
	path := "/responses/" + url.PathEscape(binding.UpstreamID)
	query := url.Values{}
	if inputItems {
		path += "/input_items"
		for _, name := range []string{"after", "before", "order", "limit"} {
			if value := r.URL.Query().Get(name); value != "" {
				if len(value) > 256 {
					writeJSONError(w, 400, "invalid_request", "query value too long")
					return
				}
				if name == "limit" {
					n, err := strconv.Atoi(value)
					if err != nil || n < 1 || n > 100 {
						writeJSONError(w, 400, "invalid_request", "limit must be 1–100")
						return
					}
				}
				if name == "order" && value != "asc" && value != "desc" {
					writeJSONError(w, 400, "invalid_request", "order must be asc or desc")
					return
				}
				query.Set(name, value)
			}
		}
	}
	for _, include := range r.URL.Query()["include[]"] {
		if len(include) > 128 {
			writeJSONError(w, 400, "invalid_request", "include value too long")
			return
		}
		query.Add("include[]", include)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	source := newChatStream(r.Context(), h.streamingFor(target.DeploymentName))
	defer source.Close()
	response, err := provider.ResponseRequest(source.ctx, r.Method, path, nil)
	if response != nil {
		source.attach(response.Body)
	}
	if err != nil {
		writeJSONError(w, 502, "upstream_error", "response lookup failed")
		return
	}
	if response.StatusCode >= 300 {
		_ = providers.ReadErrorBody(response.Body)
		status := response.StatusCode
		if status >= 500 {
			status = 502
		}
		writeJSONError(w, status, "upstream_error", "stored response operation failed")
		return
	}
	body, err := readResponseJSON(source)
	if err != nil {
		writeJSONError(w, 502, "upstream_error", "invalid stored response")
		return
	}
	if r.Method == http.MethodDelete {
		var result struct {
			Deleted bool `json:"deleted"`
		}
		if response.StatusCode == http.StatusNoContent {
			result.Deleted = true
		} else {
			_ = json.Unmarshal(body, &result)
		}
		if !result.Deleted {
			writeJSONError(w, 502, "upstream_error", "upstream did not confirm deletion")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), settlementTimeout)
		defer cancel()
		if err := h.responseStore().DeleteResponseBinding(ctx, binding.TeamID, binding.Owner, binding.ID); err != nil {
			h.responseStateError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": binding.ID, "object": "response", "deleted": true})
		return
	}
	if !inputItems {
		body, _, err = rewriteResponse(body, binding)
		if err != nil {
			writeJSONError(w, 502, "upstream_error", "invalid stored response")
			return
		}
	} else if !json.Valid(body) || !strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		writeJSONError(w, 502, "upstream_error", fmt.Sprint("invalid input item list"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	writer := newStreamWriter(r.Context(), w, source.config.WriteTimeout)
	defer writer.close()
	_ = writer.write(string(body))
}
