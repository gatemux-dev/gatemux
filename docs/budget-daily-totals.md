# Bounded budget history reads

Implemented 2026-09-16, pilot Beta. This fixes a bottleneck exposed by the first
configured-policy soak; it does not by itself qualify sustained load or establish
performance superiority over another gateway.

## Contract

Team, user, service-account, key and customer admission reads transactional UTC
daily totals. A day reads at most one matching row; a month reads at most 31.
It no longer scans every request in the billing period while holding tenant
advisory locks. Existing multi-scope admission locks, estimates and limits remain
unchanged. Model-budget reporting is outside this five-scope change.

Migration `0039_budget_daily_totals` backfills the exact previous formula:
active reservation estimates plus settled reservation costs, plus usage without
a reservation having the same request ID **and team**. Usage before budget
activation is included. Missing/unknown provider cost remains unknown; the
aggregate does not invent prices, deduplicate legacy usage or refund old holds.
Reservation dates anchor reserved/settled charges; unreserved usage uses its
original timestamp. Updating or removing a reservation restores the corresponding
unreserved usage, including multiple legacy rows and cross-day attribution.

Database row triggers maintain these totals in the same transaction as source
inserts, updates and deletes. A failed usage/settlement transaction also rolls
back its totals. Repeated journal completion cannot increment twice. Normal
reservation admission/settlement updates at most five scope/day rows in a fixed
order; settlement applies a single amount delta. A request-ID advisory lock
serializes usage/reservation correlation, including legacy writers. Matching
legacy usage lookup has a dedicated partial index. Legacy correlation changes
can touch multiple historical rows; they are not a bounded bulk-import API.

Zero totals are retained. Rows grow by scope/day, not request; no automatic
aggregate retention or source-ledger purge was added. Overflow or a negative
aggregate aborts the transaction. Do not directly edit the derived totals,
disable their triggers, or modify accounting tables through replication modes
that bypass triggers. Bulk `TRUNCATE` is rejected on both source tables and the
totals table, because it would otherwise silently break admission accounting.
Transactional source `DELETE` is supported, but erases spend evidence and may
restore budget headroom: retention requires an explicit operator policy.

## Upgrade and rollback

Back up Postgres, drain inference and run one migration owner. Migration 0039
locks both accounting source tables while creating the index, backfilling and
installing triggers atomically. Its duration depends on history size; it is not
an online, zero-downtime migration claim. Verify historical rows, pending
reservations and derived totals before resuming traffic. Invalid historical
amounts/overflow can fail migration without a partially applied schema.

Never run the down migration while this version is serving: its admission query
requires the totals table. Roll back application/schema together while drained.
Rollback removes only derived totals, triggers/functions and the correlation
index; source usage, reservations and journals are retained. Preview deployment
and production upgrade qualification must be recorded separately.

## Evidence

The first 30-minute run (`5661bb1`, `pilot-20260916-qualified-attempt-01`) failed
with 429/503 responses. A 180s diagnostic reproduced the growth. Read-only
Postgres inspection showed budget advisory-lock waits; a generic prepared query
plan used a nested-loop anti-join with one reservation probe per historical
usage row. Short preflights had not accumulated enough history to expose it.
These failures are retained and never relabeled as successful benchmarks.

`internal/usage/budget_daily_test.go` compares every derived scope/day against an
independent source-ledger aggregate. It tests a 50,000-reservation/50,001-usage
upgrade, budget enforcement after backfill, UTC month boundaries, mutations,
nullable-identity cleanup, rollback, forbidden truncation, concurrent legacy
correlation and two-pool admission contention. Existing journal idempotency,
crash-recovery and historical-budget tests also run with the triggers enabled.
The soak additionally requires zero source/aggregate mismatches before and
after disconnect/drain/restart. Workload rates and original acceptance limits
are unchanged. Corrected clean-source `a764bbd` passed the full 30-minute local
profile on 2026-09-19: 118800 load requests plus five recovery probes, zero errors,
drops or aggregate mismatches in a private local test. This is not a public
reproducible benchmark, capacity, multi-host or production-upgrade qualification.
Existing installations must follow the drained, backed-up upgrade procedure.
