package auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func jwksFixture(t *testing.T, kid string) (*ecdsa.PrivateKey, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	set := fmt.Sprintf(`{"keys":[{"kty":"EC","crv":"P-256","alg":"ES256","use":"sig","kid":%q,"x":%q,"y":%q}]}`, kid, encode(key.X.FillBytes(make([]byte, 32))), encode(key.Y.FillBytes(make([]byte, 32))))
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"sub": "fixture", "exp": time.Now().Add(time.Hour).Unix()})
	token.Header["kid"] = kid
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, set, signed
}

func TestJWKSRefreshUsesRequestContextNotExpiredBootContext(t *testing.T) {
	_, first, token1 := jwksFixture(t, "first")
	_, second, token2 := jwksFixture(t, "second")
	var current atomic.Value
	current.Store(first)
	var calls atomic.Int32
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, current.Load().(string))
	}))
	defer mock.Close()
	v := NewJWTVerifier(nil)
	defer v.Close()
	boot, cancelBoot := context.WithCancel(context.Background())
	k, err := v.loadKeyfunc(boot, mock.URL)
	cancelBoot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwt.Parse(token1, k.KeyfuncCtx(context.Background()), jwt.WithValidMethods([]string{"ES256"})); err != nil {
		t.Fatal(err)
	}
	current.Store(second)
	if _, err := jwt.Parse(token2, k.KeyfuncCtx(context.Background()), jwt.WithValidMethods([]string{"ES256"})); err != nil {
		t.Fatalf("rotation after boot context ended: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("JWKS requests: %d", calls.Load())
	}
	for range 10 {
		if _, err := jwt.Parse(token1, k.KeyfuncCtx(context.Background())); err == nil {
			t.Fatal("revoked signing key still accepted")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("unknown-key storm bypassed refresh limiter")
	}
}

func TestJWKSUnknownKeyCancellationJoinsHTTP(t *testing.T) {
	_, initial, _ := jwksFixture(t, "first")
	_, _, token := jwksFixture(t, "rotated")
	var calls atomic.Int32
	entered, canceled := make(chan struct{}), make(chan struct{})
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			_, _ = io.WriteString(w, initial)
			return
		}
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer mock.Close()
	v := NewJWTVerifier(nil)
	defer v.Close()
	k, err := v.loadKeyfunc(context.Background(), mock.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := jwt.Parse(token, k.KeyfuncCtx(ctx)); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("refresh did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled unknown key accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("key refresh ignored request cancellation")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream JWKS connection not canceled")
	}
}

func TestJWKSFetchBodyAndDeadlineBounds(t *testing.T) {
	for _, mode := range []string{"oversized", "stalled"} {
		t.Run(mode, func(t *testing.T) {
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "stalled" {
					<-r.Context().Done()
					return
				}
				// Chunked response: bound decoding even without Content-Length.
				_, _ = io.WriteString(w, `{"keys":[{"kid":"`)
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+1))
			}))
			defer mock.Close()
			v := NewJWTVerifier(nil)
			defer v.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			start := time.Now()
			if _, err := v.loadKeyfunc(ctx, mock.URL); err == nil {
				t.Fatal("invalid JWKS accepted")
			}
			if time.Since(start) > time.Second {
				t.Fatal("JWKS load exceeded deadline")
			}
		})
	}
}
