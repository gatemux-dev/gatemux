package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRenameHeaderCompatibility(t *testing.T) {
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Aiport-Tags", "one, two")
	r.Header.Set("X-Aiport-Region", "old-region")
	r.Header.Set("X-Aiport-Customer-Id", "legacy-customer")
	r.Header.Set("X-Customer-Id", "generic-customer")
	rc := resolveContextFromRequest(r)
	if strings.Join(rc.Tags, ",") != "one,two" || rc.Region != "old-region" || extractCustomerID(r, "body") != "legacy-customer" {
		t.Fatal("legacy request signals lost")
	}
	r.Header.Set("X-Gatemux-Tags", "new")
	r.Header.Set("X-Gatemux-Region", "new-region")
	r.Header.Set("X-Gatemux-Customer-Id", "new-customer")
	rc = resolveContextFromRequest(r)
	if strings.Join(rc.Tags, ",") != "new" || rc.Region != "new-region" || extractCustomerID(r, "body") != "new-customer" {
		t.Fatal("canonical header precedence lost")
	}
}

func TestRenameResponseIDs(t *testing.T) {
	id, err := newResponseID()
	if err != nil || !strings.HasPrefix(id, "resp_gatemux_") || !responseIDPattern.MatchString(id) {
		t.Fatal("new ID format")
	}
	legacy := strings.Replace(id, "resp_gatemux_", "resp_aiport_", 1)
	if !responseIDPattern.MatchString(legacy) {
		t.Fatal("legacy ID rejected")
	}
	for _, invalid := range []string{"resp_upstream", legacy + "x", "x" + legacy, strings.ToUpper(legacy)} {
		if responseIDPattern.MatchString(invalid) {
			t.Fatal("invalid ID accepted")
		}
	}
}

func TestRenameOIDCCookiesStayPaired(t *testing.T) {
	r := httptest.NewRequest("GET", "/auth/oidc/callback", nil)
	r.AddCookie(&http.Cookie{Name: "aiport_oidc_state", Value: "old-state"})
	r.AddCookie(&http.Cookie{Name: "aiport_oidc_nonce", Value: "old-nonce"})
	state, nonce, err := oidcCookies(r)
	if err != nil || state.Value != "old-state" || nonce.Value != "old-nonce" {
		t.Fatal("legacy flow lost")
	}
	r.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: "new-state"})
	state, nonce, err = oidcCookies(r)
	if err != nil || state.Value != "new-state" || nonce != nil {
		t.Fatal("cookie generations mixed")
	}
	r.AddCookie(&http.Cookie{Name: oidcNonceCookie, Value: "new-nonce"})
	_, nonce, err = oidcCookies(r)
	if err != nil || nonce.Value != "new-nonce" {
		t.Fatal("canonical flow lost")
	}
}
