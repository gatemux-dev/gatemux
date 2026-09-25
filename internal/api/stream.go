package api

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gatemux-dev/gatemux/internal/providers"
)

// streamProxy reads OpenAI-shaped SSE events from src, rewrites each chunk's
// `model` field to the public alias, captures the final `usage` event, and
// emits the (modified) chunks to w.
type streamProxy struct {
	src   *chatStream
	w     *streamWriter
	alias string
	guard *guardrailRun

	requestID     string
	upstreamModel string
	usage         *providers.Usage
	chunks        int

	// collect gathers the streamed assistant text for payload capture,
	// bounded by maxCapturedStreamText.
	collect   bool
	text      strings.Builder
	truncated bool
	finish    string
}

const maxCapturedStreamText = 256 << 10

var errUpstreamStreamReported = errors.New("upstream reported a stream error")

func (s *streamProxy) Run() error {
	puller := newEventPuller(s.src, s.w)
	defer puller.close()
	for {
		event, err := puller.next()
		if err != nil {
			return err
		}
		data := event.data
		var reported struct {
			Error json.RawMessage `json:"error"`
		}
		_ = json.Unmarshal([]byte(data), &reported)
		hasError := len(reported.Error) > 0 && string(reported.Error) != "null"
		if event.hasData && data != "[DONE]" && !hasError {
			data = s.processChunk(data)
		}
		if s.guard != nil {
			// Provider comments/event IDs/errors may contain uninspected content.
			if !event.hasData {
				continue
			}
			if hasError {
				return s.guard.finish(errUpstreamStreamReported)
			}
			if data == "[DONE]" {
				if err := s.guard.finish(s.guard.post.Complete()); err != nil {
					return err
				}
			} else {
				guarded, err := s.guard.response([]byte(data), true)
				if err != nil {
					return err
				}
				data = string(guarded)
			}
		}
		if err := s.src.pauseIdle(); err != nil {
			return err
		}
		encoded := event.encode(data)
		if s.guard != nil {
			encoded = "data: " + data + "\n\n"
		}
		if err := s.w.write(encoded); err != nil {
			return err
		}
		s.src.resumeIdle()
		if hasError {
			return errUpstreamStreamReported
		}
		if event.hasData && event.data == "[DONE]" {
			return nil // do not wait for EOF from an upstream keeping HTTP open
		}
	}
}

func (s *streamProxy) processChunk(data string) string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &raw); err != nil || raw == nil {
		return data
	}
	if s.upstreamModel == "" {
		if m, ok := raw["model"]; ok {
			_ = json.Unmarshal(m, &s.upstreamModel)
		}
	}
	if s.requestID == "" {
		if id, ok := raw["id"]; ok {
			_ = json.Unmarshal(id, &s.requestID)
		}
	}
	if s.collect {
		s.collectChunk(raw["choices"])
	}
	if u, ok := raw["usage"]; ok && string(u) != "null" {
		var u2 providers.Usage
		if err := json.Unmarshal(u, &u2); err == nil && providers.ReportedUsage(u, true) {
			s.usage = &u2
		}
	}
	if s.alias != "" {
		if aliasJSON, err := json.Marshal(s.alias); err == nil {
			raw["model"] = aliasJSON
		}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return data
	}
	s.chunks++
	return string(out)
}

func (s *streamProxy) collectChunk(choices json.RawMessage) {
	if len(choices) == 0 {
		return
	}
	var parsed []struct {
		Index int `json:"index"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	}
	if json.Unmarshal(choices, &parsed) != nil {
		return
	}
	for _, c := range parsed {
		if c.Index != 0 {
			continue
		}
		if c.FinishReason != nil {
			s.finish = *c.FinishReason
		}
		if s.truncated || c.Delta.Content == "" {
			continue
		}
		if s.text.Len()+len(c.Delta.Content) > maxCapturedStreamText {
			s.truncated = true
			continue
		}
		s.text.WriteString(c.Delta.Content)
	}
}

// capturedResponse renders the collected stream as one chat completion, the
// same shape a non-streamed response is captured in.
func (s *streamProxy) capturedResponse(alias string, usage providers.Usage) []byte {
	out := map[string]any{
		"object":   "chat.completion",
		"model":    alias,
		"streamed": true,
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": s.text.String()},
			"finish_reason": s.finish,
		}},
		"usage": usage,
	}
	if s.truncated {
		out["truncated"] = true
	}
	b, _ := json.Marshal(out)
	return b
}
