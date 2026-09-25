package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/go-chi/chi/v5"
)

// ConnectionTestResponse reports one live round trip to a deployment's
// upstream. Message is a short classification, never provider body text.
type ConnectionTestResponse struct {
	OK        bool   `json:"ok"`
	Operation string `json:"operation"`
	LatencyMs int64  `json:"latency_ms"`
	Message   string `json:"message"`
}

// TestDeploymentConnection sends the smallest possible real request to a
// deployment — a one-token chat completion, or a one-word embedding for
// embeddings-only deployments — so an administrator can confirm the
// credential, base URL and upstream model before routing traffic to it.
// The call bypasses routing, budgets and accounting and is not logged as
// usage; it may incur a negligible upstream charge.
func (h *AdminHandler) TestDeploymentConnection(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if h.Registry == nil || h.Registry.Deployment(name) == nil {
		writeJSONError(w, http.StatusNotFound, "not_found", "deployment not found")
		return
	}
	dep := h.Registry.Deployment(name)
	prov := h.Registry.Provider(name)
	if prov == nil {
		writeJSON(w, http.StatusOK, ConnectionTestResponse{OK: false, Operation: "setup",
			Message: "The deployment is not ready. Check that its credential environment variable is set on the gateway."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	caps := prov.Capabilities()
	resp := ConnectionTestResponse{}
	started := time.Now()
	var err error
	if dep.Capabilities != nil && !dep.Capabilities.Chat && dep.Capabilities.Embeddings || !caps.Chat && caps.Embeddings {
		resp.Operation = "embeddings"
		var req providers.EmbeddingRequest
		body, _ := json.Marshal(map[string]any{"model": dep.UpstreamModel, "input": "ping"})
		if err = json.Unmarshal(body, &req); err == nil {
			_, err = prov.Embeddings(ctx, &req)
		}
	} else {
		resp.Operation = "chat"
		var req providers.ChatRequest
		body, _ := json.Marshal(map[string]any{
			"model":      dep.UpstreamModel,
			"messages":   []map[string]string{{"role": "user", "content": "ping"}},
			"max_tokens": 1,
		})
		if err = json.Unmarshal(body, &req); err == nil {
			_, err = prov.ChatCompletion(ctx, &req)
		}
	}
	resp.LatencyMs = time.Since(started).Milliseconds()
	if err != nil {
		resp.Message = classifyConnectionError(ctx, err)
	} else {
		resp.OK = true
		resp.Message = "The upstream accepted a request for " + dep.UpstreamModel + "."
	}
	h.audit(r, "deployment.test", "deployment", name, map[string]any{"ok": resp.OK, "operation": resp.Operation})
	writeJSON(w, http.StatusOK, resp)
}

// classifyConnectionError turns an upstream failure into guidance without
// echoing provider response bodies, which can contain account details.
func classifyConnectionError(ctx context.Context, err error) string {
	if ctx.Err() != nil {
		return "The upstream did not respond within 15 seconds. Check the base URL and network access from the gateway."
	}
	var pe *providers.UpstreamError
	if errors.As(err, &pe) && pe.StatusCode > 0 {
		switch {
		case pe.StatusCode == http.StatusUnauthorized || pe.StatusCode == http.StatusForbidden:
			return "The upstream rejected the credential. Check the API key in the credential environment variable."
		case pe.StatusCode == http.StatusNotFound:
			return "The upstream does not recognise this model or endpoint. Check the upstream model name and base URL."
		case pe.StatusCode == http.StatusTooManyRequests:
			return "The upstream is rate limiting this credential. The connection works, but capacity is exhausted right now."
		case pe.StatusCode >= 500:
			return "The upstream returned a server error. The connection works; the provider may be degraded."
		case pe.StatusCode >= 400:
			return "The upstream rejected the test request. The credential works, but the model may not support this operation."
		}
	}
	return "Could not reach the upstream. Check the base URL and that the gateway can connect to it."
}
