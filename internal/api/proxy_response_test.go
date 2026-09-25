package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/providers"
)

func TestProxyNativeAndBinaryLifetimes(t *testing.T) {
	for _, mode := range []string{"headers", "first", "ping_only", "idle", "truncated", "done", "binary_idle", "binary", "error"} {
		t.Run(mode, func(t *testing.T) {
			done := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				if mode == "error" {
					w.WriteHeader(429)
					_, _ = io.WriteString(w, strings.Repeat("x", providers.MaxErrorBodyBytes*2))
					return
				}
				if mode == "binary" {
					w.Header().Set("Content-Type", "audio/mpeg")
					_, _ = w.Write([]byte{0, 1, 255, 0})
					return
				}
				if mode == "binary_idle" {
					_, _ = io.WriteString(w, "partial")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if mode != "headers" {
					w.Header().Set("Content-Type", "text/event-stream")
					if mode != "first" && mode != "ping_only" {
						_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":3}}}\n\n")
					}
					if mode == "done" {
						_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
					}
					w.(http.Flusher).Flush()
				}
				if mode == "truncated" {
					return
				}
				if mode == "ping_only" {
					ticker := time.NewTicker(5 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-r.Context().Done():
							return
						case <-ticker.C:
							_, _ = io.WriteString(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
							w.(http.Flusher).Flush()
						}
					}
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			r := httptest.NewRequest("POST", "/v1/messages", nil)
			req, _ := http.NewRequest("POST", upstream.URL, nil)
			w := httptest.NewRecorder()
			cfg := config.StreamingConfig{FirstEventTimeout: 100 * time.Millisecond, IdleTimeout: 100 * time.Millisecond, KeepaliveInterval: 10 * time.Millisecond}
			start := time.Now()
			status, err := (&V1Handler{}).forwardProxy(w, r, req, cfg, true)
			if time.Since(start) > time.Second {
				t.Fatal("response deadline was not bounded")
			}
			switch mode {
			case "done", "binary", "error":
				if err != nil {
					t.Fatal(err)
				}
				if mode == "error" {
					if status != 429 || w.Body.Len() > providers.MaxErrorBodyBytes+20 {
						t.Fatal("error response not bounded/preserved")
					}
				} else if status != 200 {
					t.Fatal(status)
				}
				if mode == "done" && (!strings.Contains(w.Body.String(), "message_stop") || strings.Contains(w.Body.String(), "[DONE]")) {
					t.Fatal(w.Body.String())
				}
				if mode == "binary" && w.Body.String() != string([]byte{0, 1, 255, 0}) {
					t.Fatal("binary altered")
				}
			case "truncated":
				if status != 502 || !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("%d %v", status, err)
				}
			default:
				if status != 504 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("%d %v", status, err)
				}
				if mode == "idle" && !strings.Contains(w.Body.String(), ": gatemux keepalive") {
					t.Fatal("keepalives not emitted")
				}
				if mode == "headers" || mode == "first" || mode == "ping_only" {
					if w.Code != 504 || strings.Contains(w.Body.String(), "keepalive") {
						t.Fatal("committed success before meaningful event")
					}
				}
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("upstream not cancelled")
			}
		})
	}
}

func TestProxyOpaqueSSEAllowsEOFAndDropsUnsafeHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Connection", "X-Private")
		w.Header().Set("X-Private", "secret")
		w.Header().Set("Set-Cookie", "admin=bad")
		w.Header().Set("X-Trace", "safe")
		_, _ = io.WriteString(w, "event: custom\ndata: {\"unknown\":9007199254740993}\n\n")
	}))
	defer upstream.Close()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	w := httptest.NewRecorder()
	status, err := (&V1Handler{}).forwardProxy(w, httptest.NewRequest("GET", "/passthrough/test", nil), req, config.StreamingConfig{}, false)
	if err != nil || status != 200 || !strings.Contains(w.Body.String(), "9007199254740993") {
		t.Fatalf("%d %v %s", status, err, w.Body)
	}
	if w.Header().Get("X-Private") != "" || w.Header().Get("Set-Cookie") != "" || w.Header().Get("X-Trace") != "safe" {
		t.Fatal(w.Header())
	}
}

func TestProxyDisconnectAndAbort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		cancel()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	req, _ := http.NewRequest("GET", upstream.URL, nil)
	status, err := (&V1Handler{}).forwardProxy(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(ctx), req, config.StreamingConfig{}, false)
	if status != 499 || !errors.Is(err, context.Canceled) {
		t.Fatalf("%d %v", status, err)
	}
	defer func() {
		if recover() != http.ErrAbortHandler {
			t.Error("partial response did not abort transport")
		}
	}()
	abortIncompleteProxy(&committedProxyError{io.ErrUnexpectedEOF})
}
