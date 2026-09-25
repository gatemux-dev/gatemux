package providers

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"
)

const MaxErrorBodyBytes = 64 << 10

type responseLimitKey struct{}

func WithResponseLimit(ctx context.Context, limit int) context.Context {
	return context.WithValue(ctx, responseLimitKey{}, limit)
}
func ResponseLimit(ctx context.Context) int {
	limit, _ := ctx.Value(responseLimitKey{}).(int)
	return limit
}

// ReadErrorBody bounds diagnostic memory even when an upstream sends an
// enormous failure page. The request context still bounds a slow body read.
func ReadErrorBody(body io.Reader) []byte {
	b, _ := io.ReadAll(io.LimitReader(body, MaxErrorBodyBytes+1))
	if len(b) > MaxErrorBodyBytes {
		b = append(b[:MaxErrorBodyBytes], []byte(" [truncated]")...)
	}
	return b
}

// NewHTTPClient owns a transport; no mutation of http.DefaultTransport and no
// independent total timeout that could silently truncate a long-lived stream.
// Callers MUST give each operation a bounded context. Response headers are
// covered by that operation's first-event/attempt deadline.
func NewHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true,
		MaxIdleConns:      1024, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 1024,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second,
		ExpectContinueTimeout: time.Second, MaxResponseHeaderBytes: 64 << 10,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// SharedHTTPClient is immutable after initialization and reused across adapters.
var SharedHTTPClient = NewHTTPClient()

// SDKs often read error payloads themselves. Bound those at the transport
// boundary without changing successful/event-stream response bodies.
func NewSDKHTTPClient() *http.Client {
	c := NewHTTPClient()
	c.Transport = &errorBoundTransport{base: c.Transport}
	return c
}

type errorBoundTransport struct{ base http.RoundTripper }
type limitedErrorBody struct {
	io.Reader
	io.Closer
}

func (t *errorBoundTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(r)
	if err == nil && resp.StatusCode >= 300 {
		resp.Body = &limitedErrorBody{io.LimitReader(resp.Body, MaxErrorBodyBytes), resp.Body}
	}
	return resp, err
}

func (t *errorBoundTransport) CloseIdleConnections() {
	if c, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		c.CloseIdleConnections()
	}
}
