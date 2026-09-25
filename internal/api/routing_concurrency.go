package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/go-chi/chi/v5/middleware"
)

func (h *AdminHandler) ListRoutingConcurrencyLimits(w http.ResponseWriter, r *http.Request) {
	rows, err := h.Store.ListRoutingConcurrencyLimits(r.Context())
	if err != nil {
		writeJSONError(w, 500, "internal_error", "could not load concurrency policies")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *AdminHandler) SetRoutingConcurrencyLimit(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Scope               string          `json:"scope"`
		Subject             string          `json:"subject"`
		MaxParallelRequests json.RawMessage `json:"max_parallel_requests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, 400, "invalid_request", "invalid JSON")
		return
	}
	if req.Scope != "model" && req.Scope != "provider" {
		writeJSONError(w, 400, "invalid_request", "scope must be model or provider")
		return
	}
	if req.Subject == "" || len(req.Subject) > 256 || strings.TrimSpace(req.Subject) != req.Subject {
		writeJSONError(w, 400, "invalid_request", "subject must contain 1–256 bytes without surrounding whitespace")
		return
	}
	if req.Scope == "provider" && !slices.Contains(supportedProviderTypes, req.Subject) {
		writeJSONError(w, 400, "invalid_request", "subject must be a supported provider_type")
		return
	}
	if len(req.MaxParallelRequests) == 0 {
		writeJSONError(w, 400, "invalid_request", "max_parallel_requests is required; null clears the cap")
		return
	}
	var limit *int
	if err := json.Unmarshal(req.MaxParallelRequests, &limit); err != nil {
		writeJSONError(w, 400, "invalid_request", "max_parallel_requests must be an integer or null")
		return
	}
	limit, ok := positiveLimit(w, limit, "max_parallel_requests")
	if !ok {
		return
	}
	row, err := h.Store.SetRoutingConcurrencyLimit(r.Context(), req.Scope, req.Subject, limit)
	if err != nil {
		writeJSONError(w, 500, "internal_error", "could not save concurrency policy")
		return
	}
	h.audit(r, "concurrency.update", req.Scope, req.Subject, map[string]any{"max_parallel_requests": limit})
	// Other replicas poll every five seconds. Do not report a failed save if
	// this process's refresh fails after the database transaction committed.
	h.refreshConcurrencyPolicies(r)
	writeJSON(w, http.StatusOK, row)
}

func (h *AdminHandler) refreshConcurrencyPolicies(r *http.Request) {
	if h.Registry != nil {
		if err := h.Registry.RefreshConcurrencyPolicies(r.Context()); err != nil && h.Logger != nil {
			h.Logger.Warn("saved concurrency policy awaits refresh", "err", err)
		}
	}
}

type concurrencyAdmissionError struct {
	scope       string
	retryAfter  time.Duration
	unavailable bool
}

func (e *concurrencyAdmissionError) Error() string {
	if e.unavailable {
		return "distributed concurrency check unavailable"
	}
	return e.scope + " concurrent request limit exceeded; retry later"
}

func concurrencyErrorDetails(err error) (int, string, string, bool) {
	var admissionErr *concurrencyAdmissionError
	if !errors.As(err, &admissionErr) {
		return 0, "", "", false
	}
	status, code := http.StatusTooManyRequests, "concurrency_limit_exceeded"
	if admissionErr.unavailable {
		status, code = http.StatusServiceUnavailable, "concurrency_limit_unavailable"
	}
	seconds := (admissionErr.retryAfter + time.Second - 1) / time.Second
	if seconds < 1 {
		seconds = 1
	}
	return status, code, strconv.FormatInt(int64(seconds), 10), true
}

func (h *V1Handler) acquireRoutingScope(ctx context.Context, kind, subject string) (*concurrency.Lease, error) {
	if h.Router == nil {
		return nil, nil
	}
	scopes, err := h.Router.ConcurrencyScope(kind, subject)
	return h.acquireConcurrencyScopes(ctx, kind, scopes, err)
}

func (h *V1Handler) acquireConcurrencyScopes(ctx context.Context, kind string, scopes []concurrency.Scope, err error) (*concurrency.Lease, error) {
	if err == nil && len(scopes) == 0 {
		return nil, nil
	}
	var lease *concurrency.Lease
	var result concurrency.Result
	if err == nil {
		ttl := 2 * time.Minute
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline)+30*time.Second > ttl {
			ttl = time.Until(deadline) + 30*time.Second
		}
		partition := int64(1)
		if team := auth.TeamFromContext(ctx); team != nil {
			partition = team.ID
		}
		lease, result, err = h.Concurrency.Acquire(ctx, concurrency.Request{
			ID: concurrency.NewRequestID(), Partition: partition, TTL: ttl, Scopes: scopes,
		})
	}
	outcome := "admitted"
	var denied error
	if err != nil {
		outcome = "unavailable"
		denied = &concurrencyAdmissionError{scope: kind, unavailable: true}
		if h.Logger != nil {
			h.Logger.Error("routing concurrency unavailable", "scope", kind, "err", err)
		}
	} else if !result.Allowed {
		outcome = "denied"
		kind = result.Scope
		denied = &concurrencyAdmissionError{scope: kind, retryAfter: result.RetryAfter}
	}
	if h.Telemetry != nil {
		h.Telemetry.RecordTenantConcurrency(kind, outcome)
	}
	return lease, denied
}

func (h *V1Handler) releaseRoutingLease(lease *concurrency.Lease) {
	if err := lease.Release(); err != nil {
		if h.Telemetry != nil {
			h.Telemetry.RecordTenantConcurrency("combined", "release_error")
		}
		if h.Logger != nil {
			h.Logger.Error("routing concurrency release failed", "err", err)
		}
	}
}

// Model/customer permits span the full request, including every fallback and stream.
// Called only after the existing endpoint parser extracts the alias.
func (h *V1Handler) admitRequestConcurrency(w http.ResponseWriter, r *http.Request, alias, bodyUser string) (func(), bool) {
	if !h.loadGuardrails(w, r, alias) {
		return nil, false
	}
	if !h.resolveRequestCustomer(w, r, alias, bodyUser) {
		return nil, false
	}
	finishAccounting, accounted := h.beginAccounting(w, r, alias)
	if !accounted {
		return nil, false
	}
	var scopes []concurrency.Scope
	var err error
	if h.Router != nil {
		if alias != "" {
			scopes, err = h.Router.ConcurrencyScope("model", alias)
		}
	}
	if c := requestCustomer(r.Context()); c != nil && c.ID > 0 && c.MaxParallelRequests != nil && *c.MaxParallelRequests > 0 {
		scopes = append(scopes, concurrency.Scope{Kind: "customer", ID: c.ID, Limit: *c.MaxParallelRequests})
	}
	lease, err := h.acquireConcurrencyScopes(r.Context(), "combined", scopes, err)
	if err != nil {
		status, code, retry, _ := concurrencyErrorDetails(err)
		w.Header().Set("Retry-After", retry)
		writeRoutingConcurrencyError(w, status, code, err.Error())
		h.recordDenial(r.Context(), auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), status, code)
		finishAccounting()
		return nil, false
	}
	return func() { h.releaseRoutingLease(lease); finishAccounting() }, true
}

// upstreamPermit combines local deployment capacity and a provider-wide lease.
// Both release on failed attempts; a successful stream transfers ownership to
// the caller until it has finished copying and closing the upstream body.
type upstreamPermit struct {
	once     sync.Once
	local    *router.DeploymentPermit
	provider *concurrency.Lease
}

func (h *V1Handler) tryAcquireUpstream(ctx context.Context, target *router.Resolved) (*upstreamPermit, bool, error) {
	if g := requestGuardrail(ctx); g != nil && target.ProviderType != "openai" && target.ProviderType != "openai_compatible" && target.ProviderType != "azure_openai" && target.ProviderType != "ollama" && target.ProviderType != "vllm" {
		return nil, false, g.finish(guardrails.ErrUnsupported)
	}
	local, admitted := h.tryAcquireDeployment(target)
	if !admitted {
		return nil, false, nil
	}
	lease, err := h.acquireRoutingScope(ctx, "provider", target.ProviderType)
	if err != nil {
		h.releaseDeploymentPermit(local)
		return nil, false, err
	}
	if err := h.markAccountingStarted(ctx); err != nil {
		h.releaseRoutingLease(lease)
		h.releaseDeploymentPermit(local)
		return nil, false, err
	}
	return &upstreamPermit{local: local, provider: lease}, true, nil
}

func (h *V1Handler) releaseUpstreamPermit(permit *upstreamPermit) {
	if permit == nil {
		return
	}
	permit.once.Do(func() {
		h.releaseRoutingLease(permit.provider)
		h.releaseDeploymentPermit(permit.local)
	})
}

func canFallbackConcurrency(err error) bool {
	var e *concurrencyAdmissionError
	return errors.As(err, &e) && !e.unavailable
}

func writeCapabilityRoutingError(w http.ResponseWriter, err error) {
	var accountingErr *accountingError
	if errors.As(err, &accountingErr) {
		writeJSONError(w, 503, "accounting_unavailable", accountingErr.Error())
		return
	}
	if status, code, retry, ok := concurrencyErrorDetails(err); ok {
		w.Header().Set("Retry-After", retry)
		writeRoutingConcurrencyError(w, status, code, err.Error())
	} else if errors.Is(err, router.ErrDeploymentSaturated) {
		w.Header().Set("Retry-After", "1")
		writeJSONError(w, http.StatusServiceUnavailable, "server_overloaded", err.Error())
	} else {
		writeJSONError(w, http.StatusServiceUnavailable, "no_deployment", fmt.Sprint(err))
	}
}

func writeRoutingConcurrencyError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{
		"message": message, "type": code, "code": code, "param": nil,
	}})
}
