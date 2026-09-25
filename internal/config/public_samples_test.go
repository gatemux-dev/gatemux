package config

import (
	"os"
	"testing"
)

func TestPublicComposeConfigsLoad(t *testing.T) {
	t.Setenv("GATEMUX_ADMIN_KEY", "fixture-only")
	t.Setenv("OPENAI_API_KEY", "fixture-only")
	t.Setenv("GATEMUX_MOCK_KEY", "fixture-only")
	for _, path := range []string{"../../deploy/docker/config.yaml", "../../deploy/smoke/config.yaml", "../../deploy/docker/legacy-config.yaml"} {
		if _, err := Load(path); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
}

func TestPublicComposeConfigsRejectMissingAdminKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fixture-only")
	t.Setenv("GATEMUX_MOCK_KEY", "fixture-only")
	for _, state := range []string{"unset", "empty"} {
		t.Run(state, func(t *testing.T) {
			for _, name := range []string{"GATEMUX_ADMIN_KEY", "AIPORT_ADMIN_KEY"} {
				t.Setenv(name, "")
				if state == "unset" {
					if err := os.Unsetenv(name); err != nil {
						t.Fatal(err)
					}
				}
			}
			for _, path := range []string{"../../deploy/docker/config.yaml", "../../deploy/smoke/config.yaml", "../../deploy/docker/legacy-config.yaml"} {
				_, err := Load(path)
				if err == nil || err.Error() != `env var "GATEMUX_ADMIN_KEY" (admin.master_key_env) is empty` {
					t.Fatalf("%s: expected missing admin key error, got %v", path, err)
				}
			}
		})
	}
}
