package config

import (
	"os"
	"testing"
)

func TestRenameEnvironmentCompatibility(t *testing.T) {
	const name = "GATEMUX_RENAME_TEST"
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AIPORT_RENAME_TEST", "legacy")
	if Env(name) != "legacy" {
		t.Fatal("legacy fallback lost")
	}
	t.Setenv(name, "current")
	if Env(name) != "current" {
		t.Fatal("canonical precedence lost")
	}
	t.Setenv(name, "")
	if Env(name) != "" {
		t.Fatal("explicit empty canonical value must not fall back")
	}
	if Env("AIPORT_RENAME_TEST") != "legacy" {
		t.Fatal("explicit old reference lost")
	}
	t.Setenv("PROVIDER_RENAME_TEST", "provider")
	if Env("PROVIDER_RENAME_TEST") != "provider" {
		t.Fatal("literal provider reference changed")
	}
}

func TestRenamePreservesDefaultStorageIdentity(t *testing.T) {
	var cfg Config
	cfg.applyDefaults()
	if cfg.Redis.KeyPrefix != "aiport" || cfg.Telemetry.ServiceName != "gatemux" {
		t.Fatal("storage and display identities must remain separate")
	}
	cfg.Redis.KeyPrefix = "custom-existing"
	cfg.applyDefaults()
	if cfg.Redis.KeyPrefix != "custom-existing" {
		t.Fatal("explicit prefix changed")
	}
}
