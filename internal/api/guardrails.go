package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/guardrails"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5"
)

// ListGuardrails projects the bounded administrator catalog and decision counts.
func (h *AdminHandler) ListGuardrails(w http.ResponseWriter, r *http.Request) {
	if !guardrailAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, err := h.Store.ListGuardrailCatalog(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load guardrail policies")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *AdminHandler) GetGuardrailScope(w http.ResponseWriter, r *http.Request) {
	if !guardrailAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	raw, err := h.Store.GetGuardrailScope(ctx, chi.URLParam(r, "scope"), chi.URLParam(r, "subject"))
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "scope not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "guardrail_policy_unavailable", "could not load scope policies")
		return
	}
	writeJSON(w, 200, raw)
}

func guardrailAdmin(w http.ResponseWriter, r *http.Request) bool {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil || !p.IsAdmin() {
		writeJSONError(w, 403, "forbidden", "administrator required")
		return false
	}
	return true
}

// PUT atomically replaces one scope's assignment. Add/edit/delete are changes
// to this bounded array. expected provides compare-and-swap against lost edits.
func (h *AdminHandler) SetGuardrailScope(w http.ResponseWriter, r *http.Request) {
	if !guardrailAdmin(w, r) {
		return
	}
	var body struct {
		Expected json.RawMessage `json:"expected"`
		Policies json.RawMessage `json:"policies"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(new(any)) != io.EOF || !json.Valid(body.Expected) {
		writeJSONError(w, 400, "invalid_request", "expected and policies are required JSON values")
		return
	}
	if _, err := guardrails.Decode(body.Policies); err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	scope, id := chi.URLParam(r, "scope"), chi.URLParam(r, "subject")
	if (scope != "team" && scope != "alias") || id == "" || len(id) > 256 {
		writeJSONError(w, 400, "invalid_request", "invalid scope")
		return
	}
	p := auth.PrincipalFromContext(r.Context())
	actor := "master_key"
	if p.User != nil {
		actor = strconv.FormatInt(p.User.ID, 10)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	err := h.Store.ReplaceGuardrailScope(ctx, scope, id, body.Expected, body.Policies, actor)
	if errors.Is(err, store.ErrGuardrailConflict) {
		writeJSONError(w, 409, "conflict", err.Error())
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, 404, "not_found", "scope not found")
		return
	}
	if err != nil {
		writeJSONError(w, 503, "guardrail_policy_unavailable", "could not save policies and audit")
		return
	}
	writeJSON(w, 200, body.Policies)
}

// ListGuardrailAssignments projects every scope with guardrails assigned so
// the console can show the whole enforcement surface in one table.
func (h *AdminHandler) ListGuardrailAssignments(w http.ResponseWriter, r *http.Request) {
	if !guardrailAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	rows, err := h.Store.ListGuardrailAssignments(ctx)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", "could not load guardrail assignments")
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

type guardrailTestResult struct {
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Phase    string `json:"phase"`
	Decision string `json:"decision"` // allow, block, redact, flag, or skipped when the phase doesn't apply
}

type guardrailTestResponse struct {
	Decision string                `json:"decision"` // strongest outcome across rules
	Output   string                `json:"output"`   // text after redactions, empty when blocked
	Results  []guardrailTestResult `json:"results"`
}

// TestGuardrails evaluates sample text against either draft policies or a
// scope's saved policies, one rule at a time so every rule's verdict is
// visible (the live filter stops at the first block). Nothing is stored
// and no upstream is called.
func (h *AdminHandler) TestGuardrails(w http.ResponseWriter, r *http.Request) {
	if !guardrailAdmin(w, r) {
		return
	}
	var body struct {
		Text      string          `json:"text"`
		Phase     string          `json:"phase"`
		ScopeType string          `json:"scope_type"`
		ScopeID   string          `json:"scope_id"`
		Policies  json.RawMessage `json:"policies"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		writeJSONError(w, 400, "invalid_request", "text and either policies or a scope are required")
		return
	}
	if body.Phase == "" {
		body.Phase = "pre"
	}
	if body.Phase != "pre" && body.Phase != "post" {
		writeJSONError(w, 400, "invalid_request", "phase must be pre or post")
		return
	}
	raw := body.Policies
	if len(raw) == 0 || string(raw) == "null" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		scoped, err := h.Store.GetGuardrailScope(ctx, body.ScopeType, body.ScopeID)
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, 404, "not_found", "scope not found")
			return
		}
		if err != nil {
			writeJSONError(w, 503, "guardrail_policy_unavailable", "could not load scope policies")
			return
		}
		raw = scoped
	}
	policies, err := guardrails.Decode(raw)
	if err != nil {
		writeJSONError(w, 400, "invalid_request", err.Error())
		return
	}
	rank := map[string]int{"block": 3, "redact": 2, "flag": 1}
	resp := guardrailTestResponse{Decision: "allow", Results: []guardrailTestResult{}}
	passable := []guardrails.Policy{}
	for _, p := range policies {
		res := guardrailTestResult{Name: p.Name, Mode: p.Mode, Phase: p.Phase, Decision: "skipped"}
		if p.Phase == body.Phase || p.Phase == "both" {
			f := guardrails.New([]guardrails.Policy{p}, body.Phase)
			_, err := f.Text(body.Text)
			switch {
			case errors.Is(err, guardrails.ErrBlocked):
				res.Decision = "block"
			case err != nil:
				writeJSONError(w, 400, "invalid_request", err.Error())
				return
			default:
				res.Decision = f.Results()[0].Decision
			}
			if p.Mode != "block" {
				passable = append(passable, p)
			}
		}
		if rank[res.Decision] > rank[resp.Decision] {
			resp.Decision = res.Decision
		}
		resp.Results = append(resp.Results, res)
	}
	if resp.Decision != "block" {
		out, err := guardrails.New(passable, body.Phase).Text(body.Text)
		if err != nil {
			writeJSONError(w, 400, "invalid_request", err.Error())
			return
		}
		resp.Output = out
	}
	writeJSON(w, http.StatusOK, resp)
}
