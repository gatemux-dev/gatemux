package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gatemux-dev/gatemux/internal/loadtest"
)

const defaultBody = `{"model":"benchmark","messages":[{"role":"user","content":"Reply with ok."}],"max_tokens":8}`

type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ",") }
func (h *headerFlags) Set(value string) error {
	if !strings.Contains(value, ":") {
		return errors.New("header must use 'Name: value' format")
	}
	*h = append(*h, value)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gatemux-load:", err)
		os.Exit(1)
	}
}

func run() error {
	var headers headerFlags
	url := flag.String("url", "", "gateway endpoint URL (required)")
	token := flag.String("token", "", "bearer token (not included in results)")
	label := flag.String("label", "", "result label, for example local-gateway")
	rate := flag.Float64("rate", 100, "fixed offered requests per second")
	duration := flag.Duration("duration", 30*time.Second, "measurement duration")
	warmup := flag.Duration("warmup", 5*time.Second, "unreported warmup duration")
	timeout := flag.Duration("request-timeout", 2*time.Minute, "per-request timeout")
	maxInFlight := flag.Int("max-in-flight", 1024, "bounded client-side concurrent request cap")
	expectedStatus := flag.Int("expect-status", http.StatusOK, "HTTP status counted as success")
	bodyFile := flag.String("body-file", "", "JSON request body file; defaults to a small chat request")
	stream := flag.Bool("stream", false, "set stream=true in the built-in request body")
	validation := flag.String("validate", "auto", "response validation: auto, chat, sse, none")
	output := flag.String("output", "-", "result JSON path, or - for stdout")
	flag.Var(&headers, "header", "additional request header in 'Name: value' format; repeatable")
	flag.Parse()

	body := []byte(defaultBody)
	if *bodyFile != "" {
		var err error
		body, err = os.ReadFile(*bodyFile)
		if err != nil {
			return fmt.Errorf("read body file: %w", err)
		}
	} else if *stream {
		var envelope map[string]any
		if err := json.Unmarshal(body, &envelope); err != nil {
			return err
		}
		envelope["stream"] = true
		var err error
		body, err = json.Marshal(envelope)
		if err != nil {
			return err
		}
	}

	httpHeaders := make(http.Header)
	for _, raw := range headers {
		name, value, _ := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("invalid empty header name in %q", raw)
		}
		httpHeaders.Add(name, strings.TrimSpace(value))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *validation == "auto" {
		var envelope struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &envelope)
		*validation = "chat"
		if envelope.Stream {
			*validation = "sse"
		}
	}
	result, err := loadtest.Run(ctx, loadtest.Config{
		Label: *label, URL: *url, Token: *token, Body: body, Headers: httpHeaders,
		Rate: *rate, Duration: *duration, Warmup: *warmup,
		RequestTimeout: *timeout, MaxInFlight: *maxInFlight,
		ExpectedStatus: *expectedStatus,
		Validation:     *validation,
	})
	if err != nil {
		return err
	}

	var writer *os.File
	if *output == "-" {
		writer = os.Stdout
	} else {
		writer, err = os.Create(*output)
		if err != nil {
			return fmt.Errorf("create output: %w", err)
		}
		defer writer.Close()
	}
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return fmt.Errorf("write result: %w", err)
	}
	return nil
}
