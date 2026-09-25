package config

import "testing"

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
