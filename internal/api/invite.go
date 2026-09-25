package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

type InviteHandler struct {
	Store         *store.Store
	SecureCookies bool
}

type GetInviteResponse struct {
	Email     string    `json:"email,omitempty"`
	TeamSlug  string    `json:"team_slug,omitempty"`
	Role      string    `json:"role"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *InviteHandler) GetInvite(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "token required")
		return
	}
	inv, err := h.Store.GetInviteByTokenHash(r.Context(), auth.HashKey(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "invite not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if inv.AcceptedAt != nil {
		writeJSONError(w, http.StatusGone, "invite_used", "this invite has already been accepted")
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		writeJSONError(w, http.StatusGone, "invite_expired", "this invite has expired")
		return
	}
	out := GetInviteResponse{Email: inv.Email, Role: inv.Role, ExpiresAt: inv.ExpiresAt}
	if inv.TeamID != nil {
		if t, err := h.Store.GetTeamByID(r.Context(), *inv.TeamID); err == nil {
			out.TeamSlug = t.Slug
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type AcceptInviteRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

type AcceptInviteResponse struct {
	Token string                `json:"token"`
	User  AuthenticatedUserInfo `json:"user"`
}

// normalizeInviteRole maps invite.role strings to the role enum users.role
// uses. Older invites used 'user' as a synonym for 'member'; new invites
// can pass 'manager' explicitly.
func normalizeInviteRole(role string) string {
	switch role {
	case "admin":
		return "admin"
	case "manager":
		return "manager"
	case "member", "user", "":
		return "member"
	default:
		return "member"
	}
}

func (h *InviteHandler) AcceptInvite(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "token required")
		return
	}
	var req AcceptInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if req.Email == "" || req.Password == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "email and password are required")
		return
	}

	tokenHash := auth.HashKey(token)
	inv, err := h.Store.GetInviteByTokenHash(r.Context(), tokenHash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "invite not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if inv.AcceptedAt != nil {
		writeJSONError(w, http.StatusGone, "invite_used", "this invite has already been accepted")
		return
	}
	if time.Now().After(inv.ExpiresAt) {
		writeJSONError(w, http.StatusGone, "invite_expired", "this invite has expired")
		return
	}
	if inv.Email != "" && !strings.EqualFold(inv.Email, req.Email) {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"this invite is pinned to "+inv.Email)
		return
	}

	pwHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	role := normalizeInviteRole(inv.Role)
	user, err := h.Store.CreateUser(r.Context(),
		strings.ToLower(strings.TrimSpace(req.Email)),
		strings.TrimSpace(req.Name),
		pwHash,
		inv.TeamID,
		role,
	)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := h.Store.MarkInviteAccepted(r.Context(), tokenHash, user.ID); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	rawSess, sessHash, err := auth.GenerateSessionToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.Store.CreateSessionWithMeta(r.Context(), sessHash, user.ID, time.Now().Add(sessionDuration), auth.ClientIP(r), r.UserAgent()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Mirror /auth/login: set the HttpOnly cookie so accept-then-redirect
	// lands the user signed in without the SPA storing the token.
	auth.ClearLegacySessionCookie(w, h.SecureCookies || r.TLS != nil)
	http.SetCookie(w, &http.Cookie{
		Name:     auth.SessionCookieName,
		Value:    rawSess,
		Path:     "/",
		HttpOnly: true,
		Secure:   h.SecureCookies || r.TLS != nil,
		SameSite: http.SameSiteStrictMode,
		Expires:  time.Now().Add(sessionDuration),
	})
	_ = h.Store.UpdateLastLogin(r.Context(), user.ID)

	writeJSON(w, http.StatusOK, AcceptInviteResponse{
		Token: rawSess,
		User:  userToInfo(h.Store, r, user),
	})
}
