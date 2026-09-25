package config

import (
	"os"
	"strings"
)

// Env reads the configured name literally. New GateMux names also accept the
// pre-rename alias when unset; an explicitly empty new value does not fall back.
// Provider credential references remain exact and are not rewritten.
func Env(name string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	if strings.HasPrefix(name, "GATEMUX_") {
		return os.Getenv("AIPORT_" + strings.TrimPrefix(name, "GATEMUX_"))
	}
	return ""
}
