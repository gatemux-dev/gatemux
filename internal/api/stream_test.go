package api

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/go-chi/chi/v5/middleware"
)

func TestSSEFramingAndBounds(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		input := strings.Join([]string{"\ufeffevent: message", "id: 42", "data:{", `data: "model":"private",`, `data: "choices":[]}`, "", "data:[DONE]", "", ""}, newline)
		r := newSSEReader(strings.NewReader(input))
		event, err := r.next()
		if err != nil || event.data != "{\n\"model\":\"private\",\n\"choices\":[]}" || event.fields != "event: message\nid: 42\n" {
			t.Fatalf("newline %q: event %+v err %v", newline, event, err)
		}
		if next, err := r.next(); err != nil || next.data != "[DONE]" {
			t.Fatalf("terminal event: %+v %v", next, err)
		}
	}
	for _, input := range []string{strings.Repeat("x", maxStreamEventBytes), strings.Repeat("data: small\n", maxStreamEventBytes/10) + "\n"} {
		if _, err := newSSEReader(strings.NewReader(input)).next(); !errors.Is(err, errStreamEventTooLarge) {
			t.Fatalf("unbounded event accepted: %v", err)
		}
	}
	if _, err := newSSEReader(strings.NewReader("data: partial\n")).next(); !errors.Is(err, io.EOF) {
		t.Fatalf("unterminated event dispatched: %v", err)
	}
}

func TestStreamFirstEventTimeoutIgnoresCommentsAndPartialData(t *testing.T) {
	for _, fragment := range []string{": ping\n\n", "data: "} {
		t.Run(fragment, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer pw.Close()
			s := newChatStream(context.Background(), config.StreamingConfig{FirstEventTimeout: 50 * time.Millisecond})
			defer s.Close()
			s.attach(pr)
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					if _, err := io.WriteString(pw, fragment); err != nil {
						return
					}
					select {
					case <-s.ctx.Done():
						return
					case <-time.After(5 * time.Millisecond):
					}
				}
			}()
			if err := s.prefetch(); !errors.Is(err, errStreamFirstEventTimeout) {
				t.Fatalf("first event error = %v", err)
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("timeout did not unblock producer")
			}
		})
	}
}

func TestSSEBareCRDispatchesWithoutWaitingForAnotherByte(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	s := newChatStream(context.Background(), config.StreamingConfig{FirstEventTimeout: 200 * time.Millisecond})
	defer s.Close()
	s.attach(pr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(pw, "data: {}\r\r")
	}()
	if err := s.prefetch(); err != nil {
		t.Fatalf("complete CR event stalled: %v", err)
	}
	<-done
}

func TestStreamIdleDeadlineAndContextCancellation(t *testing.T) {
	for _, cancelParent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		pr, pw := io.Pipe()
		s := newChatStream(ctx, config.StreamingConfig{IdleTimeout: 50 * time.Millisecond})
		s.attach(pr)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.WriteString(pw, "data: {\"choices\":[]}\n\n")
		}()
		if err := s.prefetch(); err != nil {
			t.Fatal(err)
		}
		<-done
		if _, err := s.next(); err != nil {
			t.Fatal(err)
		}
		want := errStreamIdleTimeout
		if cancelParent {
			cancel()
			want = context.Canceled
		}
		if _, err := s.next(); !errors.Is(err, want) {
			t.Fatalf("stream read error = %v, want %v", err, want)
		}
		s.Close()
		pw.Close()
		cancel()
	}
}

func TestStreamProgressResetsIdleButNotTotalDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 220*time.Millisecond)
	defer cancel()
	pr, pw := io.Pipe()
	defer pw.Close()
	s := newChatStream(ctx, config.StreamingConfig{FirstEventTimeout: time.Second, IdleTimeout: 150 * time.Millisecond})
	defer s.Close()
	s.attach(pr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := io.WriteString(pw, "data: {}\n\n"); err != nil {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	}()
	count := 0
	for {
		_, err := s.next()
		if err != nil {
			if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errStreamIdleTimeout) {
				t.Fatalf("total deadline: %v", err)
			}
			break
		}
		count++
	}
	<-done
	if count < 2 {
		t.Fatalf("expected repeated progress, got %d events", count)
	}
}

func TestStreamProxyPreservesFieldsUsageAndStopsAtDone(t *testing.T) {
	input := "event: message\nid: 123\ndata:{\ndata: \"id\":\"chunk\",\"model\":\"private\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}],\ndata: \"usage\":{\"prompt_tokens\":4,\"completion_tokens\":2},\"future\":true}\n\n: ping\n\ndata:[DONE]\n\ndata: unexpected\n\n"
	s := newChatStream(context.Background(), config.StreamingConfig{})
	defer s.Close()
	s.attach(io.NopCloser(strings.NewReader(input)))
	if err := s.prefetch(); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	writer := newStreamWriter(context.Background(), w, time.Second)
	defer writer.close()
	proxy := &streamProxy{src: s, w: writer, alias: "public"}
	if err := proxy.Run(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"event: message\nid: 123\n", `"model":"public"`, `"future":true`, `"tool_calls"`, ": ping\n\n", "data: [DONE]\n\n"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %q: %s", want, w.Body.String())
		}
	}
	if strings.Contains(w.Body.String(), "unexpected") || proxy.upstreamModel != "private" || proxy.usage == nil || proxy.usage.CompletionTokens != 2 {
		t.Fatalf("stream terminal/usage preservation: %s %+v", w.Body.String(), proxy)
	}
}

func TestStreamProxyTruncationAndErrorAreNotSuccess(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  error
	}{
		{"data: {}\n\n", io.ErrUnexpectedEOF},
		{"data: null\n\n", io.ErrUnexpectedEOF},
		{"data: {\"error\":{\"message\":\"failed\"}}\n\n", errUpstreamStreamReported},
	} {
		s := newChatStream(context.Background(), config.StreamingConfig{})
		s.attach(io.NopCloser(strings.NewReader(tc.input)))
		w := httptest.NewRecorder()
		writer := newStreamWriter(context.Background(), w, time.Second)
		proxy := &streamProxy{src: s, w: writer, alias: "public"}
		err := proxy.Run()
		writer.close()
		s.Close()
		if !errors.Is(err, tc.want) || strings.Contains(w.Body.String(), "[DONE]") {
			t.Fatalf("false success: %v, body %s", err, w.Body)
		}
	}
}

type slowStreamRecorder struct{ *httptest.ResponseRecorder }

func (w *slowStreamRecorder) Write(p []byte) (int, error) {
	<-time.After(100 * time.Millisecond)
	return w.ResponseRecorder.Write(p)
}

func (w *slowStreamRecorder) WriteString(p string) (int, error) { return w.Write([]byte(p)) }

func TestStreamBackpressureDoesNotConsumeUpstreamIdleBudget(t *testing.T) {
	s := newChatStream(context.Background(), config.StreamingConfig{IdleTimeout: 50 * time.Millisecond})
	defer s.Close()
	s.attach(io.NopCloser(strings.NewReader("data: {}\n\n: ping\n\ndata: {}\n\ndata: [DONE]\n\n")))
	w := &slowStreamRecorder{httptest.NewRecorder()}
	writer := newStreamWriter(context.Background(), w, time.Second)
	defer writer.close()
	proxy := &streamProxy{src: s, w: writer}
	if err := proxy.Run(); err != nil {
		t.Fatalf("client backpressure counted as upstream stall: %v", err)
	}
}

func TestStreamIdleCommentsDoNotResetBudget(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	s := newChatStream(context.Background(), config.StreamingConfig{IdleTimeout: 60 * time.Millisecond})
	defer s.Close()
	s.attach(pr)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.WriteString(pw, "data: {}\n\n")
		for {
			if _, err := io.WriteString(pw, ": ping\n\n"); err != nil {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	writer := newStreamWriter(context.Background(), httptest.NewRecorder(), time.Second)
	defer writer.close()
	proxy := &streamProxy{src: s, w: writer}
	if err := proxy.Run(); !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("comment-only idle error = %v", err)
	}
	<-done
}

func TestStreamWriterSlowClientAndCancellation(t *testing.T) {
	for _, disconnect := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		started := make(chan struct{})
		var writes atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			timeout := 100 * time.Millisecond
			if disconnect {
				timeout = 10 * time.Second // cancellation must interrupt, not await this
			}
			writer := newStreamWriter(ctx, middleware.NewWrapResponseWriter(w, r.ProtoMajor), timeout)
			defer writer.close()
			close(started)
			payload := "data: " + strings.Repeat("x", 256*1024) + "\n\n"
			for i := 0; i < 1024; i++ {
				if err := writer.write(payload); err != nil {
					done <- err
					return
				}
				writes.Add(1)
			}
			done <- errors.New("socket unexpectedly accepted 256 MiB")
		}))
		conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: localhost\r\n\r\n")
		<-started
		if disconnect {
			// Allow the tiny receive window to fill, so cancellation interrupts
			// an active write rather than only short-circuiting before the first.
			<-time.After(100 * time.Millisecond)
			cancel()
		}
		select {
		case err := <-done:
			var writeErr *streamWriteError
			if !errors.As(err, &writeErr) {
				t.Errorf("write error = %v, writes = %d", err, writes.Load())
			}
		case <-time.After(3 * time.Second):
			t.Error("slow client held handler beyond deadline")
		}
		cancel()
		conn.Close()
		server.Close()
	}
}
