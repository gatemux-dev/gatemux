package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

const sessionDuration = 7 * 24 * time.Hour

type AuthHandler struct {
	Store         *store.Store
	Logger        *slog.Logger
	SecureCookies bool
	// MasterKeyLoginDisabled mirrors config.admin.disable_master_key_login
	// so the public /auth/login-modes endpoint can tell the web login
	// page whether to render the emergency-admin affordance.
	MasterKeyLoginDisabled bool
	// OIDCEnabled controls whether the "Sign in with X" button shows.
	// Server.New flips it true after successful OIDC discovery.
	OIDCEnabled      bool
	OIDCProviderName string
}

// LoginModes is the public shape that drives Login.tsx's UI. The endpoint
// is unauthenticated by design — it carries no secrets, just policy
// hints about which auth surfaces are exposed.
type LoginModes struct {
	MasterKeyEnabled bool   `json:"master_key_enabled"`
	OIDCEnabled      bool   `json:"oidc_enabled"`
	OIDCProviderName string `json:"oidc_provider_name,omitempty"`
}

func (h *AuthHandler) LoginModesHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, LoginModes{
		MasterKeyEnabled: !h.MasterKeyLoginDisabled,
		OIDCEnabled:      h.OIDCEnabled,
		OIDCProviderName: h.OIDCProviderName,
	})
}

type AuthenticatedUserInfo struct {
	ID       int64  `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     string `json:"role"`
	IsAdmin  bool   `json:"is_admin"`
	TeamID   *int64 `json:"team_id,omitempty"`
	TeamSlug string `json:"team_slug,omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	User AuthenticatedUserInfo `json:"user"`
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
		return
	}
	user, hash, err := h.Store.GetUserByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusUnauthorized, "authentication_error", "invalid email or password")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !auth.CheckPassword(hash, req.Password) {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "invalid email or password")
		return
	}
	if user.DisabledAt != nil {
		// Block sign-in even though credentials matched — admin override
		// trumps the password. Same behavior the OIDC callback enforces.
		writeJSONError(w, http.StatusForbidden, "account_disabled",
			"account is disabled — contact your administrator")
		return
	}
	rawToken, tokenHash, err := auth.GenerateSessionToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.Store.CreateSessionWithMeta(r.Context(), tokenHash, user.ID, time.Now().Add(sessionDuration), auth.ClientIP(r), r.UserAgent()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Issue an HttpOnly session cookie so the browser never has to put
	// the token in JS-readable storage. SameSite=Strict gives us CSRF
	// protection without a separate token because the admin SPA is
	// same-origin with /admin. Secure is set when the request landed
	// over TLS — local dev over plain HTTP still works.
	auth.ClearLegacySessionCookie(w, h.SecureCookies || r.TLS != nil)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    rawToken,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(sessionDuration),
	})
	_ = h.Store.UpdateLastLogin(r.Context(), user.ID)
	_ = h.Store.InsertAuditEvent(r.Context(), store.AuditEvent{
		ActorType:    "user",
		ActorID:      fmt.Sprintf("%d", user.ID),
		Action:       "auth.login",
		ResourceType: "session",
		ResourceID:   fmt.Sprintf("%d", user.ID),
		Metadata: map[string]any{
			"email":    user.Email,
			"is_admin": user.IsAdmin,
		},
	})
	writeJSON(w, http.StatusOK, LoginResponse{
		User: userToInfo(h.Store, r, user),
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	// Cookie wins when both are present (matches Session middleware
	// precedence) so a stale Bearer header in JS storage doesn't keep
	// the row alive after the browser session is killed.
	token := auth.ExtractSessionToken(r)
	if token != "" {
		_ = h.Store.DeleteSession(r.Context(), auth.HashKey(token))
	}
	// Revoke both browser identities if a pre-rename cookie coexists.
	if c, err := r.Cookie(auth.LegacySessionCookieName); err == nil && c.Value != "" && c.Value != token {
		_ = h.Store.DeleteSession(r.Context(), auth.HashKey(c.Value))
	}
	auth.ClearLegacySessionCookie(w, h.SecureCookies || r.TLS != nil)
	// Always clear the cookie even if nothing was sent — covers the case
	// where the cookie exists in the browser but the row has been pruned.
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   h.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

type WhoamiResponse struct {
	User        *AuthenticatedUserInfo `json:"user,omitempty"`
	IsMasterKey bool                   `json:"is_master_key"`
}

func (h *AuthHandler) Whoami(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	if p == nil {
		writeJSONError(w, http.StatusUnauthorized, "authentication_error", "not authenticated")
		return
	}
	resp := WhoamiResponse{IsMasterKey: p.IsMasterKey}
	if p.User != nil {
		info := userToInfo(h.Store, r, p.User)
		resp.User = &info
	}
	writeJSON(w, http.StatusOK, resp)
}

func userToInfo(s *store.Store, r *http.Request, u *store.User) AuthenticatedUserInfo {
	info := AuthenticatedUserInfo{
		ID: u.ID, Email: u.Email, Name: u.Name,
		Role: u.Role, IsAdmin: u.IsAdmin, TeamID: u.TeamID,
		TeamSlug: u.TeamSlug,
	}
	// Fall back to a teams lookup for older callers that pass a User struct
	// loaded without the JOIN (most paths populate TeamSlug already).
	if info.TeamSlug == "" && u.TeamID != nil && s != nil {
		if t, err := s.GetTeamByID(r.Context(), *u.TeamID); err == nil {
			info.TeamSlug = t.Slug
		}
	}
	return info
}
