package server

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeadlineInterruptsSlowInboundUpload(t *testing.T) {
	done := make(chan error, 1)
	srv := httptest.NewServer(deadlineMiddleware(75 * time.Millisecond)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.Copy(io.Discard, r.Body)
		done <- err
	})))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST /v1/audio/transcriptions HTTP/1.1\r\nHost: test\r\nContent-Length: 10000\r\n\r\nx")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("partial body unexpectedly complete")
		}
	case <-time.After(time.Second):
		t.Fatal("context deadline did not unblock request body read")
	}
}
