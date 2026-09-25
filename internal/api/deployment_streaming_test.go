package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
)

func TestAdminDeploymentStreamingRoundTripPreserveAndClear(t *testing.T) {
	e := newTestEnv(t)
	name := "stream-policy-" + randHex(4)
	code, body := e.POST("/admin/deployments", e.MasterKey, map[string]any{"name": name, "provider_type": "openai_compatible", "upstream_model": "local", "streaming": map[string]string{"idle_timeout": "250ms", "keepalive_interval": "20ms"}})
	if code != http.StatusCreated {
		t.Fatalf("%d %s", code, body)
	}
	for _, patch := range []map[string]any{{"max_parallel_requests": 1}, {"streaming": map[string]string{"idle_timeout": "250ms", "keepalive_interval": "20ms"}}} {
		code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, patch)
		var result DeploymentResponse
		if err := json.Unmarshal(body, &result); err != nil || code != 200 || result.Streaming == nil || result.Streaming.IdleTimeout != 250*time.Millisecond || result.Streaming.KeepaliveInterval != 20*time.Millisecond {
			t.Fatalf("%d %s %v", code, body, err)
		}
	}
	for _, patch := range []any{map[string]string{"idle_timeout": "-1s"}, map[string]string{"keepalive_interval": "1ns"}, map[string]int{"idle_timeout": 5}} {
		code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{"streaming": patch})
		if code != 400 {
			t.Fatalf("invalid accepted: %d %s", code, body)
		}
	}
	dep, err := e.Store.GetDeploymentByName(context.Background(), name)
	if err != nil || dep.Streaming == nil || dep.Streaming.IdleTimeout != 250*time.Millisecond {
		t.Fatalf("invalid update mutated policy: %+v %v", dep, err)
	}
	code, body = e.PATCH("/admin/deployments/"+name, e.MasterKey, map[string]any{"streaming": nil})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	dep, err = e.Store.GetDeploymentByName(context.Background(), name)
	if err != nil || dep.Streaming != nil {
		t.Fatalf("clear did not restore inheritance: %+v %v", dep, err)
	}
}

func TestDeploymentStreamingRuntimeRefresh(t *testing.T) {
	f := newChatFixture(t)
	override := &config.StreamingConfig{FirstEventTimeout: 25 * time.Millisecond, IdleTimeout: 50 * time.Millisecond, WriteTimeout: 75 * time.Millisecond}
	if _, err := f.Store.UpsertDeployment(context.Background(), f.DepName, "openai", f.UpModel, f.CredEnv, &f.BaseURL, nil, nil, nil, override); err != nil {
		t.Fatal(err)
	}
	if err := f.Registry.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.Handler.streamingFor(f.DepName); got != *override {
		t.Fatalf("policy not applied: %+v", got)
	}
}
