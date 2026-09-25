package api

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, code int, errType, msg string) {
	writeJSON(w, code, map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    errType,
		},
	})
}

// parsePagination reads ?limit and ?offset from the request. Defaults and
// upper bounds are enforced by store.NormalizePage; this helper just parses.
func parsePagination(r *http.Request) (limit, offset int) {
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	return limit, offset
}

// setTotalCount writes the X-Total-Count response header. The frontend
// pagination component reads this to render page indicators.
func setTotalCount(w http.ResponseWriter, total int64) {
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
}
