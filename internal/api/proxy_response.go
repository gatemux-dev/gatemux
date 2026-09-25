package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/config"
	"github.com/gatemux-dev/gatemux/internal/providers"
)

// forwardProxy owns one complete upstream attempt. Every response copy has
// bounded first-progress, idle, write and inherited total deadlines. No retry
// occurs after forwarding any data. The returned status is for accounting; it
// may differ from the already-committed HTTP status when a stream fails.
func (h *V1Handler) forwardProxy(w http.ResponseWriter, r *http.Request, req *http.Request, cfg config.StreamingConfig, nativeMessages bool) (status int, resultErr error) {
	src := newChatStream(r.Context(), cfg)
	defer src.Close()
	committed := false
	defer func() {
		if resultErr == nil {
			return
		}
		status = proxyErrorStatus(resultErr)
		if !committed {
			writeJSONError(w, status, "upstream_error", "upstream response failed: "+resultErr.Error())
		} else {
			// HTTP cannot change status after a partial binary/JSON response.
			// Abort its transport so clients cannot mistake truncation for success.
			// Native SSE clients also see an incomplete terminal sequence.
			if h.Logger != nil {
				h.Logger.Warn("interrupted proxy response", "status", status, "error", resultErr)
			}
			resultErr = &committedProxyError{resultErr}
		}
	}()
	resp, err := h.passthroughClient().Do(req.WithContext(src.ctx))
	if err != nil {
		return 0, err
	}
	src.attach(resp.Body)
	writer := newStreamWriter(r.Context(), w, src.config.WriteTimeout)
	defer writer.close()
	commit := func() {
		copyProxyResponseHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		committed = true
	}
	if resp.StatusCode >= 300 {
		body := providers.ReadErrorBody(resp.Body)
		if err := context.Cause(src.ctx); err != nil {
			return 0, err
		}
		if err := src.pauseIdle(); err != nil {
			return 0, err
		}
		commit()
		return resp.StatusCode, writer.write(string(body))
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType == "text/event-stream" {
		src.allowEOF = !nativeMessages
		if nativeMessages {
			src.progress = func(e *sseEvent) bool { return e.hasData && e.data != "" && nativeEventType(e) != "ping" }
		}
		if err := src.prefetch(); err != nil {
			if errors.Is(err, io.EOF) && !nativeMessages {
				commit()
				return resp.StatusCode, nil
			}
			return 0, err
		}
		if err := src.pauseIdle(); err != nil {
			return 0, err
		}
		w.Header().Set("X-Accel-Buffering", "no")
		commit()
		puller := newEventPuller(src, writer)
		defer puller.close()
		for {
			event, err := puller.next()
			if errors.Is(err, io.EOF) && !nativeMessages {
				return resp.StatusCode, nil
			}
			if err != nil {
				return 0, err
			}
			if err := src.pauseIdle(); err != nil {
				return 0, err
			}
			if err := writer.write(event.encode(event.data)); err != nil {
				return 0, err
			}
			src.resumeIdle()
			if nativeMessages {
				switch nativeEventType(event) {
				case "message_stop":
					return resp.StatusCode, nil
				case "error":
					return 0, errUpstreamStreamReported
				}
			}
		}
	}
	buf := make([]byte, 32<<10)
	for {
		n, readErr := src.readBytes(buf)
		if n > 0 || errors.Is(readErr, io.EOF) {
			if err := src.pauseIdle(); err != nil {
				return 0, err
			}
			if !committed {
				commit()
			}
			if n > 0 {
				if err := writer.write(string(buf[:n])); err != nil {
					return 0, err
				}
			}
			src.resumeIdle()
		}
		if errors.Is(readErr, io.EOF) {
			return resp.StatusCode, nil
		}
		if readErr != nil {
			return 0, readErr
		}
	}
}

type committedProxyError struct{ error }

func (e *committedProxyError) Unwrap() error { return e.error }

// Call only after recording the outcome; deferred permits/budgets still run.
func abortIncompleteProxy(err error) {
	var committed *committedProxyError
	if errors.As(err, &committed) {
		panic(http.ErrAbortHandler)
	}
}

func nativeEventType(e *sseEvent) string {
	var data struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(e.data), &data)
	return data.Type
}

func proxyErrorStatus(err error) int {
	var writeErr *streamWriteError
	if errors.As(err, &writeErr) || errors.Is(err, context.Canceled) {
		return 499
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

func connectionHeaders(h http.Header) map[string]bool {
	out := map[string]bool{}
	for _, line := range h.Values("Connection") {
		for _, value := range strings.Split(line, ",") {
			out[strings.ToLower(strings.TrimSpace(value))] = true
		}
	}
	return out
}

func copyProxyResponseHeaders(dst, src http.Header) {
	nominated := connectionHeaders(src)
	for k, values := range src {
		// An upstream service must not set cookies for the gateway admin origin.
		if shouldHopByHop(k) || nominated[strings.ToLower(k)] || strings.EqualFold(k, "Set-Cookie") {
			continue
		}
		dst[k] = append([]string(nil), values...)
	}
}
