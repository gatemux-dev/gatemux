package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
	"github.com/go-chi/chi/v5/middleware"
)

type accountingKey struct{}
type accountingNonBillableKey struct{}
type accountingRun struct {
	entry             store.UsageEntry
	pending           *store.UsageEntry
	complete, started bool
}

func requestAccounting(ctx context.Context) *accountingRun {
	run, _ := ctx.Value(accountingKey{}).(*accountingRun)
	return run
}

func (h *V1Handler) beginAccounting(w http.ResponseWriter, r *http.Request, alias string) (func(), bool) {
	if h.Usage == nil {
		return func() {}, true
	}
	if err := h.Usage.Ready(); err != nil {
		writeJSONError(w, 503, "accounting_unavailable", "usage recovery is pending; inference is paused")
		return nil, false
	}
	ctx := r.Context()
	cancel := func() {}
	deadline, ok := ctx.Deadline()
	if !ok {
		ctx, cancel = context.WithTimeout(ctx, 90*time.Second)
		deadline, _ = ctx.Deadline()
	}
	id := middleware.GetReqID(ctx)
	if id == "" {
		var entropy [16]byte
		if _, err := rand.Read(entropy[:]); err != nil {
			cancel()
			writeJSONError(w, 503, "accounting_unavailable", "cannot initialize accounting")
			return nil, false
		}
		id = hex.EncodeToString(entropy[:])
		ctx = context.WithValue(ctx, middleware.RequestIDKey, id)
	}
	team := auth.TeamFromContext(ctx)
	if team == nil {
		cancel()
		writeJSONError(w, 401, "authentication_error", "team required")
		return nil, false
	}
	e := store.UsageEntry{RequestID: id, TeamID: team.ID, Alias: alias, ModelRequested: alias, Ts: time.Now().UTC()}
	if nonBillable, _ := ctx.Value(accountingNonBillableKey{}).(bool); nonBillable {
		e.Accounting = "not_billable"
	}
	if key := auth.VirtualKeyFromContext(ctx); key != nil {
		if key.ID > 0 {
			e.KeyID = &key.ID
		}
		e.UserID = key.UserID
		e.ServiceAccountID = key.ServiceAccountID
	}
	attributeCustomer(ctx, &e)
	work, done := context.WithTimeout(ctx, 2*time.Second)
	err := h.Usage.Store().BeginAccounting(work, e, time.Until(deadline))
	done()
	if err != nil {
		cancel()
		status, code := 503, "accounting_unavailable"
		if errors.Is(err, store.ErrAccountingConflict) {
			status, code = 409, "request_id_reused"
		}
		writeJSONError(w, status, code, "cannot admit request without a fresh durable accounting record")
		return nil, false
	}
	run := &accountingRun{entry: e}
	*r = *r.WithContext(context.WithValue(ctx, accountingKey{}, run))
	return func() {
		defer cancel()
		if !run.complete {
			entry := run.entry
			if entry.Accounting == "" {
				entry.Accounting = "unknown"
			}
			entry.StatusCode = 503
			entry.Error = "request_interrupted_or_unrecorded"
			if run.pending != nil {
				entry = *run.pending
			}
			_, _ = h.persistUsage(r.Context(), entry)
		}
	}, true
}

// A stored Responses read/delete does not generate tokens. Record the operation
// status, without charging the usage counters in the retrieved historic body.
type accountingResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *accountingResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *accountingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *accountingResponseWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (h *V1Handler) markAccountingStarted(ctx context.Context) error {
	run := requestAccounting(ctx)
	if run == nil || run.started {
		return nil
	}
	work, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.Usage.Store().MarkAccountingStarted(work, run.entry.RequestID); err != nil {
		return &accountingError{}
	}
	run.started = true
	return nil
}

type accountingError struct{}

func (*accountingError) Error() string { return "durable accounting unavailable before upstream call" }

func (h *V1Handler) persistUsage(ctx context.Context, e store.UsageEntry) (store.UsageReceipt, error) {
	run := requestAccounting(ctx)
	if run != nil {
		e.AccountingID = run.entry.RequestID
		run.pending = &e
	}
	receipt, err := h.Usage.Record(e)
	if run != nil && err == nil {
		run.complete = true
		run.pending = nil
	}
	return receipt, err
}
