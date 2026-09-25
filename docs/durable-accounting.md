# Durable request accounting (pilot Beta)

Implemented 2026-09-15. This is crash-safe gateway bookkeeping, not provider
invoice reconciliation or a claim of complete modality pricing.

## Request lifecycle

After authentication, parsing, guardrail-policy lookup and customer resolution,
inference admission commits a metadata-only `inference_journal` intent before
any provider IO. Acquiring an upstream permit also commits a started marker.
If either write fails, the provider is not called. Journal IDs are the gateway
request IDs: supply a fresh, globally unique `X-Request-Id` (1–256 bytes), or let
the gateway generate one. Reusing a journal/reservation ID returns HTTP 409
`request_id_reused`; this is duplicate prevention, not a replay/idempotency API.
A pre-admission denial sharing that correlation ID cannot settle its journal.

Completion locks the intent and reservation and commits the usage row,
reservation settlement and completed journal state in one Postgres transaction.
Duplicate completions return the original receipt and cannot change its cost.
A failed insert rolls back settlement. Usage/capture no longer take independent
insertion paths; optional captured content attaches to the committed usage ID.

There is no lossy in-memory usage queue. Completion IO has an independent 2s
deadline, even after client cancellation. A failed completion has one bounded,
request-local retry at handler exit; persistent failure leaves the intent for
recovery. Inference on that process pauses with HTTP 503 `accounting_unavailable`
and `/readyz` returns 503. The recovery worker uses a fixed DB-clock checkpoint
to resume after the relevant pending intents complete, including when the local
retry succeeded or a transient recovery failure occurred with no pending work.
Later work on another healthy replica does not keep advancing this checkpoint.

One cancelable, joined worker per process checks every 5s. Each recovery transaction
claims at most 64 expired intents with `FOR UPDATE SKIP LOCKED`, under a 2s IO
deadline. It never sends/replays provider requests. Expiry is DB time plus the
remaining request deadline and 30s cleanup grace. Live requests are not claimed
early; larger backlogs take multiple batches. Request duration is bounded to 24h
for journal admission (90s fallback when an embedded handler lacks a deadline).

Intent metadata is capped at 24 KiB serialized / 32 KiB JSONB. The worker retains
at most 64 records, one health-check timestamp and counters, not a request-sized
unbounded map. Global admission continues to bound active request writers.
The journal contains identifiers and attribution, not prompts or responses.

## Cost evidence

Usage API rows and usage CSV expose `accounting_state`. Requests displays unknown
and unpriced amounts as words, not `$0`; estimates and request-detail warnings
remain visible. Spend totals carry a caveat about incomplete and estimated costs.

| State | Meaning |
| --- | --- |
| `priced` | Reported usage priced using the configured token rates, including explicitly reported zero counters. Not an invoice guarantee. |
| `estimated` | A started request lacked complete cost evidence, so its budget reservation estimate was retained; also used for previously settled differing evidence. |
| `unknown` | Work may have started, but its cost cannot be recovered and there was no reservation estimate. Numeric zero is only a storage placeholder. |
| `unpriced` | No applicable token price, or an unsupported native/opaque modality. Numeric zero is not evidence of free work. |
| `not_billable` | No upstream attempt started, a cache hit, or a stored Responses read/delete that does not generate new tokens. |
| `legacy` | Pre-migration or direct imported history without the new evidence contract. |

Missing, partial, null or negative token counters are not authoritative zero
usage. Chat JSON/SSE, embeddings and native Responses preserve this distinction.
After interruption, a started request with a reservation settles its estimate;
one without a started marker settles zero. Recovered outcomes use synthetic
status 503 and `request_interrupted_or_unrecorded`: the client may actually have
received a successful response before the completion write was lost. Late
completion cannot overwrite an already reconciled row.

Team, user, service-account, key and customer budget admission includes priced
usage from before that scope acquired a budget, plus active/settled reservations
without double counting their usage rows. Recorded unknown/unpriced amounts
cannot reconstruct historical provider spend when a budget is enabled later.
Migration 0039 now maintains those five scopes as transactional UTC daily totals:
monthly admission reads at most 31 matching rows rather than scanning request
history. See [bounded budget reads](budget-daily-totals.md) for atomic backfill,
trigger/retention rules, rollback requirements and failure evidence.

## Operations and upgrade

Migration `0038_durable_accounting` adds the journal, nullable unique usage
accounting ID and evidence state. Existing usage is labeled `legacy` without
rewriting cost or deduplicating history. Existing reservations without a journal
are **not** reconstructed or silently refunded. Drain old replicas and inspect
such reservations against provider records before declaring a clean upgrade;
the new recovery guarantee applies to new journaled requests only. Use a single
migration owner; multi-replica migration startup qualification is separate work.
Do not run the down migration with newer replicas active. Back up Postgres first.

Useful read-only operator checks:

```sql
SELECT state, count(*) FROM inference_journal GROUP BY state;
SELECT request_id, team_id, recover_after, upstream_started
FROM inference_journal
WHERE state = 'pending' AND recover_after < NOW()
ORDER BY recover_after LIMIT 64;
SELECT b.request_id, b.team_id, b.estimated_cost_cents
FROM budget_reservations b
WHERE b.status = 'reserved'
  AND NOT EXISTS (SELECT 1 FROM inference_journal j WHERE j.request_id = b.request_id)
ORDER BY b.created_at LIMIT 64;
```

Metrics: `gatemux_accounting_operations_total{operation,outcome}`,
`gatemux_accounting_duration_seconds{operation}` and
`gatemux_accounting_recovered_total`. Labels admit only completion/recovery and
committed/failed, never request IDs, tenants, error text or credentials. Failure
logs include the correlation ID and whether a durable intent exists. Optional
callbacks and inference counters remain best-effort derivatives; use Postgres
usage/reservations as the bookkeeping source, not callback delivery or counters.

Completed journal and usage retention/purge remains operator-managed; there is
no new automatic deletion. Database loss is not protected by an in-database
journal: backup/restore and storage durability remain deployment responsibilities.
No provider-invoice import, per-attempt fallback invoice reconciliation, full
audio/image/tool billing or fractional-cent accounting is claimed. Admission
estimates are conservative for configured supported token rates, not guaranteed
upper bounds on every provider's actual charges.

The added synchronous transactions and aggregate updates have a real
database/latency cost. The old benchmark results predate these changes. Configured
policy soak, outage/restart/drain qualification and a fresh comparison are separate
release gates; do not reuse earlier throughput numbers as current evidence.

## Verification

`internal/usage/logger_test.go` uses uniquely owned disposable schemas for
concurrent duplicate completion, rollback, recovery/health checkpoints, 64-row
replica claims, deleted identity, budget history, migration preservation and a
child process exiting without cleanup followed by a replacement recovery worker.
`internal/api/accounting_test.go` verifies request-ID collision, paused/failed
admission, expired starts and permit release, JSON/SSE usage evidence and stored
Responses no-rebilling. Provider counter and fixed-label metric tests accompany
them. `web/tests/accounting.live.cjs` tests real API/UI and CSV evidence using the
isolated manager fixture; it performs no paid inference or preview writes.
