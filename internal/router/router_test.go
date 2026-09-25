package router

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/admission"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

type stubProvider struct{}

func (stubProvider) Type() string { return "stub" }

func (stubProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: true}
}

func (stubProvider) ChatCompletion(context.Context, *providers.ChatRequest) (*providers.ChatResponse, error) {
	return nil, nil
}

func (stubProvider) ChatCompletionStream(context.Context, *providers.ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (stubProvider) Embeddings(context.Context, *providers.EmbeddingRequest) (*providers.EmbeddingResponse, error) {
	return nil, nil
}

func TestResolveNextWeightedRoundRobin(t *testing.T) {
	r := &Registry{
		aliases: map[string][]aliasTarget{
			"smart-chat": {
				{DeploymentName: "dep-a", Priority: 0, Weight: 2},
				{DeploymentName: "dep-b", Priority: 0, Weight: 1},
			},
		},
		deployments: map[string]*store.Deployment{
			"dep-a": {Name: "dep-a", ProviderType: "openai", UpstreamModel: "gpt-4o", Enabled: true},
			"dep-b": {Name: "dep-b", ProviderType: "openai", UpstreamModel: "gpt-4.1-mini", Enabled: true},
		},
		providers: map[string]providers.Provider{
			"dep-a": stubProvider{},
			"dep-b": stubProvider{},
		},
		rr:       map[string]int{},
		breakers: map[string]*breakerState{},
	}

	got := make([]string, 0, 6)
	for range 6 {
		resolved, err := r.ResolveNext("smart-chat", nil)
		if err != nil {
			t.Fatalf("ResolveNext returned error: %v", err)
		}
		got = append(got, resolved.DeploymentName)
	}

	want := []string{"dep-a", "dep-a", "dep-b", "dep-a", "dep-a", "dep-b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("selection %d = %q, want %q (full sequence %v)", i, got[i], want[i], got)
		}
	}
}

func TestResolveNextSkipsOpenCircuit(t *testing.T) {
	r := &Registry{
		aliases: map[string][]aliasTarget{
			"smart-chat": {
				{DeploymentName: "primary", Priority: 0, Weight: 1},
				{DeploymentName: "secondary", Priority: 1, Weight: 1},
			},
		},
		deployments: map[string]*store.Deployment{
			"primary":   {Name: "primary", ProviderType: "openai", UpstreamModel: "gpt-4o", Enabled: true},
			"secondary": {Name: "secondary", ProviderType: "anthropic", UpstreamModel: "claude-sonnet-4", Enabled: true},
		},
		providers: map[string]providers.Provider{
			"primary":   stubProvider{},
			"secondary": stubProvider{},
		},
		rr:       map[string]int{},
		breakers: map[string]*breakerState{},
	}

	retryable := &providers.UpstreamError{Provider: "openai", StatusCode: http.StatusServiceUnavailable, Message: "unavailable"}
	for range breakerFailureThreshold {
		r.RecordFailure("primary", retryable)
	}

	resolved, err := r.ResolveNext("smart-chat", nil)
	if err != nil {
		t.Fatalf("ResolveNext returned error: %v", err)
	}
	if resolved.DeploymentName != "secondary" {
		t.Fatalf("ResolveNext picked %q, want secondary", resolved.DeploymentName)
	}

	health := r.Health()
	if len(health) != 2 {
		t.Fatalf("Health len = %d, want 2", len(health))
	}
	if health[0].Name == "primary" {
		if health[0].Circuit != "open" {
			t.Fatalf("primary circuit = %q, want open", health[0].Circuit)
		}
		if health[0].OpenUntil == nil || health[0].OpenUntil.Before(time.Now()) {
			t.Fatalf("primary OpenUntil = %v, want future timestamp", health[0].OpenUntil)
		}
		return
	}
	if health[1].Name == "primary" {
		if health[1].Circuit != "open" {
			t.Fatalf("primary circuit = %q, want open", health[1].Circuit)
		}
		if health[1].OpenUntil == nil || health[1].OpenUntil.Before(time.Now()) {
			t.Fatalf("primary OpenUntil = %v, want future timestamp", health[1].OpenUntil)
		}
		return
	}
	t.Fatalf("primary deployment missing from health output: %+v", health)
}

type chatOnlyProvider struct{ stubProvider }

func (chatOnlyProvider) Capabilities() providers.Capabilities {
	return providers.Capabilities{Chat: true, StreamChat: true, Embeddings: false}
}

func TestResolveNextForEmbeddingsSkipsChatOnlyProviders(t *testing.T) {
	r := &Registry{
		aliases: map[string][]aliasTarget{
			"embed-any": {
				{DeploymentName: "anthropic", Priority: 0, Weight: 1},
				{DeploymentName: "openai", Priority: 0, Weight: 1},
			},
		},
		deployments: map[string]*store.Deployment{
			"anthropic": {Name: "anthropic", ProviderType: "anthropic", UpstreamModel: "claude", Enabled: true},
			"openai":    {Name: "openai", ProviderType: "openai", UpstreamModel: "text-embedding-3-small", Enabled: true},
		},
		providers: map[string]providers.Provider{
			"anthropic": chatOnlyProvider{},
			"openai":    stubProvider{},
		},
		rr:       map[string]int{},
		breakers: map[string]*breakerState{},
	}

	resolved, err := r.ResolveNextFor("embed-any", nil, providers.CapabilityEmbeddings)
	if err != nil {
		t.Fatalf("ResolveNextFor returned error: %v", err)
	}
	if resolved.DeploymentName != "openai" {
		t.Fatalf("ResolveNextFor picked %q, want openai", resolved.DeploymentName)
	}
}

func TestDeploymentConcurrencyPermitEnforcesLimitAndReleases(t *testing.T) {
	limit := 1
	r := &Registry{
		deployments: map[string]*store.Deployment{
			"primary": {Name: "primary", ProviderType: "openai", Enabled: true, MaxParallelRequests: &limit},
		},
		providers: map[string]providers.Provider{"primary": stubProvider{}},
		breakers:  map[string]*breakerState{},
		deploymentLimits: map[string]*admission.Counter{
			"primary": admission.NewCounter(1),
		},
	}

	first, state, ok := r.TryAcquireDeployment("primary")
	if !ok {
		t.Fatal("first acquisition was rejected")
	}
	if state.InFlight != 1 || state.Limit != 1 {
		t.Fatalf("first state = %+v, want in_flight=1 limit=1", state)
	}
	if _, state, ok := r.TryAcquireDeployment("primary"); ok {
		t.Fatal("second acquisition succeeded above the configured limit")
	} else if state.InFlight != 1 || state.Limit != 1 {
		t.Fatalf("saturated state = %+v, want in_flight=1 limit=1", state)
	}

	health := r.Health()
	if len(health) != 1 || health[0].InFlight != 1 || health[0].MaxParallelRequests != 1 {
		t.Fatalf("health = %+v, want concurrency state", health)
	}

	first.Release()
	if state := r.DeploymentConcurrency("primary"); state.InFlight != 0 {
		t.Fatalf("state after release = %+v, want in_flight=0", state)
	}
	second, _, ok := r.TryAcquireDeployment("primary")
	if !ok {
		t.Fatal("acquisition after release was rejected")
	}
	second.Release()
}
