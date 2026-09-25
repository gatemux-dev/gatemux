package providers

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// TranslateGeminiStream handles both direct Gemini and Vertex's identical SSE
// response envelope. Memory is bounded to one event, and pipe writes provide
// backpressure. Never synthesize successful completion after an upstream error.
func TranslateGeminiStream(src io.ReadCloser, dst *io.PipeWriter, model string) {
	defer src.Close()
	defer dst.Close()
	id := fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano())
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	finished := false
	var usage *Usage
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		var event struct {
			Candidates []struct {
				Index   int `json:"index"`
				Content struct {
					Parts []struct {
						Text    string `json:"text"`
						Thought bool   `json:"thought"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			Usage *GeminiUsage    `json:"usageMetadata"`
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			_ = dst.CloseWithError(err)
			return
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			_ = dst.CloseWithError(fmt.Errorf("Gemini stream error"))
			return
		}
		if event.Usage != nil {
			u, err := event.Usage.Canonical()
			if err != nil {
				_ = dst.CloseWithError(err)
				return
			}
			usage = &u
		}
		for _, candidate := range event.Candidates {
			var content strings.Builder
			for _, part := range candidate.Content.Parts {
				if !part.Thought {
					content.WriteString(part.Text)
				}
			}
			choice := map[string]any{"index": candidate.Index, "delta": map[string]any{"content": content.String()}}
			if candidate.FinishReason != "" {
				finished = true
				reason := "stop"
				if candidate.FinishReason == "MAX_TOKENS" {
					reason = "length"
				} else if candidate.FinishReason != "STOP" {
					reason = "content_filter"
				}
				choice["finish_reason"] = reason
			}
			if err := WriteUsageChunk(dst, id, model, []any{choice}, nil); err != nil {
				_ = dst.CloseWithError(err)
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = dst.CloseWithError(err)
		return
	}
	if !finished {
		_ = dst.CloseWithError(io.ErrUnexpectedEOF)
		return
	}
	if usage != nil {
		if err := WriteUsageChunk(dst, id, model, []any{}, usage); err != nil {
			_ = dst.CloseWithError(err)
			return
		}
	}
	_, _ = io.WriteString(dst, "data: [DONE]\n\n")
}

// WriteUsageChunk keeps canonical metadata in native-provider SSE translation.
func WriteUsageChunk(dst io.Writer, id, model string, choices []any, usage *Usage) error {
	chunk := map[string]any{"id": id, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": model, "choices": choices}
	if usage != nil {
		chunk["usage"] = usage
	}
	raw, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(dst, "data: %s\n\n", raw)
	return err
}
