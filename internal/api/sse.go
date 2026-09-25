package api

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

const maxStreamEventBytes = 1024 * 1024

var errStreamEventTooLarge = errors.New("upstream SSE event exceeds 1 MiB limit")

type sseEvent struct {
	fields  string
	data    string
	hasData bool
}

type sseReader struct {
	scanner *bufio.Scanner
	first   bool
}

func newSSEReader(src io.Reader) *sseReader {
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 4096), maxStreamEventBytes)
	sc.Split(splitSSELines())
	return &sseReader{scanner: sc, first: true}
}

// SSE accepts LF, CRLF and bare CR; do not dispatch an unterminated event at EOF.
func splitSSELines() bufio.SplitFunc {
	skipLF := false
	return func(data []byte, atEOF bool) (int, []byte, error) {
		prefix := 0
		if skipLF && len(data) > 0 {
			skipLF = false
			if data[0] == '\n' {
				data = data[1:]
				prefix = 1
			}
		}
		for i, b := range data {
			if b == '\n' {
				return prefix + i + 1, data[:i], nil
			}
			if b == '\r' {
				// CR is already a complete line ending. Swallow a following LF on
				// the next scan, without waiting for another byte to dispatch now.
				skipLF = true
				return prefix + i + 1, data[:i], nil
			}
		}
		if atEOF && len(data) > 0 {
			return prefix + len(data), data, nil
		}
		return prefix, nil, nil
	}
}

func (r *sseReader) next() (*sseEvent, error) {
	event := &sseEvent{}
	var data, fields strings.Builder
	size := 0
	for r.scanner.Scan() {
		line := r.scanner.Text()
		if r.first {
			line = strings.TrimPrefix(line, "\ufeff")
			r.first = false
		}
		size += len(line) + 1
		if size > maxStreamEventBytes {
			return nil, errStreamEventTooLarge
		}
		if line == "" {
			event.data = data.String()
			event.fields = fields.String()
			return event, nil
		}
		field, value, _ := strings.Cut(line, ":")
		if field == "data" {
			if event.hasData {
				data.WriteByte('\n')
			}
			event.hasData = true
			data.WriteString(strings.TrimPrefix(value, " "))
		} else {
			fields.WriteString(line)
			fields.WriteByte('\n')
		}
	}
	if err := r.scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, errStreamEventTooLarge
		}
		return nil, err
	}
	return nil, io.EOF
}

func (e *sseEvent) encode(data string) string {
	var out strings.Builder
	out.WriteString(e.fields)
	if e.hasData {
		for line := range strings.SplitSeq(data, "\n") {
			out.WriteString("data: ")
			out.WriteString(line)
			out.WriteByte('\n')
		}
	}
	out.WriteByte('\n')
	return out.String()
}
