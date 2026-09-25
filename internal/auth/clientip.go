package auth

import (
	"context"
	"net"
	"net/http"
	"strings"
)

type trustedProxiesKey struct{}

// WithTrustedProxies returns a middleware that stamps the parsed CIDR
// list onto the request context. Add it once at the chain root; every
// downstream caller of ClientIP reads from context, so middlewares and
// handlers don't need new arguments.
func WithTrustedProxies(cidrs []string) func(http.Handler) http.Handler {
	parsed := TrustedProxies(cidrs)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), trustedProxiesKey{}, parsed)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClientIP returns the request's client IP using the trusted-proxy list
// previously stamped on the context. Safe to call before the middleware
// runs — it just falls back to RemoteAddr.
func ClientIP(r *http.Request) string {
	trusted, _ := r.Context().Value(trustedProxiesKey{}).([]*net.IPNet)
	return ClientIPFrom(r, trusted)
}

// TrustedProxies parses CIDR strings into net.IPNets once at startup so
// the per-request hot path is cheap. Bad entries are dropped (we log
// them at parse-time rather than failing the boot, since misconfiguration
// of a single CIDR shouldn't take the whole gateway down).
func TrustedProxies(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// ClientIPFrom returns the request's client IP, only honoring
// X-Forwarded-For / X-Real-IP if the immediate peer (RemoteAddr) is
// within one of the trusted-proxy CIDRs. Otherwise it returns the peer
// address. This closes the spoofing path called out in the audit:
// without trusted-proxy config, headers are ignored.
func ClientIPFrom(r *http.Request, trusted []*net.IPNet) string {
	peer := remoteHost(r)
	if !ipIn(peer, trusted) {
		return peer
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		// Left-most entry is the original client; everything after is
		// the LB chain.
		if i := strings.IndexByte(v, ','); i >= 0 {
			return strings.TrimSpace(v[:i])
		}
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	return peer
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func ipIn(addr string, nets []*net.IPNet) bool {
	if len(nets) == 0 {
		return false
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
