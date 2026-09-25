package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// GenericPassthrough resolves a /passthrough/{name}/* request to a
// configured Passthrough row, swaps the inbound bearer token for the
// configured upstream credential, and proxies the request to
// target_url + remainder. Streams the response body straight back so
// large/streaming responses don't pin gateway memory.
//
// The inbound auth model is identical to /v1: the client sends their
// GateMux-issued virtual key, Bearer middleware validates it, and rate
// limits / audit / per-key allowlists apply uniformly. We do NOT run
// budget admission today because passthrough endpoints don't have a
// canonical token-counting model — every request is logged with cost=0
// so audit history still works for forensics.
func (h *V1Handler) GenericPassthrough(w http.ResponseWriter, r *http.Request) {
	team := auth.TeamFromContext(r.Context())
	vk := auth.VirtualKeyFromContext(r.Context())
	if team == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "no team in context")
		return
	}
	releaseCustomer, admitted := h.admitRequestConcurrency(w, r, "", "")
	if !admitted {
		return
	}
	defer releaseCustomer()
	if !h.admitUnpricedCustomer(w, r, "passthrough:"+chi.URLParam(r, "name")) {
		return
	}

	name := chi.URLParam(r, "name")
	rest := chi.URLParam(r, "*") // chi names the catch-all "*"
	if name == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "passthrough name is required")
		return
	}

	pt, err := h.Usage.Store().GetPassthroughByName(r.Context(), name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "no passthrough configured for: "+name)
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !pt.Enabled {
		writeJSONError(w, http.StatusServiceUnavailable, "passthrough_disabled", "passthrough is disabled: "+name)
		return
	}

	target, err := url.Parse(pt.TargetURL)
	if err != nil || target.Scheme == "" || target.Host == "" {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "passthrough has invalid target_url")
		return
	}
	target.Path = strings.TrimRight(target.Path, "/") + "/" + strings.TrimLeft(rest, "/")
	target.RawQuery = r.URL.RawQuery

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "build upstream request: "+err.Error())
		return
	}
	upstreamReq.ContentLength = r.ContentLength

	// Copy hop-safe inbound headers (Content-Type, Accept, etc.) before
	// stamping the upstream credential. Drop the inbound Authorization
	// so the client's gw-* key never leaks past the gateway.
	nominated := connectionHeaders(r.Header)
	for k, vs := range r.Header {
		if shouldHopByHop(k) || nominated[strings.ToLower(k)] {
			continue
		}
		switch strings.ToLower(k) {
		case "authorization", "host", "cookie", "x-api-key",
			"x-forwarded-for", "x-forwarded-host", "x-forwarded-proto",
			"x-real-ip", "x-gatemux-replay", "x-aiport-replay":
			continue
		}
		for _, v := range vs {
			upstreamReq.Header.Add(k, v)
		}
	}
	if pt.AuthHeader != "" {
		val := pt.AuthValuePrefix
		if pt.AuthValueEnv != "" {
			val += os.Getenv(pt.AuthValueEnv)
		}
		upstreamReq.Header.Set(pt.AuthHeader, val)
	}

	started := time.Now()
	if err := h.markAccountingStarted(r.Context()); err != nil {
		writeJSONError(w, 503, "accounting_unavailable", err.Error())
		return
	}
	status, err := h.forwardProxy(w, r, upstreamReq, h.Streaming, false)
	h.logPassthroughAudit(r.Context(), team, vk, pt.Name, rest, status, time.Since(started))
	abortIncompleteProxy(err)
}

// logPassthroughAudit writes a usage_log row for every passthrough
// request — same shape as /v1 entries, but cost_cents=0 because we
// don't compute pricing. Operators can still filter by alias prefix
// "passthrough:<name>" in Usage.
func (h *V1Handler) logPassthroughAudit(ctx context.Context, team *store.Team, vk *store.VirtualKey, name, path string, statusCode int, elapsed time.Duration) {
	if h.Usage == nil || team == nil {
		return
	}
	entry := store.UsageEntry{
		TeamID:       team.ID,
		Alias:        "passthrough:" + name,
		RequestID:    chimw.GetReqID(ctx),
		StatusCode:   statusCode,
		LatencyMs:    int(elapsed.Milliseconds()),
		Ts:           time.Now().UTC(),
		TokenDetails: json.RawMessage(`{"accounting":"unpriced_passthrough"}`),
	}
	if path != "" {
		entry.ModelUsed = path
	}
	if vk != nil {
		if vk.ID > 0 {
			entry.KeyID = &vk.ID
		}
		entry.UserID = vk.UserID
		entry.ServiceAccountID = vk.ServiceAccountID
	}
	if statusCode >= 400 {
		entry.Error = fmt.Sprintf("upstream_%d", statusCode)
	}
	attributeCustomer(ctx, &entry)
	_, _ = h.persistUsage(ctx, entry)
}

var defaultPassthroughClient = providers.NewHTTPClient()

// passthroughClient is a shared HTTP client used for upstream
// proxy calls. We don't reuse the provider clients because passthrough
// targets aren't necessarily LLM providers and shouldn't share connection
// pools with provider streaming clients.
func (h *V1Handler) passthroughClient() *http.Client {
	if h.ptClient != nil {
		return h.ptClient
	}
	return defaultPassthroughClient
}
