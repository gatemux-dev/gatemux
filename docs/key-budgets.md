# Virtual-key budgets

Implemented pilot slice, 2026-09-21. Uses the existing key-budget column and
reservation/daily-total accounting; **no migration** is needed. This completes
the control-plane creation/read/edit gap, not every cost or comparison gate.

## API and access

- `POST /admin/teams/{slug}/keys` accepts optional `usd_limit_cents`, alongside
  existing key settings. Administrators and that team's managers may issue keys.
- `POST /admin/service-accounts/{id}/keys` accepts the same field; existing
  administrator-only issuance rules remain unchanged.
- `GET /admin/keys/{id}/budget` returns `limit_cents` when set, `used_cents`,
  `period`, `window_start` and `window_end`. Admins and owning-team managers only.
  Usage includes pending reservations, settlements and earlier unreserved usage
  from the same bounded daily aggregate used by admission. Read failure is 503,
  never a fabricated zero balance. Revoked keys remain readable.
- `PATCH /admin/keys/{id}/budget` requires exactly `{"usd_limit_cents":125}`
  or `{"usd_limit_cents":null}` and returns 204. Body is capped at 4 KiB;
  unknown fields, trailing JSON and missing values are rejected. Only the budget
  column changes; ownership, allowlists, metadata, expiry and other limits remain.
  Revoked keys cannot be edited. Lookup/update/summary have 2-second DB deadlines.
- Existing `PATCH /admin/keys/{id}` remains a **full replacement** of editable
  settings. Include the budget when using it; omission clears the cap as before.
  Prefer the new budget-only route for isolated cap changes. The general editor
  sends all editable fields. Creation, general edits and budget-only changes
  record budget metadata through the existing best-effort admin audit mechanism;
  this is not a new transactional/compliance audit guarantee.

Limits are integer US cents, between 0 and 9007199254740991, or null. Negative,
fractional, string and overflow values are rejected at the API; bounds are also
validated by store methods. Null means no key-specific cap. **Zero retains its
existing meaning of no key cap**, unlike a zero customer budget. Other scope
limits are still enforced. The UI parses decimal dollars exactly without rounding.

## Enforcement and visibility

- Key budgets inherit the team's UTC calendar day/month window. Changing or
  clearing a cap does not reset usage. Lowering below used/reserved spend denies
  subsequent positive-cost admissions. Already admitted work can still settle
  against its earlier policy snapshot.
- Positive caps require known prices. Chat, embeddings and supported Responses
  use existing shared reservation/admission and durable settlement. Missing
  pricing denies before upstream IO; exhausted budgets return 403
  `insufficient_quota`. Atomic advisory-lock reservations prevent simultaneous
  requests from reserving the same remaining balance.
- Capped keys reject native/unmetered routes, including generic passthrough,
  with `key_accounting_unsupported` before upstream IO. Unsupported audio,
  hosted-charge, prediction and non-default service-tier dimensions follow the
  same pricing restrictions as customer budgets. Keys without a positive cap
  keep their previous behavior; this does not redesign team-only budget policy.
- Capped chat requests receive an explicit 1024-token output ceiling when none
  is supplied. A supplied ceiling must be 1–1000000, only one of `max_tokens` /
  `max_completion_tokens`, and `n` must be 1–128. Input estimates include the raw
  envelope; output estimates account for `n`. Token estimation and actual
  upstream usage can differ: a cap is **not an exact provider-invoice ceiling**.
- Rotation copies the cap but creates a new key ID with a separate spend total.
  Team and owner budgets remain shared. This is **not rotation-family budgeting**;
  use parent budgets when a limit must survive replacement/rotation of credentials.
- Team → Virtual keys shows the cap in Limits, supports budget-at-issue and
  general edit, and has a dedicated Budget action with live used/reserved totals.
  Failed loads show an error/retry instead of allowing edits against unknown data.

## Pause and rotation

Migration 0040 adds reversible key pause. Owning-team managers and administrators
can call `POST /admin/keys/{id}/pause` and `/resume`. Paused keys cannot authenticate
or rotate; resuming preserves identity, settings and spend history. Revoked keys
cannot be resumed. Already admitted inference is not canceled by pausing a key.

Rotation accepts optional `{"grace_seconds":3600}`, from zero through 604800
(seven days). Zero revokes the old key immediately; a positive value limits its
remaining lifetime to the earlier of its expiry and the grace deadline. A paused
source is rechecked under the rotation transaction's row lock, so a pause that
commits before that locked check cannot issue a working replacement. Rotation
does not grant a shared family budget; the new key still has its own spend ID.

## Verification and remaining work

`internal/api/key_budget_test.go` covers validation, manager/member/foreign-team
boundaries, unrelated-policy preservation, audit events, revoked keys, live cap
changes, exhaustion, pending reservations under contention, independent key
balances, parent limits, pre-cap history, unavailable reads, service-account
issuance, default output limits and unpriced/native/opaque-route denial.
The OpenAPI route contract, store bounds and exact decimal conversion have tests.
`web/tests/manager.live.cjs` exercises issue/edit/clear and role denial through
real password sessions and disposable Postgres, without API interception.
`internal/api/key_lifecycle_test.go` additionally covers pause/resume, grace
bounds and deterministic pause-during-rotation interleavings with and without
grace. A rejected rotation leaves no replacement or changes to the source key.

Broader concurrent reservations, accounting/recovery and production-workload
qualification remain open. These tests do not establish universal budget or
provider-invoice equivalence. See [alpha limitations](public-alpha.md).
