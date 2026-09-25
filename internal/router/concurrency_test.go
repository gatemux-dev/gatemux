package router

import (
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

func TestConcurrencyPoliciesFailClosedWhenStaleIncludingEmptySnapshot(t *testing.T) {
	limit := 2
	r := &Registry{store: &store.Store{}}
	r.concurrencyPolicies.loadedAt = time.Now()
	r.concurrencyPolicies.byScope = map[string]map[string]store.RoutingConcurrencyLimit{
		"model": {"chat": {ID: 7, MaxParallelRequests: &limit}},
	}
	scopes, err := r.ConcurrencyScope("model", "chat")
	if err != nil || len(scopes) != 1 || scopes[0].Limit != 2 {
		t.Fatalf("fresh policy: %+v %v", scopes, err)
	}
	r.concurrencyPolicies.loadedAt = time.Now().Add(-ConcurrencyPolicyMaxAge - time.Second)
	for _, subject := range []string{"chat", "not-configured"} {
		if _, err := r.ConcurrencyScope("model", subject); err == nil {
			t.Fatalf("stale %s policy was accepted", subject)
		}
	}
}
