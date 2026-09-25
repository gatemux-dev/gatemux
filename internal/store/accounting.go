package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrAccountingConflict = errors.New("request ID already used")

type UsageReceipt struct {
	ID, CostCents int64
	Accounting    string
}

// BeginAccounting must commit before any upstream call. The persisted deadline
// uses DB time plus remaining duration, avoiding host/DB wall-clock skew.
func (s *Store) BeginAccounting(ctx context.Context, e UsageEntry, remaining time.Duration) error {
	if e.RequestID == "" || len(e.RequestID) > 256 || remaining <= 0 || remaining > 24*time.Hour {
		return errors.New("invalid accounting request ID or deadline")
	}
	e.Ts = time.Now().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if len(b) > 24<<10 {
		return errors.New("accounting metadata exceeds capacity")
	}
	tag, err := s.Pool.Exec(ctx, `INSERT INTO inference_journal(request_id,team_id,entry,recover_after)
		SELECT $1,$2,$3,NOW()+($4::bigint * interval '1 millisecond')+interval '30 seconds'
		WHERE NOT EXISTS(SELECT 1 FROM budget_reservations WHERE request_id=$1)
		ON CONFLICT DO NOTHING`, e.RequestID, e.TeamID, b, remaining.Milliseconds())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrAccountingConflict
	}
	return nil
}

func (s *Store) MarkAccountingStarted(ctx context.Context, requestID string) error {
	tag, err := s.Pool.Exec(ctx, `UPDATE inference_journal SET upstream_started=true WHERE request_id=$1 AND state='pending' AND recover_after>NOW()`, requestID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("accounting intent missing or expired")
	}
	return nil
}

type journalRecord struct {
	requestID string
	raw       []byte
	started   bool
	usageID   *int64
	state     string
}

func (s *Store) FinalizeUsage(ctx context.Context, e UsageEntry) (UsageReceipt, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return UsageReceipt{}, err
	}
	defer tx.Rollback(ctx)
	var j journalRecord
	if e.AccountingID == "" {
		// Denials before parsed admission cannot incur provider cost. Legacy
		// callers retain independent inserts, even when a client reuses another
		// request's correlation ID. Only the admitted request owns its journal.
		e.Accounting = classifyAccounting(e)
		id, err := insertUsage(ctx, tx, e)
		if err != nil {
			return UsageReceipt{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return UsageReceipt{}, err
		}
		return UsageReceipt{id, e.CostCents, e.Accounting}, nil
	}
	err = tx.QueryRow(ctx, `SELECT request_id,entry,upstream_started,usage_id,state FROM inference_journal WHERE request_id=$1 FOR UPDATE`, e.AccountingID).Scan(&j.requestID, &j.raw, &j.started, &j.usageID, &j.state)
	if err != nil {
		return UsageReceipt{}, err
	}
	receipt, err := finalizeJournal(ctx, tx, j, e, false)
	if err != nil {
		return UsageReceipt{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return UsageReceipt{}, err
	}
	return receipt, nil
}

func classifyAccounting(e UsageEntry) string {
	if e.Accounting != "" {
		return e.Accounting
	}
	if strings.Contains(string(e.TokenDetails), `"unpriced_`) {
		return "unpriced"
	}
	if e.Cached || e.StatusCode >= 400 && e.CostCents == 0 {
		return "not_billable"
	}
	if e.CostCents > 0 {
		return "priced"
	}
	return "unknown"
}

func finalizeJournal(ctx context.Context, tx pgx.Tx, j journalRecord, e UsageEntry, recovering bool) (UsageReceipt, error) {
	var base UsageEntry
	if err := json.Unmarshal(j.raw, &base); err != nil {
		return UsageReceipt{}, err
	}
	if !recovering && e.TeamID != base.TeamID {
		return UsageReceipt{}, errors.New("accounting principal mismatch")
	}
	if j.state == "complete" {
		var receipt UsageReceipt
		if j.usageID == nil {
			return receipt, errors.New("completed accounting entry was removed")
		}
		err := tx.QueryRow(ctx, `SELECT id,cost_cents,accounting_state FROM usage_log WHERE id=$1`, *j.usageID).Scan(&receipt.ID, &receipt.CostCents, &receipt.Accounting)
		return receipt, err
	}
	if recovering {
		e = base
		// Optional identities may have been deleted since admission. Match the
		// usage table's ON DELETE SET NULL semantics; keep external attribution.
		if e.UserID != nil || e.KeyID != nil || e.ServiceAccountID != nil || e.CustomerID != nil {
			err := tx.QueryRow(ctx, `SELECT
				(SELECT id FROM users WHERE id=$1),
				(SELECT id FROM virtual_keys WHERE id=$2 AND team_id=$5),
				(SELECT id FROM service_accounts WHERE id=$3 AND team_id=$5),
				(SELECT id FROM customers WHERE id=$4 AND team_id=$5)`, e.UserID, e.KeyID, e.ServiceAccountID, e.CustomerID, e.TeamID).Scan(&e.UserID, &e.KeyID, &e.ServiceAccountID, &e.CustomerID)
			if err != nil {
				return UsageReceipt{}, err
			}
		}
		e.StatusCode = 503
		e.Error = "request_interrupted_or_unrecorded"
		if e.Accounting != "not_billable" {
			e.Accounting = "unknown"
		}
		e.LatencyMs = int(time.Since(base.Ts).Milliseconds())
	}
	e.AccountingID = j.requestID
	e.RequestID = j.requestID
	e.Ts = base.Ts
	e.Accounting = classifyAccounting(e)
	if !j.started {
		e.CostCents = 0
		e.Accounting = "not_billable"
	}
	// A reservation is independent durable evidence of possible spending. Never
	// silently refund it when authoritative usage cannot be recovered.
	var estimated, settled int64
	var status string
	err := tx.QueryRow(ctx, `SELECT estimated_cost_cents,settled_cost_cents,status FROM budget_reservations WHERE request_id=$1 FOR UPDATE`, j.requestID).Scan(&estimated, &settled, &status)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UsageReceipt{}, err
	}
	if err == nil {
		if status == "settled" {
			if e.CostCents != settled || e.Accounting == "unknown" {
				e.Accounting = "estimated"
			}
			e.CostCents = settled
		} else {
			if j.started && (e.Accounting == "unknown" || e.Accounting == "estimated" || e.Accounting == "unpriced") {
				e.CostCents = estimated
				e.Accounting = "estimated"
			}
			if _, err = tx.Exec(ctx, `UPDATE budget_reservations SET settled_cost_cents=$2,status='settled',settled_at=NOW() WHERE request_id=$1 AND status='reserved'`, j.requestID, e.CostCents); err != nil {
				return UsageReceipt{}, err
			}
		}
	}
	if e.CostCents < 0 {
		return UsageReceipt{}, errors.New("negative accounting cost")
	}
	id, err := insertUsage(ctx, tx, e)
	if err != nil {
		return UsageReceipt{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE inference_journal SET state='complete',usage_id=$2,completed_at=NOW() WHERE request_id=$1`, j.requestID, id); err != nil {
		return UsageReceipt{}, err
	}
	return UsageReceipt{id, e.CostCents, e.Accounting}, nil
}

// ReconcileAccounting is replica-safe and bounded to 64 intents per transaction.
// The persisted request deadline plus cleanup grace protects live streams.
func (s *Store) ReconcileAccounting(ctx context.Context) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT request_id,entry,upstream_started,usage_id,state FROM inference_journal WHERE state='pending' AND recover_after<NOW() ORDER BY recover_after,request_id LIMIT 64 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return 0, err
	}
	batch := make([]journalRecord, 0, 64)
	for rows.Next() {
		var j journalRecord
		if err = rows.Scan(&j.requestID, &j.raw, &j.started, &j.usageID, &j.state); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, j := range batch {
		if _, err = finalizeJournal(ctx, tx, j, UsageEntry{}, true); err != nil {
			return 0, fmt.Errorf("reconcile accounting: %w", err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// AccountingCheckpoint checks a fixed DB-clock watermark after a local write
// failure. Later requests on healthy replicas cannot perpetually prevent this
// replica from resuming. The worker retains only this timestamp, not an
// unbounded list of failed request IDs.
func (s *Store) AccountingCheckpoint(ctx context.Context, before *time.Time) (time.Time, bool, error) {
	var cutoff time.Time
	var pending bool
	err := s.Pool.QueryRow(ctx, `WITH checkpoint AS (SELECT COALESCE($1::timestamptz,NOW()) AS cutoff)
		SELECT cutoff,EXISTS(SELECT 1 FROM inference_journal WHERE state='pending' AND created_at<=cutoff) FROM checkpoint`, before).Scan(&cutoff, &pending)
	return cutoff, pending, err
}
