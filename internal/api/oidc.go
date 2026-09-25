package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/store"
)

// OIDCHandler covers the entire IdP flow: redirect to /authorize, accept
// the callback, verify the ID token, upsert the user, apply claim
// mapping, and issue a session cookie. Disabled (config.OIDC.Enabled()
// false) callers receive 404 from every endpoint so the OIDC surface is
// invisible when not configured.
type OIDCHandler struct {
	SecureCookies bool
	Store         *store.Store
	Logger        *slog.Logger
	Cfg           config.OIDCConfig
	provider      *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	oauth         *oauth2.Config
	secret        string
}

func NewOIDCHandler(ctx context.Context, st *store.Store, log *slog.Logger, cfg config.OIDCConfig, secret string) (*OIDCHandler, error) {
	h := &OIDCHandler{Store: st, Logger: log, Cfg: cfg, secret: secret}
	if !cfg.Enabled() {
		return h, nil
	}
	if secret == "" {
		return nil, fmt.Errorf("oidc client secret is empty (set %s)", cfg.ClientSecretEnv)
	}
	prov, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc provider discovery: %w", err)
	}
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		// openid + profile + email is the universal default. groups are
		// requested implicitly when role mapping is configured because
		// most IdPs require an explicit scope to include them.
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
		if cfg.RoleClaim != "" {
			scopes = append(scopes, "groups")
		}
	}
	h.provider = prov
	h.verifier = prov.Verifier(&oidc.Config{ClientID: cfg.ClientID})
	h.oauth = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: secret,
		Endpoint:     prov.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       scopes,
	}
	return h, nil
}

func (h *OIDCHandler) Enabled() bool { return h != nil && h.oauth != nil }

const (
	oidcStateCookie = "gatemux_oidc_state"
	oidcNonceCookie = "gatemux_oidc_nonce"
	oidcStateTTL    = 10 * time.Minute
)

// Start sets a state + nonce in short-lived HttpOnly cookies and 302s the
// browser to the IdP authorize endpoint. State + nonce are returned by
// the IdP and re-checked in Callback.
func (h *OIDCHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.NotFound(w, r)
		return
	}
	state, err := randomToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	nonce, err := randomToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.setShortCookie(w, r, oidcStateCookie, state)
	h.setShortCookie(w, r, oidcNonceCookie, nonce)
	clearShortCookie(w, "aiport_oidc_state")
	clearShortCookie(w, "aiport_oidc_nonce")
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, oidc.Nonce(nonce)), http.StatusFound)
}

// Callback verifies state, exchanges the auth code, verifies the ID
// token, upserts the user, applies claim mapping, issues a session
// cookie, and 302s back to /. Error states render a small HTML page
// that links back to /login rather than dumping JSON, since this URL
// lands in the browser bar.
func (h *OIDCHandler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		http.NotFound(w, r)
		return
	}
	stateCookie, nonceCookie, err := oidcCookies(r)
	if err != nil || stateCookie.Value == "" {
		h.fail(w, r, "missing oidc state")
		return
	}
	if r.URL.Query().Get("state") != stateCookie.Value {
		h.fail(w, r, "oidc state mismatch")
		return
	}
	if nonceCookie == nil || nonceCookie.Value == "" {
		h.fail(w, r, "missing oidc nonce")
		return
	}
	clearShortCookie(w, oidcStateCookie)
	clearShortCookie(w, oidcNonceCookie)
	clearShortCookie(w, "aiport_oidc_state")
	clearShortCookie(w, "aiport_oidc_nonce")

	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		h.fail(w, r, "idp error: "+errMsg)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.fail(w, r, "missing code")
		return
	}
	tok, err := h.oauth.Exchange(r.Context(), code)
	if err != nil {
		h.fail(w, r, "code exchange failed: "+err.Error())
		return
	}
	rawIDToken, _ := tok.Extra("id_token").(string)
	if rawIDToken == "" {
		h.fail(w, r, "id_token missing from token response")
		return
	}
	idToken, err := h.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		h.fail(w, r, "id_token verification failed: "+err.Error())
		return
	}
	if idToken.Nonce != nonceCookie.Value {
		h.fail(w, r, "id_token nonce mismatch")
		return
	}

	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		h.fail(w, r, "claims parse: "+err.Error())
		return
	}
	if claims.Email == "" {
		h.fail(w, r, "email claim is required")
		return
	}

	role := mapRole(claims.Groups, h.Cfg)
	team := mapTeam(claims.Email, h.Cfg)

	// Upsert: prefer matching by oidc_sub (stable across email changes),
	// fall back to email so an existing password account can claim the
	// SSO identity on first sign-in.
	user, err := h.Store.LookupUserByOIDCSub(r.Context(), idToken.Subject)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		h.fail(w, r, "lookup by sub: "+err.Error())
		return
	}
	if user == nil {
		existing, err := h.Store.LookupUserByEmail(r.Context(), claims.Email)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			h.fail(w, r, "lookup by email: "+err.Error())
			return
		}
		if existing != nil {
			if err := h.Store.BindOIDCSub(r.Context(), existing.ID, idToken.Subject); err != nil {
				h.fail(w, r, "bind sub: "+err.Error())
				return
			}
			user = existing
			// Existing accounts default to admin-curated role/team; OIDC
			// claim mapping doesn't get to overwrite. The bind above
			// alone is enough — admin keeps the wheel.
		} else {
			created, err := h.Store.CreateOIDCUser(r.Context(), store.CreateOIDCUserParams{
				OIDCSub:  idToken.Subject,
				Email:    claims.Email,
				Name:     firstNonEmpty(claims.Name, claims.PreferredUsername, claims.Email),
				Role:     role,
				TeamSlug: team,
			})
			if err != nil {
				h.fail(w, r, "create user: "+err.Error())
				return
			}
			user = created
		}
	} else {
		// Repeat sign-in. Apply mapping defaults; ApplyOIDCMapping
		// short-circuits when an admin previously set
		// role_managed_by_oidc=false.
		if err := h.Store.ApplyOIDCMapping(r.Context(), user.ID, role, team); err != nil && h.Logger != nil {
			h.Logger.Warn("oidc apply mapping", "err", err)
		}
	}

	if user.DisabledAt != nil {
		h.fail(w, r, "your account is disabled — contact your administrator")
		return
	}

	rawToken, tokenHash, err := auth.GenerateSessionToken()
	if err != nil {
		h.fail(w, r, "session: "+err.Error())
		return
	}
	if err := h.Store.CreateSessionWithMeta(r.Context(), tokenHash, user.ID, time.Now().Add(7*24*time.Hour), auth.ClientIP(r), r.UserAgent()); err != nil {
		h.fail(w, r, "session create: "+err.Error())
		return
	}
	auth.ClearLegacySessionCookie(w, h.SecureCookies || r.TLS != nil)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(7 * 24 * time.Hour),
	})
	_ = h.Store.UpdateLastLogin(r.Context(), user.ID)
	_ = h.Store.InsertAuditEvent(r.Context(), store.AuditEvent{
		ActorType:    "user",
		ActorID:      itoa(user.ID),
		Action:       "auth.oidc_login",
		ResourceType: "session",
		ResourceID:   itoa(user.ID),
		Metadata:     map[string]any{"sub": idToken.Subject, "email": claims.Email},
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

type oidcClaims struct {
	Email             string   `json:"email"`
	Name              string   `json:"name"`
	PreferredUsername string   `json:"preferred_username"`
	Groups            []string `json:"groups"`
}

// mapRole picks the first matching group from the IdP groups claim that
// has a RoleMap entry. Falls back to DefaultRole, then to "member".
// Order matters when a user is in multiple mapped groups: the first
// match wins, so put "admin" mappings before "manager" if you list both.
func mapRole(groups []string, cfg config.OIDCConfig) string {
	if cfg.RoleClaim != "" && cfg.RoleMap != nil {
		for _, g := range groups {
			if r, ok := cfg.RoleMap[g]; ok {
				return r
			}
		}
	}
	if cfg.DefaultRole != "" {
		return cfg.DefaultRole
	}
	return store.RoleMember
}

// mapTeam matches the email's domain against TeamFromEmailDomain. The
// match is case-insensitive on the domain and exact on the part after
// the @ — no subdomain heuristics, no wildcards, intentionally simple.
func mapTeam(email string, cfg config.OIDCConfig) string {
	if email == "" {
		return cfg.DefaultTeam
	}
	at := strings.LastIndex(email, "@")
	if at < 0 || at == len(email)-1 {
		return cfg.DefaultTeam
	}
	domain := strings.ToLower(email[at+1:])
	if t, ok := cfg.TeamFromEmailDomain[domain]; ok {
		return t
	}
	return cfg.DefaultTeam
}

// fail writes a tiny HTML page so the browser-bar URL ends with a human
// message rather than raw JSON. The link drops them back at /login.
func (h *OIDCHandler) fail(w http.ResponseWriter, r *http.Request, msg string) {
	if h.Logger != nil {
		h.Logger.Warn("oidc callback failed", "msg", msg, "remote_ip", auth.ClientIP(r))
	}
	clearShortCookie(w, oidcStateCookie)
	clearShortCookie(w, oidcNonceCookie)
	clearShortCookie(w, "aiport_oidc_state")
	clearShortCookie(w, "aiport_oidc_nonce")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;max-width:480px;margin:64px auto;padding:24px;line-height:1.45">
<h1 style="font-size:18px">Sign-in failed</h1>
<p>%s</p>
<p><a href="/">Back to sign in</a></p>
</body></html>`, htmlEscape(msg))
}

func (h *OIDCHandler) setShortCookie(w http.ResponseWriter, r *http.Request, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.SecureCookies || r.TLS != nil,
		// Lax (not Strict) so the cookie survives the cross-site IdP →
		// gateway redirect — Strict would drop it on the callback.
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(oidcStateTTL),
	})
}

func clearShortCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// Choose a state/nonce pair from one naming generation, never a mixture.
func oidcCookies(r *http.Request) (*http.Cookie, *http.Cookie, error) {
	state, err := r.Cookie(oidcStateCookie)
	nonceName := oidcNonceCookie
	if err == http.ErrNoCookie {
		state, err = r.Cookie("aiport_oidc_state")
		nonceName = "aiport_oidc_nonce"
	}
	if err != nil {
		return nil, nil, err
	}
	nonce, _ := r.Cookie(nonceName)
	return state, nonce, nil
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// htmlEscape is the minimal escaper for fail()'s message — html/template
// would be overkill for one string. Keeps imports tight.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// Loosely-typed claims dump for /auth/oidc/debug — useful for verifying
// what the IdP is actually sending while writing role_map. Disabled in
// production-style configs.
type debugClaims map[string]json.RawMessage
