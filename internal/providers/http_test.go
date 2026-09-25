package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestBoundedErrorBody(t *testing.T) {
	r := &io.LimitedReader{R: strings.NewReader(strings.Repeat("x", MaxErrorBodyBytes*4)), N: MaxErrorBodyBytes * 4}
	b := ReadErrorBody(r)
	if len(b) > MaxErrorBodyBytes+len(" [truncated]") || !strings.HasSuffix(string(b), " [truncated]") || r.N != MaxErrorBodyBytes*3-1 {
		t.Fatalf("unbounded diagnostic: %d bytes, unread %d", len(b), r.N)
	}
	if got := string(ReadErrorBody(strings.NewReader("short error"))); got != "short error" {
		t.Fatal(got)
	}
}

func TestProviderTransportBoundsAndNoCredentialRedirect(t *testing.T) {
	c := NewHTTPClient()
	defer c.CloseIdleConnections()
	tr := c.Transport.(*http.Transport)
	if tr.MaxConnsPerHost != 1024 || tr.TLSHandshakeTimeout <= 0 || tr.MaxResponseHeaderBytes != 64<<10 {
		t.Fatal("missing transport bounds")
	}
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	resp, err := c.Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if reached.Load() || resp.StatusCode != 307 {
		t.Fatal("followed credential-bearing redirect")
	}
}
