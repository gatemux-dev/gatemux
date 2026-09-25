package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/budget"
	"github.com/gatemux-dev/gatemux/internal/cache"
	"github.com/gatemux-dev/gatemux/internal/callbacks"
	"github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/ratelimit"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/gatemux-dev/gatemux/internal/telemetry"
	"github.com/gatemux-dev/gatemux/internal/usage"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const nonStreamAttemptTimeout = 45 * time.Second

type V1Handler struct {
	Logger      *slog.Logger
	Router      *router.Registry
	Usage       *usage.Logger
	RateLimit   *ratelimit.Limiter
	Budget      *budget.Service
	Telemetry   *telemetry.Collector
	Cache       *cache.Cache
	Callbacks   *callbacks.Bus
	Concurrency *concurrency.Limiter
	Streaming   config.StreamingConfig
	ptClient    *http.Client
}

type modelsResponse struct {
	Object string      `json:"object"`
	Data   []modelInfo `json:"data"`
}

type modelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

func (h *V1Handler) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	team := auth.TeamFromContext(r.Context())
	vk := auth.VirtualKeyFromContext(r.Context())
	ownerUser := auth.OwnerUserFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not read request body: "+err.Error())
		return
	}
	var req providers.ChatRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not parse request body: "+err.Error())
		return
	}
	if req.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "messages must be non-empty")
		return
	}

	requestedAlias := req.Model
	requestID := middleware.GetReqID(r.Context())
	if !h.modelAllowed(team, vk, requestedAlias) {
		h.recordDenial(r.Context(), team, vk, requestedAlias, requestID, http.StatusForbidden, "model_not_allowed")
		writeJSONError(w, http.StatusForbidden, "model_not_allowed", "model not allowed for this api key: "+requestedAlias)
		return
	}
	releaseModel, admitted := h.admitRequestConcurrency(w, r, requestedAlias, req.User)
	if !admitted {
		return
	}
	defer releaseModel()
	requestID = middleware.GetReqID(r.Context())
	if requestGuardrail(r.Context()) != nil {
		var allowed bool
		rawBody, allowed = h.guardrailPre(w, r, rawBody)
		if !allowed {
			return
		}
		if err = json.Unmarshal(rawBody, &req); err != nil {
			writeJSONError(w, 503, "guardrail_unavailable", "could not apply guarded request")
			return
		}
	}
	if !h.admitCustomerPricing(w, r, requestedAlias, rawBody) {
		return
	}
	promptTokens, completionTokens := ratelimit.EstimateChatUsage(&req)
	if c := requestCustomer(r.Context()); hasKeyBudget(r) || c != nil && (c.UsdLimitCents != nil || c.TPM != nil && *c.TPM > 0) {
		completionTokens, err = req.BoundCustomerCompletion()
		if err != nil {
			writeJSONError(w, 400, "invalid_request", err.Error())
			return
		}
		// Include tool schemas and multimodal envelopes in the approximate input
		// estimate. Exact billing still uses authoritative upstream token usage.
		promptTokens = max(promptTokens, (len(rawBody)+3)/4)
	}
	if !h.enforceRateLimit(w, r, team, vk, requestedAlias, requestID, promptTokens+completionTokens) {
		return
	}

	// Cache lookup runs after auth/rate-limit so cache hits still respect
	// per-key rate limits, but before budget admission so a cached request
	// doesn't reserve budget it won't spend.
	cacheBypass := r.Header.Get("X-Gatemux-No-Cache") == "1" || r.Header.Get("X-Aiport-No-Cache") == "1"
	cacheOn, ttl, cacheKey := h.cacheParams(team.ID, requestedAlias, rawBody)
	// Protected requests never read/write a cache populated under older policy.
	if requestGuardrail(r.Context()) != nil {
		cacheOn = false
	}
	if cacheOn && !cacheBypass && !req.Stream {
		if entry, err := h.Cache.Get(r.Context(), cacheKey); err == nil {
			if h.Telemetry != nil {
				h.Telemetry.RecordCacheLookup(requestedAlias, "hit")
			}
			h.replayCachedChat(w, r, entry, team, ownerUser, vk, requestedAlias, requestID, started)
			return
		} else if errors.Is(err, cache.ErrMiss) {
			if h.Telemetry != nil {
				h.Telemetry.RecordCacheLookup(requestedAlias, "miss")
			}
		} else if h.Logger != nil {
			h.Logger.Warn("cache lookup failed", "alias", requestedAlias, "err", err)
			if h.Telemetry != nil {
				h.Telemetry.RecordCacheLookup(requestedAlias, "error")
			}
		}
	} else if cacheOn && cacheBypass && h.Telemetry != nil {
		h.Telemetry.RecordCacheLookup(requestedAlias, "bypass")
	}

	if !h.enforceBudget(w, r, team, ownerUser, requestedAlias, requestID, promptTokens, completionTokens, providers.CapabilityChat) {
		return
	}
	defer h.reconcileBudgetOnPanic(requestID)

	rc := resolveContextFromRequest(r)
	r = r.WithContext(withCaptureBody(r.Context(), rawBody))
	if req.Stream {
		h.streamChat(w, r, &req, team, ownerUser, vk, requestedAlias, requestID, started, rc)
		return
	}

	queueEnd := time.Now()
	resolved, resp, err := h.runChatWithFallback(r.Context(), requestedAlias, &req, rc)
	upstreamEnd := time.Now()
	if err != nil {
		h.writeRoutingError(w, r, team, ownerUser, vk, nil, requestedAlias, requestID, started, err)
		return
	}

	upstreamModel := resp.Model
	usageUnknown := !resp.UsageReported()
	resp.Model = requestedAlias
	if g := requestGuardrail(r.Context()); g != nil {
		body, guardErr := guardedResponse(g, resp)
		if guardErr != nil {
			failure := guardrailFailure(guardErr)
			h.recordUsage(r.Context(), team, ownerUser, vk, resolved, requestedAlias, requestID, upstreamModel, resp.Usage, started, failure.status, failure.code, false, "", rc.Tags, Timings{UnknownUsage: usageUnknown})
			writeJSONError(w, failure.status, failure.code, "guardrail rejected upstream response")
			return
		}
		if err = json.Unmarshal(body, resp); err != nil {
			writeJSONError(w, 503, "guardrail_unavailable", "could not apply guarded response")
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
	postEnd := time.Now()
	respBytes, _ := json.Marshal(resp)
	timings := Timings{
		UnknownUsage:     usageUnknown,
		QueueMs:          int(queueEnd.Sub(started) / time.Millisecond),
		UpstreamMs:       int(upstreamEnd.Sub(queueEnd) / time.Millisecond),
		PostprocessMs:    int(postEnd.Sub(upstreamEnd) / time.Millisecond),
		CapturedRequest:  rawBody,
		CapturedResponse: respBytes,
		ClientIP:         extractClientIP(r),
	}
	h.recordUsage(r.Context(), team, ownerUser, vk, resolved, requestedAlias, requestID, upstreamModel, resp.Usage, started, http.StatusOK, "", false, "", rc.Tags, timings)

	if cacheOn && !cacheBypass && cacheable(resp) {
		bodyBytes, _ := json.Marshal(resp)
		_ = h.Cache.Set(r.Context(), cacheKey, &cache.Entry{
			StatusCode:       http.StatusOK,
			Body:             bodyBytes,
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			UpstreamModel:    upstreamModel,
			OriginRequestID:  requestID,
			OriginDeployment: resolved.DeploymentName,
			CachedAt:         time.Now(),
		}, ttl)
	}
}

// resolveContextFromRequest extracts smart-routing signal data from
// request headers. X-Gatemux-Tags is comma-separated; X-Gatemux-Region is
// a free-form string compared against the deployment's region column.
// Returns the zero ResolveContext when no signals are set, in which case
// the alias's strategy still applies (e.g. cost-based ordering).
func resolveContextFromRequest(r *http.Request) router.ResolveContext {
	rc := router.ResolveContext{}
	if v := strings.TrimSpace(gatewayHeader(r, "Tags")); v != "" {
		for _, t := range strings.Split(v, ",") {
			if s := strings.TrimSpace(t); s != "" {
				rc.Tags = append(rc.Tags, s)
			}
		}
	}
	if v := strings.TrimSpace(gatewayHeader(r, "Region")); v != "" {
		rc.Region = v
	}
	return rc
}

// extractClientIP delegates to auth.ClientIP, which honors the
// trusted-proxy CIDR list configured in server.config.server.trusted_proxies.
// Kept as a thin wrapper so the call site in ChatCompletions reads cleanly.
func extractClientIP(r *http.Request) string { return auth.ClientIP(r) }

// extractCustomerID returns the customer external_id from request headers
// (or the OpenAI-shape `user` body field as a fallback). Empty string
// means "no customer dimension" — admission and logging treat that the
// same as today's pre-customer behavior.
func extractCustomerID(r *http.Request, bodyUser string) string {
	if v := gatewayHeader(r, "Customer-Id"); v != "" {
		return v
	}
	if v := r.Header.Get("X-Customer-Id"); v != "" {
		return v
	}
	return bodyUser
}

// cacheParams returns whether caching is enabled for the alias, the TTL,
// and the deterministic cache key. Falls back to (false, 0, "") if caching
// is unconfigured or the cache package is the no-op nil.
func (h *V1Handler) cacheParams(teamID int64, alias string, body []byte) (bool, time.Duration, string) {
	if h.Cache == nil || h.Router == nil {
		return false, 0, ""
	}
	meta, ok := h.Router.Meta(alias)
	if !ok || !meta.CacheEnabled || meta.CacheTTLSeconds <= 0 {
		return false, 0, ""
	}
	key, err := h.Cache.Key(teamID, alias, body)
	if err != nil {
		return false, 0, ""
	}
	return true, time.Duration(meta.CacheTTLSeconds) * time.Second, key
}

// cacheable returns true when the response is safe to cache. Tool-calling
// outputs are skipped because the caller may want a fresh tool call each
// time (the call has side effects when re-executed). Detected via
// finish_reason since ChatMessage doesn't surface tool_calls today.
func cacheable(resp *providers.ChatResponse) bool {
	if resp == nil || len(resp.Choices) == 0 {
		return false
	}
	for _, c := range resp.Choices {
		switch c.FinishReason {
		case "tool_calls", "function_call":
			return false
		}
	}
	return true
}

// replayCachedChat writes a cached response back to the client and records
// it in the usage log so spend dashboards reflect the saving.
func (h *V1Handler) replayCachedChat(
	w http.ResponseWriter,
	r *http.Request,
	entry *cache.Entry,
	team *store.Team,
	ownerUser *store.User,
	vk *store.VirtualKey,
	alias, requestID string,
	started time.Time,
) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Gatemux-Cache", fmt.Sprintf("hit; age=%ds; origin=%s", int(time.Since(entry.CachedAt).Seconds()), entry.OriginRequestID))
	w.Header().Set("X-Aiport-Cache", w.Header().Get("X-Gatemux-Cache")) // Legacy client alias.
	w.WriteHeader(entry.StatusCode)
	_, _ = w.Write(entry.Body)
	usage := providers.Usage{
		PromptTokens:     entry.PromptTokens,
		CompletionTokens: entry.CompletionTokens,
		TotalTokens:      entry.PromptTokens + entry.CompletionTokens,
	}
	resolved := &router.Resolved{DeploymentName: entry.OriginDeployment}
	h.recordUsage(r.Context(), team, ownerUser, vk, resolved, alias, requestID, entry.UpstreamModel, usage, started, http.StatusOK, "", true, entry.OriginRequestID, nil, Timings{})
}

func (h *V1Handler) Embeddings(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	team := auth.TeamFromContext(r.Context())
	vk := auth.VirtualKeyFromContext(r.Context())
	ownerUser := auth.OwnerUserFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}

	var req providers.EmbeddingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "could not parse request body: "+err.Error())
		return
	}
	if req.Model == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}
	if req.Input.Len() == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "input must be non-empty")
		return
	}

	requestedAlias := req.Model
	requestID := middleware.GetReqID(r.Context())
	if !h.modelAllowed(team, vk, requestedAlias) {
		h.recordDenial(r.Context(), team, vk, requestedAlias, requestID, http.StatusForbidden, "model_not_allowed")
		writeJSONError(w, http.StatusForbidden, "model_not_allowed", "model not allowed for this api key: "+requestedAlias)
		return
	}
	releaseModel, admitted := h.admitRequestConcurrency(w, r, requestedAlias, req.User)
	if !admitted {
		return
	}
	defer releaseModel()
	requestID = middleware.GetReqID(r.Context())
	promptTokens := ratelimit.EstimateEmbeddingTokens(&req)
	if !h.enforceRateLimit(w, r, team, vk, requestedAlias, requestID, promptTokens) {
		return
	}
	if !h.enforceBudget(w, r, team, ownerUser, requestedAlias, requestID, promptTokens, 0, providers.CapabilityEmbeddings) {
		return
	}
	defer h.reconcileBudgetOnPanic(requestID)

	rc := resolveContextFromRequest(r)
	resolved, resp, err := h.runEmbeddingsWithFallback(r.Context(), requestedAlias, &req, rc)
	if err != nil {
		h.writeRoutingError(w, r, team, ownerUser, vk, nil, requestedAlias, requestID, started, err)
		return
	}

	upstreamModel := resp.Model
	resp.Model = requestedAlias
	writeJSON(w, http.StatusOK, resp)
	h.recordUsage(r.Context(), team, ownerUser, vk, resolved, requestedAlias, requestID, upstreamModel, resp.Usage, started, http.StatusOK, "", false, "", rc.Tags, Timings{UnknownUsage: !resp.UsageReported()})
}

func (h *V1Handler) Models(w http.ResponseWriter, r *http.Request) {
	team := auth.TeamFromContext(r.Context())
	vk := auth.VirtualKeyFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}

	aliases := h.Router.Aliases()
	sort.Strings(aliases)

	out := make([]modelInfo, 0, len(aliases))
	for _, alias := range aliases {
		if !h.modelAllowed(team, vk, alias) {
			continue
		}
		if _, err := h.Router.Resolve(alias); err != nil {
			continue
		}
		out = append(out, modelInfo{
			ID:      alias,
			Object:  "model",
			Created: 0,
			OwnedBy: "gatemux",
		})
	}

	writeJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data:   out,
	})
}

func (h *V1Handler) modelAllowed(team *store.Team, vk *store.VirtualKey, alias string) bool {
	if team != nil && !team.AllowsModel(alias) {
		return false
	}
	if vk != nil && !vk.AllowsModel(alias) {
		return false
	}
	return true
}

func (h *V1Handler) enforceRateLimit(
	w http.ResponseWriter,
	r *http.Request,
	team *store.Team,
	vk *store.VirtualKey,
	alias string,
	requestID string,
	tokens int,
) bool {
	if h.RateLimit == nil {
		if c := requestCustomer(r.Context()); c != nil && (c.RPM != nil && *c.RPM > 0 || c.TPM != nil && *c.TPM > 0) {
			writeJSONError(w, 503, "rate_limit_unavailable", "customer rate policy requires a configured limiter")
			return false
		}
		return true
	}
	ctx, span := otel.Tracer("gatemux/admission").Start(r.Context(), "rate_limit_check")
	defer span.End()
	span.SetAttributes(
		attribute.String("team", team.Slug),
		attribute.Int("tokens", tokens),
	)
	result, err := h.RateLimit.Check(ctx, ratelimit.Check{
		Team:     team,
		Key:      vk,
		Customer: requestCustomer(ctx),
		Tokens:   tokens,
	})
	if err != nil {
		span.RecordError(err)
		h.Logger.Error("rate limit unavailable", "team", team.Slug, "key_id", keyID(vk), "err", err)
		writeJSONError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "rate limit check unavailable")
		return false
	}
	if result.Allowed {
		span.SetAttributes(attribute.String("outcome", "allowed"))
		return true
	}
	span.SetAttributes(
		attribute.String("outcome", "denied"),
		attribute.String("scope", result.Scope),
		attribute.String("metric", result.Metric),
	)
	if h.Telemetry != nil {
		h.Telemetry.RecordRateLimit(result.Scope, result.Metric)
	}
	retryAfter := int(result.RetryAfter.Seconds())
	if retryAfter < 1 {
		retryAfter = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	code := fmt.Sprintf("rate_limited:%s_%s", result.Scope, result.Metric)
	h.recordDenial(r.Context(), team, vk, alias, requestID, http.StatusTooManyRequests, code)
	writeJSONError(w, http.StatusTooManyRequests, "rate_limit_exceeded",
		fmt.Sprintf("%s %s limit exceeded; retry later", result.Scope, result.Metric))
	return false
}

// recordDenial writes a minimal usage_log row when admission denies a
// request before any upstream call. Lets the UI's "why was this request
// denied?" panel attach to a real row instead of disappearing into the
// rate-limit / budget void.
func (h *V1Handler) recordDenial(ctx context.Context, team *store.Team, vk *store.VirtualKey, alias, requestID string, statusCode int, errCode string) {
	if h.Usage == nil || team == nil {
		return
	}
	entry := store.UsageEntry{
		TeamID:     team.ID,
		Alias:      alias,
		RequestID:  requestID,
		StatusCode: statusCode,
		Error:      errCode,
		Ts:         time.Now().UTC(),
	}
	if vk != nil {
		if vk.ID > 0 {
			entry.KeyID = &vk.ID
		}
		entry.UserID = vk.UserID
		entry.ServiceAccountID = vk.ServiceAccountID
	}
	attributeCustomer(ctx, &entry)
	_, _ = h.persistUsage(ctx, entry)
}

func (h *V1Handler) enforceBudget(
	w http.ResponseWriter,
	r *http.Request,
	team *store.Team,
	user *store.User,
	alias, requestID string,
	promptTokens, completionTokens int,
	capability providers.Capability,
) bool {
	if h.Budget == nil || team == nil || alias == "" || requestID == "" {
		if c := requestCustomer(r.Context()); c != nil && c.UsdLimitCents != nil {
			writeJSONError(w, 503, "budget_unavailable", "customer budget policy requires accounting")
			return false
		}
		return true
	}
	ctx, span := otel.Tracer("gatemux/admission").Start(r.Context(), "budget_admit")
	defer span.End()
	ctx, cancelAdmission := context.WithTimeout(ctx, settlementTimeout)
	defer cancelAdmission()
	span.SetAttributes(
		attribute.String("team", team.Slug),
		attribute.String("alias", alias),
		attribute.Int("prompt_tokens", promptTokens),
		attribute.Int("completion_tokens", completionTokens),
	)
	targets, err := h.Router.TargetsFor(alias, capability)
	if err != nil {
		span.RecordError(err)
		return true
	}
	budgetTargets := make([]budget.Target, 0, len(targets))
	for _, target := range targets {
		budgetTargets = append(budgetTargets, budget.Target{
			ProviderType:  target.ProviderType,
			UpstreamModel: target.UpstreamModel,
		})
	}
	vk := auth.VirtualKeyFromContext(ctx)
	var sa *store.ServiceAccount
	if vk != nil && vk.ServiceAccountID != nil && h.Usage != nil {
		// Load SA so admission can gate on its budget; lookup is bounded
		// by the auth middleware's earlier query, so failure here is a
		// real DB hiccup not a missing row.
		loaded, err := h.Usage.Store().GetServiceAccountByID(ctx, *vk.ServiceAccountID)
		if err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "budget_unavailable", "service account budget lookup unavailable")
			return false
		}
		sa = loaded
	}
	_, err = h.Budget.Admit(ctx, budget.AdmissionRequest{
		RequestID:        requestID,
		Team:             team,
		User:             user,
		ServiceAccount:   sa,
		Key:              vk,
		Customer:         requestCustomer(ctx),
		Alias:            alias,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		Targets:          budgetTargets,
	})
	if err == nil {
		span.SetAttributes(attribute.String("outcome", "admitted"))
		return true
	}
	span.RecordError(err)
	var exceeded *budget.ExceededError
	if errors.As(err, &exceeded) {
		if h.Telemetry != nil {
			h.Telemetry.RecordBudgetDeny(exceeded.Scope)
		}
		span.SetAttributes(
			attribute.String("outcome", "exceeded"),
			attribute.String("scope", exceeded.Scope),
		)
		h.recordDenial(r.Context(), team, vk, alias, requestID, http.StatusForbidden, "budget_exceeded:"+exceeded.Scope)
		writeJSONError(w, http.StatusForbidden, "insufficient_quota", err.Error())
		return false
	}
	var pricingErr *budget.PricingUnavailableError
	if errors.As(err, &pricingErr) {
		h.Logger.Error("budget admission blocked by missing pricing", "alias", alias, "provider", pricingErr.ProviderType, "model", pricingErr.UpstreamModel)
		writeJSONError(w, http.StatusServiceUnavailable, "budget_unavailable", "pricing unavailable for budgeted request")
		return false
	}
	h.Logger.Error("budget admission failed", "alias", alias, "team", team.Slug, "err", err)
	writeJSONError(w, http.StatusServiceUnavailable, "budget_unavailable", "budget check unavailable")
	return false
}

func keyID(vk *store.VirtualKey) int64 {
	if vk == nil {
		return 0
	}
	return vk.ID
}

func (h *V1Handler) streamChat(
	w http.ResponseWriter,
	r *http.Request,
	req *providers.ChatRequest,
	team *store.Team,
	ownerUser *store.User,
	vk *store.VirtualKey,
	requestedAlias, requestID string,
	started time.Time,
	rc router.ResolveContext,
) {
	resolved, body, permit, err := h.openStreamWithFallback(r.Context(), requestedAlias, rc, func(ctx context.Context, target *router.Resolved) (io.ReadCloser, error) {
		upstream := *req
		upstream.Model = target.UpstreamModel
		upstream.Stream = true
		upstream.StreamOptions = &providers.StreamOptions{IncludeUsage: true}
		return target.Provider.ChatCompletionStream(ctx, &upstream)
	})
	if err != nil {
		h.writeRoutingError(w, r, team, ownerUser, vk, nil, requestedAlias, requestID, started, err)
		return
	}
	defer func() { h.releaseUpstreamPermit(permit) }()
	defer body.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	writer := newStreamWriter(r.Context(), w, body.config.WriteTimeout)
	defer writer.close()
	sp := &streamProxy{src: body, w: writer, alias: requestedAlias, guard: requestGuardrail(r.Context())}
	// Unguarded streams record the text the client received; guarded ones
	// record only the request, since chunks are seen here before redaction.
	sp.collect = sp.guard == nil && captureBody(r.Context()) != nil
	status := http.StatusOK
	errMsg := ""
	firstEventMs := int(time.Since(started).Milliseconds())
	runErr := sp.Run()
	if sp.guard != nil {
		runErr = sp.guard.finish(runErr)
	}
	_ = body.Close()
	h.releaseUpstreamPermit(permit)
	permit = nil // release before settlement; the defer remains panic-safe
	if runErr != nil {
		status = http.StatusBadGateway
		errType := "upstream_error"
		if errors.Is(runErr, context.DeadlineExceeded) {
			status, errType = http.StatusGatewayTimeout, "upstream_timeout"
		}
		errMsg = runErr.Error()
		var writeErr *streamWriteError
		if r.Context().Err() != nil || errors.As(runErr, &writeErr) {
			status = 499 // usage outcome only; HTTP status may already be 200
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
			}
		} else {
			var guardErr *guardrailError
			if errors.As(runErr, &guardErr) {
				status, errType = guardErr.status, guardErr.code
			} else {
				h.Router.RecordFailure(resolved.DeploymentName, runErr)
			}
			if !errors.Is(runErr, errUpstreamStreamReported) {
				payload, _ := json.Marshal(map[string]any{"error": map[string]string{"type": errType, "message": "stream interrupted before completion"}})
				_ = writer.write("data: " + string(payload) + "\n\n")
			}
		}
		if h.Logger != nil {
			h.Logger.Warn("stream proxy ended with error",
				"team", team.Slug, "alias", requestedAlias, "deployment", resolved.DeploymentName, "err", runErr)
		}
	} else {
		h.Router.RecordSuccess(resolved.DeploymentName)
	}

	var u providers.Usage
	if sp.usage != nil {
		u = *sp.usage
	}
	timings := Timings{TTFBMs: firstEventMs, UnknownUsage: sp.usage == nil, CapturedRequest: captureBody(r.Context()), ClientIP: extractClientIP(r)}
	if sp.collect {
		timings.CapturedResponse = sp.capturedResponse(requestedAlias, u)
	}
	h.recordUsage(r.Context(), team, ownerUser, vk, resolved, requestedAlias, requestID, sp.upstreamModel, u, started, status, errMsg, false, "", rc.Tags, timings)
}

func (h *V1Handler) runChatWithFallback(ctx context.Context, alias string, req *providers.ChatRequest, rc router.ResolveContext) (*router.Resolved, *providers.ChatResponse, error) {
	tried := map[string]bool{}
	var lastErr error
	saturated := false
	for {
		target, err := h.Router.ResolveWithContext(alias, tried, providers.CapabilityChat, rc)
		if err != nil {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			if saturated {
				return nil, nil, router.ErrDeploymentSaturated
			}
			return nil, nil, err
		}
		tried[target.DeploymentName] = true
		permit, admitted, capacityErr := h.tryAcquireUpstream(ctx, target)
		if capacityErr != nil {
			if canFallbackConcurrency(capacityErr) {
				lastErr = capacityErr
				continue
			}
			return nil, nil, capacityErr
		}
		if !admitted {
			saturated = true
			continue
		}

		attemptReq := *req
		attemptReq.Model = target.UpstreamModel
		attemptCtx, span := otel.Tracer("gatemux/router").Start(ctx, "chat_attempt")
		span.SetAttributes(
			attribute.String("alias", alias),
			attribute.String("deployment", target.DeploymentName),
			attribute.String("provider", target.ProviderType),
			attribute.String("upstream_model", target.UpstreamModel),
		)
		attemptCtx, cancel := context.WithTimeout(attemptCtx, nonStreamAttemptTimeout)
		attemptStart := time.Now()
		resp, callErr := func() (*providers.ChatResponse, error) {
			defer h.releaseUpstreamPermit(permit)
			defer cancel()
			defer func() {
				if cause := recover(); cause != nil {
					span.End()
					panic(cause)
				}
			}()
			return target.Provider.ChatCompletion(attemptCtx, &attemptReq)
		}()
		cancel()
		statusCode := 0
		if resp != nil {
			statusCode = http.StatusOK
		}
		if callErr != nil {
			span.RecordError(callErr)
			var upstream *providers.UpstreamError
			if errors.As(callErr, &upstream) {
				statusCode = upstream.StatusCode
			}
		}
		span.End()
		if h.Telemetry != nil {
			h.Telemetry.RecordUpstream(target.ProviderType, target.DeploymentName, statusCode, time.Since(attemptStart))
		}
		if callErr == nil {
			h.Router.RecordSuccess(target.DeploymentName)
			return target, resp, nil
		}
		h.Router.RecordFailure(target.DeploymentName, callErr)
		lastErr = callErr
		if !providers.IsRetryable(callErr) || ctx.Err() != nil {
			return target, nil, callErr
		}
	}
}

func (h *V1Handler) runEmbeddingsWithFallback(ctx context.Context, alias string, req *providers.EmbeddingRequest, rc router.ResolveContext) (*router.Resolved, *providers.EmbeddingResponse, error) {
	tried := map[string]bool{}
	var lastErr error
	saturated := false
	for {
		target, err := h.Router.ResolveWithContext(alias, tried, providers.CapabilityEmbeddings, rc)
		if err != nil {
			if lastErr != nil {
				return nil, nil, lastErr
			}
			if saturated {
				return nil, nil, router.ErrDeploymentSaturated
			}
			return nil, nil, err
		}
		tried[target.DeploymentName] = true
		permit, admitted, capacityErr := h.tryAcquireUpstream(ctx, target)
		if capacityErr != nil {
			if canFallbackConcurrency(capacityErr) {
				lastErr = capacityErr
				continue
			}
			return nil, nil, capacityErr
		}
		if !admitted {
			saturated = true
			continue
		}

		attemptReq := *req
		attemptReq.Model = target.UpstreamModel
		attemptCtx, span := otel.Tracer("gatemux/router").Start(ctx, "embedding_attempt")
		span.SetAttributes(
			attribute.String("alias", alias),
			attribute.String("deployment", target.DeploymentName),
			attribute.String("provider", target.ProviderType),
			attribute.String("upstream_model", target.UpstreamModel),
		)
		attemptCtx, cancel := context.WithTimeout(attemptCtx, nonStreamAttemptTimeout)
		attemptStart := time.Now()
		resp, callErr := func() (*providers.EmbeddingResponse, error) {
			defer h.releaseUpstreamPermit(permit)
			defer cancel()
			defer func() {
				if cause := recover(); cause != nil {
					span.End()
					panic(cause)
				}
			}()
			return target.Provider.Embeddings(attemptCtx, &attemptReq)
		}()
		cancel()
		statusCode := 0
		if resp != nil {
			statusCode = http.StatusOK
		}
		if callErr != nil {
			span.RecordError(callErr)
			var upstream *providers.UpstreamError
			if errors.As(callErr, &upstream) {
				statusCode = upstream.StatusCode
			}
		}
		span.End()
		if h.Telemetry != nil {
			h.Telemetry.RecordUpstream(target.ProviderType, target.DeploymentName, statusCode, time.Since(attemptStart))
		}
		if callErr == nil {
			h.Router.RecordSuccess(target.DeploymentName)
			return target, resp, nil
		}
		h.Router.RecordFailure(target.DeploymentName, callErr)
		lastErr = callErr
		if !providers.IsRetryable(callErr) || ctx.Err() != nil {
			return target, nil, callErr
		}
	}
}

func (h *V1Handler) openStreamWithFallback(ctx context.Context, alias string, rc router.ResolveContext, open func(context.Context, *router.Resolved) (io.ReadCloser, error)) (*router.Resolved, *chatStream, *upstreamPermit, error) {
	tried := map[string]bool{}
	var lastErr error
	saturated := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		target, err := h.Router.ResolveWithContext(alias, tried, providers.CapabilityStreamChat, rc)
		if err != nil {
			if lastErr != nil {
				return nil, nil, nil, lastErr
			}
			if saturated {
				return nil, nil, nil, router.ErrDeploymentSaturated
			}
			return nil, nil, nil, err
		}
		tried[target.DeploymentName] = true
		permit, admitted, capacityErr := h.tryAcquireUpstream(ctx, target)
		if capacityErr != nil {
			if canFallbackConcurrency(capacityErr) {
				lastErr = capacityErr
				continue
			}
			return nil, nil, nil, capacityErr
		}
		if !admitted {
			saturated = true
			continue
		}

		attemptCtx, span := otel.Tracer("gatemux/router").Start(ctx, "stream_attempt")
		span.SetAttributes(
			attribute.String("alias", alias),
			attribute.String("deployment", target.DeploymentName),
			attribute.String("provider", target.ProviderType),
			attribute.String("upstream_model", target.UpstreamModel),
		)
		body, callErr := func() (stream *chatStream, callErr error) {
			transferred := false
			stream = newChatStream(attemptCtx, h.streamingFor(target.DeploymentName))
			defer span.End()
			defer func() {
				if !transferred {
					defer h.releaseUpstreamPermit(permit)
					_ = stream.Close()
				}
			}()
			upstream, callErr := open(stream.ctx, target)
			if upstream != nil {
				stream.attach(upstream)
			}
			if cause := context.Cause(stream.ctx); cause != nil {
				callErr = cause
			}
			if callErr == nil && upstream == nil {
				callErr = errors.New("provider returned a nil stream")
			}
			if callErr == nil {
				callErr = stream.prefetch()
			}
			if callErr != nil {
				span.RecordError(callErr)
			}
			transferred = callErr == nil
			return
		}()
		if callErr == nil {
			return target, body, permit, nil
		}
		h.Router.RecordFailure(target.DeploymentName, callErr)
		lastErr = callErr
		if !providers.IsRetryable(callErr) || ctx.Err() != nil {
			return target, nil, nil, callErr
		}
	}
}

func (h *V1Handler) tryAcquireDeployment(target *router.Resolved) (*router.DeploymentPermit, bool) {
	if h.Router == nil || target == nil {
		return nil, false
	}
	permit, state, admitted := h.Router.TryAcquireDeployment(target.DeploymentName)
	if h.Telemetry != nil {
		h.Telemetry.RecordDeploymentAdmission(target.DeploymentName, admitted, state.InFlight, state.Limit)
	}
	return permit, admitted
}

func (h *V1Handler) releaseDeploymentPermit(permit *router.DeploymentPermit) {
	if permit == nil {
		return
	}
	name := permit.Name()
	permit.Release()
	if h.Telemetry != nil && h.Router != nil {
		state := h.Router.DeploymentConcurrency(name)
		h.Telemetry.SetDeploymentConcurrency(name, state.InFlight, state.Limit)
	}
}

func (h *V1Handler) writeRoutingError(
	w http.ResponseWriter,
	r *http.Request,
	team *store.Team,
	user *store.User,
	vk *store.VirtualKey,
	resolved *router.Resolved,
	alias, requestID string,
	started time.Time,
	err error,
	unknownUsage ...bool,
) {
	status := http.StatusBadGateway
	errType := "upstream_error"
	msg := err.Error()
	switch {
	case errors.Is(err, router.ErrUnknownAlias):
		status = http.StatusNotFound
		errType = "model_not_found"
		msg = "unknown model: " + alias
	case errors.Is(err, router.ErrNoHealthyDeployment):
		status = http.StatusServiceUnavailable
		errType = "upstream_unavailable"
		msg = "no healthy deployment available for model: " + alias
	case errors.Is(err, router.ErrDeploymentSaturated):
		status = http.StatusServiceUnavailable
		errType = "server_overloaded"
		msg = "all deployments are at concurrency capacity for model: " + alias
		w.Header().Set("Retry-After", "1")
	}
	var upstream *providers.UpstreamError
	if errors.As(err, &upstream) {
		switch {
		case upstream.StatusCode == http.StatusTooManyRequests:
			status = http.StatusTooManyRequests
			errType = "rate_limit_exceeded"
		case upstream.StatusCode == http.StatusRequestTimeout:
			status = http.StatusGatewayTimeout
			errType = "upstream_timeout"
		case upstream.StatusCode >= 500:
			status = http.StatusBadGateway
			errType = "upstream_unavailable"
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		errType = "upstream_timeout"
	}
	if concurrencyStatus, code, retry, ok := concurrencyErrorDetails(err); ok {
		status, errType = concurrencyStatus, code
		w.Header().Set("Retry-After", retry)
	}
	var accountingErr *accountingError
	if errors.As(err, &accountingErr) {
		status, errType, msg = 503, "accounting_unavailable", accountingErr.Error()
	}
	if g := requestGuardrail(r.Context()); g != nil {
		guardErr := g.finish(err)
		failure := guardrailFailure(guardErr)
		status, errType, msg = failure.status, failure.code, "protected request failed before completion"
		err = guardErr // never log or capture raw diagnostics from protected traffic
		unknownUsage = []bool{true}
	}
	if status >= 500 && team != nil && h.Logger != nil {
		h.Logger.Error("routed call failed", "team", team.Slug, "alias", alias, "deployment", deploymentName(resolved), "err", err)
	}
	if _, _, _, ok := concurrencyErrorDetails(err); ok {
		writeRoutingConcurrencyError(w, status, errType, msg)
	} else {
		writeJSONError(w, status, errType, msg)
	}
	// No authoritative usage accompanies this error. A started attempt may have
	// incurred cost even if headers/first event never arrived; preserve estimates.
	timings := Timings{UnknownUsage: true}
	if requestGuardrail(r.Context()) == nil {
		timings.CapturedRequest = captureBody(r.Context())
	}
	h.recordUsage(r.Context(), team, user, vk, resolved, alias, requestID, "", providers.Usage{}, started, status, err.Error(), false, "", nil, timings)
}

func deploymentName(resolved *router.Resolved) string {
	if resolved == nil {
		return ""
	}
	return resolved.DeploymentName
}

// Timings is an optional timing breakdown captured by the v1 hot path for
// the Settings → Usage detail row's "Timing breakdown" card. Zero values
// store as NULL in the DB (legacy behavior), so older code that doesn't
// thread Timings keeps working unchanged.
type Timings struct {
	UnknownUsage  bool // upstream accepted work but did not report authoritative usage
	QueueMs       int
	UpstreamMs    int
	TTFBMs        int
	PostprocessMs int
	// CapturedRequest / CapturedResponse hold the JSON bodies the caller
	// already serialized; recordUsage forwards them to the payload table
	// when the team has capture_payloads enabled.
	CapturedRequest  []byte
	CapturedResponse []byte
	// ClientIP is the source IP of the /v1 request, captured by the
	// caller from X-Forwarded-For or RemoteAddr.
	ClientIP string
}

func (h *V1Handler) recordUsage(
	ctx context.Context,
	team *store.Team,
	user *store.User,
	vk *store.VirtualKey,
	resolved *router.Resolved,
	alias, requestID, modelUsed string,
	u providers.Usage,
	started time.Time,
	status int,
	errMsg string,
	cached bool,
	cacheOriginRequestID string,
	tags []string,
	t Timings,
) {
	costCents := int64(0)
	accounting := "unknown"
	pricingCtx, cancelPricing := context.WithTimeout(context.Background(), settlementTimeout)
	if cached {
		accounting = "not_billable"
	} else if resolved != nil && h.Budget != nil && !t.UnknownUsage {
		cost, err := h.Budget.ComputeUsageCost(pricingCtx, resolved.ProviderType, resolved.UpstreamModel, u)
		if err == nil {
			costCents = cost
			accounting = "priced"
		} else {
			var unpriced *budget.PricingUnavailableError
			if errors.As(err, &unpriced) {
				accounting = "unpriced"
			}
			if h.Logger != nil {
				h.Logger.Error("cost evidence unavailable", "request_id", requestID, "alias", alias, "err", err)
			}
		}
	}
	cancelPricing()
	if h.Usage == nil {
		// Compatibility for independently embedded/test handlers without logging.
		if h.Budget != nil {
			work, cancel := context.WithTimeout(context.Background(), settlementTimeout)
			defer cancel()
			if accounting == "unknown" || accounting == "unpriced" {
				_, _ = h.Budget.SettleEstimated(work, requestID)
			} else {
				_ = h.Budget.Settle(work, requestID, costCents)
			}
		}
		return
	}
	var keyID *int64
	if vk != nil && vk.ID > 0 {
		kid := vk.ID
		keyID = &kid
	}
	var userID *int64
	if user != nil {
		uid := user.ID
		userID = &uid
	}
	var teamID int64
	if team != nil {
		teamID = team.ID
	}
	var depName string
	if resolved != nil {
		depName = resolved.DeploymentName
	}
	latencyMs := int(time.Since(started) / time.Millisecond)
	var serviceAccountID *int64
	if vk != nil && vk.ServiceAccountID != nil {
		v := *vk.ServiceAccountID
		serviceAccountID = &v
	}
	entry := store.UsageEntry{
		TeamID:               teamID,
		Accounting:           accounting,
		TokenDetails:         usageDetailsJSON(u),
		UserID:               userID,
		KeyID:                keyID,
		Alias:                alias,
		DeploymentName:       depName,
		RequestID:            requestID,
		ModelRequested:       alias,
		ModelUsed:            modelUsed,
		PromptTokens:         u.PromptTokens,
		CompletionTokens:     u.CompletionTokens,
		TotalTokens:          u.TotalTokens,
		CostCents:            costCents,
		LatencyMs:            latencyMs,
		QueueMs:              t.QueueMs,
		UpstreamMs:           t.UpstreamMs,
		TTFBMs:               t.TTFBMs,
		PostprocessMs:        t.PostprocessMs,
		StatusCode:           status,
		Error:                errMsg,
		Cached:               cached,
		CacheOriginRequestID: cacheOriginRequestID,
		RequestTags:          tags,
		ClientIP:             t.ClientIP,
		ServiceAccountID:     serviceAccountID,
	}
	attributeCustomer(ctx, &entry)
	// Usage and budget settlement commit in one transaction. Capture is optional
	// and attaches only to that committed, idempotent usage row.
	receipt, persistErr := h.persistUsage(ctx, entry)
	if persistErr == nil {
		costCents = receipt.CostCents
	}
	if persistErr == nil && t.CapturedRequest != nil && team != nil {
		captureCtx, cancelCapture := context.WithTimeout(context.Background(), settlementTimeout)
		defer cancelCapture()
		if on, err := h.Usage.Store().TeamCapturesPayloads(captureCtx, team.ID); err == nil && on {
			_ = h.Usage.Store().InsertUsagePayload(captureCtx, receipt.ID, t.CapturedRequest, t.CapturedResponse)
		}
	}
	if h.Callbacks != nil {
		evtType := callbacks.EventRequestCompleted
		if status >= 400 {
			evtType = callbacks.EventRequestFailed
		}
		teamSlug := ""
		if team != nil {
			teamSlug = team.Slug
		}
		userIDValue := int64(0)
		if userID != nil {
			userIDValue = *userID
		}
		keyPrefix := ""
		if vk != nil {
			keyPrefix = vk.KeyPrefix
		}
		h.Callbacks.Emit(callbacks.Event{
			Type:       evtType,
			RequestID:  requestID,
			TeamSlug:   teamSlug,
			UserID:     userIDValue,
			KeyPrefix:  keyPrefix,
			Alias:      alias,
			Deployment: depName,
			ModelUsed:  modelUsed,
			Tokens: callbacks.TokenUsage{
				Prompt:     u.PromptTokens,
				Completion: u.CompletionTokens,
				Total:      u.TotalTokens,
			},
			CostCents:  costCents,
			LatencyMs:  latencyMs,
			StatusCode: status,
			Cached:     cached,
			Error:      errMsg,
		})
	}
	if h.Telemetry != nil {
		providerType := ""
		if resolved != nil {
			providerType = resolved.ProviderType
		}
		h.Telemetry.RecordInference(alias, providerType, u.PromptTokens, u.CompletionTokens, u.TotalTokens, costCents)
		h.Telemetry.RecordInferenceRequest(alias, providerType, status, time.Since(started))
	}
}

type captureBodyKey struct{}

// withCaptureBody keeps the request body as the gateway forwarded it, so
// paths that fail after routing can still attach it to the usage row.
// Capture itself stays gated on the team's capture_payloads setting.
func withCaptureBody(ctx context.Context, body []byte) context.Context {
	return context.WithValue(ctx, captureBodyKey{}, body)
}

func captureBody(ctx context.Context) []byte {
	b, _ := ctx.Value(captureBodyKey{}).([]byte)
	return b
}
