package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/store"
)

type ctxKey int

const (
	ctxKeyTeam ctxKey = iota
	ctxKeyVirtualKey
	ctxKeyOwnerUser
	ctxKeyPrincipal
	ctxKeySessionHash
)

// Principal represents an authenticated caller of the admin/me APIs.
// Either IsMasterKey is true OR User is non-nil (never both, never neither).
type Principal struct {
	IsMasterKey bool
	User        *store.User
}

func (p *Principal) IsAdmin() bool {
	if p == nil {
		return false
	}
	return p.IsMasterKey || (p.User != nil && p.User.Role == store.RoleAdmin)
}

// Role returns the principal's effective role. Master-key principals are
// treated as admin (they're a synthetic super-admin). A nil-or-anonymous
// principal returns "".
func (p *Principal) Role() string {
	if p == nil {
		return ""
	}
	if p.IsMasterKey {
		return store.RoleAdmin
	}
	if p.User == nil {
		return ""
	}
	return p.User.Role
}

// IsManager is true when the principal is a regular user with the manager
// role. Admins / master-key callers are NOT managers — use CanAccessTeam
// instead when you want the union.
func (p *Principal) IsManager() bool {
	return p != nil && p.User != nil && p.User.Role == store.RoleManager
}

// TeamSlug returns the slug of the team this principal belongs to, or ""
// if there isn't one (master-key, admin without a team membership, or a
// member who hasn't been assigned to a team yet).
func (p *Principal) TeamSlug() string {
	if p == nil || p.User == nil {
		return ""
	}
	return p.User.TeamSlug
}

// IsManagerOf returns true only if the principal is a manager whose user
// row points at a team with the given slug.
func (p *Principal) IsManagerOf(teamSlug string) bool {
	if teamSlug == "" {
		return false
	}
	return p.IsManager() && p.User.TeamSlug == teamSlug
}

// CanAccessTeam is the standard "may this principal touch this team's
// resources" predicate: admins always; managers only for their own team.
func (p *Principal) CanAccessTeam(teamSlug string) bool {
	if p.IsAdmin() {
		return true
	}
	return p.IsManagerOf(teamSlug)
}

// SessionHashFromContext returns the current session token hash that
// the Session middleware stamped on the request. Empty for master-key
// principals (no session row).
func SessionHashFromContext(ctx context.Context) []byte {
	v, _ := ctx.Value(ctxKeySessionHash).([]byte)
	return v
}

func PrincipalFromContext(ctx context.Context) *Principal {
	v, _ := ctx.Value(ctxKeyPrincipal).(*Principal)
	return v
}

func TeamFromContext(ctx context.Context) *store.Team {
	v, _ := ctx.Value(ctxKeyTeam).(*store.Team)
	return v
}

func VirtualKeyFromContext(ctx context.Context) *store.VirtualKey {
	v, _ := ctx.Value(ctxKeyVirtualKey).(*store.VirtualKey)
	return v
}

func OwnerUserFromContext(ctx context.Context) *store.User {
	v, _ := ctx.Value(ctxKeyOwnerUser).(*store.User)
	return v
}

// WithTeam, WithVirtualKey, and WithOwnerUser stamp the same context
// values the Bearer middleware would set. Exposed so admin tooling
// (e.g. usage-row replay) can synthesize a /v1-shaped context without
// going through the Bearer middleware.
func WithTeam(ctx context.Context, t *store.Team) context.Context {
	return context.WithValue(ctx, ctxKeyTeam, t)
}

func WithVirtualKey(ctx context.Context, vk *store.VirtualKey) context.Context {
	return context.WithValue(ctx, ctxKeyVirtualKey, vk)
}

func WithOwnerUser(ctx context.Context, u *store.User) context.Context {
	return context.WithValue(ctx, ctxKeyOwnerUser, u)
}

// Bearer authenticates /v1 requests via virtual key bearer token, or a
// JWT verified against a per-team JWKS (doc 0008). Pass nil verifier to
// disable the JWT path entirely.
func Bearer(s *store.Store, verifier *JWTVerifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rawKey := ExtractBearer(r)
			if rawKey == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}

			// JWT path runs first when the token looks like a JWT and we
			// have a verifier. On a successful JWT match we synthesize a
			// VirtualKey-shaped principal so downstream code (rate
			// limits, budget, usage logging) treats it uniformly.
			if verifier != nil && looksLikeJWT(rawKey) {
				principal, err := verifier.Verify(r.Context(), rawKey)
				if err != nil {
					writeAuthError(w, http.StatusUnauthorized, "jwt verification failed: "+err.Error())
					return
				}
				if principal != nil {
					ctx := context.WithValue(r.Context(), ctxKeyTeam, principal.Team)
					// Synthesize a VirtualKey for downstream attribution.
					synth := &store.VirtualKey{
						TeamID:    principal.Team.ID,
						KeyPrefix: "jwt-" + truncSubject(principal.Subject),
						Name:      "jwt:" + principal.Subject,
					}
					ctx = context.WithValue(ctx, ctxKeyVirtualKey, synth)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// JWT-shape token but no team matched → fall through to
				// virtual-key lookup (which will likely also fail and
				// return a clean error). This keeps "I configured JWT
				// for team A but I'm sending team B's virtual key" working.
			}

			vk, team, owner, err := s.LookupKey(r.Context(), HashKey(rawKey))
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeAuthError(w, http.StatusUnauthorized, "invalid api key")
					return
				}
				writeAuthError(w, http.StatusUnauthorized, "auth lookup failed")
				return
			}
			if vk.RevokedAt != nil {
				writeAuthError(w, http.StatusUnauthorized, "key revoked")
				return
			}
			if vk.DisabledAt != nil {
				writeAuthError(w, http.StatusUnauthorized, "key paused")
				return
			}
			if vk.ExpiresAt != nil && vk.ExpiresAt.Before(time.Now()) {
				writeAuthError(w, http.StatusUnauthorized, "key expired")
				return
			}
			if team != nil && team.ArchivedAt != nil {
				writeAuthError(w, http.StatusUnauthorized, "team archived")
				return
			}
			// IP allowlist enforcement (doc 0008). When AllowedCIDRs is
			// set on the key, the client IP must match at least one CIDR.
			if len(vk.AllowedCIDRs) > 0 {
				if !ipAllowed(clientIP(r), vk.AllowedCIDRs) {
					writeAuthError(w, http.StatusForbidden, "ip not in allowlist")
					return
				}
			}
			ctx := context.WithValue(r.Context(), ctxKeyTeam, team)
			ctx = context.WithValue(ctx, ctxKeyVirtualKey, vk)
			if owner != nil {
				ctx = context.WithValue(ctx, ctxKeyOwnerUser, owner)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Session authenticates /admin and /auth/me requests. Accepts either the
// admin master key (synthetic super-admin) or a user session token.
// SessionCookieName is the HttpOnly cookie set by /auth/login. Browsers
// send it automatically on same-origin admin requests, so the SPA never
// has to read or store the session token in JS — that closes the XSS
// pivot the audit called out.
const SessionCookieName = "gatemux_session"
const LegacySessionCookieName = "aiport_session"

// extractSessionToken returns the session token from either the
// HttpOnly cookie (browser flow) or the Authorization header (CLI /
// master-key / API). Cookie wins when both are present so a browser
// session can't be downgraded to the Bearer path by malicious script.
func ExtractSessionToken(r *http.Request) string {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	if c, err := r.Cookie(LegacySessionCookieName); err == nil && c.Value != "" {
		return c.Value
	}
	return ExtractBearer(r)
}

// ClearLegacySessionCookie prevents a retired browser identity from resurfacing
// after the new cookie expires or is removed. No token is exposed to JavaScript.
func ClearLegacySessionCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: LegacySessionCookieName, Path: "/", HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func Session(masterKey string, s *store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := ExtractSessionToken(r)
			if token == "" {
				writeAuthError(w, http.StatusUnauthorized, "missing bearer token")
				return
			}
			if masterKey != "" && subtle.ConstantTimeCompare([]byte(token), []byte(masterKey)) == 1 {
				ctx := context.WithValue(r.Context(), ctxKeyPrincipal, &Principal{IsMasterKey: true})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			tokenHash := HashKey(token)
			user, err := s.LookupSession(r.Context(), tokenHash)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeAuthError(w, http.StatusUnauthorized, "invalid or expired session")
					return
				}
				writeAuthError(w, http.StatusUnauthorized, "auth lookup failed")
				return
			}
			if user.DisabledAt != nil {
				// Disable trumps a live session — admin override beats
				// whatever the IdP still asserts.
				_ = s.DeleteSession(r.Context(), tokenHash)
				writeAuthError(w, http.StatusForbidden, "account disabled")
				return
			}
			// Bump last_seen_at lazily; failure isn't fatal so swallow.
			_ = s.TouchSession(r.Context(), tokenHash)
			ctx := context.WithValue(r.Context(), ctxKeyPrincipal, &Principal{User: user})
			ctx = context.WithValue(ctx, ctxKeySessionHash, tokenHash)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdmin gates a handler to admin principals (master key or user.is_admin).
// Apply after Session so the principal is in context.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFromContext(r.Context())
		if !p.IsAdmin() {
			writeAuthError(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireManagerOrAdmin admits managers and admins; rejects members and
// unauthenticated callers. Use it for endpoints whose handler does its own
// per-team filtering (list teams, list usage, list/create invites).
func RequireManagerOrAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := PrincipalFromContext(r.Context())
		if !p.IsAdmin() && !p.IsManager() {
			writeAuthError(w, http.StatusForbidden, "manager or admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireTeamAccess gates a route to principals that can access the team
// identified by the chi URL param (typically "slug"). Admins always pass;
// managers pass only when their team_slug matches; members are rejected.
func RequireTeamAccess(paramName string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFromContext(r.Context())
			slug := chi.URLParam(r, paramName)
			if slug == "" {
				writeAuthError(w, http.StatusBadRequest, "missing team identifier")
				return
			}
			if !p.CanAccessTeam(slug) {
				writeAuthError(w, http.StatusForbidden, "no access to team "+slug)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// MasterKey is preserved for callers that explicitly need master-key-only auth.
func MasterKey(masterKey string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			provided := ExtractBearer(r)
			if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(masterKey)) != 1 {
				writeAuthError(w, http.StatusUnauthorized, "invalid admin credentials")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ExtractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	errType := "authentication_error"
	if status == http.StatusForbidden {
		errType = "forbidden"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": msg,
			"type":    errType,
		},
	})
}
