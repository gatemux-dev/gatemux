package config

import (
	"encoding/json"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestStreamingWireAndInheritance(t *testing.T) {
	base := StreamingConfig{FirstEventTimeout: time.Second, IdleTimeout: 2 * time.Second, WriteTimeout: 3 * time.Second, KeepaliveInterval: time.Second}
	var override StreamingConfig
	if err := json.Unmarshal([]byte(`{"idle_timeout":"250ms"}`), &override); err != nil {
		t.Fatal(err)
	}
	got := base.WithOverride(&override)
	if got.FirstEventTimeout != time.Second || got.IdleTimeout != 250*time.Millisecond || got.WriteTimeout != 3*time.Second || got.KeepaliveInterval != 0 {
		t.Fatal(got)
	}
	if base.WithOverride(nil) != base {
		t.Fatal("nil did not inherit")
	}
	b, _ := json.Marshal(got)
	var roundTrip StreamingConfig
	if err := json.Unmarshal(b, &roundTrip); err != nil || roundTrip != got {
		t.Fatalf("%s: %v", b, err)
	}
	var dep DeploymentConfig
	if err := yaml.Unmarshal([]byte("name: test\nstreaming:\n  idle_timeout: 250ms\n  keepalive_interval: 15s\n"), &dep); err != nil || dep.Streaming == nil || dep.Streaming.IdleTimeout != 250*time.Millisecond {
		t.Fatalf("yaml: %+v %v", dep, err)
	}
}

func TestStreamingRejectsInvalidJSONPolicy(t *testing.T) {
	for _, input := range []string{`{"idle_timeout":12}`, `{"idle_timeout":"-1s"}`, `{"write_timeout":"bad"}`, `{"first_event_timeout":"25h"}`, `{"keepalive_interval":"1ns"}`, `{"idl_timeout":"1s"}`} {
		var cfg StreamingConfig
		if err := json.Unmarshal([]byte(input), &cfg); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}
