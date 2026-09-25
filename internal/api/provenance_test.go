package api

import (
	"testing"

	"github.com/gatemux-dev/gatemux/internal/config"
)

// Entries from the config file are re-applied at start, so the console must
// be told which ones console edits won't survive.
func TestConfigProvenance(t *testing.T) {
	h := &AdminHandler{Config: &config.Config{
		Deployments: []config.DeploymentConfig{{Name: "from-file"}},
		Aliases:     []config.AliasConfig{{Alias: "file-alias"}},
	}}
	if got := h.withDeploymentSource(DeploymentResponse{Name: "from-file"}).ManagedBy; got != "config" {
		t.Fatalf("config deployment managed_by = %q", got)
	}
	if got := h.withDeploymentSource(DeploymentResponse{Name: "from-console"}).ManagedBy; got != "" {
		t.Fatalf("console deployment managed_by = %q", got)
	}
	if got := h.withAliasSource(AliasResponse{Alias: "file-alias"}).ManagedBy; got != "config" {
		t.Fatalf("config alias managed_by = %q", got)
	}
	if got := (&AdminHandler{}).withAliasSource(AliasResponse{Alias: "x"}).ManagedBy; got != "" {
		t.Fatalf("nil config managed_by = %q", got)
	}
}
