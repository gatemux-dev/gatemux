package api

import (
	"context"
	"time"
)

const settlementTimeout = 2 * time.Second

// Must be directly deferred after budget admission; preserve the original panic
// for HTTP recovery while reconciling with a fresh, bounded cleanup context.
func (h *V1Handler) reconcileBudgetOnPanic(requestID string) {
	if cause := recover(); cause != nil {
		// The request's accounting defer owns atomic usage + settlement when
		// logging is enabled. Never settle independently ahead of that transaction.
		if h.Budget != nil && h.Usage == nil {
			ctx, cancel := context.WithTimeout(context.Background(), settlementTimeout)
			_, err := h.Budget.SettleEstimated(ctx, requestID)
			cancel()
			if err != nil && h.Logger != nil {
				h.Logger.Error("panic budget reconciliation failed", "request_id", requestID, "err", err)
			}
		}
		panic(cause)
	}
}
