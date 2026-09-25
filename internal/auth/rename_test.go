package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRenameSessionPrecedence(t *testing.T) {
	r := httptest.NewRequest("GET", "/auth/me", nil)
	r.Header.Set("Authorization", "Bearer master")
	if ExtractSessionToken(r) != "master" {
		t.Fatal("bearer lost")
	}
	r.AddCookie(&http.Cookie{Name: LegacySessionCookieName, Value: "legacy"})
	if ExtractSessionToken(r) != "legacy" {
		t.Fatal("legacy cookie must precede bearer")
	}
	r.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "current"})
	if ExtractSessionToken(r) != "current" {
		t.Fatal("new cookie must precede legacy")
	}
}
