package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync/atomic"
	"time"
)

type mockServer struct {
	delay          time.Duration
	jitter         time.Duration
	streamChunks   int
	streamInterval time.Duration
	failEvery      uint64
	requests       atomic.Uint64
	responseIDs    atomic.Uint64 // never reset: providers issue unique response IDs
	inFlight       atomic.Int64
	peakInFlight   atomic.Int64
}

func main() {
	addr := flag.String("addr", ":18080", "listen address")
	delay := flag.Duration("delay", 25*time.Millisecond, "base response delay")
	jitter := flag.Duration("jitter", 0, "deterministic extra delay range")
	streamChunks := flag.Int("stream-chunks", 20, "SSE content chunks per stream")
	streamInterval := flag.Duration("stream-interval", 25*time.Millisecond, "delay between SSE chunks")
	failEvery := flag.Uint64("fail-every", 0, "return 503 for every Nth request; 0 disables")
	flag.Parse()

	s := &mockServer{
		delay: *delay, jitter: *jitter, streamChunks: *streamChunks,
		streamInterval: *streamInterval, failEvery: *failEvery,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/stats", s.stats)
	mux.HandleFunc("/reset", s.reset)
	mux.HandleFunc("/v1/chat/completions", s.chat)
	mux.HandleFunc("/v1/embeddings", s.embeddings)

	server := &http.Server{
		Addr: *addr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("mock OpenAI upstream listening on %s", *addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func (s *mockServer) begin() (uint64, func()) {
	sequence := s.requests.Add(1)
	active := s.inFlight.Add(1)
	for old := s.peakInFlight.Load(); active > old && !s.peakInFlight.CompareAndSwap(old, active); old = s.peakInFlight.Load() {
	}
	return sequence, func() { s.inFlight.Add(-1) }
}

func (s *mockServer) pause(ctx context.Context, sequence uint64) error {
	delay := s.delay
	if s.jitter > 0 {
		delay += time.Duration((sequence * 7919) % uint64(s.jitter+1))
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *mockServer) shouldFail(sequence uint64) bool {
	return s.failEvery > 0 && sequence%s.failEvery == 0
}

func (s *mockServer) chat(w http.ResponseWriter, r *http.Request) {
	sequence, done := s.begin()
	responseID := s.responseIDs.Add(1)
	defer done()
	var request struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.pause(r.Context(), sequence); err != nil {
		return
	}
	if s.shouldFail(sequence) {
		http.Error(w, "injected upstream failure", http.StatusServiceUnavailable)
		return
	}
	if request.Stream {
		s.streamChat(w, r, request.Model, responseID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": fmt.Sprintf("chatcmpl-bench-%d", responseID), "object": "chat.completion",
		"created": time.Now().Unix(), "model": request.Model,
		"choices": []map[string]any{{
			"index": 0, "message": map[string]any{"role": "assistant", "content": "ok"},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": 1, "total_tokens": 9},
	})
}

func (s *mockServer) streamChat(w http.ResponseWriter, r *http.Request, model string, sequence uint64) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for i := 0; i < s.streamChunks; i++ {
		chunk, _ := json.Marshal(map[string]any{
			"id": fmt.Sprintf("chatcmpl-bench-%d", sequence), "object": "chat.completion.chunk",
			"model": model, "choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": "x"}}},
		})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
		if flusher != nil {
			flusher.Flush()
		}
		if i+1 < s.streamChunks && s.streamInterval > 0 {
			timer := time.NewTimer(s.streamInterval)
			select {
			case <-timer.C:
			case <-r.Context().Done():
				timer.Stop()
				return
			}
			timer.Stop()
		}
	}
	usage, _ := json.Marshal(map[string]any{
		"id": fmt.Sprintf("chatcmpl-bench-%d", sequence), "object": "chat.completion.chunk",
		"model": model, "choices": []any{},
		"usage": map[string]any{"prompt_tokens": 8, "completion_tokens": s.streamChunks, "total_tokens": 8 + s.streamChunks},
	})
	_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", usage)
	if flusher != nil {
		flusher.Flush()
	}
}

func (s *mockServer) embeddings(w http.ResponseWriter, r *http.Request) {
	sequence, done := s.begin()
	defer done()
	var request struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&request); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.pause(r.Context(), sequence); err != nil {
		return
	}
	if s.shouldFail(sequence) {
		http.Error(w, "injected upstream failure", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list", "model": request.Model,
		"data":  []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
		"usage": map[string]any{"prompt_tokens": 8, "total_tokens": 8},
	})
}

func (s *mockServer) stats(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"requests": s.requests.Load(), "in_flight": s.inFlight.Load(),
		"peak_in_flight": s.peakInFlight.Load(),
	})
}

func (s *mockServer) reset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if s.inFlight.Load() != 0 {
		http.Error(w, "requests still in flight", http.StatusConflict)
		return
	}
	s.requests.Store(0)
	s.peakInFlight.Store(0)
	w.WriteHeader(http.StatusNoContent)
}
