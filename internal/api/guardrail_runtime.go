package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/gatemux-dev/gatemux/internal/telemetry"
	"github.com/go-chi/chi/v5/middleware"
)

type guardrailContextKey struct{}
type guardrailRun struct {
	store            *store.Store
	teamID           int64
	alias, requestID string
	pre, post        *guardrails.Filter
	postAudited      bool
	telemetry        *telemetry.Collector
}
type guardrailError struct {
	status int
	code   string
}

func (e *guardrailError) Error() string { return e.code }
func guardrailFailure(err error) *guardrailError {
	var typed *guardrailError
	if errors.As(err, &typed) {
		return typed
	}
	if errors.Is(err, guardrails.ErrBlocked) {
		return &guardrailError{403, "guardrail_blocked"}
	}
	if errors.Is(err, guardrails.ErrUnsupported) {
		return &guardrailError{400, "guardrail_unsupported"}
	}
	return &guardrailError{503, "guardrail_unavailable"}
}
func requestGuardrail(ctx context.Context) *guardrailRun {
	g, _ := ctx.Value(guardrailContextKey{}).(*guardrailRun)
	return g
}

func (h *V1Handler) loadGuardrails(w http.ResponseWriter, r *http.Request, alias string) bool {
	st := h.responseStore()
	team := auth.TeamFromContext(r.Context())
	if st == nil || team == nil {
		return true
	} // isolated non-data-plane test handlers
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	policies, err := st.GuardrailPolicies(ctx, team.ID, alias)
	if err == nil && len(policies) == 0 {
		return true
	}
	g := &guardrailRun{store: st, teamID: team.ID, alias: alias, requestID: middleware.GetReqID(r.Context()), telemetry: h.Telemetry}
	if err == nil && (r.URL.Path != "/v1/chat/completions" || r.Method != "POST") {
		err = guardrails.ErrUnsupported
	}
	if err != nil {
		_ = g.audit(nil, err)
		failure := guardrailFailure(err)
		h.recordDenial(r.Context(), team, auth.VirtualKeyFromContext(r.Context()), alias, g.requestID, failure.status, failure.code)
		writeJSONError(w, failure.status, failure.code, "guardrail policy unavailable or unsupported for this endpoint")
		return false
	}
	g.pre = guardrails.New(policies, "pre")
	g.post = guardrails.New(policies, "post")
	*r = *r.WithContext(providers.WithResponseLimit(context.WithValue(r.Context(), guardrailContextKey{}, g), guardrails.MaxPayload))
	return true
}

// One bounded transaction per phase, not one queued write per stream event.
// Reasons contain classification only: never prompts, terms or provider bodies.
func (g *guardrailRun) audit(f *guardrails.Filter, cause error) (err error) {
	defer func() {
		if err != nil {
			g.telemetry.RecordGuardrail("validation", "audit_error", 0)
		}
	}()
	latency := time.Duration(0)
	if f != nil {
		latency = f.Latency
	}
	results := []guardrails.Result{}
	if f != nil {
		results = f.Results()
	}
	if cause != nil {
		decision := "error"
		if errors.Is(cause, guardrails.ErrUnsupported) {
			decision = "unsupported"
		}
		if errors.Is(cause, guardrails.ErrBlocked) {
			decision = "block"
		}
		results = append(results, guardrails.Result{Name: "policy", Phase: "validation", Decision: decision})
	}
	if len(results) == 0 {
		return nil
	}
	for _, result := range results {
		g.telemetry.RecordGuardrail(result.Phase, result.Decision, latency)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := g.store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, r := range results {
		if _, err = tx.Exec(ctx, `INSERT INTO guardrail_decisions(request_id,team_id,alias,guardrail_name,phase,decision,reason,latency_ms) VALUES($1,$2,$3,$4,$5,$6,$6,$7)`, g.requestID, g.teamID, g.alias, r.Name, r.Phase, r.Decision, latency.Milliseconds()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (g *guardrailRun) finish(err error) error {
	if !g.postAudited {
		g.postAudited = true
		if auditErr := g.audit(g.post, err); auditErr != nil {
			return guardrailFailure(auditErr)
		}
	}
	if err != nil {
		return guardrailFailure(err)
	}
	return nil
}

func (g *guardrailRun) response(raw []byte, stream bool) ([]byte, error) {
	out, err := guardrails.Response(raw, g.post, stream)
	if err != nil {
		return nil, g.finish(err)
	}
	if !stream {
		err = g.finish(nil)
	}
	return out, err
}

func (h *V1Handler) guardrailPre(w http.ResponseWriter, r *http.Request, raw []byte) ([]byte, bool) {
	g := requestGuardrail(r.Context())
	if g == nil {
		return raw, true
	}
	out, err := guardrails.Request(raw, g.pre)
	if auditErr := g.audit(g.pre, err); auditErr != nil {
		err = auditErr
	}
	if err != nil {
		failure := guardrailFailure(err)
		h.recordDenial(r.Context(), auth.TeamFromContext(r.Context()), auth.VirtualKeyFromContext(r.Context()), g.alias, g.requestID, failure.status, failure.code)
		writeJSONError(w, failure.status, failure.code, "guardrail rejected request; protected policies support plain-text chat")
		return nil, false
	}
	return out, true
}

// Decode the newly serialized envelope so provider MarshalJSON cannot restore
// the original unredacted raw response.
func guardedResponse(g *guardrailRun, raw any) ([]byte, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, g.finish(err)
	}
	return g.response(b, false)
}
