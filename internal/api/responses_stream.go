package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/providers"
	"github.com/gatemux-dev/gatemux/internal/store"
)

func (h *V1Handler) forwardResponseStream(w http.ResponseWriter, r *http.Request, source *chatStream, binding *store.ResponseBinding, storeResponse bool) (*providers.ResponseUsage, int, error) {
	writer := newStreamWriter(r.Context(), w, source.config.WriteTimeout)
	defer writer.close()
	puller := newEventPuller(source, writer)
	defer puller.close()
	committed, stored := false, false
	var usage *providers.ResponseUsage
	fail := func(err error) (*providers.ResponseUsage, int, error) {
		status := proxyErrorStatus(err)
		if committed {
			data, _ := json.Marshal(map[string]any{"type": "error", "code": "gateway_stream_error", "message": "Response stream interrupted before completion", "param": nil})
			_ = writer.write("event: error\ndata: " + string(data) + "\n\n")
		} else {
			writeJSONError(w, status, "upstream_error", "response stream could not be validated or recorded")
		}
		return usage, status, err
	}
	for {
		event, err := puller.next()
		if err != nil {
			return fail(err)
		}
		if !event.hasData || event.data == "" {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(event.data), &raw); err != nil || raw == nil {
			return fail(fmt.Errorf("malformed Responses event"))
		}
		var kind string
		if err := json.Unmarshal(raw["type"], &kind); err != nil || kind == "" {
			return fail(fmt.Errorf("Responses event type missing"))
		}
		for line := range strings.SplitSeq(event.fields, "\n") {
			field, value, _ := strings.Cut(line, ":")
			if field == "event" && strings.TrimPrefix(value, " ") != kind {
				return fail(fmt.Errorf("Responses SSE event name disagrees with JSON type"))
			}
		}
		terminal := kind == "response.completed" || kind == "response.incomplete" || kind == "response.failed"
		if body := raw["response"]; len(body) > 0 && string(body) != "null" {
			rewritten, envelope, err := rewriteResponse(body, binding)
			if err != nil {
				return fail(err)
			}
			if envelope.Usage != nil {
				usage = envelope.Usage
			}
			if terminal && kind != "response."+envelope.Status {
				return fail(fmt.Errorf("inconsistent response terminal status"))
			}
			if storeResponse && (!stored || terminal) {
				if err := h.saveResponseBinding(r.Context(), *binding, !stored); err != nil {
					return fail(err)
				}
				stored = true
			}
			raw["response"] = rewritten
		} else if terminal {
			return fail(fmt.Errorf("terminal Responses event omitted response"))
		}
		if kind == "error" {
			if !committed {
				return fail(errUpstreamStreamReported)
			}
		} else if binding.UpstreamID == "" {
			return fail(fmt.Errorf("response metadata missing before output"))
		}
		if _, ok := raw["response_id"]; ok {
			raw["response_id"], _ = json.Marshal(binding.ID)
		}
		data, err := json.Marshal(raw)
		if err != nil {
			return fail(err)
		}
		if err := source.pauseIdle(); err != nil {
			return fail(err)
		}
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			committed = true
		}
		if err := writer.write(event.encode(string(data))); err != nil {
			return fail(err)
		}
		source.resumeIdle()
		if terminal || kind == "error" {
			if kind == "response.failed" || kind == "error" {
				h.Router.RecordFailure(binding.Deployment, errUpstreamStreamReported)
				return usage, 502, errUpstreamStreamReported
			}
			h.Router.RecordSuccess(binding.Deployment)
			return usage, 200, nil // native terminal, no synthetic chat [DONE]
		}
	}
}
