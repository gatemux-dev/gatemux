package api

import (
	"net/http"
	"net/http/httptest"
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
	}
}
