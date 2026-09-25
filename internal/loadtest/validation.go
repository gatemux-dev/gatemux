package loadtest

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

var errInvalidResponse = errors.New("invalid gateway response")

func validateResponse(body io.Reader, mode string) error {
	switch mode {
	case "chat":
		data, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 8<<20 {
			return fmt.Errorf("%w: response too large", errInvalidResponse)
		}
		var response struct {
			ID      string            `json:"id"`
			Object  string            `json:"object"`
			Choices []json.RawMessage `json:"choices"`
			Error   json.RawMessage   `json:"error"`
		}
		if err := json.Unmarshal(data, &response); err != nil || response.ID == "" || response.Object != "chat.completion" || len(response.Choices) == 0 || (len(response.Error) != 0 && string(response.Error) != "null") {
			return errInvalidResponse
		}
		return nil
	case "sse":
		// Benchmark fixtures emit LF/CRLF. Bound both line and total event size;
		// HTTP 200, comment pings, or EOF without [DONE] are never success.
		scanner := bufio.NewScanner(body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		var data strings.Builder
		size, events := 0, 0
		for scanner.Scan() {
			line := scanner.Text()
			size += len(line) + 1
			if size > 1<<20 {
				return errInvalidResponse
			}
			if line == "" {
				value := strings.TrimSuffix(data.String(), "\n")
				if value == "[DONE]" {
					if events == 0 {
						return errInvalidResponse
					}
					return nil
				}
				if value != "" {
					var event map[string]json.RawMessage
					if err := json.Unmarshal([]byte(value), &event); err != nil || event == nil {
						return errInvalidResponse
					}
					if e := event["error"]; len(e) > 0 && string(e) != "null" {
						return errInvalidResponse
					}
					events++
				}
				data.Reset()
				size = 0
			} else if value, ok := strings.CutPrefix(line, "data:"); ok {
				data.WriteString(strings.TrimPrefix(value, " "))
				data.WriteByte('\n')
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		return fmt.Errorf("%w: missing stream terminal", errInvalidResponse)
	default:
		_, err := io.Copy(io.Discard, body)
		return err
	}
}
