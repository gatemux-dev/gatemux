package ratelimit

import (
	"context"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func intPtr(v int) *int { return &v }

func TestEstimateChatTokens(t *testing.T) {
	req := &providers.ChatRequest{
		Messages: []providers.ChatMessage{
			{Role: "user", Content: "hello world"},
			{Role: "assistant", Content: "ack"},
		},
		MaxTokens: intPtr(50),
	}
	got := EstimateChatTokens(req)
	if got <= 50 {
		t.Fatalf("EstimateChatTokens() = %d, want prompt contribution plus max_tokens", got)
	}
}

func TestEstimateEmbeddingTokens(t *testing.T) {
	req := &providers.EmbeddingRequest{
		Input: providers.EmbeddingInput{Texts: []string{"abcd", "abcdefgh"}},
	}
	got := EstimateEmbeddingTokens(req)
	if got != 3 {
		t.Fatalf("EstimateEmbeddingTokens() = %d, want 3", got)
	}
}

func TestMemoryLimiterEnforcesKeyAndTeamLimits(t *testing.T) {
	teamRPM := 2
	teamTPM := 100
	keyRPM := 1
	keyTPM := 20
	limiter := New()

	check := Check{
		Team:   &store.Team{ID: 1, RPM: &teamRPM, TPM: &teamTPM},
		Key:    &store.VirtualKey{ID: 99, ScopedRPM: &keyRPM, ScopedTPM: &keyTPM},
		Tokens: 10,
	}

	result, err := limiter.Check(context.Background(), check)
	if err != nil {
		t.Fatalf("first Check returned error: %v", err)
	}
	if !result.Allowed {
		t.Fatalf("first Check denied unexpectedly: %+v", result)
	}

	result, err = limiter.Check(context.Background(), check)
	if err != nil {
		t.Fatalf("second Check returned error: %v", err)
	}
	if result.Allowed {
		t.Fatalf("second Check allowed unexpectedly")
	}
	if result.Scope != "key" || result.Metric != "rpm" {
		t.Fatalf("second Check denied on %s %s, want key rpm", result.Scope, result.Metric)
	}
}

func TestMemoryLimiterEnforcesTPM(t *testing.T) {
	keyTPM := 12
	limiter := New()

	check := Check{
		Key:    &store.VirtualKey{ID: 1, ScopedTPM: &keyTPM},
		Tokens: 8,
	}
	if result, err := limiter.Check(context.Background(), check); err != nil || !result.Allowed {
		t.Fatalf("first Check = %+v, %v; want allowed", result, err)
	}
	result, err := limiter.Check(context.Background(), check)
	if err != nil {
		t.Fatalf("second Check returned error: %v", err)
	}
	if result.Allowed {
		t.Fatalf("second Check allowed unexpectedly")
	}
	if result.Scope != "key" || result.Metric != "tpm" {
		t.Fatalf("second Check denied on %s %s, want key tpm", result.Scope, result.Metric)
	}
}
