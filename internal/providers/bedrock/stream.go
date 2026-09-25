package bedrock

import (
	"encoding/json"
	"fmt"
	"github.com/gatemux-dev/gatemux/internal/providers"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
)

// translateStream consumes Bedrock's event-stream output and emits
// OpenAI-shaped SSE chunks on the pipe writer. The downstream V1Handler
// passes these bytes through unchanged.
//
// AWS event-stream signals transport errors by closing the channel and
// stashing the error on GetStream().Err() — checking that AFTER the
// range loop completes is the only way to distinguish clean end-of-stream
// from truncation. Mirrors the Anthropic adapter's CloseWithError pattern
// so clients see a real error instead of a clean [DONE].
func translateStream(stream *bedrockruntime.ConverseStreamOutput, dst *io.PipeWriter, model string) {
	id := fmt.Sprintf("chatcmpl-bedrock-%d", time.Now().UnixNano())
	es := stream.GetStream()
	defer es.Close()
	defer dst.Close()
	finished := false

	for evt := range es.Events() {
		switch e := evt.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if d, ok := e.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				if err := writeChunk(dst, id, model, d.Value, ""); err != nil {
					_ = dst.CloseWithError(err)
					return
				}
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			finish := string(e.Value.StopReason)
			if err := writeChunk(dst, id, model, "", finish); err != nil {
				_ = dst.CloseWithError(err)
				return
			}
			finished = true
		case *types.ConverseStreamOutputMemberMetadata:
			u, err := canonicalUsage(e.Value.Usage)
			if err != nil {
				_ = dst.CloseWithError(err)
				return
			}
			if err := providers.WriteUsageChunk(dst, id, model, []any{}, &u); err != nil {
				_ = dst.CloseWithError(err)
				return
			}
		}
	}
	if err := es.Err(); err != nil {
		_ = dst.CloseWithError(fmt.Errorf("bedrock stream: %w", err))
		return
	}
	if !finished {
		_ = dst.CloseWithError(io.ErrUnexpectedEOF)
		return
	}
	_, _ = fmt.Fprint(dst, "data: [DONE]\n\n")
	_ = dst.Close()
}

func writeChunk(dst io.Writer, id, model, deltaText, finishReason string) error {
	choice := map[string]any{"index": 0, "delta": map[string]any{}}
	if deltaText != "" {
		choice["delta"] = map[string]any{"role": "assistant", "content": deltaText}
	}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
	raw, _ := json.Marshal(chunk)
	_, err := fmt.Fprintf(dst, "data: %s\n\n", raw)
	return err
}
