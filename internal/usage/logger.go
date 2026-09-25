package usage

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gatemux-dev/gatemux/internal/store"
)

// Logger has no lossy in-memory queue. Each completion commits synchronously;
// durable pre-call intents recover interrupted or unavailable completion writes.
type Logger struct {
	store    *store.Store
	log      *slog.Logger
	cancel   context.CancelFunc
	done     chan struct{}
	once     sync.Once
	failures atomic.Uint64
	healthy  atomic.Uint64
	// Only the single recovery worker accesses its fixed health checkpoint.
	observed uint64
	cutoff   *time.Time
	observer Observer
}

type Observer interface {
	RecordAccounting(operation, outcome string, recovered int, duration time.Duration)
}

func NewLogger(s *store.Store, log *slog.Logger, observers ...Observer) *Logger {
	ctx, cancel := context.WithCancel(context.Background())
	l := &Logger{store: s, log: log, cancel: cancel, done: make(chan struct{})}
	if len(observers) > 0 {
		l.observer = observers[0]
	}
	go l.run(ctx)
	return l
}
func (l *Logger) Store() *store.Store { return l.store }

func (l *Logger) Ready() error {
	if l.failures.Load() != l.healthy.Load() {
		return errors.New("usage recovery pending after failed completion")
	}
	return nil
}

func (l *Logger) Record(e store.UsageEntry) (store.UsageReceipt, error) {
	started := time.Now()
	if e.Ts.IsZero() {
		e.Ts = time.Now().UTC()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	receipt, err := l.store.FinalizeUsage(ctx, e)
	if err != nil && e.AccountingID != "" {
		l.failures.Add(1)
	}
	if err != nil && l.log != nil {
		l.log.Error("usage completion unavailable", "request_id", e.RequestID, "team_id", e.TeamID, "durable_intent", e.AccountingID != "", "err", err)
	}
	l.observe("completion", 0, started, err)
	return receipt, err
}

func (l *Logger) run(ctx context.Context) {
	defer close(l.done)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			started := time.Now()
			work, cancel := context.WithTimeout(ctx, 2*time.Second)
			count, err := l.reconcile(work)
			cancel()
			l.observe("recovery", count, started, err)
			if err != nil && ctx.Err() == nil {
				l.failures.Add(1)
			}
			if err != nil && ctx.Err() == nil && l.log != nil {
				l.log.Error("usage reconciliation unavailable", "err", err)
			} else if count > 0 && l.log != nil {
				l.log.Info("interrupted usage reconciled", "count", count)
			}
		}
	}
}

func (l *Logger) observe(operation string, recovered int, started time.Time, err error) {
	if l.observer == nil {
		return
	}
	outcome := "committed"
	if err != nil {
		outcome = "failed"
	}
	l.observer.RecordAccounting(operation, outcome, recovered, time.Since(started))
}

func (l *Logger) reconcile(ctx context.Context) (int, error) {
	count, err := l.store.ReconcileAccounting(ctx)
	if err != nil {
		return count, err
	}
	epoch := l.failures.Load()
	if epoch == l.healthy.Load() {
		return count, nil
	}
	if epoch != l.observed {
		l.observed, l.cutoff = epoch, nil
	}
	cutoff, pending, err := l.store.AccountingCheckpoint(ctx, l.cutoff)
	if err != nil {
		return count, err
	}
	l.cutoff = &cutoff
	if !pending {
		// A completion failing during this query increments failures separately;
		// it cannot be erased by this acknowledgement of the older epoch.
		l.healthy.Store(epoch)
	}
	return count, nil
}
func (l *Logger) CloseContext(ctx context.Context) error {
	l.once.Do(l.cancel)
	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Logger) Close() { _ = l.CloseContext(context.Background()) }
