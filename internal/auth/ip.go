package auth

import (
	"net"
	"net/http"
	"strings"
)

// clientIP delegates to ClientIP, which reads the trusted-proxy CIDR
// list from request context. Kept as a tiny shim so existing callers
// inside this package don't change shape; new code should call
// ClientIP directly.
func clientIP(r *http.Request) string { return ClientIP(r) }

// ipAllowed returns true when ip matches at least one CIDR in the list.
// CIDRs that fail to parse are skipped (operator typo shouldn't lock
// people out completely without warning, but we still don't allow on
// parse failure).
func ipAllowed(ip string, cidrs []string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, c := range cidrs {
		// Bare IP is treated as a /32 or /128 single-host match.
		if !strings.Contains(c, "/") {
			if net.ParseIP(c).Equal(parsed) {
				return true
			}
			continue
		}
		_, network, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		if network.Contains(parsed) {
			return true
		}
	}
	return false
}
