package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gatemux-dev/gatemux/internal/auth"
	"github.com/gatemux-dev/gatemux/internal/store"
)

const passwordResetTTL = 24 * time.Hour

// IssuePasswordResetResponse is the one-shot reveal of the reset URL.
// Mirrors the invite/key reveal pattern: admins get the value in the
// response body once, then never again.
type IssuePasswordResetResponse struct {
	UserID    int64     `json:"user_id"`
	Email     string    `json:"email"`
	Token     string    `json:"token"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssuePasswordReset is the admin-only endpoint behind the "Reset
// password" button on the Users page. Returns a token + ready-to-share
// URL with no implicit user notification (we don't send email).
func (h *AdminHandler) IssuePasswordReset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "id must be numeric")
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	rawToken, tokenHash, err := auth.GeneratePasswordResetToken()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	expiresAt := time.Now().Add(passwordResetTTL)

	var createdBy *int64
	if p := auth.PrincipalFromContext(r.Context()); p != nil && p.User != nil {
		uid := p.User.ID
		createdBy = &uid
	}

	if err := h.Store.CreatePasswordReset(r.Context(), user.ID, tokenHash, expiresAt, createdBy); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	h.audit(r, "user.password_reset_issued", "user", strconv.FormatInt(user.ID, 10), map[string]any{
		"email":      user.Email,
		"expires_at": expiresAt,
	})

	writeJSON(w, http.StatusOK, IssuePasswordResetResponse{
		UserID:    user.ID,
		Email:     user.Email,
		Token:     rawToken,
		URL:       buildResetURL(r, rawToken),
		ExpiresAt: expiresAt,
	})
}

func buildResetURL(r *http.Request, token string) string {
	// Don't honor X-Forwarded-Host — a request reaching this admin
	// endpoint with a spoofed header would otherwise mint a reset URL
	// pointing at attacker.example. r.Host is what the listener actually
	// served, so it's the trustworthy source. Operators behind a TLS
	// terminator should set the public hostname via X-Forwarded-Proto on
	// the LB and configure gatemux's listener to match the public host.
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/reset/" + token
}

// PasswordResetHandler covers the public unauthenticated reset surface
// (GET to confirm the token, POST to consume it). Lives outside
// AdminHandler because it mounts under /reset, not /admin.
type PasswordResetHandler struct {
	Store *store.Store
}

type GetPasswordResetResponse struct {
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (h *PasswordResetHandler) Get(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing token")
		return
	}
	pr, err := h.Store.LookupPasswordReset(r.Context(), auth.HashKey(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "reset link is invalid or expired")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, GetPasswordResetResponse{
		Email:     pr.UserEmail,
		ExpiresAt: pr.ExpiresAt,
	})
}

type ConsumePasswordResetRequest struct {
	NewPassword string `json:"new_password"`
}

// Consume validates the token, writes the new password, marks the
// reset used, and revokes every existing session for the user — a
// stolen session shouldn't outlive a forced reset. The user signs in
// fresh from the login screen.
func (h *PasswordResetHandler) Consume(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if token == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "missing token")
		return
	}
	var req ConsumePasswordResetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if len(req.NewPassword) < 8 {
		writeJSONError(w, http.StatusBadRequest, "invalid_request",
			"new password must be at least 8 characters")
		return
	}
	pr, err := h.Store.LookupPasswordReset(r.Context(), auth.HashKey(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound, "not_found", "reset link is invalid or expired")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	hash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.Store.UpdatePasswordHash(r.Context(), pr.UserID, hash); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if err := h.Store.MarkPasswordResetUsed(r.Context(), pr.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// Force re-login on every device — a stolen session shouldn't
	// survive a password reset.
	_ = h.Store.DeleteSessionsForUser(r.Context(), pr.UserID)
	_ = h.Store.InsertAuditEvent(r.Context(), store.AuditEvent{
		ActorType:    "user",
		ActorID:      strconv.FormatInt(pr.UserID, 10),
		Action:       "auth.password_reset_consumed",
		ResourceType: "user",
		ResourceID:   strconv.FormatInt(pr.UserID, 10),
	})
	w.WriteHeader(http.StatusNoContent)
}
