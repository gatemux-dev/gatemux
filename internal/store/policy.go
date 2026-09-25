package store

import (
	"encoding/json"
	"strings"
)

func allowsModel(allowed []string, alias string) bool {
	if alias == "" || len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if candidate == "*" || candidate == alias {
			return true
		}
	}
	return false
}

func parseAllowedModels(raw []byte) []string {
	if len(raw) == 0 {
		return []string{"*"}
	}
	var allowed []string
	if err := json.Unmarshal(raw, &allowed); err != nil {
		return []string{"*"}
	}
	return normalizeAllowedModels(allowed)
}

func normalizeAllowedModels(allowed []string) []string {
	if len(allowed) == 0 {
		return []string{"*"}
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(allowed))
	for _, value := range allowed {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if value == "*" {
			return []string{"*"}
		}
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) == 0 {
		return []string{"*"}
	}
	return out
}

func parseMetadata(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
		return map[string]any{}
	}
	return meta
}
