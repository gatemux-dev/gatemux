package server

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Never record log text/attributes. A closed event/cause vocabulary diagnoses
// failures without exporting prompts, credentials, IDs or database details.
type soakLogHandler struct {
	mu     sync.Mutex
	counts map[string]int
}

func (h *soakLogHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= slog.LevelWarn
}
func (h *soakLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *soakLogHandler) WithGroup(_ string) slog.Handler      { return h }
func (h *soakLogHandler) Handle(_ context.Context, r slog.Record) error {
	event := "other"
	switch r.Message {
	case "routing concurrency unavailable":
		event = "routing_concurrency"
	case "distributed concurrency unavailable":
		event = "principal_concurrency"
	case "distributed concurrency lease release failed":
		event = "principal_release"
	case "routing concurrency lease release failed":
		event = "routing_release"
	case "budget admission failed":
		event = "budget_admission"
	case "routed call failed":
		event = "routed_call"
	case "concurrency policy refresh failed":
		event = "policy_refresh"
	case "stream proxy ended with error":
		event = "stream"
	}
	cause := "other"
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "err" {
			if err, ok := a.Value.Any().(error); ok {
				cause = soakErrorKind(err)
			}
		}
		return true
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.counts == nil {
		h.counts = map[string]int{}
	}
	h.counts[event+":"+cause]++
	return nil
}

func soakErrorKind(err error) string {
	var netErr net.Error
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "network_timeout"
	case errors.As(err, &pgErr):
		switch pgErr.Code {
		case "40P01":
			return "db_deadlock"
		case "23503":
			return "db_foreign_key"
		case "23514":
			return "db_constraint"
		case "22003":
			return "db_overflow"
		case "57014":
			return "db_canceled"
		default:
			return "db_other"
		}
	case strings.Contains(err.Error(), "concurrency policy snapshot is stale"):
		return "stale_policy"
	default:
		return "other"
	}
}

func (h *soakLogHandler) snapshot() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for k, v := range h.counts {
		out[k] = v
	}
	return out
}

func TestSoakDiagnosticVocabulary(t *testing.T) {
	h := &soakLogHandler{}
	log := slog.New(h).With("credential", "must-not-export")
	log.Error("private unknown message", "err", errors.New("private provider text"))
	log.Error("routing concurrency unavailable", "err", context.DeadlineExceeded)
	got := h.snapshot()
	if len(got) != 2 || got["other:other"] != 1 || got["routing_concurrency:deadline"] != 1 {
		t.Fatalf("unsafe diagnostics: %+v", got)
	}
}
