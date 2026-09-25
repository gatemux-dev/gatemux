package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// Moderation, Rerank, Images, and the Anthropic Messages passthrough all
// share a "find a deployment with the right capability and forward the
// HTTP request body" shape. They do not require provider-shape transforms
// today — the canonical client uses provider-native bodies and the same
// upstream model name.
//
// Auth, allowlists and concurrency admission apply. Token accounting for these
// native modalities remains separate from the chat/embedding accounting path.

type passthroughCapability string

const (
	capModeration passthroughCapability = "moderation"
	capRerank     passthroughCapability = "rerank"
	capImages     passthroughCapability = "images"
	capMessages   passthroughCapability = "messages_passthrough"
)

func (h *V1Handler) Moderations(w http.ResponseWriter, r *http.Request) {
	h.passthrough(w, r, capModeration, "/v1/moderations")
}

func (h *V1Handler) Rerank(w http.ResponseWriter, r *http.Request) {
	h.passthrough(w, r, capRerank, "/v2/rerank")
}

func (h *V1Handler) ImagesGenerations(w http.ResponseWriter, r *http.Request) {
	h.passthrough(w, r, capImages, "/v1/images/generations")
}

func (h *V1Handler) Messages(w http.ResponseWriter, r *http.Request) {
	h.passthrough(w, r, capMessages, "/v1/messages")
}

// passthrough finds a deployment that advertises the requested capability,
// proxies the request body to it (substituting the deployment's auth
// header), and writes bounded native SSE, JSON or binary responses back.
func (h *V1Handler) passthrough(w http.ResponseWriter, r *http.Request, cap passthroughCapability, upstreamPath string) {
	started := time.Now()
	team := auth.TeamFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var bodyMap map[string]json.RawMessage
	if len(body) > 0 {
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid_request", "request body must be JSON")
			return
		}
	}
	model := ""
	if bodyMap != nil {
		_ = json.Unmarshal(bodyMap["model"], &model)
	}
	if model == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}

	if !h.modelAllowed(team, auth.VirtualKeyFromContext(r.Context()), model) {
		writeJSONError(w, http.StatusForbidden, "model_not_allowed", "model not allowed for this api key: "+model)
		return
	}
	var bodyUser string
	_ = json.Unmarshal(bodyMap["user"], &bodyUser)
	releaseModel, admitted := h.admitRequestConcurrency(w, r, model, bodyUser)
	if !admitted {
		return
	}
	defer releaseModel()
	if !h.admitUnpricedCustomer(w, r, model) {
		return
	}
	resolved, permit, err := h.resolveForCapability(r.Context(), model, cap)
	if err != nil {
		writeCapabilityRoutingError(w, err)
		return
	}
	defer h.releaseUpstreamPermit(permit)

	upstreamURL, apiKey, err := upstreamFor(resolved, upstreamPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if bodyMap != nil {
		bodyMap["model"], _ = json.Marshal(resolved.UpstreamModel)
		body, _ = json.Marshal(bodyMap)
	}

	upReq, err := http.NewRequestWithContext(r.Context(), "POST", upstreamURL, bytes.NewReader(body))
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Accept", "application/json")
	if apiKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if cap == capMessages {
		upReq.Header.Del("Authorization")
		upReq.Header.Set("X-Api-Key", apiKey)
		version := r.Header.Get("Anthropic-Version")
		if version == "" {
			version = "2023-06-01"
		}
		upReq.Header.Set("Anthropic-Version", version)
		if beta := r.Header.Get("Anthropic-Beta"); beta != "" {
			upReq.Header.Set("Anthropic-Beta", beta)
		}
		upReq.Header.Set("Accept", "application/json, text/event-stream")
	}
	status, err := h.forwardProxy(w, r, upReq, h.streamingFor(resolved.DeploymentName), cap == capMessages)
	h.recordNativeAudit(r, resolved, model, started, status)
	abortIncompleteProxy(err)
}

func (h *V1Handler) resolveForCapability(ctx context.Context, alias string, cap passthroughCapability) (*router.Resolved, *upstreamPermit, error) {
	if h.Router == nil {
		return nil, nil, fmt.Errorf("router not initialized")
	}
	tried := map[string]bool{}
	saturated := false
	capabilityMiss := false
	var capacityErr error
	for {
		resolved, err := h.Router.ResolveNext(alias, tried)
		if err != nil {
			if capacityErr != nil {
				return nil, nil, capacityErr
			}
			if saturated {
				return nil, nil, router.ErrDeploymentSaturated
			}
			if capabilityMiss {
				return nil, nil, fmt.Errorf("no deployment supports %s for model %q", cap, alias)
			}
			return nil, nil, err
		}
		tried[resolved.DeploymentName] = true
		dep := h.Router.Deployment(resolved.DeploymentName)
		if dep == nil || dep.Capabilities == nil || !capabilityMatches(dep.Capabilities, cap) {
			capabilityMiss = true
			continue
		}
		permit, admitted, err := h.tryAcquireUpstream(ctx, resolved)
		if err != nil {
			if canFallbackConcurrency(err) {
				capacityErr = err
				continue
			}
			return nil, nil, err
		}
		if !admitted {
			saturated = true
			continue
		}
		return resolved, permit, nil
	}
}

func capabilityMatches(c *store.DeploymentCapabilities, cap passthroughCapability) bool {
	switch cap {
	case capModeration:
		return c.Moderation
	case capRerank:
		return c.Rerank
	case capImages:
		return c.Images
	case capMessages:
		return c.MessagesPassthrough
	case capAudioTranscribe:
		return c.AudioTranscribe
	case capAudioSpeech:
		return c.AudioSpeech
	}
	return false
}

func upstreamFor(r *router.Resolved, path string) (string, string, error) {
	base := r.BaseURL
	if base == "" {
		return "", "", fmt.Errorf("deployment %q has no base_url for passthrough", r.DeploymentName)
	}
	base = strings.TrimRight(base, "/")
	// OpenAI-compatible base URLs conventionally already include /v1.
	if strings.HasSuffix(base, "/v1") && strings.HasPrefix(path, "/v1/") {
		path = strings.TrimPrefix(path, "/v1")
	}
	return base + path, r.APIKey, nil
}

// shouldHopByHop matches headers we shouldn't forward verbatim.
func shouldHopByHop(h string) bool {
	switch strings.ToLower(h) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "trailers", "transfer-encoding", "upgrade", "content-length":
		return true
	}
	return false
}
