package api

import "net/http"

// gatewayHeader accepts the former brand's client headers. The canonical
// nonempty header takes precedence; no-cache is separately conservative (OR).
func gatewayHeader(r *http.Request, suffix string) string {
	if value := r.Header.Get("X-Gatemux-" + suffix); value != "" {
		return value
	}
	return r.Header.Get("X-Aiport-" + suffix)
}
