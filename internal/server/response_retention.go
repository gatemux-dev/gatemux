package server

import (
	"context"
	"github.com/gatemux-dev/gatemux/internal/store"
	"log/slog"
	"time"
)

// One worker per server, one bounded 1000-row statement per second. Expired
// bindings are denied immediately even when physical retention cleanup lags.
func startResponseRetention(ctx context.Context, st *store.Store, logger *slog.Logger) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
				err := st.PruneResponseBindings(bounded)
				cancel()
				if err != nil && ctx.Err() == nil {
					logger.Warn("response metadata retention failed", "error", err)
				}
			}
		}
	}()
	return done
}
