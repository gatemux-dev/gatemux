package api

import (
	"net/http"
	"testing"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// TestCapabilities_ChatOnlyDeniesEmbeddings asserts that when a deployment
// is explicitly marked chat-only, /v1/embeddings against an alias backed by
// only that deployment fails before it ever reaches the upstream — i.e.
// admission rejects, not the provider. This is the wrong-shape protection
// you'd want before paying for an upstream call that can't possibly work.
func TestCapabilities_ChatOnlyDeniesEmbeddings(t *testing.T) {
	f := newChatFixture(t)
	f.setDeploymentCapabilities(&store.DeploymentCapabilities{
		Chat: true, StreamChat: true, Embeddings: false,
	})
	team := f.createTeam("chat-only")
	rawKey := f.issueKey(team, nil)

	// Chat works fine — that's the point of having this deployment.
	if code, body := f.chatPOST(rawKey); code != http.StatusOK {
		t.Fatalf("chat on chat-only dep: got %d, want 200 (body=%s)", code, body)
	}

	// Embeddings should NOT work.
	code, body := f.embeddingsPOST(rawKey)
	if code == http.StatusOK {
		t.Fatalf("embeddings on chat-only dep: unexpectedly got 200 (body=%s)", body)
	}
	if code < 400 || code >= 600 {
		t.Errorf("expected an error status, got %d (body=%s)", code, body)
	}
}

// TestCapabilities_EmbeddingsOnlyDeniesChat is the dual: a deployment
// marked embeddings-only should not see chat traffic.
func TestCapabilities_EmbeddingsOnlyDeniesChat(t *testing.T) {
	f := newChatFixture(t)
	f.setDeploymentCapabilities(&store.DeploymentCapabilities{
		Chat: false, StreamChat: false, Embeddings: true,
	})
	team := f.createTeam("embed-only")
	rawKey := f.issueKey(team, nil)

	// Embeddings works.
	if code, body := f.embeddingsPOST(rawKey); code != http.StatusOK {
		t.Fatalf("embeddings on embed-only dep: got %d, want 200 (body=%s)", code, body)
	}

	// Chat should NOT work.
	code, body := f.chatPOST(rawKey)
	if code == http.StatusOK {
		t.Fatalf("chat on embed-only dep: unexpectedly got 200 (body=%s)", body)
	}
	if code < 400 || code >= 600 {
		t.Errorf("expected an error status, got %d (body=%s)", code, body)
	}
}

// TestCapabilities_NullCapsFallsBackToProviderDefaults guards the
// back-compat path: when capabilities is unset (NULL in DB), the router
// uses the provider type's declared defaults — which for openai means
// chat + streaming + embeddings all on. This is the behavior every
// pre-migration deployment relies on.
func TestCapabilities_NullCapsFallsBackToProviderDefaults(t *testing.T) {
	f := newChatFixture(t)
	// Default fixture upserts with capabilities=nil.
	team := f.createTeam("default-caps")
	rawKey := f.issueKey(team, nil)

	if code, body := f.chatPOST(rawKey); code != http.StatusOK {
		t.Errorf("chat with default caps: got %d, want 200 (body=%s)", code, body)
	}
	if code, body := f.embeddingsPOST(rawKey); code != http.StatusOK {
		t.Errorf("embeddings with default caps: got %d, want 200 (body=%s)", code, body)
	}
}
