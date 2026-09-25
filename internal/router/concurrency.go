package router

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gatemux-dev/gatemux/internal/concurrency"
	"github.com/gatemux-dev/gatemux/internal/store"
)

const ConcurrencyPolicyRefreshInterval = 5 * time.Second
const ConcurrencyPolicyMaxAge = 15 * time.Second

type concurrencyPolicies struct {
	mu        sync.RWMutex
	refreshMu sync.Mutex
	byScope   map[string]map[string]store.RoutingConcurrencyLimit
	loadedAt  time.Time
}

// RefreshConcurrencyPolicies serializes loads so an older concurrent query
// cannot overwrite a more recent policy. Requests only read the snapshot.
func (r *Registry) RefreshConcurrencyPolicies(ctx context.Context) error {
	p := &r.concurrencyPolicies
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	rows, err := r.store.ListRoutingConcurrencyLimits(ctx)
	if err != nil {
		return err
	}
	customers, err := r.store.ListCustomerConcurrencyLimits(ctx)
	if err != nil {
		return err
	}
	rows = append(rows, customers...)
	byScope := map[string]map[string]store.RoutingConcurrencyLimit{}
	for _, row := range rows {
		if row.MaxParallelRequests == nil {
			continue
		}
		if byScope[row.Scope] == nil {
			byScope[row.Scope] = map[string]store.RoutingConcurrencyLimit{}
		}
		byScope[row.Scope][row.Subject] = row
	}
	p.mu.Lock()
	p.byScope, p.loadedAt = byScope, time.Now()
	p.mu.Unlock()
	return nil
}

// WatchConcurrencyPolicies runs one bounded, cancelable refresh loop per server.
func (r *Registry) WatchConcurrencyPolicies(ctx context.Context) {
	ticker := time.NewTicker(ConcurrencyPolicyRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			callCtx, cancel := context.WithTimeout(ctx, time.Second)
			err := r.RefreshConcurrencyPolicies(callCtx)
			cancel()
			if err != nil && ctx.Err() == nil && r.log != nil {
				r.log.Warn("concurrency policy refresh failed", "err", err)
			}
		}
	}
}

// ConcurrencyScope rejects stale policy snapshots, including formerly empty
// ones: another replica may have configured a cap during a database outage.
func (r *Registry) ConcurrencyScope(kind, subject string) ([]concurrency.Scope, error) {
	p := &r.concurrencyPolicies
	p.mu.RLock()
	defer p.mu.RUnlock()
	// A registry constructed directly in a unit test has no backing store.
	if r.store != nil && (p.loadedAt.IsZero() || time.Since(p.loadedAt) > ConcurrencyPolicyMaxAge) {
		return nil, fmt.Errorf("concurrency policy snapshot is stale")
	}
	row, exists := p.byScope[kind][subject]
	if !exists {
		return nil, nil
	}
	return []concurrency.Scope{{Kind: kind, ID: row.ID, Limit: *row.MaxParallelRequests}}, nil
}
