// Package loadtest implements a bounded open-loop HTTP load generator used by
// GateMux's reproducible gateway benchmarks.
package loadtest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const histogramBuckets = 4096

const (
	maxOfferedRate    = 1_000_000
	maxClientInFlight = 1_000_000
)

type Config struct {
	Label          string
	URL            string
	Token          string
	Body           []byte
	Headers        http.Header
	Rate           float64
	Duration       time.Duration
	Warmup         time.Duration
	RequestTimeout time.Duration
	MaxInFlight    int
	ExpectedStatus int
	Validation     string // none, chat, or sse (requires terminal [DONE])
}

type ConfigSummary struct {
	Label           string  `json:"label,omitempty"`
	URL             string  `json:"url"`
	Rate            float64 `json:"offered_rps"`
	DurationSeconds float64 `json:"duration_seconds"`
	WarmupSeconds   float64 `json:"warmup_seconds"`
	TimeoutSeconds  float64 `json:"request_timeout_seconds"`
	MaxInFlight     int     `json:"client_max_in_flight"`
	ExpectedStatus  int     `json:"expected_status"`
	BodyBytes       int     `json:"body_bytes"`
	BodySHA256      string  `json:"body_sha256"`
	Validation      string  `json:"response_validation"`
}

type Environment struct {
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	GoVersion  string `json:"go_version"`
	CPUs       int    `json:"logical_cpus"`
	GOMAXPROCS int    `json:"gomaxprocs"`
}

type Counts struct {
	Scheduled       uint64            `json:"scheduled"`
	Started         uint64            `json:"started"`
	ClientDropped   uint64            `json:"client_dropped"`
	Completed       uint64            `json:"completed"`
	Succeeded       uint64            `json:"succeeded"`
	HTTPError       uint64            `json:"http_error"`
	TransportError  uint64            `json:"transport_error"`
	StatusCodes     map[string]uint64 `json:"status_codes"`
	TransportErrors map[string]uint64 `json:"transport_errors"`
	HTTPErrorCodes  map[string]uint64 `json:"http_error_codes,omitempty"`
}

type LatencySummary struct {
	Count  uint64  `json:"count"`
	MinMS  float64 `json:"min_ms"`
	MeanMS float64 `json:"mean_ms"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	P99MS  float64 `json:"p99_ms"`
	MaxMS  float64 `json:"max_ms"`
}

type Result struct {
	SchemaVersion          int            `json:"schema_version"`
	StartedAt              time.Time      `json:"started_at"`
	FinishedAt             time.Time      `json:"finished_at"`
	MeasurementWallSeconds float64        `json:"measurement_wall_seconds"`
	Config                 ConfigSummary  `json:"config"`
	Environment            Environment    `json:"environment"`
	Counts                 Counts         `json:"counts"`
	Latency                LatencySummary `json:"latency"`
	FirstByte              LatencySummary `json:"first_byte"`
	CompletedRPS           float64        `json:"completed_rps"`
	SuccessfulRPS          float64        `json:"successful_rps"`
	GatewayErrorRate       float64        `json:"gateway_error_rate"`
	ClientDropRate         float64        `json:"client_drop_rate"`
}

type collector struct {
	mu        sync.Mutex
	counts    Counts
	latency   latencyHistogram
	firstByte latencyHistogram
}

type latencyHistogram struct {
	buckets [histogramBuckets]uint64
	count   uint64
	min     time.Duration
	max     time.Duration
	totalNS float64
}

func Run(ctx context.Context, cfg Config) (Result, error) {
	if err := validate(cfg); err != nil {
		return Result{}, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = cfg.MaxInFlight
	transport.MaxIdleConnsPerHost = cfg.MaxInFlight
	transport.MaxConnsPerHost = cfg.MaxInFlight
	client := &http.Client{Transport: transport, Timeout: cfg.RequestTimeout}
	defer transport.CloseIdleConnections()

	if cfg.Warmup > 0 {
		if _, err := runPhase(ctx, client, cfg, cfg.Warmup); err != nil {
			return Result{}, fmt.Errorf("warmup: %w", err)
		}
	}
	return runPhase(ctx, client, cfg, cfg.Duration)
}

func validate(cfg Config) error {
	switch {
	case cfg.URL == "":
		return errors.New("target URL is required")
	case cfg.Rate <= 0 || math.IsNaN(cfg.Rate) || math.IsInf(cfg.Rate, 0):
		return errors.New("rate must be positive")
	case cfg.Rate > maxOfferedRate:
		return fmt.Errorf("rate must not exceed %d requests per second", maxOfferedRate)
	case cfg.Duration <= 0:
		return errors.New("duration must be positive")
	case cfg.RequestTimeout <= 0:
		return errors.New("request timeout must be positive")
	case cfg.MaxInFlight <= 0:
		return errors.New("max in flight must be positive")
	case cfg.MaxInFlight > maxClientInFlight:
		return fmt.Errorf("max in flight must not exceed %d", maxClientInFlight)
	case cfg.ExpectedStatus < 100 || cfg.ExpectedStatus > 599:
		return errors.New("expected status must be between 100 and 599")
	case cfg.Validation != "" && cfg.Validation != "none" && cfg.Validation != "chat" && cfg.Validation != "sse":
		return errors.New("validation must be none, chat, or sse")
	}
	return nil
}

func runPhase(ctx context.Context, client *http.Client, cfg Config, duration time.Duration) (Result, error) {
	startedAt := time.Now()
	deadline := startedAt.Add(duration)
	sem := make(chan struct{}, cfg.MaxInFlight)
	c := &collector{counts: Counts{
		StatusCodes:     map[string]uint64{},
		TransportErrors: map[string]uint64{},
		HTTPErrorCodes:  map[string]uint64{},
	}}
	var wg sync.WaitGroup

	for sequence := uint64(0); ; sequence++ {
		due := startedAt.Add(time.Duration(float64(sequence) / cfg.Rate * float64(time.Second)))
		if !due.Before(deadline) {
			break
		}
		if err := waitUntil(ctx, due); err != nil {
			wg.Wait()
			return Result{}, err
		}

		c.mu.Lock()
		c.counts.Scheduled++
		c.mu.Unlock()
		select {
		case sem <- struct{}{}:
			c.mu.Lock()
			c.counts.Started++
			c.mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				execute(ctx, client, cfg, c)
			}()
		default:
			c.mu.Lock()
			c.counts.ClientDropped++
			c.mu.Unlock()
		}
	}
	if err := waitUntil(ctx, deadline); err != nil {
		wg.Wait()
		return Result{}, err
	}
	wg.Wait()
	finishedAt := time.Now()
	return c.result(cfg, startedAt, finishedAt), nil
}

func waitUntil(ctx context.Context, due time.Time) error {
	wait := time.Until(due)
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func execute(ctx context.Context, client *http.Client, cfg Config, c *collector) {
	started := time.Now()
	var firstByte atomic.Int64
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotFirstResponseByte: func() { firstByte.CompareAndSwap(0, int64(time.Since(started))) }})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.URL, bytes.NewReader(cfg.Body))
	if err == nil {
		req.Header.Set("Content-Type", "application/json")
		for key, values := range cfg.Headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
		if cfg.Token != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.Token)
		}
	}

	statusCode := 0
	if err == nil {
		var resp *http.Response
		resp, err = client.Do(req)
		if resp != nil {
			statusCode = resp.StatusCode
			var copyErr error
			if statusCode == cfg.ExpectedStatus {
				copyErr = validateResponse(resp.Body, cfg.Validation)
			} else {
				var body []byte
				body, copyErr = io.ReadAll(io.LimitReader(resp.Body, 64<<10))
				// Fixed vocabulary only; neither messages nor unknown provider codes
				// can leak payloads or grow a benchmark's result cardinality.
				code := classifyHTTPError(body)
				c.mu.Lock()
				c.counts.HTTPErrorCodes[code]++
				c.mu.Unlock()
			}
			closeErr := resp.Body.Close()
			if err == nil {
				err = copyErr
			}
			if err == nil {
				err = closeErr
			}
		}
	}

	c.record(time.Since(started), time.Duration(firstByte.Load()), statusCode, cfg.ExpectedStatus, err)
}

func classifyHTTPError(body []byte) string {
	var envelope struct {
		Error struct {
			Code string `json:"code"`
			Type string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) == nil {
		if envelope.Error.Code == "" {
			envelope.Error.Code = envelope.Error.Type
		}
		switch envelope.Error.Code {
		case "concurrency_limit_exceeded", "concurrency_limit_unavailable", "server_overloaded",
			"server_draining", "rate_limit_exceeded", "rate_limit_unavailable", "accounting_unavailable",
			"budget_unavailable", "budget_exceeded", "guardrail_unavailable", "guardrail_blocked":
			return envelope.Error.Code
		}
	}
	return "other"
}

func (c *collector) record(latency, firstByte time.Duration, statusCode, expectedStatus int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts.Completed++
	c.latency.observe(latency)
	if firstByte > 0 {
		c.firstByte.observe(firstByte)
	}
	if statusCode > 0 {
		c.counts.StatusCodes[strconv.Itoa(statusCode)]++
	}
	if err != nil {
		c.counts.TransportError++
		c.counts.TransportErrors[classifyTransportError(err)]++
		return
	}
	if statusCode != expectedStatus {
		c.counts.HTTPError++
		return
	}
	c.counts.Succeeded++
}

// classifyTransportError intentionally returns a fixed vocabulary. Benchmark
// diagnostics stay useful without allowing error strings or upstream data to
// create an unbounded result map.
func classifyTransportError(err error) string {
	switch {
	case errors.Is(err, errInvalidResponse):
		return "invalid_response"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "unexpected_eof"
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return "network_timeout"
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil && urlErr.Err != err {
		return classifyTransportError(urlErr.Err)
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection reset"):
		return "connection_reset"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "broken pipe"):
		return "broken_pipe"
	case strings.Contains(message, "server closed idle connection"):
		return "idle_connection_closed"
	case strings.Contains(message, "tls"):
		return "tls_error"
	default:
		return "other"
	}
}

func (c *collector) result(cfg Config, startedAt, finishedAt time.Time) Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	wall := finishedAt.Sub(startedAt).Seconds()
	bodyHash := sha256.Sum256(cfg.Body)
	result := Result{
		SchemaVersion:          2,
		StartedAt:              startedAt.UTC(),
		FinishedAt:             finishedAt.UTC(),
		MeasurementWallSeconds: wall,
		Config: ConfigSummary{
			Label: cfg.Label, URL: cfg.URL, Rate: cfg.Rate,
			DurationSeconds: cfg.Duration.Seconds(), WarmupSeconds: cfg.Warmup.Seconds(),
			TimeoutSeconds: cfg.RequestTimeout.Seconds(), MaxInFlight: cfg.MaxInFlight,
			ExpectedStatus: cfg.ExpectedStatus, BodyBytes: len(cfg.Body),
			BodySHA256: fmt.Sprintf("%x", bodyHash),
			Validation: cfg.Validation,
		},
		Environment: Environment{
			GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, GoVersion: runtime.Version(),
			CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0),
		},
		Counts:    c.counts,
		Latency:   c.latency.summary(),
		FirstByte: c.firstByte.summary(),
	}
	if wall > 0 {
		result.CompletedRPS = float64(result.Counts.Completed) / wall
		result.SuccessfulRPS = float64(result.Counts.Succeeded) / wall
	}
	if result.Counts.Completed > 0 {
		failures := result.Counts.HTTPError + result.Counts.TransportError
		result.GatewayErrorRate = float64(failures) / float64(result.Counts.Completed)
	}
	if result.Counts.Scheduled > 0 {
		result.ClientDropRate = float64(result.Counts.ClientDropped) / float64(result.Counts.Scheduled)
	}
	return result
}

func (h *latencyHistogram) observe(value time.Duration) {
	if value < 0 {
		value = 0
	}
	if h.count == 0 || value < h.min {
		h.min = value
	}
	if value > h.max {
		h.max = value
	}
	h.count++
	h.totalNS += float64(value)
	micros := float64(value) / float64(time.Microsecond)
	index := int(math.Log2(micros+1) * 64)
	if index < 0 {
		index = 0
	}
	if index >= len(h.buckets) {
		index = len(h.buckets) - 1
	}
	h.buckets[index]++
}

func (h *latencyHistogram) summary() LatencySummary {
	if h.count == 0 {
		return LatencySummary{}
	}
	return LatencySummary{
		Count:  h.count,
		MinMS:  durationMS(h.min),
		MeanMS: h.totalNS / float64(h.count) / float64(time.Millisecond),
		P50MS:  h.quantileMS(0.50),
		P95MS:  h.quantileMS(0.95),
		P99MS:  h.quantileMS(0.99),
		MaxMS:  durationMS(h.max),
	}
}

func (h *latencyHistogram) quantileMS(q float64) float64 {
	target := uint64(math.Ceil(q * float64(h.count)))
	if target == 0 {
		target = 1
	}
	var seen uint64
	for index, count := range h.buckets {
		seen += count
		if seen >= target {
			micros := math.Pow(2, float64(index)/64) - 1
			return micros / 1000
		}
	}
	return durationMS(h.max)
}

func durationMS(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}
