package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/router"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5/middleware"
)

type customerContextKey struct{}

func requestCustomer(ctx context.Context) *store.Customer {
	c, _ := ctx.Value(customerContextKey{}).(*store.Customer)
	return c
}

func attributeCustomer(ctx context.Context, entry *store.UsageEntry) {
	if c := requestCustomer(ctx); c != nil {
		entry.CustomerExternalID = c.ExternalID
		if c.ID > 0 {
			id := c.ID
			entry.CustomerID = &id
		}
	}
}

// Resolve once, after endpoint-specific parsing and before every admission stage.
// The object is request-local; never mutate shared auth or router snapshots.
func (h *V1Handler) resolveRequestCustomer(w http.ResponseWriter, r *http.Request, alias, bodyUser string) bool {
	if requestCustomer(r.Context()) != nil {
		return true
	}
	team := auth.TeamFromContext(r.Context())
	if team == nil {
		return true
	}
	id := extractCustomerID(r, bodyUser)
	mode := team.CustomerRegistration
	if mode == "" {
		mode = "optional"
	}
	deny := func(status int, code, message string) bool {
		h.recordDenial(r.Context(), team, auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), status, code)
		writeJSONError(w, status, code, message)
		return false
	}
	if id == "" {
		if mode != "optional" {
			return deny(403, "customer_required", "this team requires a registered customer identity")
		}
		return true
	}
	if err := store.ValidateCustomerID(id); err != nil {
		return deny(400, "invalid_customer", err.Error())
	}
	identity := &store.Customer{TeamID: team.ID, ExternalID: id}
	*r = *r.WithContext(context.WithValue(r.Context(), customerContextKey{}, identity))
	st := h.responseStore()
	if st == nil {
		return deny(503, "customer_policy_unavailable", "customer policy store unavailable")
	}
	ctx, cancel := context.WithTimeout(r.Context(), settlementTimeout)
	defer cancel()
	c, err := st.GetCustomerByExternalID(ctx, team.ID, id)
	if errors.Is(err, store.ErrNotFound) && mode == "auto_create" {
		c, err = st.GetOrCreateCustomer(ctx, team.ID, id)
	}
	if errors.Is(err, store.ErrNotFound) {
		if mode == "optional" {
			return true
		}
		return deny(403, "customer_not_registered", "customer is not registered for this team")
	}
	if errors.Is(err, store.ErrCustomerCapacity) {
		return deny(403, "customer_registration_capacity", "automatic registration capacity reached; contact your team administrator")
	}
	if err != nil {
		return deny(503, "customer_policy_unavailable", "customer policy lookup unavailable")
	}
	*r = *r.WithContext(context.WithValue(r.Context(), customerContextKey{}, c))
	if c.ArchivedAt != nil {
		return deny(403, "customer_archived", "customer is archived")
	}
	if err := store.ValidateCustomerLimits(c.UsdLimitCents, c.Period, c.RPM, c.TPM); err != nil {
		return deny(503, "customer_policy_unavailable", "stored customer policy is invalid")
	}
	return true
}

// Unpriced native/opaque modalities cannot silently bypass key/customer cost
// or customer TPM policy. RPM and concurrency apply without those policies.
func (h *V1Handler) admitUnpricedCustomer(w http.ResponseWriter, r *http.Request, alias string) bool {
	if hasKeyBudget(r) {
		h.recordDenial(r.Context(), auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), 400, "key_accounting_unsupported")
		writeJSONError(w, 400, "key_accounting_unsupported", "this endpoint cannot enforce the key's cost policy")
		return false
	}
	c := requestCustomer(r.Context())
	if c != nil && (c.UsdLimitCents != nil || (c.TPM != nil && *c.TPM > 0)) {
		h.recordDenial(r.Context(), auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), 400, "customer_accounting_unsupported")
		writeJSONError(w, 400, "customer_accounting_unsupported", "this endpoint cannot enforce the customer's token or cost policy")
		return false
	}
	return h.enforceRateLimit(w, r, auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), 0)
}

// The token-price table does not price hosted tools, audio, prediction tokens or
// service-tier multipliers. Reject those dimensions before IO for keys/customers
// with a spend ceiling; ordinary text-token billing would bypass policy.
func (h *V1Handler) admitCustomerPricing(w http.ResponseWriter, r *http.Request, alias string, raw []byte) bool {
	c := requestCustomer(r.Context())
	if !hasKeyBudget(r) && (c == nil || c.UsdLimitCents == nil) {
		return true
	}
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil {
		return false // endpoint parsing precedes this helper
	}
	unsupported := false
	for _, key := range []string{"audio", "web_search_options", "prediction", "moderation"} {
		if value := body[key]; len(value) > 0 && string(value) != "null" {
			unsupported = true
		}
	}
	if value := body["service_tier"]; len(value) > 0 && string(value) != "null" {
		var tier string
		unsupported = unsupported || json.Unmarshal(value, &tier) != nil || tier != "default"
	}
	if value := body["modalities"]; len(value) > 0 && string(value) != "null" {
		var modalities []string
		if json.Unmarshal(value, &modalities) != nil {
			unsupported = true
		}
		for _, modality := range modalities {
			unsupported = unsupported || modality != "text"
		}
	}
	var messages []struct {
		Content json.RawMessage `json:"content"`
	}
	_ = json.Unmarshal(body["messages"], &messages)
	for _, message := range messages {
		var parts []struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(message.Content, &parts)
		for _, part := range parts {
			unsupported = unsupported || part.Type == "input_audio"
		}
	}
	if unsupported {
		code := "customer_accounting_unsupported"
		if hasKeyBudget(r) {
			code = "key_accounting_unsupported"
		}
		h.recordDenial(r.Context(), auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), alias, middleware.GetReqID(r.Context()), 400, code)
		writeJSONError(w, 400, code, "key/customer budgets support configured token pricing, not audio, hosted tools, prediction or non-default service-tier charges")
		return false
	}
	return true
}

func (h *V1Handler) recordNativeAudit(r *http.Request, resolved *router.Resolved, alias string, started time.Time, status int) {
	if h.Usage == nil {
		return
	}
	team := auth.TeamFromContext(r.Context())
	if team == nil {
		return
	}
	entry := store.UsageEntry{
		TeamID: team.ID, Alias: alias, ModelRequested: alias, ModelUsed: resolved.UpstreamModel,
		DeploymentName: resolved.DeploymentName, RequestID: middleware.GetReqID(r.Context()),
		StatusCode: status, LatencyMs: int(time.Since(started).Milliseconds()), Ts: time.Now().UTC(),
		TokenDetails: json.RawMessage(`{"accounting":"unpriced_native"}`),
	}
	if key := auth.VirtualKeyFromContext(r.Context()); key != nil {
		if key.ID > 0 {
			entry.KeyID = &key.ID
		}
		entry.UserID, entry.ServiceAccountID = key.UserID, key.ServiceAccountID
	}
	if status >= 400 {
		entry.Error = "native_upstream_failed"
	}
	attributeCustomer(r.Context(), &entry)
	_, _ = h.persistUsage(r.Context(), entry)
}
