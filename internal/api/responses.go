package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5/middleware"
)

func (h *V1Handler) responseStore() *store.Store {
	if h.Usage != nil {
		return h.Usage.Store()
	}
	if h.Budget != nil {
		return h.Budget.Store
	}
	return nil
}

func (h *V1Handler) Responses(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	team, key, user := auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), auth.OwnerUserFromContext(r.Context())
	if team == nil {
		writeJSONError(w, 401, "authentication_error", "no team in context")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxResponseBody+1))
	if err != nil || len(body) > maxResponseBody {
		writeJSONError(w, 400, "invalid_request", "Responses input exceeds 8 MiB or could not be read")
		return
	}
	req, err := decodeResponseRequest(body)
	if err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	requestID := middleware.GetReqID(r.Context())
	if !h.modelAllowed(team, key, req.Model) {
		h.recordDenial(r.Context(), team, key, req.Model, requestID, 403, "model_not_allowed")
		writeJSONError(w, 403, "model_not_allowed", "model not allowed")
		return
	}
	owner := responseOwner(r)
	storeResponse := req.Store == nil || *req.Store
	if owner == "" || (storeResponse && h.responseStore() == nil) {
		writeJSONError(w, 503, "response_state_unavailable", "stable principal and response metadata store required")
		return
	}
	var previous *store.ResponseBinding
	if req.PreviousID != "" {
		previous, err = h.loadResponseBinding(r, req.PreviousID)
		if err != nil {
			h.responseStateError(w, err)
			return
		}
		if previous.Alias != req.Model || previous.CustomerExternalID != extractCustomerID(r, req.User) {
			writeJSONError(w, 404, "not_found", "response not found")
			return
		}
		if previous.Status != "completed" && previous.Status != "incomplete" {
			writeJSONError(w, 409, "response_not_ready", "previous response has not completed")
			return
		}
	}
	release, admitted := h.admitRequestConcurrency(w, r, req.Model, req.User)
	if !admitted {
		return
	}
	defer release()
	requestID = middleware.GetReqID(r.Context())
	if !h.admitCustomerPricing(w, r, req.Model, body) {
		return
	}
	prompt, completion, err := req.estimate(previous)
	if err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	if !h.enforceRateLimit(w, r, team, key, req.Model, requestID, prompt+completion) {
		return
	}
	if !h.enforceBudget(w, r, team, user, req.Model, requestID, prompt, completion, providers.CapabilityResponses) {
		return
	}
	defer h.reconcileBudgetOnPanic(requestID)
	id, err := newResponseID()
	if err != nil {
		h.writeRoutingError(w, r, team, user, key, nil, req.Model, requestID, started, err)
		return
	}
	if storeResponse {
		ctx, cancel := context.WithTimeout(r.Context(), settlementTimeout)
		err = h.responseStore().Pool.Ping(ctx)
		cancel()
		if err != nil {
			h.writeRoutingError(w, r, team, user, key, nil, req.Model, requestID, started, err)
			return
		}
	}
	target, source, permit, payload, err := h.openResponseWithFallback(r, req, previous)
	if err != nil {
		var accepted *acceptedResponseError
		h.writeRoutingError(w, r, team, user, key, target, req.Model, requestID, started, err, errors.As(err, &accepted))
		return
	}
	defer h.releaseUpstreamPermit(permit)
	defer source.Close()
	binding := &store.ResponseBinding{ID: id, TeamID: team.ID, Owner: owner, Alias: req.Model, Deployment: target.DeploymentName, TargetFingerprint: responseTargetFingerprint(h.Router, target), PreviousID: req.PreviousID, CustomerExternalID: extractCustomerID(r, req.User)}
	var usage *providers.ResponseUsage
	status := http.StatusOK
	var outcomeErr error
	// Deferred accounting runs before permits return, including store errors and
	// partial native streams. Missing authoritative usage retains the estimate.
	defer func() {
		var canonical providers.Usage
		if usage != nil {
			canonical = usage.Canonical()
		}
		errText := ""
		if outcomeErr != nil {
			errText = outcomeErr.Error()
		}
		h.recordUsage(r.Context(), team, user, key, target, req.Model, requestID, target.UpstreamModel, canonical, started, status, errText, false, "", resolveContextFromRequest(r).Tags, Timings{UnknownUsage: !usage.UsageReported()})
	}()
	if req.Stream {
		usage, status, outcomeErr = h.forwardResponseStream(w, r, source, binding, storeResponse)
		return
	}
	rewritten, envelope, err := rewriteResponse(payload, binding)
	if err == nil {
		usage = envelope.Usage
	}
	if err == nil && binding.Status != "completed" && binding.Status != "incomplete" && binding.Status != "failed" {
		err = fmt.Errorf("synchronous Responses returned nonterminal state")
	}
	if err == nil && storeResponse {
		err = h.saveResponseBinding(r.Context(), *binding, true)
	}
	if err != nil {
		status = 502
		outcomeErr = err
		writeJSONError(w, status, "upstream_error", "response could not be validated or recorded")
		return
	}
	if binding.Status == "failed" {
		status = 502
		outcomeErr = errUpstreamStreamReported
		h.Router.RecordFailure(target.DeploymentName, outcomeErr)
	} else {
		h.Router.RecordSuccess(target.DeploymentName)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	writer := newStreamWriter(r.Context(), w, source.config.WriteTimeout)
	defer writer.close()
	if err := writer.write(string(rewritten)); err != nil {
		status, outcomeErr = proxyErrorStatus(err), err
	}
}

func (h *V1Handler) openResponseWithFallback(r *http.Request, req *responseRequest, previous *store.ResponseBinding) (*router.Resolved, *chatStream, *upstreamPermit, json.RawMessage, error) {
	tried := map[string]bool{}
	var last error
	for {
		var target *router.Resolved
		var err error
		if previous != nil {
			target, err = h.responseBoundTarget(previous)
			if target != nil && tried[target.DeploymentName] {
				return target, nil, nil, nil, last
			}
		} else {
			target, err = h.Router.ResolveWithContext(req.Model, tried, providers.CapabilityResponses, resolveContextFromRequest(r))
		}
		if err != nil {
			if last != nil {
				err = last
			}
			return nil, nil, nil, nil, err
		}
		tried[target.DeploymentName] = true
		provider, ok := target.Provider.(providers.ResponsesProvider)
		if !ok {
			last = fmt.Errorf("deployment does not implement native Responses")
			continue
		}
		permit, admitted, err := h.tryAcquireUpstream(r.Context(), target)
		if err != nil {
			if canFallbackConcurrency(err) {
				last = err
				continue
			}
			return target, nil, nil, nil, err
		}
		if !admitted {
			last = router.ErrDeploymentSaturated
			continue
		}
		source := newChatStream(r.Context(), h.streamingFor(target.DeploymentName))
		attemptStart := time.Now()
		var payload json.RawMessage
		accepted := false
		err = func() error {
			transferred := false
			defer func() {
				if !transferred {
					source.Close()
					h.releaseUpstreamPermit(permit)
				}
			}()
			previousID := ""
			if previous != nil {
				previousID = previous.UpstreamID
			}
			body, err := req.encoded(target.UpstreamModel, previousID)
			if err != nil {
				return err
			}
			response, err := provider.ResponseRequest(source.ctx, http.MethodPost, "/responses", body)
			if response != nil {
				source.attach(response.Body)
			}
			if err != nil {
				return err
			}
			if response.StatusCode >= 300 {
				detail := providers.ReadErrorBody(response.Body)
				return &providers.UpstreamError{Provider: target.ProviderType, StatusCode: response.StatusCode, Message: string(detail)}
			}
			accepted = true
			if req.Stream {
				if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
					return fmt.Errorf("Responses stream has invalid content type")
				}
				err = source.prefetch()
			} else {
				payload, err = readResponseJSON(source)
			}
			if err != nil {
				return err
			}
			transferred = true
			return nil
		}()
		if h.Telemetry != nil {
			status := 200
			if err != nil {
				status = 502
				var upstream *providers.UpstreamError
				if errors.As(err, &upstream) {
					status = upstream.StatusCode
				}
			}
			h.Telemetry.RecordUpstream(target.ProviderType, target.DeploymentName, status, time.Since(attemptStart))
		}
		if err == nil {
			return target, source, permit, payload, nil
		}
		if accepted {
			// A native Response may now exist, even before the first event. Do not
			// replay accepted work or silently refund an unmeasured completion.
			return target, nil, nil, nil, &acceptedResponseError{err}
		}
		h.Router.RecordFailure(target.DeploymentName, err)
		last = err
		if previous != nil || !providers.IsRetryable(err) || r.Context().Err() != nil {
			return target, nil, nil, nil, err
		}
	}
}

type acceptedResponseError struct{ error }

func (e *acceptedResponseError) Unwrap() error { return e.error }

func readResponseJSON(source *chatStream) (json.RawMessage, error) {
	out := make([]byte, 0, 32<<10)
	buf := make([]byte, 32<<10)
	for {
		n, err := source.readBytes(buf)
		if len(out)+n > maxResponseBody {
			return nil, fmt.Errorf("Responses output exceeds 8 MiB")
		}
		out = append(out, buf[:n]...)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (h *V1Handler) saveResponseBinding(ctx context.Context, binding store.ResponseBinding, create bool) error {
	ctx, cancel := context.WithTimeout(ctx, settlementTimeout)
	defer cancel()
	if create {
		return h.responseStore().CreateResponseBinding(ctx, binding)
	}
	return h.responseStore().UpdateResponseBinding(ctx, binding)
}

func (h *V1Handler) loadResponseBinding(r *http.Request, id string) (*store.ResponseBinding, error) {
	team := auth.TeamFromContext(r.Context())
	owner := responseOwner(r)
	if team == nil || owner == "" || !responseIDPattern.MatchString(id) {
		return nil, store.ErrNotFound
	}
	if h.responseStore() == nil {
		return nil, fmt.Errorf("response metadata unavailable")
	}
	ctx, cancel := context.WithTimeout(r.Context(), settlementTimeout)
	defer cancel()
	return h.responseStore().GetResponseBinding(ctx, team.ID, owner, id)
}

func (h *V1Handler) responseStateError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "response not found")
	} else {
		writeJSONError(w, 503, "response_state_unavailable", "response metadata unavailable")
	}
}

func (h *V1Handler) responseBoundTarget(binding *store.ResponseBinding) (*router.Resolved, error) {
	targets, err := h.Router.TargetsFor(binding.Alias, providers.CapabilityResponses)
	if err != nil {
		return nil, err
	}
	for _, target := range targets {
		if target.DeploymentName == binding.Deployment && responseTargetFingerprint(h.Router, target) == binding.TargetFingerprint {
			return target, nil
		}
	}
	return nil, fmt.Errorf("original response deployment is no longer available; stored state cannot fall back across providers")
}
