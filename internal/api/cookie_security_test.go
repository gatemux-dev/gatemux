package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOIDCSecureCookiePolicy(t *testing.T) {
	for _, secure := range []bool{false, true} {
		h := &OIDCHandler{SecureCookies: secure}
		r := httptest.NewRequest("GET", "http://gateway/auth/oidc/login", nil)
		r.Header.Set("X-Forwarded-Proto", "https") // never trust caller headers
		w := httptest.NewRecorder()
		h.setShortCookie(w, r, "state", "fixture")
		cookie := w.Result().Cookies()[0]
		if cookie.Secure != secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode {
			t.Fatal("incorrect proxy cookie policy")
		}
		// Clearing must use the same policy, or browsers keep the original.
		w = httptest.NewRecorder()
		h.clearShortCookie(w, r, "state")
		cleared := w.Result().Cookies()[0]
		if cleared.Secure != secure || !cleared.HttpOnly || cleared.SameSite != http.SameSiteLaxMode || cleared.MaxAge >= 0 {
			t.Fatalf("incorrect clear-cookie policy: %+v", cleared)
		}
	}
}

func TestOIDCFailEscapesMessage(t *testing.T) {
	h := &OIDCHandler{}
	r := httptest.NewRequest("GET", "http://gateway/auth/oidc/callback", nil)
	w := httptest.NewRecorder()
	h.fail(w, r, `<script>alert("x")</script> & 'quote'`)
	body, _ := io.ReadAll(w.Result().Body)
	if strings.Contains(string(body), "<script>") || !strings.Contains(string(body), "&lt;script&gt;") {
		t.Fatalf("message not escaped: %s", body)
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", w.Code)
	}
}
