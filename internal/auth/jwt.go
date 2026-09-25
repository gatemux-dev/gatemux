package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/MicahParks/jwkset"
	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// JWTVerifier verifies bearer tokens that look like JWTs against the
// configured per-team JWKS endpoints. Per design doc 0008, the verifier
// walks the configured teams (cached, refreshed periodically) to locate
// the team a token was minted for via a configurable claim
// (default `team_slug`).
//
// The JWT path runs *before* the virtual-key path so a key-shaped string
// that happens to contain dots can't accidentally be misread as a JWT —
// see looksLikeJWT().
type JWTVerifier struct {
	store     *store.Store
	mu        sync.RWMutex
	refreshMu sync.Mutex
	transport *http.Transport
	client    *http.Client
	teams     []store.JWTTeamConfig
	// jwks is keyed on jwks_url; one keyfunc per upstream issuer so we
	// don't refetch JWKS for every request.
	jwks map[string]keyfunc.Keyfunc
}

func NewJWTVerifier(s *store.Store) *JWTVerifier {
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 2 * time.Second,
		MaxResponseHeaderBytes: 32 << 10, MaxConnsPerHost: 2, MaxIdleConns: 16,
		MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &JWTVerifier{store: s, jwks: map[string]keyfunc.Keyfunc{}, transport: transport,
		client: &http.Client{Timeout: 2 * time.Second, Transport: boundedJWKSTransport{transport},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
}

// Close releases idle HTTP connections after request and refresh workers join.
func (v *JWTVerifier) Close() {
	if v != nil && v.transport != nil {
		v.transport.CloseIdleConnections()
	}
}

type boundedJWKSTransport struct{ http.RoundTripper }
type limitedJWKSBody struct {
	io.Reader
	io.Closer
}

func (b boundedJWKSTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := b.RoundTripper.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > 1<<20 {
		_ = resp.Body.Close()
		return nil, errors.New("JWKS exceeds 1 MiB")
	}
	resp.Body = limitedJWKSBody{io.LimitReader(resp.Body, 1<<20), resp.Body}
	return resp, nil
}

// No library-owned refresh goroutines: the server's joined worker owns the
// periodic refresh. Unknown-key refresh stays synchronous, rate limited per
// issuer, and uses the inference request context (including shutdown cancel).
func (v *JWTVerifier) loadKeyfunc(ctx context.Context, address string) (keyfunc.Keyfunc, error) {
	storage, err := jwkset.NewStorageFromHTTP(address, jwkset.HTTPClientStorageOptions{Ctx: ctx, Client: v.client, HTTPTimeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	client, err := jwkset.NewHTTPClient(jwkset.HTTPClientOptions{HTTPURLs: map[string]jwkset.Storage{address: storage}, RefreshUnknownKID: rate.NewLimiter(rate.Every(5*time.Minute), 1), RateLimitWaitMax: time.Millisecond})
	if err != nil {
		return nil, err
	}
	return keyfunc.New(keyfunc.Options{Storage: client})
}

// Refresh loads the team list and resolves a keyfunc per JWKS URL.
// Called once on startup and on every Refresh from the admin path so
// adding a JWT-enabled team is visible without a restart.
func (v *JWTVerifier) Refresh(ctx context.Context) error {
	if !v.refreshMu.TryLock() {
		return errors.New("JWT refresh already running")
	}
	defer v.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	teams, err := v.store.ListJWTTeams(ctx)
	if err != nil {
		return err
	}
	next := map[string]keyfunc.Keyfunc{}
	v.mu.RLock()
	prev := v.jwks
	v.mu.RUnlock()
	for _, t := range teams {
		if _, ok := next[t.JWKSURL]; ok {
			continue
		}
		k, err := v.loadKeyfunc(ctx, t.JWKSURL)
		if err != nil {
			// Retain an existing issuer cache on transient failure. Removed
			// issuer URLs are not copied into the next snapshot.
			if previous := prev[t.JWKSURL]; previous != nil {
				next[t.JWKSURL] = previous
			}
			continue
		}
		next[t.JWKSURL] = k
	}
	v.mu.Lock()
	v.teams = teams
	v.jwks = next
	v.mu.Unlock()
	return nil
}

// looksLikeJWT returns true when a bearer string has the shape "x.y.z".
// Virtual keys are flat opaque strings, so this disambiguates the two
// paths cheaply before any signature work.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

// JWTPrincipal is the auth result for a successful JWT verification.
// Returned to the bearer middleware which then attaches it to the
// request context so the v1 handler can attribute spend by subject.
type JWTPrincipal struct {
	Team    *store.Team
	Subject string
	Email   string
	Claims  map[string]any
}

// Verify parses + validates the token against every configured team's
// JWKS, picks the team identified by the configured team-claim, checks
// audience + expiry. Returns nil principal + nil error when the token
// isn't a JWT (caller falls back to the virtual-key path).
func (v *JWTVerifier) Verify(ctx context.Context, token string) (*JWTPrincipal, error) {
	if !looksLikeJWT(token) {
		return nil, nil
	}
	v.mu.RLock()
	teams := v.teams
	jwksByURL := v.jwks
	v.mu.RUnlock()
	if len(teams) == 0 {
		return nil, nil
	}
	// We don't yet know which team minted this — try each issuer's JWKS
	// in order. The successful Parse returns the validated claims; we
	// then check the team-claim matches one of our configured teams.
	for _, t := range teams {
		kf, ok := jwksByURL[t.JWKSURL]
		if !ok {
			continue
		}
		parser := jwt.NewParser(jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "PS256"}))
		var claims jwt.MapClaims
		_, err := parser.ParseWithClaims(token, &claims, kf.KeyfuncCtx(ctx))
		if err != nil {
			continue
		}
		// Audience check, if configured.
		if t.Audience != "" {
			if !audMatches(claims, t.Audience) {
				continue
			}
		}
		// Resolve the team-claim → which team this token belongs to.
		teamSlug, _ := claims[t.TeamClaim].(string)
		if teamSlug == "" {
			continue
		}
		// The team-claim must match this team (the JWKS already validated
		// the signature against this team's issuer, but a single JWKS may
		// serve many teams via the claim).
		if teamSlug != t.TeamSlug {
			// Try to match a different team using the same JWKS.
			continue
		}
		team, _, err := v.store.GetTeamBySlugForJWT(ctx, teamSlug)
		if err != nil {
			return nil, fmt.Errorf("load team after jwt verify: %w", err)
		}
		if team == nil {
			continue
		}
		subject, _ := claims["sub"].(string)
		email, _ := claims["email"].(string)
		return &JWTPrincipal{Team: team, Subject: subject, Email: email, Claims: claims}, nil
	}
	return nil, ErrInvalidJWT
}

// ErrInvalidJWT is returned when a bearer token has JWT shape but no
// configured team validates it.
var ErrInvalidJWT = errors.New("invalid or unrecognized jwt")

func audMatches(claims jwt.MapClaims, want string) bool {
	switch v := claims["aud"].(type) {
	case string:
		return v == want
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == want {
				return true
			}
		}
	case []string:
		for _, s := range v {
			if s == want {
				return true
			}
		}
	}
	return false
}

func truncSubject(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// startBackgroundRefresh refreshes the verifier's team list every 5
// minutes so config changes propagate without a restart.
func (v *JWTVerifier) StartBackgroundRefresh(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer v.Close()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				work, cancel := context.WithTimeout(ctx, 10*time.Second)
				_ = v.Refresh(work)
				cancel()
			}
		}
	}()
	return done
}
