package callbacks

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Webhook is a generic JSON POST sink. Operators give us a URL and we
// deliver events one-by-one. Failures bubble up to the bus so the
// circuit breaker takes over.
type Webhook struct {
	NameValue  string
	URL        string
	Headers    map[string]string
	Subscribed []EventType
	Client     *http.Client
}

func (w *Webhook) Name() string            { return w.NameValue }
func (w *Webhook) EventTypes() []EventType { return w.Subscribed }

func (w *Webhook) Send(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range w.Headers {
		req.Header.Set(k, v)
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("webhook status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// Langfuse delivers request.completed events as Langfuse traces. The
// trace name is the alias; one observation per request captures prompt
// + completion + tokens + cost. Authentication uses Langfuse's basic
// auth (public key + secret key).
type Langfuse struct {
	NameValue  string
	Host       string // e.g. https://cloud.langfuse.com
	PublicKey  string
	SecretKey  string
	Subscribed []EventType
	Client     *http.Client
}

func (l *Langfuse) Name() string            { return l.NameValue }
func (l *Langfuse) EventTypes() []EventType { return l.Subscribed }

type langfuseIngestionEvent struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp string         `json:"timestamp"`
	Body      map[string]any `json:"body"`
}

type langfuseIngestionPayload struct {
	Batch    []langfuseIngestionEvent `json:"batch"`
	Metadata map[string]any           `json:"metadata,omitempty"`
}

func (l *Langfuse) Send(ctx context.Context, e Event) error {
	host := l.Host
	if host == "" {
		host = "https://cloud.langfuse.com"
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	traceID := e.RequestID
	if traceID == "" {
		traceID = e.ID
	}

	// One trace + one generation observation. Langfuse infers cost from
	// the model + token counts when usage is supplied; we send our
	// computed cost as a metadata field for cross-reference.
	body := langfuseIngestionPayload{
		Batch: []langfuseIngestionEvent{
			{
				ID:        e.ID + "-trace",
				Type:      "trace-create",
				Timestamp: now,
				Body: map[string]any{
					"id":        traceID,
					"name":      e.Alias,
					"userId":    e.KeyPrefix,
					"sessionId": e.TeamSlug,
					"metadata": map[string]any{
						"team_slug":  e.TeamSlug,
						"deployment": e.Deployment,
						"cached":     e.Cached,
						"cost_cents": e.CostCents,
						"latency_ms": e.LatencyMs,
					},
					"tags": []string{"gatemux"},
				},
			},
			{
				ID:        e.ID + "-gen",
				Type:      "generation-create",
				Timestamp: now,
				Body: map[string]any{
					"id":                  e.ID + "-gen",
					"traceId":             traceID,
					"name":                e.Alias,
					"model":               e.ModelUsed,
					"startTime":           now,
					"endTime":             now,
					"completionStartTime": now,
					"usage": map[string]any{
						"input":  e.Tokens.Prompt,
						"output": e.Tokens.Completion,
						"total":  e.Tokens.Total,
						"unit":   "TOKENS",
					},
					"level":         levelFor(e),
					"statusMessage": e.Error,
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", host+"/api/public/ingestion", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(l.PublicKey, l.SecretKey)
	client := l.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("langfuse status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func levelFor(e Event) string {
	if e.StatusCode >= 500 {
		return "ERROR"
	}
	if e.StatusCode >= 400 {
		return "WARNING"
	}
	return "DEFAULT"
}

// Slack reuses the webhook delivery but renders human-readable blocks for
// alert-shaped events. Other event types fall back to a JSON code block.
type Slack struct {
	NameValue  string
	URL        string
	Subscribed []EventType
	Client     *http.Client
}

func (s *Slack) Name() string            { return s.NameValue }
func (s *Slack) EventTypes() []EventType { return s.Subscribed }

func (s *Slack) Send(ctx context.Context, e Event) error {
	payload := map[string]any{"text": s.format(e)}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("slack webhook status %d", resp.StatusCode)
	}
	return nil
}

func (s *Slack) format(e Event) string {
	switch e.Type {
	case EventBudgetThreshold:
		pct := ""
		if e.Payload != nil {
			if v, ok := e.Payload["threshold_pct"]; ok {
				pct = fmt.Sprint(v)
			}
		}
		return fmt.Sprintf(":warning: Team *%s* has used %s%% of its budget", e.TeamSlug, pct)
	case EventBudgetExceeded:
		return fmt.Sprintf(":no_entry: Team *%s* exhausted its budget — requests are now denied", e.TeamSlug)
	case EventCircuitOpened:
		return fmt.Sprintf(":electric_plug: Provider circuit opened for *%s*: %s", e.Deployment, e.Error)
	case EventCircuitClosed:
		return fmt.Sprintf(":white_check_mark: Provider circuit recovered for *%s*", e.Deployment)
	case EventGuardrailBlocked:
		return fmt.Sprintf(":shield: Guardrail blocked request on *%s* (alias %s)", e.TeamSlug, e.Alias)
	case EventKeyRevoked:
		return fmt.Sprintf(":key: Key revoked: *%s* on team %s", e.KeyPrefix, e.TeamSlug)
	}
	body, _ := json.MarshalIndent(e, "", "  ")
	return fmt.Sprintf("```%s```", string(body))
}

// S3Archive writes one NDJSON file per UTC hour, gzip-compressed. Doc
// 0003 lists S3 and GCS as separate sinks; this implementation handles
// both via a generic writer interface — the real S3 wiring lives in the
// caller (left as a stub here that writes to the local filesystem so
// tests work without AWS creds).
//
// Configure with Putter set to an actual S3 / GCS uploader. The default
// fallback writes to LocalDir which is fine for development.
type S3Archive struct {
	NameValue  string
	LocalDir   string
	Putter     func(ctx context.Context, key string, body []byte) error
	Subscribed []EventType

	mu     sync.Mutex
	bucket *bytes.Buffer
	gz     *gzip.Writer
	hour   string
}

func (s *S3Archive) Name() string            { return s.NameValue }
func (s *S3Archive) EventTypes() []EventType { return s.Subscribed }

func (s *S3Archive) Send(ctx context.Context, e Event) error {
	hour := e.Ts.UTC().Format("2006/01/02/15")
	s.mu.Lock()
	if s.hour != hour && s.bucket != nil {
		// Hour rolled over — flush before appending the new event.
		if err := s.flushLocked(ctx); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	if s.bucket == nil {
		s.bucket = &bytes.Buffer{}
		s.gz = gzip.NewWriter(s.bucket)
		s.hour = hour
	}
	body, err := json.Marshal(e)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	body = append(body, '\n')
	if _, err := s.gz.Write(body); err != nil {
		s.mu.Unlock()
		return err
	}
	// Flush every 256KB so a crash doesn't lose more than ~that.
	shouldFlush := s.bucket.Len() > 256*1024
	s.mu.Unlock()
	if shouldFlush {
		s.mu.Lock()
		err := s.flushLocked(ctx)
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *S3Archive) flushLocked(ctx context.Context) error {
	if s.bucket == nil || s.bucket.Len() == 0 {
		return nil
	}
	if err := s.gz.Close(); err != nil {
		return err
	}
	body := s.bucket.Bytes()
	key := fmt.Sprintf("events/%s/gatemux-%d.ndjson.gz", s.hour, time.Now().UnixNano())
	s.bucket = nil
	s.gz = nil
	if s.Putter != nil {
		return s.Putter(ctx, key, body)
	}
	if s.LocalDir == "" {
		return errors.New("s3archive: no Putter and no LocalDir configured")
	}
	full := filepath.Join(s.LocalDir, key)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, body, 0o644)
}

func (s *S3Archive) Flush(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked(ctx)
}
