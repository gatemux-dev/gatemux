package callbacks

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cancelSink struct {
	entered chan struct{}
	calls   atomic.Int32
	flushes atomic.Int32
}

func (s *cancelSink) Name() string            { return "cancel-test" }
func (s *cancelSink) EventTypes() []EventType { return []EventType{EventRequestCompleted} }
func (s *cancelSink) Send(ctx context.Context, _ Event) error {
	if s.calls.Add(1) == 1 {
		close(s.entered)
	}
	<-ctx.Done()
	return ctx.Err()
}
func (s *cancelSink) Flush(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	s.flushes.Add(1)
	return nil
}

func awaitBus(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("callback worker did not join")
	}
}

func TestBusOneGenerationCancellationAndDropAccounting(t *testing.T) {
	b := NewBus(nil)
	s := &cancelSink{entered: make(chan struct{})}
	if err := b.Register(s); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := b.Start(ctx)
	for range 10 {
		if b.Start(ctx) != done {
			t.Fatal("multiple worker generations")
		}
	}
	if err := b.Register(s); err == nil {
		t.Fatal("registered an unstarted worker after Start")
	}
	b.Emit(Event{Type: EventRequestCompleted})
	awaitBus(t, s.entered)
	// Emit races with shutdown; each matching event must be delivered, failed,
	// queued or counted as dropped. Cancellation must not strand queued work.
	var emitters sync.WaitGroup
	for range 16 {
		emitters.Add(1)
		go func() {
			defer emitters.Done()
			for range 1000 {
				b.Emit(Event{Type: EventRequestCompleted})
			}
		}()
	}
	cancel()
	emitters.Wait()
	awaitBus(t, done)
	b.Emit(Event{Type: EventRequestCompleted})
	b.Emit(Event{Type: EventKeyCreated}) // filtered, not counted
	stat := b.Stats()[0]
	if stat.QueueLen != 0 || stat.Delivered+stat.Failed+stat.Dropped != 16002 {
		t.Fatalf("stranded or lost events: %+v", stat)
	}
	if s.calls.Load() != 1 || s.flushes.Load() != 1 {
		t.Fatalf("worker counts %d/%d", s.calls.Load(), s.flushes.Load())
	}
}

func TestBusRegistrationBound(t *testing.T) {
	b := NewBus(nil)
	for range 32 {
		if err := b.Register(&cancelSink{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Register(&cancelSink{}); err == nil {
		t.Fatal("unbounded callback population")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	awaitBus(t, b.Start(ctx))
}

func TestArchiveFlushesLastBufferedChunkOnShutdown(t *testing.T) {
	var body []byte
	var writes atomic.Int32
	sink := &S3Archive{NameValue: "archive-test", Putter: func(ctx context.Context, _ string, raw []byte) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		body = bytes.Clone(raw)
		writes.Add(1)
		return nil
	}}
	b := NewBus(nil)
	if err := b.Register(sink); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := b.Start(ctx)
	b.Emit(Event{ID: "buffered-final-event", Type: EventRequestCompleted})
	deadline := time.Now().Add(time.Second)
	for b.Stats()[0].Delivered != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if b.Stats()[0].Delivered != 1 {
		t.Fatal("event not buffered")
	}
	cancel()
	awaitBus(t, done)
	if writes.Load() != 1 {
		t.Fatalf("archive writes: %d", writes.Load())
	}
	r, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil || event.ID != "buffered-final-event" {
		t.Fatalf("incomplete archive: %v", err)
	}
	if err := sink.Flush(context.Background()); err != nil || writes.Load() != 1 {
		t.Fatalf("non-idempotent flush: %v", err)
	}
}
