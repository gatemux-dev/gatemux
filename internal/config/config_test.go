package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestApplyDefaultsAdmission(t *testing.T) {
	var cfg Config
	cfg.applyDefaults()
	if got, want := cfg.Server.Admission.MaxInFlight, 1024; got != want {
		t.Fatalf("max in flight = %d, want %d", got, want)
	}
	if got, want := cfg.Server.Admission.MaxQueued, 256; got != want {
		t.Fatalf("max queued = %d, want %d", got, want)
	}
	if got, want := cfg.Server.Admission.QueueTimeout, 250*time.Millisecond; got != want {
		t.Fatalf("queue timeout = %s, want %s", got, want)
	}
}

func TestShutdownDefaultsAndBounds(t *testing.T) {
	var cfg Config
	cfg.applyDefaults()
	if got := cfg.Server.Shutdown; got != (ShutdownConfig{GracePeriod: 30 * time.Second, CleanupTimeout: 10 * time.Second}) {
		t.Fatalf("shutdown defaults: %+v", got)
	}
	for _, policy := range []ShutdownConfig{{GracePeriod: -1}, {CleanupTimeout: -1}, {GracePeriod: 25 * time.Hour}, {CleanupTimeout: 6 * time.Minute}} {
		if err := policy.Validate(); err == nil {
			t.Fatalf("invalid shutdown accepted: %+v", policy)
		}
	}
	cfg.Callbacks = make([]CallbackConfig, 33)
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "32 callbacks") {
		t.Fatalf("callback bound: %v", err)
	}
}

func TestValidateRejectsNegativeAdmissionLimits(t *testing.T) {
	cfg := Config{Server: ServerConfig{Admission: AdmissionConfig{MaxInFlight: -1}}}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "max_in_flight") {
		t.Fatalf("validate error = %v, want max_in_flight error", err)
	}
}

func TestLoadAdmissionYAML(t *testing.T) {
	t.Setenv("TEST_GATEMUX_ADMIN_KEY", "test-secret")
	path := filepath.Join(t.TempDir(), "config.yaml")
	contents := []byte(`
server:
  shutdown:
    grace_period: 7s
    cleanup_timeout: 4s
  streaming:
    first_event_timeout: 12s
    idle_timeout: 9s
    write_timeout: 3s
  admission:
    max_in_flight: 77
    max_queued: 11
    queue_timeout: 125ms
database:
  driver: postgres
  dsn: postgres://example.invalid/gatemux
admin:
  master_key_env: TEST_GATEMUX_ADMIN_KEY
deployments:
  - name: primary
    type: openai
    upstream_model: gpt-4o
    api_key_env: OPENAI_API_KEY
    max_parallel_requests: 37
`)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Server.Admission.MaxInFlight; got != 77 {
		t.Fatalf("max in flight = %d, want 77", got)
	}
	if got := cfg.Server.Shutdown; got != (ShutdownConfig{GracePeriod: 7 * time.Second, CleanupTimeout: 4 * time.Second}) {
		t.Fatalf("shutdown YAML: %+v", got)
	}
	if got := cfg.Server.Admission.MaxQueued; got != 11 {
		t.Fatalf("max queued = %d, want 11", got)
	}
	if got := cfg.Server.Admission.QueueTimeout; got != 125*time.Millisecond {
		t.Fatalf("queue timeout = %s, want 125ms", got)
	}
	if got := cfg.Deployments[0].MaxParallelRequests; got != 37 {
		t.Fatalf("deployment max parallel requests = %d, want 37", got)
	}
	if got := cfg.Server.Streaming; got != (StreamingConfig{FirstEventTimeout: 12 * time.Second, IdleTimeout: 9 * time.Second, WriteTimeout: 3 * time.Second}) {
		t.Fatalf("streaming settings = %+v", got)
	}
}

func TestStreamingDefaultsAndInvalidTimeouts(t *testing.T) {
	var cfg Config
	cfg.applyDefaults()
	if got := cfg.Server.Streaming; got != (StreamingConfig{FirstEventTimeout: 30 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 15 * time.Second}) {
		t.Fatalf("streaming defaults = %+v", got)
	}
	for _, streaming := range []StreamingConfig{{FirstEventTimeout: -1}, {IdleTimeout: -1}, {WriteTimeout: -1}} {
		cfg := Config{Server: ServerConfig{Streaming: streaming}}
		cfg.applyDefaults()
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "server.streaming") {
			t.Fatalf("negative streaming timeout accepted: %v", err)
		}
	}
}

func TestValidateRejectsNegativeDeploymentConcurrency(t *testing.T) {
	t.Setenv("TEST_GATEMUX_ADMIN_KEY", "test-secret")
	cfg := Config{
		Database: DatabaseConfig{Driver: "postgres", DSN: "postgres://example.invalid/gatemux"},
		Admin:    AdminConfig{MasterKeyEnv: "TEST_GATEMUX_ADMIN_KEY"},
		Deployments: []DeploymentConfig{{
			Name: "primary", Type: "openai", UpstreamModel: "gpt-4o",
			APIKeyEnv: "OPENAI_API_KEY", MaxParallelRequests: -1,
		}},
	}
	err := cfg.validate()
	if err == nil || !strings.Contains(err.Error(), "max_parallel_requests") {
		t.Fatalf("validate error = %v, want max_parallel_requests error", err)
	}
}
